# Usage tracking

`dtctl usage` shows a running total of the DQL queries dtctl ran on this
machine and the data they scanned: today, the last 7 days and the last 30 days.
It is meant for people who have no access to billing data but want to see what
their own scripts, n8n flows and AI agents consume.

Usage tracking is **opt-in per context and off by default**. With it off, dtctl
writes nothing.

> **Experimental.** The command, the flag and the file format may change.

## What it covers, and what it does not

The tally counts only DQL queries that **dtctl** ran **on this machine** in a
context that has tracking on. It does not see queries from the web UI,
notebooks, dashboards, apps, MCP servers, other machines or other tools. It is
not a bill: scan volume is shown in GiB/MiB, never as a currency, because what
a GiB costs depends on your contract.

For authoritative consumption, query the billing events (needs
`storage:system:read`):

```bash
dtctl query 'fetch dt.system.events | filter event.kind == "BILLING_USAGE_EVENT"'
```

## Turn it on

```bash
dtctl config set-context prod --usage-tracking          # on
dtctl config set-context prod --usage-tracking=false    # off (keeps recorded data)
```

`dtctl ctx set` takes the same flag. In the config file it is a per-context key:

```yaml
contexts:
  - name: prod
    context:
      environment: https://abc12345.apps.dynatrace.com
      token-ref: prod
      usage-tracking: true
```

`dtctl ctx describe` shows `Usage: tracked locally` for a context that has it on.

Only the global config (or one named with `--config` / `DTCTL_CONFIG`) can turn
tracking on. A project-local `.dtctl.yaml` cannot, because the ledger writes to
your disk: `usage-tracking: true` there is ignored (`dtctl usage` says so), and
`set-context --usage-tracking` refuses to write it into one; add `--global`. A
local context is tracked when the global context that binds the same token-ref
and environment has tracking on.

## Read it

```bash
dtctl usage                    # current context: today, 7d, 30d
dtctl usage --all-contexts     # every context with tracking on or with recorded data
dtctl usage -o json            # also yaml, csv, toon, wide; --agent gives the envelope
```

Days are UTC days. `today` is the current UTC date, `7d` and `30d` include
today. Columns: queries, failed queries, scanned bytes (human-readable),
scanned records, and (with `-o wide`) scanned data points.

## What is recorded

Every DQL execution dtctl runs in an opted-in context adds to one counter for
the pair (UTC date, context name): `dtctl query`, `dtctl wait query`, `dtctl
inventory`, lookup table queries and so on. A query that fails or is cancelled
still counts, with whatever it scanned before it ended, and also adds to the
`failed` counter.

Each counter holds only:

| Field               | Meaning                                    |
| ------------------- | ------------------------------------------ |
| `queries`           | DQL executions                             |
| `failed`            | executions that ended with an error        |
| `scannedBytes`      | bytes scanned, as reported by Grail        |
| `scannedRecords`    | records scanned                            |
| `scannedDataPoints` | metric data points scanned                 |

Query text, results, user identity, tenant URLs and tokens are **never
stored**. Days older than 90 are pruned on the next write.

Embedded use (the Go library, `dtctl serve`) never records, and `dtctl usage`
is not available there.

## Where it is stored

One file, `usage.json`, in dtctl's state directory:

| OS      | Path                                             |
| ------- | ------------------------------------------------ |
| Linux   | `~/.local/state/dtctl/usage.json`                |
| macOS   | `~/Library/Application Support/dtctl/usage.json` |
| Windows | `%LOCALAPPDATA%\dtctl\usage.json`                |

`XDG_STATE_HOME` overrides the base directory on every OS. The file is
created with mode `0600`, and the directory with `0700` if dtctl creates it.
Several dtctl processes can run at once: each update takes a lock
(`usage.json.lock`) and replaces the file atomically. `dtctl usage` prints the
exact path in table output.

Recording never fails a command: if the file cannot be locked (within 2
seconds), read or written, the update is skipped (`-v` shows why). A corrupt
file is replaced with a fresh tally.

The file looks like this:

```json
{
  "version": 1,
  "days": {
    "2026-10-12": {
      "prod": { "queries": 12, "failed": 1, "scannedBytes": 3221225472, "scannedRecords": 1200 }
    }
  }
}
```

## Clear it

```bash
dtctl usage reset                  # current context (asks for confirmation)
dtctl usage reset --all-contexts   # delete the whole file
dtctl usage reset -y               # no prompt
```

As elsewhere in dtctl, `--plain` and agent mode skip the prompt. Deleting the
file by hand works too.
