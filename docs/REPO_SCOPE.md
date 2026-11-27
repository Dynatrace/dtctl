# Repo Scope Guide

> **Experimental:** the `repo-scope` commands, `--repo-scope`/`--no-repo-scope` on `query` and
> `verify query`, `--repo-scope` on `wait query`, and the breakpoint commands that read a scope may change in any release.
> A context with `min-stability: stable` withholds them and runs every query as typed; exceptions for both flags
> (`'query --repo-scope'`, `'query --no-repo-scope'`) bring default scoping back. See [STABILITY.md](STABILITY.md#choosing-what-this-environment-accepts).

This guide explains how to link a git repository to the Dynatrace entities that run its code, so that the queries you make from inside it are about that code without naming a service every time.

## Overview

A **repo scope** is a data-only file at the repository root, `.dtctl-repo-scope.yaml`, committed like code. For each Dynatrace environment it names the Kubernetes workloads, service names, process groups and services that run the repository's code. Inside a linked repository:

- `dtctl query` and `dtctl verify query` narrow `fetch logs` and `fetch spans` to those entities by default, and say so on stderr
- `--no-repo-scope` runs one query exactly as typed; `DTCTL_NO_REPO_SCOPE=1` does it for a whole shell or CI job
- `dtctl wait query` is scoped only when you name the entry (`--repo-scope <name>`): its answer is an exit code, read where nobody reads stderr. Without the name it runs as typed and says in one line that an entry covers the directory
- `--repo-scope <name>` picks one entry where several exist, as in a monorepo

The commands that manage the file:

- `dtctl repo-scope discover` finds the entities that run this repository's code and prints the line that saves them
- `dtctl repo-scope set <name>` saves an entry
- `dtctl repo-scope current` shows the entry that applies in the working directory, or why none does
- `dtctl repo-scope describe [name]` shows an entry's bindings and segments, the DQL filter the bindings render, and the Live Debugger workspace filters they set
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

#  CLUSTER  WORKLOAD           SERVICE NAME  PROCESS GROUP                   SERVICE                   TIER
1  prod-eu  payments/checkout  checkout      PROCESS_GROUP-FEDCBA9876543210  SERVICE-0123456789ABCDEF  exact
   k8s.cluster.uid "11111111-2222-3333-4444-555555555555" (k8s.cluster.name "prod-eu")
   k8s.namespace.name "payments" from deploy/k8s/deployment.yaml:5
   k8s.workload.name "checkout" from deploy/k8s/deployment.yaml:4, deploy/k8s/deployment.yaml:11, go.mod:1
   service.name "checkout" from deploy/k8s/deployment.yaml:4, deploy/k8s/deployment.yaml:11, go.mod:1
   12400 spans, 88000 logs in 24h

Verdict: match: workload, service name, process group, service
2 of 11 queries ran in 0.6s. No repo scope was saved. Save it with:
  dtctl --context prod repo-scope set checkout --cluster-uid 11111111-2222-3333-4444-555555555555 --namespace payments --workload checkout
```

A candidate is listed workload first: its cluster, namespace and workload, then its service name, then the entity ids, the same order its `set` line and every other `repo-scope` output use. The cluster column shows the cluster's name; the uid a binding uses is in the evidence and the line.

**`discover` saves no repo scope.** It does not write the scope file and changes nothing in the environment. (Like every command that queries, it is recorded in the local usage ledger where a context has usage tracking on, and a token it had to refresh is stored.) Each candidate that can be saved comes with the `dtctl repo-scope set` line that saves it; running that line is the confirmation. Where the scope file already holds an entry of the line's name for this environment, a note says that the line replaces it, path and segments included, and where another entry already covers the line's path, that `set` would refuse it. When several candidates match, the verdict is `ambiguous` and each gets its own line. When none does, `discover` lists every query it tried and every value it sent, and says what to do next:

```bash
dtctl repo-scope discover --term billing-engine     # a name the repository does not spell out
dtctl repo-scope discover --path services/ledger    # another service of a monorepo
```

`--term` takes what a name read from the repository is: letters, digits, `-`, `_` and `.`, at least three letters or digits, and not a generic word (`api`, `server`, `web`). Anything else is refused, never sent; a service that really is called `api` is linked by hand with `dtctl repo-scope set <name> --service-name api`.

The `set` line saves exactly the candidate shown:

- It binds the workload, with the cluster its records name (`--cluster-uid`), and nothing beside it: the bindings of an entry are alternatives, so a service name on the same line would select that name's records in every cluster and namespace. A candidate no workload binding describes gets its service name, when that is a name discovery sent: the queries look for a service name only among the names sent, so for any other name nothing checked what else reports it. Entity ids are proposed only for a candidate neither describes, or whose logs neither reaches (below), because both id fields are deprecated (see [Save an entry](#2-save-an-entry)), and never for one whose ids discovery could not list in full: a workload that runs as 200 service ids shows two of them and `+198 more`, and gets no `--service` line.
- A line of ids alone is held to more, and a note says which rule left a candidate without one. No other candidate may carry one of the ids. The ids must reach the records the candidate was found in: a scope filters spans by the service id and logs by the process group, so a candidate found in spans needs a service id. And the candidate must be named exactly or by a whole segment: an entity whose name only holds a sent name inside a longer word (`prepaymentgateway` for `pay`) is listed, never bound.
- A line never carries a value `set` would refuse. Records hold what their sender wrote, such as a namespace in capitals or an entity id of another kind; a binding a scope file cannot hold is left out, and a note names it and the reason.
- When the result is partial, a line binds the workload only, as for a sampled result (below): a query that did not answer could have found a second candidate with the same service name or ids. A candidate with no workload binding then gets no line, and a note says so.
- Logs need not carry `k8s.workload.name` or `service.name`: depending on how they are ingested they may carry only `dt.entity.process_group`. When a candidate's spans match and no logs do, discovery looks the logs up by the process groups the spans carried. If it finds them, the evidence says so (`5012 logs in 24h carry its process group, none its workload or service name`) and the line adds `--process-group`; without it the entry would return the candidate's spans and none of its logs. If it does not, or cannot ask because the process groups are not all listed or another workload carries one of them, a note says the candidate's logs were not found. When the logs query itself was capped or sampled, the logs have not been shown to be absent, so nothing is looked up and nothing is said.
- One namespace and workload name in two clusters is two candidates, each with its own line, and the verdict is `ambiguous`. A note says how to bind more than one of them, which no line does: run a line without `--cluster-uid` for the workload in every cluster, or list the workload once per `cluster-uid` in [the file](#the-file) for some of several. A candidate whose records name no cluster, while another runs the same namespace and workload in one, gets no line and a note: without `--cluster-uid` its binding would select both.
- Records of one workload do not always all name the cluster: its spans may carry `k8s.cluster.uid` and its logs not. When the workload runs in one cluster only, that is still one candidate; its evidence says which records carry no cluster (`its logs carry none`), and its line leaves `--cluster-uid` out, with a note, because an entry bound to the cluster would return the spans and none of the logs. The entry then selects the workload in any cluster. When another candidate runs the same namespace and workload, the line keeps `--cluster-uid` instead, since without it the entry would select both, and the note says which records it then leaves out.
- A repository holds names that are not its own, and a candidate that only such a name matched is shown and not proposed: the image of a sidecar or of a product deployed beside the service (`envoy`, `redis`), a manifest or chart that is named neither like the unit's build file, nor like its directory, nor like the repository (`deploy/redis.yaml` in the `checkout` repository), the origin remote's name by itself, and anything under a directory kept for other projects' code or for test content (`vendor`, `node_modules`, `testdata`, `examples` and the like), the directory's own name included. The verdict is then `ambiguous`, the candidate carries `indirect` with the reason and no `set` line, and a note names the way to confirm it: `dtctl repo-scope discover --term <name>`, since a term is your own word that the name is this code's.
- A candidate found by its `service.name` alone, in a workload no name of the repository names (`service.name` `checkout` reported by `infra/edge-gateway`), is shown and not proposed either. The queries return the records under the names they sent, so discovery read that workload's `checkout` records and nothing else it reports, and a line binding the workload would take all of it in. The verdict is `ambiguous`, the candidate carries `workloadUnnamed` and no `set` line, and a note names the workload to confirm with `--term`. A workload whose name holds the repository's (`acme-prod-checkout`) is named, and so are records that carry the service name and no workload.
- Two layouts of a single service stay `ambiguous` for the same reason, although only one thing runs the code, and `--term` with the workload's name settles both: a service whose workload is named for something else than the repository (`service.name` `checkout` in the workload `web`), and one whose only names come from a manifest or chart that is not named like its build file, its directory or the repository.
- A workload deployed under a longer name than the repository spells (`acme-prod-checkout` for `checkout`) is found when the repository's name is a whole run of the workload name's segments, the parts between `-` and `.`. Its tier is `segment`, it ranks below a workload named exactly, its evidence says which name it holds (`k8s.workload.name "acme-prod-checkout" contains workload "checkout" (deploy/k8s/deployment.yaml:4) (segment match)`), and on its own it is `ambiguous`, never a match: the repository does not say which prefix is its own. Only a name of at least five letters and digits is looked for this way; a shorter one, such as `cart`, is part of too many workload names and matches only whole.
- A candidate found by sampling (see [What discovery sends](#what-discovery-sends)) gets its workload binding only (`--cluster-uid`, `--namespace`, `--workload`): a quieter candidate sharing its service name or ids could sit below the sample's floor. One with no workload gets no line, and a note says why. When spans or logs were sampled, a candidate read whole gets no `--service-name`, `--process-group` or `--service` either, since a candidate sharing the name or an id could be the one below the other data object's floor.
- An id is proposed only while no other candidate could carry it: not when another lists it, and not when another lists only 50 of its ids, since the id could be among the rest. The lookup of logs by process group is not sent beside such a candidate either.
- A row whose workload and service name are both names discovery did not send (Grail returned it for a reason no file shows, an array-valued `service.name` for one) is listed with the tier `substring` and gets no line.
- When more than one candidate reports the same service name and one of them has no workload binding to stand in for it, the lines leave `--service-name` out and a note says so: the bindings of an entry are alternatives, and that name would select every such candidate's records. A candidate no other binding selects alone gets no line; when no candidate has one, discovery says nothing tells them apart and offers the `--service-name` line as selecting all of them.
- A run for a directory several Gradle deployables share proposes lines for each of them. `set` replaces an entry of the same name, so when two deployables' lines would save under one name, the later one is named after its unit (`webproxy-gateway` for `gateway` found for `webproxy`), and a note says so. Running both lines leaves both entries.
- It always carries the `--context` discovery ran in, however that was chosen (`--context`, `DTCTL_CONTEXT` or the config's current context), and `--config` when discovery was given one, so it saves under the environment discovery asked, also after `dtctl ctx` has moved on.

### Units

Discovery works per unit: the nearest directory at or above the working directory (or `--path`, taken in the spelling the repository holds the directory in, and refused when it is a symbolic link) that holds a build file (`go.mod`, `package.json`, `pom.xml`, `build.gradle(.kts)`, `settings.gradle(.kts)`, `pyproject.toml`, `*.csproj`) or a Dockerfile; the repository root otherwise. Kubernetes manifests and Helm charts do not make units: their names are attributed to the unit named like them, or the nearest one above them. Each Maven module is a unit of its own. A Gradle build is one unit, at its settings file, unless several of its modules are deployed (see [Monorepos](#monorepos)); its settings file contributes `rootProject.name`, a module's build file its `archivesName`, and `buildSrc` and `.gradle` are never read. When the unit is a root no Gradle build holds, the working directory's own name is sent too, because in a repository with one build at the top the directory you stand in is often what the service is called. The `set` line covers the unit when it is not the root. When it is the root, the line carries `--path` for the directory discovery ran for only if that directory's name is what matched the candidate: from `services/ledger` of a repository whose only `go.mod` is at the top, a workload named `ledger` gets `--path services/ledger`, so `services/checkout` beside it can be linked to its own service; from `internal/handlers` of a single-module repository, the service its `go.mod` names gets an entry for the whole repository. The lines for linking by hand, printed when nothing matched, carry no such `--path` and say how to add it.

### What discovery sends

Before the first query, `discover` prints every value it is about to send on stderr (the `Sending to` line above), in every output mode, agent mode included. Only names derived from the repository (and any `--term`) leave the machine, each in the spellings it is matched under: as written, lowercase, camelCase split into words, and joined with `-`, `_`, `.` and nothing (`BillingEngine` is sent as `BillingEngine`, `billing-engine`, `billing.engine`, `billing_engine` and `billingengine`). Generic words such as `app`, `api`, `server`, `service`, `web` and `main`, and anything shorter than three characters, are never sent. Nor is a directory named for its place in a layout (`src`, `internal`, `cmd`, `pkg`, `deploy`, `services`, `java` and the like), or what a Dockerfile merely starts the service with (`nginx`, `npm start`, `uvicorn`, `index.js`, `run.sh`). Of the origin remote only the repository's name is read, never its credentials, query string or fragment, and of a Dockerfile's command line never a flag, what follows the first flag (it may be the flag's value; the archive after `-jar` is the one exception), an assignment or a shell line. Namespaces are compared against what the records say, never sent. The one other thing a run may send is not the repository's: process group ids the environment itself returned for a candidate, in the lookup of that candidate's logs.

Test content is not read, because the names in it describe what the tests deploy, not what runs. Discovery does not enter directories such as `test`, `testdata`, `fixtures`, `__tests__`, `e2e` or `examples`, in any case, hidden or not, and skips files with such a word in their name, words being split at `.`, `-` and `_` (`Dockerfile.test`, `deployment-e2e.yaml`; `spec` counts after the first word, so `spec.yaml` is read). A directory on the way to the one discovery runs for is always read, so `examples/checkout` still discovers from there, and `dev` is not test content. What is read there finds a candidate without settling it: the report is `ambiguous` and proposes no line until a `--term` confirms the name. The same holds for the name of a directory under `vendor`, `node_modules` or build output, which discovery never reads. A name that starts with `test`, `mock` or `fixture` as a whole word (`test-platform-service-a`, `mockLedger`) is not sent, and the report says which names were held back; to look for one anyway, pass it with `--term`.

`--dry-run` prints every query and value exactly as a real run would send them, and sends nothing. It needs no token, so you can review the disclosure before configuring one:

```bash
dtctl repo-scope discover --dry-run
```

With `-o json`, `-o yaml` or `-o toon` the dry run prints the plans themselves, a list with one plan per unit, as a run prints its list of reports.

A plan holds eleven queries, and a run sends at most ten of them, since the last two kinds exclude each other: two record queries, three sampled retries of each (only when the scan cap stops the record query, and only until one answers), two entity searches (only when the records find nothing), and one lookup of logs by process group (only when a candidate's spans match and no logs do). The record queries always run:

```text
fetch spans, from:now()-24h, scanLimitGBytes:5
| filter in(k8s.workload.name, array(<values>)) or in(service.name, array(<values>)) or contains(k8s.workload.name, "<value>")
| summarize {records = count(), clusterName = takeAny(k8s.cluster.name),
    serviceCount = countDistinct(dt.entity.service), services = collectDistinct(dt.entity.service, maxLength:51),
    processGroupCount = countDistinct(dt.entity.process_group), processGroups = collectDistinct(dt.entity.process_group, maxLength:51)},
    by:{k8s.cluster.uid, k8s.namespace.name, k8s.workload.name, service.name}
| fieldsAdd workloadSegments = concat("-", replaceString(k8s.workload.name, ".", "-"), "-"),
    nameTier = if(in(k8s.workload.name, array(<values>)) or in(service.name, array(<values>)), 0, else:1)
| filter nameTier == 0 or contains(workloadSegments, "-<value>-")
| sort nameTier asc, records desc
| limit 200
| fieldsRemove workloadSegments, nameTier
```

and the same over `logs`. Each row is one cluster, namespace, workload and service name. Its entity ids are listed up to 50, so a workload that runs as hundreds of service ids is one candidate, not a page of them. The query collects one id more than that, which is how discovery knows a list was cut; the count beside it is DQL's estimate of how many ids such a list stands for.

The `contains` clause and the stages after the aggregation are how a workload named with a prefix is found in the same query: the first filter keeps records whose workload name holds a sent name, and the second keeps, of the aggregated rows, those where it is a whole run of segments. Rows named exactly sort first (`nameTier`), so when a page fills, the 200-row limit cuts the looser matches and not the workload the repository names. They are there only when a sent name has at least five letters and digits; without one the query sorts by `records` and has neither field. Where many workload names hold a sent name, their records are read as well, and the query can scan many times what the exact comparison alone would; it stays under the same 5 GB cap, and is retried sampled when it reaches it. What the search inside workload names costs where no workload holds the name has not been measured on an environment. A name whose words a workload joins with both `-` and `.` (`billing.engine-v2` for `billing-engine-v2`) is not found this way.

On a busy environment a day of spans or logs scans more than the 5 GB cap even with the filter, and a capped scan covers an unknown part of the window. Discovery then retries the record query sampled, coarsest first, and stops at the first retry that finds a candidate:

| Retry | Window | Sampling | For | Floor |
|---|---|---|---|---|
| 1 | 1h | 1-in-100000 | the largest environments: an hour is what fits | about 300000 records in the hour |
| 2 | 24h | 1-in-100000 | large environments: the whole window | about 300000 records in the day |
| 3 | 24h | 1-in-1000 | medium environments: quieter services | about 3000 records in the day |

A retry whose scan the previous one predicts to exceed the cap is skipped rather than sent, and a retry the environment does not sample ends the walk with the capped answer. A sample can miss a quiet service but not invent one, so a sampled answer with candidates is complete at the floor it states (one note, one agent suggestion: `a service with fewer than about 300000 spans over 1h may be missing`). What sits below that floor could be a second candidate, so a sampled answer is never a `match`: one candidate is `ambiguous`, with its line under "If it is what runs this code". A sampled answer with no candidate is a bound, not an absence, and is partial. The capped query's own rows are not counted, since a partial scan has no count to give, but a workload or service they name that the sample did not return is still listed (`cappedScan`, with the evidence `seen in the spans scan the 5 GB cap stopped, not in the sample that replaced it`), and the result is then partial. A sampled candidate shows the hits the sample matched, never an estimate (`17 sampled span hits in 1h (1 in 100000 requested)`), and candidates rank by records or hits per hour.

Only when the record queries find nothing are entity names searched:

```text
fetch dt.entity.service, from:now()-24h
| filter matchesValue(entity.name, "*<value>*") or …
| fields id, entity.name
| fieldsAdd nameLength = stringLength(entity.name)
| sort nameLength asc
| limit 200
```

and the same over `dt.entity.process_group`. A name found only there is never a `match`, however exactly it fits: an entity outlives its last record, so with no span or log in 24 hours nothing shows that it runs. One such candidate is `ambiguous`, with its line under "If it is what runs this code".

When the records leave a candidate with spans and no logs, and both record queries read the whole day, its logs are looked up by the process groups its spans carried, with the filter a `--process-group` binding renders. The dry run shows this query with a placeholder, since the ids are the environment's answer:

```text
fetch logs, from:now()-24h, scanLimitGBytes:5
| filter in(dt.entity.process_group, array(<ids>))
| summarize records = count(), by:{dt.entity.process_group}
| limit 200
```

It is sent for at most 50 ids, and only ids no candidate of another workload carries. Failing, it leaves a note and the verdict as it was.

The discovery of one unit stops after 60 seconds and reports what it found; a run for a directory several Gradle deployables share has that long for each of them. An interrupted run (Ctrl-C) prints what it found so far, every query it did not send marked as skipped. In agent mode it returns the error envelope instead, with no report: an envelope is one or the other. A query that returns 200 rows, or that Grail reports as cut short (other than a capped scan a sampled retry answered), caps the verdict at `ambiguous` and marks the result partial, as does a run that times out or is interrupted. A token that provably cannot read spans or logs is refused before the first query is sent; one that cannot read the entity tables gets a note in place of those two queries.

## 2. Save an entry

```bash
dtctl repo-scope set checkout --namespace payments --workload checkout --service-name checkout
```

```text
OK Repo scope "checkout" saved to .dtctl-repo-scope.yaml for abc12345.apps.dynatrace.com (commit the file to share it)
```

The entry is filed under the current context's environment host. `set` always replaces the whole entry, so pass every binding it should keep. Every value is validated before anything is written, so a refused `set` leaves the file exactly as it was. A workload or a segment given twice is refused rather than written twice. The new file is written beside the old one and renamed over it, which on Linux and macOS is atomic; on Windows the rename can fail while another program holds the file open, and leaves the old file in place when it does.

| Flag | Binds | Format |
|---|---|---|
| `--namespace` with `--workload` | `k8s.namespace.name` and `k8s.workload.name` on spans and logs, where the records carry them | Kubernetes names; `--workload` is repeatable |
| `--cluster-uid` | `k8s.cluster.uid` of the `--workload` names; without it, the workload in any cluster | a lowercase UUID, as `discover` prints it; needs `--namespace` and `--workload` |
| `--service-name` | `service.name` on spans and logs | any printable text without `"` or `\` (repeatable) |
| `--process-group` | `dt.entity.process_group` on logs | `PROCESS_GROUP-` and 16 hex digits (repeatable) |
| `--service` | `dt.entity.service` on spans | `SERVICE-` and 16 hex digits (repeatable) |
| `--segment` | an existing filter segment a query under the entry also sends | the segment's id, with variables inline: `"ID?var=val&var2=val1,val2"` (repeatable, at most 10) |
| `--path` | the directory the entry covers | a directory of the repository, relative to its root, not a symbolic link; saved in the spelling the repository holds it in; default the whole repository |

A cluster is bound by its uid, never its name: the uid is what identifies a cluster, while a name is chosen at deployment and can be changed or given to another cluster. `discover` shows the name beside the uid so you can tell which cluster a line is for. `--cluster-uid` applies to every `--workload` of the command and to no other flag: a `--service-name`, `--process-group` or `--service` beside it still selects records of every cluster (see [the file](#the-file)). `--namespace` and `--cluster-uid` are each given once, for every `--workload` of the command; a second value is refused, because the last one would silently rebind the workloads named before it. An entry whose workloads run in different namespaces or clusters is written by hand in [the file](#the-file).

Workload and service-name bindings are the ones to prefer: they are names you already know and they need no entity ids. They select only records that carry those fields, though, and logs do not always: an environment may record a workload's spans with `k8s.workload.name` and its logs with `dt.entity.process_group` alone, and a workload binding then returns no logs. `discover` checks this and proposes `--process-group` beside the workload when the logs need it; for an entry written by hand, [check your scope](#check-your-scope). They are also stable fields in the Dynatrace semantic dictionary, while both id fields are deprecated there (`dt.entity.service` in favour of `dt.smartscape.service`, `dt.entity.process_group` in favour of `dt.process_group.id`). Spans and logs carry the classic ids today, so `--service` and `--process-group` bind them; a later version of the file will move id bindings to their successors, and workload and service-name bindings are unaffected.

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

When a scoped query returns no records, a second line says so, `0 records under repo scope checkout; compare with --no-repo-scope`, because a binding the records do not carry looks exactly like a quiet service. A query that ends in an ungrouped `summarize` returns one row even for nothing, so the line is also printed when that row's `count()` is 0; no other aggregate is read that way, since a zero `countIf` or `sum` says nothing about how many records the scope let through. `wait query --repo-scope checkout` polls, so it does not say this per attempt. It says once, when the wait ends, `condition not met under repo scope checkout; compare without --repo-scope checkout`, or the `0 records` line when a condition such as `--for none` was satisfied by nothing.

### What a scope costs

A scope narrows the rows a query returns, not what Grail reads. Its `==` filter right after `fetch` helps the scan, but on a busy environment a day of logs is far more than the cap, scoped or not. So when nothing bounds the scan (no `scanLimitGBytes:` or `samplingRatio:` in the `fetch`, and no positive `--default-scan-limit-gbytes` or `query-limits` in the context), the scope line says so before the query is sent, as above. With a scan limit of `-1`, in the `fetch` or as the flag, it says instead that no scan limit applies at all. That is advice. A scoped query that the cap then stops gets one line afterwards, whatever was said before; `wait query` prints either line once:

```text
repo scope checkout already filters early with ==, and the scan still hit its cap; scan less another way the scan-limit hint lists
```

The scope is applied only where dtctl is sure of the result. The query runs exactly as typed, with a warning that says why, when:

- it does not start with `fetch logs` or `fetch spans`
- the entry binds nothing that data object is filtered by (an entry with only `--service` cannot filter logs)
- a `filter` or `filterOut` stage already compares a field the scope filters on: your own filter wins. Grouping or projecting by such a field (`summarize … by:`, `fields`) is not an override
- the data object is fetched again inside a subquery (`append`, `join`, `lookup`), which the inserted filter cannot reach
- dtctl cannot read the text safely: an unterminated string or comment, unbalanced brackets, a single quote, or a `#` outside a string or comment

With `--repo-scope <name>` each of these is an error instead, and nothing is sent: you asked for that entry's rows. To run the query as typed from there, replace `--repo-scope <name>` with `--no-repo-scope`, which is what the error advises: the two flags exclude each other, and dropping the name alone applies the entry that covers the directory. The exception is your own filter on a scoped field, which still wins under a name too: that query succeeds, runs as typed with your filter and no inserted one, and the warning says so.

`dtctl exec dql` is the raw passthrough and is never scoped.

### Filter segments

An entry can also name filter segments that already exist in the environment. A query under the entry then sends them as if you had typed `--segment`, with the variable values the entry gives:

```bash
dtctl get segments                                    # find the id
dtctl repo-scope set checkout --namespace payments --workload checkout \
  --segment "Ab3dEf9hIjk?environment=production"
```

```text
applying repo scope checkout to fetch logs: (k8s.namespace.name == "payments" and k8s.workload.name == "checkout") with segments Ab3dEf9hIjk?environment=production (use --no-repo-scope to ignore it)
```

- **By id.** A segment is referenced by its id, never its name: a committed file must not change meaning when a segment is renamed. dtctl only reads segments; it never creates or changes one.
- **Both must hold.** The bindings are alternatives among themselves; the filter they render and every segment narrow the query together. What neither reaches is under [Limits](#limits).
- **Segments alone.** An entry may hold only segments (`dtctl repo-scope set platform --segment Ab3dEf9hIjk`). It inserts no filter and applies to every query, `timeseries` and events included, which bindings do not reach.
- **Where the filter cannot go.** When the filter cannot be inserted for one of the reasons above, the entry's segments are still sent, and a warning says which half applied. Such a query is narrower than the one you typed and wider than the entry, so in agent mode it is reported with `applied: false` and the reason the filter was refused, with the segments that were sent listed.
- **With your own.** `--segment` and `--segments-file` are sent as well. A segment you name that the entry also lists is sent once, with your variables, and the scope line then lists only the segments the entry added. To give an entry's segment other variables for one query, name it: `--segment "Ab3dEf9hIjk?environment=staging"` (`--segment-var` works only on a segment you gave with `--segment`). A query takes ten segments; more than that, yours and the entry's together, is an error.
- **A segment that is gone.** Before sending, dtctl reads each segment the entry references. If one was deleted, is not shared with you, or your token cannot read segments (`storage:filter-segments:read`), the query runs exactly as typed, without the filter either, and a warning names the segment. With `--repo-scope <name>` it is an error, which covers every scoped `wait query`, and `query --live` fails as well rather than refresh over the whole environment. A read that fails for any other reason (a server error, a rate limit, a timeout) is always an error and nothing is sent: it says nothing about the segment, so retry, or pass `--no-repo-scope`.
- **Variables.** The entry has to give a value for every variable the segment requires. dtctl does not check that before sending: the platform fails the query, and the error names the segment, the variable and the entry, and says to give the variable a value in `.dtctl-repo-scope.yaml` or pass `--no-repo-scope`. A segment can also need a variable that only some data can supply. It then fails other queries run in the repository, an entity query for example, with an error that names the entry in the same way; remove that segment from the entry, or give it an entry of its own under a narrower `path`. `wait query` ends on either error at the first attempt, as on any query the platform refuses for good, and prints it with the text that was sent.
- **Commands.** `query` sends the segments, and so does `wait query` under `--repo-scope <name>`. `verify query` checks the query text only: it neither reads nor sends them, and says so, so a segment that cannot be read shows on `query`, not there. `repo-scope describe` lists them and says that they are sent where no filter is inserted. Breakpoints are not affected: see [Live Debugger](#4-live-debugger).

### Check your scope

A scope can be valid and still miss the records you care about, so check it once against real data. For a breakpoint, `dtctl describe breakpoint <id>` lists the hostnames of the rooks (agents) it reached; compare them with the nodes the workload runs on. Under the scope, count what `fetch logs` and `fetch spans` reach, and before relying on scoped `fetch logs`, check that the discovery evidence showed logs as well as spans. Then rerun one query with `--no-repo-scope` and compare.

```bash
dtctl query 'fetch logs | summarize count(), by:{k8s.workload.name}'
dtctl query 'fetch logs | summarize count(), by:{k8s.workload.name}' --no-repo-scope
```

## 4. Live Debugger

An entry's bindings are also the labels a Live Debugger workspace is filtered by: `--repo-scope <name>` on `create breakpoint` and `update breakpoint` sets every filter set of the named entry, in workspace `<repo>/<name>`. Nothing is set from an entry you did not name: `dtctl create breakpoint` only says that an entry covers the working directory and how to apply it. [Filters from the repo scope](LIVE_DEBUGGER.md#filters-from-the-repo-scope) says what is sent and what is verified.

Filter segments do not apply to breakpoints. A workspace is filtered by labels, and has no place to take a segment, so the segments an entry references narrow its queries only, and an entry that holds nothing else sets no workspace filters.

## Reading the answer

`current` and `describe` print a view for a terminal (`-o table`, the default, and `-o wide`) or the value itself (`-o json`, `-o yaml`, `-o toon`, and agent mode); any other format is refused, because the answer is one entry and not rows. `list` prints its rows in `table`, `wide` and `csv`, and the entries themselves in the other formats. As everywhere in dtctl, `--plain` leaves `current` and `describe` as text and turns the table of `list` into JSON.

The value says what kind of answer it is in `code`, so a program need not read `reason`:

| `code` | Meaning |
|---|---|
| `linked` | `entry` is the entry asked for. `selected` says how it was chosen: `directory` (it covers the working directory), `build` (it covers it through the Gradle build) or `name` (it was named, and need not cover this directory) |
| `no_entry` | the environment has entries and none covers the directory; `others` lists them |
| `ambiguous_entry` | the entries in `tied` cover the directory as closely, so none applies |
| `no_entry_for_environment` | the file has no entry for this context's environment; `hosts` lists the ones it has |
| `no_file` | the repository has no scope file |
| `invalid_file` | the file is there and does not load: it fails validation or cannot be read |
| `not_a_repository` | the working directory is in no git repository |

`current` exits 0 with every one of these. A command that names an entry (`describe <name>`, `delete <name>`, `--repo-scope <name>`) exits 4 when the environment does not define it, and a value or a file that is refused exits 2.

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

A Gradle build is discovered by what it deploys: a module that holds a Dockerfile, or that a Helm chart or workload manifest is in or named after, unless the module is named like a test (`smoke-tests/`). With one such module, or none, the build is one unit, and discovery from any module proposes one entry for the build (with `--path <settings directory>` when the build sits below the repository root). With several, each deployable is a unit, and its entry's `path` is the deployable's module:

```bash
dtctl repo-scope set webproxy --namespace edge --workload webproxy --path webproxy
```

A library module resolves to the deployable whose closure of `project(":x")`, `project(path: ":x")` or `projects.x` references (outside test configurations such as `testImplementation`) holds it, read from the build files when the command runs, so the entry follows the build as it changes; `repo-scope current` says so. Discovery from a library module proposes the entry of the one deployable that depends on it. A module the deployables of several entries depend on resolves to none of them, so a query from there runs unscoped until you pick one with `--repo-scope <name>`, and discovery from it, or from the settings directory, runs for each deployable: each is a run of its own, with its own queries, scans and 60-second bound (in agent mode `result` is then a list and `context.total` its length). A `projectDir` remap is not read, so run from such a module with `--repo-scope <name>`. Maven modules stay units of their own until field data shows a Maven build should be read the same way.

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
apiVersion: v1                        # written by set; a file without it is read as v1
environments:
  abc12345.apps.dynatrace.com:        # environment host, lowercase
    - name: checkout
      path: services/checkout         # optional; omitted = whole repository
      workloads:
        - cluster-uid: 11111111-2222-3333-4444-555555555555   # optional; omitted = any cluster
          namespace: payments
          name: checkout
      service-names: [checkout]
      process-groups: [PROCESS_GROUP-FEDCBA9876543210]
      services: [SERVICE-0123456789ABCDEF]
      segments:                       # optional; existing filter segments, by id
        - id: Ab3dEf9hIjk
          variables:                  # optional; name: [values]
            environment: [production]
      annotations:                    # optional; notes dtctl keeps and never reads
        owner: team-payments
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

Within an entry the bindings are alternatives, OR-ed together; each data object is filtered on these fields, and a record that carries none of the bound ones is not selected:

| Data object | `workloads` | `service-names` | `process-groups` | `services` |
|---|---|---|---|---|
| `spans` | `k8s.namespace.name` and `k8s.workload.name`, with `k8s.cluster.uid` when bound | `service.name` | — | `dt.entity.service` |
| `logs` | `k8s.namespace.name` and `k8s.workload.name`, with `k8s.cluster.uid` when bound | `service.name` | `dt.entity.process_group` | — |

A workload with a `cluster-uid` renders `(k8s.cluster.uid == "…" and k8s.namespace.name == "…" and k8s.workload.name == "…")`, so it selects only records that carry all three fields. `k8s.cluster.uid` then counts as a field the scope filters on: a `filter` of your own on it wins, as one on the namespace or the workload does.

The cluster narrows the workload it is written on, and nothing else. A service name, a process group or a service in the same entry is an alternative to that workload, so it selects its records in every cluster: an entry with `cluster-uid` on its workload and `service-names: [checkout]` returns the `checkout` service's records from all clusters. For an entry that keeps to one cluster, bind the workload alone. `repo-scope current` and `describe` say so for an entry that mixes the two (`Cluster: narrows the workloads only: service names select records of every cluster`).

`dtctl repo-scope describe <name>` prints the exact filter for each.

`segments` are not alternatives: each one, and the filter, must hold (see [Filter segments](#filter-segments)). Ids and variable values are checked like every other value, and travel beside the query, never in its text. A variable value cannot hold `,`, `&` or `=`: dtctl prints a segment the way `--segment` takes it, and those separate its values there.

`annotations` are notes on an entry, for people and for other tools: a map of text to text. dtctl checks their form (at most 64 per entry; a key of lowercase letters, digits and `.`, `_`, `-`, `/` inside, at most 253 characters; a value of one line, at most 1024 bytes), shows them in `describe`, keeps them when `set` rewrites the entry, and never reads them. No annotation changes what an entry covers or selects, in this dtctl or a later one, so it is the one place in an entry where a key this dtctl does not know may be written.

The file spells its keys in kebab-case (`process-groups`, `service-names`, `cluster-uid`). A result is not the file: what `-o json`, `-o yaml`, `-o toon` and agent mode print uses camelCase (`processGroups`, `serviceNames`, `clusterUid`) in every format, for an entry as for everything around it, so `-o yaml` output is not something to paste into the file.

What a file may hold is bounded, and a file outside the bounds is not applied, like one with a value that fails its format:

- **Plain YAML, one document.** Anchors, aliases and merge keys (`&a`, `*a`, `<<`) are refused, as is a second `---` document, a key given twice, and a key without a value (`environments:` or `service-names:` with nothing after it): each can make a line mean something other than what it shows.
- **No empty constraint.** `path: ""`, `cluster-uid: ""`, `segments: []` and a segment's `variables: {}` are refused like a key without a value. Each of those keys narrows the entry, and left out it means the widest thing it can (the whole repository, every cluster, no segment), so an empty value is read as a line somebody has not finished, never as "all". Leave the key out to say that; `set` does.
- **`apiVersion`** is `v1` or absent. Any other version is refused by name with the advice to upgrade dtctl, before anything else in the file is read.
- **Size.** At most 1 MiB, 64 environments, 1000 entries per environment and 1000 items per list. `set` refuses a change that would take the file past them.
- **Hosts** are lowercase, without a trailing dot, at most 253 characters. The host is the key because a context name means nothing on another machine, and an environment has one host; a context whose URL ends its host with a dot uses the entries of the host without it.
- **Paths** are relative to the repository root with forward slashes, clean (no `.`, `..` or trailing slash), printable, without a space at either end, and in composed Unicode (NFC), the spelling `set` writes. Two entries of one environment may not have paths that differ only in case or composition.
- **Each value once.** A service name, an id, a workload or a segment value listed twice in one entry is refused, and so is a workload listed with a cluster and without one: without one it already selects every cluster.
- **`variables`** of a segment is a map from name to values, so a variable cannot be given twice. That differs from `--segments-file`, which takes the list the platform's API takes.

`apiVersion: v1` is defined by its first release. A file written by a development build from before that release may hold something the release refuses; correct it by hand, or delete the entry and `set` it again.

Hand edits are fine; `set` and `delete` rewrite the file in a canonical layout without your comments. A key this dtctl does not know is a typo, or one a newer dtctl wrote, and where it sits decides what happens. Beside `apiVersion` and `environments` it is reported by `current`, `describe` and every scoped query, and the entries apply without it; `set` and `delete` refuse to rewrite the file rather than drop it. Inside an entry, on the entry or on one of its workloads or segments, the file is not applied at all, as with a value that fails its format: such a key may be a constraint (`cluster_uid` for `cluster-uid`), and an entry applied without its constraint would select more than the file says while reporting that it was applied. Queries then run unscoped with a warning naming the key, until it is corrected or dtctl is upgraded.

## Turning it off

```bash
dtctl query 'fetch logs | limit 20' --no-repo-scope   # one query
export DTCTL_NO_REPO_SCOPE=1                          # a shell or CI job
```

`DTCTL_NO_REPO_SCOPE` counts as set for any value but empty, `0`, `false`, `no` and `off`, and gives way to an entry named with `--repo-scope`.

Scoping applies in every mode (terminal, CI and agent) because the file is committed and reviewed like code, can only narrow a query, and every scoped run says so on stderr. A CI job that queries from a checkout and wants the whole environment sets `DTCTL_NO_REPO_SCOPE=1` once.

Server mode (`dtctl serve`, `pkg/engine`) has no working directory: the `repo-scope` commands are not available there and queries are never scoped.

## With dtui

[dtui](https://github.com/dynatrace-oss/dtui) has a project file of its own, `.dtui.yaml`, which among other things lists the filter segments it applies to every view. The two files are separate and do different jobs:

- `.dtctl-repo-scope.yaml` binds directories of a repository to the entities that run them. dtctl writes it, and it never selects an environment. dtui does not read it yet; when it does, it is meant to go through dtctl's own package rather than a second parser, so both tools apply the same file the same way.
- `.dtui.yaml` sets dtui's environment, start view, timeframe, presets and segments. dtctl does not read it.

To query with the segments your `.dtui.yaml` names, pass them yourself; they combine with the repo scope, and a record has to match both:

```bash
dtctl query 'fetch logs | limit 20' --segment payments-prod
```

Segments the whole repository should query under belong in the scope file instead, as an entry's `segments` (see [Filter segments](#filter-segments)): there they are validated, can differ per directory, and reach everyone who clones the repository.

## AI agents

In agent mode `discover` returns its reports as the envelope `result`: a list with one report per unit, also when there is one, each with its candidates' `setCommand`. For a `match` whose candidate has a line, that line is also the first of `context.suggestions`, offered for confirmation and never to run on the agent's own initiative; a match without one (a directory a scope file cannot hold, for one) gets the templates for linking by hand instead. With `-o json`, `-o yaml` or `-o toon` the same list is printed without the envelope. Its `notes` then carry what the table prints under the candidates: what a `setCommand` leaves out, why a candidate has none, and the line that selects several candidates when nothing tells them apart. `-o wide` is the table, and every other format (`csv`, `jsonl`, `chart` and the rest) is refused before anything is sent: a report is not rows. An interrupted run is an error, and in agent and plain mode the error envelope is all stdout holds; the reports of the units that had run are not printed in front of it. A scoped query's envelope carries `context.repo_scope`; see [AGENT_MODE.md](AGENT_MODE.md#repo-scope-contextrepo_scope). The `dtctl` skill has the procedure in [references/repo-scope.md](../skills/dtctl/references/repo-scope.md).

## Limits

- Discovery's queries were written against the DQL documentation and are tested against recorded answers, not against an environment. Not verified on one: that the query texts run as written (among them `collectDistinct` without `expand` on a field that is itself an array, and `summarize` by such a field in the lookup of logs by process group), which fields spans and logs carry on a given deployment, which notification Grail sends for a cut result or a clipped array, and what the queries scan.
- A query that read the whole window read the records the token can see. Records in a bucket the token has no permission for are not in the answer, and nothing says so: a `match` is a match among what was readable.
- The `set` line is quoted for a POSIX shell (`sh`, `bash`, `zsh`). On Windows a candidate whose line would need quoting, because of a directory, context or config path with a space or a character `cmd.exe` or PowerShell reads specially, gets no line, and a note gives the values to type; this was decided by reading and has not been run on Windows.

- Only `fetch logs` and `fetch spans` are filtered by bindings. Metrics (`timeseries`), events and business events are not; their queries run unscoped with a warning, unless the entry references filter segments, which apply to them too.
- Discovery looks at the last 24 hours, or the last hour when it has to sample, and says so in its notes. A service that has been quiet longer, quieter than a sample's floor, or that runs under a name the repository does not spell out, needs `--term` or a `set` by hand. So does a workload that only holds a name shorter than five letters and digits (`shop-cart` for `cart`), and a `service.name` that is longer than the repository's name: service names are compared whole.
- Discovery reads at most 2000 build files, Dockerfiles and manifests of a repository, each up to 1 MiB, and does not go more than six directories deep, counted from the repository root and, below the directory it runs for, from that directory; the directories on the way to it are read however deep. In a larger repository the unit is still found from the directory's own build file, read in place of the last files the walk reached, but names in the files left unread are not sent: a note says so, the result is partial, and `--term` adds a name.
- One discovery sends at most 200 values. A unit whose files name more, such as a repository root that holds the manifests of many services, is refused with the count rather than cut down to some of them: run `discover` for one service with `--path`, or link by hand. A string longer than 253 characters is not read as a name, and neither is one whose words joined by a separator are longer than that, since every spelling is sent.
- Entity ids per candidate are listed up to 50 per row, counted as ids also where a record carries several in one field. `-o json` has the list, whether it was cut (`servicesCapped`) and the count (`serviceCount`): the ids listed when the list is whole, and an estimate of at least 51 when it was cut.
- The filter narrows the leading `fetch` and nothing else. Rows a query brings in another way are not narrowed, and the scope still reports as applied to the data object it names: a subquery on another data object (`append [fetch bizevents]`, `lookup [fetch dt.entity.service]`), or literal rows (`append [data record(...)]`). A subquery that fetches `logs` or `spans` again is different: the scope is then not applied at all, as described above.
- Filter segments are applied by the platform, not by dtctl. Its documentation says that subqueries in `join`, `lookup` and `in` run outside the segments of the query, so an entry's segments do not narrow those either.
- `inventory arrivals --scope` does not use the repo scope.
- `set` and `delete` rewrite the whole file: comments and hand formatting are lost, and of two that run at the same moment the later one wins, so the other's entry is gone although both reported success. `git diff` shows it.
