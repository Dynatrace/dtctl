# dtctl Repo Scope

A repo scope links the git repository you are working in to the Dynatrace entities that run its code. It lives in `.dtctl-repo-scope.yaml` at the repository root, committed like code. Inside a linked repository, `dtctl query`, `wait query` and `verify query` narrow `fetch logs` and `fetch spans` to those entities by default.

## Check first

```bash
dtctl repo-scope current -A
```

- `result.linked: true` — queries from here are already scoped to `result.entry`; `result.filters` shows the exact filter per data object. Query without naming the service.
- `result.linked: false` — `result.reason` says why: not in a git repository, no scope file, no entry for this environment, or no entry covering this directory (`result.others` lists the entries; select one with `--repo-scope <name>`).
- `result.warning` — the file holds keys this dtctl does not know (a typo, or a newer dtctl's). The entries apply without them; tell the user.

## Link a repository

1. Discover. It only reads, and never writes the file:

   ```bash
   dtctl repo-scope discover -A
   ```

   `result.verdict` is `match`, `ambiguous` or `none`. Each `result.candidates[]` has the ids and names it found, `evidence` (which repository file named it, which record field matched, how many records), and a ready-to-run `setCommand`. `result.sent` lists every value that left the machine. `result.partial: true` means a query failed, timed out, was capped or was interrupted: `none` is then not evidence that nothing runs this code.

2. Show the user the candidate(s) and the evidence, and ask. Do not save on your own initiative: the scope changes what every later query in this repository returns, for everyone who clones it.

3. On the user's confirmation, run the candidate's `setCommand` exactly as given. It already carries the `--context` discovery ran with and the `--path` the entry should cover. A candidate without a `setCommand` has no binding that selects it alone; the suggestions then say what to do instead, and say when a shared service name was left out of the lines.

On `none`: retry with a name the user knows (`--term <name>`) or another directory of a monorepo (`--path services/<dir>`), or ask for the namespace and workload, or the OpenTelemetry `service.name`, and set it by hand:

```bash
dtctl repo-scope set <name> --namespace <ns> --workload <workload>
dtctl repo-scope set <name> --service-name <service.name>
```

To preview what discovery would send without sending it: `dtctl repo-scope discover --dry-run -A`.

Discovery works per unit: the nearest directory at or above the working directory (or `--path`) that holds a build file (`go.mod`, `package.json`, `pom.xml`, `pyproject.toml`, `*.csproj`) or a Dockerfile; the repository root otherwise.

## Query under a scope

```bash
dtctl query 'fetch logs | filter loglevel == "ERROR" | limit 20' -A
```

`context.repo_scope` says what happened: `applied: true` with the `filter` inserted after `fetch`, or `applied: false` with a `code` and `reason` (the query then ran exactly as typed). Your own `filter` on a scoped field (`service.name`, `k8s.workload.name`, ...) wins over the scope. With `--repo-scope <name>`, a scope that cannot narrow the query is a `validation_error` and nothing is sent. `wait query` and `verify query` are scoped too, but report it only on stderr.

- **0 records under a scope** — run the same query with `--no-repo-scope` before concluding the service is quiet; the bindings may not match what the records carry.
- **Environment-wide question** — add `--no-repo-scope`.
- **Monorepo, other service** — add `--repo-scope <name>`.

`dtctl exec dql` is never scoped, and neither is a query in a context whose `min-stability: stable` does not admit both `--repo-scope` and `--no-repo-scope`.

## Manage

```bash
dtctl repo-scope list -A               # entries for the current context's environment
dtctl repo-scope describe <name> -A    # bindings and the rendered filters
dtctl repo-scope delete <name>         # the file goes with its last entry
```

Entries are filed under the environment host; `--context` picks the environment for every repo-scope command.
