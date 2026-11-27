package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/config"
)

// TestCreateNotebook_PreservesContentKeyOrder pins #804 for 'create': the
// uploaded content keeps the key order of the input file. Notebooks derives
// table column order from it, so re-sorting the keys reorders the columns.
func TestCreateNotebook_PreservesContentKeyOrder(t *testing.T) {
	const content = `{"version":"7","sections":[{"type":"dql","state":{"result":{"value":{` +
		`"records":[{"dt.entity.host":"HOST-1","avg":2.5,"max":4,"cpu":[1,4]}],` +
		`"types":[{"mappings":{"dt.entity.host":{"type":"string"},"avg":{"type":"double"},` +
		`"max":{"type":"double"},"cpu":{"type":"array"}}}]}}}}]}`

	var got string
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			return
		}
		if files := r.MultipartForm.File["content"]; len(files) == 1 {
			f, err := files[0].Open()
			if err == nil {
				b, _ := io.ReadAll(f)
				_ = f.Close()
				got = string(b)
			}
		}
		boundary := "resp-boundary"
		w.Header().Set("Content-Type", fmt.Sprintf("multipart/form-data; boundary=%s", boundary))
		fmt.Fprintf(w,
			"--%s\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n"+
				"{\"id\":\"nb-new\",\"name\":\"NB\",\"type\":\"notebook\",\"version\":1}\r\n--%s--\r\n",
			boundary, boundary)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	setupDocumentCmdTest(t, srv.URL, config.SafetyLevelReadWriteAll)

	file := filepath.Join(t.TempDir(), "notebook.json")
	if err := os.WriteFile(file, []byte(`{"name":"NB","type":"notebook","content":`+content+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for flag, value := range map[string]string{"file": file, "type": "notebook"} {
		if err := createDocumentCmd.Flags().Set(flag, value); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	t.Cleanup(func() {
		for _, flag := range []string{"file", "type"} {
			_ = createDocumentCmd.Flags().Set(flag, "")
		}
	})

	captureDocStderr(t, func() {
		if err := createDocumentCmd.RunE(createDocumentCmd, nil); err != nil {
			t.Fatalf("create document failed: %v", err)
		}
	})

	if got != content {
		t.Errorf("uploaded content =\n%s\nwant\n%s", got, content)
	}
}
