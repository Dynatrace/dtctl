package cmd

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
	"github.com/dynatrace-oss/dtctl/pkg/client"
)

const dqlProcessorVerifyPath = "/platform/openpipeline/v1/dqlProcessor/verify"

// setupVerifyDQLProcessorTest wires a mock server for the DQL processor verify
// endpoint and points the command at a throwaway config, restoring mutated
// globals and command flags on cleanup.
func setupVerifyDQLProcessorTest(t *testing.T, response string) {
	t.Helper()
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		dqlProcessorVerifyPath: func(w http.ResponseWriter, _ *http.Request) {
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
		testutil.ResetCommandFlags(verifyOpenPipelineDQLProcessorCmd)
	})
	testutil.ResetCommandFlags(verifyOpenPipelineDQLProcessorCmd)
}

func TestVerifyOpenPipelineDQLProcessorCmd_Valid(t *testing.T) {
	setupVerifyDQLProcessorTest(t, `{"valid":true,"notifications":[]}`)

	err := verifyOpenPipelineDQLProcessorCmd.RunE(verifyOpenPipelineDQLProcessorCmd, []string{`fieldsAdd severity = "INFO"`})
	if err != nil {
		t.Fatalf("RunE() error = %v, want nil for valid script", err)
	}
}

func TestVerifyOpenPipelineDQLProcessorCmd_Invalid(t *testing.T) {
	setupVerifyDQLProcessorTest(t, `{"valid":false,"notifications":[{"severity":"ERROR","message":"bad"}]}`)

	err := verifyOpenPipelineDQLProcessorCmd.RunE(verifyOpenPipelineDQLProcessorCmd, []string{"parsee content"})
	var silent *silentExitError
	if !errors.As(err, &silent) {
		t.Fatalf("RunE() error = %v, want *silentExitError", err)
	}
	if silent.code != client.ExitError {
		t.Errorf("exit code = %d, want %d", silent.code, client.ExitError)
	}
}

func TestVerifyOpenPipelineDQLProcessorCmd_FromFile(t *testing.T) {
	setupVerifyDQLProcessorTest(t, `{"valid":true}`)

	path := filepath.Join(t.TempDir(), "processor.dql")
	if err := os.WriteFile(path, []byte(`fieldsAdd x = 1`), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	_ = verifyOpenPipelineDQLProcessorCmd.Flags().Set("file", path)

	if err := verifyOpenPipelineDQLProcessorCmd.RunE(verifyOpenPipelineDQLProcessorCmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
}

func TestVerifyOpenPipelineDQLProcessorCmd_JSONOutput(t *testing.T) {
	setupVerifyDQLProcessorTest(t, `{"valid":true,"notifications":[]}`)

	gFlags.outputFormat = "json"
	rootCmd.PersistentFlags().Lookup("output").Changed = true

	var runErr error
	out := captureStdout(t, func() {
		runErr = verifyOpenPipelineDQLProcessorCmd.RunE(verifyOpenPipelineDQLProcessorCmd, []string{"fieldsAdd x = 1"})
	})
	if runErr != nil {
		t.Fatalf("RunE() error = %v", runErr)
	}
	if !strings.Contains(out, `"valid": true`) {
		t.Errorf("stdout missing JSON valid field:\n%s", out)
	}
}

func TestVerifyOpenPipelineDQLProcessorCmd_ServerError(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		dqlProcessorVerifyPath: func(w http.ResponseWriter, _ *http.Request) {
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
		testutil.ResetCommandFlags(verifyOpenPipelineDQLProcessorCmd)
	})
	testutil.ResetCommandFlags(verifyOpenPipelineDQLProcessorCmd)

	err := verifyOpenPipelineDQLProcessorCmd.RunE(verifyOpenPipelineDQLProcessorCmd, []string{"fieldsAdd x = 1"})
	if err == nil {
		t.Fatal("RunE() error = nil, want error for server 500")
	}
}

func TestVerifyOpenPipelineDQLProcessorCmd_MissingFile(t *testing.T) {
	setupVerifyDQLProcessorTest(t, `{"valid":true}`)
	_ = verifyOpenPipelineDQLProcessorCmd.Flags().Set("file", filepath.Join(t.TempDir(), "nope.dql"))

	err := verifyOpenPipelineDQLProcessorCmd.RunE(verifyOpenPipelineDQLProcessorCmd, nil)
	if err == nil {
		t.Fatal("RunE() error = nil, want error for missing --file")
	}
}

func TestVerifyOpenPipelineDQLProcessorCmd_ArgAndFileConflict(t *testing.T) {
	setupVerifyDQLProcessorTest(t, `{"valid":true}`)
	_ = verifyOpenPipelineDQLProcessorCmd.Flags().Set("file", "processor.dql")

	err := verifyOpenPipelineDQLProcessorCmd.RunE(verifyOpenPipelineDQLProcessorCmd, []string{"fieldsAdd x = 1"})
	if err == nil {
		t.Fatal("RunE() error = nil, want conflict error for arg + --file")
	}
}

func TestVerifyOpenPipelineDQLProcessorCmd_NoInput(t *testing.T) {
	setupVerifyDQLProcessorTest(t, `{"valid":true}`)

	err := verifyOpenPipelineDQLProcessorCmd.RunE(verifyOpenPipelineDQLProcessorCmd, nil)
	if err == nil {
		t.Fatal("RunE() error = nil, want error for missing DQL script")
	}
}
