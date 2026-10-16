package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

const (
	typedNotebookUUID  = "11111111-2222-3333-4444-555555555555"
	typedDashboardUUID = "66666666-7777-8888-9999-000000000000"
)

// typedDocumentServer serves one notebook and one dashboard by UUID and records
// every request that is not a GET, so a test can assert that nothing mutating
// reached the API.
func typedDocumentServer(t *testing.T, writes *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	metadata := map[string]string{
		typedNotebookUUID:  `{"id":"` + typedNotebookUUID + `","name":"Some Notebook","type":"notebook","owner":"owner-1","version":2}`,
		typedDashboardUUID: `{"id":"` + typedDashboardUUID + `","name":"Some Dashboard","type":"dashboard","owner":"owner-1","version":4}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mu.Lock()
			*writes = append(*writes, r.Method+" "+r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/platform/document/v1/documents/")
		id, ok := strings.CutSuffix(rest, "/metadata")
		body, known := metadata[id]
		if !ok || !known {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestTypedDocumentCommandsRefuseOtherType: a UUID-shaped argument is taken as
// an ID without a name search, so `delete dashboard <notebook-uuid>` used to
// delete the notebook. The typed commands now check the document's type before
// any write and send no mutating request on a mismatch.
func TestTypedDocumentCommandsRefuseOtherType(t *testing.T) {
	tests := []struct {
		name string
		cmd  *cobra.Command
		args []string
		want string
	}{
		{"delete dashboard given a notebook", deleteDashboardCmd, []string{typedNotebookUUID}, "is a notebook, not a dashboard"},
		{"delete notebook given a dashboard", deleteNotebookCmd, []string{typedDashboardUUID}, "is a dashboard, not a notebook"},
		{"restore dashboard given a notebook", restoreDashboardCmd, []string{typedNotebookUUID, "1"}, "is a notebook, not a dashboard"},
		{"restore notebook given a dashboard", restoreNotebookCmd, []string{typedDashboardUUID, "1"}, "is a dashboard, not a notebook"},
		{"describe dashboard given a notebook", describeDashboardCmd, []string{typedNotebookUUID}, "is a notebook, not a dashboard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var writes []string
			srv := typedDocumentServer(t, &writes)
			setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)
			gFlags.plainMode = true

			var err error
			captureDocStderr(t, func() {
				err = tt.cmd.RunE(tt.cmd, tt.args)
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
			if len(writes) != 0 {
				t.Fatalf("mutating requests = %v, want none", writes)
			}
		})
	}
}

// TestDeleteNotebookByUUIDStillWorks: the type check does not get in the way
// of the matching type.
func TestDeleteNotebookByUUIDStillWorks(t *testing.T) {
	var writes []string
	srv := typedDocumentServer(t, &writes)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)
	gFlags.plainMode = true

	captureDocStderr(t, func() {
		if err := deleteNotebookCmd.RunE(deleteNotebookCmd, []string{typedNotebookUUID}); err != nil {
			t.Fatalf("delete notebook failed: %v", err)
		}
	})
	want := "DELETE /platform/document/v1/documents/" + typedNotebookUUID
	if len(writes) != 1 || writes[0] != want {
		t.Fatalf("mutating requests = %v, want exactly [%s]", writes, want)
	}
}
