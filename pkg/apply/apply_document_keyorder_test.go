package apply

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// orderedContent is notebook content whose keys are deliberately not in
// alphabetical order, as in a stored DQL result. Apps derive behaviour (such as
// table column order) from this order, so apply must send it unchanged.
const orderedContent = `{"version":"7","sections":[{"type":"dql","state":{"result":{"value":{` +
	`"records":[{"dt.entity.host":"HOST-1","avg":2.5,"max":4,"cpu":[1,4]}],` +
	`"types":[{"mappings":{"dt.entity.host":{"type":"string"},"avg":{"type":"double"},` +
	`"max":{"type":"double"},"cpu":{"type":"array"}}}]}}}}]}`

// readContentPart returns the "content" file part of a multipart document request.
func readContentPart(t *testing.T, r *http.Request) string {
	t.Helper()
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	files := r.MultipartForm.File["content"]
	if len(files) != 1 {
		t.Fatalf("got %d content parts, want 1", len(files))
	}
	f, err := files[0].Open()
	if err != nil {
		t.Fatalf("open content part: %v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read content part: %v", err)
	}
	return string(b)
}

// keyOrderPayloads wraps orderedContent in every input shape applyDocument accepts.
func keyOrderPayloads(id string) map[string]string {
	idField := ""
	if id != "" {
		idField = fmt.Sprintf(`"id":%q,`, id)
	}
	// Direct content: the document fields sit next to the content fields, so
	// the whole (compacted) payload is sent.
	direct := `{` + idField + `"type":"notebook","name":"NB",` + orderedContent[1:]
	return map[string]string{
		"nested content": `{` + idField + `"type":"notebook","name":"NB","content":` + orderedContent + `}`,
		"double-nested content": `{` + idField + `"type":"notebook","name":"NB","content":{"content":` +
			orderedContent + `}}`,
		"pretty-printed content": `{` + idField + `"type":"notebook","name":"NB","content":` + "\n" +
			indentJSON(orderedContent) + "\n}",
		"direct content": direct,
	}
}

func indentJSON(s string) string {
	var v json.RawMessage = []byte(s)
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func wantKeyOrderContent(shape, payload string) string {
	if shape == "direct content" {
		return string(compactJSON([]byte(payload)))
	}
	return orderedContent
}

func TestApply_DocumentCreate_PreservesContentKeyOrder(t *testing.T) {
	for shape, payload := range keyOrderPayloads("") {
		t.Run(shape, func(t *testing.T) {
			var got string
			srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
				"/platform/document/v1/documents": func(w http.ResponseWriter, r *http.Request) {
					got = readContentPart(t, r)
					boundary := "resp-boundary"
					w.Header().Set("Content-Type", fmt.Sprintf("multipart/form-data; boundary=%s", boundary))
					fmt.Fprintf(w,
						"--%s\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n"+
							"{\"id\":\"nb-new\",\"name\":\"NB\",\"type\":\"notebook\",\"version\":1}\r\n--%s--\r\n",
						boundary, boundary)
				},
				"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				},
			})
			defer srv.Close()

			if _, err := NewApplier(c).Apply([]byte(payload), ApplyOptions{}); err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if want := wantKeyOrderContent(shape, payload); got != want {
				t.Errorf("uploaded content =\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func TestApply_DocumentUpdate_PreservesContentKeyOrder(t *testing.T) {
	for shape, payload := range keyOrderPayloads("nb-1") {
		for _, overrideID := range []string{"", "nb-1"} {
			t.Run(fmt.Sprintf("%s/override id %q", shape, overrideID), func(t *testing.T) {
				var got string
				srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
					"/platform/document/v1/documents/nb-1/metadata": func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(map[string]interface{}{
							"id": "nb-1", "name": "NB", "type": "notebook", "version": 3,
						})
					},
					"/platform/document/v1/documents/nb-1": func(w http.ResponseWriter, r *http.Request) {
						got = readContentPart(t, r)
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(map[string]interface{}{
							"id": "nb-1", "name": "NB", "type": "notebook", "version": 4,
						})
					},
					"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusUnauthorized)
					},
				})
				defer srv.Close()

				if _, err := NewApplier(c).Apply([]byte(payload), ApplyOptions{OverrideID: overrideID}); err != nil {
					t.Fatalf("Apply() error = %v", err)
				}
				want := wantKeyOrderContent(shape, payload)
				if shape == "direct content" && overrideID != "" {
					// --id rewrites the payload with the id field first.
					want = strings.Replace(want, `"id":"nb-1",`, "", 1)
					want = `{"id":"nb-1",` + want[1:]
				}
				if got != want {
					t.Errorf("uploaded content =\n%s\nwant\n%s", got, want)
				}
			})
		}
	}
}
