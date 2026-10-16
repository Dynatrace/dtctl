package session

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	// KeyringService is the service name used for keyring storage
	KeyringService = "dtctl"

	// EnvDisableKeyring can be set to disable keyring integration
	EnvDisableKeyring = "DTCTL_DISABLE_KEYRING"

	// EnvTokenStorage controls the OAuth token storage backend.
	// Set to "file" to use file-based storage instead of the OS keyring.
	// This is useful for headless Linux, WSL, CI/CD, and container environments
	// where a system keyring is not available.
	//
	// Valid values: "keyring" (default), "file"
	EnvTokenStorage = "DTCTL_TOKEN_STORAGE"

	// ErrMsgCollectionUnlock is the error substring returned by the Secret Service
	// backend when a persistent keyring collection does not exist or cannot be
	// unlocked. Centralised here so callers match on a single constant instead
	// of a fragile raw string.
	ErrMsgCollectionUnlock = "failed to unlock correct collection"
)

// keyringBackend abstracts secure credential storage so callers can be tested
// without a live OS keyring.
type keyringBackend interface {
	Available() bool
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
}

// osKeyring is the production implementation backed by the OS keyring.
type osKeyring struct{ store *TokenStore }

func newOSKeyring() *osKeyring                       { return &osKeyring{store: NewTokenStore()} }
func (k *osKeyring) Available() bool                 { return IsKeyringAvailable() }
func (k *osKeyring) Get(name string) (string, error) { return k.store.GetToken(name) }
func (k *osKeyring) Set(name, value string) error    { return k.store.SetToken(name, value) }
func (k *osKeyring) Delete(name string) error        { return k.store.DeleteToken(name) }

// TokenStore provides secure token storage using the OS keyring
type TokenStore struct {
	// fallbackToFile indicates whether to fall back to file-based storage
	// when keyring is unavailable
	fallbackToFile bool
}

// NewTokenStore creates a new token store
func NewTokenStore() *TokenStore {
	return &TokenStore{
		fallbackToFile: true,
	}
}

// isKeyringDisabled reports whether the keyring has been intentionally
// disabled via the DTCTL_DISABLE_KEYRING environment variable.
func isKeyringDisabled() bool {
	return os.Getenv(EnvDisableKeyring) != ""
}

// CheckKeyring probes the OS keyring and returns nil if it is usable,
// or a descriptive error explaining why it is not.
func CheckKeyring() error {
	if isKeyringDisabled() {
		return fmt.Errorf("keyring disabled via %s environment variable", EnvDisableKeyring)
	}

	_, err := keyring.Get(KeyringService, "__test__")
	if err == nil || err == keyring.ErrNotFound {
		return nil // keyring is reachable
	}
	return fmt.Errorf("keyring probe failed: %w", err)
}

// IsKeyringAvailable checks if keyring storage is available on this system
func IsKeyringAvailable() bool {
	return CheckKeyring() == nil
}

// SetToken stores a token securely in the OS keyring
func (ts *TokenStore) SetToken(name, token string) error {
	if !IsKeyringAvailable() {
		if ts.fallbackToFile {
			return nil // Will be handled by file-based storage
		}
		return fmt.Errorf("keyring not available and fallback disabled")
	}

	err := keyring.Set(KeyringService, name, token)
	if err != nil {
		return fmt.Errorf("failed to store token in keyring: %w", err)
	}
	return nil
}

// GetToken retrieves a token from the OS keyring
func (ts *TokenStore) GetToken(name string) (string, error) {
	if !IsKeyringAvailable() {
		return "", fmt.Errorf("keyring not available")
	}

	token, err := keyring.Get(KeyringService, name)
	if err == keyring.ErrNotFound {
		return "", fmt.Errorf("token %q not found in keyring", name)
	}
	if err != nil {
		return "", fmt.Errorf("failed to retrieve token from keyring: %w", err)
	}
	return token, nil
}

// DeleteToken removes a token from the OS keyring
func (ts *TokenStore) DeleteToken(name string) error {
	if !IsKeyringAvailable() {
		return nil // Nothing to delete
	}

	err := keyring.Delete(KeyringService, name)
	if err == keyring.ErrNotFound {
		return nil // Already deleted
	}
	if err != nil {
		return fmt.Errorf("failed to delete token from keyring: %w", err)
	}
	return nil
}

// MigrateTokensToKeyring migrates tokens from config file to keyring
// Returns the number of tokens migrated and any error
func MigrateTokensToKeyring(cfg *Config) (int, error) {
	if !IsKeyringAvailable() {
		return 0, fmt.Errorf("keyring not available")
	}

	ts := NewTokenStore()
	migrated := 0

	for i, nt := range cfg.Tokens {
		if nt.Token == "" {
			continue // Already migrated or empty
		}

		// Store in keyring
		if err := ts.SetToken(nt.Name, nt.Token); err != nil {
			return migrated, fmt.Errorf("failed to migrate token %q: %w", nt.Name, err)
		}

		// Clear from config (mark as migrated)
		cfg.Tokens[i].Token = ""
		migrated++
	}

	return migrated, nil
}

// GetTokenWithFallback tries to get a token from keyring first, then falls back to config
func GetTokenWithFallback(cfg *Config, tokenRef string) (string, error) {
	// Try keyring first
	if IsKeyringAvailable() {
		ts := NewTokenStore()
		token, err := ts.GetToken(tokenRef)
		if err == nil && token != "" {
			return token, nil
		}
	}

	// Fall back to config file
	return cfg.GetToken(tokenRef)
}

// KeyringBackend returns a string describing the keyring backend in use
func KeyringBackend() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS Keychain"
	case "linux":
		return "Secret Service (libsecret)"
	case "windows":
		return "Windows Credential Manager"
	default:
		return "OS Keyring"
	}
}

// IsFileTokenStorage reports whether the user has explicitly opted into
// file-based OAuth token storage, either per shell via DTCTL_TOKEN_STORAGE=file
// or once per machine by consenting to it (see PersistFileTokenStorage).
//
// The environment variable always wins: any other non-empty value, such as
// "keyring", overrides a persisted consent.
//
// A persisted consent answers "what should I do on a machine with no keyring?",
// so it applies only while the keyring is definitively absent (see
// IsKeyringAbsent). If the keyring is later installed, it is used again; if it
// is merely locked or broken, the failure surfaces instead of tokens quietly
// landing in plaintext. Consent is also ignored while the keyring is disabled
// (DTCTL_DISABLE_KEYRING), which is how embedded sessions keep away from host
// credential stores.
func IsFileTokenStorage() bool {
	if v := os.Getenv(EnvTokenStorage); v != "" {
		return strings.EqualFold(v, "file")
	}
	if isKeyringDisabled() || !hasFileStorageConsent() {
		return false
	}
	return IsKeyringAbsent(checkKeyring())
}

// checkKeyring is CheckKeyring behind a seam so tests can model a keyring that
// is absent, locked or present without a real D-Bus.
var checkKeyring = CheckKeyring

// fileStorageConsentPath is the marker recording that the user agreed to keep
// OAuth tokens in files on this machine. It lives beside the token files
// because it is a fact about that store, not a user preference.
func fileStorageConsentPath() string {
	return filepath.Join(DataDir(), "token-storage")
}

func hasFileStorageConsent() bool {
	data, err := os.ReadFile(fileStorageConsentPath())
	return err == nil && strings.EqualFold(strings.TrimSpace(string(data)), "file")
}

// PersistFileTokenStorage records the user's consent to file-based OAuth token
// storage so later invocations use it without DTCTL_TOKEN_STORAGE=file. Callers
// must only invoke it after an explicit user decision.
func PersistFileTokenStorage() error {
	path := fileStorageConsentPath()
	if err := os.MkdirAll(filepath.Dir(path), oauthTokenDirMode); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("file\n"), oauthTokenFileMode); err != nil {
		return fmt.Errorf("failed to record token storage choice: %w", err)
	}
	// WriteFile applies the mode only when it creates the file; tighten a marker
	// that already existed with looser permissions.
	if err := os.Chmod(path, oauthTokenFileMode); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("failed to restrict %s: %w", path, err)
	}
	return nil
}

// FileTokenStorageConsentPath returns the marker PersistFileTokenStorage writes,
// for messages that tell the user how to undo the choice.
func FileTokenStorageConsentPath() string { return fileStorageConsentPath() }

// keyringAbsentMarkers are substrings of the errors the Secret Service backend
// returns when no provider exists at all: nothing owns org.freedesktop.secrets,
// or there is no D-Bus session to ask. They are deliberately narrow; a keyring
// that exists but is locked, slow or misbehaving must stay an error rather than
// look like a machine that simply has none.
var keyringAbsentMarkers = []string{
	"was not provided by any .service files",
	"org.freedesktop.dbus.error.serviceunknown",
	"couldn't determine address of session bus",
	"dbus-launch",
}

// IsKeyringAbsent reports whether a CheckKeyring error means the machine has no
// keyring provider (typical for headless Linux, containers and fresh VMs), as
// opposed to a keyring that is present but unusable.
func IsKeyringAbsent(err error) bool {
	if err == nil || isKeyringDisabledErr(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, strings.ToLower(ErrMsgCollectionUnlock)) {
		return false
	}
	for _, m := range keyringAbsentMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// isKeyringDisabledErr reports the CheckKeyring error for an intentionally
// disabled keyring, which is a choice rather than an absence.
func isKeyringDisabledErr(err error) bool {
	return strings.Contains(err.Error(), EnvDisableKeyring)
}

// IsOAuthStorageAvailable reports whether OAuth tokens can be stored
// and retrieved — either via the OS keyring or file-based storage.
func IsOAuthStorageAvailable() bool {
	return IsKeyringAvailable() || IsFileTokenStorage()
}

// TokenStorage names the store an OAuth token actually lives in.
//
// That is a different question from "is the keyring reachable?". CheckKeyring
// only probes reads, and a keyring can answer reads while refusing writes —
// macOS rejects keychain writes from unsigned binaries with `security` exit
// status 44 — in which case saveToken falls back to the file store while every
// keyring probe still succeeds. Diagnostics that want to say where a token is
// must ask where it is, not whether the keyring answers.
type TokenStorage string

const (
	// TokenStorageKeyring means the token is held by the OS keyring.
	TokenStorageKeyring TokenStorage = "keyring"
	// TokenStorageFile means the token is held by the file store
	// (see OAuthFileStore), whether by choice (DTCTL_TOKEN_STORAGE=file) or
	// because the keyring refused the write.
	TokenStorageFile TokenStorage = "file"
)

// Label returns a human-readable description of the store, in the same form
// OAuthStorageBackend uses.
func (s TokenStorage) Label() string {
	if s == TokenStorageFile {
		return fileStorageLabel()
	}
	return KeyringBackend()
}

func fileStorageLabel() string {
	return fmt.Sprintf("file (%s)", oauthTokensDir())
}

// OAuthStorageBackend returns a human-readable label describing
// where OAuth tokens are (or will be) stored.
//
// This is the configured preference, derived from the environment and the
// keyring probe alone; it deliberately keeps no memory of earlier saves, so it
// cannot carry one invocation's outcome into another in an embedded process.
// Because the probe only tests reads, a keyring that refuses writes is still
// reported here. To learn where a token actually went, use the store returned
// by TokenManager.SaveTokenWithStorage or TokenManager.GetTokenInfoWithStorage.
func OAuthStorageBackend() string {
	return oauthStorageBackend(IsKeyringAvailable)
}

// oauthStorageBackend is OAuthStorageBackend with the keyring probe injected,
// so tests can model a keyring that answers reads but refuses writes.
func oauthStorageBackend(keyringAvailable func() bool) string {
	if IsFileTokenStorage() {
		return fileStorageLabel()
	}
	if keyringAvailable() {
		return KeyringBackend()
	}
	// Fallback: file storage is used implicitly when keyring is unavailable
	// and the token manager falls back to file.
	return fileStorageLabel()
}
