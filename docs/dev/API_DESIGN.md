# dtctl API Design

A kubectl-inspired CLI tool for managing Dynatrace platform resources.

## Table of Contents

- [Design Principles](#design-principles)
- [Command Structure](#command-structure)
- [Resource Types](#resource-types)
- [Common Operations](#common-operations)
- [Configuration & Context](#configuration--context)
  - [Current User Context](#current-user-context)
  - [Filtering Resources by Owner](#filtering-resources-by-owner---mine)
- [Output Formats](#output-formats)
- [Examples](#examples)

## Design Principles

### Core Philosophy

**"Kubectl for Dynatrace"**: Leverage existing DevOps muscle memory. If a user knows kubectl, they should intuitively know how to use dtctl.

**Developer Experience (DX) First**: Prioritize speed, interactivity, and "human-readable" inputs over 1:1 API purity.

**AI-Native**: The tool must function as an efficient backend for AI Agents, providing structured discovery and error recovery.

### 1. Command Structure (The Grammar)

Strictly follow the **verb + noun** pattern.

**Management (CRUD)**: `dtctl get | describe | edit | delete | apply [resource]`
- Resources: dashboard, notebook, alert, slo, workflow, etc.

**Execution (Data)**: `dtctl query [dql-string]` and `dtctl exec [resource]`

**Separation of Concerns**: Never mix configuration flags with data query logic.

### 2. The "No Leaky Abstractions" Rule (Data Querying)

Do not invent a new query language via CLI flags.

**Passthrough**: The query command is a "dumb pipe" for DQL.
- ✅ `dtctl query "fetch logs | filter ..."`
- ❌ `dtctl get logs --filter-status=ERROR` (Anti-pattern)

**File Support**: Always support reading queries from files (`-f query.dql`) to handle complex, multi-line logic.

**Templating**: Allow basic variable substitution in DQL files (`--set host=h-123`) to make them reusable.

### 3. Configuration Management (The "Monaco Bridge")

dtctl is the "Runtime" companion to Monaco's "Build time."

**Unit Testing**: Enable rapid testing of Monaco-style JSON templates without running a full pipeline.

**Apply Logic**:
- **Strict Mode**: Validate payloads against the API schema before sending.
- **Template Injection**: Support `dtctl apply -f template.json --set name="Dev"` to render variables client-side.
- **Idempotency**: `apply` must handle the logic of "Create (POST) if new, Update (PUT) if exists."

### 4. Input/Output Standards (Human vs. Machine)

**Input (Write)**: Support YAML with Comments.
- **Principle**: "Humans write YAML, Machines speak JSON."
- **Implementation**: Automatically convert user YAML to API-compliant JSON on the fly.

**Output (Read)**:
- **Default**: Human-readable TUI tables (ASCII).
- **JSON** (`-o json`): Raw API response for piping (jq).
- **YAML** (`-o yaml`): Reconstructed YAML for copy-pasting.
- **Snapshot decoding** (`--decode-snapshots`): Decoded Live Debugger snapshot payloads, composable with any output format.
- **Charts**: Sparklines, bar charts, and line charts for timeseries data.

### 5. Handling Identity (Naming)

**Resolution Loop**: Never assume names are unique.
- If a user asks for `dtctl get dashboard "Production"` and multiple exist, stop and ask (Interactive Disambiguation).
- Display NAME and ID side-by-side in lists.

**Safety**: Destructive actions (delete, apply) on ambiguous names must require confirmation or an exact UUID.

### 6. AI Enablement Strategy

Design features specifically to help LLMs drive the tool.

**Self-Discovery**: Ensure `--help` provides exhaustive, structured context.

**Machine Output**: Implement a `--plain` flag that strips ANSI colors, spinners, and interactive prompts, returning strict JSONL/YAML for agents.

**Error Messages**: Errors should be descriptive and suggest the fix (e.g., "Unknown flag --ownr, did you mean --owner?") to allow Agentic auto-correction.

### 7. kubectl-like User Experience
- **Verb-noun pattern**: `dtctl <verb> <resource> [options]`
- **Consistent flags**: Same flags work across similar operations
- **Multiple output formats**: Table (human), JSON, YAML, TOON (LLM-optimised), charts
- **Declarative configuration**: Apply YAML/JSON files to create/update resources
- **Context management**: Switch between environments easily

### 8. Resource-Oriented Design
- Every Dynatrace API concept is exposed as a resource
- Resources have standard CRUD operations where applicable

### 9. Watch Mode Pattern

**Philosophy**: Enable real-time monitoring without custom query filters.

**Implementation**:
- Add `--watch`, `--interval`, and `--watch-only` flags to all `get` commands
- Use polling with configurable intervals (default: 2s, minimum: 1s)
- Display incremental changes with kubectl-style prefixes:
  - `+` (green) for additions
  - `~` (yellow) for modifications
  - `-` (red) for deletions
- Graceful shutdown on Ctrl+C via context cancellation
- Automatic retry on transient errors (timeouts, rate limits, network issues)

**Usage Pattern**:
```bash
# Watch workflows
dtctl get workflows --watch

# Watch with custom interval
dtctl get workflows --watch --interval 5s

# Live query results
dtctl query 'fetch logs | filter status == "ERROR"' --live

# Only show changes (skip initial state)
dtctl get workflows --watch --watch-only
```

**Design Decisions**:
- ✅ Use polling (simple, universal compatibility)
- ❌ WebSocket streaming (limited API support, complex implementation)
- ✅ Incremental updates (better UX than full refresh)
- ✅ Memory-efficient (only store last state)
- ✅ Works with existing filters and flags
- Resources can have sub-resources (e.g., `workflow/executions`)
- Resources support filtering, sorting, and field selection

### 9. Progressive Disclosure
- Simple commands for common tasks
- Advanced options available via flags
- Comprehensive help at every level

### 10. Embeddable by Construction
**The CLI is the API.** A service embeds the same command surface rather than a
parallel one, so there is nothing to drift: `pkg/engine` takes a command line
exactly as a user would type it and returns the bytes the CLI would have
printed.

- **No second surface** — anything typeable is callable, and output is
  byte-identical (enforced by a CLI-vs-engine equality test, not by review)
- **The invocation carries its world** — credentials, files, streams, and
  environment arrive per request; nothing about the host process leaks into one,
  and nothing about one request survives into the next
- **Host abilities are opt-in** — subprocesses, editors, browsers, and the host
  disk are granted explicitly by the CLI and withheld from embedded callers, so
  they are structurally unreachable rather than merely discouraged
- **Practical consequence for contributors** — user-supplied file paths go
  through `pkg/vfs`, subprocess spawns go through a capability gateway, and
  command bodies return errors instead of calling `os.Exit`

See [SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md).

## Command Structure

### Core Verbs

```
get         - List or retrieve resources
describe    - Show detailed information about a resource
create      - Create a resource from file or arguments
delete      - Delete resources
edit        - Edit a resource interactively (supports YAML and JSON)
apply       - Apply configuration from file (create or update, supports templates)
logs        - Print logs for a resource
query       - Execute a DQL query (with template support)
exec        - Execute a workflow or function
history     - Show version history (snapshots) of a document
restore     - Restore a document to a previous version
claim       - Claim access to a shared resource (claim environment-share)
wait        - Wait for a specific condition (query results, resource state)
alias       - Manage command aliases (set, list, delete, import, export)
ctx         - Quick context management (list, switch, describe, set, delete)
doctor      - Health check (config, context, token, connectivity, auth)
diff        - Show differences between local and remote resources
commands    - Machine-readable command catalog for AI agents (JSON/YAML, --brief, howto)
serve       - Run dtctl as a server, one subcommand per protocol (serve http)
              (development-tier: registered only when opted in, see docs/STABILITY.md)

# (not implemented yet)
# patch       - Update specific fields of a resource
# explain     - Show documentation for a resource type
```

### Syntax Pattern

```bash
dtctl [verb] [resource-type] [resource-name] [flags]

# Examples:
dtctl get dashboards
dtctl get notebooks --name "analysis"
dtctl describe dashboard "Production Dashboard"
dtctl delete workflow my-workflow-id
dtctl apply -f workflow.yaml
dtctl query "fetch logs | limit 10"
```

### Global Flags

```
--context string      # Use a specific context
-o, --output string   # Output format: json|yaml|toon|csv|table|wide|chart|sparkline|barchart|braille
--plain               # Plain output for machine processing (no colors, no interactive prompts)
--no-headers          # Omit headers in table output
-v, --verbose         # Verbose output (-v for details, -vv for full HTTP debug)
--debug               # Enable debug mode (full HTTP logging, equivalent to -vv)
--field-selector string # Filter by fields (e.g., owner=me,type=notebook)
-A, --agent           # Agent output mode: wrap all output in a structured JSON envelope
--no-agent            # Disable auto-detected agent mode
-w, --watch           # Watch for changes (with --interval, --watch-only)

# (not implemented yet)
# -l, --selector        # Label selector for filtering
```

**Color control** follows the [no-color.org](https://no-color.org/) standard:
- `NO_COLOR` env var (any non-empty value) disables ANSI color output
- `FORCE_COLOR=1` env var overrides TTY detection to force color on
- Color is automatically disabled when stdout is not a TTY (piped output)
- `--plain` flag also disables color (and interactive prompts)

### Agent Mode (`--agent` / `-A`)

When `--agent` (or `-A`) is passed, all CLI output is wrapped in a structured JSON envelope designed for AI agents and automation consumers:

```json
{
  "ok": true,
  "result": [ ... ],
  "context": {
    "total": 5,
    "has_more": true,
    "verb": "get",
    "resource": "workflow",
    "suggestions": [
      "Run 'dtctl describe workflow <id>' for details",
      "More results available. Use '--chunk-size 0' to retrieve all, or filter with DQL"
    ]
  }
}
```

**Envelope fields:**
- `ok` (bool) — `true` for success, `false` for errors. Always present.
- `result` — The command output data. Always present (may be `null`).
- `error` (object, optional) — Structured error with `code`, `message`, `operation`, `status_code`, `request_id`, `suggestions`; DQL errors add `position` and `snippet` (see [AGENT_MODE.md](../AGENT_MODE.md#error-responses)).
- `context` (object, optional) — Operational metadata: `total`, `has_more`, `verb`, `resource`, `suggestions`, `warnings`, `duration`, `links`.

**Auto-detection:** Agent mode is automatically enabled when dtctl detects it is running inside an AI agent environment (via the `aidetect` package). Use `--no-agent` to opt out. Auto-detection is skipped if an explicit `--output` format is set.

**Behavior:** Agent mode implies `--plain` (no colors, no interactive prompts).

### Debug Mode

The `--debug` flag (or `-vv`) enables full HTTP request/response logging for troubleshooting:

```bash
dtctl get workflows --debug

# Shows:
# ===> REQUEST <===
# GET https://abc12345.apps.dynatrace.com/platform/automation/v1/workflows
# HEADERS:
#     User-Agent: dtctl/0.12.0
#     Authorization: [REDACTED]
#
# ===> RESPONSE <===
# STATUS: 200 OK
# TIME: 234ms
# HEADERS:
#     Content-Type: application/json
# BODY:
# {"workflows": [...]}
```

Sensitive headers (Authorization, X-API-Key, Cookie, etc.) are always redacted in debug output for security.

### Error Messages & Troubleshooting

dtctl provides enhanced error messages with contextual troubleshooting suggestions:

**Example 401 Unauthorized:**
```
Failed to get workflows (HTTP 401): Authentication failed

Request ID: abc-123-def-456

Troubleshooting suggestions:
  • Token may be expired or invalid. Run 'dtctl config get-context' to check your configuration
  • Verify your API token has not been revoked in the Dynatrace console
  • Try refreshing your authentication with 'dtctl context set' and a new token
```

**Example 403 Forbidden:**
```
Failed to delete dashboard (HTTP 403): Forbidden

Troubleshooting suggestions:
  • Insufficient permissions. Check that your API token has the required scopes
  • Re-run the same command with --check-scopes to verify the scopes it requires
  • Token scopes and IAM policy permissions are separate gates — if --check-scopes reports "ok", or "unknown" because the token is not introspectable, the tenant's IAM policy is what denies the request
  • View current context and safety level: 'dtctl config get-context'
  • If using a 'readonly' context, switch to a context with write permissions
  • Review required token scopes in the documentation
```

Common HTTP status codes:
- **401**: Authentication error (invalid/expired token)
- **403**: Permission error (insufficient scopes or readonly context)
- **404**: Resource not found
- **429**: Rate limited (dtctl auto-retries)
- **500/502/503/504**: Server errors (dtctl auto-retries)

Use `--debug` to see full HTTP details when troubleshooting.

### AI Agent Detection

dtctl automatically detects when running under AI coding assistants. Detection enables [agent mode](../AGENT_MODE.md#auto-detection) (opt out with `--no-agent`) and names the agent in the User-Agent header for telemetry. The environment variables, and the order they are checked in, are listed in [AGENT_MODE.md](../AGENT_MODE.md#auto-detection); the implementation is `sdk/agentmode/detect.go`.

Example User-Agent: `dtctl/0.12.0 (AI-Agent: opencode)`

This telemetry helps improve the CLI experience for AI-assisted workflows.

### Skills Management

dtctl includes a `skills` command for installing skill files that teach AI coding assistants how to use dtctl effectively. Skills follow the [agentskills.io](https://agentskills.io) open standard.

**Supported agents**: claude, codex, copilot, cursor, junie, kiro, opencode, openclaw

**Installation modes**:

| Mode | Command | Path |
|------|---------|------|
| Per-agent (project) | `dtctl skills install --for claude` | `<project>/.claude/skills/dtctl/` |
| Per-agent (global) | `dtctl skills install --for claude --global` | `~/.claude/skills/dtctl/` |
| Cross-client (project) | `dtctl skills install --cross-client` | `<project>/.agents/skills/dtctl/` |
| Cross-client (global) | `dtctl skills install --cross-client --global` | `~/.agents/skills/dtctl/` |
| Auto-detect | `dtctl skills install` | Detects agent from env vars |

**Cross-client directory**: The `--cross-client` flag installs to the shared `.agents/skills/` directory defined by the agentskills.io convention. Skills installed here are automatically discovered by any compatible agent without needing per-agent installation. The `--for cross-client` syntax on `status` is also supported.

**Subcommands**: `install`, `uninstall`, `status`

**Agent-mode output**: All subcommands support `--agent` / `-A` for structured JSON output.

## Resource Types

> **Note**: Like kubectl, dtctl supports both singular and plural resource names (e.g., `dashboard` or `dashboards`, `notebook` or `notebooks`), as well as short aliases for convenience.

### 1. Documents (Generic)

The `documents` resource provides type-agnostic access to the Dynatrace Documents API. It lists **all** document types by default and always shows the `TYPE` column. Use this as the escape hatch for document types beyond `dashboard` and `notebook` (e.g. `launchpad`, custom app documents).

```bash
# Resource name: document/documents (short: doc)
dtctl get documents                              # List all documents (all types)
dtctl get documents --type launchpad             # Filter by any type
dtctl get documents --type my-app:config         # Custom app document types
dtctl get documents --name "production"          # Filter by name
dtctl get documents --mine                       # Only my documents
dtctl get documents --types                      # Discover distinct types and counts
dtctl get document <id>                          # Get specific document by ID (any type)
dtctl describe document <id-or-name>             # Show detailed metadata
dtctl create document -f file.json --type launchpad  # Create with explicit type
dtctl create document -f file.yaml               # Type read from payload "type" field
dtctl edit document <id-or-name>                 # Edit in $EDITOR (YAML by default)
dtctl edit document <id> --format=json           # Edit in JSON format
dtctl delete document <id-or-name>               # Move to trash
dtctl delete document "My Launchpad" -y          # Delete by name, skip confirmation
dtctl history document <id-or-name>              # Show version history (snapshots)
dtctl restore document <id-or-name> <version>    # Restore to a previous snapshot
```

> **Relationship to `dashboards`/`notebooks`**: `dtctl get documents` is a superset — it includes dashboards and notebooks too. The type-specific commands (`dtctl get dashboards`, `dtctl get notebooks`) remain as convenient aliases with type-specific UX (tile counts, known app URLs). Nothing is deprecated.

### 2. Dashboards

Dashboards are visual documents for monitoring and analysis.

```bash
# Resource name: dashboard/dashboards (short: dash, db)
dtctl get dashboards                             # List all dashboards
dtctl get dashboard <id>                         # Get specific dashboard by ID
dtctl get dashboards --name "production"         # Filter by name
dtctl describe dashboard <id>                    # Detailed view with metadata
dtctl describe dashboard "Production Dashboard"  # Describe by name
dtctl edit dashboard <id>                        # Edit in $EDITOR (YAML by default)
dtctl edit dashboard "My Dashboard"              # Edit by name
dtctl edit dashboard <id> --format=json          # Edit in JSON format
dtctl delete dashboard <id>                      # Move to trash
dtctl delete dashboard "Old Dashboard" -y        # Delete by name, skip confirmation
dtctl create dashboard -f dashboard.yaml         # Create new dashboard
dtctl apply -f dashboard.yaml                    # Create or update

# Sharing
dtctl share dashboard <id> --user <user-sso-id>  # Share with user (read access)
dtctl share dashboard <id> --user <id> --access read-write  # Read-write access
dtctl share dashboard <id> --group <group-sso-id> # Share with group
dtctl unshare dashboard <id> --user <user-sso-id> # Remove user access
dtctl unshare dashboard <id> --all               # Remove all user and group shares
dtctl share dashboard <id> --environment link    # Environment share link (prints #share= URL)
dtctl share dashboard <id> --environment link --access read-write  # Link with write access
dtctl share dashboard <id> --environment public  # Everyone in the environment can read it
dtctl unshare dashboard <id> --environment link  # Delete the environment share link(s)
dtctl unshare dashboard <id> --environment public  # Make it private again
dtctl share dashboard <id> --environment link,public  # Both at once (unshare too)
dtctl claim environment-share <share-id|share-url> # Claim a #share= link; prints the document ID

# (not implemented yet)
# dtctl lock dashboard <id>                        # Acquire active lock
# dtctl unlock dashboard <id>                      # Release active lock
```

### 3. Notebooks

Notebooks are interactive documents for data exploration and analysis.

```bash
# Resource name: notebook/notebooks (short: nb)
dtctl get notebooks                              # List all notebooks
dtctl get notebook <id>                          # Get specific notebook by ID
dtctl get notebooks --name "analysis"            # Filter by name
dtctl describe notebook <id>                     # Detailed view with metadata
dtctl describe notebook "Analysis Notebook"      # Describe by name
dtctl edit notebook <id>                         # Edit in $EDITOR (YAML by default)
dtctl edit notebook "My Notebook"                # Edit by name
dtctl edit notebook <id> --format=json           # Edit in JSON format
dtctl delete notebook <id>                       # Move to trash
dtctl delete notebook "Old Notebook" -y          # Delete by name, skip confirmation
dtctl create notebook -f notebook.yaml           # Create new notebook
dtctl apply -f notebook.yaml                     # Create or update

# Sharing
dtctl share notebook <id> --user <user-sso-id>   # Share with user (read access)
dtctl share notebook <id> --user <id> --access read-write  # Read-write access
dtctl share notebook <id> --group <group-sso-id> # Share with group
dtctl unshare notebook <id> --user <user-sso-id> # Remove user access
dtctl unshare notebook <id> --all                # Remove all user and group shares
dtctl share notebook <id> --environment link     # Environment share link (prints #share= URL)
dtctl share notebook <id> --environment public   # Everyone in the environment can read it
dtctl unshare notebook <id> --environment link   # Delete the environment share link(s)
dtctl unshare notebook <id> --environment public # Make it private again

# (not implemented yet)
# dtctl lock notebook <id>                         # Acquire active lock
```

### 4. Document Version History (Snapshots)

These operations apply to dashboards, notebooks, and any generic document type. Snapshots capture document content at specific points in time and can be used to restore previous versions.

```bash
# View version history
dtctl history dashboard <id-or-name>             # List dashboard snapshots
dtctl history notebook <id-or-name>              # List notebook snapshots
dtctl history document <id-or-name>              # List snapshots for any document type
dtctl history dashboard "Production Dashboard"   # By name
dtctl history notebook "Analysis Notebook" -o json  # Output as JSON

# Restore to previous version
dtctl restore dashboard <id-or-name> <version>   # Restore dashboard to version
dtctl restore notebook <id-or-name> <version>    # Restore notebook to version
dtctl restore document <id-or-name> <version>    # Restore any document type to version
dtctl restore dashboard "My Dashboard" 5         # Restore by name to version 5
dtctl restore notebook "My Notebook" 3 --force   # Skip confirmation

# Notes:
# - Snapshots are opt-in: pass --create-snapshot on update/apply/edit to capture
#   the pre-update content (--snapshot-description labels it)
# - At most 5 snapshots per document per minute are accepted
# - Maximum 50 snapshots per document (oldest deleted when exceeded)
# - Snapshots auto-delete after 30 days
# - Only document owner can restore snapshots
# - Restoring creates a snapshot of current state before restoring

# Trash management (not implemented yet)
# dtctl get trash                                  # List deleted documents
# dtctl restore trash <id>                         # Restore from trash
# dtctl delete trash <id> --permanent              # Permanently delete
```

### 5. Service Level Objectives (SLOs)

```bash
# Resource name: slo/slos
dtctl get slos                                   # List all SLOs
dtctl get slos --filter 'name~my-service'        # Filter by name
dtctl describe slo <id>                          # Show SLO details
dtctl create slo -f slo-definition.yaml          # Create SLO
dtctl delete slo <id>                            # Delete SLO
dtctl apply -f slo-definition.yaml               # Create or update

# SLO Templates
dtctl get slo-templates                          # List templates
dtctl describe slo-template <id>                 # Template details
dtctl create slo --from-template <template-id>   # Create from template

# Evaluation
dtctl exec slo <id>                              # Evaluate SLO now
dtctl exec slo <id> --timeout 60                 # Custom timeout (seconds)
dtctl exec slo <id> -o json                      # Output as JSON
```

### 6. Automation Workflows

```bash
# Resource name: workflow/workflows (short: wf)
dtctl get workflows                              # List workflows
dtctl get workflow <id>                          # Get specific workflow
dtctl describe workflow <id>                     # Workflow details by ID
dtctl describe workflow "My Workflow"            # Workflow details by name
dtctl edit workflow <id>                         # Edit in $EDITOR (YAML by default)
dtctl edit workflow "My Workflow"                # Edit by name
dtctl edit workflow <id> --format=json           # Edit in JSON format
dtctl delete workflow <id>                       # Delete workflow by ID
dtctl delete workflow "Old Workflow"             # Delete by name (with confirmation)
dtctl delete workflow "Old Workflow" -y          # Delete by name, skip confirmation
dtctl apply -f workflow.yaml                     # Create or update

# Workflow execution
dtctl exec workflow <id>                         # Run workflow
dtctl exec workflow <id> --params key=value      # Run with parameters
dtctl exec workflow <id> --wait                  # Run and wait for completion
dtctl exec workflow <id> --wait --timeout 10m    # Run with custom timeout

# Workflow Executions (sub-resource)
dtctl get workflow-executions                    # List all executions
dtctl get workflow-executions -w <workflow-id>   # List executions for workflow
dtctl get wfe <execution-id>                     # Get specific execution
dtctl describe workflow-execution <execution-id> # Execution details with tasks
dtctl describe wfe <execution-id>                # Short alias

# Execution Logs
dtctl logs workflow-execution <execution-id>     # View execution logs
dtctl logs wfe <execution-id>                    # Short alias
dtctl logs wfe <execution-id> --follow           # Stream logs in real-time
dtctl logs wfe <execution-id> --all              # Full logs for all tasks
dtctl logs wfe <execution-id> --task <name>      # Logs for specific task

# Version History
dtctl history workflow <id-or-name>              # List workflow versions
dtctl history workflow "My Workflow"             # By name
dtctl history workflow <id> -o json              # Output as JSON

# Restore Previous Version
dtctl restore workflow <id-or-name> <version>    # Restore to version
dtctl restore workflow "My Workflow" 5           # Restore by name
dtctl restore workflow <id> 3 --force            # Skip confirmation
```

### 7. Identity & Access Management (IAM)

```bash
# Users
dtctl get users                                  # List users
dtctl describe user <id>                         # User details
dtctl get users --group <group-id>               # Users in group

# Groups
dtctl get groups                                 # List groups
dtctl describe group <id>                        # Group details

# (not implemented yet)
# dtctl create group -f group.yaml                 # Create group
# dtctl delete group <id>                          # Delete group

# Permissions & Policies (not implemented yet)
# dtctl get policies                               # List policies
# dtctl describe policy <id>                       # Policy details
# dtctl create policy -f policy.yaml               # Create policy
# dtctl get permissions --user <id>                # User's permissions
```

### 8. Grail Data & Queries

```bash
# DQL Queries
dtctl query "fetch logs | limit 100"             # Execute DQL query
dtctl query -f query.dql                         # Execute from file
dtctl query "fetch logs" -o json                 # Output as JSON
dtctl query "fetch logs" -o yaml                 # Output as YAML
dtctl query "fetch logs" -o table                # Output as table

# DQL with template variables
dtctl query -f query.dql --set host=h-123        # With variable substitution
dtctl query -f query.dql --set host=h-123 --set timerange=2h

# Template Syntax:
#   Use {{.variable}} to reference variables
#   Use {{.variable | default "value"}} for default values

# Wait for Query Results
# Poll a query until a specific condition is met
dtctl wait query "fetch spans | filter test_id == 'test-123'" --for=count=1 --timeout 5m
dtctl wait query 'fetch logs | filter status == "ERROR"' --for=any --timeout 2m
dtctl wait query -f query.dql --set test_id=my-test --for=count-gte=1

# Wait conditions:
#   count=N       - Exactly N records
#   count-gte=N   - At least N records (>=)
#   count-gt=N    - More than N records (>)
#   count-lte=N   - At most N records (<=)
#   count-lt=N    - Fewer than N records (<)
#   any           - Any records (count > 0)
#   none          - No records (count == 0)

# Wait with custom backoff strategy
dtctl wait query "..." --for=any \
  --min-interval 500ms --max-interval 15s --backoff-multiplier 1.5

# Wait and output results when condition is met
dtctl wait query "..." --for=count=1 -o json > result.json

# Storage Buckets
# Resource name: bucket/buckets (short: bkt)
dtctl get buckets                                # List storage buckets
dtctl get bucket <bucket-name>                   # Get specific bucket
dtctl describe bucket <bucket-name>              # Bucket details
dtctl create bucket -f bucket.yaml               # Create bucket
dtctl delete bucket <bucket-name>                # Delete bucket
dtctl apply -f bucket.yaml                       # Create or update bucket

# Fieldsets (not implemented yet)
# dtctl get fieldsets                              # List fieldsets
# dtctl describe fieldset <id>                     # Fieldset details
# dtctl create fieldset -f fieldset.yaml           # Create fieldset

# Filter Segments
dtctl get segments                                # List filter segments
dtctl get segments <uid>                          # Get segment by UID
dtctl describe segment <uid>                      # Segment details
dtctl create segment -f segment.yaml              # Create segment
dtctl edit segment <uid>                          # Edit segment in $EDITOR
dtctl delete segment <uid>                        # Delete segment
dtctl apply -f segment.yaml                       # Create-or-update segment
dtctl query "fetch logs" --segment <uid>          # Query with segment filter
dtctl query "fetch logs" --segments-file f.yaml   # Query with segments file (supports variables)

# Storage usage info (not implemented yet)
# dtctl get bucket-usage                           # Storage usage info
```

### 9. Notifications

```bash
# Resource name: notification/notifications (short: notif)
dtctl get notifications                          # List event notifications
dtctl get notification <id>                      # Get specific notification
dtctl get notifications --type <type>            # Filter by notification type
dtctl delete notification <id>                   # Delete notification

# (not implemented yet)
# dtctl create notification -f notif.yaml          # Create notification
```

### 10. App Engine

```bash
# Apps (Registry)
dtctl get apps                                   # List installed apps
dtctl describe app <id>                          # App details
dtctl delete app <id>                            # Uninstall app

# App Functions (from installed apps)
# Resource name: function/functions (short: fn, func)
dtctl get functions                              # List all functions across all apps
dtctl get functions --app <app-id>               # List functions for a specific app
dtctl get function <app-id>/<function-name>      # Get specific function details
dtctl get functions -o wide                      # Show title, description, resumable, stateful
dtctl get functions -o json                      # JSON output with all metadata

# Describe function with detailed information
dtctl describe function <app-id>/<function-name> # Show function details and usage
dtctl describe function <app-id>/<function-name> -o json  # JSON output

# Execute functions
dtctl exec function <app-id>/<function-name>     # Execute function (GET)
dtctl exec function <app-id>/<function-name> --method POST --payload '{"key":"value"}'
dtctl exec function <app-id>/<function-name> --method POST --data payload.json  # payload from file (- for stdin)
dtctl exec function <app-id>/<function-name> -o json  # JSON output

# Deferred (async) execution for resumable functions
dtctl exec function <app-id>/<function-name> --defer

# Ad-hoc JavaScript execution (no app deployment)
dtctl exec function --code 'export default async function() { return "hello" }'
dtctl exec function -f script.js --payload '{"input":"data"}'  # -f - reads code from stdin
# dtctl describe deferred-execution <execution-id> # Execution details

# Function Executor (ad-hoc code execution)
dtctl exec function -f script.js                 # Execute JavaScript file
dtctl exec function -f script.js --payload '{"input":"data"}'
dtctl exec function --code 'export default async function() { return "hello" }'
dtctl get sdk-versions                           # List available SDK versions

# App Intents
# Resource name: intent/intents
# Intents enable inter-app communication by defining entry points that apps expose
dtctl get intents                                # List all intents across all apps
dtctl get intents --app <app-id>                 # List intents for a specific app
dtctl get intent <app-id>/<intent-id>            # Get specific intent details
dtctl get intents -o wide                        # Show app ID and required properties
dtctl describe intent <app-id>/<intent-id>       # Show intent details, properties, and usage

# Find matching intents for data
dtctl find intents --data <key>=<value>          # Find intents matching data
dtctl find intents --data trace_id=abc,timestamp=2026-02-02T16:04:19.947Z
dtctl find intents --data log_id=xyz789 -o json  # JSON output

# Generate and open intent URLs
dtctl open intent <app-id>/<intent-id> --data <key>=<value>  # Generate intent URL
dtctl open intent <app-id>/<intent-id> --data trace_id=abc123,timestamp=now
dtctl open intent <app-id>/<intent-id> --data-file payload.json  # From JSON file
dtctl open intent <app-id>/<intent-id> --data-file - # From stdin
dtctl open intent <app-id>/<intent-id> --data trace_id=abc --browser  # Open in browser

# EdgeConnect
# Resource name: edgeconnect/edgeconnects (short: ec)
dtctl get edgeconnects                           # List EdgeConnect configs
dtctl get edgeconnect <id>                       # Get specific EdgeConnect
dtctl describe edgeconnect <id>                  # EdgeConnect details
dtctl create edgeconnect -f edgeconnect.yaml     # Create EdgeConnect
dtctl delete edgeconnect <id>                    # Delete EdgeConnect
```

### 11. OpenPipeline

**Note**: The OpenPipeline API for managing configurations (`/platform/openpipeline/v1/configurations`) is deprecated,
and has been migrated to Settings API v2. Only to validate DQL processors, or Matchers use the OpenPipeline API, CRUD operations
are using the Settings API.


```bash
# List configurations (uses Settings 2.0)
dtctl get openpipeline configurations --schema <schema_id>
dtctl get openpipeline configurations --schema builtin:openpipeline.logs.pipelines

# Get configuration (uses Settings 2.0)
dtctl get openpipeline configurations <object-id>

# Create configuration (uses Settings 2.0)
dtctl create openpipeline configuration --type <id> 
dtctl create openpipeline configuration --type logs 
dtctl apply -f pipeline-config.yaml

# Update configuration (uses Settings 2.0)
dtctl update openpipeline configuration <object-id> -f config.yaml # Update existing
dtctl update openpipeline configuration <object-id> -f config.yaml --set version=v2

# Delete configuration (uses Settings 2.0)
dtctl delete openpipeline configuration <object-id>                
dtctl delete openpipeline configuration <object-id> -y             # Skip confirmation

# Matcher Operations
dtctl verify openpipeline matcher 'matchesValue(content, "error")'

# DQL Processor Operations
dtctl verify openpipeline processor 'fieldsAdd(environment: "production")'

# Preview a processor with sample data (requires JSON file with processor + sample record)
dtctl exec openpipeline preview-processor -f preview-request.json
dtctl exec openpipeline preview-processor -f preview-request.json -o json
```
Sample payload for preview-processor:
```json
{
	"processor": {
		"type": "fieldsRename",
		"enabled": false,
		"editable": true,
		"id": "hostname-field-normalizer",
		"description": "hostname field normalizer",
		"matcher": "isNotNull(\"hostname\")",
		"sampleData": "{\"hostname\": \"raspberry-pi 4\",\"ip\":\"10.0.0.123\"}",
		"fields": [
			{
				"fromName": "hostname",
				"toName": "host.name"
			},
			{
				"fromName": "ip",
				"toName": "ip.address"
			}
		]
	}
}
```



### 12. Settings API v2

Settings API v2 provides access to Dynatrace configuration objects including OpenPipeline configurations, monitoring settings, and other environment settings. Each settings type is defined by a schema, and objects are instances of these schemas.

```bash
# Schemas (Settings Types)
# Resource name: settings-schemas (short: schema, schemas)
dtctl get settings-schemas                       # List all schemas
dtctl get settings-schemas | grep openpipeline   # Filter for OpenPipeline schemas
dtctl describe settings-schema builtin:openpipeline.logs.pipelines  # Schema details

# Settings Objects
# Resource name: settings (short: setting)
dtctl get settings --schema builtin:openpipeline.logs.pipelines  # List settings for schema
dtctl get settings --schema <schema-id> --scope environment      # Filter by scope
dtctl get setting <object-id>                    # Get specific settings object
dtctl describe setting <object-id>               # Detailed view with metadata

# Create settings
dtctl create settings -f pipeline-config.yaml    # Create from file
dtctl create settings -f config.yaml --set env=prod --set team=platform

# Update settings
dtctl update settings <object-id> -f config.yaml # Update existing
dtctl update settings <object-id> -f config.yaml --set version=v2

# Apply settings (create or update)
dtctl apply -f settings-config.yaml              # Idempotent operation

# Edit settings
dtctl edit setting <object-id>                   # Edit in $EDITOR (YAML by default)
dtctl edit setting <object-id> --format=json     # Edit in JSON format
dtctl edit setting <uid> --schema <schema-id> --scope environment  # Edit using UID

# Delete settings
dtctl delete settings <object-id>                # Delete settings object
dtctl delete settings <object-id> -y             # Skip confirmation

# Template variables support
dtctl create settings -f config.yaml --set environment={{.env}} --set owner={{.team}}
```

**Example Settings Object (YAML)**:
```yaml
schemaId: builtin:openpipeline.logs.pipelines
scope: environment
value:
  id: custom-log-pipeline
  enabled: true
  processors:
    - dqlProcessor:
        id: enrich-logs
        enabled: true
        description: "Enrich logs with metadata"
        processor: |
          fieldsAdd(environment: "production")
  routing:
    - type: default
      output: default_logs
```

**Common Use Cases**:

```bash
# OpenPipeline Configuration Management
# List all OpenPipeline schemas
dtctl get settings-schemas | grep openpipeline

# View logs pipeline configuration
dtctl get settings --schema builtin:openpipeline.logs.pipelines

# Update logs pipeline
dtctl apply -f logs-pipeline.yaml

# Deploy pipeline across environments
dtctl apply -f base-pipeline.yaml --set env=dev --context dev
dtctl apply -f base-pipeline.yaml --set env=prod --context prod

# Monitoring Settings
# List monitoring schemas
dtctl get settings-schemas | grep monitoring

# Get current monitoring settings
dtctl get settings --schema builtin:monitoring.settings
```

**Notes**:
- Settings objects use optimistic locking (version-based)
- Update/Delete operations automatically handle version management
- Scope determines where settings apply (environment, tenant, etc.)
- Settings are schema-validated by the API
- Many schemas are read-only (managed by Dynatrace)

### 13. Vulnerabilities

```bash
# Resource name: vulnerability/vulnerabilities (short: vuln)
dtctl get vulnerabilities                        # List vulnerabilities
dtctl get vulnerabilities --severity critical    # Filter by severity
dtctl describe vulnerability <id>                # Vulnerability details
dtctl get vulnerabilities --affected <entity-id> # By affected entity
```

### 14. Davis AI

Davis AI provides predictive/causal analysis (Analyzers) and generative AI chat (CoPilot).

```bash
# Analyzers
# Resource name: analyzer/analyzers (short: az)
dtctl get analyzers                              # List all available analyzers
dtctl get analyzer dt.statistics.GenericForecastAnalyzer  # Get analyzer definition
dtctl get analyzers --filter "name contains 'forecast'"   # Filter analyzers
dtctl get analyzers -o json                      # Output as JSON

# Execute Analyzers
dtctl exec analyzer dt.statistics.GenericForecastAnalyzer -f input.json
dtctl exec analyzer dt.statistics.GenericForecastAnalyzer --input '{"query":"timeseries avg(dt.host.cpu.usage)"}'
dtctl exec analyzer dt.statistics.GenericForecastAnalyzer --query "timeseries avg(dt.host.cpu.usage)"

# Analyzer execution options
dtctl exec analyzer <name> -f input.json --validate  # Validate input without executing
dtctl exec analyzer <name> -f input.json --wait      # Wait for completion (default)
dtctl exec analyzer <name> -f input.json --timeout 600  # Custom timeout (seconds)
dtctl exec analyzer <name> -f input.json -o json     # Output result as JSON

# Davis CoPilot Skills
dtctl get copilot-skills                         # List available CoPilot skills

# Davis CoPilot Chat
# Resource name: copilot (short: cp, chat)
dtctl exec copilot "What caused the CPU spike?"  # Ask a question
dtctl exec copilot -f question.txt               # Read question from file
dtctl exec copilot "Explain errors" --stream     # Stream response in real-time

# CoPilot chat options
dtctl exec copilot "Analyze this" --context "Additional context here"
dtctl exec copilot "What is DQL?" --no-docs      # Disable Dynatrace docs retrieval
dtctl exec copilot "List errors" --instruction "Answer in bullet points"

# NL to DQL
dtctl exec copilot nl2dql "show me error logs from the last hour"
dtctl exec copilot nl2dql "find hosts with high CPU usage"
dtctl exec copilot nl2dql -f prompt.txt          # Read prompt from file
dtctl exec copilot nl2dql "..." -o json          # Output as JSON (includes messageToken)

# DQL to NL
dtctl exec copilot dql2nl 'fetch logs | filter status == "ERROR" | limit 10'
dtctl exec copilot dql2nl -f query.dql           # Read query from file
dtctl exec copilot dql2nl "..." -o json          # Output as JSON (includes summary + explanation)

# Document Search
dtctl exec copilot document-search "CPU analysis" --collections notebooks
dtctl exec copilot document-search "error monitoring" --collections dashboards,notebooks
dtctl exec copilot document-search "performance" --exclude doc-123,doc-456
```

**Analyzer Input Example** (`input.json`):
```json
{
  "query": "timeseries avg(dt.host.cpu.usage)",
  "forecastHorizon": 100,
  "generalParameters": {
    "timeframe": {
      "startTime": "now-7d",
      "endTime": "now"
    }
  }
}
```

### 15. Platform Management

```bash
# Environments and accounts (not implemented yet)
# dtctl get environments                           # List environments
# dtctl describe environment <id>                  # Environment details
# dtctl get accounts                               # List accounts (if multi-account)
```

### 16. Extensions 2.0

**API Spec**: [Extensions 2.0 API](https://docs.dynatrace.com/docs/dynatrace-api/environment-api/extensions-2-0)

Extensions 2.0 manages installed extension packages and their monitoring configurations.

```bash
# Resource names: extensions/extension (short: ext, exts)
#                 extension-configs/extension-config (short: ext-config, ext-configs)

# List all installed extensions
dtctl get extensions

# Filter extensions by name
dtctl get extensions --name "com.dynatrace"

# Get versions of a specific extension
dtctl get extension com.dynatrace.extension.postgres

# Get versions with wide output (shows author, feature sets, data sources)
dtctl get extension com.dynatrace.extension.postgres -o wide

# Describe an extension (schema, feature sets, data sources, file info)
dtctl describe extension com.dynatrace.extension.postgres

# Describe a specific version
dtctl describe extension com.dynatrace.extension.postgres --version 2.9.3

# List monitoring configurations for an extension
dtctl get extension-configs com.dynatrace.extension.postgres

# Filter monitoring configurations by version
dtctl get extension-configs com.dynatrace.extension.postgres --version 2.9.3

# Describe a specific monitoring configuration
dtctl describe extension-config com.dynatrace.extension.postgres --config-id <object-id>

# Apply (create or update) a monitoring configuration from YAML
dtctl apply extension-config com.dynatrace.extension.postgres -f config.yaml

# Apply with scope override
dtctl apply extension-config com.dynatrace.extension.postgres -f config.yaml --scope HOST-1234

# Apply with template variables
dtctl apply extension-config com.dynatrace.extension.postgres -f config.yaml --set env=prod

# Dry run to preview
dtctl apply extension-config com.dynatrace.extension.postgres -f config.yaml --dry-run
```

**Behavior notes**:
- `get extensions` lists all installed extension packages with name, latest version, and active version.
- `get extension <name>` lists all installed versions of a specific extension.
- `describe extension` displays schema details, feature sets, data sources, and file information for the latest (or specified) version.
- `get extension-configs` lists monitoring configurations with scope, version, and status.
- `apply extension-config` creates a new configuration if no `objectId` is present in the file, or updates an existing one if `objectId` is set.
- The Extensions API limits page sizes to 100; dtctl automatically caps the `--chunk-size` value.
- Template variables (`--set`) are supported for manifest files.

```bash
# Certificates (not implemented yet)
# dtctl get certificates                           # List certificates
```

### 16a. Hub Catalog Extensions

**API Spec**: [Hub Catalog API](https://docs.dynatrace.com/docs/dynatrace-api/hub-api) (v1)

The Hub catalog lets users browse available extensions before installing them. All commands are read-only.

```bash
# Resource names: hub-extensions/hub-extension
#                 hub-extension-releases/hub-extension-release

# List all available Hub extensions
dtctl get hub-extensions

# Filter by keyword (client-side, case-insensitive substring match on name, ID, description)
dtctl get hub-extensions --filter kafka

# Wide output (includes description)
dtctl get hub-extensions -o wide

# Get a specific Hub extension by ID
dtctl get hub-extensions com.dynatrace.extension.host-monitoring

# Describe a Hub extension
dtctl describe hub-extensions com.dynatrace.extension.host-monitoring

# List releases for an extension
dtctl get hub-extension-releases com.dynatrace.extension.host-monitoring
```

**Behavior notes**:
- The Hub API does not support server-side filtering; `--filter` fetches all pages and applies a case-insensitive substring match against `id`, `name`, and `description` fields.
- Pagination follows `PaginationDefault` style (`page-key` / `page-size`).
- Extension IDs are URL-path-escaped to handle dots and special characters safely.
- These commands are read-only and do not require safety checks.

### 17. Lookup Tables (Grail Resource Store)

Lookup tables are tabular files stored in Grail Resource Store that can be loaded and joined with observability data in DQL queries for data enrichment.

```bash
# Resource name: lookup/lookups (short: lkup, lu)

# List lookup tables
dtctl get lookups                                # List all lookup tables
dtctl get lookups -o wide                        # Show additional columns

# Get lookup table with data preview
dtctl get lookup /lookups/grail/pm/error_codes   # Show metadata + preview
dtctl get lookup /lookups/grail/pm/error_codes -o csv > data.csv  # Export as CSV
dtctl get lookup /lookups/grail/pm/error_codes -o json # Export as JSON

# Describe lookup table (metadata only)
dtctl describe lookup /lookups/grail/pm/error_codes

# Create lookup table from CSV (auto-detect headers)
dtctl create lookup -f error_codes.csv \
  --path /lookups/grail/pm/error_codes \
  --lookup-field code \
  --display-name "Error Codes" \
  --description "HTTP error code descriptions"

# Create with custom parse pattern (non-CSV formats)
dtctl create lookup -f data.txt \
  --path /lookups/custom/data \
  --lookup-field id \
  --parse-pattern "LD:id '|' LD:value '|' LD:timestamp" \
  --skip-records 1

# Create from manifest (YAML)
dtctl create lookup -f lookup-manifest.yaml

# Apply (create or update - idempotent)
dtctl apply -f lookup-manifest.yaml

# Update by deleting and recreating
dtctl delete lookup /lookups/grail/pm/error_codes -y
dtctl create lookup -f updated_data.csv \
  --path /lookups/grail/pm/error_codes \
  --lookup-field code

# Delete
dtctl delete lookup /lookups/grail/pm/error_codes # Requires confirmation
dtctl delete lookup /lookups/grail/pm/error_codes -y # Skip confirmation

# Use in DQL queries
dtctl query "
  fetch logs
  | lookup [load '/lookups/grail/pm/error_codes'], lookupField:status_code
  | fields timestamp, status_code, message, severity
"
```

**CSV Data File Example** (`error_codes.csv`):
```csv
code,message,severity
E001,Connection timeout,high
E002,Invalid credentials,critical
E003,Resource not found,medium
E004,Rate limit exceeded,low
```

**Usage with CSV**:
```bash
# Create from CSV (auto-detects structure)
dtctl create lookup -f error_codes.csv \
  --path /lookups/grail/pm/error_codes \
  --lookup-field code \
  --display-name "Error Codes" \
  --description "HTTP error code descriptions"

# Update existing (delete first)
dtctl delete lookup /lookups/grail/pm/error_codes -y
dtctl create lookup -f error_codes.csv \
  --path /lookups/grail/pm/error_codes \
  --lookup-field code
```

**Features**:
- Auto-detect CSV headers and generate DPL parse patterns
- Support custom parse patterns for non-CSV formats (pipe-delimited, tab-delimited, etc.)
- Multipart form upload to Grail Resource Store API
- Path validation (must start with `/lookups/`, alphanumeric + `-_./ only`, max 500 chars)
- Update via delete and recreate workflow
- Export to CSV/JSON for backup
- List with metadata (path, size, records, modified timestamp)
- Get with 10-row data preview

**API Endpoints**:
- `POST /platform/storage/resource-store/v1/files/tabular/lookup:upload` - Upload
- `POST /platform/storage/resource-store/v1/files:delete` - Delete
- `fetch dt.system.files | filter path starts_with "/lookups/"` - List (via DQL)
- `load "<path>"` - Load data (via DQL)

**Required Scopes**: `storage:files:read`, `storage:files:write`, `storage:files:delete`

See [../TOKEN_SCOPES.md](../TOKEN_SCOPES.md) for complete scope reference.

### 18. Email (Templates)

```bash
# Email templates and sending (not implemented yet)
# dtctl get email-templates                        # List templates
# dtctl send email --template <id> --to user@ex.com # Send email
```

### 19. State Management

```bash
# State storage for apps/extensions (not implemented yet)
# dtctl get state <key>                            # Get state value
# dtctl set state <key> <value>                    # Set state
# dtctl delete state <key>                         # Delete state
```

### 20. Azure Connection
**API Spec**: Settings API v2 (`builtin:hyperscaler-authentication.connections.azure`)

Azure Connection manages authentication credentials used by Azure monitoring configurations.

```bash
# Resource path: azure connection(s)

# List all Azure connections
dtctl get azure connections

# Get by name (preferred) or object ID
dtctl get azure connections <name-or-id>

# JSON/YAML output
dtctl get azure connections -o json
dtctl get azure connections -o yaml

# Imperative create from flags
dtctl create azure connection --name "my-conn" --type federatedIdentityCredential
dtctl create azure connection --name "my-conn" --type clientSecret

# Imperative update by name or ID
dtctl update azure connection --name "my-conn" --directoryId "<tenant-id>" --applicationId "<client-id>"
dtctl update azure connection <object-id> --directoryId "<tenant-id>" --applicationId "<client-id>"

# Apply/create-update from manifest
dtctl apply -f azure_connection.yaml
```

**Behavior notes**:
- Name-based lookup is supported for `get` and `apply` flows.
- `apply` performs idempotent create-or-update logic (POST if new, PUT if existing).
- For federated credentials, `create azure connection` prints actionable Azure CLI guidance with dynamic `Issuer`, `Subject`, and `Audience`.
- Guided flow includes assigning `Reader` role on subscription scope and finalizing with `dtctl update azure connection`.
- `--type` supports: `federatedIdentityCredential`, `clientSecret`.
- CLI completion supports `--type` value suggestions.
- After creating federated credential in Entra ID, short propagation delay may occur; retry update when receiving transient `AADSTS70025`.

### 21. Azure Monitoring Configuration
**API Spec**: Extensions API (`com.dynatrace.extension.da-azure`)

Azure Monitoring Configuration manages monitoring profiles for Azure subscriptions/management groups.

```bash
# Resource path: azure monitoring

# List all monitoring configurations
dtctl get azure monitoring

# Get by description (name) or object ID
dtctl get azure monitoring <description-or-id>

# Helper: list available Azure locations from latest extension schema
dtctl get azure monitoring-locations

# Helper: list available FeatureSetsType values from latest extension schema
dtctl get azure monitoring-feature-sets

# Imperative create from flags
dtctl create azure monitoring --name "my-monitoring" --credentials "my-conn"

# Apply/create-update from manifest
dtctl apply -f azure_monitoring_config.yaml

# Describe with runtime status section
dtctl describe azure monitoring "my-monitoring"
```

**Behavior notes**:
- `apply` supports optional `objectId`; when missing, dtctl resolves existing config by description and updates it.
- If `version` is omitted during update, dtctl preserves the currently configured version.
- If `version` is omitted during create, dtctl resolves and uses the latest extension version.
- Locations and feature sets are discovered dynamically from the latest extension schema.
- `describe azure monitoring` supports name-first lookup with ID fallback.
- `describe azure monitoring` prints operational status based on DQL metrics/events, including latest value timestamp.

### 22. Google Cloud Connection
**API Spec**: Settings API v2 (`builtin:hyperscaler-authentication.connections.gcp`)

Google Cloud Connection manages service-account impersonation credentials used by GCP monitoring configurations.

```bash
# Resource path: gcp connection(s)

# List all GCP connections
dtctl get gcp connections

# Get by name (preferred) or object ID
dtctl get gcp connections <name-or-id>

# JSON/YAML output
dtctl get gcp connections -o json
dtctl get gcp connections -o yaml

# Imperative create from flags
dtctl create gcp connection --name "my-gcp-conn" --serviceAccountId "reader@project.iam.gserviceaccount.com"

# Imperative update by name or ID
dtctl update gcp connection --name "my-gcp-conn" --serviceAccountId "reader@project.iam.gserviceaccount.com"
dtctl update gcp connection <object-id> --serviceAccountId "reader@project.iam.gserviceaccount.com"

# Delete by name or ID
dtctl delete gcp connection <name-or-id>

# Apply/create-update from manifest
dtctl apply -f gcp_connection.yaml
```

**Behavior notes**:
- On create/apply, dtctl ensures the Dynatrace singleton principal (`builtin:hyperscaler-authentication.connections.gcp-dynatrace-principal`) exists.
- `get`, `update`, and `delete` support name-first lookup with object ID fallback.
- Connection type defaults to `serviceAccountImpersonation`.
- `apply` performs idempotent create-or-update logic (POST if new, PUT if existing).

### 23. Google Cloud Monitoring Configuration
**API Spec**: Extensions API (`com.dynatrace.extension.da-gcp`)

Google Cloud Monitoring Configuration manages monitoring profiles for GCP integrations.

```bash
# Resource path: gcp monitoring

# List all monitoring configurations
dtctl get gcp monitoring

# Get by description (name) or object ID
dtctl get gcp monitoring <description-or-id>

# Helper: list available GCP locations from latest extension schema
dtctl get gcp monitoring-locations

# Helper: list available FeatureSetsType values from latest extension schema
dtctl get gcp monitoring-feature-sets

# Imperative create from flags
dtctl create gcp monitoring --name "my-gcp-monitoring" --credentials "my-gcp-conn"

# Imperative update by name or ID
dtctl update gcp monitoring --name "my-gcp-monitoring" --locationFiltering "us-central1,europe-west1"
dtctl update gcp monitoring <object-id> --featureSets "compute_engine_essential,cloud_run_essential"

# Delete by name or ID
dtctl delete gcp monitoring <name-or-id>

# Apply/create-update from manifest
dtctl apply -f gcp_monitoring_config.yaml

# Describe with runtime status section
dtctl describe gcp monitoring "my-gcp-monitoring"
```

**Behavior notes**:
- `apply` supports optional `objectId`; when missing, dtctl resolves existing config by description and updates it.
- If `version` is omitted during update, dtctl preserves the currently configured version.
- If `version` is omitted during create, dtctl resolves and uses the latest extension version.
- Locations and feature sets are discovered dynamically from the latest extension schema.
- Default create behavior uses all discovered locations and all discovered `*_essential` feature sets unless explicitly overridden.
- `describe gcp monitoring` supports name-first lookup with ID fallback.
- `describe gcp monitoring` prints operational status based on GCP-specific DQL metrics/events.

### 24. AWS Connection
**API Spec**: TBD

```bash
# Placeholder (to be implemented)
```

### 25. AWS Monitoring Configuration
**API Spec**: TBD

```bash
# Placeholder (to be implemented)
```

## Common Operations

### Create Resources

```bash
# From file with create command
dtctl create workflow -f workflow.yaml
dtctl create dashboard -f dashboard.yaml
dtctl create notebook -f notebook.yaml
dtctl create slo -f slo.yaml
dtctl create bucket -f bucket.yaml
dtctl create edgeconnect -f edgeconnect.yaml

# From file with apply (create or update)
dtctl apply -f resource.yaml
dtctl apply -f directory/                        # Multiple files

# From stdin
cat resource.yaml | dtctl apply -f -
cat workflow.yaml | dtctl create workflow -f -

# With template variables
dtctl create workflow -f workflow.yaml --set env=prod --set owner=team-a

# Inline creation (not implemented yet)
# dtctl create document --name "My Notebook" --type notebook
```

### Update Resources

```bash
# Declarative update (apply)
dtctl apply -f resource.yaml                     # Create if not exists, update if exists

# Imperative update (patch - not implemented yet)
# dtctl patch document <id> --name "New Name"

# Interactive edit
dtctl edit document <id>                         # Opens in $EDITOR
```

### Delete Resources

```bash
# Single resource by ID
dtctl delete document <id>

# Single resource by name (with name resolution)
dtctl delete workflow "My Workflow"
dtctl delete dashboard "Production Dashboard"

# From file
dtctl delete -f resource.yaml

# Multiple resources (supported for some resource types)
dtctl delete document <id1> <id2> <id3>

# Skip confirmation prompt
dtctl delete document <id> -y
dtctl delete document <id> --yes

# Note: Deletion requires confirmation by default (shows resource details)
# Use -y/--yes to skip, or --plain to disable interactive prompts
```

### List & Filter

```bash
# Basic list
dtctl get dashboards
dtctl get notebooks

# Filter by field (limited support - depends on resource type)
dtctl get dashboards --mine                      # Filter to current user's dashboards
dtctl get notebooks --mine                       # Filter to current user's notebooks

# Limit results
dtctl get workflows --chunk-size 10              # Control pagination

# Wide output (more columns)
dtctl get dashboards -o wide
dtctl get notebooks -o wide

# (not implemented yet)
# dtctl get dashboards --sort-by=.metadata.modified  # Sort results
# dtctl get slos --filter 'name~production'          # Advanced filters
# dtctl get dashboards --output custom-columns=NAME:.name,TYPE:.type,OWNER:.owner
```

## Configuration & Context

### Configuration File Structure

**Location** (platform-specific):
- **Linux**: `$XDG_CONFIG_HOME/dtctl/config` (default: `~/.config/dtctl/config`)
- **macOS**: `~/Library/Application Support/dtctl/config`
- **Windows**: `%LOCALAPPDATA%\dtctl\config`

**Note**: dtctl follows the [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html) and adapts to platform conventions.

```yaml
apiVersion: v1
kind: Config
current-context: prod

contexts:
- name: dev
  context:
    environment: https://dev.apps.dynatrace.com
    token-ref: dev-token
    safety-level: dangerously-unrestricted  # Full access for dev
    description: "Development sandbox"

- name: prod
  context:
    environment: https://prod.apps.dynatrace.com
    token-ref: prod-token
    safety-level: readonly                   # Read-only for production
    description: "Production - read only"

tokens:
- name: dev-token
  token: dt0s16.***

- name: prod-token
  token: dt0s16.***

preferences:
  output: table
  editor: vim
```

### Context Safety Levels

Safety levels provide **client-side** protection against accidental destructive operations. They are a convenience feature to prevent mistakes, **not a security boundary**.

> **Important**: For actual security, use proper API token scopes. Configure your Dynatrace API tokens with the minimum required permissions.

| Level | Description | Bucket Delete |
|-------|-------------|---------------|
| `readonly` | No modifications allowed | No |
| `readwrite-mine` | Modify own resources only | No |
| `readwrite-all` | Modify all resources (default) | No |
| `dangerously-unrestricted` | All operations | Yes |

```bash
# Create a read-only production context
dtctl config set-context prod-viewer \
  --environment https://prod.dynatrace.com \
  --token-ref prod-token \
  --safety-level readonly

# Create an unrestricted dev context
dtctl config set-context dev \
  --environment https://dev.dynatrace.com \
  --token-ref dev-token \
  --safety-level dangerously-unrestricted

# View context details including safety level
dtctl config describe-context prod-viewer

See [Context Safety Levels](context-safety-levels.md) for detailed documentation.

### Context Management Commands

```bash
# Create project config
dtctl config init                                # Generate .dtctl.yaml template
dtctl config init --context staging             # Custom context name
dtctl config init --force                        # Overwrite existing file

# View configuration
dtctl config view                                # View full config
dtctl config view --minify                       # View without defaults

# View contexts
dtctl config get-contexts                        # List all contexts
dtctl config current-context                     # Show current context

# Switch context
dtctl config use-context prod                    # Switch to prod

# Set context properties
dtctl config set-context dev --environment https://...

# Set credentials
dtctl config set-credentials dev --token dt0s16.***

# Delete context
dtctl config delete-context dev

# Rename context
dtctl config rename-context old-name new-name
```

#### Context Shortcut (`dtctl ctx`)

The `ctx` command provides a top-level shortcut for common context operations:

```bash
dtctl ctx                    # List all contexts (highlights current)
dtctl ctx prod               # Switch to 'prod' context
dtctl ctx current            # Show current context name
dtctl ctx describe           # Describe current context
dtctl ctx describe prod      # Describe specific context
dtctl ctx set <name> --environment <url>  # Create or update a context (also activates it)
dtctl ctx delete old-env     # Delete a context
dtctl ctx rm old-env         # Alias for delete
```

### Diagnostics

```bash
# Run all health checks
dtctl doctor

# Checks performed (in order):
# 1. Version    - shows dtctl version
# 2. Config     - verifies config file exists and is readable
# 3. Context    - validates current context and environment URL
# 4. Token      - checks token presence and expiration
# 5. Connect    - lightweight HEAD request to environment URL
# 6. Auth       - validates token via metadata API
```

### Authentication

```bash
# Set token for current context
dtctl config set-credentials --token dt0s16.***

# Login interactively (OAuth flow, if supported)
dtctl login

# View current auth info
dtctl auth whoami

# Test authentication
dtctl auth can-i create documents
dtctl auth can-i delete slo my-slo-id
dtctl auth can-i '*' '*'                         # Test all permissions
```

### Current User Context

The `auth whoami` command displays information about the currently authenticated user.

```bash
# View current user info
dtctl auth whoami
# Output:
# User ID:    621321d-1231-dsad-652321829b50
# User Name:  John Doe
# Email:      john.doe@example.com
# Context:    prod
# Environment: https://abc12345.apps.dynatrace.com

# Machine-readable output
dtctl auth whoami -o json
# {"userId":"621321d-...","userName":"John Doe","emailAddress":"john.doe@example.com"}

dtctl auth whoami -o yaml

# Get just the user ID (useful for scripting)
dtctl auth whoami --id-only
# 621321d-1231-dsad-652321829b50
```

**Implementation Notes:**
- Primary: Calls `/platform/metadata/v1/user` API (requires `app-engine:apps:run` scope)
- Fallback: Decodes JWT token's `sub` claim (works offline, but only provides user ID)

### Filtering Resources by Owner (`--mine`)

Many resources support ownership. The `--mine` flag filters to show only resources owned by the current user.

```bash
# List only my dashboards
dtctl get dashboards --mine

# List only my notebooks  
dtctl get notebooks --mine

# List only my workflows
dtctl get workflows --mine

# Combine with other filters
dtctl get dashboards --mine --name "production"
dtctl get notebooks --mine -o json
```

**Supported Resources:**
| Resource | `--mine` Support | Filter Field |
|----------|------------------|--------------|
| `dashboards` | ✅ | `owner` |
| `notebooks` | ✅ | `owner` |
| `workflows` | ✅ | `owner` |
| `slos` | ✅ | `owner` |
| `filter-segments` | ✅ | `owner` |
| `apps` | ❌ | N/A (environment-wide) |

**How it works:**
1. dtctl fetches the current user ID (via metadata API or JWT)
2. Adds `owner=='<user-id>'` to the API filter parameter
3. Returns only resources owned by the authenticated user

**Alternative: Explicit owner filter**

For more control, use the `--field-selector` flag:

```bash
# Filter by specific owner
dtctl get dashboards --field-selector owner=<user-id>

# Filter by creator (different from owner for transferred docs)
dtctl get notebooks --field-selector modificationInfo.createdBy=<user-id>

# The special value "me" resolves to current user ID
dtctl get dashboards --field-selector owner=me
dtctl get notebooks --field-selector modificationInfo.createdBy=me
```

**Caching User ID:**

To avoid repeated API calls, dtctl caches the user ID for the current context:
- Cache location: `~/.cache/dtctl/<context>/user.json`
- Cache TTL: 24 hours (configurable via `preferences.user-cache-ttl`)
- Force refresh: `dtctl auth whoami --refresh`

### Command Aliases

Command aliases allow users to create shortcuts for frequently used commands. They are stored in the config file and support three types:

1. **Simple Aliases**: Direct text replacement
2. **Parameterized Aliases**: Support `$1-$9` positional parameters
3. **Shell Aliases**: Execute through system shell (prefix with `!`)

**Design Goals:**
- Reduce typing for common workflows
- Enable team sharing via import/export
- Support both simple shortcuts and complex shell pipelines
- Prevent shadowing of built-in commands for safety

**Commands:**

```bash
# Set alias
dtctl alias set <name> <expansion>

# List aliases
dtctl alias list

# Delete alias
dtctl alias delete <name>

# Import/export
dtctl alias import -f <file.yaml>
dtctl alias export -f <file.yaml>
```

**Examples:**

```bash
# Simple alias
dtctl alias set wf "get workflows"
dtctl wf  # Expands to: dtctl get workflows

# Parameterized alias
dtctl alias set logs-status "query 'fetch logs | filter status=\$1 | limit \$2'"
dtctl logs-status ERROR 100
# Expands to: dtctl query 'fetch logs | filter status=ERROR | limit 100'

# Shell alias (with pipes, external tools)
dtctl alias set wf-count "!dtctl get workflows -o json | jq '.workflows | length'"
dtctl wf-count
# Executes through shell: dtctl get workflows -o json | jq '.workflows | length'
```

**Storage Format (in config file):**

```yaml
apiVersion: v1
kind: Config
current-context: prod
contexts: [...]
aliases:
  wf: get workflows
  wfe: get workflow-executions
  logs-error: query 'fetch logs | filter status=ERROR | limit 100'
  top-errors: "!dtctl query 'fetch logs | filter status=ERROR' -o json | jq -r '.records[].message' | sort | uniq -c | sort -rn | head -10"
```

**Resolution:**
- Happens before Cobra command parsing (intercepts `os.Args`)
- Aliases cannot shadow built-in commands (`get`, `describe`, `create`, etc.)
- Recursive alias expansion is not supported
- Shell aliases (`!` prefix) execute the full expansion through `/bin/sh` (Unix) or `cmd.exe` (Windows)

**Security Considerations:**
- Aliases are stored in plain text in config file
- Shell aliases can execute arbitrary commands
- Import with `--no-overwrite` to prevent accidental overwrites
- Validate alias names (alphanumeric + `-_`, no spaces)

See [ALIAS_DESIGN.md](ALIAS_DESIGN.md) for complete specification.

## Output Formats

### Table (default)
```bash
dtctl get documents
# NAME              TYPE       OWNER    MODIFIED
# my-notebook       notebook   me       2h ago
# prod-dashboard    dashboard  team     1d ago
```

### JSON
```bash
dtctl get documents -o json
# {"items": [{"id": "...", "name": "my-notebook", ...}]}

# Pretty print
dtctl get documents -o json | jq .
```

### YAML
```bash
dtctl get document my-notebook -o yaml
# apiVersion: document/v1
# kind: Document
# metadata:
#   id: abc-123
#   name: my-notebook
# ...
```

### Custom Output
```bash
# JSONPath query
dtctl get documents -o jsonpath='{.items[*].name}'

# Custom columns
dtctl get documents -o custom-columns=NAME:.name,ID:.id
```

### Chart (Timeseries Visualization)
```bash
# Visualize timeseries data as ASCII line charts in the terminal
dtctl query "timeseries avg(dt.host.cpu.usage)" -o chart

# Forecast analyzer with chart output
dtctl exec analyzer dt.statistics.GenericForecastAnalyzer \
  --input '{"timeSeriesData":"timeseries avg(dt.host.cpu.usage)","forecastHorizon":50}' \
  -o chart

# Multiple series (grouped by dimension) - limited to 10 series
dtctl query "timeseries avg(dt.host.cpu.usage), by:{dt.entity.host}" -o chart
```

### Sparkline (Compact Timeseries)
```bash
# Compact single-line visualization with stats
dtctl query "timeseries avg(dt.host.cpu.usage)" -o sparkline

# Compare multiple series compactly (alias: -o spark)
dtctl query "timeseries avg(dt.host.cpu.usage), by:{dt.entity.host}" -o spark
```

Output example:
```
HOST-A  ▁▂▃▄▅▆▇█▇▆▅▄▃▂▁  (min: 22.7, max: 38.4, avg: 33.2)
HOST-B  ▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅  (min: 99.0, max: 100.0, avg: 99.5)
```

### Bar Chart (Aggregated Comparison)
```bash
# Compare average values across series as horizontal bars
dtctl query "timeseries avg(dt.host.cpu.usage), by:{dt.entity.host}" -o barchart

# Short alias
dtctl query "timeseries avg(dt.host.cpu.usage), by:{dt.entity.host}" -o bar
```

Output example:
```
HOST-A  ██████████░░░░░░░░░░  33.2
HOST-B  ██████████████████████████████████████████████████  100.0
```

**Note**: All timeseries output formats (`chart`, `sparkline`, `barchart`) require timeseries data 
(records with `timeframe` and `interval` fields). If the data is not timeseries, they fall back 
to JSON output with a warning. When more than 10 series are present, only the first 10 are displayed.

**Color**: Charts, sparklines, bar charts, and watch mode use ANSI colors when enabled. Color follows the [no-color.org](https://no-color.org/) standard — it is automatically disabled when piped, when `NO_COLOR` is set, or when `--plain` is used. Set `FORCE_COLOR=1` to override TTY detection.

### Snapshot Decoding (Live Debugger Query Output) -- Experimental
```bash
# Decode and simplify snapshot payloads (variant wrappers → plain values)
dtctl query "fetch application.snapshots | sort timestamp desc | limit 5" --decode-snapshots

# Full decoded tree with type annotations
dtctl query "fetch application.snapshots | sort timestamp desc | limit 5" --decode-snapshots=full

# Compose with any output format
dtctl query "fetch application.snapshots | limit 5" --decode-snapshots -o json
dtctl query "fetch application.snapshots | limit 5" --decode-snapshots -o yaml
```

The `--decode-snapshots` flag enriches each record with `parsed_snapshot`, decoded from:
- `snapshot.data`
- `snapshot.string_map`

By default, `--decode-snapshots` simplifies variant wrappers to plain values (e.g., `{"type": "Integer", "value": 42}` becomes `42`). Use `--decode-snapshots=full` to preserve the full decoded tree with type annotations.

This flag is composable with any output format (`-o json`, `-o yaml`, `-o table`, `-o csv`, etc.), unlike the previous `-o snapshot` which was a standalone format locked to JSON output.

## Live Debugger Command Pattern (Experimental)

> **Note:** Live Debugger support is experimental. The underlying APIs and query behavior may change in future releases.

Live Debugger follows the standard verb-noun grammar and avoids introducing a separate command tree:

```bash
dtctl create breakpoint File.java:line --filters key:value     # set workspace filters + create a breakpoint in one step
dtctl create breakpoint File.java:line                         # create breakpoint
dtctl update breakpoint --filters key:value[,key:value...]     # configure workspace filters
dtctl get breakpoints                                          # list breakpoints
dtctl describe breakpoint <breakpoint-id|filename:line>                   # describe breakpoint rollout/status
dtctl update breakpoint <id|filename:line> --condition "..."   # update condition
dtctl update breakpoint <id|filename:line> --enabled true|false # enable/disable
dtctl update breakpoint <id|filename:line> --log-message "..."  # update the hit log message
dtctl delete breakpoint <id|filename:line|--all>               # delete breakpoints
```

`--filters` accepts both `key:value` and `key=value` forms.

Design notes:
- `dtctl describe` keeps existing resource-subcommand behavior; breakpoint describe is only routed for breakpoint-like identifiers.
- Mutating operations (`update` filter update, `create`, `update`, `delete`) must run safety checks, including in dry-run mode.
- Breakpoint log messages use `{variable}` placeholders and are stored qualified by the backend (`{frame.line}` → `store.rookout.frame.line`, other names → `store.rookout.variables.*`). The CLI passes the raw user string through on update and strips those prefixes back out for display in `get`/`describe`, so users only ever deal with the short form. `get breakpoints` includes the log message in its default (non-wide) table.
- `--filters` is optional on `create`. Filters are workspace-scoped and sticky: once set (via `update breakpoint --filters` or `create breakpoint ... --filters`), they persist for subsequent breakpoints until changed. Because a single filter set applies to the whole workspace, changing the filters re-scopes all existing breakpoints (not just new ones); `create`/`update` count the active breakpoints affected and prompt for confirmation before applying the change, bypassable with `--yes` (`-y`) and skipped in non-interactive contexts (`--plain`/agent mode). When `--filters` is supplied on `create`, the workspace filters are updated first, then the breakpoint is created; otherwise `create` requires that workspace filters were already configured.

## Examples

### Working with Dashboards and Notebooks

```bash
# List dashboards and notebooks
dtctl get dashboards
dtctl get notebooks

# Filter by name
dtctl get dashboards --name "production"
dtctl get notebooks --name "analysis"

# View details
dtctl describe dashboard "Production Dashboard"
dtctl describe notebook "Analysis Notebook"

# Edit a dashboard (opens in $EDITOR)
dtctl edit dashboard <dashboard-id>
dtctl edit dashboard "Production Dashboard"

# Edit in JSON format instead of YAML
dtctl edit notebook <notebook-id> --format=json

# Delete (moves to trash)
dtctl delete dashboard <dashboard-id>
dtctl delete notebook "Old Notebook" --force  # Skip confirmation

# Apply changes from file
dtctl apply -f dashboard.yaml
dtctl apply -f notebook.yaml --set environment=prod
```

### Managing SLOs

```bash
# Create SLO from template
dtctl get slo-templates --filter 'name~availability'
dtctl describe slo-template template-id
dtctl create slo --from-template template-id \
  --name "API Availability" \
  --target 99.9

# Check SLO status
dtctl get slos
dtctl describe slo my-slo-id

# Evaluate SLO performance
dtctl exec slo my-slo-id                         # Evaluate and show results
dtctl exec slo my-slo-id -o json | jq '.evaluationResults[].errorBudget'
```

### Automation Workflows

```bash
# List and view workflows
dtctl get workflows
dtctl describe workflow <workflow-id>

# Edit a workflow
dtctl edit workflow <workflow-id>
dtctl edit workflow "My Workflow"

# Apply workflow from file (create or update)
dtctl apply -f workflow.yaml
dtctl apply -f workflow.yaml --set environment=prod

# Execute workflow
dtctl exec workflow <workflow-id>
dtctl exec workflow <workflow-id> --params severity=high --params env=prod

# Execute and wait for completion
dtctl exec workflow <workflow-id> --wait
dtctl exec workflow <workflow-id> --wait --timeout 10m

# Monitor executions
dtctl get workflow-executions
dtctl get wfe -w <workflow-id>              # Filter by workflow
dtctl describe wfe <execution-id>           # Detailed view with tasks

# View execution logs
dtctl logs wfe <execution-id>
dtctl logs wfe <execution-id> --follow      # Stream in real-time
dtctl logs wfe <execution-id> --all         # All tasks with headers
dtctl logs wfe <execution-id> --task <name> # Specific task
```

### Querying Grail Data

```bash
# Simple query
dtctl query 'fetch logs | filter status == "ERROR" | limit 100'

# Query with output formatting
dtctl query "fetch logs | summarize count(), by: {status}" -o json
dtctl query "fetch logs | limit 10" -o yaml
dtctl query "fetch logs | limit 10" -o table

# Execute query from file
dtctl query -f analysis.dql
dtctl query -f analysis.dql -o json > results.json

# Query with template variables
dtctl query -f logs-by-host.dql --set host=h-123 --set timerange=2h

# Template example (logs-by-host.dql):
# fetch logs
# | filter host = "{{.host}}"
# | filter timestamp > now() - {{.timerange | default "1h"}}
# | limit {{.limit | default 100}}
```

### Waiting for Query Results

```bash
# Wait for test data to arrive (common in CI/CD)
dtctl wait query "fetch spans | filter test_id == 'integration-test-123'" \
  --for=count=1 \
  --timeout 5m

# Wait for any error logs in the last 5 minutes
dtctl wait query 'fetch logs | filter status == "ERROR" | filter timestamp > now() - 5m' \
  --for=any \
  --timeout 2m

# Wait for at least 10 metrics records
dtctl wait query "fetch metrics | filter metric.key == 'custom.test.metric'" \
  --for=count-gte=10 \
  --timeout 1m

# Wait with template variables
dtctl wait query -f wait-for-span.dql \
  --set test_id=my-test-456 \
  --set span_name="http.server.request" \
  --for=count=1 \
  --timeout 5m

# Custom backoff for fast CI/CD pipelines
dtctl wait query "fetch spans | filter test_id == 'ci-build-789'" \
  --for=any \
  --timeout 10m \
  --min-interval 500ms \
  --max-interval 15s \
  --backoff-multiplier 1.5

# Conservative retry strategy (lower load on system)
dtctl wait query -f query.dql \
  --for=any \
  --timeout 30m \
  --min-interval 10s \
  --max-interval 2m

# Wait and capture results as JSON
dtctl wait query "fetch spans | filter test_id == 'test-xyz'" \
  --for=count=1 \
  --timeout 5m \
  -o json > span-data.json

# Wait with initial delay (allow ingestion pipeline time to process)
dtctl wait query "fetch logs | filter test_id == 'load-test'" \
  --for=count-gte=100 \
  --timeout 10m \
  --initial-delay 30s

# Limit retry attempts (prevent infinite loops)
dtctl wait query "fetch logs | filter test_id == 'flaky-test'" \
  --for=any \
  --timeout 10m \
  --max-attempts 20

# Use in shell scripts with exit codes
if dtctl wait query "..." --for=count=1 --timeout 2m --quiet; then
  echo "Data arrived successfully"
  # Continue with test assertions
else
  echo "Timeout waiting for data" >&2
  exit 1
fi

# Real-world example: Capture trace ID from HTTP request and wait for trace data
TRACE_ID=$(curl -s -A "Mozilla/5.0" https://example.com/your-app \
  -D - -o /dev/null | grep "dtTrId" | sed -E 's/.*dtTrId;desc="([^"]+)".*/\1/')
echo "Trace ID: $TRACE_ID"

# Wait for the trace to be ingested and queryable
dtctl wait query "fetch spans | filter trace.id == \"$TRACE_ID\"" \
  --for=any \
  --timeout 3m \
  -o json | jq '.records[] | {name: .span.name, duration: .duration}'

# Use in automated tests
test_endpoint() {
  local url=$1

  # Make request and capture trace ID
  local trace_id=$(curl -s -A "Mozilla/5.0" "$url" \
    -D - -o /dev/null | grep "dtTrId" | sed -E 's/.*dtTrId;desc="([^"]+)".*/\1/')

  echo "Testing trace: $trace_id"

  # Wait for trace data with 2 minute timeout
  if dtctl wait query "fetch spans | filter trace.id == \"$trace_id\"" \
    --for=any --timeout 2m -o json > /tmp/trace.json; then

    # Run assertions on the trace
    local error_count=$(jq '[.records[] | select(.status == "ERROR")] | length' /tmp/trace.json)
    if [ "$error_count" -gt 0 ]; then
      echo "❌ Found $error_count errors in trace"
      return 1
    fi

    echo "✅ Trace validated successfully"
    return 0
  else
    echo "❌ Timeout waiting for trace data"
    return 1
  fi
}

test_endpoint "https://example.com/api/checkout"
```

### Apply Operations

```bash
# Apply workflow (create or update)
dtctl apply -f workflow.yaml

# Apply with template variable substitution
dtctl apply -f workflow.yaml --set environment=production --set owner=team-a

# Dry run to preview changes
dtctl apply -f workflow.yaml --dry-run

# Apply dashboard or notebook
dtctl apply -f dashboard.yaml
dtctl apply -f notebook.yaml --set environment=prod

# Export resources for backup
dtctl get workflows -o yaml > workflows-backup.yaml
dtctl get dashboards -o json > dashboards-backup.json
```

### Pipeline Operations

```bash
# All pipeline operations (not implemented yet)
# View current pipeline config
# dtctl get pipelines

# Update pipeline
# dtctl apply -f logs-pipeline.yaml

# Validate before applying
# dtctl validate pipeline -f logs-pipeline.yaml

# Test pipeline with sample data
# dtctl ingest --pipeline logs-pipeline --file test-data.json --dry-run
```

### IAM Operations

```bash
# List users and their groups
dtctl get users -o wide
dtctl get groups

# (not implemented yet)
# dtctl get permissions --user user@example.com
# dtctl create policy -f service-account.yaml
# dtctl get policies -o yaml > iam-audit.yaml
```

## Advanced Features

### Wait for Conditions

```bash
# Wait for query results
dtctl wait query "fetch logs" --for=any --timeout=5m
dtctl wait query "fetch logs" --for=count-gte=100

# (not implemented yet)
# dtctl wait --for=condition=complete execution <id>  # Wait for workflow/resource conditions
# dtctl wait --for=condition=evaluated slo <id>
```

### Watch Mode

```bash
# (not implemented yet)
# dtctl get documents --watch
# dtctl get executions <workflow-id> --watch
# dtctl get slos --watch --interval 30s
```

### Dry Run

```bash
# Preview changes without applying
dtctl apply -f resource.yaml --dry-run
dtctl delete document <id> --dry-run
```

### Diff

```bash
# Compare local file with remote resource
dtctl diff -f workflow.yaml

# Compare two local files
dtctl diff -f workflow-v1.yaml -f workflow-v2.yaml

# Compare two remote resources
dtctl diff workflow prod-workflow staging-workflow

# Different output formats
dtctl diff -f dashboard.yaml --semantic
dtctl diff -f workflow.yaml -o json-patch
dtctl diff -f dashboard.yaml --side-by-side

# Ignore metadata and order
dtctl diff -f workflow.yaml --ignore-metadata --ignore-order
# dtctl diff -f resource.yaml
# dtctl diff document <id> local-copy.yaml
```

### Explain Resources

```bash
# (not implemented yet)
# dtctl explain document
# dtctl explain slo
# dtctl explain workflow
```

### Shell Completion

```bash
# Generate completion script
dtctl completion bash > /etc/bash_completion.d/dtctl
dtctl completion zsh > /usr/local/share/zsh/site-functions/_dtctl

# Enable for current session
source <(dtctl completion bash)
```

## Error Handling

### Exit Codes
- `0`: Success
- `1`: General error
- `2`: Usage error (invalid flags/arguments)
- `3`: Authentication error
- `4`: Not found
- `5`: Permission denied

### Error Output
```bash
Error: document "my-doc" not found
  Run 'dtctl get documents' to list available documents
  Run 'dtctl get trash' to check if document is in trash

Exit code: 4
```

## Implementation Notes

### API Mapping
- Resource operations should generate appropriate REST API calls
- Handle pagination automatically for list operations
- Support filtering and sorting via query parameters

### Rate Limiting
- Implement exponential backoff for rate-limited requests
- Show progress for long-running operations
- Support `--wait` flag for async operations

### Caching
- Cache SLO templates locally
- Cache document metadata for fast listing
- Invalidate cache on create/update/delete
- Provide `--no-cache` flag to force refresh

### Validation
- Validate manifests against OpenAPI specs before applying
- Provide helpful error messages with suggestions
- Support `--validate=false` to skip validation

## Future Enhancements

- Interactive mode with prompts for resource creation
- Resource templates and generators
- Bulk operations (e.g., delete multiple filtered resources)
- Resource diffing and change previews
- Integration with CI/CD pipelines
- ~~Plugin system for custom commands~~ — shipped 2026-07-12: kubectl-style exec plugins (`dtctl-<name>` on PATH; see [PLUGIN_CONVENTIONS.md](PLUGIN_CONVENTIONS.md))
- ~~Run dtctl as a service~~ — shipped: `cmd.Run` + `pkg/engine` + `dtctl serve http` (see [SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md))
- Shell integration (kubectl-like autocompletion)
- Resource usage analytics and cost estimation
