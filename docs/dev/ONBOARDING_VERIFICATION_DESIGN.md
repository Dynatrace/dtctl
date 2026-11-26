# Onboarding Verification Design Proposal

**Status:** Implemented (see Implementation notes)
**Created:** 2026-09-11
**Author:** dtctl team

## Overview

After you onboard a new data source — instrument a service, point an OTel collector
at the tenant, `dtctl ingest` a file — the next question is always the same:

> Did it land? Which signals, how many records, how recently?

Today that question is answered either by an LLM investigation (minutes, non-deterministic)
or by hand-authoring one DQL query per signal and knowing each stream's schema. This
proposal makes it a single deterministic command — `dtctl inventory arrivals`, which adds
a **time window** and a **scope filter** to the existing inventory machinery — and closes
the confirmation gap that `dtctl ingest` is about to open.

## Goals

1. **Deterministic** — same inputs, same answer, in seconds; no model in the loop.
2. **Per-signal** — logs / spans / metrics / bizevents / … answered separately, each with
   a count and a `last_seen`.
3. **Windowed** — "since T", so a source that emitted once last week does not read as healthy.
4. **Honest** — keep `inventory`'s three-state verdict. A probe that could not run is
   `unknown`, never `absent`.
5. **Cheap** — an onboarding check that scans 200 GB is not a check anyone will run in a loop.
6. **Agent-consumable** — one call, small structured result, usable as a gate.

## Non-Goals

- A new server-side endpoint or any backend-mediated read. dtctl holds the tenant's own
  credentials and already has DQL; this check belongs in the client.
- Entity/service joins, VCS-repository matching, or a `--service` flag — that is product-side
  domain modelling, and `dt.entity.*` is not available on Grail-only tenants.
- A second query language. The scope filter is a DQL fragment, nothing else.
- Judging "health". Report presence, counts, and timestamps; the caller judges.

---

## What exists today

| Need | Command | Gap |
|---|---|---|
| Poll until data arrives | `dtctl wait query '<dql>' --for=any` | You must author the DQL and know each stream's schema; one signal per invocation |
| What data exists here | `dtctl inventory` | Environment-wide, **no time window**, **no scope filter** |
| Validate before executing | `dtctl verify query` / `verify openpipeline-*` | Static; never touches live data |
| Push data in | `dtctl ingest` (design, `feat/ingest`) | Per **D12**, reports *HTTP status only* — a `202` says nothing about landing |

So roughly 80% of the machinery is already in the repo. The gap is narrower than it first
appears, and it is concentrated in `inventory`.

## The core design tension

`inventory`'s structural capability shapes (`DataObject`, `EntityTypes`, `MetricKey`) are
**retention-scoped, not window-scoped**. `DataObject` presence is decided from
`facts.streamRows` — bucket record counts summed across the whole retention period
(`sdk/inventory/discover.go:150`). "logs: present" therefore means *logs exist somewhere in
retention*, which is precisely the false-healthy reading this proposal sets out to fix.

A window cannot be bolted onto the structural shapes; it needs a different evidence shape —
an actual counting query per stream. That is the real work in this proposal, and it turns out
to be dominated by query cost, not by plumbing.

---

## Measured: probe cost is the design constraint

All figures measured against a live busy tenant (`gmg`) on 2026-09-11, 15-minute window
unless stated. This is not incidental detail — it determines the command's shape.

| Probe shape | Scanned | Time |
|---|---|---|
| `fetch logs \| summarize count()` — unfiltered | **203.7 GB** | 1293 ms |
| `fetch logs \| summarize total=count(), matched=countIf(<expr>), last_seen=takeMax(timestamp)` | **204.9 GB** | 1752 ms |
| `fetch logs \| filter <expr> \| summarize matched=count(), last_seen=takeMax(timestamp)` | **10.8 GB** | 209 ms |
| …same, 5-minute window | 4.1 GB | 150 ms |
| `fetch spans \| summarize …countIf(<expr>)…` | 56.5 GB | 1278 ms |
| `fetch spans \| filter <expr> \| summarize …` | **1.2 GB** | 45 ms |
| `fetch bizevents \| filter <expr> \| summarize …` | 12.1 GB | 635 ms |
| `metrics \| filter matchesValue(metric.key,"dt.kubernetes.*") \| summarize count()` | **0 B** | 1132 ms |
| `timeseries count(<key>), from:now()-15m, filter:<expr>` — *scoped* metric arrival | **0 B** | 24 ms |
| `fetch dt.system.buckets \| summarize sum(records), by:{dt.system.table}` — retention metadata | **0 B** | 45 ms |

Three conclusions, each of which changes the design:

1. **Filter-first, never `countIf`.** Pushing the predicate into `| filter` before
   `summarize` is **19× cheaper on logs** and **48× on spans** than computing
   `total` and `matched` together with `countIf`. The combined-probe shape — the obvious
   way to write it, and the one that would let us report "stream is live but your filter
   matched nothing" in a single query — is unaffordable.
2. **The unfiltered count is the most expensive query available.** `fetch logs | summarize
   count()` scans 203.7 GB — *more* than the filtered probe, despite referencing no columns.
   An unscoped windowed count must never run.
3. **Metric arrival, and the retention metadata, are free.** `timeseries … filter:<expr>`
   scopes metrics by the same `--scope` predicate at 0 scanned bytes and 24 ms, and the
   bucket-metadata query that backs the `empty` vs `no-data` distinction is likewise free.
   Metrics get a different probe than the `fetch` streams, and cost is a non-issue on either.

Scan scales roughly linearly with window: 5m = 4.1 GB, 15m = 10.8 GB on logs. At
`--scan-limit-gbytes 25` (today's default) a 1-hour unfiltered-stream window would be
refused and correctly degrade to `unknown` — useless, but not wrong. Hence a small default
window and a mandatory scope.

---

## Design

### `dtctl inventory arrivals --scope <dql-filter>`

```
--scope <dql-filter>           DQL filter fragment applied to each probed stream. Required.
--since <duration>             Arrival window, e.g. 15m or 2h (default 15m).
--signals <list>               Restrict probing to named capabilities (default: signal streams).
--require <list>               Exit non-zero unless every named signal is live.
--stale-after <duration>       Age past which a signal is stale, not live (default max(2m, window/3)).
```

This is a **subcommand**, not a mode of `dtctl inventory` — see D1. The parent keeps its
original shape and carries none of the windowed flags.

`--scope` takes a **DQL filter expression**, inserted as `| filter <expr>` immediately after
the fetch. It is not a new mini-language, not a key=value DSL, and not translated. This
keeps faith with the "no second query language" constraint and means anything the user can
express in DQL works on day one.

Windowed mode probes only the signal-stream capabilities. Topology (`EntityTypes`) and
metric-family (`MetricKey`) shapes have no natural "since" meaning for a live census, so
they are reported as evaluated-unwindowed rather than silently reinterpreted.

### Why `--since` requires a scope

Onboarding verification is inherently *"did **this new thing** arrive"*. Without a scope you
are asking "is the tenant alive", which plain `inventory` already answers for free from
bucket metadata. And per measurement 2, the unscoped windowed count is the single most
expensive query in the battery. Requiring a scope — `--scope`, `-S/--segment`, or both (see
open question 5) — removes a 200 GB footgun and costs nothing in expressiveness.

### Distinguishing "no data" from "wrong filter"

The valuable disambiguation — *your filter matched nothing, but the stream itself is live* —
would naively come from the combined `countIf` probe, which measurement 1 rules out. It
comes for free instead from the bucket metadata `inventory` **already collects**
(`facts.streamRows`, one query for all streams). That answers "does this stream hold data at
all, in retention" without a window. Combining the two gives three distinguishable absences:

| `streamRows` | windowed `matched` | Verdict and evidence |
|---|---|---|
| stream not in catalog | — | `absent` — "no `bizevents` in the data-object catalog" |
| 0 rows in retention | 0 | `absent` — "stream exists but holds no data in retention" |
| > 0 rows in retention | 0 | `absent` — "stream holds data in retention, but 0 records matched `<filter>` in the window" ← *the onboarding-failure case* |
| any | > 0 | `present` |
| any | probe truncated / scan-capped / errored | **`unknown`** — never absent |

That third row is the one that actually matters after an instrumentation change, and it is
reachable with zero extra queries.

### Scope applicability — when the question was never askable

A scope does not apply uniformly to every signal. `k8s.namespace.name` will never appear on
RUM or synthetic events; `service.name` will never appear on Kubernetes or host metrics;
Davis problems and security events carry neither unless something backend-side emits them
for that scope. Grail does not complain about this: a `filter` on a field the stream does
not have returns **zero records, no error, no notification**. Indistinguishable from a real
onboarding failure.

The consequence is not cosmetic. Under a namespace scope, `rum` came back as a confident
`empty` — *"the stream works, this scope is not producing into it"* — which reads as an
instrumentation problem to fix. And `--require rum` would exit non-zero forever on a signal
that structurally cannot carry the scope. The all-zero tripwire does not catch it either: it
fires only when *every* signal is empty, and here the k8s signals were live.

So a zero match is not accepted at face value. It is disambiguated, and only ever after the
cheaper explanations are ruled out:

1. **No-data short-circuit.** If bucket metadata already says the stream holds nothing in
   retention, the verdict is `no-data` and no applicability probe runs. Nothing to learn.
2. **Field-existence probe**, per shape:
   - **Streams** — `fetch <obj>, from:<window> | filter isNotNull(<f1>) or … | limit 1 |
     summarize present = count()`. `isNotNull` over the scope's field names is the only
     sound test here.
   - **Metric families** — `timeseries n = count(<key>), from:<window>, by:{<f1>, …} |
     limit 1`, read through the result's **type metadata**: a dimension the data does not
     carry comes back typed `undefined`. Exact and zero-byte.
3. **Quiet-window confirmation.** If the field probe finds nothing, a second near-free
   `fetch <obj>, from:<window> | limit 1 | summarize any = count()` checks whether the
   stream produced *anything at all* in the window. If it did not, the verdict is
   `unknown`, never `n/a` — a quiet stream must not be reported as a missing field.

Step 3 exists because step 2 alone conflates "the field is absent" with "the stream was
idle". Without it, any stream that happened to be quiet for fifteen minutes would be
declared structurally incapable of the scope.

**The two probes do not support equally strong claims, and the evidence must not pretend
otherwise.** A metric dimension is resolved against the metric *definition*, so an
`undefined` type really does mean the dimension does not exist on that key — structural,
window-independent. A stream is only *sampled*: all the probe establishes is that no record
in this window carried the field. That is strong evidence, not proof, and Grail offers
nothing to make it proof — `describe` returns only core fields and the catalog carries no
field list at all (open question 4). So the two verdicts are worded differently:

| Shape | Evidence | Claim |
|---|---|---|
| metric family | `… is not a dimension of any of the 3 keys probed from dt.host.*` | **structural** — "an empty result here is a property of the question, not of the data" |
| stream | `no logs record in the last 15m carries the scope's field (k8s.namespace.name)` | **window-scoped** — "the scope selects on nothing this signal has — widen `--since` to test that harder" |

Collapsing these into one sentence was the first version, and it was wrong in the same way
the bug this feature fixes is wrong: it dressed a windowed observation up as a structural
fact. A multi-tenant run caught it — `bizevents` on one tenant and `logs` on another came
back `n/a` under a namespace scope, both of which are fields those streams carry in general.

**The type trick does not transfer to streams.** `| limit 1 | fields x = <field>` looked
like a cheaper test than `isNotNull`, and it is wrong: on a live tenant, `service.name` over
`logs` types as `undefined` because stream result types are inferred from the *sampled
record*, not from a schema. A record without the attribute is not evidence the attribute
cannot exist. For `timeseries … by:{}` the grouping dimension is resolved against the metric
definition, so there the same signal is sound.

Cost is bounded: the probes run only for signals that matched nothing and hold data, at most
two extra queries each, and the metric variant is free. The worst case measured — a scope
where every signal came back empty — was 69 queries / 29.9 s.

Scope fields are extracted by lexing the expression: backtick-quoted identifiers first, then
string literals blanked out so their contents cannot be mistaken for fields, then bare
dotted identifiers that are neither DQL keywords nor followed by `(`. A scope naming no
fields at all — `--scope true` — yields no applicability verdict and everything behaves as
before.

### The scan cap is the binding constraint on large tenants

Measurement 1 priced a filtered 15-minute logs probe at 10.8 GB, comfortably under the 25 GB
default cap. That figure does not generalise. On the largest tenant tested, **`spans` scan
44 GB in a *five*-minute window** — so at the default cap `logs` and `spans` are
unanswerable there at any window worth asking about, and they are exactly the two signals
an onboarding check is usually about.

This is honest degradation: the probe reports `unknown`, never `absent`, so nothing is
fabricated. But "unknown" next to `logs` is easy to read as a finding, and the original
evidence line made it worse by listing three possible causes and two remedies:

> not evaluated: probe was cut short by a limit (scan cap, result cap, or read timeout) —
> narrow --since or raise the cap

On that tenant, narrowing `--since` does not work at any practical window, so the first
remedy offered is the one that cannot help. The three causes are therefore kept distinct
(`TruncationCause`, threaded from `pkg/exec`'s existing notification classification through
`RunResult` to the evidence) and each gets the remedy that applies to it:

> not evaluated: spans scans more than the 25 GB scan cap over the last 15m, so the probe
> stopped before it could count — raise --scan-limit-gbytes, or narrow --since far enough
> that the stream fits under the cap

Verified: at `--scan-limit-gbytes 500` the same two signals resolve on that tenant (spans
live at 66,289 records; logs genuinely `empty`). The advice works, which is the only reason
it is worth printing.

Because a skimmed table shows only the word `unknown`, the scan cap is also called out
above the evidence block and in the agent suggestions, and `Signal.Truncation` carries the
cause structurally so a caller can branch on it rather than parse prose. The cap the run
used is reported on the window (`ArrivalWindow.ScanLimitGBytes`) — without the number, "raise
the cap" is not actionable.

### Per-stream time field

`fetch spans` carries **no `timestamp` field** — verified: `takeMax(timestamp)` on spans
returns type `undefined` and the field is dropped from the record entirely, while
`takeMax(start_time)` works. So `last_seen` cannot use one field name across streams.

`CapabilityDef` gains an optional `timeField` (default `timestamp`; `spans` → `start_time`),
used only in windowed mode. It is a fourth optional attribute, not a fifth discovery shape,
so the exactly-one-shape contract in `validateDef` is untouched.

### Per-signal ingest state

The three-state capability verdict (`present` / `absent` / `unknown`) is too coarse once a
window exists. `arrivals` assigns each signal type one of seven states, derived from the
windowed probe plus the free bucket metadata and, for a zero match, an applicability probe:

| State | Condition | Means |
|---|---|---|
| `live` | `matched > 0`, `last_seen` within `--stale-after` | Arriving now |
| `stale` | `matched > 0`, `last_seen` older than `--stale-after` | **Started, then stopped inside the window** |
| `empty` | `matched == 0`, stream holds records in retention | Stream works; this source is not producing into it |
| `no-data` | `matched == 0`, stream empty in retention | Stream exists but is unused on this tenant |
| `n/a` | `matched == 0`, and the scope's fields are absent from this signal — structurally for a metric family, across the window for a stream | **The question was never askable of this source** — see "Scope applicability" |
| `absent` | Stream not in the data-object catalog | Not available in this environment |
| `unknown` | Probe truncated, scan-capped, or errored | No verdict — never read as absence |

`stale` is the state that only becomes expressible once there is a window, and it is the most
diagnostic one for onboarding: a source that emitted during rollout and then stopped looks
identical to a healthy one under any retention-scoped check. Default `--stale-after` is
`max(2m, window/3)`.

For `--require` purposes `live` passes; `stale`, `empty`, `no-data`, `n/a`, and `absent`
fail; `unknown` and an unprobed name exit 11. A required `n/a` signal is reported as a gate
that *can never pass* rather than as a telemetry failure.

### Exit codes

Plain `dtctl inventory` keeps exit code 0 always — no behaviour change. `--require` opts in
to gate semantics, matching the `verify` family's conventions:

```
 0 - every required signal live
10 - at least one required signal is not live (stale, empty, no-data, n/a, absent)
11 - at least one required signal is unknown (no verdict — retry or widen the budget)
```

`unknown` deliberately does not collapse into "not live": a CI gate that fails closed on a
scan-capped probe is reporting a dtctl budget problem as a customer telemetry problem.

Exit **1** is left to cobra and to ordinary command failure — a mistyped flag, an auth
error, an unparseable scope. Overloading it with a gate result would make a usage error
indistinguishable from a genuine telemetry verdict, which is exactly the confusion a CI
gate must not have. Hence the gate codes start at 10.

---

## Examples

### 1. Did the new namespace start reporting?

Every number below is real: measured on the `gmg` tenant, 2026-09-11, with the probe shapes
this document specifies.

```console
$ dtctl inventory arrivals --since 15m --scope 'k8s.namespace.name == "dps-ingest"'

Context:    gmg
Generated:  2026-09-11T06:25:00Z
Window:     now()-15m → now()  (15m, stale after 5m)
Scope:      k8s.namespace.name == "dps-ingest"

SIGNAL             STATE      VOLUME    LAST SEEN   AGE
spans              live    3,630,872    06:22:53    19s
logs               live      237,194    06:22:32    39s
events             live        7,434    06:22:46    28s
security.events    live        4,920    06:23:55    47s
bizevents          live        4,499    06:22:52    21s
k8s-metrics        live    400 points   06:24:00     —
user.events        empty           0           —     —
davis-events       absent          —           —     —

6 live · 1 empty · 1 absent · 0 unknown

Notes
  user.events  — 0 records matched in the window, but the stream holds 2.9B records
                 tenant-wide: the stream works, this source is not producing into it
  davis-events — not in this environment's data-object catalog

Discovery: 8 queries, 1.5s query time, 29.0 GB scanned
```

The `user.events` line is what earns the feature. A retention-scoped check reports that
stream as simply "present" (2.9B records exist); an unscoped windowed check cannot attribute
it to this source at all. Only the combination says *the stream works, your source isn't
producing into it* — and it costs no extra query, because the retention figure comes from the
bucket metadata `inventory` already collects for free.

### 2. The false-healthy case this exists to prevent

```console
$ dtctl inventory
Capabilities: logs, spans, bizevents        # ← retention-scoped: "yes, sometime in 35 days"
```

```console
$ dtctl inventory arrivals --since 30m --scope 'service.name == "pricing-service"'

SIGNAL      STATE     VOLUME   LAST SEEN   AGE
logs        stale     12,904    05:58:11   27m
spans       stale      3,551    05:58:09   27m
bizevents   empty          0           —    —

0 live · 2 stale · 1 empty · 0 unknown

Notes
  logs, spans — data arrived in this window but stopped 27m ago (stale after 10m):
                the source emitted during rollout and is no longer reporting
```

Same service, opposite answers. The unwindowed check calls it healthy. The windowed one shows
the actual failure mode — instrumentation that worked at deploy time and then stopped — which
is invisible to any retention-scoped check. A source that emitted once last week, or once
during a deploy, reads as perfectly healthy without a window.

### 3. As a CI / agent gate

```console
$ dtctl inventory arrivals --since 10m \
    --scope 'k8s.namespace.name == "payments"' \
    --require logs,spans
$ echo $?
0
```

```console
$ dtctl inventory arrivals --since 10m --scope 'service.name == "checkout"' --require logs,spans,bizevents
...
$ echo $?
1        # bizevents absent
```

### 4. Agent mode

```console
dtctl inventory arrivals --since 15m --scope 'k8s.namespace.name == "payments"' --agent
```

```json
{
  "ok": true,
  "result": {
    "context": "prod-eu",
    "generatedAt": "2026-09-11T09:14:22Z",
    "window": { "since": "now()-15m", "filter": "k8s.namespace.name == \"payments\"" },
    "summary": { "live": 6, "stale": 0, "empty": 1, "noData": 0, "notApplicable": 0, "absent": 1, "unknown": 0 },
    "signals": [
      { "signal": "spans",           "state": "live",   "records": 3630872, "lastSeen": "2026-09-11T06:22:53Z", "ageSeconds": 19 },
      { "signal": "logs",            "state": "live",   "records": 237194,  "lastSeen": "2026-09-11T06:22:32Z", "ageSeconds": 39 },
      { "signal": "events",          "state": "live",   "records": 7434,    "lastSeen": "2026-09-11T06:22:46Z", "ageSeconds": 28 },
      { "signal": "security.events", "state": "live",   "records": 4920,    "lastSeen": "2026-09-11T06:23:55Z", "ageSeconds": 47 },
      { "signal": "bizevents",       "state": "live",   "records": 4499,    "lastSeen": "2026-09-11T06:22:52Z", "ageSeconds": 21 },
      { "signal": "k8s-metrics",     "state": "live",   "datapoints": 400,  "lastSeen": "2026-09-11T06:24:00Z", "ageSeconds": 60 },
      { "signal": "user.events",     "state": "empty",  "records": 0,
        "evidence": "0 matched in window; stream holds 2902744233 records in retention" },
      { "signal": "davis-events",    "state": "absent",
        "evidence": "no dt.davis.events in the data-object catalog" }
    ],
    "discovery": { "queries": 8, "seconds": 1.5, "scannedGbytes": 29.0 }
  },
  "context": {
    "verb": "inventory",
    "duration": "1.6s",
    "suggestions": [
      "user.events is live tenant-wide but empty for this scope — check the source's emission, not the tenant",
      "Unknown signals got no verdict; re-run with --budget-seconds raised rather than treating them as absent"
    ]
  }
}
```

The envelope is the existing `output.Response` shape (`pkg/output/agent.go:37`) — agents that
already consume `inventory` need no changes beyond the new `arrivals` block.

### 5. Closing the ingest loop

`dtctl ingest` reports HTTP status only (**D12**: the 3rd-gen endpoints will not return
accepted-line counts at all), so `202` is not confirmation. The documented recipe:

```console
$ dtctl ingest logs -f payload.json
202 Accepted

$ dtctl inventory arrivals --since 5m --scope 'log.source == "batch-import"' --require logs
$ echo $?
0
```

Or, when you want to block rather than check once:

```console
$ dtctl wait query 'fetch logs | filter log.source == "batch-import"' \
    --for=count-gte=1 --timeout 60s
```

### 6. An unknown is not an absence

```console
$ dtctl inventory arrivals --since 1h --scope 'k8s.namespace.name == "payments"'

Unknown (no verdict — not evidence of absence)
  logs — probe exceeded the 25 GB scan cap (1h window ≈ 43 GB on this stream);
         narrow --since or raise --scan-limit-gbytes

Discovery: 6 queries, 12.1s query time
  note: 1 probe refused by the scan cap — the inventory is partial
```

Exit code with `--require logs` would be **11**, not 10: dtctl ran out of budget, the customer's
telemetry is not implicated.

---

## Decision log

- **D1 — Extend `inventory`, do not add a verb; windowed mode is a *subcommand*.** The
  evidence machinery, the budget, the verdict model, and the agent envelope all already
  exist, and `verify` is the wrong home: in dtctl it means "validate without executing", and
  this executes. But **revised after review**: windowed mode is
  `dtctl inventory arrivals`, not a flag on `dtctl inventory`. Four things had already made
  it a separate command in all but name — two mutually exclusive flag sets, two disjoint
  output schemas (`Capabilities`/`Absent`/`Unknown` versus `Signals`/`Window`/`Summary`),
  a separate renderer, and an exit-code contract that exists in only one of the two modes.
  A flag that suppresses most of a command's output and replaces the rest is a subcommand
  wearing a disguise; naming it one makes the help text, the flag validation, and the
  scope-required rule all fall out for free.
- **D2 — `--scope` is a DQL filter fragment.** No key=value DSL, no `--service`. Upholds the
  no-second-query-language constraint and works with any field on day one. Named `--scope`,
  not `--where`, after review: `--where` sets up a SQL expectation the flag does not meet
  (it is a fragment, not a clause, and it is applied to *every* probed stream rather than to
  one query), and "scope" is already the word this design uses throughout for the thing
  being verified.
- **D3 — Filter-first probe shape; never `countIf`.** Measured 19× cheaper on logs, 48× on
  spans. Forfeits the single-query "live but unmatched" signal, which D4 recovers for free.
- **D4 — `arrivals` requires a scope** (`--scope` or, later, `-S/--segment`). The unscoped
  windowed count is the most expensive query available (203.7 GB measured), and bucket
  metadata already answers the unscoped question at no cost. `--since` now defaults to `15m`
  rather than being the mode switch — with a subcommand there is no mode to switch.
- **D5 — Per-stream `timeField` on `CapabilityDef`** (default `timestamp`, `spans` →
  `start_time`). Verified necessary: spans carry no `timestamp`. An optional attribute, not a
  new discovery shape — `validateDef`'s exactly-one-shape contract is untouched.
- **D6 — Metrics are probed with `timeseries … , filter:<scope>`, not `fetch`, over a bounded
  sample of the family's keys.** Measured **0 scanned bytes / ~25 ms** per probe while
  honouring the same scope. Two constraints found during implementation, both
  verified on a live tenant:
  - **The keys cannot be combined into one query.** A multi-aggregation
    `timeseries a = count(k1), b = count(k2), …` returns *no rows at all* when any single key
    has no data — one quiet key erases the evidence of the live ones.
  - **One key is not a family.** Resolving the glob to a single representative produced a
    confident `empty` for a namespace whose k8s metrics were demonstrably flowing: the
    arbitrary pick (`dt.kubernetes.container.cpu_throttled`, first alphabetically) is a key
    nobody emits, while `…cpu_usage` was reporting 25 datapoints a minute.

  So up to 8 keys are probed, one free query each. A hit settles the family. A miss across the
  sample becomes `empty` **only if the sample was the whole family and the catalog was
  complete** — otherwise `unknown`, naming how many of how many keys were checked. Metric
  signals report datapoints rather than records.
- **D7 — Three-state verdict preserved.** Truncated, scan-capped, or errored probe →
  `unknown`. Absence evidence distinguishes catalog-absent / retention-empty / window-empty.
- **D8 — Exit codes are opt-in via `--require`.** Plain `inventory` stays exit-0.
- **D9 — No entity or repository joins.** Domain modelling belongs on the product side;
  Grail-only tenants have no `dt.entity.*`.
- **D10 — No `--wait-for` on `ingest` in v1.** `ingest` is pass-through by design and cannot
  know what identifies a payload; inferring a discriminator would mean introspecting the body.
  Ship the documented `ingest` → `inventory --since` / `wait query` recipe instead. A future
  `ingest --wait-for '<dql-filter>'` — where the *user* supplies the discriminator — stays
  compatible with pass-through and can be added without rework.
- **D11 — New `arrivals` block on `Inventory`.** Additive; existing consumers unaffected.
- **D12 — A zero match is disambiguated, not reported.** The `n/a` state and the two-step
  applicability probe above. The alternative — a static table of which scope fields apply to
  which signal — was rejected: it would be wrong the moment a customer adds an attribute,
  and it cannot be derived from any catalog surface (open question 4).
- **D13 — Signal names are validated before the battery runs.** `--signals` and `--require`
  are checked against the definition set up front, and `--require` outside `--signals` is
  rejected. Previously a typo in `--require` ran the full battery and then exited as though
  the signal were down, which is the most expensive possible way to report a typo.
- **D14 — A scope that does not parse is a usage error, not a per-signal `unknown`.** DQL
  parse notifications are detected and surfaced once, with the offending scope quoted,
  instead of costing the whole battery and yielding N identical failures.
- **D15 — Gate exit codes start at 10.** Exit 1 stays cobra's, so a usage error is never
  mistaken for a telemetry verdict.
- **D16 — The `n/a` evidence is worded to the strength of the probe behind it.** Structural
  for metric dimensions, window-scoped for streams. Found by running across tenants: a
  single shared sentence overclaimed on every stream verdict.
- **D17 — Truncation causes are distinguished, not collapsed.** A scan cap, a result cap and
  a read timeout have different remedies, and on a high-volume tenant the scan cap is the
  binding constraint on the feature's headline use case. The cause is carried structurally
  on `Signal.Truncation`, and the cap in force is reported on the window.
- **D18 — D16 extended: a metric family's `n/a` is only *structural per key*, and the
  wording now says which.** The dimension probe samples at most
  `metricApplicabilitySampleSize` keys, and a family need not be homogeneous —
  `dt.kubernetes.*` spans node- and container-level keys with different dimension sets.
  Measured on a live tenant: `cloud.aws.*` holds **44** keys and `dt.host.*` **49**, of which
  **3** were probed, yet the evidence read "this signal structurally cannot carry this scope".
  That is the same sampled-to-absolute leap D16 removed from the stream wording, one level
  down. The verdict stays `n/a` — degrading it to `unknown` would erase the state's whole
  point — but the line now names the sample and the family size, and the `--require` message
  says "were not found on" rather than "do not exist on".
- **D19 — Evidence may only name a remedy the CLI accepts.** A sampled metric family's
  `unknown` advised "probe a specific key with `--signals`", but `--signals` selects
  capability names, so following it returned `unknown signal dt.kubernetes.container.cpu_usage`.
  It now names the `dtctl query 'timeseries …'` that actually settles the key. Guarded by
  `TestMetricSampleAdviceIsRunnable`.
- **D20 — Usage is validated before the client is built, and `--scope` is not
  `MarkFlagRequired`.** Cobra's required-flag check runs ahead of `RunE`, so marking the flag
  made `required flag(s) "scope" not set` the message every user saw and left the one
  explaining *why* a scope is mandatory unreachable. Likewise a mistyped `--since` or signal
  name is a typo, not an environment problem: resolving credentials first reported it as an
  auth failure on any machine without a usable context.
- **D21 — A matched signal with no event time says so.** `takeMax` over a field a stream does
  not carry yields an *undefined column, not an error*, so the signal reported `live` with no
  age and could never reach `stale` — the state the feature exists to surface. That is exactly
  the defect `TimeField` fixes for `spans` and `rum`, and a custom definition can reintroduce
  it; it is now visible in the evidence instead of presenting as a clean verdict.
- **D22 — A scan-capped stream is re-probed with sampling, and a sampled miss is still not an
  absence.** The scan cap was the dominant source of `unknown` on a large tenant, and it lands
  on exactly the signals an operator came to check. `samplingRatio` is the only lever that
  reduces the *scan* rather than the result: measured on `dre`, the scoped 15-minute logs
  probe fell from **341 GB to 0.92 GB** at 1-in-1000 while landing within **1.5%** of the true
  count, turning an `unknown` into a real `live`. Four properties make it safe to lean on, and
  each is a guarded rule rather than an assumption:
  - *A hit is conclusive at any ratio*, because sampling only ever drops records. So the
    ladder walks **most-aggressive-first and stops at the first hit** — it does not converge
    on precision, which would cost ~10x per extra rung for no change in verdict
    (`TestSampledProbeStopsAtFirstConclusiveRung`).
  - *A miss is a bound, not an absence.* A sampled zero stays `unknown` and reports the
    population it can rule out (~3x the ratio, at 95%). This is strictly better evidence than
    the truncation it replaces, which covers an unspecified slice of the window and supports
    no bound at all (`TestSampledZeroIsNeverEmpty`).
  - *The ratio is divided out of the data, never copied from the request.* Grail refuses to
    sample `events`, `bizevents` and `security.events`, and rounds every ratio down to a power
    of ten — reporting both as a warning on a **200**, with a full unsampled result. Trusting
    the requested ratio would have overstated a refused object's volume by that entire factor,
    so the probe sums `dt.system.sampling_ratio` and the runner reads `metadata.sampled`
    (`TestSampledCountIgnoresRequestedRatio`, `TestSamplingRefusalIsNotRetried`). A rung
    without the flag is a refusal only if it matched a record or was itself cut short. One
    that matched nothing and ran to completion is an empty rung: the walk continues past it,
    and it supplies no bound, since nothing in the response confirms the ratio
    (`TestUnflaggedEmptyRungDoesNotEndTheWalk`, `TestSampledBoundComesFromAFlaggedRung`).
  - *A sampled `takeMax` is biased old by about `window / sampled_records`* — measured at 91s
    for 11 records over 15m, and under a second for 676. A `stale` verdict the bias alone could
    explain is therefore withheld as `unknown`, because inventing an ingest outage is the most
    expensive wrong answer this command can give (`TestSampledStaleInsideTheBiasIsUnknown`).

  The error sampling introduces shrinks with volume, and the cap only binds on high volume, so
  the two failure modes barely overlap: the signals sampling is imprecise about are the ones
  that never needed it. The *applicability* probe behind `n/a` is deliberately never sampled —
  a sampled miss there would manufacture the strongest claim the feature makes. `--no-sample`
  restores the plain `unknown` for a caller who prefers no answer to an approximate one.

## Open questions — with recommendations

All five carry a recommendation; three were settled empirically on `gmg`, 2026-09-11.

### 1. Time fields for the remaining streams — RESOLVED

`timestamp` works for `logs`, `bizevents`, `events`, `security.events`, and `user.events`;
`spans` requires `start_time`. The default-plus-one-override model in D5 holds, with `spans`
as the only known override. `dt.synthetic.events` and `dt.davis.events` are absent from that
tenant and remain unverified — treat an unresolvable time field as `unknown`, not `absent`.

### 2. The `metrics` "result cap" — RESOLVED, and it exposed a bug in `main`

**It is not a result cap.** `metrics | summarize count()` returns exactly 100,000 alongside
this notification:

```
FETCH_EXEC_TIME_LIMIT (WARNING) — "Your result is incomplete because the data couldn't be
read within the internal time limit of 10008 ms. Please try narrowing your timeframe."
```

So the 100,000 is wherever a **10-second internal read limit** cut the read, not a fixed
ceiling. Nothing should hardcode it.

**And sometimes there is no notification at all.** The same catalog query scoped to a 15m
window returned exactly 100,000 keys in 3.9 s with an empty `notifications` array — a silent
cap. Notification-based truncation detection is necessary but not sufficient: the read site
must also treat "returned exactly as many rows as the limit" as truncation, which is what
`metricCatalogLimit` in `sdk/inventory/discover.go` now does.

**Recommendation — fix `classifyNotification` first, as a standalone bug fix.**
`pkg/exec/dql.go:481` handles `FETCH_TIMEOUT` but **not** `FETCH_EXEC_TIME_LIMIT`, and neither
message fallback matches this wording. It therefore returns `""`, so `ResultIsPartial`
(`pkg/exec/dql.go:509`) reports **false** and a truncated result is treated as complete.

That is a live fabricated-absence bug in `inventory` today, independent of this proposal: a
metric catalog cut short at 100k keys is read as complete, and every metric family below the
cut is reported `absent` with confident evidence. It is the same class of bug
`inventoryMaxResultRecords` (`cmd/inventory.go:158`) was introduced to prevent, arriving by a
different route.

Add `case "FETCH_EXEC_TIME_LIMIT": return notifTimeout`, plus a message fallback on
`"internal time limit"`. Once that lands, windowed mode needs no metric-specific truncation
handling at all — the existing `Truncated` → `unknown` path does the right thing.

### 3. Default window — recommend 15m, leave the cap alone, add a pre-flight warning

Scan cost is essentially linear in window width, measured on the same scope:

| Window | Scanned | Records |
|---|---|---|
| 5m (`kube-system`) | 4.1 GB | 29.1M |
| 15m (`kube-system`) | 10.8 GB | 85.1M |
| 15m (`dps-ingest`) | 6.0 GB | 84.7M |
| 30m (`dps-ingest`) | 11.7 GB | 155.3M |

Two-fold window, 1.94× the bytes. Extrapolating, the 25 GB default cap bites somewhere
between 30m and 60m depending on how selective the scope is.

**Recommendation:** default `--since 15m`. Do **not** make the scan cap window-aware — the
cap is a global safety net and silently raising it for this command would defeat its purpose.
Instead warn at parse time when `--since` exceeds 1h that probes are likely to truncate to
`unknown`, and let the existing scan-limit hint (`pkg/exec/dql.go:524`) carry the remedy. The
honest-degradation path already exists; it just needs to be reachable and explained.

### 4. Filter validity — recommend *not* validating fields, and adding a tripwire instead

I tried to resolve this with free schema metadata and the answer is that it cannot be done:

- `fetch dt.system.data_objects` carries `name`, `type`, `scope`, `owner`, `query_string`,
  `description`, `has_access`, `usable_with` — **no field list at all**.
- `describe logs` is free (0 bytes, 2 ms) but returns only the 8 **core** fields
  (`timestamp`, `content`, `ordinal`, the `dt.system.*` set). It does **not** list
  `k8s.namespace.name` — the very field that matched 237,194 records in the example above.

So neither surface distinguishes "this field does not exist" from "this field is a dynamic
attribute". Validation built on either would reject exactly the attributes people actually
scope by. **Do not build it.**

**Recommendation — two cheap measures instead:**

1. **Syntax-check** the assembled probe through the existing `verify query` path before
   running the battery. It catches typos and malformed DQL; it will not catch unknown fields,
   and the docs should say so.
2. **All-zero tripwire.** If *every* probed signal returns 0 while the streams themselves hold
   records in retention, that is far more likely a wrong `--scope` than a source that failed
   on every signal at once. Emit a warning saying so rather than a confident all-absent
   verdict. Costs no extra query — both facts are already in hand.

This does not fully close the gap, and the doc should be honest that it does not.

### 5. Segment interaction — recommend composing, and let `-S` satisfy the scope requirement

**Not implemented in the first PR** — `--scope` only. Segment name-to-UID resolution lives in
`cmd/query.go`, and wiring it into `inventory` is a separate change.

**Recommendation for the follow-up:** `-S/--segment` and `--scope` compose with AND. They are
both filters, and a segment is simply the reusable, governed form of the same idea.

Consequently, restate D4 as *`arrivals` requires **a scope***, satisfied by `--scope`, by
`-S`, or by both. Demanding a redundant `--scope` next to an existing segment would be
busywork, and the cost argument that motivates D4 is satisfied either way.

## Implementation sketch

| Area | Change |
|---|---|
| `pkg/exec/dql.go` | **Prerequisite bug fix** (open question 2): `classifyNotification` must map `FETCH_EXEC_TIME_LIMIT` → `notifTimeout`, else truncated probes are read as complete |
| `sdk/inventory/inventory.go` | `SignalState` enum + `Signal` type; `Signals []Signal`, `Summary`, `Window` on `Inventory`; `TimeField` on `CapabilityDef` |
| `sdk/inventory/discover.go` | `DiscoverOptions.Since` / `.Scope` / `.StaleAfter`; windowed branch in `evaluateCapabilities` emitting filter-first probes; state derivation from windowed count + `facts.streamRows` |
| `sdk/inventory/definitions.go` | `timeField` parse + `spans: start_time` in `BuiltinDefinitions` |
| `cmd/inventory.go` | `--since`, `--scope`, `--signals`, `--require`, `--stale-after`; mutual-requirement validation; signal-state table in `printInventoryHuman`; exit-code mapping |
| `docs/` | Onboarding-verification recipe; `ingest` → verify pairing |

**Tests**
- SDK: fixture-driven `Runner` covering each signal state, including the `empty` vs
  `no-data` split, the `n/a` applicability path, and the `live`/`stale` boundary at
  `--stale-after`; truncated probe → `unknown`; a missing `--scope` rejected; `spans` and
  `rum` use `start_time`.
- cmd: flag validation, exit-code mapping (0/10/11), human and `--agent` golden output.
- Integration (not run in CI, Grail-only tenant per repo convention): probe shapes execute and
  return the expected columns on `logs`, `spans`, `bizevents`, `metrics`.

---

## Implementation notes

| Area | What landed |
|---|---|
| `pkg/exec/dql.go` | Prerequisite fix: `FETCH_EXEC_TIME_LIMIT` → `notifTimeout`, plus an `"internal time limit"` message fallback. Re-exports `ColumnTypes`/`ColumnType` so result type metadata reaches the SDK; `PartialCause` exposes *which* limit truncated a result, where `ResultIsPartial` only said *that* one did |
| `sdk/inventory/inventory.go` | `SignalState` (7 states), `Signal`, `ArrivalWindow`, `StateSummary`; `TimeField` and `BackingBuckets` on `CapabilityDef` |
| `sdk/inventory/scope.go` | Scope-field lexing, `isNotNull` predicate assembly, metric-dimension applicability from result types, the two `n/a` evidence variants |
| `sdk/inventory/arrivals.go` | Windowed probing, state derivation, metric-family sampling, retention coverage through views, the applicability probes, scope-syntax-error detection, the all-zero tripwire |
| `sdk/inventory/discover.go` | `Since`/`Scope`/`StaleAfter`/`Signals` options; `RunResult.ColumnTypes`; view→bucket/table resolution from the catalog's `query_string` |
| `sdk/inventory/definitions.go` | `spans`/`rum` → `start_time`; Davis `backingBuckets`; `timeField` and `backingBuckets` validation |
| `cmd/inventory.go` | Parent command reduced to its original shape; shared discovery flags, definition loading, budget, and runner construction extracted for both commands |
| `sdk/inventory/runner.go` | The Runner author's half of the contract: `ColumnTypesOf`, `TruncationCauseOf`, `FirstTruncationCause`, `NormalizeSince`, `DefaultStaleAfter` |
| `sdk/inventory/require.go` | `CheckRequired`/`RequireVerdict`, `ValidateSignalNames`, and the exit-code contract |
| `cmd/inventory_signals.go` | The `arrivals` subcommand: flags, the signal table, rendering, result-specific agent suggestions — all decisions delegated to the SDK |
| `pkg/auth/resource_scopes.go` | `arrivals` → query scopes |

**Deferred:** `-S/--segment` composition (open question 5).

### Three defects the review found, all verified on a live tenant

- **`rum` had the same missing-`timeField` defect the design fixed for `spans`.**
  `describe user.events` lists `start_time`; `takeMax(timestamp)` over 1,005,336 records
  returns type `undefined`. RUM could therefore only ever report `live` with no age, and
  never `stale` — the one state this feature exists to surface. The other seven streams were
  each checked and do carry `timestamp`.
- **Retention coverage never resolved for views.** `facts.streamRows` is keyed by bucket
  *table*, but `dt.davis.problems`, `dt.davis.events`, and `dt.synthetic.events` are **views
  over `events`**, so the lookup always missed and those three permanently reported
  "retention coverage unknown" — losing the `empty` vs `no-data` split exactly where
  onboarding questions are asked. Fixed by resolving a view's buckets from the catalog's
  `query_string`, with `backingBuckets` as the declarative override for views that do not
  declare them.
- **The all-zero tripwire could not fire.** It counted `no-data` signals as "evaluated and
  not empty", so any tenant with one unused stream disarmed it. `no-data`, `absent`,
  `unknown`, and `n/a` are now all excluded from the evaluated set.

### Live verification

**Six tenants, five reachable** (one had no token in the keyring), spanning a Grail-only dev
tenant, three mid-size tenants, a demo tenant, and one very large production tenant. Two
scope shapes (`k8s.namespace.name`, `service.name`), plus a multi-field scope, a malformed
scope, an unknown signal name, and all four exit codes (0 / 10 not-live / 10 n/a / 11
unknown / 1 usage error).

On the mid-size tenants the applicability logic behaved consistently and reciprocally: under
`k8s.namespace.name`, RUM / synthetic / host / AWS metrics go `n/a` while the k8s families
stay live; under `service.name`, the k8s / process / host families go `n/a` while
service-metrics stays live. That reciprocity is the property worth checking, and it held
everywhere.

Same tenant, two namespaces, to price the extremes: a namespace with no data — the worst
case, since every signal triggers an applicability probe — cost 69 queries / 29.9 s;
a busy one, 43 queries / 20.6 s, with Davis now resolving retention through its buckets
(3,835,197,090 records).

**The multi-tenant sweep is what found the two defects above**, and neither was reachable
from a single tenant: the scan cap only binds at a volume the smaller tenants never reach,
and the overclaiming `n/a` wording only shows itself on a tenant where a stream that
normally carries a field happens not to during the window.

Earlier runs caught two defects no unit test would have: the metric-family false `empty`
described in D6, and timeseries bucket timestamps landing in the future (a bucket is stamped
with its end, so the current one is ahead of `now`; it is clamped).

### The SDK seam

`sdk/inventory` advertises itself as execution-agnostic — any DQL executor can
embed discovery through the `Runner` interface. That held for the retention-scoped
path and **did not hold for arrivals**: driving it from outside `cmd/` meant
reimplementing six helpers, two of which are correctness-critical and fail
silently.

`RunResult.ColumnTypes` is the sharp one. It is what separates `n/a` from
`empty` for a metric family, and a Runner that leaves it nil does not get an
error — it gets the wrong verdict. Measured on the `n/a` fixture, identical in
every respect except the omitted field:

| ColumnTypes | Verdict |
|---|---|
| populated | `n/a` — *"k8s.namespace.name is not a dimension of dt.host.*"* |
| omitted | `empty` — *"all 1 keys matching dt.host.\* exist but reported no datapoints for this scope"* |

The second line is the confident false-blame this whole feature exists to
delete, reintroduced by an embedder who simply did not know the obligation
existed. So the SDK now performs the translation instead of documenting it:

| Correctness | |
|---|---|
| `ColumnTypesOf` | flattens the API's per-index-range type blocks (a known type beats `undefined`) |
| `TruncationCauseOf` / `FirstTruncationCause` | classifies which limit cut a result short |

| Convenience | |
|---|---|
| `NormalizeSince` | `"15m"` → `"now()-15m"` plus the window length |
| `DefaultStaleAfter` | `max(2m, window/3)` |
| `ValidateSignalNames` | rejects typos and unprobeable requirements before the battery runs |
| `CheckRequired` → `RequireVerdict` | the gate decision, with `ExitCode()` and `Messages` |
| `ExitRequiredSignalNotLive` / `…Unknown` | the exit contract, 10 and 11 |

`cmd/` delegates to all of them and keeps only flag wiring, rendering, and agent
suggestions.

This also dissolved a seam the scan-cap work had to guard by hand: the cause
classification used to take a `pkg/exec` notification class and map it onto the
SDK enum by matching string values **across two Go modules**, with a test as the
only thing holding them together. `TruncationCauseOf` takes `query.Notification`
directly — the types now live in the same module, so there is nothing left to
drift.

**D18 — the Runner contract is discharged by helpers, not by documentation.** A
rule that must be followed to avoid a silent wrong answer is a design defect,
not a documentation gap. Where the obligation could not be removed outright, a
test pins the degradation so that if it ever becomes detectable, that is noticed
rather than absorbed.

### Smaller UX changes from the review

- `--where` → `--scope` (D2).
- `--signals` / `--require` names validated before the battery runs (D13).
- A scope that does not parse fails once, as a usage error, quoting the scope (D14).
- Gate exit codes moved to 10/11 so exit 1 stays cobra's (D15).
- `--require` on an unprobed signal is `unknown` (11), not "not live" (10).
- `--require` on an `n/a` signal says the gate can never pass, rather than blaming the source.
- The table's `RECORDS` header is now `VOLUME`: the column also carries metric datapoints
  ("391 pts"), which are not records.
- `--since` defaults to `15m` instead of being the mode switch.
- The `--scope true` escape hatch is documented in the long help, alongside the seven states.
- Truncation evidence names the limit that fired and the remedy for *that* limit, the scan
  cap is called out above the evidence block, and the cap in force is reported on the window.
- The `n/a` evidence no longer claims a structural absence for streams (D16).
