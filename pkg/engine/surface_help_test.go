package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/engine"
)

// TestHelpAndCompletionHonorTheProfile: under a profile (here as
// DTCTL_PROFILE=query), `help <command>` answers like `<command> --help`, and
// completing a masked command offers nothing of its own. Each case runs twice
// on both engines, because cobra creates help and __complete lazily and a
// reused tree used to behave differently on the second run.
func TestHelpAndCompletionHonorTheProfile(t *testing.T) {
	engines := map[string]*engine.Engine{
		"serialized": engine.New(engine.Limits{}),
		"concurrent": engine.New(engine.Limits{}, engine.WithConcurrentExecution(2)),
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
							Profile:        "query",
							MinStability:   floor,
						})
						require.NoError(t, err)
						return res
					}
					where := func(cmd string) string { return fmt.Sprintf("%s, round %d, floor %q: %s", name, round, floor, cmd) }

					res := run("help query")
					require.Zero(t, res.ExitCode, where("help query"))
					require.Contains(t, string(res.Stdout), "DQL", where("help query"))

					for _, masked := range []string{"apply", "get workflows"} {
						help, flag := run("help "+masked), run(masked+" --help")
						require.Equal(t, 2, help.ExitCode, where("help "+masked))
						require.Contains(t, string(help.Stderr), "not available in profile", where("help "+masked))
						require.Empty(t, help.Stdout, where("help "+masked))
						require.Equal(t, flag.ExitCode, help.ExitCode, where(masked+" --help"))
						require.Equal(t, string(flag.Stderr), string(help.Stderr), where(masked+" --help"))
					}

					// A masked command offers only what its visible parent
					// offers (inherited flags).
					for masked, parent := range map[string]string{"get workflows": "get", "apply": ""} {
						offered := func(cmd string) []string {
							res := run(strings.TrimSpace("__complete " + cmd + " --"))
							require.Zero(t, res.ExitCode, where("__complete "+cmd))
							return strings.Split(string(res.Stdout), "\n")
						}
						require.Subset(t, offered(parent), offered(masked), where("__complete "+masked))
					}
					res = run("__complete apply -")
					require.NotContains(t, string(res.Stdout), "--file", where("__complete apply"))
					require.NotContains(t, string(res.Stdout), "-f\t", where("__complete apply"))

					res = run("__complete query --max-result-r")
					require.Zero(t, res.ExitCode, where("__complete query"))
					require.Contains(t, string(res.Stdout), "--max-result-records", where("__complete query"))
				}
			}
		})
	}
}

// TestHelpHonorsBlockedCommandsAndTheFloor: `help` refuses a blocked or
// below-floor command like `--help` does, and is itself stable.
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

// TestAMaskEndsWithItsRun: on the shared tree, what a mask hides is back for the
// next request, including the --help flag cobra adds after the snapshot.
func TestAMaskEndsWithItsRun(t *testing.T) {
	run := func(profile, command string) *engine.Result {
		t.Helper()
		res, err := engine.Execute(context.Background(), engine.Request{
			Command: command, EnvironmentURL: unroutable, Token: "t", Profile: profile,
		})
		require.NoError(t, err)
		return res
	}

	run("query", "get buckets")                 // runs the guard: cobra adds --help to `get buckets`
	run("query", "__complete get buckets --")   // masks it again, --help included
	res := run("", "__complete get buckets --") // no profile
	require.Contains(t, string(res.Stdout), "--help")
}

// TestFullCatalogLeavesOutMaskedCommands: `commands --full` lists the
// unadvertised `exec api`, unless a profile masks it.
func TestFullCatalogLeavesOutMaskedCommands(t *testing.T) {
	for profile, listed := range map[string]bool{"": true, "query": false} {
		res, err := engine.Execute(context.Background(), engine.Request{
			Command:        "commands --full -o json",
			EnvironmentURL: unroutable,
			Token:          "t",
			Profile:        profile,
		})
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, "profile %q: %s", profile, res.Stderr)

		var catalog struct {
			Verbs map[string]struct {
				Resources []string `json:"resources"`
			} `json:"verbs"`
		}
		require.NoError(t, json.Unmarshal(res.Stdout, &catalog), "profile %q", profile)
		require.Contains(t, catalog.Verbs, "exec", "profile %q", profile)
		require.Equal(t, listed, slices.Contains(catalog.Verbs["exec"].Resources, "api"), "profile %q: exec resources %v", profile, catalog.Verbs["exec"].Resources)
	}
}
