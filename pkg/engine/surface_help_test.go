package engine_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/engine"
)

// TestHelpAndCompletionHonorTheSurface: `help <command>` answers what
// `<command> --help` answers, and completing a masked command offers nothing
// of its own, at every floor, on a reused command tree (serialized) and on a
// tree per invocation (concurrent). Each case runs twice: cobra creates its
// help and __complete commands lazily, so a second run of the same tree is
// where the two used to differ.
func TestHelpAndCompletionHonorTheSurface(t *testing.T) {
	def := engine.ProfileDefinition{Name: "p", Commands: []string{"query"}}
	engines := map[string]*engine.Engine{
		"serialized": engine.New(engine.Limits{}, engine.WithProfileDefinition(def)),
		"concurrent": engine.New(engine.Limits{}, engine.WithProfileDefinition(def), engine.WithConcurrentExecution(2)),
	}
	for name, e := range engines {
		t.Run(name, func(t *testing.T) {
			for round := range 2 {
				for _, floor := range []string{"", "experimental"} {
					run := func(command string) *engine.Result {
						t.Helper()
						res, err := e.Execute(context.Background(), engine.Request{
							Command:        command,
							EnvironmentURL: unroutable,
							Token:          "t",
							MinStability:   floor,
						})
						require.NoError(t, err)
						return res
					}
					where := func(cmd string) string { return fmt.Sprintf("%s, round %d, floor %q: %s", name, round, floor, cmd) }

					res := run("help query")
					require.Zero(t, res.ExitCode, where("help query"))
					require.Contains(t, string(res.Stdout), "DQL", where("help query"))

					for _, masked := range []string{"apply", "get buckets"} {
						help, flag := run("help "+masked), run(masked+" --help")
						require.Equal(t, 2, help.ExitCode, where("help "+masked))
						require.Contains(t, string(help.Stderr), "not available in profile", where("help "+masked))
						require.Empty(t, help.Stdout, where("help "+masked))
						require.Equal(t, flag.ExitCode, help.ExitCode, where(masked+" --help"))
						require.Equal(t, string(flag.Stderr), string(help.Stderr), where(masked+" --help"))
					}

					// A masked command completes only the global flags, which
					// every command inherits from the root; `apply` keeps its
					// own --file to itself.
					global := strings.Split(string(run("__complete --").Stdout), "\n")
					res = run("__complete get buckets --")
					require.Zero(t, res.ExitCode, where("__complete get buckets"))
					require.Subset(t, global, strings.Split(string(res.Stdout), "\n"), where("__complete get buckets"))
					res = run("__complete apply --")
					require.NotContains(t, string(res.Stdout), "--file", where("__complete apply"))

					res = run("__complete query --max-result-r")
					require.Zero(t, res.ExitCode, where("__complete query"))
					require.Contains(t, string(res.Stdout), "--max-result-records", where("__complete query"))
				}
			}
		})
	}
}

// TestHelpHonorsBlockedCommandsAndTheFloor: without a profile, help on a
// command the engine blocks, or one below the stability floor, is that
// stage's refusal, as `<command> --help` is; help itself is stable and
// survives the default floor.
func TestHelpHonorsBlockedCommandsAndTheFloor(t *testing.T) {
	for round := range 2 {
		for command, want := range map[string]string{
			"help config":      "unsupported_in_service",
			"config --help":    "unsupported_in_service",
			"help inventory":   "experimental",
			"inventory --help": "experimental",
		} {
			res, err := engine.Execute(context.Background(), engine.Request{
				Command:        command + " --agent",
				EnvironmentURL: unroutable,
				Token:          "t",
			})
			require.NoError(t, err)
			require.NotZero(t, res.ExitCode, "round %d: %s", round, command)
			require.Contains(t, string(res.Stdout)+string(res.Stderr), want, "round %d: %s", round, command)
		}

		res, err := engine.Execute(context.Background(), engine.Request{
			Command:        "help get buckets",
			EnvironmentURL: unroutable,
			Token:          "t",
		})
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, "round %d: help get buckets", round)
		require.Contains(t, string(res.Stdout), "Usage:", "round %d: help get buckets", round)
	}
}

// TestAMaskEndsWithItsRun: what a mask hides on the shared command tree is
// back for the next request that does not mask it — including the --help flag
// cobra adds to a command the first time it runs, after the tree's snapshot.
func TestAMaskEndsWithItsRun(t *testing.T) {
	masked := engine.New(engine.Limits{}, engine.WithProfileDefinition(engine.ProfileDefinition{Name: "p", Commands: []string{"query"}}))
	run := func(e func(context.Context, engine.Request) (*engine.Result, error), command string) *engine.Result {
		t.Helper()
		res, err := e(context.Background(), engine.Request{Command: command, EnvironmentURL: unroutable, Token: "t"})
		require.NoError(t, err)
		return res
	}

	run(masked.Execute, "get buckets")                      // runs the guard: cobra adds --help to `get buckets`
	run(masked.Execute, "__complete get buckets --")        // masks it again, --help included
	res := run(engine.Execute, "__complete get buckets --") // no profile
	require.Contains(t, string(res.Stdout), "--help")
}
