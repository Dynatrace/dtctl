package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// runDoctorWithOAuthStorage runs the doctor checks with the keyring probe
// reporting the keyring as reachable (the read-only probe on a system that
// refuses keyring writes still succeeds) and the current token's OAuth session
// reporting it was found in storage. It returns the results by row name.
func runDoctorWithOAuthStorage(t *testing.T, storage auth.TokenStorage) map[string]checkResult {
	t.Helper()

	exp := time.Now().Add(30 * time.Minute)
	withStubbedSessionStatus(t, &SessionStatus{
		IsOAuth:              true,
		Storage:              storage.Label(),
		storage:              storage,
		AccessTokenPresent:   true,
		AccessTokenExpiresAt: &exp,
		RefreshTokenPresent:  true,
	})

	origProbe := checkKeyringFunc
	checkKeyringFunc = func() error { return nil }
	t.Cleanup(func() { checkKeyringFunc = origProbe })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	configPath := filepath.Join(t.TempDir(), "config")
	originalCfgFile := gFlags.cfgFile
	t.Cleanup(func() { gFlags.cfgFile = originalCfgFile })
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", server.URL, "test-oauth")
	if err := cfg.SetToken("test-oauth", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	rows := make(map[string]checkResult)
	for _, r := range runDoctorChecks(context.Background()) {
		rows[r.Name] = r
	}
	return rows
}

// TestDoctor_TokenStorage_KeyringReachableButTokenInFile is #393: the keyring
// answers reads but refused the write, so the OAuth token went to the file
// store. Doctor must stop naming the keyring as the backend.
func TestDoctor_TokenStorage_KeyringReachableButTokenInFile(t *testing.T) {
	t.Setenv(config.EnvTokenStorage, "")

	rows := runDoctorWithOAuthStorage(t, auth.TokenStorageFile)

	storage, ok := rows["Token storage"]
	if !ok {
		t.Fatalf("expected a 'Token storage' row, got %+v", rows)
	}
	if storage.Status != "warn" {
		t.Errorf("Token storage status = %q, want warn (detail: %s)", storage.Status, storage.Detail)
	}
	for _, want := range []string{"is reachable", auth.TokenStorageFile.Label(), config.EnvTokenStorage + "=file"} {
		if !strings.Contains(storage.Detail, want) {
			t.Errorf("Token storage detail = %q, want it to mention %q", storage.Detail, want)
		}
	}

	token := rows["Token"]
	if !strings.Contains(token.Detail, "file store") || strings.Contains(token.Detail, "keyring (") {
		t.Errorf("Token detail = %q, want it to name the file store, not the keyring", token.Detail)
	}
}

func TestDoctor_TokenStorage_TokenInKeyring(t *testing.T) {
	t.Setenv(config.EnvTokenStorage, "")

	rows := runDoctorWithOAuthStorage(t, auth.TokenStorageKeyring)

	storage := rows["Token storage"]
	if storage.Status != "ok" || storage.Detail != config.KeyringBackend() {
		t.Errorf("Token storage = %s %q, want ok %q", storage.Status, storage.Detail, config.KeyringBackend())
	}
	token := rows["Token"]
	if !strings.Contains(token.Detail, "keyring ("+config.KeyringBackend()+")") {
		t.Errorf("Token detail = %q, want it to name the keyring", token.Detail)
	}
}

// With DTCTL_TOKEN_STORAGE=file the file store is the chosen backend, so a
// reachable keyring is neither the backend nor a reason to warn.
func TestDoctor_TokenStorage_ExplicitFileWithReachableKeyring(t *testing.T) {
	t.Setenv(config.EnvTokenStorage, "file")

	rows := runDoctorWithOAuthStorage(t, auth.TokenStorageFile)

	storage := rows["Token storage"]
	if storage.Status != "ok" {
		t.Errorf("Token storage status = %q, want ok (detail: %s)", storage.Status, storage.Detail)
	}
	if !strings.Contains(storage.Detail, "file-based") {
		t.Errorf("Token storage detail = %q, want it to say file-based", storage.Detail)
	}
	if strings.Contains(storage.Detail, "keyring unavailable") {
		t.Errorf("Token storage detail = %q, must not claim the keyring is unavailable when the probe succeeded", storage.Detail)
	}
}

// TestBuildSessionStatus_ReportsStoreTheTokenWasFoundIn checks that auth
// status takes its Storage label from where the token was read, using the
// real file store.
func TestBuildSessionStatus_ReportsStoreTheTokenWasFoundIn(t *testing.T) {
	t.Setenv(config.EnvTokenStorage, "file")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	const (
		envURL    = "https://abc12345.apps.dynatrace.com"
		tokenName = "storage-ctx-oauth"
	)
	tm, err := auth.NewTokenManager(auth.OAuthConfigFromEnvironmentURL(envURL))
	if err != nil {
		t.Fatalf("new token manager: %v", err)
	}
	if err := tm.SaveToken(tokenName, &auth.TokenSet{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("save token: %v", err)
	}

	status, err := buildSessionStatus("storage-ctx", &config.Context{Environment: envURL}, tokenName)
	if err != nil {
		t.Fatalf("buildSessionStatus: %v", err)
	}
	if !status.IsOAuth {
		t.Fatal("expected an OAuth session")
	}
	if status.storage != auth.TokenStorageFile {
		t.Errorf("storage = %q, want %q", status.storage, auth.TokenStorageFile)
	}
	if status.Storage != auth.TokenStorageFile.Label() {
		t.Errorf("Storage = %q, want %q", status.Storage, auth.TokenStorageFile.Label())
	}
}
