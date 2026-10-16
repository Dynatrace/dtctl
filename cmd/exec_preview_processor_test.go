package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
)

const previewProcessorPath = "/platform/openpipeline/v1/preview/processor"

// setupExecPreviewTest wires a mock server for the preview endpoint and points
// the command at a throwaway config, restoring mutated globals and flags on
// cleanup.
func setupExecPreviewTest(t *testing.T, response string) {
	t.Helper()
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		previewProcessorPath: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(response))
		},
	})
	t.Cleanup(ms.Close)

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	t.Cleanup(cleanup)

	restore := pinVerifyGlobals(t, configPath)
	t.Cleanup(func() {
		restore()
		testutil.ResetCommandFlags(execPreviewProcessorCmd)
	})
	testutil.ResetCommandFlags(execPreviewProcessorCmd)
}

func writeProcessorFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "processor.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestExecPreviewProcessorCmd_Success(t *testing.T) {
	setupExecPreviewTest(t, `{"results":[{"matched":true,"record":{"content":"hi"}}]}`)

	path := writeProcessorFile(t, `{"type":"dql","matcher":"true","dqlScript":"fieldsAdd x = 1"}`)
	_ = execPreviewProcessorCmd.Flags().Set("file", path)

	var runErr error
	out := captureStdout(t, func() {
		runErr = execPreviewProcessorCmd.RunE(execPreviewProcessorCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("RunE() error = %v", runErr)
	}
	// Default output is an indented JSON array of results.
	if !strings.Contains(out, `"matched": true`) {
		t.Errorf("stdout missing matched result:\n%s", out)
	}
}

func TestExecPreviewProcessorCmd_JSONOutput(t *testing.T) {
	setupExecPreviewTest(t, `{"results":[{"matched":true,"record":{"content":"hi"}}]}`)

	path := writeProcessorFile(t, `{"type":"dql","matcher":"true","dqlScript":"fieldsAdd x = 1"}`)
	_ = execPreviewProcessorCmd.Flags().Set("file", path)

	// An explicit -o json delegates to the printer's PrintList instead of the
	// default indented-JSON path.
	gFlags.outputFormat = "json"
	rootCmd.PersistentFlags().Lookup("output").Changed = true

	var runErr error
	out := captureStdout(t, func() {
		runErr = execPreviewProcessorCmd.RunE(execPreviewProcessorCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("RunE() error = %v", runErr)
	}
	if !strings.Contains(out, `"matched"`) {
		t.Errorf("stdout missing matched field:\n%s", out)
	}
}

// TestExecPreviewProcessorCmd_FileRequired: --file is enforced at parse time
// (marked required, empty rejected), so RunE no longer checks it.
func TestExecPreviewProcessorCmd_FileRequired(t *testing.T) {
	f := execPreviewProcessorCmd.Flags().Lookup("file")
	if got := f.Annotations[cobra.BashCompOneRequiredFlag]; len(got) == 0 || got[0] != "true" {
		t.Fatalf("--file annotations = %v, want it marked required", f.Annotations)
	}
}

func TestExecPreviewProcessorCmd_MissingFile(t *testing.T) {
	setupExecPreviewTest(t, `{"results":[]}`)
	_ = execPreviewProcessorCmd.Flags().Set("file", filepath.Join(t.TempDir(), "nope.json"))

	err := execPreviewProcessorCmd.RunE(execPreviewProcessorCmd, nil)
	if err == nil {
		t.Fatal("RunE() error = nil, want error for missing file")
	}
}
