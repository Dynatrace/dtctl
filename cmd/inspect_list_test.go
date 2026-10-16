package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/inspect"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

func TestRunInspectList_RejectsFileArg(t *testing.T) {
	c := newInspectTestCmd(t, "--list")
	err := runInspectList(c, []string{"q-x.jsonl"})
	if ie, ok := err.(*inspect.Error); !ok || ie.Code != "inspect_bad_flags" {
		t.Fatalf("err = %v, want inspect_bad_flags for --list with a file arg", err)
	}
}

func TestRunInspectList_RejectsPrimitiveCombination(t *testing.T) {
	for _, argv := range [][]string{
		{"--list", "--head", "5"},
		{"--list", "--schema"},
		{"--list", "--fields", "a,b"},
		{"--list", "--page", "--offset", "10"},
	} {
		c := newInspectTestCmd(t, argv...)
		err := runInspectList(c, nil)
		if ie, ok := err.(*inspect.Error); !ok || ie.Code != "inspect_bad_flags" {
			t.Fatalf("argv %v: err = %v, want inspect_bad_flags", argv, err)
		}
	}
}

func TestRunInspectList_EmitsFileListEnvelope(t *testing.T) {
	// A user-chosen spill dir (managed=false) keeps the test independent of the
	// machine's real context/cache: the listing targets <dir>/results directly.
	base := t.TempDir()
	t.Setenv("DTCTL_SPILL_DIR", base)
	resultsDir := filepath.Join(base, "results")
	if err := os.MkdirAll(resultsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	spill := filepath.Join(resultsDir, "q-1234.jsonl")
	if err := os.WriteFile(spill, []byte("{\"a\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := output.WriteSidecar(spill, &output.SidecarManifest{
		Format: "jsonl", Query: "fetch logs", Rows: 1,
	}); err != nil {
		t.Fatal(err)
	}

	origAgent, origFormat, origJQ := agentMode(context.Background()), outputFormat(context.Background()), jqFilter(context.Background())
	defer func() { gFlags.agentMode, gFlags.outputFormat, gFlags.jqFilter = origAgent, origFormat, origJQ }()
	gFlags.agentMode, gFlags.outputFormat, gFlags.jqFilter = true, "json", ""

	var runErr error
	out := captureStdout(t, func() {
		c := newInspectTestCmd(t, "--list")
		runErr = runInspectList(c, nil)
	})
	if runErr != nil {
		t.Fatalf("runInspectList: %v", runErr)
	}

	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Kind  string `json:"kind"`
			Files []struct {
				Path  string `json:"path"`
				Query string `json:"query"`
			} `json:"files"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("envelope not valid JSON: %v\n%s", err, out)
	}
	if !resp.OK || resp.Result.Kind != output.KindFileList {
		t.Fatalf("want ok file-list envelope, got %+v", resp)
	}
	if len(resp.Result.Files) != 1 || resp.Result.Files[0].Query != "fetch logs" {
		t.Fatalf("want one listed file with provenance, got %+v", resp.Result.Files)
	}
}
