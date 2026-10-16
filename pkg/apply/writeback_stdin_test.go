package apply

import (
	"io"
	"os"
	"testing"
)

// TestApplyWriteBack_StdinSourceIsNeverAFile: input piped via `apply -f -` is
// named StdinSourceFile. There is no file behind it, so no ID is written and
// the recovery hint (which suggests re-running with that name as -f) is not
// printed either.
func TestApplyWriteBack_StdinSourceIsNeverAFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	for name, writeID := range map[string]bool{"hint": false, "write-id": true} {
		t.Run(name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			orig := os.Stderr
			os.Stderr = w
			var warnings []string
			(&Applier{}).applyWriteBack(StdinSourceFile, "wf-1", "workflow", writeID, false, &warnings)
			os.Stderr = orig
			_ = w.Close()
			out, _ := io.ReadAll(r)

			if len(out) != 0 {
				t.Errorf("expected no stderr output for stdin input, got %q", out)
			}
			if len(warnings) != 0 {
				t.Errorf("expected no warnings, got %v", warnings)
			}
			// "<stdin>" is not even a valid name on Windows, so check the
			// directory rather than stat-ing the name.
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("expected no file to be written, found %v", entries)
			}
		})
	}
}
