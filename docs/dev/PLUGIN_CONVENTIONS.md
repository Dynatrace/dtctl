# dtctl Plugin Conventions

**Status:** v1, since 2026-07-12
**Audience:** authors of dtctl exec plugins.

dtctl follows the kubectl exec-plugin convention and nothing heavier: an
executable named `dtctl-<name>` on `PATH` runs as `dtctl <name>`. There is no
installer, no registry, no RPC — deliberately.

## Dispatch semantics

- On an unknown top-level command, dtctl searches `PATH` for the **longest
  dash-joined match**: `dtctl foo bar baz` tries `dtctl-foo-bar-baz`, then
  `dtctl-foo-bar` (arg `baz`), then `dtctl-foo` (args `bar baz`).
- The plugin is exec'd (process replacement on Unix; child process with
  verbatim exit-code propagation on Windows, where `.exe`/`.bat`/`.cmd` are
  recognized). Remaining arguments pass through untouched.
- **Built-in commands always win.** A plugin can never shadow or override a
  built-in dtctl command; `dtctl plugin list` warns when a plugin name
  collides.
- Leading dtctl flags (`dtctl --context prod foo`) are consumed by dtctl and
  reflected into the environment contract below — they are not passed to the
  plugin.
- **Dispatch is a gated host ability.** It runs only when
  `Capabilities.PluginDispatch` is granted: the CLI binary grants it, embedded
  and service invocations do not. Without the grant there is no `PATH` lookup at
  all — an unknown command falls through to the normal suggestion-enhanced
  unknown-command error. `plugin` itself is one of the top-level commands
  removed from the service surface
  ([SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md)).

## Environment contract

dtctl sets these variables for the plugin process:

| Variable | Meaning |
|---|---|
| `DTCTL_CONTEXT` | Context-name override (reflects `--context` when given; otherwise inherited from the caller's environment, where it has the same meaning) |
| `DTCTL_CONFIG` | Config file path in effect (explicit `--config`, discovered `.dtctl.yaml`, or the default global path) |
| `DTCTL_AGENT=1` | Agent mode is active — emit the structured JSON envelope |
| `DTCTL_PLAIN=1` | Plain mode — no colors, no interactive prompts |
| `DTCTL_CALLER_VERSION` | The dispatching dtctl's version, for compatibility decisions |

**No tokens from dtctl.** dtctl never passes credentials via argv, and it
strips its documented credential variables (`DTCTL_TOKEN`, `DT_API_TOKEN`,
`DTCTL_ACCOUNT_TOKEN`) from the plugin's environment. The rest of the caller's environment is
inherited as-is — a token you placed in a custom variable (e.g. one referenced
via `${MY_TOKEN}` in the config file) is not recognized as a credential and
reaches the plugin like any other inherited variable. Plugins resolve
credentials themselves via the shared config contract
([CONFIG_CONTRACT.md](CONFIG_CONTRACT.md)): the config file named by
`DTCTL_CONFIG` and the OS keyring service `dtctl`. The dtctl sdk
(`github.com/Dynatrace/dtctl/sdk`) is the supported way to do this.

## Conventions a good plugin follows

1. **Flags**: support `-o json` for machine-readable output and honor
   `DTCTL_PLAIN` / `--plain` (no colors, no prompts). Respect
   [`NO_COLOR`](https://no-color.org/).
2. **Agent envelope**: when `DTCTL_AGENT=1`, wrap output in the dtctl
   envelope — `{"ok": true, "result": ...}` on success,
   `{"ok": false, "error": {"code": "...", "message": "..."}}` on failure —
   on stdout. AI agents are dtctl's primary persona; a plugin that ignores
   this is invisible garbage to them.
3. **Safety levels**: the context's `safety-level` is a promise to the user
   that dtctl cannot enforce inside your process. Check it before mutating
   anything (the sdk exports the safety semantics); a plugin that writes
   through a `readonly` context is broken.
4. **Exit codes**: 0 success, non-zero failure. Exit codes propagate to the
   caller verbatim.
5. **Secrets hygiene**: never print tokens; never accept them via argv.
6. **macOS keychain**: each distinct binary gets its own keychain-access
   prompt on first credential read. Expected — tell your users once.

## Discovery and support triage

- `dtctl plugin list` shows every `dtctl-*` executable on PATH with its
  binary and path — the first stop when triaging "is this a dtctl bug or a
  plugin bug".
- `dtctl commands --brief -o json` (the agent bootstrap catalog) includes
  discovered plugins under `plugins`, so agents can see them.

## Non-goals

No plugin installer, no registry, no scaffolding, no Go `plugin` package, no
gRPC. If real third-party plugins appear (rule of thumb: ≥5), revisit.
