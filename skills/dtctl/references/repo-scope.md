# dtctl Repo Scope

A repo scope links the git repository you are working in to the Dynatrace entities that run its code. It lives in `.dtctl-repo-scope.yaml` at the repository root, committed like code. Inside a linked repository, `dtctl query` narrows `fetch logs` and `fetch spans` to those entities by default. `wait query` and `verify query` do so only under `--repo-scope <name>`.

## Check first

```bash
dtctl repo-scope current -A
```

- `result.code: "linked"`: queries from here are already scoped to `result.entry`; `result.filters` shows the exact filter per data object. Query without naming the service.
- any other `result.code`: no entry applies, and the code says why: `not_a_repository`, `no_file`, `no_entry_for_environment`, `no_entry` (none covers this directory; `result.others` lists the entries, select one with `--repo-scope <name>`), `ambiguous_entry` (`result.tied` lists the entries that cover it equally; select one the same way), or `invalid_file` (the file does not load; `result.reason` names the line, the user corrects it, and discovering again is not the fix).
- `result.warning` — the file holds document keys this dtctl does not know (a typo, or a newer dtctl's). The entries apply without them; tell the user. An unknown key inside an entry is different: the file is not applied (`reason` names the key), because it could be a constraint.

## Link a repository

1. Discover. It only reads, and saves no repo scope:

   ```bash
   dtctl repo-scope discover -A
   ```

   `result` is a list of reports, one per unit: one in most repositories (`result[0]`), and what follows describes each. `verdict` is `match`, `ambiguous` or `none`. Each `candidates[]` has the ids and names it found, `evidence` (which repository file named it, which record field matched, how many records), and a ready-to-run `setCommand`. `sent` lists every value that left the machine; the same values are printed on stderr before the first query. `partial: true` means a query failed, timed out, was capped or was interrupted, or a sample matched nothing: `none` is then not evidence that nothing runs this code. On a busy environment discovery samples instead of scanning the whole day: a candidate with `sampled: true` was found that way, the verdict is then never `match`, and a `context.suggestions` entry states the floor below which a service may be missing; tell the user. `services` lists up to 50 service ids per row; `servicesCapped: true` means there are more, and `serviceCount` then estimates how many.

2. Show the user the candidate(s) and the evidence, and ask. Do not save on your own initiative: the scope changes what every later query in this repository returns, for everyone who clones it.

   A candidate is a workload first: `clusterUid` and `clusterName`, `namespace`, `workload`, then `serviceName`, then the entity ids. The same namespace and workload in two clusters are two candidates (`workloadShared: true`); ask which cluster the user means, by `clusterName`. `tier: "segment"` means the repository's name is only part of the workload's name (`acme-prod-checkout` for `checkout`): such a candidate is never a `match`, so confirm it is the user's deployment before saving.

   `indirect` (a reason) or `workloadUnnamed: true` marks a candidate nothing shows to run this code: only a name that is not the unit's own matched it (a sidecar's image, a manifest not named like the unit), or only its `service.name` did while the repository does not name its workload. It has no `setCommand` and is never a `match`. Ask the user; if it is theirs, run `dtctl repo-scope discover -A --term <workload or service name>` (the report's note gives the exact command) and read the new report. A candidate found by its entity name alone (no `workload` and no `serviceName`) exists, but no span or log in 24h carried a name of the repository: say so when you show it.

3. On the user's confirmation, run the candidate's `setCommand` exactly as given. A note of the report says when the line replaces an entry the file already holds, or would be refused because another entry covers its path: show it to the user first. `set` replaces the whole entry of that name: if `dtctl repo-scope describe <name> -A`, run with the `--context` (and `--config`) the `setCommand` carries, shows one already (`result.code: "linked"`), show the user its bindings and segments first, because the ones the line does not repeat are removed. It already carries the `--cluster-uid` of the cluster the candidate runs in (left out, with a suggestion saying so, when some of its records name no cluster: `withoutCluster`), the `--context` discovery ran with and the `--path` the entry should cover. A candidate without a `setCommand` has no binding that selects it alone (on a partial or sampled result only a workload binding is proposed, and ids only when they are the candidate's own); the suggestions then say what to do instead, and say when a shared service name was left out of the lines.

On `none`: retry with a name the user knows (`--term <name>`) or another directory of a monorepo (`--path services/<dir>`), or ask for the namespace and workload, or the OpenTelemetry `service.name`, and set it by hand:

```bash
dtctl repo-scope set <name> --namespace <ns> --workload <workload>
dtctl repo-scope set <name> --cluster-uid <k8s.cluster.uid> --namespace <ns> --workload <workload>   # one cluster only
dtctl repo-scope set <name> --service-name <service.name>
```

To preview what discovery would send without sending it: `dtctl repo-scope discover --dry-run -A`.

Discovery works per unit: the nearest directory at or above the working directory (or `--path`) that holds a build file (`go.mod`, `package.json`, `pom.xml`, `build.gradle(.kts)`, `settings.gradle(.kts)`, `pyproject.toml`, `*.csproj`) or a Dockerfile; the repository root otherwise. Each Maven module is its own unit. A Gradle build is one unit unless several of its modules are deployed; each deployable's entry then covers the modules it depends on, read from the build when a command runs, and from a module several deployables share, or the settings directory, discovery runs for each deployable and proposes each one's entry; `result` then holds one report per deployable (`context.total` is its length), and each suggestion is prefixed with its unit.

## Query under a scope

```bash
dtctl query 'fetch logs | filter loglevel == "ERROR" | limit 20' -A
```

`context.repo_scope` says what happened: `applied: true` with the `filter` inserted after `fetch`, or `applied: false` with a `code` and `reason`: the scope was not applied in full. The text was then sent exactly as typed unless `filter` is present, and it ran unscoped unless `segments` is. Your own `filter` on a scoped field (`service.name`, `k8s.workload.name`, `k8s.cluster.uid` when the entry binds a cluster, ...) wins over the scope. With `--repo-scope <name>`, a scope that cannot narrow the query is a `validation_error` and nothing is sent; the exceptions are your own filter on a scoped field (`user_filter`), which still runs as typed with `applied: false`, and `verify query` under an entry that holds only segments, which verifies the text as typed and says so. To run a query as typed from a named run, replace `--repo-scope <name>` with `--no-repo-scope`. `wait query` and `verify query` are scoped only under `--repo-scope <name>` (drop the flag to run as typed; they have no `--no-repo-scope`) and report it only on stderr; `verify query` does not send an entry's segments, and under a name its `--canonical` text holds the inserted filter.

- **Filter segments** — an entry may reference existing segments; `context.repo_scope.segments` lists the ones sent with the query, and they apply to any query, `timeseries` included. `applied: true` with no `filter` means the entry holds only segments. When an entry has bindings too and its filter could not be inserted, the segments are still sent but `applied` is false, with the refusal's `code` and a `reason` ending `the entry's filter segments were sent`: the rows are wider than the entry. `code: "segment_unavailable"` means a referenced segment was deleted or cannot be read and the query ran unscoped: tell the user, who fixes the entry (`dtctl get segments` lists ids). A segment read that fails for another reason (server error, rate limit) is an error and nothing is sent: retry it. A scoped `wait query` fails in both cases. A `--segment` you pass with the same id replaces the entry's variables, and the scope then reports `applied: false` with `code: "user_segment"`: `filter` and `segments` list what of the entry was still sent. Do not add a segment to an entry on your own initiative; like `set`, it changes every later query.
- **0 records under a scope** — run the same query with `--no-repo-scope` before concluding the service is quiet; the bindings may not match what the records carry. The same goes for a `summarize count()` of 0, and for a `wait query --repo-scope <name>` that ends with `condition not met under repo scope`: compare without the flag.
- **Unbounded scan under a scope** — the scope narrows rows, not the scan; when a suggestion says only the environment's default cap applies, add `scanLimitGBytes:` to the `fetch` or pass `--default-scan-limit-gbytes`.
- **Check the scope** — compare a few counts (`summarize count(), by:{k8s.workload.name}`) with and without `--no-repo-scope`, and check the discovery evidence showed logs before trusting scoped `fetch logs`: a note such as `candidate 1 has spans, but no logs carry its k8s.workload.name or service.name` means the workload binding reaches no logs, and a `setCommand` with `--process-group` is discovery's fix for it.
- **Environment-wide question** — add `--no-repo-scope`.
- **Monorepo, other service** — add `--repo-scope <name>`.

`dtctl exec dql` is never scoped, and neither is a query in a context whose `min-stability: stable` does not admit both `--repo-scope` and `--no-repo-scope`.

## Live Debugger

- `dtctl create breakpoint File.java:N` in a linked repository works in workspace `<repo>/<entry>` and sets no filters on its own: `--repo-scope <name>` sets every filter set of the named entry (on `update breakpoint` too), a workspace without filters fails with an error naming the covering entry, and `dtctl repo-scope describe <name> -A` → `result.liveDebuggerFilters` lists the sets. An entry's filter segments do not apply to breakpoints. A workload's cluster is not sent: a workspace set from an entry that binds one includes the same workload in other clusters. `service.name` is sent but not verified as a workspace label: a breakpoint under a set a workspace does not honour never hits. The commands say both on stderr and in `context.warnings`; tell the user, and check with `dtctl describe breakpoint <id> --repo-scope <name>`. When the scope file does not load, `create`, `update` and `delete breakpoint` refuse (their dry runs too) and the read commands warn that they read the directory's workspace: run `dtctl repo-scope current -A` and repair the file first. A failed command's `error.suggestions` carry lines to run as given; they name `--context`. Details: [LIVE_DEBUGGER.md](https://github.com/Dynatrace/dtctl/blob/main/docs/LIVE_DEBUGGER.md#filters-from-the-repo-scope).

## Manage

```bash
dtctl repo-scope list -A               # entries for the current context's environment
dtctl repo-scope describe <name> -A    # bindings and the rendered filters
dtctl repo-scope delete <name>         # the file goes with its last entry
```

Entries are filed under the environment host; `--context` picks the environment for every repo-scope command.
