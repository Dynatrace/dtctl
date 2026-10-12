# Repo Scope Design

**Status:** Implemented, experimental since 0.43.0 (`dtctl repo-scope`, `query`/`wait query`/`verify query --repo-scope` and `--no-repo-scope`)

Link a git repository to the entities that run its code, so that a developer, or an agent working in their checkout, can query "this code's" logs and spans without naming a service. User guide: [REPO_SCOPE.md](../REPO_SCOPE.md).

---

## Why not filter segments

Filter segments are how Dynatrace scopes a query, and `dtctl query` already takes `--segment`. An entry is not a segment for three reasons:

1. **Creating a segment is a write.** It needs `storage:filter-segments:write`, and linking a repository has to work for a developer with ordinary, read-only permissions. A repo scope is set up by reading (`discover`) and by writing a local file (`set`); nothing in the environment changes.
2. **A segment cannot express per-directory scope.** It is an environment object with one meaning wherever it is used. A monorepo needs a different filter depending on the directory a command runs from, which only the client knows.
3. **The link belongs with the code.** It is reviewed, versioned and branched with the repository, and every clone has it. A segment lives in the environment, outside that review.

Rendering an entry onto an existing segment, so that the platform applies it, is a follow-up; the file format does not stand in its way.

## Background

The question a developer asks from a checkout is about the code in front of them: what is failing in *this* service, how slow is *this* endpoint. The environment answers in terms of entities (service and process group ids, `service.name`, Kubernetes workloads), and nothing in dtctl connected the two. Every query had to name the entity, and the names in the repository (a Go module, a Helm chart, a Maven artifactId) are rarely the names the environment records.

A repo scope is that connection, written down once and committed: per environment, which entities run this repository's code. Queries made from inside the repository are narrowed to them by default.

## Overview

| Piece | Where | What it does |
|---|---|---|
| Scope file | `.dtctl-repo-scope.yaml` at the repository root | Bindings per environment host; no DQL |
| `pkg/reposcope` | `schema.go`, `decode.go`, `store.go`, `resolve.go`, `grail.go`, `render.go`, `extract*.go`, `tokens.go`, `discover.go`, `rank.go` | Validate, load and save the file, resolve an entry for a directory, render filters, read names from a repository, plan and run discovery |
| `pkg/dql` | `scan.go`, `insert.go` | The offset-preserving DQL scanner, and the lexical insertion of the filter |
| `cmd/repo_scope.go` | `dtctl repo-scope discover\|set\|current\|describe\|delete\|list` | Manage the file |
| `cmd/query_repo_scope.go` | `query`, `wait query`, `verify query` | Apply the scope, print the notice, decorate the envelope |

`pkg/reposcope` has no view of the invocation. Where the working directory comes from, whether the host grants access to it, and how anything is printed stay in `cmd`; so do flag names, which is why the `set` line discovery proposes is built in `cmd` from a candidate's fields. `store.go` is the only file in the package that touches the host disk; everything else works on values and an `fs.FS`. `pkg/dql` imports nothing from dtctl, so the executor, the hint rules and the scope file share one scanner without depending on one another.

Every Grail field and data object name the package puts into DQL is in `grail.go`. They are fields spans and logs carry today, and the entity ids are classic ids (`SERVICE-…`, `PROCESS_GROUP-…`). `service.name` and `k8s.workload.name` are stable fields in the Dynatrace semantic dictionary; both id fields are deprecated there, `dt.entity.service` in favour of `dt.smartscape.service` and `dt.entity.process_group` in favour of `dt.process_group.id` (Smartscape has no process group entity). See [Evolving the schema](#evolving-the-schema) for what that means for the file.

## The file

### A root file, not `.dtctl/`

The scope file sits at the repository root beside `.dtctl.yaml`, not in a `.dtctl/` directory. `.dtctl/` is ignored in this repository's own `.gitignore` and reads, by convention, as local state; a mapping meant to be committed and reviewed must not live where tooling expects to ignore it.

The walk-up that finds it stops at the first directory holding `.git` (a directory, or the file a worktree or submodule carries), and the file must be in that directory. It never walks past a `.git`, so a parent repository's scope cannot leak into a nested one, and it never accepts a file in a directory without `.git`. This is deliberately not the `.dtctl.yaml` search, which walks to `/`: a scope belongs to one repository. The working directory is resolved through symlinks first; a failed `Getwd` or an unreadable parent means "not in a repository", as it does for the local config.

Only a regular file of at most 1 MiB is read. git commits symlinks, so a cloned repository could otherwise point the file at `/dev/zero` or a FIFO and hang every query run inside it.

### Keyed by host

Entries are filed under the environment host (`urls.Host` of the context's environment URL), not the context name. Context names are local to one machine; the file is shared by everyone who clones the repository. It is the same reasoning the config contract applies to token bindings.

### Bindings, never DQL

The file holds structured bindings (service ids, process group ids, service names, namespace/workload pairs), and dtctl renders the filter from them. The alternative, a DQL snippet per entry, is the obvious design and the wrong one: the file arrives with every clone, so it is untrusted input to every query run in the checkout. A snippet could widen a query (`or true`), redirect it, or inject text. Rendered bindings can only narrow: every value is held to its format (`^SERVICE-[0-9A-F]{16}$`, DNS labels for namespaces, DNS subdomains for workloads, printable service names without `"` or `\`) and quoted with `dql.Quote`, and the rendered expression is a disjunction of equality and membership tests on a fixed set of fields.

| Data object | `services` | `process-groups` | `service-names` | `workloads` |
|---|---|---|---|---|
| `spans` | `dt.entity.service` | — | `service.name` | `(k8s.namespace.name == ns and k8s.workload.name == w)` |
| `logs` | — | `dt.entity.process_group` | `service.name` | same |

Entity ids always render as `in(field, array(…))`, one id included: logs may carry `dt.entity.process_group` as an array, which `==` would never match. A list of service names renders the same way; one name, and every workload pair, renders as `==`.

`set` always replaces the named entry. There is no merge, no `--replace`, and no `--environment`: the root `--context` decides the host.

The file's keys are kebab-case, like the config file. Results printed as JSON or YAML use camelCase like `sdk/inventory`, and the agent envelope's `context.repo_scope` uses snake_case like every other `context` key.

### Evolving the schema

A key this dtctl does not know is read past, reported as a warning by `current`, `describe` and every scoped query, and refused by `set` and `delete`, which would drop it. A typo and a newer dtctl's key look the same at that point, so the warning names both.

That makes the rule for `apiVersion: v1`:

- A new binding that is one more alternative in the OR may be added under v1. An older dtctl warns about it and applies the entry's other bindings, which is narrower than intended but never wider. An entry bound by the new key alone binds nothing to an older dtctl, so the file fails validation there and queries run unscoped with the `invalid_file` warning until it is upgraded.
- `paths` ships with the feature in 0.43.0; `path` is the entry's primary directory and must be one of `paths`.
- Anything that constrains an entry (a binding that must hold together with the others), or changes the format of an existing value, needs `apiVersion: v2`. An older dtctl would apply such an entry wrongly rather than narrowly.

Entity ids are the change already announced. The file pins classic ids in `dt.entity.service` and `dt.entity.process_group`, and the semantic dictionary deprecates both fields. The migration is `apiVersion: v2`: `services` moves to `dt.smartscape.service` and `process-groups` to `dt.process_group.id`, each with the id format its field carries, and v1 files keep rendering onto the classic fields for as long as the records carry them. The new bindings cannot be added under v1, because they change the format of an existing value. Workload and service-name bindings depend on neither field, which is why discovery proposes them first and offers ids only for a candidate they cannot select, and why the documentation prefers them.

## Resolution

For one invocation: the entry named by `--repo-scope`, regardless of its path; otherwise the entry whose `path` covers the working directory, longest first, compared by whole segments; otherwise the entry with no path. Validation forbids two entries sharing a `path`, regardless of case; two may list one directory in `paths` (a library two Gradle deployables share), and there the tie leaves no entry, with a reason naming both, rather than one picked by file order. Paths compare case-insensitively on macOS and Windows; refusing a case-only difference on every OS keeps a committed file valid everywhere or nowhere, instead of resolving by entry order on one and by path on another.

`query` checks cheapest first, and the disk is touched only from the walk-up on: `--no-repo-scope`, or `DTCTL_NO_REPO_SCOPE` when no entry is named; then, without a name, whether the stability policy admits both repo-scope flags on the command; then the context's environment host; then the working directory, which only an invocation with the `HostWorkingDirectory` capability and no session has; then the walk-up and the file. Every "nothing applies" is silent. Every "something resolved but did not apply" (no entry covers this directory, the file does not validate, the rewrite refused) is one warning line and an unchanged query. A broken scope file never fails a query: it is committed, and one bad edit would otherwise break every query run in the repository.

Naming an entry turns each of these into an error before anything is sent, because the caller asked for that entry's rows: a file that does not load is a `validation_error`, an unknown name `not_found`, and every refusal of the rewrite (`not_fetch`, `unsupported_object`, `no_binding`, `subquery`, `unscannable`) a `validation_error` naming the code. `user_filter` is the exception: the user's own filter on a bound field is the answer they asked for, so it runs as typed with the warning.

The stability check exists because scoping is experimental, but only its two flags carry the mark, on commands that are themselves stable; a floor that hides the flags would otherwise leave the rewrite running while refusing its opt-out. So without a name, the policy the floor applied must admit both `--repo-scope` and `--no-repo-scope` on the command, or the query is sent as typed and nothing is printed. A `stability-exceptions` entry for each admits it; one for `--repo-scope` alone admits naming an entry. `DTCTL_NO_REPO_SCOPE` works under any floor.

## The rewrite

dtctl has no DQL parser by design. The rewrite (`dql.InsertFilter`) is a lexical insertion of `| filter (<expr>)` after the leading `fetch` stage, built on the one offset-preserving scanner (`dql.Scan`, which `pkg/dqlhint` uses too), and it refuses whatever it is not sure of, because a wrong rewrite silently returns the wrong data:

- the first stage is not `fetch logs` or `fetch spans` (`not_fetch`, `unsupported_object`)
- the entry binds nothing that data object is filtered by (`no_binding`)
- a `filter`/`filterOut` stage already compares a bound field (`user_filter`): the user's own filter wins. The same field in `summarize by:`, `fields`, a comment or a string is not an override
- a scoped data object is fetched again at bracket depth > 0 (`subquery`): scoping only the outer fetch would report "applied" over environment-wide rows
- an unterminated literal or comment, unbalanced brackets, or a single quote (`unscannable`)

Two invariants are tested on every row of the rewrite table: when not applied, the output equals the input byte for byte; when applied, removing the inserted range gives back the input.

A scoped query that returns nothing is diagnosed on the text the user typed, not the one sent, so the empty-result diagnosis never blames a field the scope inserted.

**Scope and scan cost.** The inserted filter narrows the rows and, as an equality filter right after `fetch`, helps the scan, but it cannot by itself keep a wide timeframe under the scan limit. `sdk/inventory/sampling.go` records the measurement: a scoped, unsampled 15-minute count of logs still scanned 341 GB on a large environment, against 500 GB for the capped unscoped read. Nothing honest can be inserted to change that: a timeframe changes the question, `samplingRatio` makes every scoped result approximate, and a scan cap is not scoping. So the rewrite sends what it always sent, and says so before sending: when neither the fetch stage (a `scanLimitGBytes` other than `-1`, `samplingRatio`; `Rewrite.ScanBounded`) nor the resolved scan limit or sampling ratio bounds the scan, the scope line ends with a clause that only the environment's default cap applies, and `decorate` repeats the same string as the one scan suggestion. A bounded scoped query the cap still stopped gets one line after the run instead, worded to agree with the executor's "filter early with ==" hint, which lists the other levers. `wait query` says either once; `verify query` sends no query and has no clause.

## Discovery

### Record aggregates first

Discovery answers "which entities run this code" from records, not from the entity model. Its two primary queries aggregate the last 24 hours of spans and logs whose `k8s.workload.name` or `service.name` is one of the repository's names, one row per namespace, workload and service name, with each entity id field counted (`countDistinct`) and collected up to 50 (`collectDistinct(…, maxLength:50)`). Grouping by identity rather than by id matters on a large environment, where one workload can run as a couple of hundred service ids: grouped by id, that one workload filled the 200-row page by itself. The count is the true total and the list is what a binding can use; a candidate whose count exceeds its list is capped (`servicesCapped`), its per-kind verdict is tallied from the count, and no id binding is proposed for it. That proves the fields exist on the records the scope will later filter, fills in the namespace, and returns ids in exactly the form the renderer compares. The entity tables (`dt.entity.service`, `dt.entity.process_group`) are searched by name only when the records find nothing, and are reported with a note on environments that do not offer them.

Names are read per unit: the nearest directory at or above the working directory (or `--path`) that holds a build file (`go.mod`, `package.json`, `pom.xml`, `build.gradle(.kts)`, `settings.gradle(.kts)`, `pyproject.toml`, `*.csproj`) or a Dockerfile; the repository root otherwise. Kubernetes manifests and Helm charts contribute names to the unit named like them, or the nearest one above them, but do not make units. Every Maven module is its own unit. A Gradle build is not (`extract_gradle.go`): most of its modules are libraries of what it deploys, so a per-module unit attributed a deployable's chart to the deployable alone, left the libraries covered by nothing, and proposed `--path <module>` from inside each. A module is deployed when it holds a Dockerfile or a Helm chart or workload manifest holds or names it, unless it is named like a test. With at most one, the build is one unit at its settings file and no module's directory name is sent; with several, each deployable is a unit whose proposed entry carries `paths:` for its transitive module dependencies (`include` lists, `project(":x")`, `project(path: ":x")` and `projects.x` references outside test configurations, read by regexp with no DSL model), and a library several deployables share, like the settings directory, is discovered for all of them with no entry proposed (`Plan.SharedBy`). `projectDir` remaps are not read. Treating Maven builds the same way is left until field data asks for it.

The queries are fixed, at most ten, capped in their own text (`scanLimitGBytes:5`, `limit 200`), and bounded by a 60-second deadline. The filter helps the scan but cannot keep a day of a busy environment's spans or logs under 5 GB (inventory measured a scoped 15-minute log count at 341 GB unsampled), and a capped scan covers an unknown part of the window, so it cannot support a `match`. Sampling is the one lever that reduces the scan itself, so when the cap stops a record query its rows are set aside and the query is retried at `samplingRatio`: 1-in-100000 over 1h, 1-in-100000 over 24h, 1-in-1000 over 24h, coarsest first. Unlike inventory's ladder, which descends until the sample is thick enough to date a record, the walk stops at the first rung that finds a candidate, because discovery needs identities, not a precise count; the common case pays one near-free query. Before each rung the previous rung's scanned bytes are scaled by the step (×24, ×100) and a rung predicted to exceed the cap is skipped (`predicted_cap`); a rung Grail does not sample ends the walk (`sampling_refused`) and the capped rows stand. The arithmetic is inventory's (`EffectiveRatio`, `SampledMissBound`), and the floor every note states comes from the requested ratio, since Grail only ever samples more finely than asked. A result-limit notification on a page shorter than its `limit` is not read as a cut page: the only thing Grail can have limited there is a collected id array, which the count reports truthfully. The entity-name searches add the name's length as a field and sort on it, because DQL documents `sort` over fields; sorting on `stringLength(entity.name)` directly is not documented. There are no budget flags. Discovery is read-only, so like `inventory` it sets up a client without a safety check, and it runs in a `readonly` context. `dqlScopePrecheck` runs on the two record queries before the first is sent, so an OAuth token that provably cannot read spans or logs is refused up front rather than after half a report. The entity fallbacks are checked only when they are about to run, and a token that cannot read them gets an `auth` note instead: they are a fallback, and discovery answers without them.

### What leaves the machine

Only names derived from the repository and the user's `--term` values, in their match variants; namespaces are evidence, never sent. Generic words are dropped, and a `--term` is held to the characters the repository's names are made of.

Test content is not a source of names: a `kind: Deployment` under `test/fixtures/` or a `Dockerfile.test` describes what a test deploys, not what runs, and a candidate found under `test-platform-service-a` would link the repository to a test double. One word set in `tokens.go` (`testWords`) drives three rules, each extending it by name: `excludedDirs` adds `testdata`, `__tests__`, `e2e` and `examples`; `testFileWords` adds `e2e`, for files with such a word in their name (`spec` counts only after the first word, since a manifest may be called `spec.yaml`); and a name whose first word is one of `testWords` is held back after attribution, so the report lists the unit's held-back names and the drop is never silent. Holding back is a drop, not a demotion, because ranking has no weight to demote by; `--term` never goes through the rule, so it is the way to send such a name. A directory on the path to the one discovery was asked about is never excluded, so `examples/checkout` still discovers. `dev` is deliberately not excluded: it names an environment, and an overlay for it describes exactly what runs on the environment developers use most. The disclosure is the plan itself: `PlanDiscovery` needs no network, and `--dry-run` prints its exact output and returns before a client is built, so it works with no token and in any safety level.

### Never writes

`discover` has no prompt, no state store and no `--save`. It prints, per candidate, the `dtctl repo-scope set …` line that saves it, and the agent envelope carries the same line as `result.candidates[].setCommand` with the top one in `context.suggestions`, prefixed "confirm with the user". Running `set` is the confirmation. This keeps the one decision that changes what every later query returns with a person, and it means the command an agent proposes is the command a human can read, check and run.

Because the line is the whole hand-off, it has to save exactly the candidate shown, with nothing carried over but the line:

- **Environment.** `set` files under the current context's host, so the line carries `--context` when discovery ran under `--context` or `DTCTL_CONTEXT`, and `--config` when discovery was given one. `DTCTL_CONFIG` is not repeated: it applies to every command the shell runs.
- **Directory.** A unit below the root is a build unit, so the line carries `--path <unit>`. The root is also the unit of every directory without a build file, and there the directory alone does not say whether it is a service: in a monorepo with one build file at the top, `services/ledger` is the ledger, while in a single-module repository `internal/handlers` is one package of the service its `go.mod` names. Its name decides. When the name of the directory discovery ran for (`Plan.Dir`) is among what matched a candidate (`Candidate.MatchedDir`), the line carries `--path` for that directory, so the sibling service can be linked next; otherwise it covers the whole repository. The lines for linking by hand have no candidate to decide by, so they carry the unit's `--path` and say how to cover only the directory.
- **Bindings.** The line binds the workload and the service name; the bindings are alternatives, so each must select only the chosen candidate. A service name another candidate also reports, from another namespace or another workload such as a canary (`Candidate.ServiceNameShared`), would also select that one, so it is left out, with one line saying why. Entity ids are proposed only when neither remains, since both fields are deprecated; a candidate nothing selects alone gets no line. A match with no line falls back to the narrowing hint and the lines for linking by hand; an ambiguous report where no candidate has a line says nothing tells them apart and offers the shared service name, labelled as selecting all of them.

### Verdicts

Candidates are keyed by namespace, workload and service name, so the result does not depend on the order rows arrive in. They rank by tier (exact > segment > substring), then by how many distinct repository sources named them, then by records per hour, so spans sampled over an hour compare fairly with logs read over a day. A verdict is computed per kind of binding (service, process group, workload, service name) and overall: `match` is exactly one exact-tier candidate, `ambiguous` is several or a partial result, `none` is none. A failed, timed-out, cancelled or capped query marks the report partial, so `none` is never read as "nothing runs this code" when the answer was incomplete. A query whose page is as long as its own `limit 200` counts as capped (`row_limit`), as does one Grail reports as truncated. Per-kind verdicts count distinct ids, from the id count where a candidate has one, so two process groups on one candidate are `ambiguous` for that kind and so is a candidate listing one of its 200 service ids. A sampled rung that finds a candidate is complete at the floor its note states (about three times the ratio, at roughly 95% confidence) and may yield `match`; one that finds none stays partial, because a sampled miss is a bound, not an absence. A sampled candidate's set line binds the workload only: shared-service-name detection is unreliable below the floor, and its ids are only the sample's. Once any record query was sampled, no candidate's line carries `--service-name`, whether its own records were sampled or not: the candidate that would share the name may be the one below the floor.

## Decisions

1. **Default scoping applies in every mode, CI and agent mode included.** The file is committed and reviewed like code, it can only narrow a query, every scoped run prints the notice, and a scoped run that returns nothing prints the zero-rows hint pointing at `--no-repo-scope`. A CI job that wants the whole environment sets `DTCTL_NO_REPO_SCOPE=1` once. A notice-only mode for non-interactive runs would make the scope do nothing in exactly the runs nobody watches.
2. **No trust fingerprinting.** dtctl does not record which scope files a user has accepted. The file cannot widen or redirect a query, and the notice names the filter it applied; a trust store would be a second trust model next to "reviewed like code" for no additional protection.
3. **No ownership or permission checks on the repository root.** The walk-up requires `.git`; a world-writable checkout is a problem for every file in it, not for this one.
4. **No lock for concurrent `set`.** The write is a temp file, fsync and rename, so the last writer wins cleanly. The rename is atomic on POSIX systems; on Windows `os.Rename` replaces the file with `MoveFileEx`, which can fail while another process holds it open (the old file then stays in place and `set` reports the error) and is not documented as atomic. Concurrent edits of one committed file are git's problem.
5. **No set-time applicability probe.** `set` sends nothing. Whether the records carry the bound fields is what discovery checks before it proposes an id, and the zero-rows hint covers bindings written by hand.
6. **CLI only.** The working directory and the scope file are host state. `repo-scope` is in the engine's `unsupportedCommands`, and `currentWorkDir` refuses an invocation without `HostWorkingDirectory` or with a session, so a service request never resolves a scope even when the host process happens to run inside a checkout. `TestEngineOutputEqualsCLI` runs its CLI side in a temporary directory for the same reason.

## Testing

- `pkg/dql` and `pkg/reposcope`: the scanner and the insertion table, with both invariants on every row; schema, store, resolution, rendering, extraction over fixture repositories, and discovery over a fixture runner
- `pkg/output/testdata/golden/repo-scope/` and `cmd/testdata/golden/repo-scope/`: the structured printers and the terminal output of every subcommand
- Guards: `TestQueryRepoScope_UnlinkedIsByteIdentical` (outside a linked repository nothing changes; each disabling factor is paired with a positive control), `TestQueryRepoScope_StableFloor`, `TestRepoScopeCodesAreDocumented`, `TestRepoScopeDiscoverKeepsNoState` (by each file's mode and sha256), and `cmd/main_test.go`, which fails the run if the checkout's own scope file changed

## Follow-ups

1. Metrics: `timeseries` scoping on `dt.service.*` and `dt.process.*` through the `filter:` parameter. Until then metric queries run unscoped with a warning.
2. `fetch events` and `fetch bizevents`; `inventory arrivals --scope` defaulting to the repo scope.
3. Rendering an entry onto a filter segment, so the platform applies it.
4. Discovery narrowing by cluster, host or deploy time, and a configurable window.
5. A per-user overlay, export/import, staleness checks, and mapping profiling frames back to source.
6. Rebuilding `stripDQLLiterals` (`pkg/exec`) and `dqlCode` (`cmd/query_window.go`) on `dql.Scan`, so one scanner remains.
7. Promotion criteria for `repo-scope` and the query flags.
