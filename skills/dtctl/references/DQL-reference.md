# DQL Syntax Guide

## Copy These Templates Exactly

**Filter multiple values:**
```dql
filter in(loglevel, array("ERROR", "WARN", "SEVERE"))
```

**Aggregation with grouping:**
```dql
summarize cnt = count(), by:{loglevel}
```

**String length:**
```dql
fieldsAdd len = stringLength(content)
```

**Entity fields:**
```dql
smartscapeNodes SERVICE
| fields id, name
```

**Format timestamp:**
```dql
fieldsAdd ts = formatTimestamp(timestamp, format:"yyyy-MM-dd HH:mm:ss")
```

## String Quoting (read this first)

DQL string literals **must use double quotes** (`"ERROR"`), never single quotes.
`filter status == 'ERROR'` fails with `PARSE_ERROR_SINGLE_QUOTES`, and an
**unquoted** bareword like `filter status == ERROR` is parsed as a *field
reference* (compares `status` to a field named `ERROR`) — so it silently
returns **zero rows with no error**. Both are common mistakes; neither is a bug.

```dql
filter status == "ERROR"          -- correct
filter status == 'ERROR'          -- WRONG: PARSE_ERROR_SINGLE_QUOTES
filter status == ERROR            -- WRONG: matches nothing (ERROR = field ref)
```

Because the value needs double quotes, wrap the **whole query** so your shell
preserves them:

| Shell | Command |
|-------|---------|
| bash/zsh | `dtctl query 'fetch logs \| filter status == "ERROR"'` (single-quote the query) |
| cmd.exe | `dtctl query "fetch logs \| filter status == \"ERROR\""` (escape inner quotes) |
| PowerShell | `@'` / `fetch logs \| filter status == "ERROR"` / `'@ \| dtctl query` (pipe a here-string) |
| any (quote-free) | `dtctl query -f query.dql`, or pipe into `dtctl query` |

When generating a `dtctl query` command for a user, **prefer the single-quoted
wrapper** (or `-f`) so the double-quoted DQL values survive intact.

On Windows, do **not** generate the quoted-argument form: Windows PowerShell 5.1
strips double quotes from arguments to native executables, turning
`status == "ERROR"` into `status == ERROR` — valid DQL that silently matches
nothing. Generate the piped here-string or `-f query.dql` instead. Never
generate `dtctl query -f - @'...'@`: `-f -` reads stdin while the here-string
goes to argv, so the command blocks on an idle terminal.

## Data Sources

```dql
fetch logs, from:now()-1h           -- Log records
fetch events                        -- System events
fetch bizevents                     -- Business events
fetch spans                         -- Trace spans
fetch security.events               -- Security/vulnerability events
smartscapeEdges "*"                 -- Entity relationships (calls, runs_on, etc.)
smartscapeNodes HOST                -- Preferred: query hosts (dt.entity.host is legacy)
smartscapeNodes SERVICE             -- Preferred: query services (dt.entity.service is legacy)
smartscapeNodes PROCESS             -- Preferred: query processes (dt.entity.process_group_instance is legacy)
timeseries avg(dt.host.cpu.usage)   -- Metrics (NOT fetch metrics)
```

## Scan Cost (billable fetch)

`fetch logs/events/bizevents/spans/security.events` bills by bytes scanned; `timeseries` on a metric does not. Prefer a metric when one exists. For general optimization guidance (command order, filter pushdown, cardinality, timeframe sizing) see [`dt-dql-essentials/references/optimization.md`](https://github.com/Dynatrace/dynatrace-for-ai/blob/main/skills/dt-dql-essentials/references/optimization.md); the dtctl-specific knobs are below.

```dql
fetch logs, from:now()-2h, scanLimitGBytes:50      -- hard ceiling on bytes read
fetch logs, from:now()-7d, samplingRatio:1000      -- scan ~1/1000 of the data
```

- Always bound the timeframe inline (`from:`) — the tile/notebook timeframe is not a substitute in a saved query.
- `scanLimitGBytes:` is a brake, not a filter: exceeding it returns a PARTIAL result, not an error.
- `samplingRatio:` (power of 10, max 100000) trades exactness for scan volume on wide log/span windows. Extrapolate counts with `sum(dt.system.sampling_ratio)`, not `count()`.
- Same knobs as dtctl flags when the query text is fixed: `--default-scan-limit-gbytes`, `--default-sampling-ratio`. `--include-contributions --metadata=contributions` reports per-bucket scan contribution — use it to find the heavy buckets, then restrict with `bucket:{"<bucket>"}`. `--default-scan-limit-gbytes -1` is unlimited.
- Filter on the raw field (`filter loglevel == "ERROR"`), not on a transform of it (`filter lower(loglevel) == "error"` or `contains(lower(name), "x")`) — the latter defeats index pushdown and scans far more. For case-insensitive text matching use `contains(f, "x", caseSensitive:false)`, `matchesValue(f, "x*")` (whole value / prefix; case-insensitive by default) or `matchesPhrase(content, "x")` (word/phrase in free text; case-insensitive by default) — never `lower()`/`upper()` on the field.
- A PARTIAL or sampled result is not a complete answer. When Grail reports one, dtctl names the applicable reduction — a hint on stderr for humans, envelope `warnings`/`suggestions` in `--agent` mode. Act on it rather than reading the rows as final.
- `limit N | summarize` vs `summarize | limit N` is a **semantic** choice (partial sample vs full aggregate), not a cost anti-pattern. Pick by intent.

## Essential Patterns

### Filter and select
```dql
fetch logs, from:now()-1h
| filter loglevel == "ERROR"
| fields timestamp, content, loglevel
| sort timestamp desc
| limit 100
```

### Aggregate with grouping (alias required for sort)
```dql
fetch logs, from:now()-2h
| summarize cnt = count(), by:{loglevel}
| sort cnt desc
```

### Multiple values
```dql
filter loglevel == "ERROR" or loglevel == "WARN" or loglevel == "SEVERE"
-- OR --
filter in(loglevel, array("ERROR", "WARN", "SEVERE"))
```

### Metrics (timeseries command, NOT fetch)
```dql
timeseries avg(dt.host.cpu.usage), by:{dt.entity.host}, from:now()-6h, interval:5m
```

### Log time-series (makeTimeseries, NOT summarize)
```dql
fetch logs, from:now()-4h
| filter loglevel == "ERROR"
| makeTimeseries cnt = count(), interval:10m, by:{k8s.namespace.name}
```

### Entity search
Use `smartscapeNodes <TYPE>` — `dt.entity.*` is the legacy form.
```dql
smartscapeNodes SERVICE
| filter contains(name, "payment") or startsWith(name, "api-")
| fields id, name

smartscapeNodes HOST
| filter contains(name, "my-host")
| fields id, name

smartscapeNodes PROCESS
| filter contains(name, "java")
| fields id, name
```

### Array expansion (after expand, use brackets)
```dql
fetch spans
| filter isNotNull(span.events)
| expand span.events
| filter span.events[span_event.name] == "exception"
| fields span.events[exception.message], span.events[exception.type]
```
Note: After `expand arr`, access fields via `arr[field]` NOT `arr.field`

### String functions
```dql
filter contains(content, "timeout") or contains(content, "connection refused")
filter endsWith(log.source, ".log")
filter startsWith(name, "api-")
```

### Absolute timestamps
```dql
fetch events, from:"2025-01-01T00:00:00Z", to:"2025-01-02T00:00:00Z"
-- OR in filter --
filter timestamp >= toTimestamp("2025-01-01T00:00:00Z")
```

### Computed fields
```dql
fetch logs
| fieldsAdd msg_len = stringLength(content)
| fieldsAdd time_str = formatTimestamp(timestamp, format:"yyyy-MM-dd HH:mm:ss")
| fields timestamp, time_str, msg_len, content
```

### Business events aggregation
```dql
fetch bizevents, from:now()-1h
| summarize total = count(), sum_amt = sum(amount), avg_amt = avg(amount), by:{event.type}
```

### Field escaping (hyphens/special chars)
```dql
filter `error-code` == "404"
```

### Security vulnerabilities
```dql
fetch security.events
| filter event.type == "VULNERABILITY_STATE_REPORT_EVENT"
| filter vulnerability.resolution.status == "OPEN"
| sort vulnerability.risk.score desc
```

### Smartscape relationships
```dql
smartscapeEdges "*"
| filter type == "calls"
| fields source_id, target_id, type
| limit 100
```

### Finding data related to Smartscape entities
dt.smartscape.k8s_cluster
dt.smartscape.host
dt.smartscape.container
dt.smartscape.process
```dql
fetch logs
| filter dt.smartscape.host == toSmartscapeId("HOST-110D7F5A3B1BA062")
| fields content, dt.smartscape.host
| limit 100
```

## Key Functions

| Function | Usage |
|----------|-------|
| `count()` | `cnt = count()` |
| `sum(field)` | `total = sum(amount)` |
| `avg(field)` | `average = avg(duration)` |
| `contains(str, sub)` | `contains(content, "error")` |
| `startsWith(str, pre)` | `startsWith(name, "api-")` |
| `endsWith(str, suf)` | `endsWith(source, ".log")` |
| `contains(str, sub, caseSensitive:false)` | `contains(name, "payment", caseSensitive:false)` |
| `matchesPhrase(str, phrase)` | `matchesPhrase(content, "connection refused")` |
| `in(val, arr)` | `in(level, array("A","B"))` |
| `stringLength(str)` | `stringLength(content)` |
| `formatTimestamp(ts, format:f)` | `formatTimestamp(timestamp, format:"HH:mm")` |
| `toTimestamp(str)` | `toTimestamp("2025-01-01T00:00:00Z")` |
| `isNotNull(field)` | `isNotNull(span.events)` |
| `matchesValue(str, pattern)` | `matchesValue(name, "payment*")` |
| `countIf(condition)` | `errors = countIf(loglevel == "ERROR")` |
| `countDistinct(field)` | `unique_hosts = countDistinct(dt.entity.host)` |
| `percentile(field, pct)` | `p95 = percentile(duration, 95)` |
