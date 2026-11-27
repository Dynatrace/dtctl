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

- `dtctl update breakpoint` without a breakpoint identifier sets the workspace filters and needs `--filters` (or `--repo-scope`, see below); with an identifier it edits that breakpoint and takes neither as a filter source
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
`--filters`, and only when none of its values holds a `,`, which `--filters`
reads as the end of a pair; `--repo-scope <name>` sends every set, whatever
its values hold.

Filters set from an entry are not the entry. What they leave out, and what
they carry unverified, the commands say on every path that sets or previews
them, before the confirmation and before anything is sent: one line each on
stderr, and the same lines in `context.warnings` in agent mode.

- **Service ids** are not sent (see above).
- **A workload's cluster** (`cluster-uid` in the scope file) is not part of
  its filter set. `k8s.cluster.uid` has not been verified as a workspace
  label, and a label a workspace does not know selects nothing, so it is not
  sent: a workspace set from an entry that binds a cluster includes the same
  namespace and workload in other clusters. `repo-scope describe` says so
  under the sets too.
- **Filter segments** an entry references (`segments` in the scope file)
  narrow its queries and nothing else: a workspace is filtered by labels and
  cannot take a segment. The filters of an entry with segments are wider than
  its queries, and an entry that holds only segments renders none.
- **`service.name`** is sent, and is not verified as a workspace label.

```text
repo scope checkout's service ids are not sent: Live Debugger does not honour dt.entity.service as a workspace label
repo scope checkout binds a cluster, which a Live Debugger workspace is not narrowed by: the filters include the same workload in other clusters
repo scope checkout's filter segments are not applied: a workspace is filtered by labels and takes no segment, so the filters are wider than the entry's queries
repo scope checkout sets service.name, which is not verified as a workspace label: a filter set a workspace does not honour selects nothing, and a breakpoint under it is created and never hits; check with dtctl describe breakpoint <id>
```

- **A named entry.** `--repo-scope <name>` on `create breakpoint` and
  `update breakpoint` sets the workspace filters from that entry, every set,
  like `--filters` does (and with the same confirmation). It cannot be combined
  with `--filters`. To return a workspace to its repo scope's filters:
  `dtctl update breakpoint --repo-scope <name>`.
- **Nothing is set unasked.** `dtctl create breakpoint` without `--filters`
  or `--repo-scope` never writes workspace filters. A workspace's filters
  decide where every breakpoint in it can hit, and how a workspace treats the
  labels an entry renders has not all been verified against an environment
  (see [What is not verified](#what-is-not-verified)), so the choice stays
  yours. Where an entry covers the working directory, the command tells you:

  - in a workspace without filters it fails, as it does outside a linked
    repository, and the error names the flag:

    ```text
    workspace storefront/checkout has no filters, and a breakpoint needs them: repo scope checkout covers this directory, so pass --repo-scope checkout to set its filters (k8s.namespace.name:payments,k8s.workload.name:checkout), or --filters key:value
    ```

  - in a workspace whose filters are not the entry's, it creates the
    breakpoint and prints one line on stderr (in agent mode it is also a
    `context.suggestions` entry):

    ```text
    workspace storefront/checkout's filters (k8s.namespace.name:web) are not the ones repo scope checkout, which covers this directory, renders (k8s.namespace.name:payments,k8s.workload.name:checkout); to apply them: dtctl --context staging update breakpoint --repo-scope checkout
    ```

    The line names the context the create ran in (and the config, when
    `--config` or `DTCTL_CONFIG` named it), so it reaches the same environment when it is run
    later, under another current context.

  `DTCTL_NO_REPO_SCOPE=1` turns both off, and the commands then say what they
  said before repo scopes existed. `create breakpoint` has no
  `--no-repo-scope`: there is no default to turn off.

#### Which workspace a command works in

In a checkout whose scope file has entries for the context's environment, the
workspace is named after the repository: `<repo>/<entry>` where an entry covers
the working directory, and `<repo>` in every other directory of the checkout,
including one two entries tie on. `<repo>` is the last segment of the origin
remote. The checkout directory's base name is used instead when
`.git/config` has no origin to read (a worktree, which therefore gets a
workspace of its own) or when that segment is not a plain name of letters,
digits, `.`, `_` and `-`.

Everywhere else the workspace is named after the working directory's base
name, as it was before repo scopes existed: outside a checkout, in a checkout
without a scope file, and in one whose scope file has no entry for this
environment.

`--repo-scope <name>` uses `<repo>/<name>` wherever it runs, on every
breakpoint command: `get breakpoints`, `describe breakpoint`,
`delete breakpoint`, `get snapshots` and `update breakpoint <id>` take it only
to name the workspace. `DTCTL_NO_REPO_SCOPE` does not change the name.

A workspace is one environment's, so a context that does not exist or has no
environment URL fails every breakpoint command, dry runs included.

A scope file that is there and does not load (invalid YAML, an unknown key
inside an entry, a newer `apiVersion`, or no entry at all beside a top-level
key `dtctl` does not know, which is what a misspelled `environments:` looks
like) names no workspace, and `dtctl` does not guess one:

- `create`, `update` and `delete breakpoint`, and their dry runs, fail with
  what is wrong with the file and send nothing. Working in the directory's
  workspace instead would delete or re-scope breakpoints other than the ones
  the file points at.
- `get breakpoints`, `describe breakpoint` and `get snapshots` read the
  workspace named after the working directory and warn that it is not the one
  the file would name (on stderr, and in `context.warnings` in agent mode).
  With `--repo-scope <name>` they fail too, since the name asks for the file.

`dtctl repo-scope current` says what is wrong with the file. A file whose
entries name the workspace and which also holds a top-level key `dtctl` does
not know is used, and every breakpoint command says so once (on stderr, and in
`context.warnings`).

`create`, `update` and `delete breakpoint` in a workspace of a linked checkout
open with one line, also in `context.warnings`. Where the origin remote named
it, two things such a workspace relies on are not verified (see
[What is not verified](#what-is-not-verified)):

```text
workspace storefront/checkout is named after the repository, so every clone of it names the same one: it is not verified that Live Debugger accepts "/" in a workspace name, nor that a workspace is private to its user
```

Where the checkout directory named it, for want of an origin remote, no other
clone is promised the same workspace, and the line says that instead:

```text
workspace storefront-wt/checkout is named after the checkout directory, as no origin remote names the repository, so a clone in a directory of another name works in another one: it is not verified that Live Debugger accepts "/" in a workspace name
```

**Breakpoints from before the scope file.** Until a checkout has entries for
an environment, its breakpoints live in workspaces named after the directories
the commands ran in. Once it has entries, the commands run inside it use
`<repo>` and `<repo>/<entry>`; nothing is moved or deleted. They no longer
look in the directory-named workspaces, with one exception: a clone whose
directory is called like the repository, the usual case, has a root workspace
of the same name before and after, so `<repo>` still holds the breakpoints
created at the checkout root. Any other directory-named workspace stays
reachable from a directory of the same base name outside every linked
checkout:

```bash
mkdir -p /tmp/old-workspace/src && cd /tmp/old-workspace/src
dtctl get breakpoints          # the workspace "src"
dtctl delete breakpoint --all  # deletes every breakpoint in "src"
```

That directory is outside the checkout, so a `.dtctl.yaml` the repository
carries no longer applies there: name the context with `--context` if the
repository's file chose it.

Saving a further entry likewise moves the directories it covers to
`<repo>/<entry>`: from `<repo>` if no entry covered them, or from
`<repo>/<wider>` if a wider entry did. In a Gradle build the entry that covers
a shared library module follows the modules that depend on it, so linking a
second deployable can move such a module to `<repo>`. `delete breakpoint --all`
deletes every breakpoint of one workspace, those created from the other
directories that share it included: to clean up before saving an entry, delete
the breakpoints of the directories it will cover one by one, or use `--all`
only where the whole workspace may go. Afterwards the old workspace is the one
`--repo-scope <wider>` names, or `<repo>` from a directory no entry covers.

```bash
dtctl create breakpoint OrderController.java:306 --repo-scope checkout  # set the entry's filters, then create
dtctl update breakpoint --repo-scope checkout                           # replace the workspace filters
dtctl create breakpoint OrderController.java:306                        # the workspace keeps its filters
dtctl create breakpoint OrderController.java:306 --dry-run              # preview, sending nothing
```

The dry run sends nothing to the environment, not even the request that looks
a workspace up, because that request creates a workspace that does not exist
yet. It therefore does not know whether the workspace has filters, and says
that the breakpoint needs them and which flag sets them.

#### What is not verified

`k8s.namespace.name` with `k8s.workload.name`, and `dt.entity.process_group`,
have been seen to select processes; the labels of one set must all match, and
several sets are alternatives. The rest of what the repo scope integration
relies on has not been tried against an environment:

- whether a workspace name may contain `/`, as `<repo>/<entry>` does;
- whether a workspace is private to the user who created it, or shared by
  everyone who runs a command in a clone of the same repository. If it is
  shared, one user's filter change or `delete breakpoint --all` reaches
  another's breakpoints;
- whether a workspace honours `service.name` as a label;
- whether a workspace honours `k8s.cluster.uid`, which is why it is not sent;
- that a filter set whose label a workspace does not honour selects nothing,
  so a breakpoint under it is created without complaint and never hits. That
  is how an unhonoured `service.name` or `k8s.cluster.uid` is expected to
  show, and why nothing is set from an entry you did not name;
- whether a new workspace reports its filters, and its breakpoints, as empty
  or as absent, and what it answers to a filter update (`dtctl` reads these
  as "no filters", "no breakpoints" and "applied");
- whether a workspace created through `dtctl` ever has `sources`, a list a
  workspace holds beside its filters: every filter update sends it empty, as
  it did before repo scopes.

After setting filters from an entry, check with
`dtctl describe breakpoint <id> --repo-scope <name>` that the breakpoint
reaches the processes you expect. The entry is named because the filters were
set in `<repo>/<name>`, which is where a bare `describe breakpoint` looks only
from a directory that entry covers.

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
- `--repo-scope <name>` sets the filters from a repo scope entry instead; nothing is set from an entry you did not name (see [Filters from the repo scope](#filters-from-the-repo-scope))
- otherwise, when `--filters` is omitted, the workspace must already have filters configured
- setting the filters and creating the breakpoint are two requests. When the
  second fails after the first, the filters stay as set and are not rolled
  back: the error names the workspace, its filters before and after, and the
  commands that show them, restore them where one command can, and try the
  create again, each naming the context the create ran in (in agent mode
  these are the first `error.suggestions`)
- `--dry-run` is supported and sends nothing

Example:

```bash
dtctl create breakpoint OrderController.java:306 --dry-run
```

## 3. List breakpoints

List breakpoints in the current workspace:

```bash
dtctl get breakpoints
dtctl get breakpoints --repo-scope checkout   # the workspace <repo>/checkout, from any directory
```

The table is preceded by one line on stderr that names the workspace, since it
depends on where the command runs (see
[Which workspace a command works in](#which-workspace-a-command-works-in)):

```text
workspace: storefront/checkout
```

Agent mode and `-o json`/`-o yaml` print no such line. `describe breakpoint`
shows the workspace as a field, and `get snapshots` prints the same line;
both take `--repo-scope <name>` to name the workspace as well.

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
- name the breakpoint before `--enabled`, or write the value as `--enabled=false`: `--enabled false` with no breakpoint named before it leaves `false` where the identifier goes, and an argument that reads as a boolean there (`true`, `false`, `1`, `0`, `t`, `f`) is refused. A breakpoint whose identifier is such a word is named before the flag and is never refused. A value written with `=` is the flag's own, so `--enabled=false 1` disables breakpoint `1`; a second argument is the flag's value only right after the one identifier (`<id> --enabled false`), and anywhere else it is refused rather than read as a value

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

- successful mutating commands print one line, ending with the workspace they worked in; in a linked checkout `create`, `update` and `delete breakpoint` first say on stderr what about the workspace is not verified (see [Which workspace a command works in](#which-workspace-a-command-works-in))
- listing and describe commands use human-readable output by default

### Verbose and debug modes

Use `-v` or `--debug` when you want raw GraphQL payloads for troubleshooting.

### Structured output

Use `-o json` or `-o yaml` when you want automation-friendly output.

## Safety and dry-run

Live Debugger mutating commands follow `dtctl` safety conventions:

- filter updates use update safety checks, whichever command makes them:
  `create breakpoint --filters` and `create breakpoint --repo-scope` need the
  update level as well as the create level, because a workspace's filters
  re-scope every breakpoint in it. A `readwrite-mine` context therefore creates
  breakpoints in a workspace that has filters and refuses to set them, on
  create as on `update breakpoint --filters`. (Up to 0.47 `create breakpoint
  --filters` was checked as a create only and went through there.)
- breakpoint creation uses create safety checks
- edit operations use update safety checks
- delete operations use delete safety checks
- destructive delete operations support confirmation bypass with `-y`
- filter changes prompt for confirmation because they re-scope existing breakpoints; bypass with `-y`/`--yes`
- supported mutating commands provide `--dry-run`, which needs no safety
  level and changes nothing. `create breakpoint --dry-run` and both forms of
  `update breakpoint --dry-run` send no request at all. `delete breakpoint
  --dry-run` has to read what it would delete, so it sends the two reads
  `get breakpoints` sends (the workspace, then its breakpoints) and nothing
  else; the first of them creates the workspace, empty, when it does not
  exist yet. It also asks its confirmation before it previews, unless `--yes`
- when the repository's scope file is there and does not load, every
  mutating command, and its dry run, is refused before anything is sent (see
  [Which workspace a command works in](#which-workspace-a-command-works-in))

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
