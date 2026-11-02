# dtctl

[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/Dynatrace/dtctl/badge)](https://scorecard.dev/viewer/?uri=github.com/Dynatrace/dtctl)

[![Release](https://img.shields.io/github/v/release/Dynatrace/dtctl?style=flat-square)](https://github.com/Dynatrace/dtctl/releases/latest)
[![Build Status](https://img.shields.io/github/actions/workflow/status/Dynatrace/dtctl/build.yml?branch=main&style=flat-square)](https://github.com/Dynatrace/dtctl/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/Dynatrace/dtctl?style=flat-square)](https://goreportcard.com/report/github.com/Dynatrace/dtctl)
[![License](https://img.shields.io/github/license/Dynatrace/dtctl?style=flat-square)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Dynatrace/dtctl?style=flat-square)](go.mod)

**Your Dynatrace platform, one command away.**

`dtctl` is a CLI for **Dynatrace SaaS**. Manage workflows, dashboards, queries, and more from your terminal or let AI agents do it for you. Its predictable verb-noun syntax (inspired by `kubectl`) makes it easy for both humans and AI agents to operate.

> Looking for something else? [dtmgd](https://github.com/dynatrace-oss/dtmgd) covers Dynatrace Managed, and [dtwiz](https://github.com/dynatrace-oss/dtwiz) helps with Dynatrace setup and onboarding.

```bash
dtctl get workflows                           # List all workflows
dtctl query "fetch logs | limit 10"           # Run DQL queries
dtctl apply -f workflow.yaml --set env=prod   # Declarative configuration
dtctl get dashboards -o json                  # Structured output for automation
dtctl exec copilot nl2dql "error logs from last hour"
```

![dtctl dashboard workflow demo](docs/assets/dtctl-1.gif)

> **Active Development**: dtctl has stabilized considerably, but some commands and flags are still `experimental` or `development` tier (see [Stability](docs/STABILITY.md)). If you encounter any bugs or issues, please [file a GitHub issue](https://github.com/Dynatrace/dtctl/issues/new). Contributions and feedback are welcome!

**[Documentation](docs/README.md)** · **[Installation](docs/INSTALLATION.md)** · **[Quick Start](docs/QUICK_START.md)** · **[Command Reference](docs/COMMANDS.md)**

---

## Install

```bash
# Homebrew (macOS/Linux)
brew install dynatrace-oss/tap/dtctl
```

```bash
# Shell script (macOS/Linux)
curl -fsSL https://raw.githubusercontent.com/Dynatrace/dtctl/main/install.sh | sh
```

```powershell
# PowerShell (Windows)
irm https://raw.githubusercontent.com/Dynatrace/dtctl/main/install.ps1 | iex
```

Binary downloads, building from source, shell completion setup, and more in the **[Installation Guide](docs/INSTALLATION.md)**.

## Authenticate

```bash
# OAuth login (recommended, no token management needed)
dtctl auth login --context my-env --environment "https://abc12345.apps.dynatrace.com"

# Verify everything works
dtctl doctor
```

Token-based authentication and multi-environment configuration are covered in the **[Quick Start](docs/QUICK_START.md)**.

## Why dtctl?

- **Familiar CLI conventions**: `get`, `describe`, `edit`, `apply`, `delete`. If you (or your AI) know `kubectl`, you already know dtctl.
- **Built for AI agents**: Structured output (`--agent`), machine-readable command catalog (`dtctl commands`), environment data discovery (`dtctl inventory`), and a bundled [Agent Skill](https://agentskills.io) that teaches AI assistants how to operate Dynatrace
- **Multi-environment**: Switch between dev/staging/prod with a single command; safety levels prevent accidental changes
- **Watch mode**: Real-time monitoring with `--watch` for all resources
- **DQL passthrough**: Execute queries directly, with template variables and file-based input
- **Embeddable**: `dtctl serve http` (development-tier, opt in with `dtctl config set development.serve on`) or `pkg/engine` in Go runs the same command surface in-process for services and Workflow actions — multi-tenant per request, no host config, output byte-identical to the CLI
- **[NO_COLOR](https://no-color.org/) support**: Respects `NO_COLOR`, `FORCE_COLOR=1`, and auto-detects TTY

## Supported Resources

| Resource | Operations |
|----------|------------|
| Workflows | get, describe, create, edit, delete, apply, execute, logs, history, restore, diff, watch |
| Dashboards & Notebooks | get, describe, create, edit, delete, apply, share, history, restore, diff, watch |
| Documents & Trash | get, describe, create, edit, delete, share, history, restore |
| DQL Queries | execute, verify, template variables, live mode, filter segments, wait conditions, spill large results to a file + local `inspect` (rows/schema/stats) |
| SLOs | get, describe, create, edit, delete, apply, evaluate, watch |
| Settings | get schemas, get/create/update/delete objects |
| Buckets | get, describe, create, delete, apply, watch |
| Segments | get, describe, create, edit, delete, apply, watch |
| Lookup Tables | get, describe, create, delete, apply (CSV auto-detection) |
| Anomaly Detectors | get, describe, create, edit, delete, apply |
| Extensions 2.0 | get, describe, apply monitoring configs |
| Hub Extensions | get, describe, list releases, filter by keyword |
| App Functions & Intents | get, describe, execute, find, open (deep linking) |
| Analyzers & CoPilot | statistical analyzers, CoPilot chat, NL-to-DQL, document search |
| Cloud Integrations | AWS, Azure & GCP connections and monitoring (get, describe, create, delete, apply, update, enable) |
| EdgeConnect | get, describe, create, delete |
| Notifications | get, describe, delete, watch |
| Users & Groups | get, describe |
| Live Debugger | breakpoints, workspace filters, snapshot decoding |
| Platform Tokens | account create/list/delete token (`dt0s16.*` via Account Management API) |
| API Discovery | get apis (with `--uncovered`), describe api (operation index, one operation in full, raw spec) |

See the **[Command Reference](docs/COMMANDS.md)** for the full list of verbs, flags, resource types, and aliases.

## AI Agent Skills

dtctl ships with an [Agent Skill](https://agentskills.io) that teaches AI coding assistants how to use dtctl. Agents can also bootstrap at runtime with `dtctl commands`, which prints a compact minimal overview of verbs, resources, and subcommands (defaults to TOON); add `--brief` or `--full` for progressively more detail. Where `dtctl commands` answers *"what can I run?"*, `dtctl inventory` answers *"what is there to query?"* — it probes the current environment (read-only, budgeted) for fetchable data objects, buckets, filter segments, a live entity-type census, and capabilities present or absent, with the evidence cited for every absent one. The capability set is customizable via `--definitions` (see `docs/dev/examples/inventory-definitions.example.yaml`). For onboarding verification, `dtctl inventory arrivals --scope '<dql filter>'` answers *"is data arriving for this source right now?"* — a per-signal ingest state (live, stale, empty, no-data, n/a, absent, unknown) with counts and last-seen, gateable in CI with `--require`.

```bash
# Install via skills.sh
npx skills add Dynatrace/dtctl

# Or install with dtctl itself
dtctl skills install              # Auto-detects your AI agent
dtctl skills install --for claude # Or specify explicitly
dtctl skills install --global     # User-wide installation

# Or copy manually
cp -r skills/dtctl ~/.agents/skills/   # Cross-client (any agent)
```

Compatible with GitHub Copilot, Claude Code, OpenAI Codex CLI, Cursor, Kiro, Junie, OpenCode, OpenClaw, and other [Agent Skills](https://agentskills.io)-compatible tools. See the **[AI Agent Mode docs](docs/AGENT_MODE.md)** for details on the structured JSON envelope and agent auto-detection.

### Dynatrace domain skills

For deeper Dynatrace domain knowledge (DQL syntax, observability patterns, dashboards, logs, Kubernetes, and more) install the skills from **[Dynatrace/dynatrace-for-ai](https://github.com/Dynatrace/dynatrace-for-ai)**:

```bash
npx skills add dynatrace/dynatrace-for-ai
```

These skills provide the domain context (e.g., how to write DQL queries, which metrics to use for service health, how to navigate distributed traces) while dtctl provides the operational tool to act on it. Together they give AI agents everything they need to work with Dynatrace effectively.

## Running as a Service

`dtctl serve http` exposes the CLI over HTTP for AI agents and automation. It is a **development-tier** feature: run `dtctl config set development.serve on` (or set `DTCTL_DEVELOPMENT=serve`) to enable the command, and expect the contract to change. See [Stability](docs/STABILITY.md) for what each tier promises. One request executes one dtctl command line for one tenant (`POST /v1/execute`) and returns the exact output the CLI would have printed. Each request brings its own environment URL and token, file arguments resolve against per-request virtual files, and host-only commands are unavailable. Go callers can embed `pkg/engine` directly instead. It is a reference implementation with no authentication of its own — see [Server Mode](docs/SERVE.md) before exposing it beyond localhost.

## Observability

dtctl supports W3C Trace Context propagation and OTLP span export via the OpenTelemetry SDK. See [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md) for full details on distributed tracing, environment variables, and CI/CD pipeline integration.

## Documentation

Full documentation lives in the repo's **[docs/](docs/README.md)** directory:

- [Installation](docs/INSTALLATION.md): Homebrew, shell script, binary download, build from source, shell completion
- [Quick Start](docs/QUICK_START.md): Authentication, first commands, common patterns
- [Configuration](docs/CONFIGURATION.md): Contexts, credentials, safety levels, aliases
- [Command Reference](docs/COMMANDS.md): All verbs, flags, resource types, and examples
- [Output Formats](docs/OUTPUT_FORMATS.md): Table, JSON, YAML, CSV, charts
- [AI Agent Mode](docs/AGENT_MODE.md): Structured envelope, auto-detection, agent skill
- [Token Scopes](docs/TOKEN_SCOPES.md): Required API token scopes per safety level
- [Server Mode](docs/SERVE.md): Running dtctl as a server, the execute API, and embedding `pkg/engine`
- [Stability](docs/STABILITY.md): What `stable`, `experimental`, and `development` promise, and how to pin a floor

Resource-specific guides: [API Discovery](docs/resources/api-discovery.md) · [DQL Queries](docs/resources/dql-queries.md) · [Workflows](docs/resources/workflows.md) · [Dashboards](docs/resources/dashboards-notebooks.md) · [SLOs](docs/resources/slos.md) · [Settings](docs/resources/settings-api.md) · [Extensions](docs/resources/extensions.md) · [Analyzers](docs/resources/analyzers.md) · [CoPilot](docs/resources/copilot.md) · [and more...](docs/QUICK_START.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache License 2.0. See [LICENSE](LICENSE).
