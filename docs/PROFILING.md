# Code-Level Profiling (`dtctl exec profiling`)

> **Experimental.** Both the underlying code-level-analysis API and these commands
> may change in breaking ways between releases. Do not build automations directly
> against the underlying endpoint — drive it through this command, which will be
> re-pointed if the API changes.

`dtctl exec profiling` runs a code-level profiling analysis on a monitored process
and returns one row per unique call stack — in a table as **collapsed (folded)
stacks**, the representation an agent (or a human) can read directly, rather than
the API's UI render tree.

```
dtctl exec profiling hotspots    <entity> [flags]
dtctl exec profiling allocations <entity> [flags]   # alias: memory
```

`allocations` (alias `memory`) shows where memory is allocated and what survived GC
in the window: allocation pressure, not what occupies the heap now.

## Entities

Pass a **process** id, `PROCESS-xxx`. `SERVICE` ids are **not** eligible.

The API speaks the classic entity vocabulary, so dtctl rewrites `PROCESS-xxx` to its
`PROCESS_GROUP_INSTANCE-xxx` form (same entity, same id suffix; the API 404s on the
`PROCESS-` prefix). The agent envelope names the rewritten id in
`context.profile.api_entity`. Classic ids work as well: `PROCESS_GROUP_INSTANCE-xxx`, and
`PROCESS_GROUP-xxx` for a whole process group.

Find process ids:

```
dtctl query 'smartscapeNodes PROCESS
  | fields id, dt.process_group.detected_name, dt.process_group.id'
```

- `id` is the `PROCESS-xxx` id to pass.
- `dt.process_group.id` is its `PROCESS_GROUP-xxx` id.

## Output

Folded stacks — one line per unique stack, sorted by RUNNING, carrying all five
thread-state counts so a single read shows whether a stack is CPU-bound or blocked:

```
com.example.OrderController.list;com.example.OrdersOverview.build  running=412 lock=0 net_io=0 disk_io=0 wait=3
com.example.ManufacturerController.issueCreditCard;...reserveSlot  running=2   lock=388 net_io=0 disk_io=0 wait=0
```

- Each frame's own (**self**) samples are attributed to its stack (`self = node −
  Σ children`, since the API's counts are cumulative), so the counts reconcile with
  the analysis totals — not just the leaves.
- Frames read `package.Class.method`. Node.js frames have no class, so they read
  `file:function`, e.g. `node:internal/timers:listOnTimeout` or
  `graphql/execution/execute.js:executeField` (path cut after `node_modules/`).
- This is deliberately **not** single-metric folded, so it does not feed
  `flamegraph.pl`/`speedscope` directly; the multi-counter line keeps lock/IO/wait
  visible, which is what makes off-CPU and lock-contention cases diagnosable.
- `--limit N` caps the number of rows (default 100, 20 in agent mode; `0` = all); it
  never changes the shape. A capped table ends with a `# showing N of M stacks` line.
- `-o json`, `-o yaml`, `-o csv` and `--jq` print the **same rows** as fields
  (`running`, `lock`, `net_io`, `disk_io`, `wait`, `stack`; for `allocations`:
  `alloc_bytes`, `survived_bytes`, `alloc_count`, `survived_count`, `stack`), capped
  by `--limit` like the table. `--raw` prints the API's whole compacted result instead
  (megabytes for a busy process).
- An empty result prints one `# no stacks for …` line saying why: no samples in
  the window, or `--app-only` removed every frame.

### Agent envelope

With `-A` (or in an agent session) the rows ride in `result`, and `context` carries:

- `total` (all rows), `has_more` and a `warnings` entry when `--limit` cut rows;
- `window` (`from`, `to`, `span`, and a `note` when the default window was used);
- `profile`: `entity`, `api_entity` (when rewritten), `kind`, `view`, `leaf_type`,
  `state`, `status`, and the totals the rows are shares of (`samples`, or
  `allocated_bytes`/`survived_bytes`).

An empty result is an empty `result` with the reason in `context.warnings`.

### Per-function views (hotspots)

The stack view has one row per unique call stack. A function reached through
many paths is split over many rows, so it can be among the hottest functions
without any single row showing it: on a Node.js service, the module loader's
`wrapModuleLoad` had 7% of all CPU spread over 663 call paths. Two views sum the
stacks per function:

```
$ dtctl exec profiling hotspots PROCESS-ABC123 --view functions --limit 3
SELF   SELF%   TOTAL   TOTAL%   PATHS   FUNCTION
3013   20.3    3836    25.8     1       node:net:Socket
1038   7       5197    35       663     node:internal/modules/cjs/loader:wrapModuleLoad
968    6.5     5523    37.2     1       node:internal/timers:listOnTimeout
# 3 of 1955 functions, by self running samples (14866 running samples in the window)
```

- **SELF**: samples with the function running, on top of the stack. Summing the
  stack view's rows by their last frame gives the same numbers.
- **TOTAL**: samples with the function anywhere on the stack, itself plus
  everything it calls. A recursive function counts once per stack.
- **PATHS**: how many distinct call paths reach it, i.e. how scattered it is.
- Sorted by self time; `--sort total` ranks by total. Default `--limit 30`.

`--focus <function>` shows one function's callers and callees, each with its
share of the function's total:

```
$ dtctl exec profiling hotspots PROCESS-ABC123 --focus wrapModuleLoad --limit 2
RELATION   SAMPLES   SHARE%   FUNCTION
total      5197      100      node:internal/modules/cjs/loader:wrapModuleLoad
self       1038      20       node:internal/modules/cjs/loader:wrapModuleLoad
caller     5126      98.6     node:internal/modules/run_main:executeUserEntryPoint
caller     71        1.4      node:internal/modules/cjs/loader:<anonymous>
callee     4062      78.2     node:internal/modules/cjs/loader:<anonymous>
callee     39        0.8      node:fs:readFileSync
```

It takes an exact frame as printed, or a substring of exactly one (an ambiguous
one lists the candidates). Callers are taken at the function's outermost call on
each stack (how execution entered it), callees and `self` at its innermost (what
it spends its time on), so with recursion each list still adds up to the total.
Default `--limit 10` callers and 10 callees; re-run `--focus` on a caller or
callee to walk the call graph.

Both views count running (CPU) samples. `--state lock`, `net_io`, `disk_io`,
`wait`, `offcpu` (all four non-running states) or `all` counts others instead;
waiting threads are only sampled with `--show-waiting`. With `-o json`, `-o csv`,
`--jq` or `-A` they print their rows, and `--app-only` gives a hidden frame's
time to the nearest application frame above it, as in the stack view.

## Flags

| Flag | Kinds | Meaning |
|------|-------|---------|
| `--default-timeframe-start` / `--default-timeframe-end` | all | Window — what `dtctl query` takes: ISO-8601/RFC3339, epoch millis, or `now()`-relative (`now()-1d`, `now()-90m`, `now()-1h30m`). Defaults to the last 2h; a start alone covers the 2h after it, an end alone the 2h before it. The window is at most 2h long but can lie anywhere in the past; an end in the future is rejected. |
| `--app-only` | all | Show only application frames, stripping runtime, library and OneAgent frames. The base is the profiler's `apiInfo.systemApi` (JRE, Spring, Apache are off). dtctl also drops the OneAgent's own frames (`com.dynatrace.agent.*`, `Dynatrace.OneAgent.*`), which the profiler counts as app code. On Node.js it drops npm packages and the `node:` runtime. |
| `--limit N` | all | Cap output to the top N rows (`0` = all): stacks (default 100, 20 in agent mode), functions (default 30), or callers and callees each with `--focus` (default 10). |
| `--raw` | all | Print the API's whole compacted result instead of rows (`-o json` or `yaml`). |
| `--leaf-type total\|service\|background` | hotspots | Which CPU samples to attribute. `service` = inside a traced request context (reach for this to localize an endpoint); `background` = ambient (GC, JIT, pools); `total` = everything (default). |
| `--show-waiting` | hotspots | Include waiting (non-running) samples. |
| `--view stacks\|functions` | hotspots | One row per call stack (default), or one row per function with self and total samples. |
| `--focus <function>` | hotspots | One function's callers and callees, with shares of its total. |
| `--state <state>` | hotspots | What `--view functions` and `--focus` count: `running` (default), `lock`, `net_io`, `disk_io`, `wait`, `offcpu` or `all`. |
| `--sort self\|total` | hotspots | Order of `--view functions` (default `self`). |
| `--survivors-only` | allocations | Restrict to allocations that survived at least one GC in the window, ranked by survived bytes. |

## Examples

```bash
# CPU hotspots for a process, application frames only, last hour
dtctl exec profiling hotspots PROCESS-ABC123 --app-only --default-timeframe-start 'now()-1h'

# The same hour yesterday
dtctl exec profiling hotspots PROCESS-ABC123 --default-timeframe-start 'now()-1d' \
  --default-timeframe-end 'now()-23h'

# Which functions use the most CPU, summed over every stack, and who calls the top one
dtctl exec profiling hotspots PROCESS-ABC123 --view functions
dtctl exec profiling hotspots PROCESS-ABC123 --focus OrderService.load

# Which functions wait on locks
dtctl exec profiling hotspots PROCESS-ABC123 --view functions --state lock

# Localize why a specific endpoint is hot
dtctl exec profiling hotspots PROCESS-ABC123 --leaf-type service --limit 5

# Memory allocation pressure, only survivors (the leak tell)
dtctl exec profiling allocations PROCESS-DEF456 --survivors-only

# The top 5 stacks as JSON rows, for a script
dtctl exec profiling hotspots PROCESS-ABC123 --limit 5 -o json

# The API's whole result
dtctl exec profiling hotspots PROCESS-ABC123 --raw --jq '.result.dataNodes | length'
```

## Auth

Read-only. Requires `storage:entities:read` + `storage:profiles:read`, both on the
token and in the IAM policy. Both are part of dtctl's OAuth scope set, so after
`dtctl auth login` nothing else is needed; a platform token needs both scopes added.

## Rate limiting

An analysis is expensive server-side, so an environment limits how many run at
once and answers the rest with `429 Too Many Requests`. dtctl waits and submits
again (honoring `Retry-After` when the server sends one) a few times before it
gives up with a 429 error. Avoid running many analyses in parallel.
