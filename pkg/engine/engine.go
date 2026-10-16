package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/google/shlex"

	"github.com/dynatrace-oss/dtctl/cmd"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

// defaultAdmission is the admission state behind the package-level Execute
// and ExecuteWithLimits: a single execution slot, the historical behaviour.
// Callers that want overlapping executions build an Engine instead.
var defaultAdmission = &admission{slot: make(chan struct{}, 1)}

// admission is one engine's execution budget: a slot channel whose capacity
// is the concurrency limit, and the queue-depth counter guarding it.
//
// This used to be a pair of package variables, which meant two independent
// callers sharing a process also shared one queue depth — the open question
// recorded in docs/dev/SERVICE_ENGINE_DESIGN.md ("Per-engine-instance
// admission state"). Making it a value is what lets Engine size its own.
type admission struct {
	slot   chan struct{}
	queued atomic.Int64
}

// ErrTooManyQueued is returned by Execute/ExecuteWithLimits when admission
// control sheds a request because MaxQueued is already at capacity. Callers
// that expose the engine over a transport (e.g. pkg/serve) should map this to
// a "server busy" status (HTTP 503) rather than a client error, and a caller
// this happens to may retry with backoff.
var ErrTooManyQueued = errors.New("engine: too many queued requests")

// Request is one dtctl invocation for one tenant.
type Request struct {
	// Command is the command line exactly as a user would type it after
	// "dtctl", e.g. `get workflows -o json`. It is split into arguments with
	// POSIX shell rules (quotes and escapes honored; no variable expansion,
	// globbing, pipes, or redirection — dtctl is not a shell). Ignored when
	// Argv is set.
	Command string
	// Argv is the pre-split argument vector (os.Args[1:] shape). Callers that
	// already have discrete arguments should prefer it over Command — no
	// quoting round-trip. Takes precedence over Command.
	Argv []string

	// EnvironmentURL is the Dynatrace environment to run against
	// (e.g. https://abc12345.apps.dynatrace.com). Required.
	EnvironmentURL string
	// Token authenticates every API call of this request. Required.
	Token string
	// SafetyLevel bounds mutating operations for this request
	// (readonly | readwrite-mine | readwrite-all | dangerously-unrestricted).
	// Empty selects the dtctl default (readwrite-all).
	SafetyLevel string
	// Profile selects a built-in command profile (e.g. "query") that reduces
	// the visible command surface for this request. Empty is the full
	// (service-supported) surface.
	Profile string
	// MinStability is the stability floor for this request: the weakest
	// contract a command or flag may offer and still be reachable
	// (stable | experimental). `development` is rejected — that tier is gated
	// at registration, a stage earlier than the floor, and a request has no
	// opt-in for it.
	//
	// Empty selects cmd.SessionDefaultMinStability — `stable`. That is
	// stricter than the interactive CLI's default, and deliberately so: a
	// request is unattended automation, nobody reads the `[Experimental]`
	// badge on its behalf, and an experimental command's shape can change
	// under a caller that pinned no dtctl version. Ask for `experimental`
	// explicitly when the host is prepared to re-test on upgrade.
	MinStability string
	// StabilityExceptions admit individual below-floor commands and flags,
	// each written as a command path optionally suffixed with one flag
	// ("get breakpoints", "query --decode-snapshots"). Naming a command also
	// admits the flags that are below the floor only because that command is;
	// a flag with a weaker contract of its own needs its own entry.
	//
	// Prefer this to lowering MinStability: it keeps the request's widened
	// surface to what the host has actually tested, instead of also picking up
	// whatever lands in the experimental tier in the next release. Unlike the
	// floor, there is no environment variable for exceptions — this field is
	// the only way to express them in engine mode.
	StabilityExceptions []string
	// NoDeprecated makes this request refuse every deprecated command and
	// flag, as if the removal release had already happened — so a host can
	// find out in its own test suite that it still depends on something
	// scheduled to go, rather than on the day it is removed.
	//
	// Not a stability level: a deprecated command is stable in shape and
	// satisfies any floor, it merely has a removal date. Off by default.
	NoDeprecated bool

	// Files is the request's virtual filesystem: every file argument
	// (`-f x.yaml`, `--data-file`, ...) resolves against it, and files the
	// command writes (e.g. `apply --write-id` stamping an id back into the
	// source) land in it. Paths are normalized, so "x.yaml", "./x.yaml" and
	// "/x.yaml" are the same file. nil is an empty filesystem — not the host
	// disk: a user-named path resolves here or not at all, which the
	// TestUserFilePathsGoThroughVFS guard enforces across cmd/ and pkg/.
	// (Host state dtctl owns — a temp file, a cache — is a separate matter;
	// the commands that use it are blocked or ungranted in a service.)
	Files map[string][]byte
	// Stdin feeds commands that read standard input (`-f -`). nil is an empty
	// stream (immediate EOF) — never the host process's stdin.
	Stdin []byte

	// Env sets additional environment variables for the run (e.g.
	// DTCTL_OUTPUT). Applied after credential scrubbing, so entries here win;
	// grant with care — the engine deliberately does not expose this in its
	// HTTP reference wrapper. Profile, when set, overrides an Env entry for
	// DTCTL_PROFILE.
	Env map[string]string
}

// Result is the outcome of one execution. Stdout and Stderr are byte-identical
// to what the dtctl CLI would print for the same command line, credentials,
// and files — including --agent envelopes, table layouts, and error messages.
type Result struct {
	// ExitCode is the CLI process exit code (0 success; sysexits-style codes
	// for errors, e.g. 64 usage, 77 permission).
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	// Files is the complete final state of the request's virtual filesystem:
	// the input files plus anything the command wrote or rewrote.
	Files map[string][]byte
	// Truncated is true when stdout or stderr was cut at MaxOutputBytes.
	// The output is still valid up to the cap; callers should surface this
	// so users know the result is incomplete.
	Truncated bool
}

// Execute runs one dtctl invocation and returns its outcome.
//
// A non-nil error with a nil Result means the request never ran: it is
// malformed (no command, missing tenant credentials, an unparsable command
// string), was shed at admission, or its context ended before it started.
// Everything after the run starts — including command failures — is expressed
// CLI-style in Result: exit code plus stdout/stderr. The one exception is an
// Engine built with WithConcurrentExecution whose context ended while the
// command ran: it returns the Result as produced together with the context's
// error, whatever the exit code (a cancelled query exits 0).
func Execute(ctx context.Context, req Request) (*Result, error) {
	return ExecuteWithLimits(ctx, req, DefaultLimits())
}

// Engine executes commands against its own admission budget.
//
// By default an Engine runs one invocation at a time, exactly like the
// package-level Execute. Built with WithConcurrentExecution, it runs several
// invocations at once in this process, each on a command tree of its own
// (cmd.RunOptions.Concurrent) — see docs/dev/CONCURRENT_EXECUTION.md for what that
// costs and what it guarantees. The zero value is not usable; build one with New.
type Engine struct {
	limits     Limits
	adm        *admission
	concurrent bool
}

// Option configures an Engine at construction.
type Option func(*engineOptions)

type engineOptions struct {
	concurrent    bool
	maxConcurrent int
}

// WithConcurrentExecution opts an Engine into running up to maxConcurrent
// invocations at once in this process (a value below 1 counts as 1). It is the
// only way in: no Limits field and no default turns it on, so the CLI,
// `dtctl serve`, the package-level Execute and every Engine built without it
// keep running one invocation at a time. Meant for a multi-tenant service that
// has sized its memory for it.
func WithConcurrentExecution(maxConcurrent int) Option {
	return func(o *engineOptions) {
		o.concurrent = true
		o.maxConcurrent = max(maxConcurrent, 1)
	}
}

// New builds an Engine with the given budget. Without WithConcurrentExecution
// it serializes, exactly like the package-level Execute, but with an admission
// queue of its own.
func New(limits Limits, opts ...Option) *Engine {
	o := engineOptions{maxConcurrent: 1}
	for _, opt := range opts {
		opt(&o)
	}
	l := limits.withDefaults()
	// MaxQueued counts requests waiting *or* running, so a queue shallower
	// than the concurrency limit would shed requests while slots sat idle.
	// Raising it is the only reading of that combination that is not a bug.
	if l.MaxQueued < o.maxConcurrent {
		l.MaxQueued = o.maxConcurrent
	}
	return &Engine{
		limits:     l,
		adm:        &admission{slot: make(chan struct{}, o.maxConcurrent)},
		concurrent: o.concurrent,
	}
}

// MaxConcurrent reports how many invocations this engine runs at once: 1
// unless it was built with WithConcurrentExecution.
func (e *Engine) MaxConcurrent() int { return cap(e.adm.slot) }

// Execute runs one invocation against this engine's budget. Its Result and
// error read as the package-level Execute documents.
func (e *Engine) Execute(ctx context.Context, req Request) (*Result, error) {
	return executeInner(ctx, req, e.limits, e.adm, e.concurrent)
}

// Limits reports the budget this engine was built with, after defaults.
func (e *Engine) Limits() Limits { return e.limits }

// executeInner is the shared implementation called by ExecuteWithLimits.
func executeInner(ctx context.Context, req Request, limits Limits, adm *admission, concurrent bool) (*Result, error) {
	argv, err := req.argv()
	if err != nil {
		return nil, err
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	if limits.MaxFileBytes > 0 {
		var total int64
		for _, data := range req.Files {
			total += int64(len(data))
		}
		if total > limits.MaxFileBytes {
			return nil, fmt.Errorf("engine: request files total %d bytes, exceeds MaxFileBytes (%d)", total, limits.MaxFileBytes)
		}
	}
	// Apply the duration budget. The timeout context is threaded into the
	// command tree (via RunOptions.Context) so long-running loops that observe
	// cmd.Context() are cancelled when the budget elapses.
	timeoutCtx, cancelTimeout := context.WithTimeout(ctx, limits.MaxDuration)
	defer cancelTimeout()

	// Admission: bound queue depth and acquire the execution slot in a
	// context-aware way so cancelled requests (including timed-out ones) do
	// not block behind a long queue.
	// Add's return value is the count *after* this request's increment, so
	// concurrent arrivals each see their own consistent snapshot. A separate
	// Load() here would race: a request that was admissible at increment time
	// could read a counter a later arrival had already bumped further, and
	// reject itself for a slot that was in fact still available.
	if adm.queued.Add(1) > int64(limits.MaxQueued) {
		adm.queued.Add(-1)
		return nil, ErrTooManyQueued
	}
	select {
	case adm.slot <- struct{}{}:
	case <-timeoutCtx.Done():
		adm.queued.Add(-1)
		return nil, timeoutCtx.Err()
	}
	defer func() {
		<-adm.slot
		adm.queued.Add(-1)
	}()

	// Catch the race where the deadline fired between acquiring the slot and
	// starting the run.
	if err := timeoutCtx.Err(); err != nil {
		return nil, err
	}

	env := make(map[string]string, len(req.Env)+1)
	for k, v := range req.Env {
		env[k] = v
	}
	if req.Profile != "" {
		env[config.ProfileEnvVar] = req.Profile
	}

	// Always a reader, never nil: a nil RunOptions.Stdin leaves the *host*
	// process's stdin in place, which a request must never see (and which can
	// block forever on an open pipe, wedging the serialized engine). A nil
	// req.Stdin yields an empty reader, i.e. immediate EOF.
	stdin := io.Reader(bytes.NewReader(req.Stdin))

	files := vfs.NewMapFS(req.Files)
	stdout := &cappedBuffer{limit: limits.MaxOutputBytes}
	stderr := &cappedBuffer{limit: limits.MaxOutputBytes}
	started := false
	code := cmd.Run(argv, cmd.RunOptions{
		// Grant nothing: no plugins, shell aliases, hooks, editors, or
		// browser opens. Everything a request needs happens in-process.
		Capabilities: &cmd.Capabilities{},
		Session: &cmd.Session{
			EnvironmentURL: req.EnvironmentURL,
			Token:          req.Token,
			SafetyLevel:    config.SafetyLevel(req.SafetyLevel),
			// Passed as typed fields rather than through env: the floor decides
			// which commands exist for this request, so it must come from the
			// request and not from whatever DTCTL_MIN_STABILITY the host process
			// happens to carry (which Session scrubs for exactly that reason).
			MinStability:        config.StabilityLevel(req.MinStability),
			StabilityExceptions: req.StabilityExceptions,
			NoDeprecated:        req.NoDeprecated,
		},
		Env:             env,
		FS:              files,
		Stdin:           stdin,
		Stdout:          stdout,
		Stderr:          stderr,
		BlockedCommands: unsupportedCommands,
		Context:         timeoutCtx,

		// Overlapping invocations only when the engine was built with
		// WithConcurrentExecution; everything else stays serialized.
		Concurrent: concurrent,
		OnStart:    func() { started = true },
	})
	// Only a concurrent Run gives up before starting: its context ended while a
	// serialized invocation held the lock.
	if !started {
		if err := timeoutCtx.Err(); err != nil {
			return nil, err
		}
	}

	res := &Result{
		ExitCode:  code,
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		Files:     files.Files(),
		Truncated: stdout.truncated || stderr.truncated,
	}

	// The context is the invocation's only deadline. When it ended the run, say
	// so, rather than leave the caller to infer it from whatever the command
	// made of being cut off: a query reports "Query cancelled." and exits 0.
	// The Result still comes back, since the command may have written before it
	// was cut off. Only a concurrent engine does this; the serialized one
	// reports what the command returned, as it always has.
	if concurrent {
		if err := contextEnded(timeoutCtx); err != nil {
			return res, err
		}
	}
	return res, nil
}

// argv resolves the request's argument vector: Argv verbatim when set,
// otherwise Command split with POSIX shell rules.
func (r *Request) argv() ([]string, error) {
	if len(r.Argv) > 0 {
		return r.Argv, nil
	}
	if r.Command == "" {
		return nil, errors.New("engine: request has no command (set Command or Argv)")
	}
	argv, err := shlex.Split(r.Command)
	if err != nil {
		return nil, fmt.Errorf("engine: parse command: %w", err)
	}
	if len(argv) == 0 {
		return nil, errors.New("engine: request has no command (set Command or Argv)")
	}
	return argv, nil
}

// validate fails fast on request-shape problems, so malformed requests
// surface as Go errors instead of a CLI error transcript. The same
// constraints are enforced again inside the run (cmd.Session.validate) —
// this is the caller-friendly first line.
func (r *Request) validate() error {
	if r.EnvironmentURL == "" {
		return errors.New("engine: EnvironmentURL is required")
	}
	if r.Token == "" {
		return errors.New("engine: Token is required")
	}
	if r.SafetyLevel != "" {
		valid := false
		for _, l := range config.ValidSafetyLevels() {
			if config.SafetyLevel(r.SafetyLevel) == l {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("engine: invalid safety level %q", r.SafetyLevel)
		}
	}
	if r.MinStability != "" {
		level, err := config.ParseStabilityLevel(r.MinStability)
		if err != nil || level == config.StabilityDevelopment {
			return fmt.Errorf("engine: invalid minimum stability %q; valid levels are "+
				"experimental, stable (development surface is not reachable from a request)",
				r.MinStability)
		}
	}
	// Rejected, not ignored: an exception the engine silently dropped would
	// look like the command is simply gone, and the host would have no way to
	// tell a typo from a tier it misjudged.
	if _, err := stability.ParseExceptions(r.StabilityExceptions); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	return nil
}

// contextEnded reports why ctx has ended, or nil while it has not. The clock is
// consulted as well as the context's own error: the requests the invocation
// sent end at the same deadline on timers of their own, and one of those can
// fire, and the command return, a moment before this context's timer does.
func contextEnded(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
		return context.DeadlineExceeded
	}
	return nil
}

// ExecuteWithLimits runs one invocation against this engine's admission
// budget but with per-request limits. It is how a caller applies the
// deadline the request asked for — Limits.MaxDuration becomes the context
// timeout — without building a new Engine (and a new slot) per request. A
// zero field in limits is filled from the engine's own budget, not from
// DefaultLimits, so a host that sized MaxOutputBytes at construction keeps it
// on a request that only names a MaxDuration. The context is the invocation's
// only deadline: a concurrent invocation carries it on every outgoing request,
// and nothing preempts code between requests. A concurrent invocation the
// deadline cut off returns its Result together with the context's error (see
// Execute).
func (e *Engine) ExecuteWithLimits(ctx context.Context, req Request, limits Limits) (*Result, error) {
	return executeInner(ctx, req, e.effectiveLimits(limits), e.adm, e.concurrent)
}

// effectiveLimits is the budget one ExecuteWithLimits call runs under: the
// request's limits, with the engine's filling in what the request left out.
func (e *Engine) effectiveLimits(limits Limits) Limits {
	// Admission is a property of the engine, not of a request: the slot
	// channel was sized at construction and MaxQueued guards it, so a request
	// may not raise or lower it. Taking it from limits would also mean a
	// caller that set only MaxDuration silently got MaxQueued=4 from the
	// defaults, shedding requests the engine had capacity for.
	merged := limits.withDefaultsFrom(e.limits)
	merged.MaxQueued = e.limits.MaxQueued
	return merged
}
