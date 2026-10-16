package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const envRefSecret = "dt0s16.NOT-A-REAL-TOKEN.VALUE"

// envRefConfig is a config whose secret, environment URL, an omitempty field
// and a numeric field are all environment-variable references, next to a hook
// that carries shell positionals the loader must leave alone.
const envRefConfig = `apiVersion: v1
kind: Config
current-context: templated
contexts:
    - name: templated
      context:
        environment: ${DT_TEST_ENV_URL}
        token-ref: templated-token
        description: ${DT_TEST_DESCRIPTION}
        query-limits:
            max-result-records: ${DT_TEST_MAX_RECORDS}
    - name: plain
      context:
        environment: https://def67890.apps.dynatrace.com
        token-ref: plain-token
tokens:
    - name: templated-token
      token: ${DT_TEST_SECRET}
    - name: plain-token
      token: plain-value
preferences:
    hooks:
        pre-apply: bash validate.sh "$1" "$2"
`

// loadEnvRefConfig writes envRefConfig to a temp file and loads it with
// expansion, the way every config-management command does.
func loadEnvRefConfig(t *testing.T) (*Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(envRefConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	return cfg, path
}

func saveAndRead(t *testing.T, cfg *Config, path string) string {
	t.Helper()
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestSaveToPreservesEnvRefs is the regression test for #697: a load-modify-
// save cycle wrote every resolved ${VAR} back into the file, putting the secret
// on disk in plaintext, or "" when the variable was unset.
func TestSaveToPreservesEnvRefs(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
	}{
		{"variables set", true},
		{"variables unset", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("DT_TEST_ENV_URL", "https://abc12345.apps.dynatrace.com")
				t.Setenv("DT_TEST_SECRET", envRefSecret)
				t.Setenv("DT_TEST_DESCRIPTION", "resolved description")
				t.Setenv("DT_TEST_MAX_RECORDS", "500")
			} else {
				for _, v := range []string{"DT_TEST_ENV_URL", "DT_TEST_SECRET", "DT_TEST_DESCRIPTION", "DT_TEST_MAX_RECORDS"} {
					t.Setenv(v, "")
					os.Unsetenv(v)
				}
			}

			cfg, path := loadEnvRefConfig(t)
			if tc.set && cfg.Tokens[0].Token != envRefSecret {
				t.Fatalf("loaded token = %q, want it resolved", cfg.Tokens[0].Token)
			}
			cfg.CurrentContext = "plain" // what use-context does

			got := saveAndRead(t, cfg, path)
			if strings.Contains(got, envRefSecret) {
				t.Errorf("resolved secret written to the config file:\n%s", got)
			}
			for _, ref := range []string{
				"token: ${DT_TEST_SECRET}",
				"environment: ${DT_TEST_ENV_URL}",
				"description: ${DT_TEST_DESCRIPTION}",
				"max-result-records: ${DT_TEST_MAX_RECORDS}",
				`pre-apply: bash validate.sh "$1" "$2"`,
			} {
				if !strings.Contains(got, ref) {
					t.Errorf("missing %q after save:\n%s", ref, got)
				}
			}
			if !strings.Contains(got, "current-context: plain") {
				t.Errorf("the intended change was lost:\n%s", got)
			}

			// The file still loads, and resolves as before.
			if tc.set {
				again, err := LoadFrom(path)
				if err != nil {
					t.Fatalf("reload: %v", err)
				}
				if again.Tokens[0].Token != envRefSecret || again.Contexts[0].Context.QueryLimits.MaxResultRecords != 500 {
					t.Errorf("reloaded values not resolved: %+v", again)
				}
			}
		})
	}
}

// TestSaveToEnvRefChangedValueWins: a value the command changed is written as
// changed — the reference only stands in for a value nobody touched.
func TestSaveToEnvRefChangedValueWins(t *testing.T) {
	t.Setenv("DT_TEST_ENV_URL", "https://abc12345.apps.dynatrace.com")
	t.Setenv("DT_TEST_SECRET", envRefSecret)

	cfg, path := loadEnvRefConfig(t)
	cfg.Contexts[0].Context.Environment = "https://new12345.apps.dynatrace.com"
	fs := NewOAuthFileStoreWithDir(t.TempDir())
	if err := cfg.setTokenWithKeyring("templated-token", "dt0s16.REPLACED", &unavailableKeyring{}, fs); err != nil {
		t.Fatal(err)
	}

	got := saveAndRead(t, cfg, path)
	if !strings.Contains(got, "environment: https://new12345.apps.dynatrace.com") {
		t.Errorf("changed environment not written:\n%s", got)
	}
	if !strings.Contains(got, "token: dt0s16.REPLACED") || strings.Contains(got, "${DT_TEST_SECRET}") {
		t.Errorf("replaced token not written as replaced:\n%s", got)
	}
}

// TestSaveToEnvRefDeletedEntryStaysDeleted: restoring references never
// resurrects an entry the command removed.
func TestSaveToEnvRefDeletedEntryStaysDeleted(t *testing.T) {
	t.Setenv("DT_TEST_ENV_URL", "https://abc12345.apps.dynatrace.com")
	cfg, path := loadEnvRefConfig(t)
	if err := cfg.DeleteContext("templated"); err != nil {
		t.Fatal(err)
	}
	got := saveAndRead(t, cfg, path)
	if strings.Contains(got, "DT_TEST_ENV_URL") || strings.Contains(got, "DT_TEST_DESCRIPTION") {
		t.Errorf("deleted context came back:\n%s", got)
	}
}

// TestSaveToEnvRefInEntryName: an entry whose name is itself a reference is
// matched by its resolved name, so its token is restored like any other.
func TestSaveToEnvRefInEntryName(t *testing.T) {
	t.Setenv("DT_TEST_TOKEN_NAME", "resolved-name")
	t.Setenv("DT_TEST_SECRET", envRefSecret)
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
current-context: a
tokens:
    - name: ${DT_TEST_TOKEN_NAME}
      token: ${DT_TEST_SECRET}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CurrentContext = "b"

	got := saveAndRead(t, cfg, path)
	if strings.Contains(got, envRefSecret) {
		t.Errorf("resolved secret written to the config file:\n%s", got)
	}
	for _, ref := range []string{"name: ${DT_TEST_TOKEN_NAME}", "token: ${DT_TEST_SECRET}"} {
		if !strings.Contains(got, ref) {
			t.Errorf("missing %q after save:\n%s", ref, got)
		}
	}
}

// TestSetTokenEnvRefBypassesKeyring: the keyring never expands what it holds,
// so a reference stored there would authenticate with the literal "${VAR}".
// It stays inline, and an older keyring entry that would shadow it is removed
// once the reference is saved.
func TestSetTokenEnvRefBypassesKeyring(t *testing.T) {
	kr := newMockKeyring()
	kr.data["t"] = "dt0s16.OLD"
	cfg := NewConfig()
	if err := cfg.setTokenWithKeyring("t", "${DT_API_TOKEN}", kr, NewOAuthFileStoreWithDir(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tokens) != 1 || cfg.Tokens[0].Token != "${DT_API_TOKEN}" {
		t.Errorf("tokens = %+v, want the reference kept inline", cfg.Tokens)
	}
	if kr.data["t"] != "dt0s16.OLD" {
		t.Errorf("keyring entry removed before the reference was saved")
	}
	if err := cfg.SaveTo(filepath.Join(t.TempDir(), "config")); err != nil {
		t.Fatal(err)
	}
	if _, ok := kr.data["t"]; ok {
		t.Errorf("keyring still holds an entry for t after save: %q", kr.data["t"])
	}

	// A literal token still goes to the keyring.
	if err := cfg.setTokenWithKeyring("t", "dt0s16.NEW", kr, NewOAuthFileStoreWithDir(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if kr.data["t"] != "dt0s16.NEW" || cfg.Tokens[0].Token != "" {
		t.Errorf("literal token not stored in keyring: keyring=%q inline=%q", kr.data["t"], cfg.Tokens[0].Token)
	}
}

// TestSetTokenEnvRefFailedSaveKeepsKeyring: when the reference cannot be
// written, the keyring entry is still the credential and must survive.
func TestSetTokenEnvRefFailedSaveKeepsKeyring(t *testing.T) {
	kr := newMockKeyring()
	kr.data["t"] = "dt0s16.OLD"
	cfg := NewConfig()
	if err := cfg.setTokenWithKeyring("t", "${DT_API_TOKEN}", kr, NewOAuthFileStoreWithDir(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	// A directory where the file should be makes the write fail.
	if err := cfg.SaveTo(t.TempDir()); err == nil {
		t.Fatal("SaveTo onto a directory succeeded")
	}
	if kr.data["t"] != "dt0s16.OLD" {
		t.Errorf("keyring entry removed although the save failed")
	}
}

// TestSetTokenLiteralCancelsPendingDelete: a literal stored after a reference,
// before the save, must not be wiped by the cleanup the reference queued.
func TestSetTokenLiteralCancelsPendingDelete(t *testing.T) {
	kr := newMockKeyring()
	kr.data["t"] = "dt0s16.OLD"
	cfg := NewConfig()
	fs := NewOAuthFileStoreWithDir(t.TempDir())
	if err := cfg.setTokenWithKeyring("t", "${DT_API_TOKEN}", kr, fs); err != nil {
		t.Fatal(err)
	}
	if err := cfg.setTokenWithKeyring("t", "dt0s16.NEW", kr, fs); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(filepath.Join(t.TempDir(), "config")); err != nil {
		t.Fatal(err)
	}
	if kr.data["t"] != "dt0s16.NEW" {
		t.Errorf("keyring = %q, want the literal stored last", kr.data["t"])
	}
}

// flakyDeleteKeyring refuses Delete until allowed.
type flakyDeleteKeyring struct {
	*mockKeyring
	allow bool
}

func (k *flakyDeleteKeyring) Delete(n string) error {
	if !k.allow {
		return os.ErrPermission
	}
	return k.mockKeyring.Delete(n)
}

// TestPendingKeyringDeleteRetriedAfterFailure: a cleanup that fails after the
// file was written is reported and retried on the next save, not dropped.
func TestPendingKeyringDeleteRetriedAfterFailure(t *testing.T) {
	kr := &flakyDeleteKeyring{mockKeyring: newMockKeyring()}
	kr.data["t"] = "dt0s16.OLD"
	cfg := NewConfig()
	if err := cfg.setTokenWithKeyring("t", "${DT_API_TOKEN}", kr, NewOAuthFileStoreWithDir(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := cfg.SaveTo(path); err == nil || !strings.Contains(err.Error(), "config saved, but") {
		t.Fatalf("SaveTo error = %v, want the failed cleanup reported", err)
	}

	kr.allow = true
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("retry SaveTo: %v", err)
	}
	if _, ok := kr.data["t"]; ok {
		t.Error("failed cleanup was not retried on the next save")
	}
}

// TestGetTokenPrefersInlineEnvRef: while a stale keyring entry for the name
// may still exist, the reference the config names is the credential used.
func TestGetTokenPrefersInlineEnvRef(t *testing.T) {
	t.Setenv("DT_TEST_SECRET", envRefSecret)
	cfg, _ := loadEnvRefConfig(t)
	got, err := cfg.GetToken("templated-token")
	if err != nil || got != envRefSecret {
		t.Errorf("GetToken = %q, %v; want the resolved reference", got, err)
	}
}

// TestGetTokenResolvesReferenceSetInProcess: a reference stored by SetToken on
// this Config is resolved, never skipped in favour of a keyring entry a failed
// cleanup left behind.
func TestGetTokenResolvesReferenceSetInProcess(t *testing.T) {
	t.Setenv("DT_API_TOKEN", envRefSecret)
	kr := &flakyDeleteKeyring{mockKeyring: newMockKeyring()}
	kr.data["t"] = "dt0s16.STALE"
	cfg := NewConfig()
	if err := cfg.setTokenWithKeyring("t", "${DT_API_TOKEN}", kr, NewOAuthFileStoreWithDir(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(filepath.Join(t.TempDir(), "config")); err == nil {
		t.Fatal("SaveTo succeeded although the keyring refused the cleanup")
	}
	got, err := cfg.GetToken("t")
	if err != nil || got != envRefSecret {
		t.Errorf("GetToken = %q, %v; want the resolved reference", got, err)
	}
}

// TestGetTokenUnsetReferenceErrors: a reference whose variable is unset is an
// error naming the variable, not a silent fallback to a stale keyring value.
func TestGetTokenUnsetReferenceErrors(t *testing.T) {
	t.Setenv("DT_TEST_SECRET", "")
	os.Unsetenv("DT_TEST_SECRET")
	cfg, _ := loadEnvRefConfig(t)
	_, err := cfg.GetToken("templated-token")
	if err == nil || !strings.Contains(err.Error(), "${DT_TEST_SECRET}") {
		t.Errorf("GetToken error = %v, want one naming ${DT_TEST_SECRET}", err)
	}
}

// TestTokensAsStored: displaying a config shows the reference, not the secret,
// and leaves the resolved value in place for requests.
func TestTokensAsStored(t *testing.T) {
	t.Setenv("DT_TEST_SECRET", envRefSecret)
	cfg, _ := loadEnvRefConfig(t)

	shown := cfg.TokensAsStored()
	if shown.Tokens[0].Token != "${DT_TEST_SECRET}" {
		t.Errorf("shown token = %q, want the reference", shown.Tokens[0].Token)
	}
	if shown.Tokens[1].Token != "plain-value" {
		t.Errorf("literal token = %q, want it unchanged", shown.Tokens[1].Token)
	}
	if cfg.Tokens[0].Token != envRefSecret {
		t.Errorf("TokensAsStored mutated the config: %q", cfg.Tokens[0].Token)
	}
}

// TestTokenFromEnvRef backs MigrateTokensToKeyring's skip: a reference must not
// be frozen into the keyring, whether the config was loaded with expansion or
// without it.
func TestTokenFromEnvRef(t *testing.T) {
	t.Setenv("DT_TEST_SECRET", envRefSecret)
	// No numeric reference here: the non-expanding loader cannot parse one.
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
tokens:
    - name: templated-token
      token: ${DT_TEST_SECRET}
    - name: plain-token
      token: plain-value
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.tokenFromEnvRef("templated-token") || cfg.tokenFromEnvRef("plain-token") {
		t.Errorf("expanded load: templated=%v plain=%v, want true/false",
			cfg.tokenFromEnvRef("templated-token"), cfg.tokenFromEnvRef("plain-token"))
	}

	raw, err := LoadFromWithoutExpansion(path)
	if err != nil {
		t.Fatal(err)
	}
	if !raw.tokenFromEnvRef("templated-token") || raw.tokenFromEnvRef("plain-token") {
		t.Errorf("raw load: templated=%v plain=%v, want true/false",
			raw.tokenFromEnvRef("templated-token"), raw.tokenFromEnvRef("plain-token"))
	}
}

func TestIsEnvReference(t *testing.T) {
	for s, want := range map[string]bool{
		"${DT_API_TOKEN}":              true,
		"$DT_API_TOKEN":                true,
		"prefix-${X}":                  true,
		"dt0s16.ABC.DEF":               false,
		"pa$$word":                     false,
		"":                             false,
		"https://abc.apps.example.com": false,
	} {
		if got := IsEnvReference(s); got != want {
			t.Errorf("IsEnvReference(%q) = %v, want %v", s, got, want)
		}
	}
}
