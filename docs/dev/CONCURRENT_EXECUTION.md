# Per-invocation command trees and state

dtctl runs as a one-shot CLI and as an in-process library (`cmd.Run`,
`pkg/engine`, `dtctl serve`). The library path serializes invocations on a
mutex, because an invocation owns the whole process while it runs: the command
tree is a set of package-level `cobra.Command` values wired by `init()`, flag
values are package variables, and the per-run state — streams, environment,
filesystem, session, capabilities — is swapped process-wide for the duration of
the run. See [SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md)
("Serialization").

A host that evaluates many invocations in one process, most of them waiting on a
platform round trip, needs each invocation to carry a tree and state of its own
instead. This document describes the pieces that make that possible and how a
host [opts in](#opting-in). The pieces are behavior-neutral for the CLI and for
the serialized engine: every accessor falls back to the process-level default
when an invocation carries nothing of its own, and nothing turns concurrent
execution on by default.

## The command tree

Every command is a constructor, `func newXCmd() *cobra.Command`, that carries
the command's whole wiring: flags, required marks, hooks and stability tier. A
flag specific to one command binds to a variable local to its constructor and
captured by the command's `RunE`.

The CLI's tree is still the one `init()` wires, built from those constructors
(`var getBucketsCmd = newGetBucketsCmd()`, attached to its parent in `init()`).
`newCommandTree` (`cmd/root_factory.go`) builds a second, complete, freshly
allocated tree from the same constructors, so nothing one tree's flags parse is
visible to another.

Flags whose storage is shared with code outside the constructor — the root
persistent flags, `--dry-run`, and `get`'s `--limit` and `--fields` — bind to
storage on the invocation. That storage is **passed to the constructors**: a
command that is being built has no context yet to find the invocation through.

The fresh tree leaves out two things on purpose. Shell-completion registrations
are kept by cobra in a process-wide map that is never pruned, so registering
them per tree would retain every tree for the life of the process. And
development-tier commands are attached to the singleton's parents and cannot be
grafted onto a fresh tree.

`TestNewCommandTreeMatchesSingleton` holds the two trees together: the same
commands, flags, required marks, hooks and stability tiers. Wiring added to an
`init()` instead of a constructor fails it.

## Invocation state on the context

An `invocation` (`cmd/invocation.go`) carries one run's streams, environment
overlay, filesystem, session, blocked commands, capabilities, root flag values
and tracing context. `Run` attaches it to the context it threads into the
command tree, cobra hands that context to every command, and a command reaches
it with `cmdContext(cmd)`. Nothing is keyed by goroutine, so goroutines a command
starts inherit the invocation along with the context they are given.

| State | Accessor | Falls back to |
|---|---|---|
| stdout, stderr, stdin | `currentStdout`, `currentStderr`, `currentStdin` | `os.Stdout`, `os.Stderr`, `os.Stdin` |
| environment | `getenv`, `lookupEnv`, `withInvocationEnv` | the process environment |
| user-supplied files, `-` | `vfsEnv` (a `vfs.Env`) | the installed `vfs` and `os.Stdin` |
| session, blocked commands, capabilities | `currentSession`, `currentBlocked`, `currentCaps` | the package-level values `Run` maintains |
| root flag values | `curFlags` | `gFlags`, where the singleton tree binds them |

An invocation that has ended is invisible to the accessors. cobra keeps the
context a serialized `Run` bound the singleton tree to, so a later direct call to
a command would otherwise find the finished invocation's session and streams.

Library helpers that used to write to the process streams or read the process
filesystem take them explicitly, and `cmd` hands them the invocation's:
`output.NewPrinterWithOpts` (`Writer`, `Notice`), `output.NewProgressReporterTo`,
`prompt.ConfirmWith`, `exec.DQLExecutor.WithStreams` and `WithVFS`,
`apply.Applier.WithStderr` and `WithVFS`, `vfs.Env`, and `session.Config.WithEnv`.
Each existing entry point delegates with the process defaults.

## The deadline

On a tree of its own the request's context is the only deadline. The SDK builds
its client with a six-minute timeout and nearly every resource handler passes
`context.Background()`, so a context deadline would otherwise reach almost no
request. `bindClientContext` therefore joins the invocation's context to every
request whose own context cannot end. A request whose context *can* end is left
alone: it is either derived from the command's context already, or it is a bound
the SDK set on purpose — the query execute outlives a cancellation by a short
grace so that a query it started can still be cancelled on the backend.

Nothing preempts in-process code between requests: a command that loops between
calls runs on until its next call.

## Rules for request-path code

1. **No package-level mutable state** on a request path, including wiring done in
   `init()`. Guard: `TestNewCommandTreeMatchesSingleton`.
2. **No writes to the process streams.** In `cmd/` write to `currentStdout(ctx)` /
   `currentStderr(ctx)` or the command's own writers; a `pkg/` type takes its
   writers from its caller. Guards: `TestNoProcessStreamWritesOnRequestPaths`,
   `TestRequestPathsInjectTheirStreams`.
3. **A command reads the tree it runs from**, not the singleton, and takes its
   per-run state from the context, never from a package variable. Guards:
   `TestConcurrentSurfaceReadBleed`, `TestConcurrentCommandSpecificFlagBleed`,
   `TestConcurrentRunNeverLacksACommandContext`.

## Adding a command

1. Write `newXCmd()`: flags bound to locals, stability tier and hooks inside.
2. Keep `var xCmd = newXCmd()` and attach it to its parent in `init()`.
3. Add it to `newCommandTree` in `cmd/root_factory.go`.
4. In the body, start from `cmdContext(cmd)`; print through `currentStdout(ctx)`
   and `currentStderr(ctx)`; build printers, executors and appliers with the
   helpers in `cmd/invocation.go`.

`TestNewCommandTreeMatchesSingleton` fails if steps 2 and 3 disagree.

## Opting in

`RunOptions.Concurrent` runs one invocation on a tree and state of its own,
without the whole-run lock. `pkg/engine` exposes it as an engine option:

```go
eng := engine.New(engine.Limits{MaxQueued: 64}, engine.WithConcurrentExecution(32))
res, err := eng.ExecuteWithLimits(ctx, req, limits)
```

`WithConcurrentExecution(n)` is the only way in: no `Limits` field and no default
turns it on, so the CLI, `dtctl serve`, the package-level `Execute` and an
`Engine` built without the option keep running one invocation at a time
(`TestConcurrencyIsOptIn`). An `Engine` owns its admission budget — a slot per
concurrent invocation and a queue depth, with `MaxQueued` raised to `n` if it is
lower, since it counts requests waiting *or* running — so independent callers in
one process no longer share one queue.

`Run` takes `runMu` shared for a concurrent invocation and exclusive for a
serialized one. A process therefore runs either one serialized invocation or any
number of concurrent ones, never a mix. That is what lets cobra's global
`OnInitialize` hook tell them apart: it cannot be told which invocation it runs
for, so a count of active concurrent runs (`concurrentActive`) makes it skip
`initConfig`'s process-wide half, which a concurrent tree runs itself, with its
own context, from its root's `PersistentPreRunE`.

A panic in a command fails its own invocation (exit code `ExitError`, message on
its stderr) rather than the embedding process. The recovery covers the goroutine
the command runs on; a panic in a goroutine the command starts still ends the
process, as it does in the CLI.

A serialized invocation (the package-level `Execute`, `dtctl serve`) and
concurrent ones exclude each other on `runMu`. A concurrent request's wait for
it ends with its context: while a serialized invocation runs, every new
concurrent request holds its admission slot and waits, its `MaxDuration` already
counting, and when that ends first it returns the context's error and a nil
`Result` without having run (`Run` exits `ExitError` before calling `OnStart`,
and its wait leaves the lock's queue holding nothing, so a request that gives up
leaves no goroutine behind). A process that opts in to concurrent execution
should not also run serialized invocations.

When the context ended the run, a concurrent engine returns the context's error
instead of leaving the caller to infer it from what the command made of being
cut off — a query, for one, reports "Query cancelled." and exits 0. It returns
the `Result` as well, whatever the exit code: the command may have written
before it was cut off (a `create` whose POST succeeded, its `Files`), and a host
that retries on a timeout needs to see that. A nil `Result` with the error still
means the request never ran. The check also consults the clock, so a command
that returns on the deadline is reported as cut off. The serialized engine
reports what the command returned, with a nil error, as it always has.

## Gates

Each of these has failed at least once during development, and each is held by a
test that runs under `-race`.

| Test | What it holds |
|---|---|
| `cmd` `TestNewCommandTreeMatchesSingleton` | the fresh tree is the singleton's surface |
| `pkg/engine` `TestConcurrentEqualsSerialized` | a corpus run concurrently many times over, at two stability floors, is byte-identical to the serialized engine in a pristine process, and nothing reaches the host's own stdout/stderr |
| `pkg/engine` `TestConcurrentOutputEqualsCLI` | the same corpus against the real CLI binary |
| `pkg/engine` `TestConcurrentDryRunSendsNoWrite` | `--dry-run` sends no write, with or without `--plain`/`--yes` |
| `pkg/engine` `TestConcurrentTenantsStayIsolated` | tenants running at once each reach only their own environment, with only their own token |
| `pkg/engine` `TestConcurrentRequestDecidesItsOwnSurface` | a request's profile applies, and the host's `DTCTL_MIN_STABILITY` does not |
| `pkg/engine` `TestConcurrentCommandSpecificFlagBleed`, `TestConcurrentSurfaceReadBleed` | overlapping runs of one command that differ only in a flag each see their own value, and `commands` keeps reporting the masked surface while peers run |
| `pkg/engine` `TestConcurrentDeadlineEndsAStalledRequest` | a request whose upstream never answers returns at the context deadline, including from handlers that call the SDK with `context.Background()` |
| `pkg/engine` `TestConcurrentWaitForSerializedEndsAtTheDeadline` | a concurrent request queued behind a serialized invocation returns at its own deadline, never having run |
| `pkg/engine` `TestConcurrentDeadlineKeepsTheResult` | a command the deadline cut off returns its `Result` with the context's error, even at exit 0 |
| `pkg/engine` `TestConcurrencyIsOptIn`, `TestSerializedEngineStillSerializes` | nothing makes the default engine concurrent |
| `pkg/engine` `TestConcurrentOutputNeverCarriesColour` | the host's `FORCE_COLOR` (or a terminal on its stdout) does not colour a response |

## Costs

Measured on a 4-vCPU x86 host (two cores, two hyperthreads each) against a local
mock environment, with `get workflows`:

- **Memory.** About 0.7–0.9 MiB of live heap per in-flight command
  (`TestConcurrentMemoryFootprint`). Measured from outside the process, resident
  memory is about 2 MiB per command, since GC headroom and goroutine stacks come on
  top; a native dtctl process per command is about 25 MiB resident, whether it is
  computing or waiting on a response.
- **CPU.** About 1.9 ms per command in the serialized engine and 3.2 to 3.9 ms in
  concurrent mode (1 to 64 in flight): roughly twice, because each command builds
  a tree. A process per command costs about 12 ms of CPU on the same host.
- **Instance-per-request WebAssembly** (`GOOS=wasip1` under wazero, one fresh
  instance per command, a precompiled module, instance memory outside the Go
  heap) costs about 55 ms of CPU just to start the instance, of which about 16 ms
  is copying the module's 22 MiB data segment, plus about 35 MiB per in-flight
  instance and a roughly 400 MiB per-process baseline for the compiled module.
  Instances also cannot reach the network without a host-side bridge, since
  `wasip1` has no sockets.

## Limitations

1. **The tree is wired twice.** The CLI's tree is attached in `init()` and the
   fresh tree in `newCommandTree`. `TestNewCommandTreeMatchesSingleton` holds them
   together, but one factory both use would remove the duplication.
2. **Colour resolution is process-wide.** `pkg/output` decides colour once per
   process from the host's `NO_COLOR`, `FORCE_COLOR` and terminal, none of which
   a concurrent invocation prints to, and the session's scrub of those variables
   lives in the invocation's overlay, which `pkg/output` does not read. A
   concurrent `Run` therefore pins colour off for the process
   (`output.PinColorOff`); a serialized run resets the cache for itself. Agent
   mode is plain regardless.
3. **A hung command cannot be killed.** The context deadline ends the HTTP
   requests a command has in flight; nothing preempts arbitrary in-process code,
   and the query execute is deliberately detached for a short grace.
4. **Invocations share a heap.** An out-of-memory kill takes every request in
   flight. The host has to bound memory itself: `GOMEMLIMIT`, a concurrency limit
   sized against the container, and output caps (`Limits.MaxOutputBytes`).
5. **Host settings still reach requests in a few places.** `pkg/tracing` reads
   `OTEL_*` and `TRACEPARENT` from the process, and the `commands` catalog lists
   plugins found on the host's `PATH`. Neither carries tenant data. The scrub
   of `sessionScrubbedEnvVars` is an overlay on the concurrent path, so a
   `DTCTL_*` variable something reads with `os.Getenv` outside the overlay
   accessors still sees the host's value: `sdk/session`'s keyring and
   token-storage switches (`DTCTL_DISABLE_KEYRING`, `DTCTL_TOKEN_STORAGE`),
   which a sealed session config never reaches. A request's output does not
   depend on any of them, and a session-backed invocation reads no host config
   file: alias resolution, the one stage that loads it before the session is
   consulted, skips the load (`TestSessionInvocationsReadNoHostConfig`).
