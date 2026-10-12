# Live Debugger Guide

> **Experimental:** every Live Debugger command, and `dtctl query --decode-snapshots`,
> is declared [`experimental`](STABILITY.md): the underlying APIs and snapshot payload
> shapes may change in any release, so these are not safe for unattended automation.
> They are registered and usable by default. A context that pins
> `min-stability: stable` withholds them until it names one in
> `stability-exceptions` (e.g. `dtctl config set-context prod --stability-exception 'get breakpoints'`).

This guide explains how to use Dynatrace Live Debugger features from `dtctl`.

## Overview

The current Live Debugger flow in `dtctl` supports:

- configuring workspace filters with `dtctl update breakpoint --filters ...`
- creating breakpoints with `dtctl create breakpoint File.java:line`
- listing breakpoints with `dtctl get breakpoints`
- describing breakpoint status with `dtctl describe breakpoint <id|filename:line>`
- updating breakpoints with `dtctl update breakpoint ...`
- deleting breakpoints with `dtctl delete breakpoint ...`
- fetching snapshots for a breakpoint with `dtctl get snapshots <breakpoint> --decode-snapshots`

`dtctl` resolves or creates a Live Debugger workspace for the current project path, so commands operate on the workspace associated with the directory you run them from, and say which (in a linked repository it is `<repo>/<entry>` or `<repo>`; see [Filters from the repo scope](#filters-from-the-repo-scope)).

## Prerequisites

Before using Live Debugger commands:

1. Configure a Dynatrace context with `dtctl config set-context`
2. Authenticate with OAuth using `dtctl auth login`, or configure platform token credentials
3. Run Live Debugger commands from the project directory you want associated with the workspace

### Authentication note

Live Debugger breakpoint operations support both OAuth authentication and platform tokens.
When using a platform token, include the `dev-obs:breakpoints:set` scope in the token scopes.

## 1. Configure workspace filters

Use `dtctl update breakpoint --filters` to target the runtime instances you want Live Debugger to apply to.

```bash
dtctl update breakpoint --filters k8s.namespace.name:prod
```

Multiple filters are supported as comma-separated `key:value` pairs.
`key=value` is also supported for compatibility.

```bash
dtctl update breakpoints --filters k8s.namespace.name:prod,dt.entity.host:HOST-123
dtctl update breakpoint --filters k8s.namespace.name=prod,dt.entity.host=HOST-123
```

Filters are workspace-scoped: a single filter set applies to **every** breakpoint
in the workspace. Changing the filters therefore re-scopes all existing
breakpoints, not just ones you create afterwards. Because of this, `dtctl` counts
the active breakpoints that would be affected and prompts for confirmation before
applying the change. Pass `--yes` (`-y`) to skip the prompt, for example in
scripts:

```bash
dtctl update breakpoint --filters k8s.namespace.name:prod --yes
```

### Notes

- `--filters` (or `--repo-scope`, see below) is required for `dtctl update breakpoint`
- filter values are mapped to the Live Debugger workspace filter set payload
- repeated keys are supported
- changing filters re-scopes all existing breakpoints, so you are prompted to
  confirm unless you pass `--yes` (`-y`); auto-disabled breakpoints (for example
  after reaching their hit limit) are not counted, and non-interactive contexts
  (`--plain` or agent mode) skip the prompt
- in verbose/debug mode, raw GraphQL responses are printed for troubleshooting

### Filters from the repo scope

A repository linked with `dtctl repo-scope` (see [REPO_SCOPE.md](REPO_SCOPE.md))
records which workloads, service names and entities run its code, and those
bindings are the labels a workspace is filtered by. `dtctl repo-scope describe`
lists the filter sets an entry renders to, one line per set in the spelling
`--filters` takes:

```text
Live Debugger filters:
  k8s.namespace.name:payments,k8s.workload.name:checkout
  service.name:checkout
```

An entry renders one filter set per Kubernetes namespace (with that namespace's
workloads), then one for its service names and one for its process group ids
(`dt.entity.process_group`). Service ids are never sent: a workspace does not
honour `dt.entity.service`. Only a one-set entry can be pasted into
`--filters`. The default on create sends the workload set only; a named
`--repo-scope` sends every set.

- **Default on create.** `dtctl create breakpoint` without `--filters`, in a
  workspace that has no filters yet, applies the namespace-and-workload set of
  the entry covering the working directory, when it renders exactly one. The
  entry's service-name and process-group sets are not sent by default: entity
  ids are deprecated fields that change with redeploys, and `service.name` is
  unverified as a label. It says so on stderr and in the result, and in agent
  mode adds a suggestion naming `--filters` for setting them by hand and
  `--no-repo-scope` for turning the default off:

  ```text
  set the workspace filters from repo scope checkout: k8s.namespace.name:payments,k8s.workload.name:checkout (--filters sets them by hand, --no-repo-scope turns this default off)
  repo scope checkout's other filter sets (service.name:checkout) are set only with --repo-scope checkout
  ```

  A workspace that already has filters keeps them; when the covering entry
  would set different ones, a warning names `--repo-scope <entry>`. An entry
  with no workload binding, or with workloads in several namespaces, has no
  one set to apply, and the command says to pass `--repo-scope <entry>`. A
  workspace without filters normally holds no breakpoints; if it holds active
  ones, the default asks first, and is refused, naming `--yes`, where nobody
  can be asked (`--plain`, agent mode, an ended stdin).
- **A named entry.** `--repo-scope <name>` on `create breakpoint` and
  `update breakpoint` sets the workspace filters from that entry, every set,
  like `--filters` does (and with the same confirmation). It cannot be combined
  with `--filters`. To return a workspace to its repo scope's filters:
  `dtctl update breakpoint --repo-scope <name>`.
- **Opting out.** `--no-repo-scope` on `create breakpoint`, or
  `DTCTL_NO_REPO_SCOPE=1`, turns the default off; it can be combined with
  `--filters`. `update breakpoint` applies a scope only when one is named, so
  it has no `--no-repo-scope`.

In a checkout with a scope file the workspace is named after the origin
remote's repository name (the checkout directory's when `.git/config` cannot
be read, so each such worktree gets its own): `<repo>/<entry>` where an entry
covers the working directory, and `<repo>` in every other directory of the
checkout, including one two entries tie on. Without a scope file, and outside
a checkout, it is named after the working directory's base name.
`--repo-scope <name>` uses `<repo>/<name>` wherever it runs, on every
breakpoint command: `get breakpoints`, `describe breakpoint`,
`delete breakpoint`, `get snapshots` and `update breakpoint <id>` take it only
to name the workspace.
Neither `--no-repo-scope` nor `DTCTL_NO_REPO_SCOPE` changes the name. Saving an
entry moves the directories it covers from `<repo>` to `<repo>/<entry>`: to
clean up the old workspace, run `dtctl delete breakpoint --all` there before
saving, or afterwards from a directory of the checkout no entry covers.

```bash
dtctl create breakpoint OrderController.java:306                        # default from the covering entry
dtctl create breakpoint OrderController.java:306 --repo-scope checkout  # a named entry
dtctl update breakpoint --repo-scope checkout                           # replace the workspace filters
dtctl create breakpoint OrderController.java:306 --dry-run              # preview what the default will do
```

The dry run reads the workspace and says whether the default would set its
filters, keep them, warn, or fail; like `get breakpoints`, it may create an
empty workspace.

> **Verified labels.** `k8s.namespace.name` with `k8s.workload.name`, and
> `dt.entity.process_group`, select processes; the labels of one set must all
> match, and several sets are alternatives. `service.name` is not yet verified:
> check with `dtctl describe breakpoint <id>` that a breakpoint set from it
> reaches the processes you expect.

## 2. Create a breakpoint

Create a breakpoint by file and line number:

```bash
dtctl create breakpoint OrderController.java:306
```

A breakpoint can only be created in a workspace that already has filters
configured. Either configure them first (see step 1), or set them in the same
step with `--filters`:

```bash
dtctl create breakpoint OrderController.java:306 --filters k8s.namespace.name:prod
```

Filters are workspace-scoped and persist, so once set you can create additional
breakpoints (in other files) without repeating `--filters`. Because filters are
shared across the workspace, passing `--filters` here also re-scopes any existing
breakpoints, so `dtctl` prompts for confirmation unless you pass `--yes` (`-y`).

### Rules

- the expected format is `File.java:line`
- the line number must be a positive integer
- `--filters` is optional and accepts comma-separated `key:value` (or `key=value`) pairs
- when `--filters` is provided, the workspace filters are updated before the breakpoint is created
- when `--filters` changes the workspace filters, existing breakpoints are re-scoped too, so you are prompted to confirm unless you pass `--yes` (`-y`)
- `--repo-scope <name>` sets the filters from a repo scope entry instead, and in a linked repository a workspace without filters gets them from the covering entry (see [Filters from the repo scope](#filters-from-the-repo-scope))
- otherwise, when `--filters` is omitted, the workspace must already have filters configured
- `--dry-run` is supported

Example:

```bash
dtctl create breakpoint OrderController.java:306 --dry-run
```

## 3. List breakpoints

List breakpoints in the current workspace:

```bash
dtctl get breakpoints
```

Default output is a table with:

- breakpoint ID
- filename
- line number
- active state
- log message (with `{variable}` placeholders in their friendly form; see [Log messages](#log-messages))

Structured output is also supported:

```bash
dtctl get breakpoints -o json
dtctl get breakpoints -o yaml
```

## 4. Describe breakpoint status

Inspect the current status of a breakpoint by ID or source location:

```bash
dtctl describe breakpoint dtctl-rule-123
```

```bash
dtctl describe breakpoint OrderController.java:306
```

The command uses `GetRuleStatusBreakdown` and summarizes:

- enabled/disabled state
- the workspace it was read from (`workspace` in structured output)
- log message (see [Log messages](#log-messages))
- overall status
- active and pending rooks
- warnings and errors
- controller warnings and errors
- backend tips

Structured output is supported:

```bash
dtctl describe breakpoint OrderController.java:306 -o json
dtctl describe breakpoint OrderController.java:306 -o yaml
```

## 5. Update breakpoints

Update a breakpoint condition:

```bash
dtctl update breakpoint OrderController.java:306 --condition "value>othervalue"
```

Enable or disable a breakpoint:

```bash
dtctl update breakpoint OrderController.java:306 --enabled true
dtctl update breakpoint OrderController.java:306 --enabled false
```

Change the log message:

```bash
dtctl update breakpoint OrderController.java:306 --log-message "Hit on {frame.filename}:{frame.line} value={newTodoRecord.title}"
```

### Log messages

A breakpoint's log message is the line emitted each time the breakpoint is hit.
It supports `{variable}` placeholders that are interpolated at runtime:

- `{frame.filename}`, `{frame.line}`, and other `frame.*` fields expose the hit
  location and stack frame.
- `{myVar.field}` (any other name) references a captured application variable.
- Reserved namespaces such as `{rook.*}`, `{agent.*}`, `{bp.*}`,
  `{message_info.*}`, and `{controller.*}` pass through unchanged.

You always use the short, friendly form (e.g. `{frame.line}`), and the message
is displayed the same way in `get` and `describe`. If `--log-message` is omitted
on update, the current message is preserved. Passing an empty string
(`--log-message ""`) resets the message to the default:
`Hit on {frame.filename}:{frame.line}`.

### Notes

- identifiers can be either a mutable breakpoint ID or `filename:line`
- source locations resolve all matching breakpoints in the current workspace
- `--dry-run` is supported
- at least one of `--condition`, `--log-message`, or `--enabled` is required

## 6. Delete breakpoints

Delete a breakpoint by ID:

```bash
dtctl delete breakpoint dtctl-rule-123
```

Delete all breakpoints at a source location:

```bash
dtctl delete breakpoint OrderController.java:306
```

Delete all breakpoints in the current workspace:

```bash
dtctl delete breakpoint --all
```

### Delete behavior

- delete commands require confirmation by default
- `-y` or `--yes` skips confirmation
- `--dry-run` shows what would be deleted

Examples:

```bash
dtctl delete breakpoint --all -y
dtctl delete breakpoint OrderController.java:306 --dry-run
```

## 7. View snapshots

Use `dtctl get snapshots` to fetch snapshots captured by a breakpoint. Specify the breakpoint by location or stable rule ID:

```bash
# By location (filename:line)
dtctl get snapshots OrderController.java:306

# By stable rule ID (shown after dtctl create breakpoint or in dtctl get breakpoints)
dtctl get snapshots dtctl-rule-5bfb45a29fce7a46

# Decode snapshot payloads into readable fields
dtctl get snapshots OrderController.java:306 --decode-snapshots

# Full decoded tree with type annotations
dtctl get snapshots OrderController.java:306 --decode-snapshots=full

# Structured output
dtctl get snapshots OrderController.java:306 -o json
dtctl get snapshots OrderController.java:306 -o yaml

# Scope to a time window
dtctl get snapshots OrderController.java:306 \
  --default-timeframe-start 2024-01-01T00:00:00Z \
  --default-timeframe-end   2024-01-02T00:00:00Z
```

The `--decode-snapshots` flag enriches each record with a decoded `parsed_snapshot` field built from:

- `snapshot.data`
- `snapshot.string_map`

By default, `--decode-snapshots` simplifies variant wrappers to plain values (e.g., `{"type": "Integer", "value": 42}` becomes `42`). Use `--decode-snapshots=full` to preserve the full decoded tree with type annotations.

## Output and troubleshooting

### Default behavior

- successful mutating commands are quiet by default
- listing and describe commands use human-readable output by default

### Verbose and debug modes

Use `-v` or `--debug` when you want raw GraphQL payloads for troubleshooting.

### Structured output

Use `-o json` or `-o yaml` when you want automation-friendly output.

## Safety and dry-run

Live Debugger mutating commands follow `dtctl` safety conventions:

- filter updates use update safety checks
- breakpoint creation uses create safety checks
- edit operations use update safety checks
- delete operations use delete safety checks
- destructive delete operations support confirmation bypass with `-y`
- filter changes prompt for confirmation because they re-scope existing breakpoints; bypass with `-y`/`--yes`
- supported mutating commands provide `--dry-run`

## Example workflow

```bash
# Target a workload
dtctl update breakpoint --filters k8s.namespace.name:prod

# Create a breakpoint
dtctl create breakpoint OrderController.java:306

# List current breakpoints
dtctl get breakpoints

# Inspect status
dtctl describe breakpoint OrderController.java:306

# Update condition
dtctl update breakpoint OrderController.java:306 --condition "orderId != null"

# Disable the breakpoint
dtctl update breakpoint OrderController.java:306 --enabled false

# View snapshots (simplified)
dtctl query "fetch application.snapshots | sort timestamp desc | limit 5" --decode-snapshots

# View snapshots as YAML
dtctl query "fetch application.snapshots | sort timestamp desc | limit 5" --decode-snapshots -o yaml

# Delete the breakpoint
dtctl delete breakpoint OrderController.java:306
```
