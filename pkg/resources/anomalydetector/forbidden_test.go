package anomalydetector

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
)

// Every Settings API operation must report a 403 the way issue #476 asks for:
// the server's reason reaches the user, the error is typed so the process exits
// with the permission code, and the suggestions name the permission that was
// denied and separate the scope gate from the IAM policy gate.
func TestForbiddenIsTypedAndSurfacesReason(t *testing.T) {
	const body = `{"error":{"code":403,"message":"No permission to access settings object."}}`

	detector := []byte(`{"title":"Forbidden Test Detector","analyzer":{"name":"dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer"},"eventTemplate":{"event.type":"PERFORMANCE_EVENT"}}`)

	tests := []struct {
		name       string
		permission string
		// getSucceeds lets the preparatory GET through, so the 403 under test
		// comes from the verb being exercised rather than the lookup before it.
		getSucceeds bool
		call        func(h *Handler) error
	}{
		{
			name:       "list",
			permission: "settings:objects:read",
			call:       func(h *Handler) error { _, err := h.List(ListOptions{}); return err },
		},
		{
			name:       "get",
			permission: "settings:objects:read",
			call:       func(h *Handler) error { _, err := h.Get("obj-1"); return err },
		},
		{
			name:       "create",
			permission: "settings:objects:write",
			call:       func(h *Handler) error { _, err := h.Create(detector); return err },
		},
		{
			name:        "update",
			permission:  "settings:objects:write",
			getSucceeds: true,
			call:        func(h *Handler) error { _, err := h.Update("obj-1", detector); return err },
		},
		{
			name:       "delete",
			permission: "settings:objects:write",
			call:       func(h *Handler) error { return h.Delete("obj-1") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, server := newTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tt.getSucceeds && r.Method == http.MethodGet {
					fmt.Fprintf(w, `{"objectId":"obj-1","schemaId":%q,"scope":%q,"schemaVersion":"1","value":{"title":"x","enabled":true}}`, SchemaID, Scope)
					return
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(body))
			})
			defer server.Close()

			err := tt.call(h)
			if err == nil {
				t.Fatal("expected an error for 403")
			}

			var diagErr *diagnostic.Error
			if !errors.As(err, &diagErr) {
				t.Fatalf("error = %T (%v), want *diagnostic.Error", err, err)
			}
			if diagErr.StatusCode != 403 {
				t.Errorf("StatusCode = %d, want 403", diagErr.StatusCode)
			}
			if got := diagErr.ExitCode(); got != client.ExitPermissionError {
				t.Errorf("ExitCode() = %d, want %d", got, client.ExitPermissionError)
			}
			if !strings.Contains(err.Error(), "No permission to access settings object.") {
				t.Errorf("error dropped the server explanation: %q", err.Error())
			}

			joined := strings.Join(diagErr.Suggestions, "\n")
			if !strings.Contains(joined, tt.permission) {
				t.Errorf("suggestions do not name %q: %q", tt.permission, joined)
			}
			if !strings.Contains(joined, "--check-scopes") {
				t.Errorf("suggestions do not point at the scope check: %q", joined)
			}
		})
	}
}
