package engine_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/cmd"
	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/engine"
)

// The tests here pin the third narrowing axis — the stability floor — as a
// per-request decision. A service embedding dtctl publishes a contract to its
// own callers; it cannot do that if the surface depends on which environment
// variables the host process happened to inherit, or if "experimental" leaks
// in by default because the CLI's interactive default did.
//
// `inventory` stands in for the experimental tier throughout: it needs no
// arguments, so a request that gets past the floor fails on the network rather
// than on usage, which keeps "blocked" and "allowed" cleanly distinguishable.

const (
	// An environment that cannot be reached: past the floor, the request must
	// fail on transport, never on a real tenant.
	unroutable = "https://x.example.invalid"
	// Marker for "the floor stopped this", and the agent-mode code that
	// carries it.
	blockedCode = `"code":"stability_blocked"`
)

// TestExecute_DefaultFloorIsStable: a request that names no floor gets
// `stable` — stricter than the CLI's `experimental` default. An embedded
// caller reads no `[Experimental]` badge, so the wider surface is opt-in.
func TestExecute_DefaultFloorIsStable(t *testing.T) {
	require.Equal(t, config.StabilityStable, cmd.SessionDefaultMinStability,
		"the engine's documented default floor is stable")

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "inventory --agent",
		EnvironmentURL: unroutable,
		Token:          "t",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	require.Contains(t, string(res.Stdout), blockedCode)
}

// TestExecute_FloorIsAskedForNotInherited: the host process's own
// DTCTL_MIN_STABILITY must not decide a tenant request's surface. Both
// directions matter — a host that widened its own CLI must not widen every
// request, and a host that narrowed its own must not narrow a request that
// explicitly asked for more.
func TestExecute_FloorIsAskedForNotInherited(t *testing.T) {
	t.Setenv(config.MinStabilityEnvVar, "experimental")

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "inventory --agent",
		EnvironmentURL: unroutable,
		Token:          "t",
	})
	require.NoError(t, err)
	require.Contains(t, string(res.Stdout), blockedCode,
		"a host env var must not widen a request that asked for the default floor")

	t.Setenv(config.MinStabilityEnvVar, "stable")

	res, err = engine.Execute(context.Background(), engine.Request{
		Command:        "inventory --agent",
		EnvironmentURL: unroutable,
		Token:          "t",
		MinStability:   "experimental",
	})
	require.NoError(t, err)
	require.NotContains(t, string(res.Stdout), blockedCode,
		"a host env var must not narrow a request that asked for experimental")
}

// TestExecute_ExperimentalFloorAdmitsTheTier: asking for the wider floor
// actually reaches the command — it fails on the unroutable environment, which
// is proof it got past every narrowing stage.
func TestExecute_ExperimentalFloorAdmitsTheTier(t *testing.T) {
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "inventory --agent",
		EnvironmentURL: unroutable,
		Token:          "t",
		MinStability:   "experimental",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode, "the unroutable environment still fails the call")
	out := string(res.Stdout)
	require.NotContains(t, out, blockedCode)
	require.NotContains(t, out, `"code":"unknown_command"`)
}

// TestExecute_ExceptionAdmitsOneCommandOnly: the field a host should reach for
// instead of lowering the floor. Naming one command must not hand over the
// rest of the tier — that is the whole difference from MinStability.
func TestExecute_ExceptionAdmitsOneCommandOnly(t *testing.T) {
	req := engine.Request{
		EnvironmentURL:      unroutable,
		Token:               "t",
		StabilityExceptions: []string{"inventory"},
	}

	req.Command = "inventory --agent"
	res, err := engine.Execute(context.Background(), req)
	require.NoError(t, err)
	require.NotContains(t, string(res.Stdout), blockedCode, "the named command is admitted")

	// A sibling in the same tier, not named: still below the floor.
	req.Command = "get breakpoints --agent"
	res, err = engine.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Contains(t, string(res.Stdout), blockedCode,
		"an exception admits what it names, not the tier it belongs to")
}

// TestExecute_FlagExceptionIsPerFlag: a below-floor flag on a stable command
// is the case a per-flag exception exists for. `query` itself must stay
// reachable either way — the floor narrows the flag, not its host command.
func TestExecute_FlagExceptionIsPerFlag(t *testing.T) {
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        `query "fetch logs | limit 1" --decode-snapshots --agent`,
		EnvironmentURL: unroutable,
		Token:          "t",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	out := string(res.Stdout)
	require.Contains(t, out, blockedCode)
	// Rejected on use, not removed: a removed flag is indistinguishable from a
	// typo, and the caller would debug the wrong problem.
	require.NotContains(t, out, "unknown flag")

	res, err = engine.Execute(context.Background(), engine.Request{
		Command:             `query "fetch logs | limit 1" --decode-snapshots --agent`,
		EnvironmentURL:      unroutable,
		Token:               "t",
		StabilityExceptions: []string{"query --decode-snapshots"},
	})
	require.NoError(t, err)
	require.NotContains(t, string(res.Stdout), blockedCode)
}

// TestExecute_CatalogReportsTheRequestFloor: an embedded agent has to be able
// to discover its own surface, or it will read every block as "that command
// does not exist" and stop asking. `dtctl commands` reports the floor in
// force, and the below-floor commands drop out of the listing with it.
func TestExecute_CatalogReportsTheRequestFloor(t *testing.T) {
	atDefault, err := engine.Execute(context.Background(), engine.Request{
		Command:        "commands -o json",
		EnvironmentURL: unroutable,
		Token:          "t",
	})
	require.NoError(t, err)
	require.Zero(t, atDefault.ExitCode, "stderr: %s", atDefault.Stderr)
	require.Contains(t, string(atDefault.Stdout), `"min_stability": "stable"`)
	require.NotContains(t, string(atDefault.Stdout), `"inventory"`,
		"a below-floor command is absent from the catalog, not listed as usable")

	wider, err := engine.Execute(context.Background(), engine.Request{
		Command:        "commands -o json",
		EnvironmentURL: unroutable,
		Token:          "t",
		MinStability:   "experimental",
	})
	require.NoError(t, err)
	require.Zero(t, wider.ExitCode, "stderr: %s", wider.Stderr)
	require.Contains(t, string(wider.Stdout), `"min_stability": "experimental"`)
	require.Contains(t, string(wider.Stdout), `"inventory"`)
}

// TestExecute_DevelopmentSurfaceIsUnreachable: the development tier has no
// per-request field on purpose, and the host's DTCTL_DEVELOPMENT must not
// register it either. There is no floor value that reaches it — the only way
// in is an opt-in the engine does not offer.
func TestExecute_DevelopmentSurfaceIsUnreachable(t *testing.T) {
	t.Setenv(config.DevelopmentEnvVar, "all")

	for _, floor := range []string{"", "experimental"} {
		res, err := engine.Execute(context.Background(), engine.Request{
			Command:        "serve http --agent",
			EnvironmentURL: unroutable,
			Token:          "t",
			MinStability:   floor,
		})
		require.NoError(t, err)
		require.NotZero(t, res.ExitCode, "floor %q must not reach a development command", floor)
	}
}

// TestExecute_InvalidStabilityInputIsRejected: a malformed floor or exception
// fails as a Go error, before the request runs. Ignoring it would widen the
// surface the caller meant to restrict, or hide a typo behind "command not
// found".
func TestExecute_InvalidStabilityInputIsRejected(t *testing.T) {
	_, err := engine.Execute(context.Background(), engine.Request{
		Command:        "get buckets",
		EnvironmentURL: unroutable,
		Token:          "t",
		MinStability:   "beta",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "engine:")

	// `development` parses as a level, so it would slip through a plain parse
	// check and then behave exactly like `experimental` — a host that asked
	// for the widest surface would get a narrower one and never be told.
	_, err = engine.Execute(context.Background(), engine.Request{
		Command:        "get buckets",
		EnvironmentURL: unroutable,
		Token:          "t",
		MinStability:   "development",
	})
	require.ErrorContains(t, err, "not reachable from a request")

	_, err = engine.Execute(context.Background(), engine.Request{
		Command:             "get buckets",
		EnvironmentURL:      unroutable,
		Token:               "t",
		StabilityExceptions: []string{"query --decode-snapshots --extra"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "engine:")
}

// TestExecute_NoDeprecatedIsPerRequestAndDoesNotLeak pins the fifth narrowing
// stage as request state.
//
// The leak is the real risk here, not the refusal: the stage mutates the shared
// command tree (Hidden, Args, DisableFlagParsing, RunE) exactly as the floor
// does, so a request that switched it on must not leave `exec workflow
// --params` refused for the next tenant that did not ask.
func TestExecute_NoDeprecatedIsPerRequestAndDoesNotLeak(t *testing.T) {
	const deprecatedCode = `"code":"deprecated_surface"`
	// A deprecated flag rather than a deprecated command, because dtctl has no
	// deprecated command today. `--params` on `exec workflow` is the one entry
	// the manifest carries; when it is finally removed, replace it with
	// whatever else is deprecated then (or delete this test with the stage).
	const cmdline = `exec workflow wf-1 --params a=b --agent`

	req := engine.Request{
		Command:        cmdline,
		EnvironmentURL: unroutable,
		Token:          "t",
		// Experimental, because `exec workflow` was itself demoted in the
		// pre-1.0 audit (contrib #17 flips its default to waiting), so a
		// stable floor would block it at stage 3 and stage 4 would never run.
		// The two stages composing that way is correct -- it just makes this a
		// bad place to observe stage 4.
		MinStability: "experimental",
		NoDeprecated: true,
	}
	res, err := engine.Execute(context.Background(), req)
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	require.Contains(t, string(res.Stdout), deprecatedCode)
	// Not the floor's error: at this floor nothing is below it, so the only
	// thing that can refuse the invocation is the deprecation -- and the two
	// are different problems with different fixes. A stability block says the
	// contract is too weak for this deployment; this says it has a removal
	// date, and no floor or exception brings it back.
	require.NotContains(t, string(res.Stdout), blockedCode)

	req.NoDeprecated = false
	res, err = engine.Execute(context.Background(), req)
	require.NoError(t, err)
	require.NotContains(t, string(res.Stdout), deprecatedCode,
		"the previous request's refusal survived into one that did not ask for it")

	// And the host's own environment cannot decide it either — scrubbed like
	// the floor, because which surface a tenant sees is the request's call.
	t.Setenv(config.NoDeprecatedEnvVar, "1")
	res, err = engine.Execute(context.Background(), req)
	require.NoError(t, err)
	require.NotContains(t, string(res.Stdout), deprecatedCode,
		"DTCTL_NO_DEPRECATED from the host process reached a request")
}
