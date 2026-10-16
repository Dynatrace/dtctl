package cmd

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

func claimMux(t *testing.T, called *bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares/share-1/claim", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		*called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"documentId":"doc-123","documentType":"dashboard","access":["read"]}`))
	})
	mux.HandleFunc("/platform/document/v1/documents/doc-123/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"doc-123","name":"Prod overview","type":"dashboard","version":1}`))
	})
	return mux
}

func TestClaimEnvironmentShare_Success(t *testing.T) {
	var called bool
	srv := httptest.NewServer(claimMux(t, &called))
	t.Cleanup(srv.Close)
	setupShareCmdTest(t, srv)

	out := captureExtStdout(t, func() {
		if err := claimEnvironmentShareCmd.RunE(claimEnvironmentShareCmd, []string{"share-1"}); err != nil {
			t.Fatalf("claim failed: %v", err)
		}
	})
	if !called {
		t.Error("expected the claim endpoint to be called")
	}
	if !strings.Contains(out, "doc-123") || !strings.Contains(out, "dashboard") || !strings.Contains(out, "Prod overview") {
		t.Errorf("output %q lacks the document ID/type/name", out)
	}
}

func TestClaimEnvironmentShare_MetadataFailureStillSucceeds(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares/share-1/claim", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"documentId":"doc-123","documentType":"dashboard","access":["read"]}`))
	})
	mux.HandleFunc("/platform/document/v1/documents/doc-123/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	setupShareCmdTest(t, srv)

	out := captureExtStdout(t, func() {
		if err := claimEnvironmentShareCmd.RunE(claimEnvironmentShareCmd, []string{"share-1"}); err != nil {
			t.Fatalf("a failed name lookup must not fail the claim: %v", err)
		}
	})
	if !strings.Contains(out, "doc-123") {
		t.Errorf("output %q lacks the document ID", out)
	}
}

func TestClaimEnvironmentShare_APIErrors(t *testing.T) {
	tests := []struct {
		status int
		want   []string
	}{
		{http.StatusBadRequest, []string{"you own this share", "get documents --mine"}},
		{http.StatusForbidden, []string{"access denied", "dtctl auth login", "document:environment-shares:claim"}},
		{http.StatusNotFound, []string{`"share-1" not found`, "deleted the share"}},
	}
	for _, tt := range tests {
		mux := http.NewServeMux()
		mux.HandleFunc("/platform/document/v1/environment-shares/share-1/claim", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(tt.status) + `,"message":"nope"}}`))
		})
		srv := httptest.NewServer(mux)
		setupShareCmdTest(t, srv)

		err := claimEnvironmentShareCmd.RunE(claimEnvironmentShareCmd, []string{"share-1"})
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected an error", tt.status)
		}
		for _, w := range tt.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("status %d: error %q lacks %q", tt.status, err, w)
			}
		}
	}
}

func TestShareHostMismatch_SuggestsMatchingContext(t *testing.T) {
	cfg := config.NewConfig()
	cfg.SetContext("cur", "https://abc.apps.dynatrace.com", "t")
	cfg.SetContext("other", "https://xyz.apps.dynatrace.com/", "t")
	cfg.CurrentContext = "cur"

	err := shareHostMismatch(cfg, "xyz.apps.dynatrace.com", "https://abc.apps.dynatrace.com")
	if !strings.Contains(err.Error(), "--context other") {
		t.Errorf("error %q does not suggest the matching context", err)
	}

	err = shareHostMismatch(cfg, "new.apps.dynatrace.com", "https://abc.apps.dynatrace.com")
	if !strings.Contains(err.Error(), "auth login --context <name> --environment https://new.apps.dynatrace.com") {
		t.Errorf("error %q does not suggest adding a context", err)
	}
}

func TestClaimEnvironmentShare_PastedURL(t *testing.T) {
	var called bool
	srv := httptest.NewServer(claimMux(t, &called))
	t.Cleanup(srv.Close)
	setupShareCmdTest(t, srv)

	link := srv.URL + "/ui/document/v0/#share=share-1"
	if err := claimEnvironmentShareCmd.RunE(claimEnvironmentShareCmd, []string{link}); err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if !called {
		t.Error("expected the claim endpoint to be called")
	}
}

func TestClaimEnvironmentShare_HostMismatch(t *testing.T) {
	var called bool
	srv := httptest.NewServer(claimMux(t, &called))
	t.Cleanup(srv.Close)
	setupShareCmdTest(t, srv)

	link := "https://other.apps.example.invalid/ui/document/v0/#share=share-1"
	err := claimEnvironmentShareCmd.RunE(claimEnvironmentShareCmd, []string{link})
	if err == nil || !strings.Contains(err.Error(), "current context") {
		t.Fatalf("expected a host mismatch error, got %v", err)
	}
	if called {
		t.Error("a mismatched link must not reach the claim endpoint")
	}
}

func TestClaimEnvironmentShare_DryRun(t *testing.T) {
	var called bool
	srv := httptest.NewServer(claimMux(t, &called))
	t.Cleanup(srv.Close)
	setupShareCmdTest(t, srv)
	gFlags.dryRun = true

	out := captureExtStdout(t, func() {
		if err := claimEnvironmentShareCmd.RunE(claimEnvironmentShareCmd, []string{"share-1"}); err != nil {
			t.Fatalf("dry run failed: %v", err)
		}
	})
	if called {
		t.Error("dry run must not send the claim request")
	}
	if !strings.Contains(out, "Dry run") {
		t.Errorf("output %q does not read as a dry run", out)
	}
}

func TestSameEnvironment(t *testing.T) {
	tests := []struct {
		link, env string
		want      bool
	}{
		{"abc.apps.dynatrace.com", "https://abc.apps.dynatrace.com", true},
		{"abc.apps.dynatrace.com", "https://xyz.apps.dynatrace.com", false},
		{"abc.attacker.example", "https://abc.apps.dynatrace.com", false},
		{"abc.apps.dynatrace.com.attacker.example", "https://abc.apps.dynatrace.com", false},
		{"127.0.0.1", "http://127.0.0.1:8080", true},
	}
	for _, tt := range tests {
		if got := sameEnvironment(tt.link, tt.env); got != tt.want {
			t.Errorf("sameEnvironment(%q, %q) = %v, want %v", tt.link, tt.env, got, tt.want)
		}
	}
}
