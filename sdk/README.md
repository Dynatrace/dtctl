# dtctl SDK

A shared Go module for building tools and integrations against the Dynatrace platform, extracted from [dtctl](https://github.com/dynatrace-oss/dtctl).

## Install

```bash
go get github.com/dynatrace-oss/dtctl/sdk@latest
```

## Versioning

The SDK is tagged `sdk/vX.Y.Z` **in lockstep with the dtctl CLI release** it was
cut from: dtctl `vX.Y.Z` and `sdk/vX.Y.Z` are the same commit. The two modules
ship from one repository and are compiled against each other in CI, so aligning
the numbers is what lets the CLI module declare a resolvable dependency on the
SDK — and lets you pair a CLI version with the SDK that matches it.

Two consequences: a new SDK tag appears on every CLI release, including ones that
change nothing under `sdk/` (check [CHANGELOG.md](CHANGELOG.md) for what actually
moved), and the version jumped from `sdk/v0.2.0` straight to the CLI's line when
this alignment was introduced.

## What's in the SDK

### Infrastructure

| Package | Description |
|---------|-------------|
| `sdk/httpclient` | HTTP client with retry, rate limiting, typed errors, pagination helpers |
| `sdk/auth` | Token type detection (API token vs OAuth/Bearer) |
| `sdk/urls` | Dynatrace environment URL validation and normalization |
| `sdk/agentmode` | AI agent environment detection and structured JSON envelope |
| `sdk/inventory` | Environment data-inventory discovery — capabilities present/absent/unknown with evidence, entity census, buckets — driven through a caller-supplied DQL `Runner` |

### API wrappers (`sdk/api/`)

Thin, typed Go clients for Dynatrace REST APIs. Each package covers one API surface with CRUD operations, pagination, and structured error handling.

| Package | API |
|---------|-----|
| `api/analyzer` | Query analyzer (DQL validation) |
| `api/appengine` | App Engine (functions) |
| `api/bucket` | Grail bucket management |
| `api/classicpipelinestranslate` | Classic pipeline translation |
| `api/copilot` | Davis Copilot completions |
| `api/document` | Documents (dashboards, notebooks) + trash |
| `api/edgeconnect` | EdgeConnect management |
| `api/extension` | Extensions 2.0 lifecycle |
| `api/hub` | Dynatrace Hub items |
| `api/iam` | Account-level IAM (groups, users, policies) |
| `api/livedebugger` | Live debugger sessions |
| `api/notification` | Notification/alerting configuration |
| `api/platformtoken` | Account platform-token management |
| `api/query` | DQL query execution |
| `api/segment` | Data segments |
| `api/serviceuser` | Account service-user management |
| `api/settings` | Settings 2.0 objects |
| `api/slo` | Service Level Objectives |
| `api/workflow` | Workflow CRUD and execution |

## What goes into the SDK

- **Direct REST API wrappers.** One package per Dynatrace API surface, exposing typed Go structs and CRUD functions that map 1:1 to API endpoints.
- **Shared infrastructure.** HTTP client, auth, URL handling, credential storage -- anything two or more consumers would need.
- **No CLI concerns.** No Cobra, no Viper, no terminal formatting, no interactive prompts.

## What does NOT go into the SDK

- **Convenience wrappers that compose multiple APIs.** If a function calls API A then API B (e.g. creating an environment share link and then making the document public), it belongs in the CLI.
- **Display/formatting logic.** Functions like `populateDisplayFields`, `GetRaw`, DQL-to-AST conversion, or anything that transforms data for human consumption.
- **Packages that wrap non-Dynatrace APIs** or aggregate multiple API calls into a higher-level operation (e.g. `anomalydetector`, `azureconnection`, `gcpconnection`).
- **CLI-specific types.** Structs with fields that only exist for display purposes (e.g. `VariablesDisplay`, computed summary fields).
- **Lookup/resolution helpers.** Name-to-ID resolution, interactive disambiguation, resource resolvers.

## Design principles

- **No global state.** Everything is constructed explicitly.
- **No file I/O.** The SDK never reads from files or stdin. Functions like `ReadFileOrStdin` and `ParseInputFromFile` belong in the CLI layer (`pkg/resources/`). SDK functions accept Go types, never file paths.
- **Minimal dependencies.** No Cobra, Viper, logrus, or OpenTelemetry.
- **One-way dependency.** CLI -> SDK, never SDK -> CLI.
- **Logging is injected.** The SDK accepts a `Logger` interface; it never imports a logging library.
- **Errors are typed.** Use `errors.Is`/`errors.As` reliably. API errors follow the format `"API error (<code>): <message>"`.

## Versioning

- `sdk/v0.x.y` -- no stability promise during v0.
- `sdk/v1.0.0` -- once at least two consumer CLIs have adopted the module.
