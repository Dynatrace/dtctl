package apply

import (
	"os"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

func TestInjectIDIntoFileContent(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		id          string
		wantNil     bool   // expect no-op (already has id)
		wantContain string // substring the result must contain
		wantFirst   bool   // id line should be near the top
	}{
		// ── YAML ──────────────────────────────────────────────────────────────────
		{
			name: "yaml: injects id as first content line",
			content: `name: My Dashboard
type: dashboard
content:
  tiles: []
`,
			id:          "abc-123",
			wantContain: `id: abc-123`,
			wantFirst:   true,
		},
		{
			name: "yaml: no-op when id already present",
			content: `id: existing-id
name: My Dashboard
`,
			id:      "new-id",
			wantNil: true,
		},
		{
			name: "yaml: preserves leading comments",
			content: `# This is a comment
# Another comment
name: My Workflow
`,
			id:          "wf-456",
			wantContain: `id: wf-456`,
		},
		{
			name: "yaml: empty file gets id prepended",
			content: `name: Minimal
`,
			id:          "min-001",
			wantContain: `id: min-001`,
		},
		// ── JSON ──────────────────────────────────────────────────────────────────
		{
			name:        "json: injects id as first key",
			content:     "{\n  \"name\": \"My Dashboard\",\n  \"type\": \"dashboard\"\n}\n",
			id:          "dash-789",
			wantContain: `"id": "dash-789"`,
		},
		{
			name: "json: no-op when id already present",
			content: `{
  "id": "existing",
  "name": "test"
}
`,
			id:      "new-id",
			wantNil: true,
		},
		{
			name:        "json: handles compact object",
			content:     `{"name":"test","type":"dashboard"}`,
			id:          "compact-1",
			wantContain: `"id": "compact-1"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := injectIDIntoFileContent([]byte(tt.content), tt.id)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if result != nil {
					t.Errorf("expected nil (no-op), got:\n%s", result)
				}
				return
			}

			if result == nil {
				t.Fatal("expected non-nil result, got nil")
			}

			out := string(result)
			if !strings.Contains(out, tt.wantContain) {
				t.Errorf("result missing %q:\n%s", tt.wantContain, out)
			}

			if tt.wantFirst {
				// The id line should appear before any other key
				idIdx := strings.Index(out, tt.id)
				nameIdx := strings.Index(out, "name:")
				if nameIdx >= 0 && idIdx > nameIdx {
					t.Errorf("id line appears after 'name:' line; full output:\n%s", out)
				}
			}
		})
	}
}

func TestWriteIDToFile_roundtrip(t *testing.T) {
	t.Run("yaml file gets id stamped and re-read correctly", func(t *testing.T) {
		dir := t.TempDir()
		f := dir + "/dashboard.yaml"

		original := `name: Test Dashboard
type: dashboard
content:
  tiles: []
`
		if err := writeFileForTest(f, original); err != nil {
			t.Fatal(err)
		}

		if err := (&Applier{}).writeIDToFile(f, "dash-abc-123"); err != nil {
			t.Fatalf("writeIDToFile: %v", err)
		}

		got := readFileForTest(t, f)
		if !strings.Contains(got, `id: dash-abc-123`) {
			t.Errorf("id not found in file:\n%s", got)
		}
		if !strings.Contains(got, "name: Test Dashboard") {
			t.Errorf("original content lost:\n%s", got)
		}

		// Running again must be a no-op (file already has id)
		if err := (&Applier{}).writeIDToFile(f, "different-id"); err != nil {
			t.Fatalf("second writeIDToFile: %v", err)
		}
		got2 := readFileForTest(t, f)
		if strings.Contains(got2, "different-id") {
			t.Error("second call should not overwrite existing id")
		}
	})

	t.Run("json file gets id stamped", func(t *testing.T) {
		dir := t.TempDir()
		f := dir + "/workflow.json"

		original := "{\n  \"name\": \"My Workflow\",\n  \"type\": \"workflow\"\n}\n"
		if err := writeFileForTest(f, original); err != nil {
			t.Fatal(err)
		}

		if err := (&Applier{}).writeIDToFile(f, "wf-xyz-999"); err != nil {
			t.Fatalf("writeIDToFile: %v", err)
		}

		got := readFileForTest(t, f)
		if !strings.Contains(got, `"id": "wf-xyz-999"`) {
			t.Errorf("id not found in file:\n%s", got)
		}
	})

	t.Run("empty filename returns error", func(t *testing.T) {
		err := (&Applier{}).writeIDToFile("", "some-id")
		if err == nil {
			t.Error("expected error for empty filename")
		}
	})

	t.Run("nonexistent file returns error", func(t *testing.T) {
		err := (&Applier{}).writeIDToFile("/nonexistent/path/file.yaml", "some-id")
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})
}

func TestInjectID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		id      string
		wantID  string
		wantErr bool
	}{
		{
			name:   "injects id into object without id",
			input:  `{"name":"test","type":"dashboard"}`,
			id:     "new-id",
			wantID: "new-id",
		},
		{
			name:   "overwrites existing id",
			input:  `{"id":"old-id","name":"test"}`,
			id:     "new-id",
			wantID: "new-id",
		},
		{
			name:    "invalid json returns error",
			input:   `{not json}`,
			id:      "some-id",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := injectID([]byte(tt.input), tt.id)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(string(result), `"`+tt.wantID+`"`) {
				t.Errorf("want id %q in output, got: %s", tt.wantID, result)
			}
		})
	}
}

// helpers

func writeFileForTest(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// An applier given a vfs.Env writes ids back through it: into the invocation's
// own filesystem, and not onto the host's disk.
func TestWriteIDToFile_UsesTheAppliersEnv(t *testing.T) {
	fs := vfs.NewMapFS(map[string][]byte{"dash.yaml": []byte("name: My Dashboard\ntype: dashboard\n")})
	a := (&Applier{}).WithVFS(vfs.Env{FS: fs})

	if err := a.writeIDToFile("dash.yaml", "abc-123"); err != nil {
		t.Fatalf("writeIDToFile: %v", err)
	}
	if got := string(fs.Files()["dash.yaml"]); !strings.Contains(got, "id: abc-123") {
		t.Errorf("the env's file = %q, want the id written back", got)
	}
	if _, err := os.Stat("dash.yaml"); err == nil {
		t.Error("the id write-back created a file on the host's disk")
	}
}
