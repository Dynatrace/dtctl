//go:build integration
// +build integration

package integration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file is a harness for driving the real dtctl binary from the tagged
// suites. Most integration and e2e tests here call resource handlers directly,
// which is the right level for API behaviour — but some of what dtctl promises
// lives in the startup pipeline rather than in a handler: the command profile,
// the safety level, and the stability floor all shape the command tree before
// dispatch, and none of them is observable in-process the way a caller sees
// it. Those need the actual binary.

// CLIConfig describes the config file a CLI-level test runs against.
type CLIConfig struct {
	// Environment is the environment URL the single context points at. A
	// syntactically valid but unroutable URL is the right choice for any
	// assertion that must resolve before the first request.
	Environment string
	// Token is the API token stored inline, so no keychain is involved.
	Token string
	// MinStability is the context's stability floor. Empty leaves it unset,
	// which resolves to the default floor.
	MinStability string
	// StabilityExceptions are the individual commands and flags admitted below
	// the floor, written exactly as a caller would ("query --decode-snapshots").
	StabilityExceptions []string
	// UsageTracking opts the context in to the local usage ledger.
	UsageTracking bool
}

// BuildDtctl compiles the real binary into the test's temp directory and
// returns its path.
func BuildDtctl(t *testing.T) string {
	t.Helper()
	name := "dtctl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Dir = "../.." // module root, where package main lives
	out, err := build.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", out)
	return exe
}

// WriteCLIConfig writes a single-context config file and returns its path.
func WriteCLIConfig(t *testing.T, c CLIConfig) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`apiVersion: v1
kind: Config
current-context: cli
contexts:
  - name: cli
    context:
      environment: ` + c.Environment + `
      token-ref: cli
`)
	if c.MinStability != "" {
		fmt.Fprintf(&b, "      min-stability: %s\n", c.MinStability)
	}
	if len(c.StabilityExceptions) > 0 {
		b.WriteString("      stability-exceptions:\n")
		for _, e := range c.StabilityExceptions {
			fmt.Fprintf(&b, "        - %q\n", e)
		}
	}
	if c.UsageTracking {
		b.WriteString("      usage-tracking: true\n")
	}
	fmt.Fprintf(&b, "tokens:\n  - name: cli\n    token: %s\n", c.Token)

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

// RunDtctl executes the built binary and returns its exit code, stdout and
// stderr. It never fails the test on a non-zero exit: for these suites a
// refusal is frequently the expected outcome.
func RunDtctl(t *testing.T, exe, cfgPath string, extraEnv map[string]string, args ...string) (int, string, string) {
	t.Helper()
	environ := map[string]string{
		"DTCTL_CONFIG":          cfgPath,
		"DTCTL_DISABLE_KEYRING": "1",
	}
	for k, v := range extraEnv {
		environ[k] = v
	}

	cli := exec.Command(exe, args...)
	cli.Env = ScrubbedCLIEnviron(environ)
	var stdout, stderr bytes.Buffer
	cli.Stdout, cli.Stderr = &stdout, &stderr
	runErr := cli.Run()

	code := 0
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else {
		require.NoError(t, runErr)
	}
	return code, stdout.String(), stderr.String()
}

// ScrubbedCLIEnviron drops every variable that could change the reachable
// surface or the output shape, then applies the test's own.
//
// The agent markers matter most: dtctl auto-detects an AI agent from the
// environment, and in agent mode it wraps everything in a JSON envelope. Left
// in place, whether a help assertion sees plain text would depend on who ran
// the suite.
func ScrubbedCLIEnviron(extra map[string]string) []string {
	agentVars := map[string]bool{
		"CLAUDECODE": true, "CLAUDE_CODE": true, "AI_AGENT": true, "CODEX": true,
		"CURSOR_AGENT": true, "COPILOT_CLI": true, "GITHUB_COPILOT": true,
		"AGENT_CONTEXT_OUT": true, "KIRO_SESSION_ID": true, "OPENCODE": true,
		"NO_COLOR": true, "FORCE_COLOR": true,
	}
	var out []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "DTCTL_") || strings.HasPrefix(key, "DT_") ||
			strings.HasPrefix(key, "OTEL_") || agentVars[key] {
			continue
		}
		out = append(out, kv)
	}
	// NO_COLOR keeps badge assertions free of ANSI escapes.
	out = append(out, "NO_COLOR=1")
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// UnroutableEnvironment is a syntactically valid environment URL that resolves
// to no tenant. Tests whose assertions must hold *before* the first request
// use it, so a floor that only blocked after connecting would fail them.
const UnroutableEnvironment = "https://abc12345.apps.dynatrace.com"
