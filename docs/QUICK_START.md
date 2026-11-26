# dtctl Quick Start Guide

Get from a fresh install to productive in a few minutes. This guide covers the essentials; the per-resource reference in [`resources/`](resources/) and the cross-cutting guides below have the depth.

> **Note**: This guide assumes dtctl is already installed. To install or build it, see [INSTALLATION.md](INSTALLATION.md).

## 1. Configure a context

A context stores the environment URL and credentials dtctl talks to. OAuth login is the quickest path:

```bash
# OAuth login (opens a browser; no token to manage)
dtctl auth login --context my-env --environment "https://abc12345.apps.dynatrace.com"

# Confirm connectivity, auth, and permissions
dtctl doctor
```

For token-based auth, multiple environments, per-project config, safety levels, and aliases, see **[CONFIGURATION.md](CONFIGURATION.md)**.

## 2. Run your first commands

dtctl uses a predictable `verb noun` grammar (`get`, `describe`, `create`, `edit`, `apply`, `delete`, `query`, `exec`):

```bash
# List a resource
dtctl get workflows

# Run a DQL query against Grail
dtctl query "fetch logs | limit 10"

# Structured output for scripts and agents
dtctl get dashboards -o json

# Declarative apply (create or update from a file)
dtctl apply -f workflow.yaml

# Preview any mutating command without doing it
dtctl apply -f workflow.yaml --dry-run
```

Output formats (`table`, `json`, `yaml`, `csv`, `toon`, `wide`, `auto`, JSON Lines/Parquet exports) are covered in **[OUTPUT_FORMATS.md](OUTPUT_FORMATS.md)**.

## 3. Work with resources

Each resource type has its own reference page with supported operations, flags, required token scopes, output shape, and examples:

| Resource | Reference |
| --- | --- |
| Workflows | [resources/workflows.md](resources/workflows.md) |
| Dashboards & notebooks | [resources/dashboards-notebooks.md](resources/dashboards-notebooks.md) |
| DQL queries | [resources/dql-queries.md](resources/dql-queries.md) |
| SLOs | [resources/slos.md](resources/slos.md) |
| Settings | [resources/settings-api.md](resources/settings-api.md) |
| Grail buckets | [resources/grail-buckets.md](resources/grail-buckets.md) |
| Filter segments | [resources/filter-segments.md](resources/filter-segments.md) |
| Lookup tables | [resources/lookup-tables.md](resources/lookup-tables.md) |
| Notifications | [resources/notifications.md](resources/notifications.md) |
| Documents & trash | [resources/documents.md](resources/documents.md) |
| App Engine | [resources/app-engine.md](resources/app-engine.md) |
| Extensions | [resources/extensions.md](resources/extensions.md) |
| Cloud integrations (AWS/Azure/GCP) | [resources/cloud-integrations.md](resources/cloud-integrations.md) |
| EdgeConnect | [resources/edgeconnect.md](resources/edgeconnect.md) |
| Analyzers | [resources/analyzers.md](resources/analyzers.md) |
| Davis CoPilot | [resources/copilot.md](resources/copilot.md) |
| Anomaly detectors | [resources/anomaly-detectors.md](resources/anomaly-detectors.md) |
| Live Debugger | [resources/live-debugger.md](resources/live-debugger.md) |
| API discovery | [resources/api-discovery.md](resources/api-discovery.md) |
| Platform tokens | [resources/platform-tokens.md](resources/platform-tokens.md) |
| Users & groups | [resources/users-groups.md](resources/users-groups.md) |

Every flag that takes an input file (`apply -f`, `create <resource> -f`, `diff -f`, `update document -f`, `query -f`, and the rest) reads standard input when given `-`:

```bash
cat workflow.yaml | dtctl create workflow -f -
render-dashboard.sh | dtctl apply -f - --dry-run
dtctl get workflow my-wf -o yaml | dtctl diff -f - -f workflow.yaml
```

Piped input is shown as `<stdin>` wherever a file name would be. `apply --write-id` needs a file to write the ID back to, so it is rejected with `-f -`, and `diff` accepts `-` for one side only.

The full generated verb-and-resource matrix is in **[COMMANDS.md](COMMANDS.md)**; required token scopes by resource are in **[TOKEN_SCOPES.md](TOKEN_SCOPES.md)**.

## OpenPipeline

OpenPipeline processes and routes observability data. As of September 2025, OpenPipeline configurations have been migrated from the direct API to the Settings API v2 for better access control and configuration management.

<!-- prose-check:ignore -->
**Important:** The direct OpenPipeline commands (`dtctl get openpipelines`, `dtctl describe openpipeline`) have been removed. Use the Settings API instead to manage OpenPipeline configurations.

### View pipeline configurations via the Settings API

```bash
# List OpenPipeline schemas
dtctl get settings-schemas | grep openpipeline

# View specific schema details
dtctl describe settings-schema builtin:openpipeline.logs.pipelines

# List log pipelines
dtctl get settings --schema builtin:openpipeline.logs.pipelines

# Get a specific pipeline by object ID
dtctl get settings <object-id> --schema builtin:openpipeline.logs.pipelines
```

See **[resources/settings-api.md](resources/settings-api.md)** for full details on managing OpenPipeline configurations through the Settings API.

### Verify & preview pipeline components

Before you apply a pipeline change, validate its individual components against the OpenPipeline engine and dry-run a processor against sample records — all read-only, no live config is touched. These use the same restricted DQL subset the engine enforces (matchers allow only `matchesPhrase`, `matchesValue`, `isNull`, `iAny`, …; DQL processor scripts are limited to processor commands like `parse`, `fields*`, `fieldsFlatten`), so they catch pipeline-context errors that a generic `dtctl verify query` misses.

```bash
# Verify a matching condition (inline, file, or stdin)
dtctl verify openpipeline-matcher 'matchesValue(content, "error")'
dtctl verify openpipeline-matcher -f matcher.dql
echo 'matchesValue(content, "error")' | dtctl verify openpipeline-matcher -f -

# Scope to a stage context or a configuration
dtctl verify openpipeline-matcher 'matchesValue(content, "error")' --context ROUTING_RULE
dtctl verify openpipeline-matcher 'matchesValue(content, "error")' --config-id logs

# Verify a DQL processor script
dtctl verify openpipeline-dql-processor 'parse content, "IPV4:ip"'
dtctl verify openpipeline-dql-processor -f processor.dql --config-id logs

# Preview a processor against its embedded sample records (-f required; pass the
# processor body itself — dtctl builds the request envelope around it). The body
# must be a complete processor definition; the endpoint validates the full schema
# and rejects a partial body. A minimal DQL processor:
#   {"type":"dql","id":"preview","description":"preview","enabled":true,
#    "matcher":"true","dqlScript":"fieldsAdd severity = \"INFO\"",
#    "sampleData":"{\"content\":\"hello\"}"}
dtctl exec preview-processor -f processor.json
dtctl exec preview-processor -f processor.json --config-id logs
cat processor.json | dtctl exec preview-processor -f -

# Structured output on any of them (verify: json/yaml/toon; preview also table/csv)
dtctl verify openpipeline-matcher 'matchesValue(content, "error")' -o json
dtctl verify openpipeline-dql-processor 'parse content, "x"' -o yaml
dtctl exec preview-processor -f processor.json -o yaml
```

The two `verify` commands **exit non-zero on an invalid verdict** in every output mode (including `-A`), so they drop straight into CI and agent pipelines without parsing JSON. Diagnostics (severity, message, source position) are surfaced in all formats. All three need only `openpipeline:configurations:read` — already in the read scope tier, so no new grants.

### Translate Classic pipelines to OpenPipeline

Migrating from Classic pipelines to OpenPipeline? `dtctl translate classic-pipelines` converts your tenant's Classic pipeline configuration for a scope into an OpenPipeline configuration pipeline (Settings shape). It is a read-only call that returns the translated pipeline verbatim — the reliable starting point you then review and apply via the Settings API.

```bash
# Translate the logs Classic pipeline (pretty-printed pipeline document)
dtctl translate classic-pipelines logs

# Translate business events and export as YAML for review/editing
dtctl translate classic-pipelines bizevents -o yaml > reference-pipeline.yaml

# Print the translated pipeline as JSON
dtctl translate classic-pipelines logs -o json

# Skip disabled rules in the translation (overrides the server default)
dtctl translate classic-pipelines logs --skip-disabled-rules=true
```

The scope is a positional argument and must be `logs` or `bizevents`. Every output format emits the translated pipeline document directly (no `{value, withWarning}` wrapper), so the exported file is applyable as-is. The translation is deterministic where possible; when a processing rule's definition script could not be translated automatically (`withWarning=true`), a warning is printed to stderr (and carried in the agent envelope under `-A`) and that part needs a manual rewrite. Apply the reviewed result with `dtctl create settings --schema builtin:openpipeline.<scope>.pipelines -f <file>`.

**Note:** The underlying API is public but early-adopter and may change.

## Deeper guides

- **[CONFIGURATION.md](CONFIGURATION.md)** — contexts, credentials, safety levels, apply hooks, aliases, command profiles
- **[OUTPUT_FORMATS.md](OUTPUT_FORMATS.md)** — every output format and the agent envelope
- **[AGENT_MODE.md](AGENT_MODE.md)** — agent output, the command catalog, and `dtctl inventory` (environment discovery)
- **[AGENT_SKILLS.md](AGENT_SKILLS.md)** — the bundled Agent Skill for AI coding assistants
- **[COOKBOOK.md](COOKBOOK.md)** — watch mode, dry runs, idempotent applies, pipeline integration, health checks, and troubleshooting
- **[OBSERVABILITY.md](OBSERVABILITY.md)** — distributed tracing and OTLP export
- **[EXTENSIONS.md](EXTENSIONS.md)** and **[LIVE_DEBUGGER.md](LIVE_DEBUGGER.md)** — feature guides
- **[SERVE.md](SERVE.md)** — experimental server mode

## Getting help

- `dtctl <command> --help` for any command
- `dtctl doctor` to diagnose connectivity, auth, and permission issues
- [GitHub Issues](https://github.com/Dynatrace/dtctl/issues) for bugs and feature requests
