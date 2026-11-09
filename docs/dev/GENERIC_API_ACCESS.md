# Generic API Access — Spec Discovery and HTTP Passthrough

**Status:** Implemented
**Created:** 2026-08-13
**Audience:** anyone extending API discovery, the passthrough, or the native-coverage map.

> **Implementation:** `sdk/api/apispec/` (registry + specification parsing, scope
> classification), `pkg/resources/api/` (CLI handler, display projections,
> `Classify`, the coverage map), `cmd/get_apis.go`, `cmd/describe_api.go`,
> `cmd/exec_api.go`, `cmd/check_scopes.go` (per-call scope verdict),
> `pkg/commands/listing.go` (unadvertised resources).

## Overview

dtctl wraps roughly twenty Dynatrace APIs natively. An environment publishes many
more. Until now the gap had no governed answer: a caller who needed an unwrapped
API either dropped out of dtctl entirely or reached for
`dtctl exec function --code` with ad-hoc JavaScript — which AppEngine runs with a
bearer token, making the least governed path in the tool also the most convenient
one.

This feature closes the gap in two halves, and the second only makes sense
because of the first.

**Part A — discovery.** `dtctl get apis` lists what the environment publishes
specifications for; `dtctl describe api <name>` projects one specification into an
operation index, or one operation in full.

**Part B — passthrough.** `dtctl exec api <path>` sends a request to any endpoint,
gated by the same safety checker as every native mutating command — with the
operation class derived from the API's *own specification* rather than guessed
from the HTTP method.

```bash
dtctl get apis --uncovered              # what has no native command yet
dtctl describe api example              # its operations, at a coarse grain
dtctl describe api example --operation 'POST /things'
dtctl exec api /platform/example/v1/things -X POST -d @body.json
```

## Goals

1. **Make the platform's surface visible.** An agent that can list APIs and read
   one operation's schema can use an unwrapped API correctly on the first try.
2. **Govern the escape hatch instead of pretending it does not exist.** A
   passthrough inside dtctl inherits contexts, safety levels, profiles, agent
   envelopes, and audit-visible output. The alternative callers already use
   inherits none of that.
3. **Derive authority from the specification.** What a request may do is a
   property of the endpoint, published by the environment — not of the verb the
   caller typed.
4. **Don't become the integration target.** See below. This is design advice
   about where integrations should end up, not a stability caveat: `exec api`
   is `stable`.
5. **Disclose nothing.** dtctl mirrors the environment's index and filters
   nothing, and no committed artifact names a non-public API.

## Non-Goals

- **Client-side request validation.** Specifications are per-environment and
  per-version; a validator that rejects a request the platform would have accepted
  is strictly worse than the platform's own 400. The specification is used on the
  *failure* path instead (`apiRequestError`), where it costs nothing when the call
  succeeds and tells a caller what the payload should have been in one round trip.
- **A generic write E2E.** No endpoint is harmless on every tenant, so the live
  tests exercise reads and classify writes without sending them.
- **Reaching another host.** An absolute URL is refused, not followed: the
  context's credentials belong to the environment it names.
- **A local spec cache.** Every invocation reads the index fresh. A stale cache
  would answer "this API does not exist" about an environment that now publishes
  it.

## Part A — Discovery

### Conventions, not a documented contract

Three facts about the platform underpin discovery, and all three are
**conventions observed to hold**, not published contracts:

| Convention | Value |
|---|---|
| API index | `/platform/metadata/v1/swagger-ui.json` (the document the environment's own Swagger UI reads) |
| Specification document | `<base-path>/openapi.yaml` |
| Classic environment API | `<base-path>/spec3.json` (JSON, and the one documented exception) |

The index is an array of `{name, url}` pairs. A base path is derived from the
document URL by stripping the filename, which is why `Entry.BasePath()` and
`SpecPathForBase()` are inverses and why the E2E test asserts they round-trip on a
live index.

Two hard rules follow from "convention, not contract":

- **Always parse as YAML, never branch on `Content-Type`.** YAML is a superset of
  JSON, so one parser reads both `openapi.yaml` and `spec3.json`. Branching on the
  served content type would break the day a gateway relabels a document — and it
  has no upside.
- **Failures are typed and expected.** An environment may publish the index and
  refuse the documents (an interactive-session-only SSO policy does exactly
  this). `SpecUnavailableError` degrades one row of `get apis`; it never breaks the
  listing.

### "Not published" and "refused" are different answers

`RegistryUnavailableError` carries the HTTP status and phrases itself from it:

| Status | Message |
|---|---|
| 404 / no response | this environment publishes no machine-readable API index at `<path>` |
| 401 / 403 | not authorized to read the API index at `<path>` (HTTP n) |
| 5xx | the API index is temporarily unavailable |

This distinction was not theoretical. During development a stale token produced a
401, dtctl reported "this environment publishes no API index", and the E2E suite
skipped — a statement about the *environment* made from evidence about the
*credential*. Only a 404 licenses that claim. The agent-mode hints follow the same
split: on a 401/403 the suggestion is to check the token, and the hint says
explicitly that the refusal says nothing about whether an index exists.

### The projection is the feature

A published specification runs from roughly 10k to 47k tokens. Handing that to an
agent is unaffordable and handing it to a human is unreadable, so
`describe api` has three views at different grains:

| View | Output |
|---|---|
| default | operation index — every operation as `METHOD /path`, its summary, its declared scope |
| `--operation 'METHOD /path'` | one operation in full: parameters, request-body schema (one level), responses, required scopes, and a ready-to-run `dtctl exec api` invocation |
| `--raw` | the unprojected document, in the encoding it was served in |

The default view is **complete at a coarser grain**, not truncated — every
operation appears, so no drill-down target is hidden. `--raw` goes through the
same result-spill machinery as a large query result, gated on the `HostDiskSpill`
capability so an embedded caller gets bytes rather than a path it cannot read.

Two projection details worth keeping:

- An operation's ID is `METHOD /path`, and it must round-trip back into
  `--operation`. A specification may key an operation on the **empty** path
  (meaning the base path itself is the endpoint); left alone that yields the
  ID `"GET "`, which no caller can pass back and which never matches a request,
  because `Match` normalizes an incoming base path to `/`. `collectOperations`
  normalizes `""` → `"/"` at parse time. Three live specifications on one tenant
  hit this.
- A blank SCOPE means *the specification declares none*, not that none is
  required. The help text says so, because the opposite reading is the dangerous
  one.

## Part B — The Passthrough

### The HTTP method does not decide the gate

This is the load-bearing decision of the whole feature.

The obvious design keys safety on the verb: GET is a read, POST is a create,
DELETE is a delete. It is wrong, and measurably so. A sweep of one production
index turned up **15 POST operations, across three publicly documented APIs, that
declare only a read scope** — verification, validation, and search endpoints that
are POSTs purely because they take a body. (The narrower sample quoted in
`methodFloor`'s comment found 12 such POSTs in 64 specified operations, plus one
POST that declares `:delete`.) A method-keyed gate would refuse the reads in a
`readonly` context, which is exactly the context where a caller most wants them —
and in the other direction it would wave the delete through as a create.

So classification takes the **stricter of two independent lower bounds**:

```
verdict = escalateDestructive( stricter( methodFloor(method), accessFloor(method, access) ) )
```

| Method | `methodFloor` | Why |
|---|---|---|
| GET, HEAD | Read | conventional |
| **POST** | **Read** | POST carries no information (see above) |
| PUT, PATCH | Update | modifies something that exists |
| DELETE | Delete | conventional |
| anything else | Delete | unclassifiable fails closed |

`accessFloor` reads the scope verbs the specification declares for the operation
(`…:read`, `…:write`, `…:delete` → `AccessRead/Write/Delete`). `AccessWrite` on a
POST is a create; on a PUT/PATCH an update.

Taking the stricter of two floors means **neither source can weaken the other**: a
specification cannot turn a DELETE into a read, and a method cannot turn a
declared delete into a create. The live E2E test asserts precisely this invariant
across every operation the environment publishes — no verdict is ever looser than
the method's floor.

**Accepted tradeoff, eyes open:** for POST, gate correctness in a `readonly`
context depends entirely on the accuracy of the specification the environment
serves at request time — a POST is admitted as a read exactly when the document
declares only `:read` scopes for it. This is deliberate. Any stricter POST floor
is wrong in both directions (it refuses the DQL API's read-only POSTs, and it
still under-gates the POST that deletes), and the specification is the
environment's own statement about its own endpoint. An environment that
misdeclares a mutating POST as `:read` is lying to every client, not just this
one — but it is worth stating that the gate inherits that lie rather than
detecting it.

### Unknown means delete

When no operation matched — the path belongs to no listed API, the specification
could not be read, or it declares no scope — a non-read method is gated as
`OperationDelete`: the strictest generally-reachable operation. Escalating rather
than asking means existing safety semantics apply unchanged (with unknown
ownership, `OperationDelete` proceeds only at `readwrite-all`), so no new safety
level had to be invented for "might be anything".

**There is deliberately no flag to assert the operation.** A `--op create` flag
would let the caller who knows least about the endpoint overrule the gate, which
is the entire value of the gate. The error message says so explicitly — but only
when the verdict really was unresolved, which is why `Classification.Escalated`
exists: a curated escalation is a *deliberate* classification, and apologising for
being unable to classify it would be a lie.

### The one curated table

`destructivePatterns` is the single place a hard-coded override is unavoidable,
and it is the only place classification errs *lax* rather than strict.
`OperationDeleteBucket` sits above `OperationDelete` — only
`dangerously-unrestricted` permits it — and nothing about a URL says "this
destroys a data store". Without the table, deleting a bucket through the
passthrough would pass at `readwrite-all` while `dtctl delete bucket` demands
`dangerously-unrestricted`: **the passthrough would be a strictly weaker gate than
the command it shadows.** That is the failure mode the table exists to prevent, and
`TestExecAPIDoesNotUndercutTheCommandItShadows` is what keeps it prevented.

It currently holds three entries (bucket deletion, bucket truncation, record
deletion). Add one whenever an irreversible endpoint's destructiveness is invisible
in its URL.

**The table matches spellings, not just strings.** The server-side router
percent-decodes and normalizes a path before routing, so
`bucket-definitions/foo%3Atruncate`, `x/../bucket-definitions/foo`, and
`management//v1/bucket-definitions/foo` all reach the endpoint the canonical
spelling names — and a table matched only against the literal request text
would gate each of them one level too low. Two mechanisms close this:

1. `escalateDestructive` matches every *routable spelling* of the path — the
   literal text, each successive percent-decoding (to a bounded fixpoint, for
   double-encoding), and the dot-segment/duplicate-slash-normalized form of
   each. This is safe precisely because escalation only ever raises the gate.
2. `exec api` refuses any spelling that changes **structure** under
   normalization (`CanonicalRequestPath`): dot segments, duplicate or trailing
   slashes, encoded separators, control characters, and router-delimiter
   characters (`;`, `\`, decoded `?`/`#`). Benign percent-encoding (`%20` in an
   identifier) is accepted, and classification runs on the *decoded* form —
   the path the server routes — while the caller's spelling goes on the wire.

`TestClassify_NormalizationDoesNotDefeatEscalation` and
`TestExecAPIRefusesAmbiguousPathSpellings` pin both halves.

### Never the integration target

This is advice about where an integration should end up, which is a native
command (`dtctl get apis --uncovered` is the backlog). It is **not** a statement
about the command's stability contract. `exec api` (and `exec dql`) are
declared `stable`: the command's shape (path argument, `-X`, `-d`, `-H`,
`--dry-run`, the safety gate, the refusal behaviour) is additive-only like any
other stable command. What it *reaches* is whatever the environment's APIs
offer, and those change on the platform's schedule, not dtctl's. The response
body it passes through is therefore outside dtctl's stable output contract.
Only the invocation and how dtctl frames the response are covered. Demoting it
was considered and rejected (#544): under a `stable` floor, which CI and
embedding services pin, it would stop working in exactly the automation that has
no native alternative yet.

An escape hatch that becomes the normal way to use dtctl has failed, even while
working. Every native command validates input, resolves names, formats output, and
cannot be pointed at the wrong endpoint; a passthrough does none of that. So the
design pushes callers away from it at four points:

1. **Unadvertised.** `exec api` is `Hidden`, so it is absent from `--help`,
   completion, and the compact catalogs an agent bootstraps from — but
   `pkg/commands` lists it under `dtctl commands --full`. Cobra's two states are
   both wrong for an escape hatch: visible puts it next to the native commands it
   must not replace, and hidden makes it undiscoverable, which pushes a caller who
   genuinely needs it toward something worse. The `unadvertisedResources` map is
   that third state — findable by a caller who goes looking, invisible to one who
   is browsing.
2. **It names its replacement.** When a path is covered natively, the command
   warns with the *runnable* native command, and a refusal repeats it as the
   preferred route.
3. **The help text says it.** "If you find yourself scripting against it, that API
   wants a native command instead."
4. **No profile grants it.** Neither builtin profile (`query`, `investigate`)
   includes it; a profiled caller gets `profile_blocked`.
   `TestNoBuiltinProfileGrantsTheEscapeHatch` pins that.

`get apis --uncovered` is the constructive half of the same principle: it turns
the gap between the platform and dtctl into a contribution backlog. It filters to
entries on the public `/platform/` tree, because a native command should only be
proposed for a state-of-the-art, publicly supported API.

### The coverage map

`pkg/resources/api/coverage.go` maps a base path to `{Resource, Command}`.
`Resource` is the label in the DTCTL column; `Command` is what a caller should
type — and it is deliberately **not** derived from `Resource`, because not every
wrapped API is reached through `get <resource>` (`dtctl query`,
`dtctl exec copilot`, `dtctl exec preview-processor`). A suggestion that does not
run is worse than none: it teaches a caller that dtctl's advice is unreliable.
The first draft suggested `dtctl get query`, `dtctl get copilot`, and
`dtctl get lookup-tables`, none of which exist —
`TestNativeCoverageNamesRealCommands` walks every entry through `rootCmd.Find`,
which is how the third one was caught.

Path matching uses the longest base-path prefix at a segment boundary, so
`/platform/storage/management/v1` does not claim
`/platform/storage/management/v1x`.

### Other passthrough decisions

- **A body requires an explicit `-X`.** curl promotes `-d` to POST; dtctl refuses.
  Inferring the method would infer the safety operation, which contradicts the
  command's premise.
- **`-H` cannot supply credentials.** `Authorization` and `Proxy-Authorization`
  are refused: the context is the identity, dtctl attaches its credentials
  itself, and a caller-supplied value would silently fight the client's own
  auth (whichever writes last wins). Changing identity means changing context.
- **Embedded in a service, `-H` cannot set routing or origin headers.**
  `Host`, `Forwarded`, `X-Forwarded-*`, `X-Real-Ip`, `X-Client-Ip`,
  `True-Client-Ip` and the `Dt-*` namespace are refused when a session is set.
  The infrastructure in front of an environment sets them, and the services
  behind it may trust them. In a service, dtctl sends a request from inside the
  platform's network, where a forged value is more likely to be believed. Go
  already dials and names the TLS server after the session's environment, so a
  caller-set `Host` would only make the request disagree with itself. The `Dt-*`
  headers a client is meant to send (`Dt-Client-Context`, `Dt-External-Source`)
  stay allowed; dtctl sends both itself. The CLI sends all of these as given: its
  request leaves the caller's own machine with the caller's own token, which
  could send the same headers with any HTTP client.
- **The output protocol is declared, not sniffed.** A JSON body goes through the
  normal printer, so `-o json/yaml` and the agent envelope behave as they do
  everywhere else. Anything else is passed through **verbatim, even under
  `--agent`** — a passthrough can return YAML, CSV, Prometheus text, or a binary
  archive, and wrapping those would corrupt them, while refusing them would make
  the command useless for the exports it exists to reach. Verbatim means
  byte-exact: the newline that completes a line for a human terminal is added
  only when stdout *is* one, because a redirected archive that gained a trailing
  byte no longer matches the checksum the API reports for it (#643). The same
  applies to `describe api --raw`, whose whole purpose is a document the caller
  redirects. An HTML body earns a warning on stderr (it is almost always a login
  redirect), which keeps agent-mode stdout pure.
- **`--dry-run` reports the verdict instead of enforcing it.** Showing the composed
  request plus "this would be BLOCKED, here is why" is more useful than an error
  that hides the request. Credential-bearing headers are redacted in that output,
  because a dry run is what gets pasted into a bug report.
- **Errors preserve the platform's message** (capped at 4 KiB) and add what dtctl
  knows: the operation's schema command, its declared scope, the native command.

### `--check-scopes` gained a third verdict

Scope preflight is a static table keyed by `verb resource`. `exec api` has no
static answer — the required scope depends on the path — so
`scopeRequirement` became three-way: `None`, `Known`, `PerCall`.

- **Explicit `--check-scopes`** resolves `PerCall` → `Known` by reading the
  specification for the path the caller passed. It is explicit and terminal, so
  spending a request is exactly what the caller asked for.
- **The agent-mode auto-preflight** deliberately does not: it runs ahead of *every*
  command and must stay free. It bails on anything that is not `Known`.
- Otherwise the verdict is `scopeStatusUnknown`, with suggestions pointing at
  `describe api`. "No platform scopes required" would have been a lie.

## Disclosure rules

These are requirements, inherited from the design review and reflected in the
tests.

1. **dtctl mirrors the index and filters nothing.** Any dtctl-side filter of the
   API list would have to hard-code which APIs to conceal — and since dtctl is
   open source, *that list would itself be the disclosure*. What an environment
   publishes is decided by whoever configures it.
2. **Resolution never guesses.** `Resolve` consults only what the registry
   returned; it never synthesizes a candidate path from a name and retries it,
   which would turn a name lookup into an existence oracle for APIs an environment
   deliberately omits. A fully-qualified base path the caller already typed is
   accepted as given, because echoing it back discloses nothing.
3. **No committed artifact names a real non-public API.** Help text, examples,
   docs, golden files, and E2E fixtures use synthetic names
   (`/platform/example/v1/things`, `@example.invalid`).
4. **Golden output must not vary by tenant class.** All golden fixtures are
   synthetic; none is captured from a live environment.
5. **E2E tests assert mechanism invariants, never an API list.** A fixture list of
   expected APIs would be both a leak and a bug — the index is a property of the
   environment, so a hard-coded expectation would encode one tenant's shape and
   fail on another, which is the very problem this feature exists to solve.
6. **The stability warning is phrased in terms of the public tree** ("outside the
   public `/platform/` API tree"), never by naming a non-public prefix. It is a
   statement about compatibility guarantees, not about what exists.

## Embedding notes

The passthrough obeys the [service engine](SERVICE_ENGINE_DESIGN.md) invariants:

- `-d @file` goes through `vfs.ReadFileOrStdin`, so under an embedded invocation it
  reads the *request's* file and `@-` reaches the swapped `os.Stdin`. Never
  `/dev/stdin`.
- `--raw` spilling is gated on `HostDiskSpill`; an embedded caller gets the bytes.
- No `os.Exit`, no subprocess, credentials only via `LoadConfig()`/`Setup*`.
- Output goes through `pkg/output`/`fmt`, so the stream seam catches it.

`exec api` is **not** in `unsupportedCommands`: it is a plain HTTP call against the
request's own environment, which is exactly what a service embedding dtctl wants
to be able to do.

## Testing

| Layer | Where | What it pins |
|---|---|---|
| Spec parsing | `sdk/api/apispec/apispec_test.go` | YAML+JSON, `$ref` resolution, path templating, the collection-root normalization |
| Scope classification | `sdk/api/apispec/classify_test.go` | scope verb → access |
| Gate | `pkg/resources/api/classify_test.go` | both floors, escalation table, message wording |
| Command behavior | `cmd/exec_api_test.go` | method-does-not-decide-the-gate, unresolved → delete, no undercutting, path/method refusals, vfs + stdin seams, dry-run leakage, output protocol |
| Discovery commands | `cmd/api_discovery_test.go` | listing, `--uncovered`, `--ops-count`, refused-index messaging |
| Conventions | `cmd/api_coverage_test.go` | coverage commands exist, unadvertised-but-documented, no profile grants it, discovery survives a restricted profile |
| Output | `pkg/output/golden_test.go` | 11 golden files across both resources and six formats |
| Live | `test/e2e/api_test.go` (`integration`) | index parses, entries round-trip, specs parse or fail typed, no verdict looser than its floor, coverage claims vs. the live index |

`TestExecAPIMethodDoesNotDecideTheGate` is the one to keep working: same API, same
`readonly` context, a read-scoped POST permitted and a write-scoped POST refused.
It was mutation-tested in both directions.

## Open questions

- **Coverage-map drift.** Two entries look stale against live indexes: `settings`
  is keyed on the classic environment-API base path while at least one environment
  publishes a `/platform/settings/v1`, and no checked environment publishes the
  GraphQL base path claimed for breakpoints. The E2E test logs (does not fail) such
  claims, because coverage spans every tenant shape and no single tenant runs every
  service.
- **Escalation table maintenance.** It only grows by someone noticing. A periodic
  sweep for irreversible endpoints (`:truncate`, `record-deletion`, and successors)
  is the realistic mechanism.
- **`--ops-count` costs one request per API.** Fine at ~100 APIs, and it is
  opt-in. A concurrency bound would help if indexes grow.
