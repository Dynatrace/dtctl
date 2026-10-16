<!-- Migrated from the standalone docs site; SME to verify against the current dtctl binary. -->

# AI Agent Mode

dtctl provides first-class support for AI coding agents with a structured JSON output mode, automatic environment detection, and a machine-readable command catalog.

## Overview

The `--agent` (or `-A`) flag wraps all dtctl output in a structured JSON envelope:

```bash
dtctl get workflows --agent
```

This makes it straightforward for AI agents to parse responses, handle errors, and discover follow-up actions without scraping human-readable text.

## Response Format

### Successful responses

```json
{
  "ok": true,
  "result": [
    {
      "id": "wf-abc123",
      "name": "Daily Health Check",
      "state": "enabled"
    }
  ],
  "context": {
    "verb": "get",
    "resource": "workflow",
    "suggestions": [
      "dtctl describe workflow wf-abc123",
      "dtctl exec workflow wf-abc123"
    ]
  }
}
```

### Error responses

```json
{
  "ok": false,
  "error": {
    "code": "auth_required",
    "message": "No valid authentication found. Run 'dtctl auth login' or configure a token.",
    "suggestions": [
      "dtctl auth login --context my-env --environment https://abc12345.apps.dynatrace.com",
      "dtctl config set-credentials my-token --token <your-token>"
    ]
  }
}
```

Error codes are stable identifiers that agents can match on programmatically:

| Code | Meaning | What to do |
|---|---|---|
| `auth_required` | Not authenticated (HTTP 401) | Authenticate, then retry once |
| `permission_denied` | Authenticated but not allowed (HTTP 403) | Don't retry; report the missing permission |
| `insufficient_scope` | Token lacks the scopes this command needs | Don't retry; re-create the token with the `missing_scopes` in the envelope, or follow `suggestions` to an alternative the token can read |
| `not_found` | Resource does not exist (HTTP 404) | Verify the ID with `dtctl get <resource>` |
| `conflict` | Concurrent or duplicate change (HTTP 409) | Re-read the resource, re-apply on top |
| `bad_request` | The API rejected the request shape (HTTP 400) | Fix the payload; don't retry unchanged |
| `rate_limited` | Too many requests (HTTP 429) | Back off, then retry |
| `server_error` | Dynatrace-side failure (HTTP 5xx) | Retry with backoff; escalate if persistent |
| `timeout` | The operation timed out client-side | Narrow the request (timeframe, limit) and retry |
| `query_failed` | The query ended in state `FAILED` | Don't retry unchanged; fix the query |
| `query_cancelled` | The query was cancelled on the server | Retry if the cancellation was not intended |
| `result_expired` | The query result expired, was consumed, or was cancelled before it was fetched (HTTP 410) | Run the same query again |
| `unknown_query_state` | The query reported a state this dtctl does not know, or never left it before the poll deadline | Upgrade dtctl; re-run if the query was simply slow |
| `function_error` | The code you submitted to `exec function` failed | Fix the code; don't retry unchanged |
| `task_log_unavailable` | `logs wfe --tasks`/`--all` printed the logs it could fetch, but one or more task logs failed; the message names each task | Treat the printed logs as incomplete; retry a single task with `--task <name>` |
| `safety_blocked` | The context's [safety level](CONFIGURATION.md#safety-levels) forbids this operation | Don't retry; ask a human to widen the level |
| `profile_blocked` | The active [command profile](CONFIGURATION.md#command-profiles) doesn't expose this command | Re-read `dtctl commands` and pick a supported path |
| `stability_blocked` | The command or a flag you used offers a weaker contract than the context's [stability floor](STABILITY.md#choosing-what-this-environment-accepts) accepts | Don't retry; use a `stable` alternative, or ask a human to admit this entry |
| `deprecated_surface` | The command or flag is deprecated and this context refuses deprecated surface ([`DTCTL_NO_DEPRECATED`](STABILITY.md#finding-out-early-what-a-removal-will-break)) | Don't retry; migrate to the replacement named in `suggestions` |
| `development_disabled` | A `development`-tier feature that nobody enabled here | Don't retry; it needs `dtctl config set development.<feature> on` |
| `unsupported_in_service` | Host-only command, unavailable in server mode | Don't retry; the suggestion says why |
| `capability_disabled` | A host ability (plugin, alias, hook, editor, browser) isn't granted | Don't retry; use an in-process alternative |
| `hook_rejected` | A pre-apply hook rejected the resource | Fix the resource, or apply with `--no-hooks` |
| `validation_error` | Local input validation failed | Fix the file or flags |
| `unknown_command` | Unknown command, resource type or flag | Follow the suggestion (a runnable command where dtctl can name one); re-read `dtctl commands` |
| `context_error` | No active context, or the named context is missing | Select a context (`dtctl ctx <name>`) |
| `config_error` | The dtctl config could not be read or is invalid | Report it; needs human repair |
| `spill_file_not_found` | The spilled result file is gone | `dtctl inspect --list`, or re-run the query |
| `spill_file_unreadable` | The spill file exists but cannot be parsed | Re-run the query |
| `spill_file_wrong_context` | The spill file belongs to another context or tenant | Switch context, or re-query here |
| `inspect_unknown_field` | `--fields` named a column the file doesn't have | Use `dtctl inspect <path> --schema` |
| `inspect_bad_flags` | Incompatible `dtctl inspect` flags | Pick one row-access primitive per call |
| `api_index_unavailable` | The environment publishes no machine-readable API index | Stop probing for specifications; use the native commands |
| `api_spec_unavailable` | A listed API's specification could not be read | Don't retry; `dtctl get apis` still names the API |
| `error` | Unclassified failure | Read `message`; treat as non-retryable |

`dtctl query` additionally passes the DQL API's own error type through as the code
(lowercased), e.g. `unknown_data_object`. **Treat an unrecognised code as
`error`** -- read `message` and `suggestions` instead of branching on it.

The exception is a table the token cannot read: Grail's `NOT_AUTHORIZED_FOR_TABLE`
is reported as `insufficient_scope` (exit code 5), with `missing_scopes` naming
the storage scope when the error identifies the table. When the token's scopes
are introspectable (OAuth), `dtctl query` also checks the query *before* sending
it: a query that provably needs a storage scope the token lacks -- `fetch logs`
needs `storage:logs:read`, `smartscapeNodes`/`getNodeName()` need
`storage:smartscape:read` -- fails fast with the same code, without a request.
The check only blocks what it is certain of; with a platform or API token it
never blocks, and the query runs as usual.

When the DQL API reports where in the query the error is, the envelope also
carries `position` (1-based `line` and `column`, counted in characters, plus
`end_line`/`end_column` with the end inclusive) and `snippet` (the offending
line with a caret line under it). For a known authoring trap, the first
suggestion is the corrected query, ready to run:

```json
{
  "ok": false,
  "error": {
    "code": "parse_error",
    "message": "query failed (PARSE_ERROR): `by` isn't allowed here. ...",
    "status_code": 400,
    "position": {"line": 1, "column": 32, "end_line": 1, "end_column": 33},
    "snippet": "fetch logs | summarize count() by service.name\n                               ^^",
    "suggestions": [
      "group with the by: parameter, by:{field, …}, not a trailing by keyword: dtctl query 'fetch logs | summarize count(), by:{service.name}'"
    ]
  }
}
```

The traps with a rewrite: `=` instead of `==` in `filter`/`filterOut`; a
trailing `by field` instead of `by:{field}`; `name: aggregation(…)` instead of
`name = aggregation(…)`; `array.contains(arr, v)` instead of `in(v, arr)`; a
duration as `rollup:` instead of `interval:` on `timeseries`; a positional
condition on `timeseries` instead of `filter:`; and `fetch dt.entity.service` /
`dt.entity.host` where those tables are not data objects, instead of
`smartscapeNodes`. A non-metric aggregation on `timeseries` (e.g. `last()`) gets
advice rather than a rewrite, because the fix depends on whether the field is a
metric. A hint is only given when the pattern clearly matches; the suggested
command carries only the query, so re-add any flags you used.

### Exit codes

The process exit code is the same verdict at lower resolution, for shell callers
that do not parse the envelope:

| Exit | Meaning | Codes that produce it |
|---|---|---|
| `0` | Success | -- |
| `1` | General failure | every code not listed below |
| `2` | Usage error | `unknown_command`, `profile_blocked`, `stability_blocked`, `deprecated_surface`, `development_disabled`, `unsupported_in_service`, and `validation_error` for an empty flag value |
| `3` | Not authenticated (HTTP 401) | `auth_required` |
| `4` | Resource does not exist (HTTP 404) | `not_found` |
| `5` | Not allowed (HTTP 403) | `permission_denied`, `insufficient_scope` |

Branch on `code` wherever you can. An exit code cannot tell `safety_blocked`
apart from a network failure, and a new code arrives without a new exit code.

### Query results: the `result.kind` discriminator

In agent mode, `dtctl query` results are self-describing: the `result` payload
carries a `kind` field so a consumer always branches on one discriminator,
regardless of how big the result was. There are three kinds:

| `result.kind` | When | Payload |
|---|---|---|
| `records` | small result, returned inline | the rows under `result.records` |
| `result-file` | large result spilled to a file (see [Result Spill](CONFIGURATION.md#result-spill)) | a manifest: `path`, `format`, `rows`, `bytes`, column stats, `sample_rows` |
| `summary-only` | large result but the rows could not be written to disk | the same manifest **minus `path`** |

On a `result-file` result the rows are on disk, so read them with `dtctl inspect
<path>` (see [COMMANDS.md](COMMANDS.md)) -- `--head`/`--tail`/`--page`/
`--fields` for bounded row access, `--jq '<program>'` to keep only the matching
rows (a streaming filter over the whole file; a large match set re-spills via the
same `--spill*` guard), `--schema`/`--stats` to re-derive the profile, `--list`
to recover a path that has aged out of context -- instead of re-querying Grail.
On a `summary-only` result the rows are not on disk, so `context.suggestions`
carries the right next step for *why* the spill degraded: a read-only filesystem
steers you to re-query with `--spill=never` and a bound (`| fields …` /
`| limit N`, or `--max-result-records N`) so the inline result stays small, while
a one-off write failure suggests retrying with an explicit `--spill-to <path>`.

```json
{
  "ok": true,
  "envelope_version": 1,
  "result": {
    "kind": "result-file",
    "path": "~/Library/Caches/dtctl/results/prod/q-7f3a9c.jsonl",
    "format": "jsonl",
    "rows": 84213,
    "columns": [ { "name": "status", "type": "long", "nulls": 0, "min": 500, "max": 599 } ],
    "sample_rows": [ /* first few rows */ ]
  },
  "context": {
    "verb": "query", "resource": "logs", "total": 84213,
    "decided": "spilled", "threshold_bytes": 51200, "measured_bytes": 16804000
  }
}
```

`measured_bytes` is the size the rows would have taken inline, in
`measured_encoding` and laid out as this invocation prints them: compact JSON
when stdout is piped or redirected (the agent case), indented at their depth in
the envelope on a terminal. For `csv`, `yaml` and `toon`, which the envelope
carries as a JSON string, it is the encoded text before string escaping.
`--spill=auto` compares it with `threshold_bytes` (`--spill-threshold`).

A large enough result is written to that file row by row as it arrives, without
ever being held whole. Those envelopes carry `"streamed": true` in `context` and
no `measured_bytes`/`measured_encoding`: nothing was serialised for inline
emission, so the spill decision was settled by row count alone. Everything else —
`rows`, `columns`, `sample_rows`, `decided` — reads the same either way. This
applies whatever the display format (`-o json`, `toon`, `csv`, ...), because a
result that spills is never rendered in it. It needs a JSONL spill file (the
default `--spill-format`); a `csv`, `json` or `parquet` spill file, `--jq`,
`--typed` and `--decode-snapshots` keep the buffered path. Results small enough to be
emitted inline are unchanged.

The envelope carries `envelope_version` for forward compatibility. **A consumer
MUST treat an unrecognised `result.kind` as opaque** -- don't parse `result`, fall
back to the human-readable `context` (which always carries `decided`, `total`,
`warnings`, and `suggestions`). When Grail sampled the result, the per-column
stats move into a `sample_stats` block (each column tagged `basis: "sample"`) so
sample-based figures can't be misread as population truth.

> The inline `kind: "records"` envelope is emitted on the spill-aware path
> whenever agent mode emits JSON -- including under `--spill=never`, which forces
> every row inline regardless of size but still as a `kind: "records"` envelope
> (never a human table). The agent-mode default `-o auto` also keeps this
> envelope (see below). Explicit non-JSON output (`-o toon/csv/yaml`) and `--jq`
> transforms keep their requested shape and fall through to the plain
> `{ "records": …, "metadata": … }` output.

Timeseries results are the most token-expensive shape: every series is a
full-precision array. In agent mode `query` therefore defaults to
`--series=summary --precision 4`: each series is replaced by its statistics
and a sparkline (typically ~9x fewer tokens), and every number is rounded to 4
significant digits. When that changed the result, `context.suggestions` names
the opt-out: `--series=full --precision 0` restores the raw values exactly.
`--series=downsample:N` keeps at most N extreme-preserving points instead.
Summaries are nested objects, so with no `-o` the `-o auto` default encodes
them as YAML; add `-o json` for native JSON. See
[Output Formats](OUTPUT_FORMATS.md#compact-timeseries---series---precision).

### Choosing the encoding with `-o auto`

No single encoding is the cheapest for every result: CSV wins on flat rows,
while nested documents come out smaller as YAML or JSON than as TOON. `-o auto`
lets dtctl choose per result (the rules are listed in
[OUTPUT_FORMATS.md](OUTPUT_FORMATS.md#auto--o-auto) and are experimental).

**`dtctl query` uses `-o auto` by default in agent mode** when no `-o` is given.
Pass `-o json` to get the previous native-JSON rows back; any other explicit
`-o` also wins over the default. Other commands keep native JSON unless you pass
`-o auto` yourself. When the default returns CSV or YAML, `context.suggestions`
carries one entry naming the `-o json` opt-out; when it returns native JSON
(empty or scalar results), nothing is added. With `--jq` and no `-o`, the
filter's output is JSON: a program that addresses `.records` reads its answer
as JSON, so the default does not re-encode it as CSV.

The envelope names the choice in `context.format`, so branch on it before parsing:

| `context.format` | `result` (or `result.records` for `dtctl query`) |
|---|---|
| `csv` | a CSV string with a header row |
| `yaml` | a YAML string |
| `json` | a native JSON value (used for empty and scalar results) |

```json
{
  "ok": true,
  "envelope_version": 1,
  "result": { "kind": "records", "encoding": "csv", "records": "count(),loglevel\n1204,ERROR\n88311,INFO\n" },
  "context": { "verb": "query", "resource": "logs", "total": 2, "format": "csv", "decided": "inline" }
}
```

For `dtctl query`, `-o auto` keeps the `kind: "records"` envelope below the
spill threshold (unlike an explicit `-o csv`/`-o yaml`, which print raw bytes)
and the threshold is measured in the chosen encoding. A spilled result is a
`result-file` manifest as usual and carries no `context.format`.

### Query window: `context.window`

`dtctl query` reports the window the query searched, as the response's own
metadata states it, with its length in `span`. A query that names no window
(no `from:`/`to:`/`timeframe:`, no `--default-timeframe-start`/`-end`) reads the default last 2h,
and `note` says so, because a count over 2h reads exactly like a count over
24h:

```json
"window": {"from": "2026-01-01T08:00:00Z", "to": "2026-01-01T10:00:00Z", "span": "2h",
           "note": "the query names no window, so it read the default last 2h; widen it with fetch ..., from: now()-24h"}
```

### Result notes: samples and durations

Two suggestions describe what a non-empty result is *not*:

- an unsorted `| limit N` that returned exactly N rows: these are the first
  records read, not a representative sample, so they cannot show how often
  something occurs or that it never does;
- columns Grail types as `duration`: their values are nanoseconds, and when
  the query does not aggregate, one record's duration is not a typical one
  (`percentile(<col>, 50)` is).

### Empty query results: `context.empty_reason`

A misspelled field name or metric key makes DQL succeed with zero rows. On an
empty result (no rows, or the single all-zero row of a `summarize count()`),
dtctl runs one small, bounded probe before it suggests widening the time window:

| Query shape | Probe | Finding |
|---|---|---|
| `fetch <object> \| filter …` / `summarize … by:` | the query's own `fetch` stage with `\| limit 100`, and the field names in its `filter`/`filterOut` stages and `by:` clause compared against the sampled records | `field_not_in_sample` |
| `timeseries …` | the metric keys that reported series in the query window (at most its last 2h), listed with the `metrics` command | `metric_not_in_window` |

The probes are capped (1 GB scan, 10 s read time, bounded result size), run only
on an empty result that no scan, time, result or consumption limit cut short,
and never fail the query. If a probe errors, comes back
partial, or finds an empty sample, the envelope keeps the widen-the-window advice
and has no `empty_reason`.

`context.empty_reason` is set only when a missing name has a close match that
*was* observed, which is what a typo looks like. That advice replaces the
widen-the-window suggestion, because a wider window cannot fix a misspelled
name:

```json
{
  "ok": true,
  "result": { "kind": "records", "records": null },
  "context": {
    "total": 0,
    "empty_reason": {
      "code": "field_not_in_sample",
      "field": "servce.name",
      "data_object": "logs",
      "did_you_mean": ["service.name"],
      "sample_size": 100,
      "evidence": "`servce.name` is absent from all 100 sampled `logs` records (the query's fetch stage with `| limit 100`); the near match is present in the sample"
    },
    "suggestions": ["# `servce.name` did not occur in any of the 100 sampled `logs` records, but `service.name` did — likely a typo in the field name; …"]
  }
}
```

A finding is an observation about a sample or a window, not a catalog fact.
`evidence` names that basis. A field can be rare enough to be missing from a
100-record sample. A missing name with no close match therefore only adds a
hedged note to `suggestions`, and the widen-the-window advice stays.

### Query metadata

In agent mode `dtctl query` adds the Grail query metadata as a top-level
`metadata` key next to `result` and `context`. Agent mode defaults to
`--metadata=minimal`, which keeps only what an agent acts on. The full block
repeats the query text as `query` and `canonicalQuery` and adds `locale`,
`timezone`, `dqlVersion` and `queryId`, and on small results it can be larger
than the rows. Pass `-M=all` (or bare `-M`) to get it back exactly as before.
Outside agent mode metadata stays off unless `-M` is given.

The minimal set:

| Field | Included |
|---|---|
| `executionTimeMilliseconds` | always |
| `scannedBytes`, `scannedDataPoints` | when non-zero |
| `sampled` | only when `true` (the result is approximate) |
| `approximations` | only when Grail reported any (the result is approximate) |
| `analysisTimeframe` | only when the query named no window, so the server picked the default one |
| `contributions` | when requested with `--include-contributions` |

It also drops the spill measurement details (`threshold_bytes`, `measured_bytes`,
`measured_encoding`) from `context` on an inline result; they stay on a spilled or
summary-only result, where they explain the decision, and come back with `-v`.
A streamed result (`context.streamed`, see above) carries no `measured_bytes` or
`measured_encoding` at all, because nothing was serialised to measure.
`context.decided` is always present. Each of Grail's `approximations` is also a
`context.warnings` entry (`Result is approximate: …`), next to the WARNING and
ERROR notifications; the full block (`-M=all`) adds `notifications` of every
severity, INFO included. When the default dropped something,
`context.suggestions` carries one line naming `-M=all`; an explicit
`-M=minimal` does not. Add field names to opt back into more,
e.g. `--metadata=minimal,queryId`; `--metadata=` (empty) turns metadata off.

```bash
dtctl query 'fetch logs | summarize c=count(), by:{loglevel}' -A
```

```json
{
  "ok": true,
  "envelope_version": 1,
  "result": { "kind": "records", "records": [ { "c": "26", "loglevel": "ERROR" } ] },
  "context": {
    "verb": "query", "resource": "logs", "total": 1, "decided": "inline",
    "suggestions": [ "# metadata trimmed by default; -M=all for the full block" ]
  },
  "metadata": {
    "executionTimeMilliseconds": 132,
    "scannedBytes": 623373940,
    "analysisTimeframe": { "start": "2026-01-02T01:00:00Z", "end": "2026-01-02T03:00:00Z" }
  }
}
```

### Column types: `--include-types`

`dtctl query --include-types` puts the DQL per-column type block in the
envelope as `result.types`, next to the rows. It keeps the API's shape
(`indexRange` + `mappings`, see [Output Formats](OUTPUT_FORMATS.md#column-types---include-types))
and is the same on every result kind: inline `records` (whatever `-o auto`
picked, since it stays native JSON like `constant`), `result-file` and
`summary-only`. It describes the full result, so it still names a column that
compaction moved into `constant` or dropped as all-null, and its `indexRange`
indexes the full result when `--max-output-bytes` cut the rows. The block
counts toward the spill threshold and the output budget. Without an explicit
`--include-types` there is no `types` key; `--typed` requests the types only to
cast values.

### Compacted rows: `constant`

Agent mode compacts query rows by default (`--compact`, experimental): null
values are omitted, and every column that holds the same value in every row is
printed once, in a `constant` map that comes **before** `records`. Log and
span rows are dominated by resource attributes shared across the whole result,
so this is usually the larger part of the payload. Shown here with `-o json`:

```json
{
  "ok": true,
  "envelope_version": 1,
  "result": {
    "kind": "records",
    "constant": { "k8s.cluster.name": "prod-eu", "service.name": "payment" },
    "records": [ { "timestamp": "…", "span.name": "POST /pay", "duration": "812000" } ]
  }
}
```

**Reading it:** a row is `constant` merged with its entry in `records`; a key
absent from both is null. `result.kind` stays `records`, `constant` is omitted
when nothing is shared, and `context.total` still counts the rows.

- It applies to the JSON envelope and to `-o toon`, where `constant` stays a
  JSON map next to the encoded `records` string. TOON keeps a null in a column
  that has values in other rows, so the rows still encode as one table; only
  all-null columns are dropped there. `-o csv` and `--jq` get the
  full rows (a `--jq` program always sees uncompacted records).
- Under `-o auto`, which is also the agent-mode default for `query`, both
  defaults apply together: the encoding is chosen from the compacted rows
  (constant and all-null columns already removed), and `constant` stays a JSON map next to
  the encoded `records` whichever encoding was picked. When auto picks `csv`,
  partial nulls stay as empty cells so the rows remain one table; when it picks
  `yaml`, nulls are dropped as in JSON.
- `constant` needs at least two rows; a single row only loses its nulls.
- The spill decision measures the compacted payload, since that is what reaches
  the agent.
- On a `result-file` / `summary-only` manifest the same columns collapse out of
  the per-column profile: single-value columns go into `constant`, all-null
  columns are listed by name in `null_columns`, and `sample_rows` drop both.
  The spilled file and its sidecar manifest keep every row and column in full.
- When compaction changed the result, `context.suggestions` says so once and
  names the opt-out; when there was nothing to compact, it adds nothing.
- **Opt out with `--compact=false`**, which restores the full rows (and the
  full per-column profile in a manifest) exactly. Outside agent mode,
  `--compact` opts in for plain `-o json`/`yaml`/`toon`.

### Bounding inline query output: `--max-field-chars` and `--max-output-bytes`

A row limit does not bound size: twenty log lines can each carry a
multi-kilobyte `content` value or stack trace. Two flags bound what an inline
`kind: "records"` result carries, both in agent mode only (outside it the
output is unchanged and the flags warn). The value cap is on by default in
agent mode, as part of the agent-mode token-optimal defaults; the budget is
off unless you set it:

- **`--max-field-chars N`** (default **500** in agent mode) clips every string
  value longer than N characters. `--max-field-chars 0` turns the cap off and
  restores the full values exactly. A clipped value keeps its
  first N characters and ends in `…(+3214 chars)`, the same marker the spill
  summary uses. A spilled file always keeps the full values, and so does a
  `--jq` filter's input: the values are clipped after the filter ran, so it
  still matches on them. Under `-o auto` (the agent-mode default for `query`),
  values are clipped first, so auto chooses and encodes the clipped rows.
  `-o json --max-field-chars 0` restores native, unclipped rows.
- **`--max-output-bytes SIZE`** (e.g. `16KB`) or **`--max-output-tokens N`**
  (approximate: 1 token ≈ 4 bytes) is a budget on the envelope exactly as it
  is printed: compact or indented, JSON, TOON or the `-o auto` choice, with
  `context` and `metadata` included. A result over the budget keeps the
  leading rows that fit, in order. With `-o auto` the kept rows are encoded in
  the format auto picks for them, and `context.format` names it. When spilling is enabled (the agent-mode default) the full result is
  also written to disk and `context.next` is the `dtctl inspect` command that
  continues where the rows stop, with no Grail re-query. The budget covers the
  gap between "fits inline" and the spill threshold; a result above the
  threshold still spills. It is not applied to `--jq` output, which has no
  rows to cut; the envelope warns when you set both.

A bounded result says so in `context`. `truncated: true` means the result is
not the complete answer:

| `context` field | Set when | Meaning |
|---|---|---|
| `truncated` | any bound applied | the result was cut: rows, values, or both |
| `max_field_chars`, `truncated_fields` | values were clipped | the cap, and which fields had a clipped value |
| `total`, `returned` | rows were dropped | rows in the full result, rows in `result.records` |
| `next_offset` | rows were dropped | index of the first row not returned |
| `next` | rows were dropped and the result was written to disk | the command that continues at `next_offset` |
| `budget_bytes` | rows were dropped | the budget that dropped them, in bytes |

Both bounds apply after compaction (see above). A column goes into `constant`
only when its full values are equal in every row; the values in `constant` are
then clipped like the ones in `records`, and the budget counts `constant`
toward the envelope size.
Timeseries summaries (`--series=summary`, the agent-mode default) are built
before either bound. Their only strings are a sparkline of at most 24 cells and
timestamps, so the default cap leaves a summary whole.

`context.suggestions` says how to get the rest: `--max-field-chars 0` with
`| fields <col>` for full values (only when a value was actually clipped), or `--spill-to <file>` when nothing was
written to disk.

```json
{
  "ok": true,
  "envelope_version": 1,
  "result": { "kind": "records", "records": [ { "content": "java.lang.IllegalStateException: …(+3214 chars)" } ] },
  "context": {
    "verb": "query", "total": 200, "decided": "inline",
    "truncated": true, "returned": 37, "next_offset": 37, "budget_bytes": 16000,
    "next": "dtctl inspect ~/.cache/dtctl/results/prod/q-7f3a9c12.jsonl --page --offset 37 --limit 37",
    "max_field_chars": 500, "truncated_fields": ["content"]
  }
}
```

## Auto-Detection

dtctl automatically enables agent mode when it detects it is running inside a known AI agent environment. Detection is based on environment variables the agent sets on the commands it runs. Variables are checked in the order below and the first match wins, so the reported agent name is stable even when several are set (Claude Code, for example, sets both `CLAUDECODE` and `AI_AGENT`).

| Environment Variable | Agent |
|---|---|
| `AI_AGENT` naming a known agent (e.g. `claude-code_2-1-282_agent`) | that agent |
| `AGENT=amp` | Amp (also sets `CLAUDECODE`) |
| `CLAUDECODE` | Claude Code |
| `CODEX_CI`, `CODEX_THREAD_ID`, `CODEX_SANDBOX` | OpenAI Codex CLI |
| `CURSOR_AGENT` | Cursor |
| `COPILOT_CLI`, `COPILOT_AGENT` | GitHub Copilot (CLI, VS Code) |
| `GEMINI_CLI` | Gemini CLI |
| `QWEN_CODE` | Qwen Code |
| `OPENCODE` | OpenCode |
| `AGENT_CONTEXT_OUT`, `KIRO_SESSION_ID` | Kiro |
| `OPENCLAW_SHELL=exec` | OpenClaw |
| `TABNINE_CLI` | Tabnine CLI |
| `AWS_EXECUTION_ENV` containing `AmazonQ-For-CLI` | Amazon Q Developer CLI |
| `AUGMENT_AGENT` | Augment |
| `ANTIGRAVITY_AGENT` | Antigravity |
| `CLINE_ACTIVE` | Cline |
| `AI_AGENT` with any other value | reported as `generic-ai` |

`CODEX`, `GITHUB_COPILOT`, `KIRO`, `OPENCLAW`, `JUNIE`, `CODEIUM_AGENT`, `TABNINE_AGENT` and `AMAZON_Q` are also honoured as manual overrides (no agent is known to set them), after every variable above. A value of `0` or `false` does not count.

Variables an IDE sets in *every* integrated terminal (such as `CURSOR_TRACE_ID` or `TERM_PROGRAM`) are deliberately ignored: they would switch a human's output to JSON.

When auto-detected, agent mode is enabled without requiring the `--agent` flag.

To give a hosted agent sandbox (such as Claude Code on the web) a working dtctl,
see [Cloud agent sandboxes](CONFIGURATION.md#cloud-agent-sandboxes-claude-code-on-the-web).

### Opting out

To disable auto-detection and get normal human-readable output:

```bash
dtctl get workflows --no-agent
```

## Behavior

Agent mode implies `--plain`:

- No ANSI colors in output
- No interactive prompts (e.g. name disambiguation)
- No progress spinners or animations

This ensures output is always machine-parseable.

A few consequences worth knowing when you parse the output:

- **Command-line mistakes are enveloped too.** An unknown command, resource type
  or flag produces `{"ok": false, "error": {"code": "unknown_command", ...}}` on
  stdout and exits with the usage code (2) -- also when agent mode was
  auto-detected rather than requested with `-A`. Where dtctl can name the command
  you most likely meant, `suggestions` carries it as a line that runs as-is: a
  `get` with the misspelled resource `dashbords` suggests `dtctl get dashboards`.
- **Query notifications live in the envelope, not on stderr.** Result-limit,
  scan-limit and timeout notices are in `context.warnings`, with the advice in
  `context.suggestions`. dtctl writes them to stderr only when there is no
  envelope to carry them (`-o csv`, `-o yaml`, charts). When a result limit cuts
  a `summarize ..., by:{...}` that has no `sort`, the suggestions say so: the
  groups kept are arbitrary, so rank before the cut (`| sort <agg> desc | limit N`).

## Stability of agent-mode output

Agent-mode output is shaped for agents, and it keeps being tuned for them. What
the envelope carries (which fields and how many items a default returns, how
rows are encoded, how numbers are rounded) may change in any minor release, on
`stable` commands too. An agent reads every response afresh, so it adapts, and
the release notes do not treat such a change as breaking. See [what `stable` promises before
1.0](STABILITY.md#what-stable-promises-before-10).

What does not change without a deprecation cycle:

- **The envelope skeleton.** `ok`, `error.code`, `result.kind` and the `context`
  keys keep their names, types and meanings.
- **Explicit flags.** A flag you pass keeps its meaning. If a program parses the
  output instead of a model reading it, pin the shape with flags (for example
  `-o json`) rather than relying on agent-mode defaults.
- **Self-description.** When a default drops, clips, rounds, pages or re-encodes
  data, the envelope says so, in `context.format`, `context.has_more`, or a
  `context.suggestions` entry naming the flag that restores the full data.

Output without agent mode is not affected by any of this.

## Command Catalog

AI agents can bootstrap their knowledge of dtctl using the built-in command catalog:

```bash
# Minimal overview -- verbs, resources, and subcommands only (defaults to TOON)
dtctl commands

# Brief catalog -- adds mutating status, access levels, flag types, and scopes
dtctl commands --brief -o json

# Full catalog -- detailed command descriptions, flag defaults, and global flags
dtctl commands --full -o json

# Human-readable how-to guide in Markdown
dtctl commands howto
```

The bare `dtctl commands` overview is ideal for including in an agent's system prompt or initial context, giving it a complete map of available operations without consuming excessive tokens; step up to `--brief` or `--full` when more detail is needed.

## Environment Inventory

`dtctl inventory` probes the current context's environment and reports what data
actually exists there. Where `dtctl commands` (above) answers *"what can I
run?"*, `dtctl inventory` answers *"what is there to query?"* -- which makes it
the natural first call before exploratory DQL, for humans and AI agents alike.

Discovery is **read-only and budgeted**: it runs a small battery of DQL queries (4 by default) and stops with a partial inventory rather than overrunning its budget. Nothing is persisted.

```bash
# The environment inventory for the current context
dtctl inventory

# Structured output (full lists, no compaction)
dtctl inventory -o json
dtctl inventory -o yaml
```

### What it reports

- **Data objects** -- which Grail catalog objects are fetchable, and which are query-only (`metrics`, `smartscape.*`) so you aren't baited into `fetch` calls that cannot work. The hundreds of legacy `dt.entity.*` lookback views are collapsed to an `entityViews` count.
- **Buckets** and **filter segments** -- segments can be applied to queries with `-S <name>`.
- **Live entity-type census** -- entity type → count via `smartscapeNodes`, the current-state topology (not the `dt.entity.*` lookback views, which diverge from it).
- **Capabilities** -- spans, logs, RUM, k8s, cloud integrations, metric families, and anything you define yourself: each reported as **present**, **absent**, or **unknown**.

Example (synthetic):

```
Context:      example
Generated:    2026-01-02T03:04:05Z
Capabilities: hosts, logs, spans
Absent (what was checked)
  rum — user.events is in the catalog, but all its buckets are empty (0 records within retention)
Unknown (no verdict — not evidence of absence)
  genai — probe failed: scan limit exceeded
Entities:     K8S_POD:200 SERVICE:40 HOST:12
Data objects: logs, spans (+2 dt.entity.* lookback views)
Query-only:   metrics (no fetch — see notes)
Buckets:      default_logs, default_spans
Segments (apply with -S <name>)
  prod — production workloads
```

### Verdicts carry evidence

Every **absent** capability cites exactly what was checked (`no K8S_* entities in the live census`, `user.events is in the catalog, but all its buckets are empty`), so a negative finding is citable without anyone re-deriving it with fresh probes.

A capability whose check could not run -- a failed probe, an exhausted budget, an unavailable or truncated fact source -- is reported as **unknown** with the reason, never as absent. Unknown must not be read as absent: the capability may still exist.

Stream capabilities get liveness checking for free: bucket statistics (already fetched for the bucket list) reveal when a catalog object holds zero records within retention, so a tenant that never ingested RUM reports `rum` as absent even though `user.events` sits in its catalog.

### Customizing the capability set

dtctl ships a built-in, structural-only capability set (topology, signal streams, metric families). Your own definitions merge over it:

```bash
# Merge org-specific definitions over the built-in set (repeatable, later files win)
dtctl inventory --definitions ./our-capabilities.yaml

# Only your definitions, without the built-in set
dtctl inventory --definitions ./our-capabilities.yaml --no-builtin-definitions
```

Each capability is defined by *how* it is discovered -- exactly one of four fixed shapes, deliberately not an expression language:

| Shape | Present when | Evidence strength | Extra cost |
|-------|--------------|-------------------|------------|
| `dataObject` | the object is in the catalog and its buckets hold data | strong | none |
| `entityTypes` (globs) | a matching type has live entities in the census | strong | none |
| `metricKey` (glob) | a matching key is in the live metric catalog | strong | none (shared query) |
| `probe` + `window` | the DQL probe returns at least one row | **weak negative** | 1 query per run |

```yaml
apiVersion: dtctl.dev/v1alpha1
kind: InventoryDefinitions
capabilities:
  # Managed postgres appears under hyperscaler entity types, not the generic one
  postgres:
    entityTypes: [DB_INSTANCE_POSTGRES, "*_DBFORPOSTGRESQL_*"]

  # Only visible in span attributes: probe with a capped, sampled scan
  genai:
    probe: 'fetch spans, from:now()-24h, samplingRatio:100 | filter isNotNull(gen_ai.system) | limit 1'
    window: 24h

  # Remove a built-in capability from the merged set
  aws-cloudwatch: null
```

Globs are case-sensitive: Smartscape entity types are UPPERCASE (`K8S_*`, `AZURE_*`), so a lowercase pattern silently never matches. Probe shapes must declare `window` -- the evidence window the probe covers -- because their negatives are weak: absence of events in a window is not absence of the capability.

See [docs/dev/examples/inventory-definitions.example.yaml](dev/examples/inventory-definitions.example.yaml) for the annotated format.

### Budgets and cost

Discovery is bounded on three axes; it stops with a partial inventory (and says so) rather than overrunning:

```bash
dtctl inventory --budget-queries 100     # max queries (default 100)
dtctl inventory --budget-seconds 300     # max cumulative query seconds (default 300)
dtctl inventory --scan-limit-gbytes 25   # scan cap applied to every probe (default 25)
```

The default battery is 4 queries: data-object catalog, buckets, entity census, and the metric catalog (only when a `metricKey` definition needs it, and filtered to the globs those definitions actually use -- a `metricKey` pattern with `?` or `[a-z]` character classes cannot be pushed into the query, so it falls back to reading the whole catalog). Probe-shaped definitions cost one query each. A consumption receipt (`discovery: {queries, seconds}`) is attached to every inventory.

`--budget-seconds` is a hard bound, not a running tally: a query is issued with the budget still unspent as its deadline, so a single slow query is cut off at the budget instead of overrunning it. Whatever it would have answered degrades to `unknown` with its evidence, exactly like any other skipped check. Callers running `inventory` under an outer timeout should still leave headroom -- client setup and the segment fetch happen before the first query and are not part of the query budget.

### For AI agents

In agent mode (auto-detected, see [Auto-Detection](#auto-detection) above), the inventory arrives in the structured JSON envelope with suggestions attached -- sample a listed data object, cite absence evidence instead of re-probing. Absent and unknown capabilities are structured `{name, evidence}` pairs:

```json
"absent": [
  {"name": "rum", "evidence": "user.events is in the catalog, but all its buckets are empty (0 records within retention)"}
]
```

This is about the data in the environment, not the resources you manage -- for dashboards, workflows, SLOs, and the rest, use `dtctl get <resource>`.

## Tips and Tricks

### Keep list output small

In agent mode a `get` list returns a page of at most **50** items by
default. When the page cuts the list, `context.total` holds the full count,
`context.has_more` is `true`, and a suggestion names the opt-out: `--limit 0`
returns every item, a larger `--limit` returns more. Trim the items themselves
with `--fields` *(experimental)*:

```bash
dtctl get dashboards -A --fields id,name,owner,modificationInfo.lastModifiedTime
dtctl get dashboards -A --limit 0     # every dashboard
```

With `-o toon`, the projected fields are flattened into columns so the result stays a single TOON table. See
[Output Formats](OUTPUT_FORMATS.md#trimming-lists---limit---fields).

### Name resolution

When agent mode is active, interactive name disambiguation is disabled. Use exact IDs instead of display names to avoid ambiguity:

```bash
# Prefer IDs in agent mode
dtctl describe workflow wf-abc123

# Names may fail if multiple resources share the same name
dtctl describe workflow "Daily Health Check"
```

All `describe` subcommands support agent mode, returning the full resource object in the JSON envelope:

```bash
dtctl describe workflow wf-abc123 --agent
dtctl describe slo my-slo -A
dtctl describe dashboard my-dash -o json -A
```

### Dry-run

Use `--dry-run` to preview mutating operations without making changes:

```bash
dtctl apply -f workflow.yaml --dry-run
```

`--dry-run` is not a global flag: each command that can preview its own mutation declares it. Most `apply`, `create`, `update`, `delete`, `restore`, `share`/`unshare` and `enable`/`disable` commands do, as does `exec api`; `dtctl <verb> <resource> --help` is the authoritative list. A command that has no dry run, such as `exec workflow` or `query`, rejects the flag with a usage error (exit code 2) and does nothing — for a DQL statement, `dtctl verify query` is the check-without-running command.

### Diff

Use `--show-diff` to see what would change when updating an existing resource:

```bash
dtctl apply -f workflow.yaml --show-diff
```

### Verbose output

Use `-v` or `--verbose` for additional debugging information:

```bash
dtctl get workflows -v --agent
```

### Environment variables

Point dtctl at a prepared config and supply the token out of band, so an agent
never runs an interactive login:

```bash
export DTCTL_CONFIG="$PWD/.dtctl.yaml"   # explicit config file (also trusts its hooks)
export DTCTL_CONTEXT="prod"              # pick a context for this invocation
export DTCTL_TOKEN="dt0s16.XXXXXXXX.YYYYYYYY"
dtctl get workflows --agent
```

The environment URL comes from the context, not from the environment. There is
no `DTCTL_ENVIRONMENT`; create the context once with `dtctl config set-context`
(see [Configuration](CONFIGURATION.md)) and select it with `DTCTL_CONTEXT`.

### Pipeline commands

Chain dtctl commands with standard Unix tools:

```bash
# Get all workflow IDs, then describe each one
dtctl get workflows -o json --agent | jq -r '.result[].id' | xargs -I{} dtctl describe workflow {} --agent

# Export query results for processing
dtctl query 'fetch logs | filter status == "ERROR" | limit 10' -o json --agent | jq '.result'
```

## Implementation notes (for contributors)

- Agent envelope: `pkg/output/agent.go` (`AgentPrinter`, `Response`, `PrintError`)
- Per-command context enrichment: the `enrichAgent()` helper in `cmd/root.go`
- Environment auto-detection: `sdk/agentmode/`, wired into `pkg/aidetect/detect.go`
- Environment inventory discovery: `sdk/inventory/`

When adding support for a new AI agent environment variable, update **all** of:
code (`pkg/aidetect/detect.go`, `pkg/skills/installer.go`, `cmd/skills.go`),
tests (`pkg/aidetect/detect_test.go`, `pkg/skills/installer_test.go`,
`cmd/skills_test.go`), and docs (`README.md`, this file). See `AGENTS.md` in the
repo root for the full contributor checklist.
