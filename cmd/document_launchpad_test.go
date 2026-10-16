package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// setupDocumentCmdTest points the command tree at srv through a temporary
// config at the given safety level and restores global state afterwards.
func setupDocumentCmdTest(t *testing.T, srvURL string, level config.SafetyLevel) {
	t.Helper()
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")
	configPath := filepath.Join(t.TempDir(), "config")
	origCfgFile, origDryRun, origPlain := cfgFile(context.Background()), dryRun(context.Background()), plainMode(context.Background())
	restorePristineTree(context.Background())
	t.Cleanup(func() {
		gFlags.cfgFile, gFlags.dryRun, gFlags.plainMode = origCfgFile, origDryRun, origPlain
		restorePristineTree(context.Background())
	})
	gFlags.cfgFile, gFlags.dryRun = configPath, false

	cfg := config.NewConfig()
	cfg.SetContextWithOptions("test", srvURL, "test-token", &config.ContextOptions{SafetyLevel: level})
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
}

func captureDocStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	restore := func() {
		_ = w.Close()
		os.Stderr = orig
	}
	defer restore() // also on t.Fatal inside fn
	fn()
	restore()
	return <-done
}

// TestCreateDocument_LaunchpadURL pins #361 item 2: a created launchpad links
// to the launcher app. The "dynatrace.launchpads" path the command used to
// print returns 404.
func TestCreateDocument_LaunchpadURL(t *testing.T) {
	const id = "team-launchpad"
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		boundary := "resp-boundary"
		w.Header().Set("Content-Type", fmt.Sprintf("multipart/form-data; boundary=%s", boundary))
		fmt.Fprintf(w,
			"--%s\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n{\"id\":%q,\"name\":\"Team Launchpad\",\"type\":\"launchpad\",\"version\":1}\r\n--%s--\r\n",
			boundary, id, boundary)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	file := filepath.Join(t.TempDir(), "launchpad.json")
	if err := os.WriteFile(file, []byte(`{"name":"Team Launchpad","type":"launchpad","content":{"blocks":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for flag, value := range map[string]string{"file": file, "type": "launchpad", "id": id} {
		if err := createDocumentCmd.Flags().Set(flag, value); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	t.Cleanup(func() {
		for _, flag := range []string{"file", "type", "id"} {
			_ = createDocumentCmd.Flags().Set(flag, "")
		}
	})

	stderr := captureDocStderr(t, func() {
		if err := createDocumentCmd.RunE(createDocumentCmd, nil); err != nil {
			t.Fatalf("create document failed: %v", err)
		}
	})

	want := srv.URL + "/ui/apps/dynatrace.launcher/launchpad/" + id
	if !strings.Contains(stderr, "URL:  "+want) {
		t.Errorf("output does not link the launcher app\nwant URL: %s\ngot:\n%s", want, stderr)
	}
	if strings.Contains(stderr, "dynatrace.launchpads") {
		t.Errorf("output still links the non-existent dynatrace.launchpads app:\n%s", stderr)
	}
}

// slugDocumentServer serves one launchpad whose ID is a slug, plus a different
// document whose *name* is that slug, and records DELETE requests.
func slugDocumentServer(t *testing.T, deleted *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/team-launchpad/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"team-launchpad","name":"Team Launchpad","type":"launchpad","owner":"owner-1","version":3}`))
	})
	mux.HandleFunc("/platform/document/v1/documents/other-doc-id/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"other-doc-id","name":"team-launchpad","type":"dashboard","owner":"owner-1","version":1}`))
	})
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"documents":[{"id":"other-doc-id","name":"team-launchpad","type":"dashboard","version":1}],"totalCount":1}`))
	})
	deleteHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		*deleted = append(*deleted, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("/platform/document/v1/documents/team-launchpad", deleteHandler)
	mux.HandleFunc("/platform/document/v1/documents/other-doc-id", deleteHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDeleteDocument_BySlugID pins #361 item 3: `delete document <slug>`
// resolves the argument as a document ID, as `get document <slug>` does,
// instead of failing with "no document found with name". A different document
// whose name equals the slug is never the one deleted.
func TestDeleteDocument_BySlugID(t *testing.T) {
	var deleted []string
	srv := slugDocumentServer(t, &deleted)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)
	gFlags.plainMode = true

	captureDocStderr(t, func() {
		if err := deleteDocumentCmd.RunE(deleteDocumentCmd, []string{"team-launchpad"}); err != nil {
			t.Fatalf("delete document failed: %v", err)
		}
	})

	want := "/platform/document/v1/documents/team-launchpad?optimistic-locking-version=3"
	if len(deleted) != 1 || deleted[0] != want {
		t.Fatalf("DELETE requests = %v, want exactly [%s]", deleted, want)
	}
}

// TestDeleteDocument_BySlugIDStillNeedsSafetyLevel: resolving a slug ID must
// not bypass the safety check; a readonly context sends no DELETE.
func TestDeleteDocument_BySlugIDStillNeedsSafetyLevel(t *testing.T) {
	var deleted []string
	srv := slugDocumentServer(t, &deleted)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadOnly)
	gFlags.plainMode = true

	var err error
	captureDocStderr(t, func() {
		err = deleteDocumentCmd.RunE(deleteDocumentCmd, []string{"team-launchpad"})
	})
	if err == nil {
		t.Fatal("delete document succeeded in a readonly context")
	}
	if !strings.Contains(err.Error(), "does not allow delete") {
		t.Errorf("error = %v, want the safety check's refusal", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("DELETE requests = %v in a readonly context, want none", deleted)
	}
}
