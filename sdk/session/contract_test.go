package session

// Contract tests: lock the shared config-file contract that dtctl and every
// dtctl-* plugin depend on — see docs/dev/CONFIG_CONTRACT.md.
// The fixtures under testdata/contract/ are the golden artifacts of that
// contract; a change that breaks these tests is a contract change and needs
// the spec updated in the same PR.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func contractFixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", "contract", name)
}

// copyFixture copies a fixture into a temp dir so save paths can be exercised
// without touching the golden file.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(contractFixture(t, name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return path
}

func TestContract_LoadFullFixture(t *testing.T) {
	cfg, err := LoadFrom(contractFixture(t, "v1-full.yaml"))
	if err != nil {
		t.Fatalf("LoadFrom(v1-full) = %v; the full fixture must always load", err)
	}

	if cfg.CurrentContext != "dev" {
		t.Errorf("CurrentContext = %q, want dev", cfg.CurrentContext)
	}
	if len(cfg.Contexts) != 2 {
		t.Fatalf("len(Contexts) = %d, want 2", len(cfg.Contexts))
	}
	dev := cfg.Contexts[0].Context
	if dev.Environment != "https://dev.example.invalid" {
		t.Errorf("dev environment = %q", dev.Environment)
	}
	if dev.SafetyLevel != SafetyLevelReadOnly {
		t.Errorf("dev safety level = %q, want readonly", dev.SafetyLevel)
	}
	if dev.Hooks.PreApply != "echo pre" {
		t.Errorf("dev pre-apply hook = %q", dev.Hooks.PreApply)
	}
	if dev.Spill == nil || dev.Spill.Threshold != "50KB" {
		t.Errorf("dev spill override not parsed: %+v", dev.Spill)
	}
	if len(cfg.Tokens) != 2 {
		t.Errorf("len(Tokens) = %d, want 2", len(cfg.Tokens))
	}
	if cfg.Preferences.Output != "table" {
		t.Errorf("preferences.output = %q", cfg.Preferences.Output)
	}
	if cfg.Aliases["errlogs"] == "" {
		t.Error("aliases not parsed")
	}
	if cfg.Spill.TTL != "24h" {
		t.Errorf("spill.ttl = %q", cfg.Spill.TTL)
	}
	if cfg.QueryLimits.ScanLimitGbytes != 500 || cfg.QueryLimits.MaxResultRecords != 5000 {
		t.Errorf("query-limits not parsed: %+v", cfg.QueryLimits)
	}
	if dev.QueryLimits == nil || dev.QueryLimits.ScanLimitGbytes != 50 {
		t.Errorf("dev query-limits override not parsed: %+v", dev.QueryLimits)
	}
	if !dev.UsageTracking {
		t.Error("dev usage-tracking not parsed")
	}
	if cfg.Contexts[1].Context.UsageTracking {
		t.Error("prod usage-tracking must default to off when the key is absent")
	}
}

func TestContract_LoadMinimalFixture(t *testing.T) {
	cfg, err := LoadFrom(contractFixture(t, "v1-minimal.yaml"))
	if err != nil {
		t.Fatalf("LoadFrom(v1-minimal) = %v; apiVersion must be optional", err)
	}
	if cfg.CurrentContext != "only" || len(cfg.Contexts) != 1 {
		t.Errorf("minimal fixture misparsed: current=%q contexts=%d", cfg.CurrentContext, len(cfg.Contexts))
	}
}

// Tolerant parsing: unknown fields anywhere in the document must not fail the
// load. (The full fixture plants unknown keys at the top level, inside a
// context, inside a token, and inside preferences.)
func TestContract_TolerantParsingOfUnknownFields(t *testing.T) {
	if _, err := LoadFrom(contractFixture(t, "v1-full.yaml")); err != nil {
		t.Fatalf("unknown fields must be ignored on load, got: %v", err)
	}
}

// Three apiVersion spellings denote schema v1 in the wild; all must load.
func TestContract_AcceptedSchemaVersionSpellings(t *testing.T) {
	for _, v := range []string{"", "v1", "dtctl.io/v1"} {
		doc := "current-context: only\ncontexts:\n  - name: only\n    context:\n      environment: https://only.example.invalid\n      token-ref: t\n"
		if v != "" {
			doc = "apiVersion: " + v + "\n" + doc
		}
		path := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFrom(path); err != nil {
			t.Errorf("apiVersion %q must be accepted as schema v1, got: %v", v, err)
		}
	}
}

func TestContract_UnsupportedSchemaVersionRejected(t *testing.T) {
	_, err := LoadFrom(contractFixture(t, "future-version.yaml"))
	if err == nil {
		t.Fatal("loading an unsupported schema version must fail, got nil error")
	}
	if !strings.Contains(err.Error(), "schema version") || !strings.Contains(err.Error(), "v99") {
		t.Errorf("error should name the offending schema version, got: %v", err)
	}
}

func TestContract_SaveWritesCurrentSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := NewConfig().SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "apiVersion: "+CurrentAPIVersion) {
		t.Errorf("saved config must carry apiVersion %s, got:\n%s", CurrentAPIVersion, data)
	}
}

// Round-trip preservation: a load-modify-save cycle by this build must not
// destroy fields written by a newer schema-v1 writer, while still honoring
// deletions of state this build owns.
func TestContract_RoundTripPreservesUnknownFields(t *testing.T) {
	path := copyFixture(t, "v1-full.yaml")

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Typical management operations: edit one context, delete another,
	// add a third, clear an omitempty field.
	cfg.SetContext("dev", "https://dev2.example.invalid", "dev-token")
	if err := cfg.DeleteContext("prod"); err != nil {
		t.Fatalf("delete context: %v", err)
	}
	cfg.SetContext("staging", "https://staging.example.invalid", "staging-token")
	cfg.Contexts[0].Context.Description = ""
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	saved := string(data)

	for _, want := range []string{
		"future-top-level-field: keep-me",   // unknown top-level key
		"future-context-field: keep-me-too", // unknown key in surviving context
		"future-token-field: keep-me-three", // unknown key in surviving token
		"future-preference: keep-me-four",   // unknown key in preferences
		"https://dev2.example.invalid",      // the edit itself
		"name: staging",                     // the added context
	} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved config lost %q:\n%s", want, saved)
		}
	}
	for _, gone := range []string{
		"name: prod\n",                    // deleted context must not resurrect ("prod-token" still exists)
		"https://prod.example.invalid",    // nor any of its fields
		"description: development tenant", // cleared omitempty field stays gone
	} {
		if strings.Contains(saved, gone) {
			t.Errorf("saved config resurrected %q:\n%s", gone, saved)
		}
	}

	// The saved file must still load — preservation must not corrupt it.
	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload after preserve: %v", err)
	}
	if len(reloaded.Contexts) != 2 {
		t.Errorf("reloaded contexts = %d, want 2 (dev, staging)", len(reloaded.Contexts))
	}
}

// An unknown field that aliases an anchor on a known field cannot be grafted
// intact: the fresh marshal rewrote the anchor away, so the merged document
// would carry a dangling alias and fail every subsequent load. The save must
// detect that and fall back to the fresh marshal — the file always stays
// loadable, even at the cost of dropping the aliasing field.
func TestContract_SaveWithCrossBoundaryAliasStaysLoadable(t *testing.T) {
	for name, doc := range map[string]string{
		"alias": "apiVersion: v1\nkind: Config\ncurrent-context: &cc dev\ncontexts:\n" +
			"  - name: dev\n    context:\n      environment: https://dev.example.invalid\n      token-ref: t\n" +
			"future-ref: *cc\n",
		"merge-key": "apiVersion: v1\nkind: Config\ncurrent-context: dev\ncontexts:\n" +
			"  - name: dev\n    context: &base\n      environment: https://dev.example.invalid\n      token-ref: t\n" +
			"future-thing:\n  <<: *base\n  extra: y\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFrom(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if err := cfg.SaveTo(path); err != nil {
				t.Fatalf("save: %v", err)
			}
			if _, err := LoadFrom(path); err != nil {
				t.Fatalf("config corrupted by save round-trip: %v", err)
			}
		})
	}
}

// Anchors and aliases living entirely inside an unknown subtree stay intact:
// the subtree is grafted as one node, so nothing dangles.
func TestContract_UnknownSubtreeAliasPreserved(t *testing.T) {
	doc := "apiVersion: v1\nkind: Config\ncurrent-context: dev\ncontexts:\n" +
		"  - name: dev\n    context:\n      environment: https://dev.example.invalid\n      token-ref: t\n" +
		"future-thing:\n  base: &b hello\n  ref: *b\n"
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "future-thing") {
		t.Errorf("self-contained unknown subtree was dropped:\n%s", data)
	}
	if _, err := LoadFrom(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

// A save over a file that does not parse falls back to a plain write instead
// of failing — preservation is strictly best-effort.
func TestContract_SaveOverCorruptFileStillSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("{{ not yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := NewConfig()
	cfg.SetContext("dev", "https://dev.example.invalid", "dev-token")
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo over corrupt file: %v", err)
	}
	if _, err := LoadFrom(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

// The keyring service name is part of the credential contract shared with
// every consumer — changing it strands every stored credential.
func TestContract_KeyringServiceName(t *testing.T) {
	if KeyringService != "dtctl" {
		t.Errorf("KeyringService = %q; this is a shared contract (docs/dev/CONFIG_CONTRACT.md) — changing it requires a migration", KeyringService)
	}
}
