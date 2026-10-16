package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// setupShareCmdTest wires a fresh file-backed config pointed at srv and
// resets the command tree/dry-run flag afterward, the same shape
// TestShareDocument_NoNotify uses.
func setupShareCmdTest(t *testing.T, srv *httptest.Server) {
	t.Helper()

	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")
	configPath := filepath.Join(t.TempDir(), "config")
	originalCfgFile, originalDryRun := cfgFile(context.Background()), dryRun(context.Background())
	restorePristineTree(context.Background())
	t.Cleanup(func() {
		gFlags.cfgFile, gFlags.dryRun = originalCfgFile, originalDryRun
		restorePristineTree(context.Background())
	})
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", srv.URL, "test-token")
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
}

// TestShareDocument_NoNotify drives the real share command against a mock
// Document API and checks that --no-notify reaches the API on both paths the
// command can take: creating a new share, and adding to an existing one.
// Without it the API notifies every recipient, and a group recipient is every
// member of the group.
func TestShareDocument_NoNotify(t *testing.T) {
	tests := []struct {
		name          string
		existingShare bool
		noNotify      bool
		wantPath      string
		wantParam     string
	}{
		{"create notifies by default", false, false, "/platform/document/v1/direct-shares", ""},
		{"create with --no-notify", false, true, "/platform/document/v1/direct-shares", "false"},
		{"add notifies by default", true, false, "/platform/document/v1/direct-shares/share-1/recipients/add", ""},
		{"add with --no-notify", true, true, "/platform/document/v1/direct-shares/share-1/recipients/add", "false"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			mutations := map[string]string{}
			record := func(r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				mutations[r.URL.Path] = r.URL.Query().Get("send-notification")
			}

			mux := http.NewServeMux()
			mux.HandleFunc("/platform/document/v1/documents/doc-123/metadata", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"doc-123","name":"Dash","type":"dashboard","owner":"owner-1","version":1}`))
			})
			mux.HandleFunc("/platform/document/v1/direct-shares", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					if tt.existingShare {
						_, _ = w.Write([]byte(`{"direct-shares":[{"id":"share-1","documentId":"doc-123","access":["read"]}],"totalCount":1}`))
					} else {
						_, _ = w.Write([]byte(`{"direct-shares":[],"totalCount":0}`))
					}
					return
				}
				record(r)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"share-1","documentId":"doc-123","access":["read"]}`))
			})
			mux.HandleFunc("/platform/document/v1/direct-shares/share-1/recipients/add", func(w http.ResponseWriter, r *http.Request) {
				record(r)
				w.WriteHeader(http.StatusNoContent)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			t.Setenv("DTCTL_DISABLE_KEYRING", "1")
			t.Setenv(config.EnvTokenStorage, "file")
			configPath := filepath.Join(t.TempDir(), "config")
			originalCfgFile, originalDryRun := cfgFile(context.Background()), dryRun(context.Background())
			// Another test may have left a stability floor wrapped around this
			// command; start from, and leave behind, the as-registered tree.
			restorePristineTree(context.Background())
			t.Cleanup(func() {
				gFlags.cfgFile, gFlags.dryRun = originalCfgFile, originalDryRun
				restorePristineTree(context.Background())
			})
			gFlags.cfgFile, gFlags.dryRun = configPath, false

			cfg := config.NewConfig()
			cfg.SetContext("test", srv.URL, "test-token")
			if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
				t.Fatalf("failed to set token: %v", err)
			}
			cfg.CurrentContext = "test"
			if err := cfg.SaveTo(configPath); err != nil {
				t.Fatalf("failed to save config: %v", err)
			}

			_ = shareDocumentCmd.Flags().Set("group", "group-1")
			if tt.noNotify {
				_ = shareDocumentCmd.Flags().Set("no-notify", "true")
			}

			captureExtStdout(t, func() {
				if err := shareDocumentCmd.RunE(shareDocumentCmd, []string{"doc-123"}); err != nil {
					t.Fatalf("share document failed: %v", err)
				}
			})

			if len(mutations) != 1 {
				t.Fatalf("got %d mutating calls %v, want exactly one to %s", len(mutations), mutations, tt.wantPath)
			}
			got, ok := mutations[tt.wantPath]
			if !ok {
				t.Fatalf("mutating calls %v, want one to %s", mutations, tt.wantPath)
			}
			if got != tt.wantParam {
				t.Errorf("send-notification = %q, want %q", got, tt.wantParam)
			}
		})
	}
}
