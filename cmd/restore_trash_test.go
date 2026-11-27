package cmd

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRestoreTrashConfirmation runs `restore trash` end to end against a mock
// document service. --yes skips the prompt and nothing else; --force skips it
// too but also sends the name-conflict override; a declined prompt sends no
// restore at all.
func TestRestoreTrashConfirmation(t *testing.T) {
	const id = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	docPath := "/platform/document/v1/trash/documents/" + id
	restorePath := docPath + "/restore"

	tests := []struct {
		name        string
		flags       []string
		stdin       string
		wantPrompt  bool
		wantRestore bool
		wantForce   bool
	}{
		{"--yes", []string{"--yes"}, "", false, true, false},
		{"-y", []string{"-y"}, "", false, true, false},
		{"--force", []string{"--force"}, "", false, true, true},
		{"confirmed", nil, "y\n", true, true, false},
		{"declined", nil, "n\n", true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				restores []string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == docPath:
					_, _ = fmt.Fprintf(w, `{"id":%q,"name":"My Dashboard","type":"dashboard","version":1}`, id)
				case r.Method == http.MethodPost && r.URL.Path == restorePath:
					mu.Lock()
					restores = append(restores, r.URL.RawQuery)
					mu.Unlock()
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
				}
			}))
			t.Cleanup(srv.Close)
			isolatedConfig(t, safetyLevelConfig(srv.URL, "readwrite-all"))

			var stdout, stderr bytes.Buffer
			argv := append([]string{"restore", "trash", id, "--no-agent"}, tt.flags...)
			code := Run(argv, RunOptions{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(tt.stdin)})
			out := stdout.String() + stderr.String()

			require.Equal(t, tt.wantPrompt, strings.Contains(out, "from trash? [y/N]"), out)

			mu.Lock()
			defer mu.Unlock()
			if !tt.wantRestore {
				require.Empty(t, restores, "a declined prompt must not send the restore")
				require.Contains(t, out, "Restore cancelled")
				require.NotZero(t, code, out)
				return
			}
			require.Zero(t, code, out)
			require.Len(t, restores, 1)
			require.Equal(t, tt.wantForce, strings.Contains(restores[0], "force=true"),
				"query %q: only --force may send the override", restores[0])
		})
	}
}
