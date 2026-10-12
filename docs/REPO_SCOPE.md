# Repo Scope Guide

> **Experimental:** the `repo-scope` commands and `--repo-scope`/`--no-repo-scope` on `query`, `wait query` and
> `verify query` may change in any release.
> A context with `min-stability: stable` withholds them and runs every query as typed; exceptions for both flags
> (`'query --repo-scope'`, `'query --no-repo-scope'`) bring default scoping back. See [STABILITY.md](STABILITY.md#choosing-what-this-environment-accepts).

This guide explains how to link a git repository to the Dynatrace entities that run its code, so that the queries you make from inside it are about that code without naming a service every time.

## Overview

A **repo scope** is a data-only file at the repository root, `.dtctl-repo-scope.yaml`, committed like code. For each Dynatrace environment it names the services, process groups, service names and Kubernetes workloads that run the repository's code. Inside a linked repository:

- `dtctl query`, `dtctl wait query` and `dtctl verify query` narrow `fetch logs` and `fetch spans` to those entities by default, and say so on stderr
- `--no-repo-scope` runs one query exactly as typed; `DTCTL_NO_REPO_SCOPE=1` does it for a whole shell or CI job
- `--repo-scope <name>` picks one entry where several exist, as in a monorepo

The commands that manage the file:

- `dtctl repo-scope discover` finds the entities that run this repository's code and prints the line that saves them
- `dtctl repo-scope set <name>` saves an entry
- `dtctl repo-scope current` shows the entry that applies in the working directory, or why none does
- `dtctl repo-scope describe [name]` shows an entry's bindings and the DQL filter they render
- `dtctl repo-scope list` lists the entries for the current context's environment (also bare `dtctl repo-scope`)
- `dtctl repo-scope delete <name>` removes an entry

Nothing here writes to Dynatrace. `discover` only reads, and `set` and `delete` change only the local file.

## Prerequisites

1. Configure a Dynatrace context with `dtctl config set-context` (see [CONFIGURATION.md](CONFIGURATION.md))
2. For `discover`, a token that can read spans and logs (`storage:spans:read`, `storage:logs:read`). With `storage:entities:read` as well, it can also search entity names when the records find nothing
3. Run the commands from inside a git checkout. dtctl finds the repository by walking up from the working directory to the first directory holding `.git` (a directory, or the file a worktree or submodule carries) and never past it

## 1. Discover

```bash
dtctl repo-scope discover
```

`discover` reads names from the repository (Kubernetes manifests, Helm charts, Dockerfiles, build files and the origin remote) and compares them to `k8s.workload.name` and `service.name` in the last 24 hours of spans and logs:

```text
Sending to abc12345.apps.dynatrace.com: "checkout" (compared to k8s.workload.name, service.name, entity.name); nothing else leaves this machine
Unit: .

#  SERVICE                   PROCESS GROUP                   WORKLOAD           SERVICE NAME  TIER
1  SERVICE-0123456789ABCDEF  PROCESS_GROUP-FEDCBA9876543210  payments/checkout  checkout      exact
   k8s.workload.name "checkout" from deploy/k8s/deployment.yaml:4, deploy/k8s/deployment.yaml:11, go.mod:1
   service.name "checkout" from deploy/k8s/deployment.yaml:4, deploy/k8s/deployment.yaml:11, go.mod:1
   k8s.namespace.name "payments" from deploy/k8s/deployment.yaml:5
   12400 spans, 88000 logs in 24h

Verdict: match: process group, service, service name, workload
2 of 10 queries ran in 0.6s. Nothing was written. Save it with:
  dtctl repo-scope set checkout --namespace payments --workload checkout --service-name checkout
```

**`discover` never writes.** Each candidate comes with the `dtctl repo-scope set` line that saves it; running that line is the confirmation. When several candidates match, the verdict is `ambiguous` and each gets its own line. When none does, `discover` lists every query it tried and every value it sent, and says what to do next:

```bash
dtctl repo-scope discover --term billing-engine     # a name the repository does not spell out
dtctl repo-scope discover --path services/ledger    # another service of a monorepo
```

`--term` takes letters, digits, `-`, `_` and `.`, the characters the names read from the repository are made of.

The `set` line saves exactly the candidate shown:

- It binds the workload and the service name. Entity ids are proposed only for a candidate neither describes, because both id fields are deprecated (see [Save an entry](#2-save-an-entry)), and never for one whose ids discovery could not list in full: a workload that runs as 200 service ids shows two of them and `+198 more`, and gets no `--service` line.
- A candidate found by sampling (see [What discovery sends](#what-discovery-sends)) gets its `--namespace`/`--workload` binding only: a quieter candidate sharing its service name or ids could sit below the sample's floor. One with no workload gets no line, and a note says why. When spans or logs were sampled, a candidate read whole gets no `--service-name` either, since a candidate sharing the name could be the one below the other data object's floor.
- When more than one candidate reports the same service name (two namespaces, or a canary beside its main workload), their lines leave `--service-name` out and a note says so: the bindings of an entry are alternatives, and that name would select every such candidate's records. A candidate no other binding selects alone gets no line; when no candidate has one, discovery says nothing tells them apart and offers the `--service-name` line as selecting all of them.
- It carries `--context` when discovery ran in a context other than the config's current one (`--context` or `DTCTL_CONTEXT`), and `--config` when discovery was given one, so it saves under the environment discovery asked.

### Units

Discovery works per unit: the nearest directory at or above the working directory (or `--path`) that holds a build file (`go.mod`, `package.json`, `pom.xml`, `build.gradle(.kts)`, `settings.gradle(.kts)`, `pyproject.toml`, `*.csproj`) or a Dockerfile; the repository root otherwise. Kubernetes manifests and Helm charts do not make units: their names are attributed to the unit named like them, or the nearest one above them. Each Maven module is a unit of its own. A Gradle build is one unit, at its settings file, unless several of its modules are deployed (see [Monorepos](#monorepos)); its settings file contributes `rootProject.name`, a module's build file its `archivesName`, and `buildSrc` and `.gradle` are never read. When the unit is a root no Gradle build holds, the working directory's own name is sent too, because in a repository with one build at the top the directory you stand in is often what the service is called. The `set` line covers the unit when it is not the root. When it is the root, the line carries `--path` for the directory discovery ran for only if that directory's name is what matched the candidate: from `services/ledger` of a repository whose only `go.mod` is at the top, a workload named `ledger` gets `--path services/ledger`, so `services/checkout` beside it can be linked to its own service; from `internal/handlers` of a single-module repository, the service its `go.mod` names gets an entry for the whole repository. The lines for linking by hand, printed when nothing matched, carry no such `--path` and say how to add it.

### What discovery sends

Only names derived from the repository (and any `--term`) leave the machine, each in the spellings it is matched under: as written, lowercase, camelCase split into words, and joined with `-`, `_`, `.` and nothing (`BillingEngine` is sent as `BillingEngine`, `billing-engine`, `billing.engine`, `billing_engine` and `billingengine`). Generic words such as `app`, `api`, `server`, `service`, `web` and `main`, and anything shorter than three characters, are never sent. Namespaces are compared against what the records say, never sent.

Test content is not read, because the names in it describe what the tests deploy, not what runs. Discovery does not enter directories such as `test`, `testdata`, `fixtures`, `__tests__`, `e2e` or `examples`, in any case, and skips files with such a word in their name, words being split at `.`, `-` and `_` (`Dockerfile.test`, `deployment-e2e.yaml`; `spec` counts after the first word, so `spec.yaml` is read). A directory on the way to the one discovery runs for is always read, so `examples/checkout` still discovers from there, and `dev` is not test content. A name that starts with `test`, `mock` or `fixture` as a whole word (`test-platform-service-a`, `mockLedger`) is not sent, and the report says which names were held back; to look for one anyway, pass it with `--term`.

`--dry-run` prints every query and value exactly as a real run would send them, and sends nothing. It needs no token, so you can review the disclosure before configuring one:

```bash
dtctl repo-scope discover --dry-run
```

A run sends at most ten queries: two record queries, three sampled retries of each (only when the scan cap stops the record query, and only until one answers), and two entity searches (only when the records find nothing). The record queries always run:

```text
fetch spans, from:now()-24h, scanLimitGBytes:5
| filter in(k8s.workload.name, array(<values>)) or in(service.name, array(<values>))
| summarize records = count(),
    serviceCount = countDistinct(dt.entity.service), services = collectDistinct(dt.entity.service, maxLength:50),
    processGroupCount = countDistinct(dt.entity.process_group), processGroups = collectDistinct(dt.entity.process_group, maxLength:50),
    by:{k8s.namespace.name, k8s.workload.name, service.name}
| sort records desc
| limit 200
```

and the same over `logs`. Each row is one namespace, workload and service name. Its entity ids are counted exactly and listed up to 50, so a workload that runs as hundreds of service ids is one candidate, not a page of them.

On a busy environment a day of spans or logs scans more than the 5 GB cap even with the filter, and a capped scan covers an unknown part of the window. Discovery then retries the record query sampled, coarsest first, and stops at the first retry that finds a candidate:

| Retry | Window | Sampling | For | Floor |
|---|---|---|---|---|
| 1 | 1h | 1-in-100000 | the largest environments: an hour is what fits | about 300000 records in the hour |
| 2 | 24h | 1-in-100000 | large environments: the whole window | about 300000 records in the day |
| 3 | 24h | 1-in-1000 | medium environments: quieter services | about 3000 records in the day |

A retry whose scan the previous one predicts to exceed the cap is skipped rather than sent, and a retry the environment does not sample ends the walk with the capped answer. A sample can miss a quiet service but not invent one, so a sampled answer with candidates is complete at the floor it states (one note, one agent suggestion: `a service with fewer than about 300000 spans over 1h may be missing`), while a sampled answer with none is a bound, not an absence, and stays partial. A sampled candidate shows the hits the sample matched, never an estimate (`17 sampled span hits in 1h (1 in 100000 requested)`), and candidates rank by records or hits per hour.

Only when the record queries find nothing are entity names searched:

```text
fetch dt.entity.service, from:now()-24h
| filter matchesValue(entity.name, "*<value>*") or …
| fields id, entity.name
| fieldsAdd nameLength = stringLength(entity.name)
| sort nameLength asc
| limit 200
```

and the same over `dt.entity.process_group`. A run stops after 60 seconds and reports what it found. A query that returns 200 rows, or that Grail reports as cut short (other than a capped scan a sampled retry answered), caps the verdict at `ambiguous` and marks the result partial, as does a run that times out or is interrupted. A token that provably cannot read spans or logs is refused before the first query is sent; one that cannot read the entity tables gets a note in place of those two queries.

## 2. Save an entry

```bash
dtctl repo-scope set checkout --namespace payments --workload checkout --service-name checkout
```

```text
OK Repo scope "checkout" saved to .dtctl-repo-scope.yaml for abc12345.apps.dynatrace.com (commit the file to share it)
```

The entry is filed under the current context's environment host. `set` always replaces the whole entry, so pass every binding it should keep. Every value is validated before anything is written, so a refused `set` leaves the file exactly as it was. The new file is written beside the old one and renamed over it, which on Linux and macOS is atomic; on Windows the rename can fail while another program holds the file open, and leaves the old file in place when it does.

| Flag | Binds | Format |
|---|---|---|
| `--service` | `dt.entity.service` on spans | `SERVICE-` and 16 hex digits (repeatable) |
| `--process-group` | `dt.entity.process_group` on logs | `PROCESS_GROUP-` and 16 hex digits (repeatable) |
| `--service-name` | `service.name` on spans and logs | any printable text without `"` or `\` (repeatable) |
| `--namespace` with `--workload` | `k8s.namespace.name` and `k8s.workload.name` on spans and logs | Kubernetes names; `--workload` is repeatable |
| `--path` | the directory the entry covers | relative to the repository root; default the whole repository |

Workload and service-name bindings are the ones to prefer: they are names you already know, they need no entity ids, and they match OpenTelemetry and Kubernetes data whether or not OneAgent enriched it. They are also stable fields in the Dynatrace semantic dictionary, while both id fields are deprecated there (`dt.entity.service` in favour of `dt.smartscape.service`, `dt.entity.process_group` in favour of `dt.process_group.id`). Spans and logs carry the classic ids today, so `--service` and `--process-group` bind them; a later version of the file will move id bindings to their successors, and workload and service-name bindings are unaffected.

## 3. Query

From anywhere inside the repository:

```bash
dtctl query 'fetch logs | filter loglevel == "ERROR" | limit 20'
```

```text
applying repo scope checkout to fetch logs: service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout") (use --no-repo-scope to ignore it); the repo scope filters early with ==, but no scan limit is set for this query, so the environment's default cap applies: add scanLimitGBytes: or --default-scan-limit-gbytes to scan less
```

The text sent is the query with one stage inserted after `fetch`:

```text
fetch logs | filter (service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout"))
| filter loglevel == "ERROR" | limit 20
```

When a scoped query returns no records, a second line says so, `0 records under repo scope checkout; compare with --no-repo-scope`, because a binding the records do not carry looks exactly like a quiet service.

### What a scope costs

A scope narrows the rows a query returns, not what Grail reads. Its `==` filter right after `fetch` helps the scan, but on a busy environment a day of logs is far more than the cap, scoped or not. So when nothing bounds the scan (no `scanLimitGBytes:` or `samplingRatio:` in the `fetch`, and no positive `--default-scan-limit-gbytes` or `query-limits` in the context), the scope line says so before the query is sent, as above. A bounded scoped query that the cap still stops gets one line afterwards; `wait query` prints either line once:

```text
repo scope checkout already filters early with ==, and the scan still hit its cap; scan less another way the scan-limit hint lists
```

The scope is applied only where dtctl is sure of the result. The query runs exactly as typed, with a warning that says why, when:

- it does not start with `fetch logs` or `fetch spans`
- the entry binds nothing that data object is filtered by (an entry with only `--service` cannot filter logs)
- a `filter` or `filterOut` stage already compares a field the scope filters on: your own filter wins. Grouping or projecting by such a field (`summarize … by:`, `fields`) is not an override
- the data object is fetched again inside a subquery (`append`, `join`, `lookup`), which the inserted filter cannot reach
- dtctl cannot read the text safely: an unterminated string or comment, unbalanced brackets, or a single quote

With `--repo-scope <name>` each of these is an error instead, and nothing is sent: you asked for that entry's rows. The exception is your own filter on a scoped field, which still wins, so that query runs as typed with the warning.

`dtctl exec dql` is the raw passthrough and is never scoped.

### Check your scope

A scope can be valid and still miss the records you care about, so check it once against real data. For a breakpoint, `dtctl describe breakpoint <id>` lists the hostnames of the rooks (agents) it reached; compare them with the nodes the workload runs on. Under the scope, count what `fetch logs` and `fetch spans` reach, and before relying on scoped `fetch logs`, check that the discovery evidence showed logs as well as spans. Then rerun one query with `--no-repo-scope` and compare.

```bash
dtctl query 'fetch logs | summarize count(), by:{k8s.workload.name}'
dtctl query 'fetch logs | summarize count(), by:{k8s.workload.name}' --no-repo-scope
```

## Monorepos

Give each service its own entry with `--path`:

```bash
dtctl repo-scope set checkout --path services/checkout --namespace payments --workload checkout
dtctl repo-scope set ledger   --path services/ledger   --namespace payments --workload ledger
```

A query uses the entry whose path covers the working directory: the longest one when several do, matched by whole directory names (`services/checkout` covers `services/checkout/internal`, not `services/checkout-v2`). Without one, the entry with no path, if there is one. From a directory no entry covers, the query runs unscoped with a warning; `--repo-scope <name>` selects any entry from anywhere:

```bash
dtctl query 'fetch spans | limit 5' --repo-scope ledger
```

A Gradle build is discovered by what it deploys: a module that holds a Dockerfile, or that a Helm chart or workload manifest is in or named after, unless the module is named like a test (`smoke-tests/`). With one such module, or none, the build is one unit, and discovery from any module proposes one entry for the build (with `--path <settings directory>` when the build sits below the repository root). With several, each deployable is a unit, and its entry covers the modules it depends on through `include` and `project(":x")`, `project(path: ":x")` or `projects.x` references outside test configurations such as `testImplementation`, one `--path` each:

```bash
dtctl repo-scope set webproxy --namespace edge --workload webproxy \
  --path webproxy --path webproxy-api --path webproxy-core
```

Discovery from a library module proposes the entry of the one deployable that depends on it. A module several deployables depend on is in each of their entries, so a query from there runs unscoped until you pick one with `--repo-scope <name>`, and discovery from it, or from the settings directory, proposes no entry but a `discover --path` line per deployable. A `projectDir` remap is not read. Maven modules stay units of their own until field data shows a Maven build should be read the same way.

## Several environments

Entries are filed under the environment host, not the context name: context names are local to your machine, and the file is shared. `--context` decides which environment `set`, `delete`, `current`, `describe` and `list` work on:

```bash
dtctl --context staging repo-scope set checkout --service-name checkout
DTCTL_CONTEXT=staging dtctl query 'fetch spans | limit 5'
```

A query in a context whose environment has no entry runs unscoped and prints nothing.

## The file

```yaml
# .dtctl-repo-scope.yaml — at the repository root, committed like code.
apiVersion: v1                        # optional
environments:
  abc12345.apps.dynatrace.com:        # environment host, lowercase
    - name: checkout
      path: services/checkout         # optional; omitted = whole repository
      paths: [services/checkout, libs/cart]  # optional; more directories, path among them
      services: [SERVICE-0123456789ABCDEF]
      process-groups: [PROCESS_GROUP-FEDCBA9876543210]
      service-names: [checkout]
      workloads:
        - namespace: payments
          name: checkout
    - name: ledger
      path: services/ledger
      workloads:
        - namespace: payments
          name: ledger
  stg98765.apps.dynatrace.com:
    - name: checkout
      path: services/checkout
      service-names: [checkout]
```

The file holds bindings, never DQL. dtctl renders the filter itself, with every value checked against its format and quoted, so a cloned repository's file can narrow your queries but cannot widen them, redirect them, or inject text into them. A file that fails validation is reported (`repo-scope current` shows why) and queries run unscoped until it is fixed.

Within an entry the bindings are alternatives, OR-ed together; each data object is filtered on the fields it carries:

| Data object | `services` | `process-groups` | `service-names` | `workloads` |
|---|---|---|---|---|
| `spans` | `dt.entity.service` | — | `service.name` | `k8s.namespace.name` and `k8s.workload.name` |
| `logs` | — | `dt.entity.process_group` | `service.name` | `k8s.namespace.name` and `k8s.workload.name` |

`dtctl repo-scope describe <name>` prints the exact filter for each.

The file spells its keys in kebab-case (`process-groups`, `service-names`); the JSON and YAML that `-o json`, `-o yaml` and agent mode print for a result use camelCase (`processGroups`, `serviceNames`), except an entry in YAML output, which keeps the file's spelling.

`paths` ships with repo scopes in 0.43.0; an entry with `paths` sets `path` too, its primary directory, and lists it among them.

Hand edits are fine; `set` and `delete` rewrite the file in a canonical layout without your comments. A key this dtctl does not know, a typo or one a newer dtctl wrote, is reported by `current`, `describe` and every scoped query, and the entries apply without it; `set` and `delete` refuse to rewrite the file rather than drop it.

## Turning it off

```bash
dtctl query 'fetch logs | limit 20' --no-repo-scope   # one query
export DTCTL_NO_REPO_SCOPE=1                          # a shell or CI job
```

`DTCTL_NO_REPO_SCOPE` counts as set for any value but empty, `0`, `false`, `no` and `off`, and gives way to an entry named with `--repo-scope`.

Scoping applies in every mode (terminal, CI and agent) because the file is committed and reviewed like code, can only narrow a query, and every scoped run says so on stderr. A CI job that queries from a checkout and wants the whole environment sets `DTCTL_NO_REPO_SCOPE=1` once.

Server mode (`dtctl serve`, `pkg/engine`) has no working directory: the `repo-scope` commands are not available there and queries are never scoped.

## AI agents

In agent mode `discover` returns its report as the envelope `result`, with each candidate's `setCommand` and the top one in `context.suggestions`, offered for confirmation and never to run on the agent's own initiative. A scoped query's envelope carries `context.repo_scope`; see [AGENT_MODE.md](AGENT_MODE.md#repo-scope-contextrepo_scope). The `dtctl` skill has the procedure in [references/repo-scope.md](../skills/dtctl/references/repo-scope.md).

## Limits

- Only `fetch logs` and `fetch spans` are scoped. Metrics (`timeseries`), events and business events are not; their queries run unscoped with a warning.
- Discovery looks at the last 24 hours, or the last hour when it has to sample, and says so in its notes. A service that has been quiet longer, quieter than a sample's floor, or that runs under a name the repository does not spell out, needs `--term` or a `set` by hand.
- Entity ids per candidate are counted exactly and listed up to 50; `-o json` has the list and the count (`serviceCount`, `servicesCapped`).
- `inventory arrivals --scope` does not use the repo scope.
