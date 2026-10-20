# Changelog

All notable changes to the dt-cli-sdk will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Changed

- **Versioning is now aligned with the dtctl CLI.** The SDK is tagged `sdk/vX.Y.Z` at the same commit as the CLI's `vX.Y.Z`, so the next release jumps from `0.2.0` to the CLI's version rather than to `0.3.0`. Nothing about the API changes; the alignment is what makes the CLI module's dependency on the SDK resolvable for external importers (see `docs/dev/SERVICE_ENGINE_DESIGN.md`). A new tag therefore appears on every CLI release, whether or not `sdk/` changed — this file remains the record of what actually moved.

### Added

- `sdk/inventory` — Environment data-inventory discovery engine: capabilities present/absent/unknown with cited evidence, live entity census, buckets, data-object catalog partition, and a budgeted discovery battery. Execution-agnostic — every query runs through a caller-supplied `Runner` interface, so backend services can embed discovery with their own DQL client. Definitions parsing is byte-based (`ParseDefinitions`); file loading stays in the CLI layer. Definition sets constructed in Go (bypassing `ParseDefinitions`) are checked by `ValidateDefinitions`, which `Discover` runs up front — a malformed or nil definition fails fast instead of being silently skipped.

- `sdk/api/query` — `ExecuteRequest.PollingPromiseSeconds` field maps to the new `pollingPromiseSeconds` body parameter on `query:execute`, instructing the backend to auto-cancel a running query if the client does not poll within the specified number of seconds. `Handler.ExecuteAndPoll` defaults the field to 5 seconds when the caller leaves it unset; a caller-supplied non-zero value is preserved.

- `sdk/api/document` — `Handler.MakeDocumentPublic` and `Handler.MakeDocumentPrivate` set a document's visibility through the Document API's `:makePublic` / `:makePrivate` actions, which take no locking version and are a no-op when the document is already in that state.

### Deprecated

- `sdk/api/document` — `Handler.SetDocumentPublic`: use `MakeDocumentPublic`. It now calls `:makePublic` and ignores its version argument, instead of sending the deprecated `isPrivate` field of `PATCH /documents/{id}`, so it no longer returns `ErrVersionConflict`.

## [0.2.0] - 2026-05-16

### Added

- `sdk/api/` — Typed Go clients for 16 Dynatrace REST APIs, each with CRUD operations, pagination, and structured error handling:
  `analyzer`, `appengine`, `bucket`, `copilot`, `document`, `edgeconnect`, `extension`, `hub`, `iam`, `livedebugger`, `notification`, `query`, `segment`, `settings`, `slo`, `workflow`.

### Changed

- `sdk/httpclient` — Logger is now wired through the HTTP client; callers can inject a `Logger` to get request-level debug output.

### Dependencies

- Bump go-resty to v2.17.2, godbus to v5.2.2, go-keyring to v0.2.8.
- Bump golang.org/x/net.

## [0.1.0] - 2026-05-09

### Added

- `sdk/urls` — Dynatrace environment URL validation and correction.
- `sdk/auth` — Token type detection and classification.
- `sdk/httpclient` — HTTP client with retry, typed errors, and pagination.
- `sdk/agentmode` — AI agent detection and structured JSON envelope.
- `sdk/credstore` — OS keyring and file-based credential storage.
