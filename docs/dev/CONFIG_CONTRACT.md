# The dtctl Config Contract

**Status:** v1, normative since 2026-07-12
**Audience:** dtctl and any `dtctl-*` plugin that reads the shared
configuration. This is the state contract: everything a second binary may
rely on, and nothing more. Golden fixtures live in
`sdk/session/testdata/contract/`; `sdk/session/contract_test.go` enforces this
document. A change that breaks those tests is a contract change and must
update this spec in the same PR.

## File locations

| Artifact | Path |
|---|---|
| Global config | `$XDG_CONFIG_HOME/dtctl/config` (`~/.config/dtctl/config` on Linux, `~/Library/Application Support/dtctl/config` on macOS, `%LOCALAPPDATA%\dtctl\config` on Windows) |
| Project-local config | `.dtctl.yaml`, discovered upward from the working directory; used **exclusively** (never merged with the global file) |
| Explicit config | `--config <path>` flag, wins over discovery |
| OAuth file store | `$XDG_DATA_HOME/dtctl/oauth-tokens/<sanitized-name>.json`, mode 0600 (dir 0700) |
| Token-refresh lock | `$TMPDIR/dtctl-token-refresh-<sha256[:8] of env:tokenRef>.lock` |

Security note: auto-discovered `.dtctl.yaml` files are treated as untrusted
(the classic "checked-out repo / shared directory" scenario). Five restrictions
apply — see `Config.IsLocal()`:

1. **Env-var expansion limited to the environment URL.** `${VAR}` references
   are expanded only for a context's `environment`, and only once, at load
   (`Config.resolveLocalEnvironments`), so every reader of
   `Context.Environment` — the origin check, the client, `doctor`, the
   live-debugger handlers, spill's tenant id — sees the same resolved value
   without having to remember to resolve. `SaveTo` writes the unexpanded
   reference back (`Config.restoreRawEnvironments`) so a load-modify-save cycle
   does not bake one developer's expansion into a committed file. Every other
   field is left as-is; use `--config` or `DTCTL_CONFIG` for a trusted config
   where expansion runs everywhere.
2. **No inline tokens.** A local config may reference a `token-ref` but not
   define its value; credentials must come from the keyring / file store.
3. **Origin binding.** The environment URL in the local context must resolve to
   the same hostname as the global config binding for the same `token-ref`,
   preventing credential redirection to a foreign host.
4. **Safety-level clamp.** The context's level is reduced to
   `min(local, global)` so a local file cannot grant itself more than the owner
   of the borrowed credential granted. A `token-ref` with no global binding is
   clamped to `DefaultSafetyLevel`.
5. **Destination allowlist.** `NewClientFromConfig` requires the resolved
   environment URL to be a *bare origin* — https, a `.dynatrace.com` /
   `.dynatracelabs.com` host, and no userinfo, path, query or fragment
   (`urls.IsDynatraceEnvironmentOrigin`). The bare-origin shape is what keeps
   restriction 1 safe: expansion can fill in the destination but cannot append
   to it, so an unrelated host secret cannot ride out in a path or query.

Restrictions 3 and 4 share one lookup, `buildGlobalBindings()`, keyed by
`token-ref` — *not* by context name, since a local config is free to rename its
context while still borrowing the same credential. The clamp is resolved on
each `GetEffectiveSafetyLevel()` call rather than snapshotted at load time, so
it survives a `--context` / `DTCTL_CONTEXT` override. A `token-ref` with no
global binding yields no ceiling of its own, so restriction 4 falls back to the
default. Where several global contexts share a `token-ref`, the binding keeps
every bound host and the *strictest* level, so YAML ordering cannot widen
either.

Code-execution keys (aliases, apply hooks) are loaded for round-tripping but
**never honored**.

Write target: config-management commands write the same file they read (see
`saveConfig`), so once a `.dtctl.yaml` is discovered it captures writes too.
Because restrictions 2–4 mean a local config can only work *alongside* a global
binding, `config set-context` and `config set-credentials` take `--global` to
write to `DefaultConfigPath()` regardless of discovery; without it, the two
commands that create the binding would land in the file that needs it.

## Schema (v1)

YAML document. Top-level keys: `apiVersion`, `kind`, `current-context`,
`contexts` (list of `{name, context}`), `tokens` (list of `{name, token}`),
`preferences`, `aliases`, `spill`, `query-limits`. Per-context keys:
`environment`, `token-ref`, `safety-level` (`readonly` | `readwrite-mine` |
`readwrite-all` | `dangerously-unrestricted`; empty means `readwrite-all`),
`description`, `hooks`, `spill`, `query-limits`, `usage-tracking` (bool,
default off, honored only from the global config, never an auto-discovered
`.dtctl.yaml`; see [USAGE_TRACKING.md](../USAGE_TRACKING.md)). The Go structs in
`sdk/session/config.go` are the schema's source of truth;
`testdata/contract/v1-full.yaml` exercises every field.

`query-limits` keys: `scan-limit-gbytes`, `max-result-records`,
`max-result-bytes`, `sampling-ratio`. Like `spill`, the per-context block
overrides the top-level one **per field**, and an unset (zero) field means
"defer to the next layer" — so a reader that ignores the block behaves exactly
as one that never knew about it. Values must be non-negative; the merge
therefore only tightens, and lifting a configured limit is a command-line
decision (`--no-query-limits`), not a config one.

Semantics both binaries must share: `safety-level` (a `readonly` context means
the same thing everywhere) and token resolution order (see below).

### Version policy

- `apiVersion` spellings accepted as schema v1: **empty** (pre-enforcement
  configs), **`v1`**, and **`dtctl.io/v1`** (written by `dtctl config init`).
- An unrecognized `apiVersion` is a **hard load error** naming the version —
  never a silent misread. This is the version-skew answer: "consumer N supports
  config schema ≤ M" is testable.
- Within v1 the schema evolves **additively only**. Renaming or redefining an
  existing key requires bumping the version.

### Tolerant parsing and round-trip preservation

- Readers ignore unknown keys (yaml.v3 default — do not enable strict mode).
- Writers must not destroy unknown keys: `Config.SaveTo` grafts keys unknown
  to the running build from the file being overwritten back into the saved
  document (top level, per-context and per-token matched by `name`, and
  nested structs — see `sdk/session/preserve.go`). Known keys are owned by the
  writer: deleted contexts and cleared `omitempty` fields stay deleted.
- Comments and key order are **not** preserved; only data survives.

### Environment variable expansion

`$VAR` / `${VAR}` in the file expand from the process environment at load.
Shell positional/special parameters (`$1`, `${10}`, `$@`, …) are preserved
verbatim so hook commands survive. Unset variables expand to the empty
string. Management commands that rewrite the file must load with
`LoadWithoutExpansion` so templates round-trip unexpanded.

## Credential store

- **OS keyring service name: `dtctl`** — shared by every consumer; changing
  it strands all stored credentials.
- Key formats: plain API tokens under their token-ref name; OAuth token sets
  (JSON) under `oauth:<env>:<tokenRef>` with `<env>` ∈ `prod` | `dev` |
  `hard`, legacy entries under `oauth:<tokenRef>`.
- Token resolution order (in `Config.GetToken`): keyring OAuth entry →
  keyring plain token → OAuth file store (when the keyring is unavailable or
  `DTCTL_TOKEN_STORAGE=file`) → inline `token` value in the config file.
- `DTCTL_DISABLE_KEYRING` (any non-empty value) disables the keyring;
  `DTCTL_TOKEN_STORAGE=file` forces the file store.
- **Deletion must clear the whole fan-out.** One token ref occupies the plain
  entry, an OAuth entry per environment, a `:scopes` companion for each, the
  pre-environment legacy `oauth:<tokenRef>` form, and file-store copies of all
  of them. `Config.DeleteToken` sweeps that set across both backends; anything
  narrower leaves live token material behind, including material `GetToken` can
  no longer resolve but an attacker with the file could still read.
  Deletion imposes no ordering against context removal: `oauthKeyringNames`
  enumerates `prod`/`dev`/`hard` unconditionally and `oauthEnvironmentFromURL`
  returns nothing outside that set, so a context never names an entry the
  unconditional sweep would miss.
- **No consumer shells out to OS keychain tooling.** `security`, `secret-tool`,
  and `cmdkey` reach past dtctl's own entries, and their read verbs print
  secrets, so they must not appear in code, docs, or the shipped skill. Presence
  is checked with `dtctl auth status`, which reports it without printing the
  token.
- **macOS keychain UX**: keychain access is granted per binary, so each
  consumer (dtctl and every plugin) triggers its own one-time
  keychain-access prompt on first credential read. Expected behavior —
  document it, don't "fix" it.

## Write rules

1. **dtctl owns all config-file writes** — context CRUD, login flows,
   safety-level assignment. Plugins treat the config file as **read-only**.
2. **The token store is the one shared write surface.** OAuth refresh tokens
   rotate on use, so any long-running consumer must persist refreshed token
   sets — and must do so through the cross-process refresh lock
   (`sdk/session`, `TokenManager`), never with an unlocked read-modify-write.
   Concurrent unlocked refreshes double-spend the rotating refresh token and
   strand one side's credentials (`invalid_grant`).
3. **Context overrides are session-local.** The `--context` flag and the `DTCTL_CONTEXT` env var override the current context in
   memory only. The sole way to persist a switch is `dtctl ctx <name>`
   (or `dtctl config use-context`).

## Environment variable overrides

| Variable | Meaning |
|---|---|
| `DTCTL_CONTEXT` | Session-local current-context override; flag `--context` wins over it. Exported to plugins. |
| `DTCTL_OUTPUT` | Default output format when `-o/--output` is not given (dtctl only). |
| `DTCTL_DISABLE_KEYRING` | Disable the OS keyring (any non-empty value). |
| `DTCTL_TOKEN_STORAGE` | `file` forces the file-based OAuth store. |
| `DTCTL_CLIENT_ID` | OAuth client ID; supplying it with `DTCTL_CLIENT_SECRET` switches `auth login` to the client credentials grant. Flag `--client-id` wins over it. |
| `DTCTL_CLIENT_SECRET` | OAuth client secret for that grant. Preferred over `--client-secret`, which is visible in the process table. |
| `DTCTL_ACCOUNT_URN` | Account URN sent as the RFC 8707 resource indicator by the client credentials grant. Flag `--account-urn` wins over it. |

**Sanctioned bypass: `cmd.Session`.** An embedded invocation can carry its own
environment and token, and then none of this contract applies to it: no config
file is read (a synthetic in-memory single-context config stands in), no keyring
is touched, and `DTCTL_TOKEN`, `DT_API_TOKEN`, `DTCTL_ACCOUNT_TOKEN`,
`DTCTL_CONFIG`, `DTCTL_CONTEXT`, `DTCTL_PROFILE`, and `DTCTL_OUTPUT` are unset
for the duration of the invocation so the host's identity and preferences cannot
shape a tenant's request. See
[SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md).

## Golden fixtures

| Fixture | Asserts |
|---|---|
| `v1-full.yaml` | Every known field parses; unknown fields at all levels are tolerated and survive a load-modify-save cycle |
| `v1-minimal.yaml` | Minimal config loads; `apiVersion` is optional |
| `future-version.yaml` | Unsupported schema version fails loudly |

The fixtures live in the sdk module (`sdk/session/testdata/contract/`), so
after the repo split both binaries keep testing against the same versioned
artifacts — the fixtures are the compatibility test between independently
released binaries.
