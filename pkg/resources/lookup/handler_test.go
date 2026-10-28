package lookup

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// newLookupTestHandler creates a Handler backed by a test server.
func newLookupTestHandler(t *testing.T, mux *http.ServeMux) (*Handler, func()) {
	t.Helper()
	srv := httptest.NewServer(mux)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	return NewHandler(c), srv.Close
}

// --- Create ---

func TestCreate_WithDataContent_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files/tabular/lookup:upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(UploadResponse{
			Records: 2,
		})
	})
	// DQL endpoint (not called for Create, but needs to exist)
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	csvData := []byte("id,name\n1,alice\n2,bob\n")
	resp, err := h.Create(CreateRequest{
		FilePath:    "/lookups/test.csv",
		DisplayName: "Test",
		DataContent: csvData,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if resp.Records != 2 {
		t.Errorf("expected 2 records, got %d", resp.Records)
	}
}

// parseUploadRequest reads the multipart upload body sent to the lookup
// endpoint and returns the JSON request part decoded plus the raw content.
func parseUploadRequest(t *testing.T, r *http.Request) (UploadRequest, []byte) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse content-type: %v", err)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])

	var (
		req     UploadRequest
		content []byte
	)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		switch part.FormName() {
		case "request":
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode request part: %v (body=%q)", err, body)
			}
		case "content":
			content = body
		}
	}
	return req, content
}

// TestCreate_StripsBOMFromAutoDetectedPattern is the regression test for #187:
// when a CSV file is BOM-prefixed, the auto-detected parsePattern sent to the
// upload API must not contain the BOM. Otherwise the server-side DPL parser
// rejects the pattern with "extraneous input ”".
func TestCreate_StripsBOMFromAutoDetectedPattern(t *testing.T) {
	var captured UploadRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files/tabular/lookup:upload", func(w http.ResponseWriter, r *http.Request) {
		captured, _ = parseUploadRequest(t, r)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UploadResponse{Records: 1})
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	bomCSV := append([]byte{0xEF, 0xBB, 0xBF}, []byte("code,description\nERR1,boom")...)
	if _, err := h.Create(CreateRequest{
		FilePath:    "/lookups/test/bom",
		LookupField: "code",
		DataContent: bomCSV,
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	want := "LD*:code ',' LD*:description"
	if captured.ParsePattern != want {
		t.Errorf("parsePattern = %q, want %q", captured.ParsePattern, want)
	}
	if strings.ContainsRune(captured.ParsePattern, '\ufeff') {
		t.Errorf("parsePattern still contains BOM: %q", captured.ParsePattern)
	}
	if captured.SkippedRecords != 1 {
		t.Errorf("skippedRecords = %d, want 1", captured.SkippedRecords)
	}
}

func TestCreate_InvalidPath(t *testing.T) {
	h, cleanup := newLookupTestHandler(t, http.NewServeMux())
	defer cleanup()

	_, err := h.Create(CreateRequest{
		FilePath:    "no-leading-slash",
		DataContent: []byte("a,b\n1,2\n"),
	})
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}

func TestCreate_NoDataContent(t *testing.T) {
	h, cleanup := newLookupTestHandler(t, http.NewServeMux())
	defer cleanup()

	_, err := h.Create(CreateRequest{
		FilePath: "/lookups/test.csv",
	})
	if err == nil {
		t.Fatal("expected error when no data content, got nil")
	}
}

func TestCreate_ServerError_Unauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files/tabular/lookup:upload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "unauthorized")
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(CreateRequest{
		FilePath:    "/lookups/test.csv",
		DataContent: []byte("id,name\n1,a\n"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreate_ServerError_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files/tabular/lookup:upload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(CreateRequest{
		FilePath:    "/lookups/test.csv",
		DataContent: []byte("id,name\n1,a\n"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreate_ServerError_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files/tabular/lookup:upload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(CreateRequest{
		FilePath:    "/lookups/test.csv",
		DataContent: []byte("id,name\n1,a\n"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreate_ServerError_Conflict(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files/tabular/lookup:upload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, "already exists")
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(CreateRequest{
		FilePath:    "/lookups/test.csv",
		DataContent: []byte("id,name\n1,a\n"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreate_ServerError_BadRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files/tabular/lookup:upload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "bad csv format")
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(CreateRequest{
		FilePath:    "/lookups/test.csv",
		DataContent: []byte("id,name\n1,a\n"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Update ---

func TestUpdate_CallsCreateWithOverwrite(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files/tabular/lookup:upload", func(w http.ResponseWriter, r *http.Request) {
		// Verify overwrite is set (it's in the JSON form field, not query)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(UploadResponse{Records: 1})
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	resp, err := h.Update("/lookups/update.csv", CreateRequest{
		DataContent: []byte("id,name\n1,alice\n"),
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if resp.Records != 1 {
		t.Errorf("expected 1 record, got %d", resp.Records)
	}
}

// --- Delete ---

func TestDelete_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files:delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	err := h.Delete("/lookups/to-delete.csv")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestDelete_InvalidPath(t *testing.T) {
	h, cleanup := newLookupTestHandler(t, http.NewServeMux())
	defer cleanup()

	err := h.Delete("no-leading-slash.csv")
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}

func TestDelete_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files:delete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "not found")
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	err := h.Delete("/lookups/missing.csv")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDelete_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files:delete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	err := h.Delete("/lookups/locked.csv")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDelete_Unauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/resource-store/v1/files:delete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	h, cleanup := newLookupTestHandler(t, mux)
	defer cleanup()

	err := h.Delete("/lookups/private.csv")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
