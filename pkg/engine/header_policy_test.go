package engine_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/engine"
)

// TestWithHeaderPolicy pins the host's say over `exec api -H`: Execute and
// Parse refuse what the policy refuses, an Engine without the option refuses
// nothing new, and the policy sees the normalized name.
func TestWithHeaderPolicy(t *testing.T) {
	var seen []string
	policy := func(name string) string {
		seen = append(seen, name)
		if name == "Host" {
			return "the request goes to the session's environment"
		}
		return ""
	}
	eng := engine.New(engine.DefaultLimits(), engine.WithHeaderPolicy(policy))
	const command = `exec api /platform/widget/v1/widgets -H "host: other.example.invalid" --agent`

	// The refusal precedes any request, so the environment is never dialled.
	res, err := eng.Execute(context.Background(), engine.Request{
		Command:        command,
		EnvironmentURL: "https://abc12345.apps.example.invalid",
		Token:          "tenant-token",
	})
	require.NoError(t, err)
	require.Equal(t, 2, res.ExitCode, "stdout: %s\nstderr: %s", res.Stdout, res.Stderr)
	require.Contains(t, string(res.Stdout), `"code":"header_refused"`)
	require.Equal(t, []string{"Host"}, seen)

	_, err = eng.Parse(context.Background(), engine.ParseRequest{Command: command})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, "header_refused", ue.Code)

	_, err = engine.New(engine.DefaultLimits()).Parse(context.Background(), engine.ParseRequest{Command: command})
	require.NoError(t, err, "without WithHeaderPolicy nothing new is refused")
}
