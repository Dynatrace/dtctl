package testutil

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dynatrace/dtctl/pkg/config"
)

// SetupTestConfig creates a temporary config file for testing and returns the config path and cleanup function
func SetupTestConfig(t *testing.T, serverURL string) (configPath string, cleanup func()) {
	t.Helper()

	tmpDir := t.TempDir()
	configPath = filepath.Join(tmpDir, "config")

	cfg := config.NewConfig()
	cfg.SetContext("test", serverURL, "test-token")
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save test config: %v", err)
	}

	cleanup = func() {
		_ = os.RemoveAll(tmpDir)
	}

	return configPath, cleanup
}

// ExecuteCommand executes a cobra command with args and returns output/error
func ExecuteCommand(t *testing.T, cmd *cobra.Command, args ...string) (output string, err error) {
	t.Helper()

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)

	err = cmd.Execute()
	output = buf.String()
	return
}

// CreateTempFile creates a temporary file with content and returns the file path
func CreateTempFile(t *testing.T, content string, pattern string) string {
	t.Helper()

	if pattern == "" {
		pattern = "test-*.yaml"
	}

	tmpFile, err := os.CreateTemp(t.TempDir(), pattern)
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() {
		_ = tmpFile.Close()
	}()

	if _, err := tmpFile.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	return tmpFile.Name()
}

// ResetCommandFlags resets a command's flags to allow reuse in tests.
//
// Only the command's own flags are reset. Once cobra has merged a parent's
// persistent flags into cmd.Flags() (any earlier Execute or InheritedFlags
// call does that), resetting those too would silently clear package state a
// test set up before the call, such as cfgFile behind --config.
func ResetCommandFlags(cmd *cobra.Command) {
	cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
		flag.Changed = false
		if rv, ok := flag.Value.(interface{ Reset() }); ok {
			rv.Reset()
		} else if sv, ok := flag.Value.(pflag.SliceValue); ok {
			// SliceValue.Set appends rather than replaces; use Replace to restore the
			// declared default. StringArray stores DefValue as JSON; other slice types
			// fall back to nil (empty) if the format doesn't parse.
			var defaults []string
			if flag.DefValue != "[]" {
				_ = json.Unmarshal([]byte(flag.DefValue), &defaults)
			}
			_ = sv.Replace(defaults)
		} else {
			_ = flag.Value.Set(flag.DefValue)
		}
	})
}
