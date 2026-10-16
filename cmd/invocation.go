package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"

	"github.com/go-resty/resty/v2"
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/apply"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

// Invocation state travels on the context.
//
// dtctl's embedding seam used to keep the active invocation's streams, session,
// capabilities, filesystem and environment in package variables, which is only
// defensible while Run serializes on runMu. A concurrent invocation instead
// carries them on its context.Context: Run attaches the invocation to the
// context it threads into the command tree, cobra hands that context to every
// command, and the accessors below read it from there. Nothing is keyed by
// goroutine, so goroutines a command starts inherit the invocation with the
// context they are given.
//
// Every accessor degrades to the existing package-level default when its context
// carries no invocation. That is what keeps the ordinary CLI path unchanged: it
// has no invocation, so the accessors fall through to os.Stdout/os.Stderr and the
// package variables exactly as before, and a serialized Run still swaps the
// process streams for its writers (see redirectStdio).

// invocation is the state a single dtctl run owns. A nil field means "defer to
// the process-level default", which is how the non-concurrent path keeps its
// current behaviour without a second code path.
type invocation struct {
	session *Session
	blocked map[string]string
	caps    Capabilities

	// env overlays the process environment. A key present with ok==false was
	// scrubbed for this invocation and must read as empty even though the host
	// process still has it set.
	env map[string]envValue

	fs vfs.FS

	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader

	tracingCtx context.Context

	// flags holds the root persistent flag values for this invocation. On the
	// concurrent path newCommandTree binds the tree's root flags here, so cobra
	// writes the parsed values straight into this invocation's storage. The
	// serialized path binds the singleton to gFlags and never reads this.
	flags rootFlags

	// treeRoot is the fresh cobra root newCommandTree built for this
	// invocation, so code that needs a flag's Changed state, or the command
	// surface, can ask this request's tree rather than the singleton.
	treeRoot *cobra.Command

	// initDone records that initConfig has run for this concurrent invocation.
	initDone bool

	// ended is set when Run returns. A context can outlive its invocation: cobra
	// keeps the one a serialized Run bound the singleton tree to, so a later
	// direct call to a command would otherwise find the finished invocation's
	// session and streams. An ended invocation is invisible to the accessors.
	ended atomic.Bool

	// concurrent reports whether this invocation opted out of the whole-run
	// lock. The seams consult it to decide between per-invocation state and
	// the process-wide mutation the serialized path still performs.
	concurrent bool

	// invokedGetCmd is the get subcommand this invocation is dispatching, set
	// for the duration of its RunE by the installGetListPaging wrapper. Keeps
	// it off the package-level var so concurrent invocations don't race.
	invokedGetCmd *cobra.Command

	// getListLimit and getListFields are where this invocation's tree binds
	// get's --limit and --fields (addGetListShapeFlags).
	getListLimit  int
	getListFields string

	// previewNoticeShown records which preview notices this invocation has
	// printed, so each request sees them once, as a CLI process would.
	previewNoticeShown map[string]bool
}

// envValue is an environment entry in an invocation's overlay. present==false
// means the variable is scrubbed for this invocation.
type envValue struct {
	value   string
	present bool
}

// invocationKey is the context key the invocation is stored under.
type invocationKey struct{}

// withInvocation returns a context that carries inv. Every context derived from
// it reaches inv again, and so does every goroutine given such a context.
func withInvocation(ctx context.Context, inv *invocation) context.Context {
	return context.WithValue(ctx, invocationKey{}, inv)
}

// current returns the invocation ctx carries, or nil when there is none: the
// plain CLI, a test, or a context that did not come from Run. A nil return is
// the fail-safe: callers fall back to process-level state rather than borrowing
// some other invocation's.
func current(ctx context.Context) *invocation {
	if ctx == nil {
		return nil
	}
	inv, _ := ctx.Value(invocationKey{}).(*invocation)
	if inv != nil && inv.ended.Load() {
		return nil
	}
	return inv
}

// concurrentActive counts the concurrent invocations running in this process.
// cobra calls its global OnInitialize hooks for every tree it executes without
// saying which, and initConfig's process-wide half (viper, the output
// package's plain-mode switch) must not run for a concurrent one. Serialized
// invocations exclude concurrent ones (see runMu), so a hook that sees none
// active is running for a serialized or CLI invocation.
var concurrentActive atomic.Int64

// currentStdout resolves this invocation's stdout, falling back to the process
// stream. In the serialized path the fallback is the right answer, because
// redirectStdio has already pointed os.Stdout at the caller's writer.
func currentStdout(ctx context.Context) io.Writer {
	if inv := current(ctx); inv != nil && inv.stdout != nil {
		return inv.stdout
	}
	return os.Stdout
}

// currentStdoutOr resolves this invocation's stdout, falling back to w rather
// than to the process stream. Call sites that already had a writer of their
// own — a cobra command's OutOrStdout, which tests redirect — use this so the
// fallback stays what it was.
func currentStdoutOr(ctx context.Context, w io.Writer) io.Writer {
	if inv := current(ctx); inv != nil && inv.stdout != nil {
		return inv.stdout
	}
	return w
}

// currentStderr mirrors currentStdout for the error stream.
func currentStderr(ctx context.Context) io.Writer {
	if inv := current(ctx); inv != nil && inv.stderr != nil {
		return inv.stderr
	}
	return os.Stderr
}

// currentStdin mirrors currentStdout for the input stream.
func currentStdin(ctx context.Context) io.Reader {
	if inv := current(ctx); inv != nil && inv.stdin != nil {
		return inv.stdin
	}
	return os.Stdin
}

// currentCaps resolves the capability set for this invocation, falling back to
// the process-wide set that SetCapabilities maintains for CLI and test callers.
func currentCaps(ctx context.Context) Capabilities {
	if inv := current(ctx); inv != nil {
		return inv.caps
	}
	return caps
}

// currentSession resolves the invocation's session override, or nil for
// ordinary config-file resolution.
func currentSession(ctx context.Context) *Session {
	if inv := current(ctx); inv != nil {
		return inv.session
	}
	return runSession
}

// currentBlocked resolves the invocation's blocked-command set.
func currentBlocked(ctx context.Context) map[string]string {
	if inv := current(ctx); inv != nil {
		return inv.blocked
	}
	return runBlocked
}

// currentTracingCtx resolves the context carrying this invocation's root OTel
// span, used to inject W3C trace headers on outgoing requests.
func currentTracingCtx(ctx context.Context) context.Context {
	if inv := current(ctx); inv != nil {
		return inv.tracingCtx
	}
	return tracingRootCtx
}

// setCurrentTracingCtx records tracing, the context carrying the root span, for
// the invocation ctx carries.
func setCurrentTracingCtx(ctx, tracing context.Context) {
	if inv := current(ctx); inv != nil {
		inv.tracingCtx = tracing
		return
	}
	tracingRootCtx = tracing
}

// getenv reads an environment variable through the invocation's overlay.
//
// In concurrent mode a session's credential scrubbing and RunOptions.Env are
// recorded on the invocation instead of being applied to the process with
// os.Setenv, so every DTCTL_* read on a request path has to come through here
// to observe them. With no overlay this is plain os.Getenv.
func getenv(ctx context.Context, key string) string {
	if inv := current(ctx); inv != nil && inv.env != nil {
		if v, ok := inv.env[key]; ok {
			if !v.present {
				return ""
			}
			return v.value
		}
	}
	return os.Getenv(key)
}

// lookupEnv is getenv with os.LookupEnv's "was it set" reporting.
func lookupEnv(ctx context.Context, key string) (string, bool) {
	if inv := current(ctx); inv != nil && inv.env != nil {
		if v, ok := inv.env[key]; ok {
			if !v.present {
				return "", false
			}
			return v.value, true
		}
	}
	return os.LookupEnv(key)
}

// vfsEnv is this invocation's view of the filesystem and stdin for
// user-supplied paths. With no invocation it is the zero Env: the installed FS
// and the process's stdin.
func vfsEnv(ctx context.Context) vfs.Env {
	if inv := current(ctx); inv != nil {
		return vfs.Env{FS: inv.fs, Stdin: inv.stdin}
	}
	return vfs.Env{}
}

// newPrinter builds the printer for format on this invocation's streams.
func newPrinter(ctx context.Context, format string) output.Printer {
	return newPrinterOpts(ctx, output.PrinterOptions{Format: format})
}

// newPrinterOpts builds a printer on this invocation's streams: its output
// goes to the invocation's stdout unless opts names a writer, and the auto
// printer's format notice to its stderr.
func newPrinterOpts(ctx context.Context, opts output.PrinterOptions) output.Printer {
	if opts.Writer == nil {
		opts.Writer = currentStdout(ctx)
	}
	if opts.Notice == nil {
		opts.Notice = currentStderr(ctx)
	}
	return output.NewPrinterWithOpts(opts)
}

// newDQLExecutor, newFunctionExecutor and newApplier construct the pkg
// executors on this invocation's streams and filesystem.
func newDQLExecutor(ctx context.Context, c *client.Client) *exec.DQLExecutor {
	return exec.NewDQLExecutor(c).WithStreams(currentStdout(ctx), currentStderr(ctx)).WithVFS(vfsEnv(ctx))
}

func newFunctionExecutor(ctx context.Context, c *client.Client) *exec.FunctionExecutor {
	return exec.NewFunctionExecutor(c).WithVFS(vfsEnv(ctx))
}

func newApplier(ctx context.Context, c *client.Client) *apply.Applier {
	return apply.NewApplier(c).WithStderr(currentStderr(ctx)).WithVFS(vfsEnv(ctx))
}

// withInvocationEnv makes cfg answer the environment lookups its methods make
// (profile, stability floor, development features, deprecation mode) from this
// invocation's environment overlay, when it has one. Without an overlay the
// Config reads the process environment, as it always did.
func withInvocationEnv(ctx context.Context, cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	if inv := current(ctx); inv != nil && inv.env != nil {
		cfg.WithEnv(func(key string) (string, bool) { return lookupEnv(ctx, key) })
	}
	return cfg
}

// bindClientContext ends every request c sends when ctx ends, if the request's
// own context cannot end at all. The SDK's handlers decide that context, and
// nearly all of them pass context.Background(): those are joined to ctx, and a
// retry's wait between attempts honours it too.
//
// A request whose context can already end is left alone. It is either derived
// from the command's context, so it ends with ctx anyway, or it is a bound the
// SDK set on purpose: the query execute outlives a cancellation by a short
// grace so the query it started can still be cancelled on the backend instead
// of being orphaned (dtctl #621).
func bindClientContext(c *client.Client, ctx context.Context) {
	c.HTTP().OnBeforeRequest(func(_ *resty.Client, req *resty.Request) error {
		if rc := req.Context(); rc.Done() == nil {
			req.SetContext(joinContext(rc, ctx))
		}
		return nil
	})
}

// joinContext returns a context that ends when either req or inv does. When
// inv ends by its deadline the joined context ends by the same deadline, so the
// error that surfaces is context.DeadlineExceeded, the one a caller maps to a
// timeout, rather than a bare cancellation.
func joinContext(req, inv context.Context) context.Context {
	if inv.Done() == nil {
		return req
	}
	var (
		joined context.Context
		cancel context.CancelFunc
	)
	if d, ok := inv.Deadline(); ok {
		joined, cancel = context.WithDeadline(req, d)
	} else {
		joined, cancel = context.WithCancel(req)
	}
	// The registration lives until inv ends, which is the end of the
	// invocation; stopping it per request would need the end of each response.
	context.AfterFunc(inv, func() {
		// A deadline reaches joined on its own; cancelling here would race it and
		// turn the error into a cancellation.
		if !errors.Is(inv.Err(), context.DeadlineExceeded) {
			cancel()
		}
	})
	return joined
}
