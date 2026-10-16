package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
	"github.com/dynatrace-oss/dtctl/pkg/client"
)

const matcherVerifyPath = "/platform/openpipeline/v1/matcher/verify"

// pinVerifyGlobals sets the process-global command state to a known baseline for
// the openpipeline verify/preview command tests (which read package-level globals
// and the root "output" persistent flag) and returns a function that restores the
// previous values. Isolating these prevents state leaking between tests in the
// shared cmd package from flipping the human-vs-structured output branch.
func pinVerifyGlobals(t *testing.T, configPath string) (restore func()) {
	t.Helper()
	origCfg, origPlain, origJQ := cfgFile(context.Background()), plainMode(context.Background()), jqFilter(context.Background())
	origAgent, origFormat := agentMode(context.Background()), outputFormat(context.Background())

	outputFlag := rootCmd.PersistentFlags().Lookup("output")
	origOutputChanged := false
	if outputFlag != nil {
		origOutputChanged = outputFlag.Changed
	}

	gFlags.cfgFile = configPath
	gFlags.plainMode = true
	gFlags.jqFilter = ""
	gFlags.agentMode = false
	gFlags.outputFormat = "table"
	if outputFlag != nil {
		outputFlag.Changed = false
	}

	return func() {
		gFlags.cfgFile, gFlags.plainMode, gFlags.jqFilter = origCfg, origPlain, origJQ
		gFlags.agentMode, gFlags.outputFormat = origAgent, origFormat
		if outputFlag != nil {
			outputFlag.Changed = origOutputChanged
		}
	}
}

// setupVerifyMatcherTest wires a mock server for the matcher verify endpoint and
// points the command at a throwaway config. It restores the mutated globals and
// command flags on cleanup. The handler returns the given raw JSON response.
func setupVerifyMatcherTest(t *testing.T, response string) {
	t.Helper()
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		matcherVerifyPath: func(w http.ResponseWriter, _ *http.Request) {
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
		testutil.ResetCommandFlags(verifyOpenPipelineMatcherCmd)
	})
	testutil.ResetCommandFlags(verifyOpenPipelineMatcherCmd)
}

func TestVerifyOpenPipelineMatcherCmd_Valid(t *testing.T) {
	setupVerifyMatcherTest(t, `{"valid":true,"notifications":[]}`)

	err := verifyOpenPipelineMatcherCmd.RunE(verifyOpenPipelineMatcherCmd, []string{`matchesValue(content, "error")`})
	if err != nil {
		t.Fatalf("RunE() error = %v, want nil for valid matcher", err)
	}
}

func TestVerifyOpenPipelineMatcherCmd_Invalid(t *testing.T) {
	setupVerifyMatcherTest(t, `{"valid":false,"notifications":[{"severity":"ERROR","message":"bad"}]}`)

	err := verifyOpenPipelineMatcherCmd.RunE(verifyOpenPipelineMatcherCmd, []string{"!@#$"})
	// An invalid verdict must exit non-zero via silentExitError.
	var silent *silentExitError
	if !errors.As(err, &silent) {
		t.Fatalf("RunE() error = %v, want *silentExitError", err)
	}
	if silent.code != client.ExitError {
		t.Errorf("exit code = %d, want %d", silent.code, client.ExitError)
	}
}

func TestVerifyOpenPipelineMatcherCmd_JSONOutput(t *testing.T) {
	setupVerifyMatcherTest(t, `{"valid":true,"notifications":[]}`)

	// An explicit -o json routes through the printer instead of the human
	// summary. Flip the global format and the root flag's Changed bit; both are
	// restored by the setup helper's cleanup.
	gFlags.outputFormat = "json"
	rootCmd.PersistentFlags().Lookup("output").Changed = true

	var runErr error
	out := captureStdout(t, func() {
		runErr = verifyOpenPipelineMatcherCmd.RunE(verifyOpenPipelineMatcherCmd, []string{`matchesValue(content, "x")`})
	})
	if runErr != nil {
		t.Fatalf("RunE() error = %v", runErr)
	}
	if !strings.Contains(out, `"valid": true`) {
		t.Errorf("stdout missing JSON valid field:\n%s", out)
	}
}

func TestVerifyOpenPipelineMatcherCmd_UnsupportedOutputFormat(t *testing.T) {
	setupVerifyMatcherTest(t, `{"valid":true,"notifications":[]}`)

	// csv/wide are rejected for the verify family (same as "verify query"),
	// so the command errors before making a request.
	gFlags.outputFormat = "csv"
	rootCmd.PersistentFlags().Lookup("output").Changed = true

	err := verifyOpenPipelineMatcherCmd.RunE(verifyOpenPipelineMatcherCmd, []string{`matchesValue(content, "x")`})
	if err == nil {
		t.Fatal("RunE() error = nil, want error for unsupported output format")
	}
	if !strings.Contains(err.Error(), "unsupported output format") {
		t.Errorf("error = %q, want it to mention unsupported output format", err.Error())
	}
}

func TestVerifyOpenPipelineMatcherCmd_ServerError(t *testing.T) {
	// Point the command at a config whose server always 500s.
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		matcherVerifyPath: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
		},
	})
	t.Cleanup(ms.Close)
	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	t.Cleanup(cleanup)
	restore := pinVerifyGlobals(t, configPath)
	t.Cleanup(func() {
		restore()
		testutil.ResetCommandFlags(verifyOpenPipelineMatcherCmd)
	})
	testutil.ResetCommandFlags(verifyOpenPipelineMatcherCmd)

	err := verifyOpenPipelineMatcherCmd.RunE(verifyOpenPipelineMatcherCmd, []string{"expr"})
	if err == nil {
		t.Fatal("RunE() error = nil, want error for server 500")
	}
}

func TestVerifyOpenPipelineMatcherCmd_FromFile(t *testing.T) {
	setupVerifyMatcherTest(t, `{"valid":true}`)

	path := filepath.Join(t.TempDir(), "matcher.dql")
	if err := os.WriteFile(path, []byte(`matchesValue(content, "error")`), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	_ = verifyOpenPipelineMatcherCmd.Flags().Set("file", path)

	if err := verifyOpenPipelineMatcherCmd.RunE(verifyOpenPipelineMatcherCmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
}

func TestVerifyOpenPipelineMatcherCmd_ArgAndFileConflict(t *testing.T) {
	setupVerifyMatcherTest(t, `{"valid":true}`)
	_ = verifyOpenPipelineMatcherCmd.Flags().Set("file", "matcher.dql")

	err := verifyOpenPipelineMatcherCmd.RunE(verifyOpenPipelineMatcherCmd, []string{"expr"})
	if err == nil {
		t.Fatal("RunE() error = nil, want conflict error for arg + --file")
	}
}

func TestVerifyOpenPipelineMatcherCmd_NoInput(t *testing.T) {
	setupVerifyMatcherTest(t, `{"valid":true}`)

	err := verifyOpenPipelineMatcherCmd.RunE(verifyOpenPipelineMatcherCmd, nil)
	if err == nil {
		t.Fatal("RunE() error = nil, want error for missing matcher expression")
	}
}

func TestVerifyOpenPipelineMatcherCmd_MissingFile(t *testing.T) {
	setupVerifyMatcherTest(t, `{"valid":true}`)
	_ = verifyOpenPipelineMatcherCmd.Flags().Set("file", filepath.Join(t.TempDir(), "nope.dql"))

	err := verifyOpenPipelineMatcherCmd.RunE(verifyOpenPipelineMatcherCmd, nil)
	if err == nil {
		t.Fatal("RunE() error = nil, want error for missing --file")
	}
}

func TestReadVerifyExpressionFromFile(t *testing.T) {
	t.Run("reads file content", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "expr.dql")
		if err := os.WriteFile(path, []byte("some expression\n"), 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		got, err := readVerifyExpressionFromFile(context.Background(), path)
		if err != nil {
			t.Fatalf("readVerifyExpressionFromFile() error = %v", err)
		}
		if got != "some expression\n" {
			t.Errorf("content = %q, want %q", got, "some expression\n")
		}
	})

	t.Run("missing file is an error", func(t *testing.T) {
		if _, err := readVerifyExpressionFromFile(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}
