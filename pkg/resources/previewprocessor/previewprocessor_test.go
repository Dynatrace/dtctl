package previewprocessor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

const previewPath = "/platform/openpipeline/v1/preview/processor"

// newTestHandler wires a Handler to a mock server serving the given response body
// at the preview endpoint. When capture is non-nil the request body is recorded.
func newTestHandler(t *testing.T, status int, body string, capture *[]byte) *Handler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != previewPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewForTesting: %v", err)
	}
	return NewHandler(c)
}

// writeProcessorFile writes a processor body to a temp file and returns its path.
func writeProcessorFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "processor.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestHandler_Preview(t *testing.T) {
	resp := `{"results":[
		{"matched":true,"record":{"content":"hello","severity":"INFO"}},
		{"matched":false}
	]}`
	var reqBody []byte
	h := newTestHandler(t, http.StatusOK, resp, &reqBody)

	path := writeProcessorFile(t, `{"type":"dql","matcher":"true","dqlScript":"fieldsAdd severity = \"INFO\""}`)
	results, err := h.Preview(path, "logs")
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if !results[0].Matched {
		t.Errorf("results[0].Matched = false, want true")
	}
	if results[0].Record["severity"] != "INFO" {
		t.Errorf("results[0].Record[severity] = %v, want INFO", results[0].Record["severity"])
	}
	if results[1].Matched {
		t.Errorf("results[1].Matched = true, want false")
	}
	// The handler must wrap the processor body in an envelope and add configId.
	if !strings.Contains(string(reqBody), `"processor"`) || !strings.Contains(string(reqBody), `"configId":"logs"`) {
		t.Errorf("request body missing envelope/configId: %s", reqBody)
	}
}

func TestHandler_Preview_FileError(t *testing.T) {
	h := newTestHandler(t, http.StatusOK, `{"results":[]}`, nil)
	if _, err := h.Preview(filepath.Join(t.TempDir(), "missing.json"), ""); err == nil {
		t.Fatal("Preview() expected error for missing file, got nil")
	}
}

func TestHandler_Preview_InvalidBody(t *testing.T) {
	h := newTestHandler(t, http.StatusOK, `{"results":[]}`, nil)
	// A non-object body is valid JSON but fails the envelope object check before
	// any HTTP call is made.
	path := writeProcessorFile(t, `["not","an","object"]`)
	if _, err := h.Preview(path, ""); err == nil {
		t.Fatal("Preview() expected error for non-object body, got nil")
	}
}

func TestHandler_Preview_ServerError(t *testing.T) {
	h := newTestHandler(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`, nil)
	path := writeProcessorFile(t, `{"type":"dql"}`)
	if _, err := h.Preview(path, ""); err == nil {
		t.Fatal("Preview() expected error for 500, got nil")
	}
}

func TestBuildEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		configID   string
		wantConfig string // expected value of configId ("" = key must be absent)
		wantErr    bool
	}{
		{
			name:       "wraps processor body without configId when flag empty",
			body:       `{"type":"dql","dqlScript":"fieldsAdd foo = 1"}`,
			configID:   "",
			wantConfig: "",
		},
		{
			name:       "wraps processor body and adds configId from flag",
			body:       `{"type":"dql","dqlScript":"fieldsAdd foo = 1"}`,
			configID:   "logs",
			wantConfig: "logs",
		},
		{
			name:     "non-object processor body is an error",
			body:     `["not","an","object"]`,
			configID: "logs",
			wantErr:  true,
		},
		{
			name:     "invalid JSON body is an error",
			body:     `{not json`,
			configID: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := buildEnvelope(json.RawMessage(tt.body), tt.configID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var env map[string]json.RawMessage
			if err := json.Unmarshal(out, &env); err != nil {
				t.Fatalf("envelope is not valid JSON: %v", err)
			}

			// The processor body must be embedded verbatim under "processor".
			gotProcessor, ok := env["processor"]
			if !ok {
				t.Fatalf("envelope missing \"processor\" key")
			}
			if !jsonEqual(t, gotProcessor, json.RawMessage(tt.body)) {
				t.Errorf("processor body altered:\n got: %s\nwant: %s", gotProcessor, tt.body)
			}

			// configId present iff the flag was set.
			rawID, hasID := env["configId"]
			if tt.wantConfig == "" {
				if hasID {
					t.Errorf("configId present but flag was empty: %s", rawID)
				}
				return
			}
			var id string
			if err := json.Unmarshal(rawID, &id); err != nil {
				t.Fatalf("configId is not a JSON string: %v", err)
			}
			if id != tt.wantConfig {
				t.Errorf("configId = %q, want %q", id, tt.wantConfig)
			}
		})
	}
}

func TestReadFileOrStdin(t *testing.T) {
	t.Run("reads a valid JSON file", func(t *testing.T) {
		body := `{"type":"dql","dqlScript":"fieldsAdd foo = 1"}`
		path := filepath.Join(t.TempDir(), "processor.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		got, err := readFileOrStdin(vfs.Env{}, path)
		if err != nil {
			t.Fatalf("readFileOrStdin() error = %v", err)
		}
		if !jsonEqual(t, got, json.RawMessage(body)) {
			t.Errorf("content = %s, want %s", got, body)
		}
	})

	t.Run("missing file is an error", func(t *testing.T) {
		if _, err := readFileOrStdin(vfs.Env{}, filepath.Join(t.TempDir(), "nope.json")); err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})

	t.Run("invalid JSON is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		if _, err := readFileOrStdin(vfs.Env{}, path); err == nil {
			t.Fatal("expected error for invalid JSON, got nil")
		}
	})
}

// jsonEqual reports whether two raw JSON documents are semantically equal.
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}

// A handler given a vfs.Env reads the processor definition through it.
func TestHandler_PreviewReadsTheDefinitionThroughItsEnv(t *testing.T) {
	var reqBody []byte
	h := newTestHandler(t, http.StatusOK, `{"results":[{"matched":true,"record":{"content":"x"}}]}`, &reqBody)
	h = h.WithVFS(vfs.Env{FS: vfs.NewMapFS(map[string][]byte{
		"processor.json": []byte(`{"type":"dql","matcher":"true","dqlScript":"fieldsAdd a = 1"}`),
	})})

	results, err := h.Preview("processor.json", "logs")
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if len(results) != 1 || !results[0].Matched {
		t.Errorf("results = %+v, want one match", results)
	}
	if !strings.Contains(string(reqBody), "fieldsAdd a = 1") {
		t.Errorf("request body %q lacks the definition from the env's file", reqBody)
	}
}
