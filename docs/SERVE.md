# Server Mode (Experimental)

`dtctl serve` runs dtctl as a **server** instead of a one-shot CLI. One request
carries one dtctl command line and one tenant (environment URL + token), and the
response carries what the CLI would have printed for that command: stdout,
stderr, and the exit code.

The point is that there is no second API to learn. Anything you can type in a
terminal, a caller can send over the wire — and the bytes coming back are
byte-identical to the local CLI's. That makes server mode a natural fit for AI
agents and agent gateways that already know how to drive dtctl, for automation
that cannot ship a binary, and for Dynatrace Workflow actions that need to talk
to dtctl from inside the platform.

> **This is a reference implementation with no authentication of its own.** It
> binds to localhost by default. Put your own authentication and TLS in front
> before exposing it — see [Security](#security).

## Experimental: opt in first

Server mode is **experimental and off by default**. The request/response
contract, the one-command-at-a-time concurrency model, and the absence of
per-request deadlines are all still subject to change, so a released build does
not expose the command until you ask for it:

```bash
dtctl config set development.serve on     # persistent
DTCTL_DEVELOPMENT=serve dtctl serve http  # one process
```

Without the opt-in, `dtctl serve` is an ordinary unknown command — it does not
appear in `--help` or in the `dtctl commands` catalog. That is the
`development` tier's defining property; see [the stability
manifest](STABILITY.md) for what each tier promises, and
`dtctl config list-development` for every feature this build carries.
(`DTCTL_EXPERIMENTAL_SERVE=1` still works as a deprecated alias, as does
`DTCTL_EXPERIMENTAL_ACCOUNT` for `account`.) The gate covers the *command* only:
[`pkg/engine`](#embedding-pkgengine-instead) is importable Go API, and embedding
it is a compile-time choice rather than something an operator can trip over.

## The command surface

`serve` is a **parent** command; each protocol is its own subcommand. Naming the
protocol is mandatory, so no single one is the silent default and future
protocols land beside `http` rather than competing with an incumbent.

<!-- prose-check:ignore 4 -->
```bash
dtctl serve          # prints help: which protocols this build can speak (exit 0)
dtctl serve http     # JSON over HTTP
dtctl serve grpc     # unknown protocol: error, exit 1 (never a silent no-op)
```

A typo'd protocol is an error with a non-zero exit rather than a help dump, so a
supervisor cannot mistake it for a started server.

### `dtctl serve http`

```bash
dtctl serve http
dtctl serve http --addr 0.0.0.0:8080 --max-request-bytes 33554432
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--addr` | `127.0.0.1:7211` | listen address |
| `--max-request-bytes` | `10485760` (10 MiB) | maximum request body size (virtual files travel inline) |
| `--read-timeout` | `30s` | time allowed to read the full request including body |
| `--write-timeout` | `6m` | time allowed to write the response; must exceed `--max-duration`, since the write deadline is set when the request header is read and so covers execution *and* the response write |
| `--idle-timeout` | `2m` | maximum time to wait for the next request on a keep-alive connection |
| `--max-queued` | `4` | maximum number of requests allowed to queue (waiting + running); excess returns `503` |
| `--max-duration` | `5m` | maximum wall-clock time allowed for a single request; threaded into `cmd.Context()` so cooperating commands stop when it elapses (see [One request at a time](#one-request-at-a-time)) |

| Endpoint | Purpose |
|----------|---------|
| `POST /v1/execute` | run one dtctl command line |
| `GET /healthz` | liveness probe, answers `{"status":"ok"}` |

The server shuts down gracefully on `SIGINT`/`SIGTERM`, letting an in-flight
execution finish. Invoke `serve` directly, with nothing between it and the binary
name (`dtctl serve http`, not `dtctl -v serve http`) — the latter is rejected,
because serving from inside a command invocation would deadlock the first
request.

## Executing a command

The request body is a dtctl command line plus the tenant to run it against, and
optionally the files that command line refers to:

```bash
curl -s http://127.0.0.1:7211/v1/execute \
  -H 'Content-Type: application/json' \
  -d '{
    "command": "apply -f workflow.yaml --write-id --agent",
    "environmentUrl": "https://abc12345.apps.dynatrace.com",
    "token": "dt0s16.XXXXXXXX.YYYYYYYY",
    "safetyLevel": "readwrite-mine",
    "files": {
      "workflow.yaml": "title: Daily Health Check\ntasks: {}\n"
    }
  }'
```

| Field | Meaning |
|-------|---------|
| `command` | the dtctl command line exactly as typed locally, e.g. `get workflows -o json`. Split with POSIX shell quoting rules — no pipes, redirection, or variable expansion; dtctl is not a shell |
| `argv` | the pre-split argument vector; takes precedence over `command` (no quoting round-trip) |
| `environmentUrl` | the Dynatrace environment to run against. Required |
| `token` | authenticates every API call of this request. Required |
| `safetyLevel` | bounds mutating operations: `readonly`, `readwrite-mine`, `readwrite-all`, `dangerously-unrestricted`. Omitted means dtctl's default (`readwrite-all`) |
| `profile` | a built-in [command profile](CONFIGURATION.md#command-profiles) (e.g. `query`) that reduces the visible command surface for this request |
| `minStability` | the weakest [stability tier](STABILITY.md) this request accepts: `experimental` or `stable`. Omitted means `stable` — stricter than the CLI's default, because nobody reads an `[Experimental]` badge on a request's behalf |
| `stabilityExceptions` | commands or flags admitted below the floor, each one an audited risk acceptance (e.g. `inventory`, `query --decode-snapshots`). Naming a command also admits the flags that are below the floor only because that command is; it does not admit its subcommands |
| `files` | the request's virtual filesystem: name → file content |
| `stdin` | standard input for commands that read it (`-f -`) |

## The response

```json
{
  "exitCode": 0,
  "stdout": "{\"ok\":true,\"result\":{...}}\n",
  "stderr": "",
  "files": {
    "workflow.yaml": "id: wf-abc123\ntitle: Daily Health Check\ntasks: {}\n"
  },
  "durationMs": 412,
  "truncated": false
}
```

`files` is the **complete final state of the request's virtual filesystem** — the
files you sent plus anything the command wrote. That is how writebacks survive a
stateless request: `apply --write-id` stamps the generated id into
`workflow.yaml`, and you read the stamped file straight out of the response and
persist it wherever your source of truth lives.

`truncated` is `true` when stdout or stderr was cut at the engine's
`MaxOutputBytes` budget; the field is omitted (falsy) otherwise. A truncated
result is still valid up to the cap — a large `get` listing, say — but
incomplete, so check it before assuming `stdout`/`stderr` are the whole story.

### A failed command is still HTTP 200

A command that fails answers `200` with a non-zero `exitCode` and a message on
`stderr`, exactly like a local shell. Non-200 statuses are reserved for requests
that **never ran**:

| Status | Cause |
|--------|-------|
| `400` | malformed JSON body, or a request shape dtctl cannot run (no command, missing `environmentUrl` or `token`, unparsable command string) |
| `405` | anything other than `POST` on `/v1/execute` |
| `413` | request body exceeds `--max-request-bytes` |
| `503` | the server is shedding load: `MaxQueued` requests are already waiting for the execution slot. Retry with backoff |
| `504` | the request's `MaxDuration` budget (or the caller's own context) elapsed before or while queued |

So `200` means "dtctl ran your command line"; check `exitCode` for whether the
command succeeded, and `truncated` for whether its output is complete.

## The output is the CLI's output

Same command line, same bytes — a caller must not be able to tell whether it
reached a terminal or a server. Two consequences worth internalizing:

- **Envelopes are opt-in per request.** Put `--agent` in the `command` string to
  get the [agent mode](AGENT_MODE.md) JSON
  envelope; otherwise you get the human table, and `-o json`/`-o yaml`/`-o csv`
  behave exactly as they do locally.
- **AI-agent auto-detection is skipped**, and `DTCTL_PROFILE`/`DTCTL_OUTPUT` are
  scrubbed along with the credential variables. The host process's environment
  must not shape a tenant's output format.

## Every request brings its own tenant

Servers are multi-tenant per invocation. The local dtctl config file, the
keyring, and the credential and config environment variables (`DTCTL_TOKEN`,
`DT_API_TOKEN`, `DTCTL_ACCOUNT_TOKEN`, `DTCTL_CONFIG`, `DTCTL_CONTEXT`) are
**never read** — they are scrubbed for the duration of each run, along with the
host preferences that would otherwise shape a response's bytes (`DTCTL_PROFILE`,
`DTCTL_OUTPUT`, `DTCTL_SPILL`, `DTCTL_SPILL_DIR`, `FORCE_COLOR`, `NO_COLOR`).
Nothing about the host process's identity leaks into a request, and nothing from
one request survives into the next.

File arguments follow the same rule: `-f`, `--data-file`, query files, files to
`diff`, and writebacks resolve against the request's `files` map, never the
server's disk. Paths are normalized, so `x.yaml`, `./x.yaml`, and `/x.yaml` are
the same file. A `files` map you did not send is an empty filesystem, not the
host filesystem. Standard input is the request's `stdin` and nothing else: a
request that sends none reads an empty stream, never the server's.

These are enforced, not merely intended: `TestUserFilePathsGoThroughVFS` fails
the build if any file under `cmd/` or `pkg/` reaches the host filesystem outside
the seam without a documented reason.

## What is unavailable, and why

Server mode is deliberately not a perfect mirror of the local CLI:

- **Host-only commands are removed from the surface**: `config`, `ctx`, `auth`,
  `account`, `alias`, `edit`, `plugin`, `skills`, `doctor`, `completion`,
  `inspect`, and `serve` itself. They manage host-local state (a config file, a
  keyring, a shell, locally installed tools, a spilled result file, an
  interactive session) that does not exist for a service request — or would nest
  the service inside itself. They are hidden from `--help` and from the
  `dtctl commands` catalog, and invoking one returns the stable agent error code
  `unsupported_in_service` with the reason attached.
- **Results are never spilled to disk.** Locally, a large result can spill to a
  file and return a path (`--spill`, `--spill-to`, and automatically in agent
  mode). A service request has no host disk of its own: the file would outlive
  the request on the *server's* disk and the path would be unreadable by the
  caller, so rows always come back inline. Asking for a spill explicitly returns
  `capability_disabled` rather than quietly inlining the result.
- **No subprocesses.** Plugins, shell aliases, pre-apply hooks, interactive
  editors, and browser opens are all disabled; requesting one yields
  `capability_disabled`. Everything a request needs happens in-process.
- **Arbitrary environment variables are not exposed over HTTP.** They reach
  proxies, exporters, and other process-level behavior. Embedding hosts that
  genuinely need them use `engine.Request.Env`
  [in-process](#embedding-pkgengine-instead).
- **Output is buffered**, so long-running commands don't fit the
  request/response shape: `--watch` (`get`), `--follow` (`logs`), and
  `--live` (`query`) are refused outright with `capability_disabled` rather
  than silently hanging the request.

## One request at a time

dtctl's command tree is process-level state, so **invocations serialize**: a
server handles one dtctl command at a time per process. This is what makes the
rest of the model safe — swapping the process's streams, environment, and
filesystem per request would be indefensible under concurrency.

Scale with **more processes or more instances**, never more goroutines. Two
budgets bound one request: `MaxQueued` caps how many requests may wait for the
slot (past that, the server answers 503 rather than growing the queue
unbounded), and `MaxDuration` (5 minutes by default) cancels the request's
context if it's still queued or running when the budget elapses. Cancellation
is cooperative — most commands observe it via `cmd.Context()`, but a command
that doesn't check its context, or that issues its own request with a
different one, still runs to completion regardless of the budget. Deployments
that need a hard, host-enforced deadline put the server behind a process
boundary.

## Security

The servers under `dtctl serve` are **reference implementations**, and their
threat model is worth stating plainly:

- **No authentication of their own.** The per-request `token` authenticates
  against *Dynatrace*, not against the server. Anyone who can reach the port can
  run dtctl command lines with any token they supply.
- **Localhost by default.** `--addr` defaults to `127.0.0.1:7211`. Changing it to
  a routable address is exactly the moment to add your own authentication, TLS,
  and rate limiting in front — a reverse proxy, a sidecar, or an API gateway.
- **Not a sandbox.** The isolation described above (scrubbed credentials, virtual
  files, no subprocesses, reduced surface) is a *correctness* boundary inside one
  process: it stops an honest command line from reaching host state. It is not
  designed to contain a hostile one. Untrusted callers belong behind a process
  boundary.
- **Tokens travel in the request body.** Terminate TLS in front of the server and
  keep bodies out of access logs.

Combine `safetyLevel` and `profile` per request to bound what a caller can do:
`safetyLevel: readonly` refuses mutations, `profile: query` removes everything
but the query surface. Both are client-side conveniences, though — for real
restriction, scope the Dynatrace token.

## Embedding `pkg/engine` instead

Running a server is one way to consume this; the other is to call the engine
directly from Go. `pkg/engine` is the surface `dtctl serve http` is a thin
wrapper over — same request shape, same isolation, no HTTP hop and no port to
protect:

```bash
go get github.com/Dynatrace/dtctl@latest
```

Note that this is the **CLI** module, not the separate
[`sdk/` module](../sdk/README.md): embedding
the engine means embedding the whole command surface, because that is what makes
the output identical. If you want typed API wrappers without the CLI, use the SDK
instead. Importing the CLI module requires **v0.38.0 or newer** — earlier tags
could not be resolved as a library dependency at all.

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

Prefer embedding when you already have an HTTP service (add your own route, with
your own authentication), when you need `Env`, or when you want to speak a
protocol dtctl does not ship. The design notes live in
[`docs/dev/SERVICE_ENGINE_DESIGN.md`](dev/SERVICE_ENGINE_DESIGN.md).
