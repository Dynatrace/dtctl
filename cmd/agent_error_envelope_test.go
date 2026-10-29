package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/client"
)

type errorEnvelope struct {
	OK    bool `json:"ok"`
	Error struct {
		Code        string   `json:"code"`
		Message     string   `json:"message"`
		Suggestions []string `json:"suggestions"`
	} `json:"error"`
}

func parseErrorEnvelope(t *testing.T, out string) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	require.NoError(t, json.Unmarshal([]byte(out), &env), "stdout is not one envelope:\n%s", out)
	require.False(t, env.OK)
	return env
}

// TestParseErrorsHonorAutoDetectedAgentMode covers the errors cobra raises
// before initConfig runs — an unknown flag or command. Auto-detection lives in
// initConfig, so without a second look at the environment these reached an
// agent host as prose while `-A` on the same line produced an envelope.
func TestParseErrorsHonorAutoDetectedAgentMode(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"query", "fetch logs", "--limit", "5"}},
		{"unknown command", []string{"getx"}},
		{"unknown flag, explicit -o json", []string{"query", "fetch logs", "-o", "json", "--limit", "5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolatedConfig(t, "contexts: []\n")
			t.Setenv("CLAUDECODE", "1")

			code, out := captureRun(t, tt.args, RunOptions{})

			require.Equal(t, client.ExitUsageError, code)
			env := parseErrorEnvelope(t, out)
			require.Equal(t, "unknown_command", env.Error.Code)
		})
	}
}

// TestParseErrorsRespectAgentOptOut: the same failures stay human-readable when
// the caller opted out of auto-detection or chose a non-JSON format, exactly as
// initConfig would have decided had it run.
func TestParseErrorsRespectAgentOptOut(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"--no-agent", []string{"query", "fetch logs", "--limit", "5", "--no-agent"}},
		{"--no-agent=true", []string{"--no-agent=true", "getx"}},
		{"-o table", []string{"query", "fetch logs", "-o", "table", "--limit", "5"}},
		{"-otable", []string{"-otable", "getx"}},
		{"--output=csv", []string{"--output=csv", "getx"}},
		{"not detected", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolatedConfig(t, "contexts: []\n")
			args := tt.args
			if args == nil {
				args = []string{"getx"}
			} else {
				t.Setenv("CLAUDECODE", "1")
			}

			code, out := captureRun(t, args, RunOptions{})

			require.Equal(t, client.ExitUsageError, code)
			require.Empty(t, strings.TrimSpace(out), "a human error belongs on stderr, not stdout")
		})
	}
}

// TestUnknownCommandEnvelopedWithExplicitAgentFlag: `-A getx` used to print
// prose, because the raw-argv fallback that honors a --agent cobra never
// parsed did not cover unknown commands.
func TestUnknownCommandEnvelopedWithExplicitAgentFlag(t *testing.T) {
	for _, args := range [][]string{{"-A", "getx"}, {"getx", "--agent"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			isolatedConfig(t, "contexts: []\n")

			code, out := captureRun(t, args, RunOptions{})

			require.Equal(t, client.ExitUsageError, code)
			env := parseErrorEnvelope(t, out)
			require.Equal(t, "unknown_command", env.Error.Code)
			require.Contains(t, env.Error.Suggestions, `did you mean "get"?`)
		})
	}
}

// TestUnknownResourceTypeEnvelope: a misspelled resource is an unknown command
// like any other, and the correction is offered as a command that runs as-is.
func TestUnknownResourceTypeEnvelope(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		wantSuggestions []string
		wantMessage     string
	}{
		{
			name:            "did you mean",
			args:            []string{"-A", "get", "dashbords"},
			wantSuggestions: []string{"dtctl get dashboards"},
			wantMessage:     `unknown resource type "dashbords"`,
		},
		{
			name:            "positional args carried over and quoted",
			args:            []string{"-A", "describe", "dashbords", "my board"},
			wantSuggestions: []string{"dtctl describe dashboard 'my board'"},
			wantMessage:     `unknown resource type "dashbords"`,
		},
		{
			name:            "no close match",
			args:            []string{"-A", "get", "zzzzzzzzzz"},
			wantSuggestions: []string{"dtctl commands"},
			wantMessage:     `unknown resource type "zzzzzzzzzz"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolatedConfig(t, "contexts: []\n")

			code, out := captureRun(t, tt.args, RunOptions{})

			require.Equal(t, client.ExitUsageError, code)
			env := parseErrorEnvelope(t, out)
			require.Equal(t, "unknown_command", env.Error.Code)
			require.Equal(t, tt.wantMessage, env.Error.Message)
			require.Equal(t, tt.wantSuggestions, env.Error.Suggestions)
		})
	}
}

// TestUnknownResourceTypeHumanMessageUnchanged pins the human wording, which
// the typed error must reproduce exactly.
func TestUnknownResourceTypeHumanMessageUnchanged(t *testing.T) {
	err := requireSubcommand(getCmd, []string{"dashbords"})
	require.EqualError(t, err, `unknown resource type "dashbords", did you mean "dashboards"?`)

	err = requireSubcommand(getCmd, []string{"zzzzzzzzzz"})
	require.EqualError(t, err, "unknown resource type \"zzzzzzzzzz\"\nRun 'dtctl get --help' for available resources")
}

// TestRawAgentFlagScansStopAtDelimiter: after `--` every argument is
// positional, so a value spelled like a flag must not opt out.
func TestRawAgentFlagScansStopAtDelimiter(t *testing.T) {
	require.True(t, rawNoAgent([]string{"--no-agent"}))
	require.False(t, rawNoAgent([]string{"query", "--", "--no-agent"}))
	_, given := rawOutputFormat([]string{"query", "--", "-otable"})
	require.False(t, given)
}
