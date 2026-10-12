<!-- Migrated from the standalone docs site; SME to verify against the current dtctl binary. -->

# Output Formats

dtctl supports multiple output formats to suit different workflows, from
human-readable tables for interactive use to structured JSON for scripting and
AI agents.

## Table (Default)

The default output format is a compact, human-readable table:

```bash
dtctl get workflows
```

```
ID            NAME                     STATE    TRIGGER     LAST RUN
wf-abc123     Daily Health Check       enabled  Schedule    2025-01-15 08:00
wf-def456     Incident Remediation     enabled  Event       2025-01-15 12:34
wf-ghi789     Weekly Report            enabled  Schedule    2025-01-13 06:00
```

## JSON

Output as JSON for scripting and piping to tools like `jq`:

```bash
dtctl get workflow wf-123 -o json

# Pipe to jq for field extraction
dtctl get workflows -o json | jq '.[].name'
```

## YAML

Output as YAML, useful for round-tripping with `dtctl apply`:

```bash
dtctl get workflow wf-123 -o yaml
```

## TOON

Output as [TOON](https://github.com/toon-format/toon) (Token-Oriented Object Notation), a compact format for LLM token efficiency:

```bash
dtctl query 'fetch logs | limit 10' -o toon
```

TOON can only escape `\\`, `\"`, `\n`, `\r` and `\t`. Any other control character in a key or value (for example the ESC of an ANSI color sequence in log content) is shown as its Unicode Control Picture (`U+001B` becomes `␛`), so one such value never fails the whole output. Use `-o json` for byte-exact values.

## Wide

The wide format adds additional columns that are hidden in the default table view:

```bash
dtctl get workflows -o wide
```

```
ID            NAME                     STATE    TRIGGER     OWNER            LAST RUN            LAST STATUS
wf-abc123     Daily Health Check       enabled  Schedule    user@example.com 2025-01-15 08:00    SUCCESS
wf-def456     Incident Remediation     enabled  Event       user@example.com 2025-01-15 12:34    FAILED
```

## Describe

The `describe` command renders a vertical key-value view with full detail by default:

```bash
dtctl describe workflow wf-123
```

```
ID:          wf-abc123
Name:        Daily Health Check
State:       enabled
Trigger:     Schedule (0 8 * * *)
Owner:       user@example.com
Created:     2025-01-01 10:00:00
Modified:    2025-01-14 15:30:00
Tasks:       3
```

All `describe` subcommands support the `-o` / `--output` flag to get structured output:

```bash
# JSON output for scripting
dtctl describe workflow wf-123 -o json

# YAML output for round-tripping
dtctl describe slo my-slo -o yaml

# Agent mode envelope
dtctl describe dashboard my-dash -A
```

## CSV

Export as CSV for spreadsheets and data pipelines:

```bash
# Export workflows to a CSV file
dtctl get workflows -o csv > workflows.csv

# Export DQL query results as CSV
dtctl query 'fetch logs | filter status == "ERROR" | limit 100' -o csv > errors.csv
```

## Auto (`-o auto`)

`-o auto` picks the encoding from the shape of the result, preferring the
cheapest *lossless* one for an LLM to read. It is the default for `dtctl query`
in agent mode when no `-o` is given (pass `-o json` for native JSON rows);
everywhere else it is opt-in. The rules below are **experimental**: they may be
tuned in any release as measurements come in.

```bash
dtctl query 'fetch logs | summarize count(), by: {loglevel}' -o auto
```

| Result shape | Chosen format |
|---|---|
| empty, or a single scalar value | `json` |
| a single object, or a list with one object | `yaml` (key: value lines) |
| two or more objects, every value a scalar, at least half of the cells filled | `csv` |
| anything else: nested values, sparse rows, a list of non-objects | `yaml` |

A table is never chosen because it truncates, and TOON is never chosen because
CSV is at least as small on flat rows and YAML or JSON beats it on nested data.
Structs are judged by their JSON form, so column and key names match `-o json`.
With `--jq`, the choice is made on the filter's output. (Whether a large
`dtctl query` result spills is decided on the unfiltered rows, as for every
format, because a spilled file holds the unfiltered rows.)

The chosen format is always discoverable:

- **Outside agent mode**, stdout is exactly what the chosen format prints, and
  one line on stderr names the choice, e.g. `-o auto: csv (uniform flat rows)`.
- **In agent mode**, `context.format` names the choice (`csv`, `yaml` or
  `json`). For `csv` and `yaml` the rows are a string in that format; for
  `json` they stay a native JSON value. See
  [AGENT_MODE.md](AGENT_MODE.md#choosing-the-encoding-with--o-auto).

## JSON Lines and Parquet (large query exports)

For `dtctl query`, two additional formats are tailored to large result exports:

```bash
# JSON Lines: one compact JSON object per line (newline-delimited JSON).
# Serialised one record at a time and read natively by most local data tooling.
dtctl query 'fetch logs | limit 1000' -o jsonl > logs.jsonl

# Parquet: a columnar binary file, ideal for downstream analytics tooling.
# Pair with a raised --max-result-records when exporting large populations.
dtctl query 'fetch logs' --max-result-records 100000 -o parquet > logs.parquet
```

Notes:

- **`-o jsonl`** has no schema and appends one object per line, so it tolerates
  rows with differing fields. Each record is encoded as it is written rather than
  building the whole result into a single buffer.
- **`-o parquet`** derives its column schema from the DQL column types (it
  requests type information automatically). Nested or variant columns that do
  not map cleanly to a columnar type are stored as a JSON-encoded string column
  rather than being dropped. An empty result still produces a valid Parquet
  file (never a zero-byte file): it carries the DQL schema when types are known,
  otherwise a single placeholder column so the file stays readable by mainstream
  tooling (a column-less file is rejected by DuckDB, pyarrow, and pandas).
- **Parquet files also carry the DQL types in the file footer**, under the
  key-value metadata key `dtctl.dql.types` (a JSON object mapping column name to
  DQL type, e.g. `{"status.code":"long","content":"string"}`). This lets a reader
  recover type information the physical schema alone loses -- a Grail `long` is
  stored as `INT64`, but Grail's own JSON serialiser emits it as a quoted string,
  so a consumer reproducing Grail's wire form needs the declared type to know
  which columns to stringify. The footer records **every** declared column,
  including ones that were null in every row (Grail omits null fields from
  records, so such a column has no physical column in the file). Read it with
  DuckDB's `parquet_kv_metadata()` or any Parquet footer reader.

## Column types (`--include-types`)

Pass `--include-types` to surface the DQL per-column type information the query
API returns. In `json` and `yaml` output it appears as a top-level `types` key
alongside `records`, preserving the API's shape (`indexRange` + `mappings`):

```bash
dtctl query 'fetch logs | limit 1' -o json --include-types
# {
#   "records": [ { "content": "...", "loglevel": "INFO", "status.code": "200" } ],
#   "types": [
#     {
#       "indexRange": [0, 0],
#       "mappings": {
#         "content":     { "type": "string" },
#         "loglevel":    { "type": "string" },
#         "status.code": { "type": "long" }
#       }
#     }
#   ]
# }
```

Notes:

- **Only with an explicit flag.** The block is emitted only when you pass
  `--include-types` yourself. `--typed` and Parquet output request the same
  metadata internally to do their work, but that does not add the `types` key.
- **`json`/`yaml`/`toon` only.** `table`, `wide`, `csv`, `jsonl` (one record
  per line), the chart formats and `parquet` have no place for a document-level
  sibling, so the block is not emitted there, and dtctl prints a warning on
  stderr saying so (stdout is unchanged). With `-o auto` the warning follows the
  format it picks; with `--jq` the filter input always carries the block. A
  chart format with no rows falls back to the raw JSON response, which keeps
  its types, so that case does not warn.
  Live mode (`--live`) never emits it and warns likewise.
- **Agent mode** carries it in the envelope as `result.types`, on inline and
  spilled results alike (see [Agent Mode](AGENT_MODE.md#column-types---include-types)).
- Note the distinction from `--typed` below: `--include-types` reports the
  declared type while leaving values in their wire form (so a `long` still reads
  as `"200"`), whereas `--typed` uses the same metadata to rewrite the values.

## Numeric typing (`--typed`)

The Grail query API deliberately serialises integer-valued columns (`long`,
`duration`) as JSON **strings** to preserve full int64 precision for
JavaScript/TypeScript consumers. dtctl's `json`, `yaml`, and `jsonl` output
faithfully passes that through, so a `count()` reads as `"42"` (a string):

```bash
dtctl query 'fetch logs | summarize c = count()' -o json
# [ { "c": "42" } ]
```

Pass `--typed` to cast scalar columns to their native types using the DQL type
metadata -- `long`/`duration` become JSON numbers, `boolean` becomes a real
boolean -- so the output is ready for `jq`, pandas, or DuckDB without a
`tonumber` step:

```bash
dtctl query 'fetch logs | summarize c = count()' -o json --typed
# [ { "c": 42 } ]
```

Notes:

- **Opt-in by design.** The default output stays faithful to the API's wire
  encoding. `--typed` implies `--include-types` so the type metadata is
  available.
- **Precision-safe in dtctl.** A `long` is emitted as its full decimal digits,
  unquoted and lossless (never routed through a float). The only precision risk
  is in a downstream consumer that parses JSON numbers as 64-bit floats (browser
  `JSON.parse`, older `jq`) -- which is exactly why it is opt-in.
- **Timestamps stay strings.** JSON/YAML have no native date type, so `timestamp`
  columns keep their portable RFC3339 string form. `string`, `ip`, `timeframe`,
  and nested record/array columns are left unchanged.
- Values that do not cleanly parse to their declared type (including non-finite
  doubles such as `"NaN"`/`"Infinity"`, which JSON cannot represent) are left as
  strings rather than failing the output.

## Compact query results (`--compact`, experimental)

Telemetry rows repeat the same resource attributes (`k8s.*`, `host.*`,
`service.name`, ...) on every row, and Grail returns explicit `null`s for
fields a row's source does not have. `--compact` drops the null values and
prints every column that holds one value in every row once, under a `constant`
map that precedes `records`:

```bash
dtctl query 'fetch spans | filter service.name == "payment" | limit 50' -o json --compact
# {
#   "constant": { "k8s.namespace.name": "checkout", "service.name": "payment" },
#   "records":  [ { "span.name": "POST /pay", "duration": "812000", ... }, ... ]
# }
```

A row is `constant` merged with its record; a key absent from both is null.

Notes:

- **On by default in [agent mode](AGENT_MODE.md#compacted-rows-constant), off
  otherwise.** Outside agent mode the output does not change unless you pass
  `--compact`; in agent mode `--compact=false` restores the full rows.
- Applies to `json`, `yaml`, `toon` and `auto`. Other formats ignore it (an
  explicit `--compact` there prints a warning). `toon` keeps a null in a column
  that has values in other rows, so the rows stay one table.
- With `-o auto` outside agent mode, a `yaml`/`json` choice is compacted, while
  a `csv` choice prints the full rows (a plain CSV file has no place for
  `constant`). In agent mode `-o auto` chooses from the compacted rows and
  carries `constant` next to them whatever it picks (see
  [AGENT_MODE.md](AGENT_MODE.md#compacted-rows-constant)).
- `constant` is only computed for two or more rows; a single row just loses its
  nulls. It is never applied under `--jq`, whose program sees the full rows.
- Spilled files always hold the full rows.

## Compact timeseries (`--series`, `--precision`)

A `timeseries`/`makeTimeseries` result carries every datapoint of every series
as a full-precision array, which makes it the most expensive result shape to
read -- for a person (the table shows `<121 items>`) and for an AI agent (every
value costs ~17 digits of tokens). Two experimental `query` flags compact it.

**Agent-mode default.** In agent mode (`-A`, or auto-detected) `query`
defaults to `--series=summary --precision 4`, so timeseries arrive summarized
and numbers rounded to 4 significant digits. When that actually changed the
result, `context.suggestions` carries one entry naming the opt-out.
`--series=full --precision 0` restores the previous output exactly, and each
flag can be set on its own. Two exceptions: the default summary is skipped for
chart formats, which plot the full series, and neither default applies to
`-o parquet` exports. Outside agent mode both flags default to off and output
is unchanged.

```bash
# Per-series summary with a sparkline
dtctl query 'timeseries cpu=avg(dt.host.cpu.usage), by:{host.name}' --series=summary -o json
# {"host.name": "web-01", "interval": "60000000000", "timeframe": {...},
#  "cpu": {"n": 121, "min": 15.7, "avg": 84.5, "max": 409, "p95": 212, "last": 36.3,
#          "min_at": "2026-01-01T12:41:00Z", "max_at": "2026-01-01T13:12:00Z",
#          "spark": "▃▂▁▁▂▂▁▁▁▂▃▂▁▁▁▂▂█▂▃▃▁▂▂",
#          "step": {"at": "2026-01-01T13:05:00Z", "from": 40.1, "to": 150}}}

# At most 30 points per series, extremes kept -- still a plottable timeseries
dtctl query 'timeseries avg(dt.host.cpu.usage)' --series=downsample:30 -o csv

# Round every number to 3 significant digits
dtctl query 'timeseries avg(dt.host.cpu.usage)' --precision 3 -o json
```

`--series` modes:

- **`full`** (default outside agent mode) -- every datapoint, unchanged.
- **`summary`** (default in agent mode) -- replaces each numeric series with `n` (points), `nulls`
  (only when there are gaps), `min`/`avg`/`max`/`p95`/`last` over the non-null
  points, `min_at`/`max_at` (first occurrence), a sparkline of at most 24
  cells (longer series are bucketed; each cell keeps the value furthest from
  the mean, so a one-point spike is not averaged away; a space is a gap), and a
  `step` hint when the series is best described as a single level shift.
  Statistics are rounded to `--precision` significant digits, or to 3 when it
  is `0`. In `table`, `wide` and `csv` output the summary is one readable
  cell (`▁▂█▃ min=15.7 avg=84.5 max=409 last=36.3 n=121`). Not combinable with
  the chart formats, which plot the full series.
- **`downsample:N`** -- at most N points per series. The series is split into
  N/2 equal buckets and each contributes its minimum and maximum in time
  order, so every peak and trough survives (averaging would flatten them). An
  all-null bucket stays a gap. `interval` is rescaled to the new spacing, so
  the result is still a regular timeseries over the same `timeframe` and works
  with every output format, including charts.

`--series` touches only records carrying both `timeframe` and `interval`, and
within them only arrays of numbers (nulls allowed); dimension columns pass
through.

`--precision N` is not limited to timeseries: it applies to the whole result,
so a `fetch` or `summarize` result is rounded too. It rounds every floating-point number to N
significant digits but never into the integer part: `12345.678` at 3 digits is
`12346`, not `12300`. DQL `long` values (strings on the wire) are never
touched. Both flags apply to the agent envelope, `--jq`, and spilled files
alike; live mode ignores them.

## Plain Mode

The `--plain` flag disables colors, progress indicators, and interactive prompts. This is useful for piping output or running in non-interactive environments:

```bash
dtctl get workflows --plain
```

Color output follows the [no-color.org](https://no-color.org/) standard:

- `--plain` flag disables color
- `NO_COLOR` environment variable disables color
- Non-TTY output (piped) disables color automatically
- `FORCE_COLOR=1` overrides TTY detection to force color on

## Command catalog

`dtctl commands` prints dtctl's own command catalog in machine-readable form. Unlike other commands it defaults to **TOON** (the most compact format); pass `-o json` or `-o yaml` to override. See **[AGENT_MODE.md](AGENT_MODE.md)** for the catalog's `--brief`/`--full` levels and how agents use it.

## Agent mode

The `--agent` (or `-A`) flag wraps output in a structured JSON envelope for AI agents (`{ ok, result, context }`) and implies `--plain`. It is auto-detected in agent environments; opt out with `--no-agent`. See **[AGENT_MODE.md](AGENT_MODE.md)** for the envelope contract, error codes, and auto-detection.

## Pagination

List commands support server-side pagination with the `--chunk-size` flag:

```bash
# Fetch in chunks of 200
dtctl get workflows --chunk-size 200

# Default chunk size is 500
dtctl get workflows

# Disable chunking (fetch all at once)
dtctl get workflows --chunk-size=0
```

All pages are fetched automatically and combined into a single result set.

## Trimming lists (`--limit`, `--fields`)

Every `get` list verb accepts `--limit` and `--fields` *(experimental)*. Both
shape the result after it was fetched, so `--chunk-size` still decides how the
API is paged.

```bash
# Only the first 20 SLOs
dtctl get slos --limit 20

# Only these fields, in this order; dotted paths reach nested fields
dtctl get dashboards --fields id,name,owner,modificationInfo.lastModifiedTime -o json

# Tabular formats flatten nested fields into columns, so TOON stays tabular
dtctl get dashboards --fields id,name,modificationInfo.lastModifiedTime -o toon
# [185]{id,name,modificationInfo.lastModifiedTime}:
#   ...
```

- `json`/`yaml`/`jsonl` keep the projected fields nested
  (`{"modificationInfo":{"lastModifiedTime":...}}`), so a path or `--jq` filter
  that works on the full object works on the projection. `--jq` runs after
  `--fields`.
- `table`/`wide`/`csv`/`toon` use one column per requested path, named by the
  path. A field that holds an object expands into one column per leaf
  (`--fields modificationInfo` gives `modificationInfo.createdBy`,
  `modificationInfo.lastModifiedTime`, ...).
- A requested field no item carries produces a warning (a typo is the usual
  cause) rather than an error, since optional fields can be absent from every
  item.
- When `--limit` cuts the list, a hint on stderr says how many items there
  were; in agent mode the envelope carries `context.total`,
  `context.has_more: true` and a suggestion instead.
- Commands that already had a server-side `--limit` (`get workflows`,
  `get workflow-executions`, `get scheduling-rules`, `get snapshots`) keep
  their own flag and its meaning.
- **Agent mode pages by default.** In agent mode (`-A` or auto-detected), a
  `get` list returns at most **50** items when `--limit` is not given.
  `--limit 0` returns everything, and a larger `--limit` returns more. When the
  page cuts the list, the envelope carries `context.total`,
  `context.has_more: true` and a suggestion naming `--limit`; a list of 50 or
  fewer is returned unchanged. `get workflows` and `get scheduling-rules` pass
  the page to the API as their `--limit`; `get workflow-executions` keeps its
  server-side window of 100 and `get snapshots` its DQL record limit. Outside
  agent mode nothing is paged by default.
- `--fields` is rejected with the chart formats, which need the full records.
