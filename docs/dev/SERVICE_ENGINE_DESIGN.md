# dtctl as a Service — Engine Design

**Status:** Implemented; `dtctl serve` is a development-tier feature, registered
only when opted in (see [Maturity](#maturity))
**Created:** 2026-08-13
**Audience:** anyone changing `cmd/`, adding a command, or reading a user-supplied file.

> **Implementation:** `cmd/run.go` (`Run`, `RunOptions`, tree isolation, serialization),
> `cmd/root_factory.go` and `cmd/invocation.go` (a tree and state per invocation,
> see [CONCURRENT_EXECUTION.md](CONCURRENT_EXECUTION.md)),
> `cmd/capabilities.go` (subprocess gate), `cmd/session.go` (per-request tenant),
> `cmd/stdio.go` (stream redirection), `cmd/blocked.go` (surface mask),
> `pkg/vfs/` (file seam), `pkg/engine/` (the embedding surface),
> `pkg/serve/` (`dtctl serve http`, reference server).

## Overview

dtctl runs as a one-shot CLI *and* as an in-process library that a service can
call once per request. The same command tree, the same code paths, the same
bytes on stdout — the only difference is where the invocation's credentials,
files, and streams come from.

The motivating use cases are AI agents and automation that already know how to
drive dtctl on a terminal: a Dynatrace Workflow action, an agent gateway, an
MCP server. Rather than growing a second, parallel API surface that drifts from
the CLI, an embedding host hands over **a command line** and gets back **what
the CLI would have printed**.

```go
res, err := engine.Execute(ctx, engine.Request{
    Command:        `apply -f workflow.yaml --write-id --agent`,
    EnvironmentURL: "https://abc12345.apps.dynatrace.com",
    Token:          tenantToken,
    Files:          map[string][]byte{"workflow.yaml": src},
})
// res.Stdout is byte-identical to local CLI output;
// res.Files["workflow.yaml"] now carries the stamped id.
```

## Goals

1. **The CLI is the API.** No second command surface, no second output format.
   Anything a user can type, a service can send.
2. **Byte-identical output.** An agent moving between local CLI and service must
   not be able to tell the difference. Enforced by a test, not by review.
3. **Multi-tenant per invocation.** Each request carries its own environment and
   token. Nothing about the host process's identity leaks into a request, and
   nothing about one request survives into the next.
4. **Host abilities are opt-in.** Subprocesses, editors, browsers, the host disk
   — all off by default for embedded callers, granted explicitly by the CLI.
5. **No new trust in the host.** A service must not be able to make dtctl read
   the server's disk, credentials, or environment on a tenant's behalf.

## Non-Goals

- **A security sandbox.** The engine is a *correctness* boundary inside one
  process, not an isolation boundary. It does not contain a hostile *command*
  string; it prevents an honest one from reaching host state. Untrusted callers
  belong behind a process or WASM boundary.
- **Per-request preemption.** A run that has started cannot be killed
  mid-flight; in-process Go code cannot be preempted safely.
- **Intra-process parallelism by default.** Invocations serialize unless the host
  opts in; see "Serialization" below. Otherwise scale with more instances or
  processes.
- **Authentication.** `pkg/serve` has none of its own. A deployment puts its own
  authn/TLS/rate limiting in front, or embeds `pkg/engine` directly.

## The five seams

Each seam replaces one thing a CLI process normally takes from its environment.
All are per-invocation and restored afterwards.

| Seam | `RunOptions` field | Default (CLI) | Embedded |
|---|---|---|---|
| Command tree | — | pristine per run | same, or a fresh tree per invocation when concurrent |
| Subprocesses | `Capabilities` | all granted | none granted |
| Credentials + config | `Session` | host config, contexts, keyring | request's URL + token, host scrubbed |
| Environment | `Env` | host environment | host + explicit per-request entries |
| Files | `FS` | host filesystem | request's virtual files (`vfs.MapFS`) |
| Streams | `Stdout`/`Stderr`/`Stdin` | real process stdio | caller's writers/reader |

Surface reduction is a sixth, orthogonal knob: `BlockedCommands` removes
host-only commands from the tree per invocation.

### Serialization

The command tree is **package state** — 277 command values wired by `init()`.
Invocations therefore serialize on a package mutex (`cmd.runMu`), and that
single fact is what makes the rest of the design safe: swapping process
streams, mutating environment variables, and installing a filesystem are all
process-wide operations that would be indefensible under concurrency.

Consequences to internalize before "optimizing" this:

- A server handles one dtctl command at a time per process. `dtctl serve http`
  says so in its help text.
- Parallelism comes from more instances or more processes — or from the host
  opting in to concurrent execution (below) — never from more goroutines against
  the singleton tree.
- A server must not be started *inside* an invocation — it would hold the lock
  for its lifetime and deadlock its own first request. This is why `main`
  dispatches `serve` before the command pipeline, and why `dtctl serve http`
  refuses to run when `cmd.RunActive()` reports an invocation in progress.

### Concurrent execution (opt-in)

`RunOptions.Concurrent`, reached through
`engine.New(limits, engine.WithConcurrentExecution(n))`, runs an invocation on a
command tree and state of its own instead of the singleton, so up to `n`
invocations overlap in one process. Streams, environment, filesystem, session and
capabilities travel on the invocation's `context.Context` rather than being
swapped process-wide, and the context is the invocation's only deadline. A process
runs either one serialized invocation or any number of concurrent ones, never a
mix. It is off unless a host asks for it; what it guarantees, what it costs and
what it does not cover are in [CONCURRENT_EXECUTION.md](CONCURRENT_EXECUTION.md).

### Upstream connections are pooled across requests

Every request builds its own client, but all of them send through one
process-wide `http.Transport` (`sdk/session/transport.go`), so repeated
requests to a tenant reuse a warm connection instead of paying a TCP + TLS
handshake each time (#576). That is the one piece of client state a request
shares, and it holds nothing tenant-specific: the credential is a per-request
`Authorization` header, there are no client certificates, and net/http keys
the pool by proxy, scheme and host, so two environments never share a
connection (and requests that do share one still send their own token).
The per-client parts — timeout, redirect policy, cookie jar — stay per client.

The transport sits behind a wrapper so resty's in-place mutators
(`SetProxy`, `SetTLSClientConfig`, `SetCertificates`, …) cannot reach it: on a
bare shared transport, one client calling them would rewrite TLS trust or the
proxy for every request in the process. On a session client they cannot apply,
and they do not fail silently either: every later request on that client
returns `session.ErrSharedTransportSetting` and sends nothing, so a required
egress proxy or pinned CA is never quietly dropped. A consumer that needs its
own TLS or proxy settings installs a private transport first —
`HTTP().SetTransport(session.NewTransport())` — and then calls the setters.
`http.Client.CloseIdleConnections` is forwarded to the pool, so it closes idle
connections for every client; that is harmless, since an idle connection is a
cache. The proxy itself still comes from
`HTTPS_PROXY`/`NO_PROXY` as before — read once per process by net/http, so a
per-request `Env` entry never changed it.

### Restriction has five independent axes

Do not conflate these; each answers a different question.

| Axis | Question | Mechanism | Agent error code |
|---|---|---|---|
| Safety level | What may this command *do* to the tenant? | `pkg/safety` checks | `safety_blocked` |
| Profile | Which commands *exist* for this caller? | `applyProfile` mask | `profile_blocked` |
| Stability floor | Which commands *carry a contract* here? | `pkg/stability` mask | `stability_blocked` |
| Environment | Which commands *make sense* here at all? | `BlockedCommands` mask | `unsupported_in_service` |
| Capability | Which *host abilities* may this process use? | `cmd.Capabilities` gates | `capability_disabled` |

The masks compose — profile, stability and environment all apply to a run. The
environment mask additionally guards non-runnable parents, so a blocked
`config` cannot answer with its own help text and exit 0.

#### The stability floor defaults *stricter* in the engine

`Request.MinStability` defaults to `stable`, not to the CLI's `experimental`
(`cmd.SessionDefaultMinStability`). The middle tier exists so that a human — or
an interactive agent showing the human a `[Experimental]` badge — can knowingly
use surface that may change in any minor release. A request has nobody to show
the badge to: it is unattended automation, usually against a dtctl version the
caller did not pin. Defaulting it into the experimental tier would let a minor
upgrade change a shape the caller's own API had already promised onward.

Two per-request fields widen it, and the second is almost always the right one:

- `MinStability: "experimental"` grants the whole tier, including commands and
  flags that join it in a later release.
- `StabilityExceptions: []string{"inventory", "query --decode-snapshots"}`
  grants exactly what the host has tested. There is no environment variable for
  exceptions, so this field is the only way to express them in engine mode.

`development` is rejected as a floor value. That tier is gated at
*registration* — a stage earlier than the floor — and a session offers no
opt-in for it, so accepting the value would return an `experimental`-sized
surface to a caller who believed it had asked for more.

`DTCTL_MIN_STABILITY` and `DTCTL_DEVELOPMENT` are in `sessionScrubbedEnvVars`
for the same reason `DTCTL_PROFILE` is: they decide which commands exist, and
that is the request's decision, not the host process's. A host that exported
either one for its own CLI use must not thereby reshape every tenant's surface.

An embedded agent discovers its own floor from `dtctl commands`, which reports
`min_stability` and omits everything below it — so a block reads as "not
available to you here", not as "this command does not exist".

## Rules for contributors

These are the invariants the design rests on. Each has a guard test, because
each was violated at least once during development — including twice in the
implementing PR itself.

### 1. User-supplied file paths go through `pkg/vfs`

```go
// NO — reads the server's disk on a service request
content, err := os.ReadFile(flagValue)

// YES
content, err := vfs.ReadFile(flagValue)
content, err := vfs.ReadFileOrStdin(flagValue) // when "-" means stdin
```

Applies to every path a **user named**: `-f`, `--file`, `--data-file`, query
files, files to `diff`, and writebacks like `apply --write-id`. Under an embedded
invocation those files exist only in the request; a direct `os.ReadFile` silently
reads the *server's* filesystem instead — a broken feature and a traversal into
host state in one line.

The rule follows the path, not the package. `cmd/diff.go` read its `-f` flags
through the seam while the `pkg/diff` helper one call deeper used `os.ReadFile`,
which is the same hole with an extra stack frame — and `dtctl inspect` took a
path argument straight to `os.Open`. Both survived a `cmd/`-only guard; both are
why the guard now scans `pkg/` too.

Paths **dtctl chose itself** — editor round-trip temp files, the config file,
spill buffers — are host state and stay on `os`. That is the whole test: whose
file is it? Host state is only defensible while an embedded invocation cannot
reach it, so pair it with the thing that keeps it unreachable: a blocked command
(rule 6) or an ungranted capability (rule 2).

Also never open `/dev/stdin` as a path. The stream seam swaps the `os.Stdin`
*variable*; a path reaches the process's real fd 0, past the redirection, and
does not exist on Windows. Use `os.Stdin` (the variable) or
`vfs.ReadFileOrStdin`.

**Guard:** `TestUserFilePathsGoThroughVFS` (`cmd/vfs_guard_test.go`) fails on any
direct `os` file call under `cmd/` or `pkg/` outside an annotated allowlist,
where each entry names what keeps that path unreachable from a service request.

### 2. Never reach the host without a capability gate

Every path in `cmd/` that spawns or replaces the process goes through one of the
five gateway files, each gated on a `cmd.Capabilities` field. The same applies to
host-disk features that live outside the vfs seam: result spilling writes a file
the caller cannot read and that outlives the request, so it is gated on
`HostDiskSpill`. Embedded callers grant nothing, which makes those paths
structurally unreachable rather than merely discouraged.

A capability is the right tool when the ability is *conditional* on the host; a
blocked command (rule 6) is right when the whole command is meaningless without
one. `--spill` is the former (the rows come back inline instead); `inspect`, a
reader for spilled files, is the latter.

**Guard:** `TestSubprocessSpawnsConfinedToGateways` (`cmd/capabilities_test.go`)
confines `exec.Command`/`syscall.Exec` in `cmd/` to the gateway files.

### 3. Command bodies never call `os.Exit`

An in-process caller dies with it. Return an error — `*silentExitError` when you
need a specific exit code with no additional message.

**Guard:** `TestNoOsExitOutsideExecute` (`cmd/silent_exit_test.go`).

### 4. Credentials and config come from the invocation, not the process

Read them through `LoadConfig()`, which a `Session` transparently replaces with
a synthetic single-context config. Never reach for `os.Getenv("DTCTL_TOKEN")`,
the keyring, or a config path directly in a command — a session-backed request
has scrubbed all of them, and code that goes around `LoadConfig` would hand one
tenant's request the host's (or another tenant's) credentials.

### 5. Output goes to the process streams, and stays byte-identical

Print via `pkg/output`, `fmt.Print*`, or cobra — all of which resolve
`os.Stdout`/`os.Stderr` dynamically, so the stream seam catches them. Do not
cache a writer across invocations, and do not add host-environment-dependent
output (TTY probes are fine; they already resolve to "not a TTY" for a service).

**Guard:** `TestEngineOutputEqualsCLI` (`pkg/engine/equality_test.go`) builds the
real binary and diffs CLI vs. engine stdout/stderr/exit code.

### 6. Per-run mutation of the command tree must be restorable

If you mutate a command at runtime (hide it, wrap its `RunE`, flip
`DisableFlagParsing`), the pristine-tree snapshot in `cmd/run.go` must know
about the field, or the mutation leaks into the next invocation. Add the field
to the snapshot, and a leak test beside
`TestRunProfileMaskDoesNotLeakBetweenInvocations` (`cmd/run_test.go`).

### 7. A new top-level command decides whether it belongs in a service

Add it to `unsupportedCommands` in `pkg/engine/policy.go` with a reason if it
manages host-local state (config, keyring, shell, installed tools, an
interactive session) or would nest the service in itself. The reason string is
user-facing: it appears in the `unsupported_in_service` error's suggestions.

## `dtctl serve`

`serve` is a **parent** command; each protocol is a named subcommand. Today:

```
dtctl serve http     JSON over HTTP (POST /v1/execute, GET /healthz)
```

Bare `dtctl serve` prints help and exits 0 — naming the protocol is mandatory,
so no single protocol is the silent default and a future `dtctl serve mcp` lands
beside `http` rather than competing with an incumbent. An unrecognized protocol
name is an error with a non-zero exit, not a help dump, so a supervisor cannot
mistake a typo for a started server.

Adding a protocol: a new file in `pkg/serve/`, a `newXCommand()` constructor
registered in `NewCommand()`, and the same `cmd.RunActive()` guard in its
`RunE`. It calls `engine.Execute` per request and translates the result into its
own wire format — protocol servers hold no dtctl logic of their own.

## Deliberate divergences from the local CLI

The service is not a perfect mirror, and the differences are intentional:

- **Host-only commands are absent** (see the environment axis above).
- **AI-agent auto-detection is skipped** for session-backed runs. The host's
  environment must not shape a tenant's output format; envelopes are opt-in per
  request via `--agent`.
- **`DTCTL_PROFILE` and `DTCTL_OUTPUT` are scrubbed** along with the credential
  variables, for the same reason.
- **`Env` is not exposed over HTTP.** Arbitrary variables reach proxies,
  exporters, and other process-level behavior. Embedding hosts that need it use
  `engine.Request.Env` directly, in-process.

## Consuming `pkg/engine` from another module

`pkg/engine` lives in the **root** module, `github.com/dynatrace-oss/dtctl` — not
in `sdk/`, and it cannot move there: `engine.Execute` runs the real command tree,
so it depends on `cmd/`, which depends on every `pkg/resources/*`, which depends
back on `sdk/api/*`. The SDK is deliberately the opposite kind of artifact (a few
typed API wrappers, 8 direct dependencies, no CLI concerns); the engine pulls in
601 packages. A service embeds the CLI or it uses the SDK — those are different
choices, not two doors to the same room.

```go
import "github.com/dynatrace-oss/dtctl/pkg/engine"
```

```bash
go get github.com/dynatrace-oss/dtctl@latest
```

That works only because of a coupling worth stating explicitly, since it broke
silently once. This repo holds **two modules**, and Go resolves the inner one by
the tag `sdk/vX.Y.Z`. The root `go.mod` both requires the sdk module and
`replace`s it with `./sdk` — and **Go ignores a `replace` directive in a module
it is consuming as a dependency**. So in-repo builds and all of CI resolve the sdk
through the replace and never validate the `require`, while every external
importer resolves the `require` literally. For the module's whole life that line
read `v0.0.0-00010101000000-000000000000`, and `go get` on the root module failed
with `invalid version: unknown revision 000000000000`.

Three pieces keep it honest, and all three are load-bearing:

- The require carries `// x-release-please-version`, so release-please rewrites it
  on every release — the same generic-updater mechanism as
  `pkg/version/version.go`. The sdk require and the CLI version are therefore
  equal by construction.
- The `tag-sdk` job in `.github/workflows/release.yml` mirrors each release tag
  `vX.Y.Z` into `sdk/vX.Y.Z` at the same commit, which is the tag that require
  now names. `verify-consumable` then does the real thing from a scratch module
  outside the repo — `go get` the freshly tagged root module and build a program
  that imports `pkg/engine`.
- `TestSDKRequireIsResolvable` (`pkg/version/`) rejects a pseudo-version, a
  missing annotation, a missing replace, and drift between the require and
  `version.Version`.

Consequence for development: **only release tags are importable — `main` is not,
and there are two different windows in which it is broken.** In-repo both are
invisible, because the replace wins.

- Between a release and the next release PR, the require names the *previous*
  release's sdk tag. That tag exists, so a caller pinning a pseudo-version off
  `main` resolves it — and then fails to build if `main` has started using an sdk
  symbol added since.
- Between merging a release PR and the dispatch that actually tags it, the require
  names the *upcoming* sdk tag, which does not exist yet. A caller pinning off
  `main` there fails outright, the same `unknown revision` as the original bug.

Callers pin releases; the release is the one commit where both modules are cut and
both tags exist.

## Maturity

`pkg/engine` is the stable half: a Go caller opts into it at compile time, and
the isolation rules above are enforced by guard tests.

`dtctl serve` is **development-tier** (see [STABILITY.md](../STABILITY.md)) and
registered only when opted into — `dtctl config set development.serve on` or
`DTCTL_DEVELOPMENT=serve`; `serve.Enabled()` and the gate in `main`. Without it
the command does not exist — no help entry, no catalog entry, an ordinary
"unknown command". It graduates to a badged `experimental` command when the
items below are settled, because each one changes what an operator can rely
on:

- **Per-request deadline, with a caveat.** `engine.Limits.MaxDuration`
  (`DefaultLimits`: 5 min) wraps the request in a `context.WithTimeout`
  threaded into the command tree via `RunOptions.Context` /
  `rootCmd.ExecuteContext`, and most commands cancel cooperatively via
  `cmd.Context()`. But cancellation is opt-in per command body: one that never
  checks its context, or issues an HTTP call on a context of its own, still
  runs to completion and holds the single invocation slot regardless of the
  budget. `cmd/query.go`'s DQL execution is the tracked exception today (see
  its own comments); auditing the rest of the tree for the same gap is not
  done.
- **Admission control exists, coarsely.** `engine.Limits.MaxQueued`
  (default 4) bounds how many requests may wait for the slot;
  `engine.ErrTooManyQueued` is the "server busy" answer, mapped to HTTP 503 by
  `serve http`. The bound is process-global (a package-level counter), not
  per engine instance or caller, so two independent callers sharing a process
  share one queue depth.
- **Streaming commands are refused, not made safe.** `--watch` (`get`),
  `--follow` (`logs`), and `--live` (`query`) are blocked outright via the
  `LongRunningStreams` capability (`capability_disabled`) rather than
  supported through a different response shape — they would need a streaming
  transport, which `POST /v1/execute` (buffers the whole output) does not
  offer. This closes off the "unbounded command reachable" gap for these
  three flags specifically; it does not audit the full command tree for other
  potentially-unbounded operations.
- **Per-invocation tracing cost.** `tracing.Init`/shutdown runs per invocation
  with a 5s flush budget; in a long-lived server that belongs at process scope.

## Open questions

- **Instance-per-request via WASM.** It would remove the serialization constraint
  and give hard, host-enforced per-request deadlines (rather than the cooperative
  `cmd.Context()` cancellation above). Measured with wazero, one fresh instance
  per command and a precompiled module: about 55 ms of CPU to start each
  instance (against about 12 ms for a native process, and about 3 ms for a command
  on its own tree in process) and about 35 MiB per in-flight instance with
  instance memory outside the Go heap, on a roughly 400 MiB per-process
  baseline; `wasip1` also has no sockets, so every request needs a host-side
  bridge. Not pursued; see [CONCURRENT_EXECUTION.md](CONCURRENT_EXECUTION.md#costs).
