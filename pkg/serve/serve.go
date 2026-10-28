// Package serve is the command surface for running dtctl as a server on top
// of pkg/engine. Each protocol is its own subcommand — `dtctl serve http`
// today, room for `dtctl serve mcp` and others without redefining what bare
// `serve` means. This file holds the parent command and the standalone
// dispatch; each protocol lives in its own file (http.go).
//
// The servers here are reference implementations. They carry no
// authentication of their own: every request brings the tenant token it runs
// with, and listeners bind to localhost by default. A real deployment puts
// its own authentication, TLS, and rate limiting in front (or embeds
// pkg/engine directly instead of shipping one of these servers).
package serve

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/cmd"
)

// DevelopmentFeature is the stability opt-in key for the `dtctl serve` command
// surface. Server mode is still taking shape — the request/response contract
// and the one-invocation-at-a-time concurrency model are subject to change —
// so it is a development-tier feature: released builds do not register the
// command unless the operator opts in with `dtctl config set development.serve
// on` or DTCTL_DEVELOPMENT=serve.
//
// pkg/engine now enforces per-request admission control and a duration
// budget (see engine.Limits, engine.DefaultLimits); what remains open is
// documented in docs/dev/SERVICE_ENGINE_DESIGN.md's "Known limitations".
//
// The gate covers the *command* only. pkg/engine stays importable: embedding it
// is a deliberate Go API choice made at compile time, not a surface an end user
// can stumble into. Promote serve out of the development tier when its
// contract settles.
const DevelopmentFeature = "serve"

// Enabled reports whether the `dtctl serve` command surface is opted into.
//
// main needs this *before* the command pipeline runs, because a server must
// start outside the per-invocation lock (see Run). Inside the pipeline the
// registration stage makes the same decision for the tree.
func Enabled() bool {
	return cmd.DevelopmentFeatureEnabled(DevelopmentFeature)
}

// Run executes `dtctl serve ...` standalone with the given arguments
// (everything after "serve") and returns the process exit code. main
// dispatches to it *before* the normal command pipeline: a server must run
// outside the per-invocation lock, because every request it accepts becomes an
// engine execution that has to acquire that lock — a server started inside an
// invocation would deadlock its own first request.
func Run(argv []string) int {
	// Register serve on the root too (a second instance — the standalone one
	// below cannot be parented): requests naming "serve" then get the
	// signposted "unsupported in service" block instead of a generic unknown
	// command, and the RunActive guard keeps it inert if ever dispatched.
	//
	// Deliberately ungated, unlike the registration in main: the server is
	// already running in this process, so serve's existence is not something a
	// request could learn here that it did not already know. Gating it would
	// instead make a request that names "serve" — without repeating the opt-in
	// in its own environment — fall back to a generic unknown command.
	cmd.AddCommand(NewCommand())

	// Hang the standalone instance off a synthetic "dtctl" root so usage lines
	// read `dtctl serve http ...` rather than `serve http ...`, and so cobra's
	// auto-added help/completion children attach to that root instead of
	// showing up as protocols under serve.
	root := &cobra.Command{Use: "dtctl", SilenceUsage: true}
	root.AddCommand(NewCommand())
	root.SetArgs(append([]string{"serve"}, argv...))
	if err := root.Execute(); err != nil {
		return 1
	}
	return 0
}

// NewCommand builds the `dtctl serve` command tree. It lives outside package
// cmd because it imports pkg/engine, which imports cmd — main wires an
// instance onto the root via cmd.AddCommand for help/catalog visibility, and
// executes a standalone instance via Run (see there for why).
//
// The parent itself is not runnable: naming the protocol is mandatory, so no
// single one is the silent default. Bare `dtctl serve` prints help and exits 0.
func NewCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run dtctl as a server (reference implementations)",
		Long: `Run dtctl as a server that executes command lines for AI agents and
automation (e.g. Dynatrace Workflow actions), instead of a one-shot CLI.

Each request brings its own Dynatrace environment URL and token — servers are
multi-tenant and never read the local dtctl config, keyring, or credential
environment variables. File arguments resolve against per-request virtual
files, host-only commands (config, ctx, auth, ...) are unavailable, and no
subprocesses are spawned. Requests execute one at a time per process.

Pick the protocol you want to speak:

  dtctl serve http    JSON over HTTP (POST /v1/execute)

These are reference implementations: they perform no authentication of their
own (the per-request token only authenticates against Dynatrace) and bind to
localhost by default. Put your own authentication and TLS in front before
exposing one, or embed pkg/engine directly.`,
		// Cobra does not validate arguments on a parent with no Run, so an
		// unmatched name would silently print help and exit 0 — a typo'd
		// protocol must not look like a started server to a supervisor.
		Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown protocol %q for `dtctl serve` (available: %s)",
					args[0], strings.Join(protocolNames(c), ", "))
			}
			// No protocol named: discovery, not misuse — help and exit 0.
			return c.Help()
		},
	}
	c.AddCommand(newHTTPCommand())
	return c
}

// protocolNames lists the protocols serve can speak, for error messages.
func protocolNames(serveCmd *cobra.Command) []string {
	var names []string
	for _, sub := range serveCmd.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		names = append(names, sub.Name())
	}
	return names
}
