package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

// RunOptions configures a single embedded invocation. The zero value is the
// CLI default: every capability granted, config and credentials resolved from
// the host (config file, env, keyring), host environment untouched.
type RunOptions struct {
	// Capabilities restricts process-level abilities (subprocess spawns) for
	// this invocation. nil grants everything (the CLI default); embedded
	// callers typically pass &Capabilities{} to grant nothing.
	Capabilities *Capabilities

	// Session, when non-nil, pins the invocation to one environment + token
	// and detaches it from the host's config file, contexts, keyring, and
	// credential env vars. See Session.
	Session *Session

	// Env sets environment variables for the duration of the invocation
	// (restored afterwards), e.g. DTCTL_PROFILE to select a command profile
	// per request. Applied after session scrubbing, so an explicit entry wins.
	// For a serialized invocation the mutation is process-wide while it runs;
	// a Concurrent one records the entries on itself instead — see
	// applyRunEnvironment.
	Env map[string]string

	// FS resolves user-supplied file paths (-f and friends) for this
	// invocation, including writebacks like `apply --write-id`. nil reads and
	// writes the host filesystem (the CLI default); embedded callers pass a
	// vfs.MapFS built from the request's virtual files. See pkg/vfs.
	FS vfs.FS

	// Stdout and Stderr receive the invocation's output; Stdin feeds commands
	// that read it ("-" file arguments, piped input). nil means the process
	// streams (the CLI default). The redirection captures every output path —
	// fmt.Print*, the output package, cobra help, error envelopes — as the
	// exact byte stream the CLI would print. See redirectStdio.
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	// BlockedCommands removes top-level commands (with their whole subtree)
	// from this invocation's surface: hidden from help, the `dtctl commands`
	// catalog, and completion, and guarded so dispatch returns an
	// UnsupportedCommandError (agent-mode code "unsupported_in_service").
	// Keyed by top-level command name; the value is the human-readable reason.
	// nil is the full surface (the CLI default). See applyBlockedCommands.
	BlockedCommands map[string]string

	// Context carries the request's context into the Cobra command tree so
	// command bodies can observe cancellation and deadlines via cmd.Context().
	// nil defaults to context.Background(), preserving CLI behaviour.
	Context context.Context

	// Concurrent opts this invocation out of the whole-invocation lock, so it
	// overlaps with other Concurrent invocations in the same process.
	//
	// The invocation runs on a
	// command tree of its own (newCommandTree), with every flag bound to
	// storage only it can reach, and its streams, environment, filesystem,
	// session and capabilities travel on its context instead of being swapped
	// process-wide. A panic in the command fails this invocation (exit code
	// ExitError, message on its stderr) rather than the embedding process.
	//
	// Requires a Session: config resolution reads the host's contexts, which a
	// tree of its own cannot select (--context), so Run refuses the combination
	// with ExitUsageError.
	//
	// Its wait for a serialized invocation to finish ends with Context: Run
	// then returns ExitError without having started (OnStart is not called).
	Concurrent bool

	// OnStart, when set, is called on Run's goroutine once the invocation holds
	// the invocation lock, before anything else of it runs.
	OnStart func()
}

// runCtx holds the active invocation's context, threaded into the Cobra tree
// via rootCmd.ExecuteContext. Guarded by runMu like the rest of per-run state.
var runCtx = context.Background()

// runMu serializes invocations. The command tree is package state (277
// command values wired by init), so two interleaved executions would share
// flag values and tree mutations. In-process callers therefore queue;
// parallelism comes from running more instances or more processes.
// See docs/dev/SERVICE_ENGINE_DESIGN.md ("Serialization").
//
// RunOptions.Concurrent opts out of that: such an invocation builds a tree of
// its own instead of borrowing the singleton, and takes the lock shared, so
// concurrent invocations overlap with each other but never with a serialized
// one, which takes it exclusively. A process therefore runs either one
// serialized invocation or any number of concurrent ones, never a mix — which
// is what lets the global OnInitialize hook tell them apart (initConfigHook).
// See docs/dev/CONCURRENT_EXECUTION.md, and runLock for why it is not a
// sync.RWMutex.
var runMu = newRunLock()

// runActive counts the invocations currently executing. A counter rather
// than a flag: concurrent invocations overlap, and the first to finish must
// not report the others as done.
var runActive atomic.Int64

// RunActive reports whether a Run invocation is currently executing. Long-
// running commands that themselves embed Run — `dtctl serve` accepting
// requests — use it to refuse execution from inside another invocation:
// blocking in a RunE would hold the invocation lock for the server's whole
// lifetime and deadlock every request (main dispatches serve outside Run for
// exactly this reason).
func RunActive() bool {
	return runActive.Load() > 0
}

// Run executes one dtctl invocation in-process and returns its exit code.
// argv is the command line without the program name (os.Args[1:] shape).
//
// Run is the embedding seam for the service engine and for `dtctl serve`:
// unlike Execute it never terminates the process, and every invocation starts
// from a pristine command tree — flag values reset to declared defaults, and
// per-run tree mutations (command-profile masks, scope-preflight wraps)
// undone. Concurrent calls are safe; they execute one at a time unless
// opts.Concurrent is set, in which case each runs on a tree of its own.
func Run(argv []string, opts RunOptions) (code int) {
	if opts.Concurrent && opts.Session == nil {
		reportOptionsError(context.Background(), opts, errors.New("RunOptions.Concurrent requires a Session"))
		return client.ExitUsageError
	}
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}

	if opts.Concurrent {
		if err := runMu.RLock(ctx); err != nil {
			reportOptionsError(ctx, opts, fmt.Errorf("gave up waiting for a serialized invocation to finish: %w", err))
			return client.ExitError
		}
		defer runMu.RUnlock()
		concurrentActive.Add(1)
		defer concurrentActive.Add(-1)
	} else {
		runMu.Lock()
		defer runMu.Unlock()
	}
	runActive.Add(1)
	defer runActive.Add(-1)
	if opts.OnStart != nil {
		opts.OnStart()
	}

	granted := AllCapabilities()
	if opts.Capabilities != nil {
		granted = *opts.Capabilities
	}

	// The invocation travels on the context threaded into the command tree, so
	// every accessor below — streams, environment, capabilities, session —
	// resolves to it rather than to the package globals.
	inv := &invocation{
		session:    opts.Session,
		blocked:    opts.BlockedCommands,
		caps:       granted,
		concurrent: opts.Concurrent,
	}
	ctx = withInvocation(ctx, inv)
	// Registered first, so it runs last: nothing the deferred cleanups below
	// resolve through ctx may see the invocation already ended.
	defer inv.ended.Store(true)

	// The serialized path keeps mutating the package globals, so that a caller
	// reaching for them directly (tests, SetCapabilities) sees what it always
	// did. The concurrent path leaves them alone and reads from inv.
	if !opts.Concurrent {
		prevCtx := runCtx
		runCtx = ctx
		defer func() { runCtx = prevCtx }()

		prev := SetCapabilities(granted)
		defer SetCapabilities(prev)

		runBlocked = opts.BlockedCommands
		defer func() { runBlocked = nil }()
	}

	cleanup, err := applyRunEnvironment(ctx, opts)
	if err != nil {
		// A malformed RunOptions is an embedding-caller bug, not a command
		// error — report it on the caller's stderr with a usage exit code.
		reportOptionsError(ctx, opts, err)
		return client.ExitUsageError
	}
	defer cleanup()

	restoreStdio, err := redirectStdio(ctx, opts.Stdout, opts.Stderr, opts.Stdin)
	if err != nil {
		reportOptionsError(ctx, opts, err)
		return client.ExitUsageError
	}
	defer restoreStdio()

	if opts.Concurrent {
		// Colour is decided once per process from the host (pkg/output: its
		// NO_COLOR and FORCE_COLOR, and whether its stdout is a terminal), and
		// this invocation prints to none of that: its stdout is the caller's
		// writer. Pin colour off, so a host started from a terminal or with
		// FORCE_COLOR set does not colour a tenant's response. A serialized run
		// resets the cache for itself (restorePristineTree).
		output.PinColorOff()

		// In a process serving many tenants, one command's panic must fail
		// that request, not every request in flight. Registered after the
		// stream redirect so the report lands on this invocation's stderr, and
		// before the deferred unwinding above, which still runs.
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(currentStderr(ctx), "Error: internal error: %v\n", r)
				code = client.ExitError
			}
		}()

		// A tree of its own: newCommandTree binds every flag to storage on
		// this invocation or local to a constructor, so nothing parsed here is
		// visible to a concurrent invocation, and no restore is needed.
		tree := newCommandTree(ctx)
		inv.treeRoot = tree.root
		// Cobra writes help, usage and its own errors to the command's
		// writers, falling back to the process streams. The process streams
		// are not this invocation's, so bind the tree to its own.
		tree.root.SetOut(currentStdout(ctx))
		tree.root.SetErr(currentStderr(ctx))
		tree.root.SetIn(currentStdin(ctx))
		return executeTree(tree.root, tree.get, argv)
	}
	restorePristineTree(ctx)
	return executeArgs(argv)
}

// reportOptionsError surfaces a RunOptions problem on the invocation's stderr
// (falling back to the process stderr), without going through the redirected
// stream machinery that may itself be the thing that failed.
func reportOptionsError(ctx context.Context, opts RunOptions, err error) {
	w := io.Writer(currentStderr(ctx))
	if opts.Stderr != nil {
		w = opts.Stderr
	}
	fmt.Fprintf(w, "Error: %v\n", err)
}

// pristineCommandState is the subset of cobra.Command that dtctl mutates
// between construction and execution. applyProfile overwrites RunE/Run/Args/
// Hidden/DisableFlagParsing to mask commands, installScopePreflight wraps RunE,
// applyStabilityFloor hides below-floor flags, and applyStabilityBadges
// rewrites Short/Long and flag usage strings; restoring these fields returns a
// command to its as-registered state.
type pristineCommandState struct {
	runE               func(*cobra.Command, []string) error
	run                func(*cobra.Command, []string)
	args               cobra.PositionalArgs
	hidden             bool
	disableFlagParsing bool
	short              string
	long               string
	// flags is the as-registered hidden state and usage string of every flag,
	// keyed by flag name. Flag *values* are reset separately (resetFlagSet);
	// these two are help-surface properties that the stability stages rewrite.
	flags map[string]pristineFlagState
}

// pristineFlagState is the subset of pflag.Flag that the stability stages
// mutate: the help surface, not the value.
type pristineFlagState struct {
	hidden bool
	usage  string
}

var (
	pristineOnce sync.Once
	pristineTree map[*cobra.Command]pristineCommandState
)

func capturePristineState(c *cobra.Command) pristineCommandState {
	flags := make(map[string]pristineFlagState)
	capture := func(f *pflag.Flag) {
		flags[f.Name] = pristineFlagState{hidden: f.Hidden, usage: f.Usage}
	}
	c.Flags().VisitAll(capture)
	c.PersistentFlags().VisitAll(capture)
	return pristineCommandState{
		runE:               c.RunE,
		run:                c.Run,
		args:               c.Args,
		hidden:             c.Hidden,
		disableFlagParsing: c.DisableFlagParsing,
		short:              c.Short,
		long:               c.Long,
		flags:              flags,
	}
}

// restorePristineTree returns the whole command tree to its as-registered
// state: the snapshot taken on first use (after all init() wiring, before any
// execution) is written back over every command, and all flag values return
// to their declared defaults. Each execution then applies its own per-run
// mutations (scope-preflight wraps, profile masks) from a clean slate, so
// nothing from one invocation — a --context override, a profile mask, an
// output format — can leak into the next.
func restorePristineTree(ctx context.Context) {
	pristineOnce.Do(func() {
		pristineTree = make(map[*cobra.Command]pristineCommandState)
		walkPristineRoots(func(c *cobra.Command) {
			pristineTree[c] = capturePristineState(c)
		})
	})
	// Resolved once: it is the same for every command, and the lookup behind it
	// is not free — doing it per command made a serialized run ~4x costlier.
	runContext := ctx
	walkPristineRoots(func(c *cobra.Command) {
		state, ok := pristineTree[c]
		if !ok {
			// A command registered after the first run (not a pattern dtctl
			// uses, but harmless): its current state becomes its pristine one.
			state = capturePristineState(c)
			pristineTree[c] = state
		}
		c.RunE = state.runE
		c.Run = state.run
		c.Args = state.args
		c.Hidden = state.hidden
		c.DisableFlagParsing = state.disableFlagParsing
		c.Short = state.short
		c.Long = state.long
		restoreFlagHelp(c.Flags(), state.flags)
		restoreFlagHelp(c.PersistentFlags(), state.flags)
		// No command sets IO writers at registration time, so pristine means
		// nil: cobra then resolves currentStdout()/currentStderr() dynamically at print
		// time. A caller-bound writer (tests do this) must not outlive its
		// invocation.
		c.SetOut(nil)
		c.SetErr(nil)
		c.SetIn(nil)
		// Cobra hands a subcommand its parent's context only while the
		// subcommand's own is nil, so after the first run every command keeps
		// the context of the invocation that first executed it — by then
		// cancelled, since the engine cancels each request's context when it
		// ends. Rebind the whole tree to this invocation's context.
		c.SetContext(runContext)
		resetFlagSet(c.Flags())
		resetFlagSet(c.PersistentFlags())
	})
	// Color decisions are cached per process but depend on per-run inputs
	// (--plain, NO_COLOR, TTY-ness of the current stdout).
	output.ResetColorCache()
}

// walkPristineRoots invokes fn for every command dtctl may execute: the root
// tree, plus each declared development-tier subtree.
//
// The development subtrees must be visited explicitly because they are attached
// and detached per invocation (applyDevelopmentRegistration). Were they only
// reached through the root, a feature enabled for the first time on invocation
// N would have its already-mutated state — a stability badge, a profile mask —
// captured as its pristine one on invocation N+1.
func walkPristineRoots(fn func(*cobra.Command)) {
	walkCommands(rootCmd, fn)
	for _, dc := range developmentCommands {
		if !hasSubcommand(dc.parent, dc.cmd) {
			walkCommands(dc.cmd, fn)
		}
	}
}

// restoreFlagHelp returns every flag's help-surface properties to their
// as-registered values. A flag absent from the snapshot was declared after the
// first run; leaving it alone is correct, since its current state is its
// pristine one.
func restoreFlagHelp(fs *pflag.FlagSet, snapshot map[string]pristineFlagState) {
	fs.VisitAll(func(f *pflag.Flag) {
		if state, ok := snapshot[f.Name]; ok {
			f.Hidden = state.hidden
			f.Usage = state.usage
		}
	})
}

// resetFlagSet returns every flag in fs to its declared default. Mirrors
// testutil.ResetCommandFlags (cmd/testutil/helpers.go), which stays separate
// so test helpers don't have to reach into the cmd package.
func resetFlagSet(fs *pflag.FlagSet) {
	fs.VisitAll(func(flag *pflag.Flag) {
		flag.Changed = false
		if rv, ok := flag.Value.(interface{ Reset() }); ok {
			// Values with their own reset semantics (e.g. singleUseStringValue,
			// for which Set(DefValue) would wrongly mark the flag as used).
			rv.Reset()
		} else if sv, ok := flag.Value.(pflag.SliceValue); ok {
			// SliceValue.Set appends rather than replaces; use Replace to
			// restore the declared default.
			_ = sv.Replace(sliceFlagDefaults(flag.DefValue))
		} else {
			_ = flag.Value.Set(flag.DefValue)
		}
	})
}

// sliceFlagDefaults parses a slice flag's DefValue back into its items.
// StringArray stores DefValue as JSON; other slice types fall back to nil
// (empty) if the format doesn't parse.
func sliceFlagDefaults(defValue string) []string {
	var defaults []string
	if defValue != "[]" {
		_ = json.Unmarshal([]byte(defValue), &defaults)
	}
	return defaults
}
