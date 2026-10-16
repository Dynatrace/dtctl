# AI Agent Development Guide

kubectl-inspired CLI for Dynatrace (dashboards, workflows, SLOs, etc). Go + Cobra framework.

**Pattern**: `dtctl <verb> <resource> [flags]`

## Quick Start

1. Read [docs/dev/API_DESIGN.md](docs/dev/API_DESIGN.md) Design Principles (lines 17-110)
2. Check [IMPLEMENTATION_STATUS.md](docs/dev/IMPLEMENTATION_STATUS.md) for feature matrix
3. Copy patterns from `pkg/resources/slo/` or `pkg/resources/workflow/`

## Architecture

```text
cmd/          # Cobra commands (get, describe, create, delete, apply, exec, ctx, doctor, commands, plugin)
              # plus the embedding entrypoint: run.go (cmd.Run + per-invocation isolation),
              # session.go, capabilities.go, stdio.go, blocked.go (docs/dev/SERVICE_ENGINE_DESIGN.md)
pkg/
  ├── client/    # Shim over sdk/session's client; keeps CLI-only extras (typed errors, pagination, OTel injection) and pins the dtctl/<version> User-Agent
  ├── config/    # Shim over sdk/session — the config model/keyring implementation lives in the sdk
  ├── auth/      # Shim over sdk/session's OAuth machinery + CLI-owned scope-composition tables
  ├── safety/    # Shim over sdk/session's safety checker
  ├── plugin/    # kubectl-style exec plugin resolution/discovery (docs/dev/PLUGIN_CONVENTIONS.md)
  ├── resources/ # Resource handlers — thin CLI wrappers that delegate to sdk/api/
  ├── output/    # Formatters (table, JSON, YAML, charts, agent envelope, color control)
  ├── exec/      # DQL query execution
  ├── vfs/       # Virtual-filesystem seam: user-supplied file paths resolve against the host disk (CLI) or per-request virtual files (engine)
  ├── engine/    # Embeddable service engine: one dtctl command line per request — multi-tenant session, virtual files, CLI-identical output
  └── serve/     # `dtctl serve <protocol>` — reference servers over pkg/engine, one subcommand per protocol (`serve http` today; wired in main, outside the invocation lock). Development-tier: registered only when opted in (`dtctl config set development.serve on`)
sdk/            # Separate Go module (github.com/dynatrace-oss/dtctl/sdk)
  ├── session/     # The session layer (docs/dev/CONFIG_CONTRACT.md): config model + load/save, credential stores, OAuth flow/refresh + cross-process lock, client-from-context with parameterized User-Agent, safety semantics
  ├── api/         # Typed API wrappers (one package per Dynatrace API surface)
  │                #   apispec/ is the odd one out: it reads the environment's *own* API index and
  │                #   OpenAPI documents (docs/dev/GENERIC_API_ACCESS.md)
  ├── httpclient/  # HTTP client, response helpers, pagination, typed errors
  ├── auth/        # Token type detection
  ├── urls/        # Environment URL validation/normalization
  ├── agentmode/   # AI agent environment detection
  └── inventory/   # Environment data-inventory discovery over a caller-supplied DQL Runner
```

**SDK delegation pattern**: CLI resource handlers in `pkg/resources/` import types from `sdk/api/` (often via type aliases) and delegate HTTP calls to SDK functions. The `sdk/api/*` packages contain **no file I/O, no CLI concerns, no display logic**. File reading (e.g., `ReadFileOrStdin`, `ParseInputFromFile`) stays in `pkg/resources/`. (`sdk/session` is the deliberate exception on file I/O: it owns the config file and credential stores — that's its job.)

**Two modules, one release**: the root `go.mod` requires `dtctl/sdk` *and* `replace`s it with `./sdk`. Go ignores that `replace` downstream, so the `require` must always name a real `sdk/vX.Y.Z` tag — otherwise the root module cannot be imported at all (e.g. a service embedding `pkg/engine`), while every in-repo build stays green because the replace covers it. The require carries `// x-release-please-version` so release-please keeps it equal to the CLI version, and `.github/workflows/release.yml` mirrors each `vX.Y.Z` into `sdk/vX.Y.Z`. Never hand-edit that version or drop the annotation. *Guard*: `go test ./pkg/version/ -run TestSDKRequireIsResolvable`

**Plugins**: dtctl dispatches unknown commands to `dtctl-<name>` executables on PATH (kubectl semantics; built-ins always win). See [docs/dev/PLUGIN_CONVENTIONS.md](docs/dev/PLUGIN_CONVENTIONS.md) for the env contract (`DTCTL_CONTEXT`, `DTCTL_CONFIG`, `DTCTL_AGENT`, `DTCTL_PLAIN`, `DTCTL_CALLER_VERSION` — never tokens).

## Agent Output Mode

dtctl supports `--agent` / `-A` to wrap all output in a structured JSON envelope for AI agents:

```json
{"ok": true, "result": [...], "context": {"verb": "get", "resource": "workflow", "suggestions": [...]}}
```

- **Auto-detected** in AI agent environments (opt out with `--no-agent`)
- Implies `--plain` (no colors, no interactive prompts)
- Errors are also structured: `{"ok": false, "error": {"code": "not_found", "message": "..."}}`
- Implementation: `pkg/output/agent.go` (`AgentPrinter`, `Response`, `PrintError`)
- Per-command context enrichment via `enrichAgent()` helper in `cmd/root.go`
- **Command catalog**: `dtctl commands` gives a compact minimal overview (verbs, resources, subcommands only; defaults to TOON) — ideal for agent bootstrap. `--brief` adds mutating status, access levels, flag types, and scopes; `--full` emits the exhaustive catalog (descriptions, flag defaults, global flags, materialized scopes). Override the format with `-o json`/`-o yaml`

## Color Control

ANSI color output follows the [no-color.org](https://no-color.org/) standard:

```
Color enabled = NOT (NO_COLOR is set) AND NOT (--plain flag) AND (stdout is a TTY OR FORCE_COLOR=1)
```

- **`NO_COLOR`** env var: any non-empty value disables color
- **`FORCE_COLOR=1`** env var: overrides TTY detection to force color on (e.g., in CI with color-capable terminals)
- **`--plain`** flag: disables color (and interactive prompts)
- **Non-TTY** (piped output): color is disabled automatically
- Implementation: `pkg/output/styles.go` (`ColorEnabled()`, `Colorize()`, `ColorCode()`)
- Result is cached with `sync.Once`; use `ResetColorCache()` in tests

## Adding a Supported Agent

When adding a new AI agent to the skills system, update **all** of the following:

1. **Code**: `sdk/agentmode/detect.go` (env var, in check order — only a variable the agent sets on commands *it* runs, never one every IDE terminal carries), `pkg/skills/installer.go` (agent entry + format), `cmd/skills.go` (help text, `--for` flag)
2. **Tests**: `sdk/agentmode/detect_test.go`, `pkg/skills/installer_test.go`, `cmd/skills_test.go`
3. **Docs**: `README.md`, `docs/AGENT_MODE.md` (auto-detection table), `docs/dev/IMPLEMENTATION_STATUS.md` (skills feature line)

> Releases are automated by release-please from conventional commits — do not hand-edit a changelog. Just make sure the commit/PR title is a proper conventional commit (e.g. `feat: detect <agent> sessions`).

## Stability Tiers

Every command and flag carries a **stability tier** — what dtctl promises about
its shape over time. It is a third axis, orthogonal to the safety level ("what
may this command do?") and the command profile ("which commands exist here?").
Full design: dtctl-contrib `dev/STABILITY_TIERS_DESIGN.md`. Reference and the
current inventory: [docs/STABILITY.md](docs/STABILITY.md).

| Tier | Promise | Visibility |
|---|---|---|
| `stable` | Additive-only; removal needs a deprecation cycle | normal |
| `experimental` | May change or be removed in any release | registered, badged |
| `development` | Unfinished, no guarantees *of any kind* | **not registered** until opted in |

`development` covers the backend too: such a command usually targets an
unreleased or feature-flagged API, so a 404/403/500 on an environment where the
feature is not deployed is the tier working as intended, not a dtctl bug. dtctl
does not translate those into "not available here".

**Stable is explicit, never implied.** Every command declares its own tier where
it is built — including the stable ones:

```go
stability.MarkStable(getWorkflowsCmd)                        // yes, this is required
stability.Mark(ingestCmd, stability.Experimental, "0.39.0")  // since-version required
stability.MarkFlag(queryCmd, "spill", stability.Experimental, "0.39.0")
stability.MarkFlagStable(queryCmd, "limit")                  // a new flag on a stable command
addDevelopmentCommand(rootCmd, accountCmd, "account")        // top-level, in cmd/
cmd.AddDevelopmentCommand(serve.NewCommand(), "serve")       // wired from main
```

Stable used to be what a command got for saying nothing, which made the
strongest promise dtctl makes the one nobody chose: a command shipped an
additive-only contract because its author had not considered the question, and
by the time anyone noticed, callers had built on it. So the build now refuses an
undeclared command (`TestEveryCommandDeclaresItsTier`), and one that somehow
reaches runtime undeclared resolves to `experimental` — never `stable`
(`session.FallbackStabilityLevel`). The fallback is a backstop, not the answer;
the answer is a declaration.

Hidden commands are **not** exempt. Hiding removes a command from help, not from
the tree: `dtctl exec dql` still runs, so it still carries a contract.

**Pre-1.0, the tier carries the contract and the version number does not.**
release-please runs with `bump-minor-pre-major`, so a break ships as a minor
bump and semver's 0.x clause would let anything change at any time — which would
make every `stable` badge worthless. dtctl carves the tier out of that policy:
`stable` is additive-only from the release that declared it, whatever the version
does next, and the only exit is a deprecation whose removal lands **at least two
minor releases** later, so there is always a release that both warns and works.
`stability.Lint` fails the build on a shorter window or an incomplete record.
The policy is user-facing in the manifest header ("What `stable` promises before
1.0"); `pkg/stability/manifest.go` is where you edit it.

**Agent-mode output is the one exception to additive-only.** In agent mode the
reader is a model that reads every response afresh and adapts to it, so what the
envelope carries (default fields, item counts, row encoding, rounding) may change
in any minor release, `stable` commands included. That change needs no
deprecation cycle and no dtctl-contrib breaking-change document, and it does not
demote the command. A PR that changes what agent mode outputs still has to:

- keep the envelope skeleton additive-only (`ok`, `error.code`, `result.kind`,
  the `context` keys), because host code parses it, not the model;
- make the change describe itself in the envelope: `context.format`,
  `context.has_more`, or a `context.suggestions` entry naming the flag that
  restores the full data;
- leave every explicit flag's meaning unchanged, since that is how a program
  pins a shape.

Such a change is **not** a breaking change: no `!` in the Conventional Commit
title and no `BREAKING CHANGE:` footer, just the `feat`/`fix` type it would
otherwise get. A model adapts to what it reads, so there is no caller to warn,
and marking every agent-output tweak breaking would drown out the breaks a
program actually has to act on. The marker stays for what the rules above
protect: a PR that renames or removes an envelope skeleton key, or changes an
explicit flag's meaning, has broken a promise and is breaking like any other.

**Outside agent mode, the output contract is per command, not per field.**
The golden tests enforce it at whole-command level: they snapshot each printer's
output and fail on any change. A payload that comes from the environment (the
body `exec api` passes through, the records a query returns) is not dtctl's
to promise; only the invocation and how dtctl frames the output are. There is no field-level tier, so no experimental
or deprecated output field, no field entries in the manifest, and
`DTCTL_NO_DEPRECATED` covers commands and flags only (#542). To ship a field
without the `stable` promise, put it behind an experimental flag or on an
experimental command.

Output outside agent mode, and everything a caller types, keep the full promise.

*Flags* are the deliberate exception — an unannotated flag inherits its
command's tier. Requiring every flag on an experimental command to repeat the
annotation would be pure noise. A flag may be weaker than its command (an
experimental flag on a stable command is how a new idea ships without a new
command) but never stronger.

Inheritance only weakens, but under a *stable* command it still hands a new flag
a stable promise nobody chose. So a flag that **newly** becomes stable under a
command that was already stable must declare its tier — `MarkFlag(cmd, name,
stability.Experimental, "<next>")` or `stability.MarkFlagStable(cmd, name)`.
The manifest tags every self-declared flag `(declared)`, and `make
stability-compat` refuses a new or promoted stable flag without the tag. The
existing flags predate the rule and keep inheriting; the flags of a new (or
newly promoted) stable command are covered by its `MarkStable`. `(global)` is a
stable entry like any command, so a new global flag must declare too (on
`rootCmd`): it is the widest surface dtctl has.

After changing any of this, regenerate the checked-in manifest:

```bash
make stability-manifest   # writes docs/STABILITY.md; review the diff
```

`go test ./test/stability/` gates it. **A line that disappears from the stable
surface is a broken promise, not a cleanup** — and because a drift gate only
compares the manifest to the tree it was generated from, a PR that demotes a
command *and* regenerates looks perfectly clean to it. `make stability-compat`
closes that: it diffs the manifest against `origin/main` and refuses a stable
entry that vanished (unless it was already deprecated there) or came back
weaker. CI runs it on every PR.

The manifest also lists the root command's persistent flags under a synthetic
`(global)` group. A flag that appears nowhere in the file would be stable by
omission — the one tier nobody chose — so `--agent`, `--jq` and the rest are on
the record too, and the floor enforces them: it checks the persistent flags a
command *inherits*, on the command the caller typed, since `rootCmd`'s `RunE`
never runs for `dtctl get workflows --jq .`. A *hidden* persistent flag is left
out of the group: the root's hidden `--dry-run` only lets cobra parse the flag
ahead of the subcommand, and each command that implements a dry run declares
its own.

One consequence is worth knowing before you reach for `MarkFlag` on a global
flag: cobra hands every subcommand the same `pflag.Flag` pointer the root
declared, so a global flag has exactly one tier for the whole tree. There is no
such thing as demoting `--jq` on `get` but not on `apply`.

Enforcement is a five-stage pipeline that only ever *narrows* the surface, so
"which axis wins" has a structural answer:

1. registration — development opt-in (`applyDevelopmentRegistration`)
2. profile mask — topical allowlist (`applyProfile`)
3. stability floor — contract filter (`applyStabilityFloor`)
4. deprecated surface — removal preview (`applyNoDeprecated`, opt-in)
5. safety level — permission check (`pkg/safety`, per operation)

Stage 4 is `DTCTL_NO_DEPRECATED=1`: dtctl behaves as if every deprecated command
and flag had already been removed, so a CI job finds out it still depends on one
while that is a fixable build rather than an outage. It is not a tier and not a
floor — a deprecated command is stable in shape and satisfies any floor, it
merely has a removal date, so no floor and no exception brings it back, only a
migration. Durable per context (`no-deprecated: true`), per request
(`engine.Request.NoDeprecated`), and the env var overrides the context in *both*
directions so the run still mid-migration can opt back out. Errors carry their
own agent code, `deprecated_surface`, rather than `stability_blocked`.

No later stage can re-add what an earlier one removed. In particular a
`stability-exceptions` entry cannot resurrect an unregistered development
command. *Guard*: `go test ./cmd/ -run TestStability`

### Flags an accepted breaking change already targets

A flag can be correct, shipped, and still not `stable`: if dtctl-contrib's
`breaking-changes/` folder has an accepted document that renames or removes it,
the additive-only promise is one dtctl cannot keep. Those demotions live in
`cmd/stability_pre_1_0.go` (the `pre10Since` constant and its rationale) and are
enumerated with their driving document in
`test/stability/pre_1_0_marks_test.go`. An accepted document counts even before
its PR merges — demoting early costs a badge, demoting late means having shipped
`stable` on a flag we already knew would break.

The line to apply: **demote when 1.0 removes or renames the flag, or changes
what a currently-valid invocation does. Do not demote when 1.0 only turns input
dtctl already ignores into an error.** `--state RUNNING` means the same thing
before and after stricter enum validation; only `--state RUNNIG` changes, from a
silently wrong answer into a useful message. Withdrawing the stable contract to
warn about a typo costs every correct caller and protects none of them. The
spared rows carry their reason in `pre10RejectUnusableInputSpared`.

A since-version is the one part of the manifest the release itself can falsify:
`experimental since 0.39.0` is a claim about a version that does not exist yet
when it is written. The guard is split by what each half can see.
`TestSinceVersionsNameNoReleaseBeyondTheNextOne` sees only the tree, so it
holds every declaration to a real version no later than the next release — an
already-released since-version stays valid forever, as it must, since it is
true. `make stability-compat` sees the base branch too, so it checks the
change: a since-version that is new or changed must name the next release, not
one that already shipped without it; a released one may not be rewritten; and
an unreleased one must still be this release or the next by the head's
`pkg/version`, so a differently-numbered release fails on release-please's own
version-bump PR rather than after the tag exists. (The first version of the
test only allowed this release or the next one, and so broke main on the first
commit after every release.)

That list is a test and not a comment for a reason: `stability.MarkFlag` is a
silent no-op when the flag it names does not exist, so renaming a flag in `cmd/`
would drop its mark and quietly re-promise something 1.0 removes. When a
breaking change lands, delete the flag's entry along with its mark — the test
fails if the flag is gone but the entry stays.

**Mark the flag, or the command?** Ask whether a usable stable invocation
survives with the flag hidden. `create azure connection` keeps one (the
`federatedIdentityCredential` path needs none of the renamed credential flags),
so the flag mark is right. `update azure connection` does not: it requires one
of `--directoryId`/`--applicationId`/`--clientSecret`, so at a stable floor
`--help` offers only `--name` and every invocation fails demanding three flags
the floor just hid. Demote the *command* there — a `stability_blocked` error
that names the exception which would admit it beats a dead end. The split is
pinned by `pre10DemotedCommands` / `pre10SurvivingCommands` in the same test.

The other reason to mark the command is that the break lives in no flag at all:
`exec workflow` starts waiting by default, so there is nothing narrower to
annotate. `logs workflow-execution` was demoted for reshaping its agent-mode
output before the agent-mode exception above existed. A reshape of agent-mode
output alone no longer demotes a command. Note that
the manifest reports a flag's *effective* tier, so demoting a command shows up
on every flag underneath it.

## Adding a Resource

1. **SDK layer** (`sdk/api/<name>/`): Create typed API wrapper with CRUD functions using `httpclient.Client`. No file I/O, no display logic.
2. **CLI layer** (`pkg/resources/<name>/`): Create resource handler that delegates to SDK. Handle file reading, display fields, name resolution here. Read user-supplied paths through `pkg/vfs`, never `os` directly (see [Embedding Invariants](#-critical-embedding-invariants-)).
3. **Commands**: Add to `cmd/get.go`, `cmd/describe.go`, etc. Mutating verbs need a safety check; `-f`/`--file` flags go through `vfs`; no `os.Exit`, no ungated subprocess. Build each command in a `newXCmd()` constructor (flags bound to variables local to it, stability tier declared inside), attach it in `init()` as usual **and** list it in `newCommandTree` in `cmd/root_factory.go`; read the run's state through `cmdContext(cmd)` (see [Embedding Invariants](#-critical-embedding-invariants-) rule 7).
4. Register in resolver
5. Add tests: `sdk/api/<name>/*_test.go` (SDK unit tests) + `test/e2e/<name>_test.go` (E2E)
5b. **Declare the stability tier** — every command, stable ones included; the
   build fails on an undeclared one. Then run `make stability-manifest` (see
   [Stability Tiers](#stability-tiers)).
6. **Claim the coverage**: add the API's base path to `nativeCoverage` in `pkg/resources/api/coverage.go`, so `dtctl get apis` stops listing it as uncovered and `dtctl exec api` points callers at the new command. `Command` must be what a user would actually type (`dtctl query`, not `dtctl get query`). *Guard*: `go test ./cmd/ -run TestNativeCoverageNamesRealCommands`

**SDK handler signature** (in `sdk/api/<name>/`):
```go
func NewHandler(client *httpclient.Client) *Handler
func (h *Handler) Get(id string) (*Resource, error)
func (h *Handler) List(opts ListOptions) ([]Resource, error)
```

**Every value interpolated into a request path goes through `httpclient.PathSegment`.**

```go
Delete(fmt.Sprintf("/platform/slo/v1/slos/%s", httpclient.PathSegment(id)))
```

An id arrives from a command line or an applied file. Spliced in raw, one
containing `/`, `?` or `#` addresses a *different* resource and the request
succeeds against it — `delete slo "<id>#x"` reported success while deleting
`<id>`, because net/http drops the fragment before sending.
`httpclient.CheckRequestPath` refuses an unescaped path at run time and
`test/requestpath` catches the omission at build time, but neither is a reason
to leave it out.

**Not `url.PathEscape`.** It leaves `:` intact, and several collections spell an
action as a suffix on the resource segment — `%s:execute`, `%s:poll`,
`%s:truncate` — so an id carrying a colon names an *operation*: `describe
analyzer "foo:poll"` polled `foo`. `PathSegment` is `url.PathEscape` plus the
colon, and the suffix is unaffected because it is a literal in the format
string, never an interpolated value. The build fails on a bare `url.PathEscape`
in a request path for exactly this reason.

**One value is a path by design**: a nested app function name, where
`parseFullFunctionName` splits `app-id/a/b/c` at the first slash only, so the
slashes in `a/b/c` are separators the server expects. `sdk/api/appengine`
escapes it part by part with an unexported `escapeFunctionName`. A second such
case is added to the `escapers` list in `test/requestpath`, next to the reason —
never exempted by file.

**CLI handler** (in `pkg/resources/<name>/`): imports SDK types (often via type alias) and wraps with file I/O, display fields, etc.

## Generic API Access

`dtctl get apis` / `describe api` / `exec api` cover the APIs dtctl does **not** wrap natively. Full rationale: [docs/dev/GENERIC_API_ACCESS.md](docs/dev/GENERIC_API_ACCESS.md). Four conventions to respect when touching them:

1. **dtctl mirrors the environment's API index and filters nothing.** A dtctl-side filter would have to hard-code which APIs to conceal, and in an open-source tool that list *is* the disclosure. Resolution consults only what the index returned — never synthesize a candidate path from a name and retry it, which would turn a name lookup into an existence oracle.
2. **The HTTP method never decides the safety gate.** `resapi.Classify` takes the stricter of the method floor and the specification's declared scope; POST's floor is `OperationRead`, because plenty of read-only endpoints are POSTs. Unresolvable → `OperationDelete`. There is deliberately no flag to assert an operation.
3. **`exec api` should not end up as the integration target.** That is advice about where an integration belongs (a native command, with `dtctl get apis --uncovered` as the backlog), not a statement about its stability contract: `exec api` and `exec dql` are `stable`, so the command's shape is additive-only like any other stable command, and what it reaches is whatever the environment's APIs offer (#544). The constraints that back the advice are real: it stays `Hidden` (present in `dtctl commands --full` via `unadvertisedResources` in `pkg/commands/listing.go`, absent from `--help` and the compact catalogs), it names the native command whenever one covers the path, and no command profile grants it.
4. **No committed artifact names a non-public API.** Help text, examples, golden files, and E2E fixtures are synthetic; live tests assert invariants of the mechanism, never a list of expected APIs.

## Design Principles

1. Verb-noun pattern: `dtctl <verb> <resource>`
2. No custom query flags - use DQL passthrough
3. YAML input, multiple outputs (table/JSON/YAML/charts)
4. Interactive name resolution (disable with `--plain`)
5. Idempotent apply (POST if new, PUT if exists)

## Common Tasks

| Task | Files | Pattern |
|------|-------|---------|
| Add GET | `cmd/get.go`, `pkg/resources/<name>/` | Copy `pkg/resources/slo/` |
| Add EXEC | `cmd/exec.go`, `pkg/exec/<type>.go` | See `pkg/exec/workflow.go` polling |
| Add DQL template | `pkg/exec/dql.go`, `pkg/util/template/` | Use `text/template`, `--set` flag |
| Fix output | `pkg/output/<format>.go` | Test: `dtctl get <resource> -o <format>` |
| Read a user file | `pkg/vfs/` | `vfs.ReadFile` / `vfs.ReadFileOrStdin` — never `os.ReadFile` |
| Add a serve protocol | `pkg/serve/<proto>.go` | Copy `pkg/serve/http.go`; register in `NewCommand()` |
| Wrap a new API natively | `pkg/resources/api/coverage.go` | Add the base path with the *runnable* command (see below) |
| Gate an irreversible endpoint | `pkg/resources/api/classify.go` | Add a `destructivePatterns` entry (docs/dev/GENERIC_API_ACCESS.md) |

**Tests**: `make test` or `go test ./...` • E2E: `test/e2e/` • Integration: `test/integration/`

## Golden (Snapshot) Tests

Output formatting is covered by golden-file tests that capture the exact output of every printer for every resource type. These tests **must be updated** whenever you change output formatting, add/remove struct fields, or add a new resource.

### When to run

- **After modifying** anything in `pkg/output/` or resource structs in `pkg/resources/*/` — run golden tests to check for regressions
- **After adding a new resource** — add test cases to `pkg/output/golden_test.go` using the real production struct

### Commands

```bash
# Run golden tests (will FAIL if output changed — this is intentional)
go test ./pkg/output/ -run TestGolden

# Update golden files after intentional changes (review the diff!)
make test-update-golden
# or: go test ./pkg/output/ -run TestGolden -update

# Run full suite (golden tests are included automatically)
make test
```

### Key files

| File | Purpose |
|------|---------|
| `pkg/output/golden_test.go` | Test cases — uses **real production structs** from `pkg/resources/*` |
| `pkg/output/testdata/golden/` | Golden files across get/, describe/, query/, errors/, empty/ |
| `cmd/testutil/golden.go` | `AssertGolden` / `AssertGoldenStripped` helpers, `-update` flag |
| `pkg/output/testdata/README.md` | Workflow documentation |

### Important rules

1. **Use real structs** — Import from `pkg/resources/*`, never create test-only duplicates. This ensures golden files automatically catch when fields are added/removed.
2. **Review diffs** — After `make test-update-golden`, always `git diff` the golden files to verify changes are intentional.
3. **Privacy** — All test data must be synthetic. No real names, env IDs, tokens, or emails. Use `@example.invalid` for emails (RFC 2606).
4. **CI** — Golden tests run automatically in GitHub Actions via `go test ./...` on every PR. No separate workflow needed.

## 🚨 **CRITICAL: Safety Checks** 🚨

**ALL mutating commands MUST include safety checks.** Non-negotiable for security.

### Required for These Commands

✅ `create`, `edit`, `apply`, `delete`, `update` (all modify resources)  
✅ `exec` — every subcommand, with the operation matching what the execution actually does: `OperationRead` for an analysis or a preview, `OperationCreate` for a run that creates state, `OperationDelete` for ad-hoc code (`exec function --code` can do anything), and for `exec api` the operation derived from the API's own specification. Use `SetupWithSafety(op)`, not the ungated `SetupClient()`. *Guard*: `go test ./cmd/ -run TestExec.*Readonly`  
❌ `get`, `describe`, `query`, `logs`, `history`, `ctx`, `doctor`, `commands` (read-only)

### Pattern (after `LoadConfig()`, before client ops)

```go
cfg, err := LoadConfig()
if err != nil { return err }

// Safety check - REQUIRED
if err := CheckSafety(cfg, safety.OperationXXX, safety.OwnershipUnknown); err != nil {
    return err
}

c, err := NewClientFromConfig(cfg)
// ... proceed
```

`SetupWithSafety(op)` is this pattern plus the client, for the common case where
ownership is unknown. Either way the check goes through `CheckSafety` — **never
call `checker.CheckError` from a command**. *Guard*: `go test ./cmd/ -run TestSafetyChecksGoThroughCheckSafety`

**Operation types**: `OperationCreate`, `OperationUpdate`, `OperationDelete`, `OperationDeleteBucket`

### Dry runs are exempt, in one place

`CheckSafety` returns nil under `--dry-run`, so a command needs **no** dry-run
branch of its own around the check. A preview reads and writes nothing, so it is
gated like a read, not like the mutation it describes — and `readonly` is
precisely the context that wants a preview most: `dtctl get dashboard X` already
succeeds there, so refusing the same GET under `delete --dry-run` would be
inconsistent rather than safer.

This used to be each command's own decision, and it split about evenly: `create
bucket` previewed before checking, `create aws connection` checked first, for no
reason either could state. Both spellings are gone. The exemption lives in
`CheckSafety` and nowhere else.

It is sound only because `--dry-run` is opt-in per command (`dryRunCommands` in
`cmd/dryrun.go`): a command that reaches `CheckSafety` with `dryRun` set has a
dry-run branch that returns before any write. Adding a command to
`dryRunCommands` is what asserts that — so a command whose preview is not
write-free does not belong on that list.

*Guards*: `go test ./cmd/ -run TestDryRunNeedsNoSafetyLevel` (every cloud delete
previews in a `readonly` context and sends no write) and
`TestRealRunStillNeedsSafetyLevel` (the same command without the flag is still
refused).

`exec api` is the one command that consults the checker while `dryRun` is set:
its preview *reports* the verdict ("Safety: BLOCKED in this context"), which is
the opposite of acting on it.

**Verification**:
- [ ] Import `github.com/dynatrace-oss/dtctl/pkg/safety`
- [ ] Check after `LoadConfig()`, before operations
- [ ] Correct operation type
- [ ] Test with `readonly` context (should block)
- [ ] If the command takes `--dry-run`, its preview must send no write

**Examples**: [cmd/edit.go](cmd/edit.go), [cmd/create.go](cmd/create.go), [cmd/apply.go](cmd/apply.go)

## 🚨 **CRITICAL: Embedding Invariants** 🚨

dtctl runs as a one-shot CLI **and** as an in-process library, one invocation per
service request (`cmd.Run` → `pkg/engine` → `dtctl serve http`). Same command
tree, same code paths, same bytes on stdout — the only difference is where the
invocation's credentials, files, and streams come from.

Every rule below has a guard test, because every one of them was violated at
least once — including twice inside the PR that introduced the model. Full
rationale: [docs/dev/SERVICE_ENGINE_DESIGN.md](docs/dev/SERVICE_ENGINE_DESIGN.md).

### 1. User-supplied file paths go through `pkg/vfs` — never `os` directly

```go
content, err := os.ReadFile(pathFromFlag)          // ❌ reads the SERVER's disk
content, err := vfs.ReadFile(pathFromFlag)         // ❌ in cmd/: "-" is read as a file named "-"
content, err := readFileFlag("file", pathFromFlag) // ✅ in cmd/: vfs seam, and "-" means stdin
```

In `cmd/`, every `--file`-style flag reads through `readFileFlag`
(`cmd/file_input.go`), so `-f -` means stdin in every command, a terminal stdin
fails fast instead of hanging, and an empty pipe is an error. Piped input is
named `<stdin>` wherever the path would be shown. Stdin can be read once: a
command that could read two inputs from it (`diff -f - -f -`) refuses the
combination.
*Guard*: `go test ./cmd/ -run TestFileFlagsReadThroughReadFileFlag`

The test is **whose file is it**: a path the *user named* (`-f`, `--file`,
`--data-file`, a query file, a file to `diff`, an `apply --write-id` writeback)
is request state and must go through the seam. A path *dtctl chose* (editor temp
file, config file, spill buffer) is host state and stays on `os` — but only when
a blocked command or an ungranted capability keeps a request from reaching it.

**The rule follows the path, not the package.** `cmd/` reading through the seam
and then handing the path to a `pkg/` helper that calls `os.ReadFile` is the
same hole one frame deeper. The guard scans `cmd/` *and* `pkg/`.

Never open `/dev/stdin` as a path — the seam swaps the `os.Stdin` *variable*, so
a path slips past it and doesn't exist on Windows. Use `readFileFlag` (in
`cmd/`), `os.Stdin`, or `vfs.ReadFileOrStdin`.

*Guard*: `go test ./cmd/ -run TestUserFilePathsGoThroughVFS`

### 2. No subprocess — and no host disk — without a capability gate

Spawning (`exec.Command`, `syscall.Exec`) belongs in one of the five gateway
files, each gated on a `cmd.Capabilities` field. Host-disk features outside the
vfs seam are gated the same way: result spilling needs `HostDiskSpill`, because a
spilled file outlives the request on the server and its path means nothing to the
caller. Embedded callers grant nothing, so those paths become structurally
unreachable rather than merely discouraged.

*Guard*: `go test ./cmd/ -run TestSubprocessSpawnsConfinedToGateways`

### 3. No `os.Exit` in command bodies

An in-process caller dies with it. Return an error — `*silentExitError` for a
specific exit code with no extra message.

*Guard*: `go test ./cmd/ -run TestNoOsExitOutsideExecute`

### 4. Credentials come from `LoadConfig()`, never from the process

A `Session` transparently swaps in a synthetic single-context config. Reaching
for `os.Getenv("DTCTL_TOKEN")`, the keyring, or a config path directly would
hand one tenant's request the host's credentials — a session has scrubbed all of
them precisely so that cannot happen.

### 5. Output stays byte-identical to the CLI

Print via `pkg/output`, the invocation's streams (`currentStdout(ctx)` /
`currentStderr(ctx)` in `cmd/`), or the command's own cobra writers. A `pkg/`
type takes its writers from its caller (`WithStreams`, `PrinterOptions.Writer`,
the `...To` / `...With` variants) and keeps the process streams only as its
default. Never name `os.Stdout`/`os.Stderr` or call `fmt.Print*` in `cmd/` on a
request path: the serialized seam swaps the process streams, but an invocation
on a tree of its own cannot — there is one `os.Stdout` and several invocations
— so such a write lands in the host's log instead of the response. Don't cache
a writer across invocations; don't make output depend on the host environment.

*Guard*: `go test ./cmd/ -run 'TestNoProcessStreamWritesOnRequestPaths|TestRequestPathsInjectTheirStreams'`

*Guard*: `go test ./pkg/engine/ -run TestEngineOutputEqualsCLI` (builds the real
binary and diffs CLI vs. engine stdout/stderr/exit code)

### 6. New top-level command? Decide if it belongs in a service

Add it to `unsupportedCommands` in `pkg/engine/policy.go` **with a reason** if it
manages host-local state (config, keyring, shell, installed tools, an
interactive session) or would nest the service in itself. The reason is
user-facing — it appears in the `unsupported_in_service` error's suggestions.

### 7. Commands are built by constructors and read their run's state from their context

Every command is a `func newXCmd() *cobra.Command` that carries its whole
wiring — flags, required marks, hooks, stability tier — so a second, freshly
allocated tree can be built from the same constructors (`newCommandTree`,
`cmd/root_factory.go`). The CLI's own tree is still wired by `init()` from those
constructors, so a command attached in `init()` but missing from
`newCommandTree` (or the reverse) is a defect. A command-specific flag binds to a
variable local to its constructor, never a package variable.

A running command reaches its streams, environment, files, session, capabilities
and root flag values through `cmdContext(cmd)` and the accessors in
`cmd/invocation.go` (`currentStdout(ctx)`, `getenv(ctx, key)`, `vfsEnv(ctx)`,
`curFlags(ctx)`, ...), not through package variables. Each accessor falls back
to the process-level default when the context carries no invocation, which is
what keeps the CLI unchanged. Code that runs while a command is being *built* has
no context yet: it is handed its storage explicitly.

A command that sets its own `PersistentPreRunE` must run its parent's first.
Cobra runs only the nearest one, and the root's is what runs `initConfig` on a
concurrent tree and rejects a `--dry-run` the command does not implement on
both. `attachPreviewNotice` is the pattern.

*Guards*: `go test ./cmd/ -run TestNewCommandTreeMatchesSingleton` — the fresh
tree has the singleton's surface, command by command and flag by flag —
`go test ./cmd/ -run TestEveryCommandReachesTheRootPreRunHook`, and
`go test -race ./pkg/engine/ -run TestConcurrent`, which runs a corpus on
overlapping invocations and requires every result to equal the serialized one.

Design and rationale: [docs/dev/CONCURRENT_EXECUTION.md](docs/dev/CONCURRENT_EXECUTION.md).

## Privacy

Never put customer names, employee names, usernames, or specific Dynatrace environment identifiers into the codebase, GitHub issues, PRs, release notes, or commits.

## Common Pitfalls

❌ **Don't** add query filters as CLI flags (e.g., `--filter-status`)  
✅ **Do** use DQL: `dtctl query 'fetch logs | filter status == "ERROR"'`

❌ **Don't** assume resource names are unique  
✅ **Do** implement disambiguation or require ID

❌ **Don't** print to stdout in library code  
✅ **Do** return data, let cmd/ handle output

❌ **Don't** skip safety checks on mutating commands  
✅ **Do** add safety checks to ALL create/edit/apply/delete/update/exec commands

❌ **Don't** build an integration on `dtctl exec api` for longer than it takes to add a native command. This is advice, not a stability warning: the command is `stable`, but a passthrough doesn't validate input, resolve names or format output\
✅ **Do** add a native command for the API instead (`dtctl get apis --uncovered` is the backlog)

❌ **Don't** read a user-supplied path with `os.ReadFile` / `os.Open`  
✅ **Do** use `vfs.ReadFile` / `vfs.ReadFileOrStdin` (see Embedding Invariants)

❌ **Don't** call `exec.Command` or `os.Exit` from a command body  
✅ **Do** go through a capability gateway, and return errors instead of exiting

❌ **Don't** send `page-size` together with `next-page-key`/`page-key` on paginated requests  
❌ **Don't** drop filter/search params on subsequent pages — page tokens do NOT always preserve them  
❌ **Don't** send `schemaIds`/`scopes` with `nextPageKey` on Settings API — the page token embeds everything  
✅ **Do** use the pagination pattern below: only `page-size` goes in the if/else; filters go outside (except Settings API)

## Pagination Pattern (CRITICAL)

Dynatrace APIs **reject** requests that combine `page-size` with `next-page-key`/`page-key` (HTTP 400). However, **page tokens do NOT always preserve filter parameters** — the Document API is a confirmed example where the `filter` param is dropped on page 2+ if not resent. To be safe, **always resend filter/search params on every page request**, and only exclude `page-size` when a page key is present.

**Exception — Document API**: The Document API (`/platform/document/v1/documents`) does **not** reject `page-size` + `page-key`. It also does **not** embed the page size in the page token (defaulting to 20/page if `page-size` is omitted). For Document API endpoints, send `page-size` on every request alongside `page-key`. See `pkg/resources/document/document.go` for the reference implementation.

**Exception — Settings API**: The Settings API (`/platform/classic/environment-api/v2/settings/objects`) rejects ALL other query params when `nextPageKey` is present — not just `pageSize`, but also `schemaIds`, `scopes`, and `fields` (HTTP 400: "must not be used in combination with nextPageKey query parameter"). The page token embeds everything. For Settings API endpoints, send ONLY `nextPageKey` on page 2+. See `pkg/resources/settings/settings.go` for the reference implementation.

### Correct pattern (default — most APIs)

```go
for {
    req := h.client.HTTP().R().SetResult(&result)

    if nextPageKey != "" {
        req.SetQueryParam("page-key", nextPageKey)
    } else if chunkSize > 0 {
        req.SetQueryParam("page-size", fmt.Sprintf("%d", chunkSize))
    }
    // Always send filter, regardless of pagination
    if filter != "" {
        req.SetQueryParam("filter", filter)
    }

    resp, err := req.Get("/platform/...")
    // ... handle response, break if no more pages
}
```

### Correct pattern (Document API — accepts page-size with page-key)

```go
for {
    req := h.client.HTTP().R().SetResult(&result)

    if nextPageKey != "" {
        req.SetQueryParam("page-key", nextPageKey)
    }
    // Document API: send page-size and filter on EVERY request
    if chunkSize > 0 {
        req.SetQueryParam("page-size", fmt.Sprintf("%d", chunkSize))
    }
    if filter != "" {
        req.SetQueryParam("filter", filter)
    }

    resp, err := req.Get("/platform/document/v1/documents")
    // ... handle response, break if no more pages
}
```

### Correct pattern (Settings API — page token embeds ALL params)

```go
for {
    req := h.client.HTTP().R().SetResult(&result)

    // Settings API rejects ALL other params when nextPageKey is present
    // (pageSize, schemaIds, scopes are all embedded in the page token).
    if nextPageKey != "" {
        req.SetQueryParam("nextPageKey", nextPageKey)
    } else {
        if chunkSize > 0 {
            req.SetQueryParam("pageSize", fmt.Sprintf("%d", chunkSize))
        }
        if schemaID != "" {
            req.SetQueryParam("schemaIds", schemaID)
        }
        if scope != "" {
            req.SetQueryParam("scopes", scope)
        }
    }

    resp, err := req.Get("/platform/classic/environment-api/v2/settings/objects")
    // ... handle response, break if no more pages
}
```

### Wrong pattern 1: sending page-size with page-key (causes HTTP 400 on non-Document APIs)

```go
for {
    req := h.client.HTTP().R().SetResult(&result)

    // BUG: page-size is sent on EVERY request, including subsequent pages
    if chunkSize > 0 {
        req.SetQueryParam("page-size", fmt.Sprintf("%d", chunkSize))
    }
    if filter != "" {
        req.SetQueryParam("filter", filter)
    }
    if nextPageKey != "" {
        req.SetQueryParam("page-key", nextPageKey)
    }

    resp, err := req.Get("/platform/...")
}
```

### Wrong pattern 2: dropping filter on page 2+ (causes unfiltered results)

```go
for {
    req := h.client.HTTP().R().SetResult(&result)

    if nextPageKey != "" {
        // BUG: only sends page-key, drops filter on subsequent pages
        req.SetQueryParam("page-key", nextPageKey)
    } else {
        if filter != "" {
            req.SetQueryParam("filter", filter)
        }
        if chunkSize > 0 {
            req.SetQueryParam("page-size", fmt.Sprintf("%d", chunkSize))
        }
    }

    resp, err := req.Get("/platform/...")
}
```

### Test guard (required for paginated mock servers of non-Document APIs)

Every test mock server for a paginated endpoint (except Document API) **must** include a constraint guard that rejects the invalid combination, so the bug is caught in tests:

```go
// Simulate API constraint: page-size must not be combined with page-key
if r.URL.Query().Get("page-size") != "" && r.URL.Query().Get("page-key") != "" {
    w.WriteHeader(http.StatusBadRequest)
    w.Write([]byte(`{"error":{"code":400,"message":"Constraints violated."}}`))
    return
}
```

For Settings API mocks, the guard must also reject `schemaIds`, `scopes`, and `fields` with `nextPageKey`:

```go
// Simulate Settings API constraint: pageSize, schemaIds, scopes, and fields
// must NOT be combined with nextPageKey (all are embedded in the page token).
if r.URL.Query().Get("nextPageKey") != "" {
    for _, param := range []string{"pageSize", "schemaIds", "scopes", "fields"} {
        if r.URL.Query().Get(param) != "" {
            w.WriteHeader(http.StatusBadRequest)
            fmt.Fprintf(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
            return
        }
    }
}
```

**Reference implementations**: `pkg/resources/document/document.go` (Document API pattern), `pkg/resources/settings/settings.go` (Settings API pattern), `pkg/resources/extension/extension.go` (default pattern)

## Code Examples

- Simple CRUD: `pkg/resources/bucket/`
- Complex with subresources: `pkg/resources/workflow/`
- Execution pattern: `pkg/exec/workflow.go`
- History/versioning: `pkg/resources/document/`

## Resources

- **Design**: [docs/dev/API_DESIGN.md](docs/dev/API_DESIGN.md)
- **Architecture**: [docs/dev/ARCHITECTURE.md](docs/dev/ARCHITECTURE.md)
- **Status**: [docs/dev/IMPLEMENTATION_STATUS.md](docs/dev/IMPLEMENTATION_STATUS.md)
- **Embedding/service model**: [docs/dev/SERVICE_ENGINE_DESIGN.md](docs/dev/SERVICE_ENGINE_DESIGN.md)
- **Stability tiers**: [docs/STABILITY.md](docs/STABILITY.md) (generated inventory + tier reference)
- **API discovery + passthrough**: [docs/dev/GENERIC_API_ACCESS.md](docs/dev/GENERIC_API_ACCESS.md)
- **Future Work**: [docs/dev/FUTURE_FEATURES.md](docs/dev/FUTURE_FEATURES.md)

---

**Token Budget Tip**: Read API_DESIGN.md Design Principles section first (most critical context). Skip reading full ARCHITECTURE.md unless making structural changes.
