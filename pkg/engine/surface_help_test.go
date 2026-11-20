package engine_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/engine"
)

// TestHelpAndCompletionHonorTheSurface: `help <command>` answers what
// `<command> --help` answers, and a completion request for a masked command
// completes nothing, at every floor and on every run of a reused tree. Each case
// runs twice because cobra creates its help and __complete commands lazily, on
// first use, so a second run of the same tree is where the two used to differ.
func TestHelpAndCompletionHonorTheSurface(t *testing.T) {
	def := &engine.ProfileDefinition{Name: "p", Commands: []string{"query"}}
	for round := 0; round < 2; round++ {
		for _, floor := range []string{"", "experimental"} {
			run := func(command string, def *engine.ProfileDefinition) *engine.Result {
				t.Helper()
				res, err := engine.Execute(context.Background(), engine.Request{
					Command:           command,
					EnvironmentURL:    unroutable,
					Token:             "t",
					ProfileDefinition: def,
					MinStability:      floor,
				})
				require.NoError(t, err)
				return res
			}
			where := func(cmd string) string { return fmt.Sprintf("round %d, floor %q: %s", round, floor, cmd) }

			res := run("help query", def)
			require.Zero(t, res.ExitCode, where("help query"))
			require.Contains(t, string(res.Stdout), "DQL", where("help query"))

			for _, masked := range []string{"help apply", "help get buckets"} {
				res = run(masked, def)
				require.NotZero(t, res.ExitCode, where(masked))
				require.Contains(t, string(res.Stderr), "not available in profile", where(masked))
				require.NotContains(t, string(res.Stdout), "Usage:", where(masked))
			}

			// A command the engine blocks is refused through help too.
			res = run("help config", nil)
			require.NotZero(t, res.ExitCode, where("help config"))

			// help is stable: it survives the default floor.
			res = run("help get buckets", nil)
			require.Zero(t, res.ExitCode, where("help get buckets, no profile"))

			res = run("__complete get buckets --", def)
			require.Zero(t, res.ExitCode, where("__complete masked"))
			require.Equal(t, ":4\n", string(res.Stdout), where("__complete masked"))

			res = run("__complete query --max-result-r", def)
			require.Zero(t, res.ExitCode, where("__complete allowed"))
			require.Contains(t, string(res.Stdout), "--max-result-records", where("__complete allowed"))
		}
	}
}

// TestHelpHonorsTheStabilityFloor: help on a command below the floor is the
// floor's refusal, as `<command> --help` is.
func TestHelpHonorsTheStabilityFloor(t *testing.T) {
	for _, command := range []string{"inventory --help", "help inventory"} {
		res, err := engine.Execute(context.Background(), engine.Request{
			Command:        command,
			EnvironmentURL: unroutable,
			Token:          "t",
		})
		require.NoError(t, err)
		require.NotZero(t, res.ExitCode, command)
		require.Contains(t, string(res.Stderr), "experimental", command)
	}
}
