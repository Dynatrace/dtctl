<!-- Migrated from the standalone docs site; SME to verify against the current dtctl binary. -->

# Configuration

## Authentication

### OAuth Login (Recommended)

Browser-based SSO login with automatic token refresh:

```bash
dtctl auth login --context my-env --environment "https://abc12345.apps.dynatrace.com"
```

Tokens are stored securely in your OS keyring.

**No keyring (headless Linux, fresh VMs, containers):** run `auth login` in a
terminal and dtctl asks once whether to keep tokens in files under
`~/.local/share/dtctl/oauth-tokens/` (owner-only, not encrypted) instead. Your
answer is remembered on that machine and applies only while there is no keyring. dtctl never switches silently, and scripts
or CI never get the question: set `DTCTL_TOKEN_STORAGE=file` there. To undo a
remembered choice, delete the marker file `dtctl doctor` names, or set
`DTCTL_TOKEN_STORAGE=keyring`.

To log out:

```bash
dtctl auth logout
```

Check your current auth state with `dtctl auth status`, and force a token refresh with `dtctl auth refresh`.

### Non-Interactive OAuth (CI/CD)

Pipelines have no browser and no user, so the interactive login cannot complete
there. Supplying an OAuth client ID and secret switches `auth login` to the
OAuth 2.0 client credentials grant
([RFC 6749 §4.4](https://www.rfc-editor.org/rfc/rfc6749#section-4.4)), which
needs neither:

```bash
export DTCTL_CLIENT_ID="dt0s02.EXAMPLE"
export DTCTL_CLIENT_SECRET="dt0s02.EXAMPLE.SECRET"
export DTCTL_ACCOUNT_URN="urn:dtaccount:00000000-0000-0000-0000-000000000000"
export DTCTL_TOKEN_STORAGE=file   # no keyring on a build agent

dtctl auth login \
  --context ci \
  --environment "https://abc12345.apps.dynatrace.com" \
  --scopes storage:logs:read
```

Prefer the environment variables over the equivalent `--client-id`,
`--client-secret` and `--account-urn` flags: command line arguments are visible
to every other process on the machine. Supplying only one half of the
credential pair — or an account URN or `--scopes` with no pair at all — is a
usage error rather than a silent fallback to the browser flow, which on a runner
would look like an unexplained hang.

This is a different credential type from the platform token below: the client
credentials grant needs an OAuth client (`dt0s02.` client ID **and** secret),
not a `dt0s16.` platform token.

How it differs from the interactive flow:

- **No user identity.** The grant authenticates the application, not a person,
  so the userinfo lookup is skipped and `dtctl auth whoami` has nothing to
  report.
- **`--safety-level` does not narrow the token.** It gates dtctl on the client
  side. Without `--scopes` the token carries every scope the OAuth client was
  granted, so `auth login` warns and prints the scopes the token actually came
  back with. Pass `--scopes`, or provision the OAuth client with only the
  scopes the pipeline needs.
- **No refresh token**, per
  [RFC 6749 §4.4.3](https://www.rfc-editor.org/rfc/rfc6749#section-4.4.3) — a
  client holding its own credentials can just ask for another access token. So
  `dtctl auth refresh` cannot renew it and says so; re-run `dtctl auth login`
  instead.
- **`DTCTL_ACCOUNT_URN` is sent as an
  [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707) resource indicator.**
  Omitting it sends no indicator at all, so the audience is whatever the token
  endpoint defaults to, rather than being bound to one environment the way an
  interactive token is.
- **`--timeout` (default `5m`) bounds the token request**, so a stalled token
  endpoint fails the pipeline step instead of holding the runner.

`DTCTL_TOKEN_STORAGE=file` is normally required alongside the credentials,
since build agents rarely have a keyring — see
[Credential storage](#credential-storage).

### Token-Based Auth

For CI/CD or headless environments, use a platform API token:

```bash
dtctl config set-context my-env \
  --environment "https://abc12345.apps.dynatrace.com" \
  --token-ref my-token

dtctl config set-credentials my-token \
  --token "dt0s16.XXXXXXXX.YYYYYYYY"
```

### Creating a Platform Token

1. Go to [https://myaccount.dynatrace.com/platformTokens](https://myaccount.dynatrace.com/platformTokens) (Account Management > **My platform tokens**)
2. Select **Platform token** and fill in name, expiration, account, and environments
3. Add the required scopes for your use case
4. Select **Generate** and copy the token immediately -- it's only shown once

See the [Dynatrace Platform Tokens documentation](https://docs.dynatrace.com/docs/manage/identity-access-management/access-tokens-and-oauth-clients/platform-tokens) for detailed instructions.

### Current User Identity

Check who you're authenticated as:

```bash
dtctl auth whoami
```

Use `dtctl auth whoami -o json` for machine-readable output, or `--id-only` to get just the user ID.

### Credential storage

OAuth and platform tokens are stored in your OS keyring (macOS Keychain, Windows Credential Manager, Linux Secret Service). The only exception is file-based storage, which you opt into explicitly (`DTCTL_TOKEN_STORAGE=file`, or by answering the one-time prompt on a machine with no keyring): those OAuth tokens are kept in owner-only (`0600`) files, not encrypted. A remembered answer applies only while the machine has no keyring; a locked or broken keyring is reported as an error rather than falling back.

On **Linux and WSL**, gnome-keyring can start with only a transient session collection and no persistent login collection. When `dtctl auth login` detects a missing collection (`failed to unlock correct collection`), it connects to the D-Bus Secret Service, creates a persistent collection with the `default` alias, and triggers an OS password prompt if required (polling for up to two minutes). If automatic creation fails, the error message suggests alternatives such as token-based auth. `dtctl doctor` includes a keyring check that reports backend status.

Some keyring backends impose a per-item size limit, and a large OAuth response (many scopes or large JWTs) can exceed it. dtctl falls back automatically: it stores the full token set when it fits, drops the largest JWT fields when it does not, and in the worst case keeps only the refresh token and fetches a fresh access token on the next command. `dtctl auth login` therefore succeeds even when a size limit is reached, and tokens stay keyring-backed.

If you see `failed to save token to keyring: ... data passed to Set was too big`, a size limit was hit; dtctl falls back to compact storage automatically. Re-run `dtctl auth login --context <name> --environment <url>` if the error persists.

### Cloud agent sandboxes (Claude Code on the web)

The setup script runs without the session's environment variables, so the version
and URL go in the script and the token stays an environment variable, stored as a
`${DT_API_TOKEN}` reference that dtctl resolves at runtime.

Setup script:

```bash
set -e
DTCTL_VERSION=0.40.0
DT_ENVIRONMENT_URL=https://abc12345.apps.dynatrace.com

curl -fsSL "https://github.com/dynatrace-oss/dtctl/releases/download/v${DTCTL_VERSION}/dtctl_${DTCTL_VERSION}_linux_amd64.tar.gz" | tar -xz -C /usr/local/bin dtctl
dtctl config set-context web --environment "$DT_ENVIRONMENT_URL" --token-ref t --safety-level readonly
dtctl config set-credentials t --token '${DT_API_TOKEN}'
```

Environment variables:

```
DT_API_TOKEN=dt0s16.XXXXXXXX.YYYYYYYY
```

Allow `*.dynatrace.com` in the network settings. `install.sh` does not work here:
its GitHub API lookup hits the sandbox's shared rate limit (HTTP 403).

## Multiple Environments

### Create Contexts

```bash
# Development
dtctl config set-context dev \
  --environment "https://dev.apps.dynatrace.com" \
  --token-ref dev-token \
  --safety-level dangerously-unrestricted

# Production (read-only)
dtctl config set-context prod \
  --environment "https://prod.apps.dynatrace.com" \
  --token-ref prod-token \
  --safety-level readonly
```

### Switch Contexts

```bash
dtctl config use-context dev

# Or use the shortcut:
dtctl ctx dev

# List all contexts
dtctl ctx
```

The `ctx` command is a shorthand for the common `config` operations. With no
argument it lists contexts; with a name it switches. It also has subcommands:

```bash
dtctl ctx current                 # Show the current context name
dtctl ctx describe prod           # Show details of a context
dtctl ctx set staging \           # Create or update a context and switch to it
  --environment "https://staging.apps.dynatrace.com" --token-ref staging-token
dtctl ctx delete old-env          # Delete a context
dtctl ctx token prod              # Print the resolved token for a context (defaults to current)
```

### Delete Contexts and Credentials

Deleting a context leaves the credential it referenced in place, so removing
both is two decisions:

```bash
# Delete a context, keep the credential
dtctl config delete-context old-env

# Delete a context and the credential it references
dtctl config delete-context old-env --delete-credentials

# Delete a credential on its own (shared, or context already gone)
dtctl config delete-credentials old-token
```

`delete-context --delete-credentials` refuses as a no-op when the credential is
shared with other contexts, and the error names the command that removes it for
all of them. `delete-credentials` on its own always deletes, warning if a
context still points at the now-missing credential. Both honor `--dry-run`.

> Remove credentials only with these commands — they clear every entry a
> credential occupies, which a direct OS keychain delete does not. Never use
> `security`, `secret-tool`, or `cmdkey` on dtctl credentials. To confirm a
> credential is gone, use `dtctl auth status`, which reports presence without
> printing the token.

### One-Time Context Override

Run a single command against a different context without switching:

```bash
dtctl get workflows --context prod
```

## Per-Project Configuration

Create a `.dtctl.yaml` in your project root for team or CI/CD configuration:

```bash
dtctl config init
```

This generates a template with environment variable placeholders:

```yaml
apiVersion: dtctl.io/v1
kind: Config
current-context: production
contexts:
  - name: production
    context:
      environment: ${DT_ENVIRONMENT_URL}
      token-ref: my-token
      safety-level: readwrite-all
tokens:
  - name: my-token
    token: ${DT_API_TOKEN}
```

Commit the file to version control without secrets -- each developer or CI system provides values via environment variables.

A repository can also commit a `.dtctl-repo-scope.yaml` beside it, which links the repository to the services and workloads that run its code so that queries made from inside it are narrowed to them (see [REPO_SCOPE.md](REPO_SCOPE.md)). The two files are found differently: `.dtctl.yaml` is searched for in every parent up to `/`, while the scope file is read only from the root of the git repository around the working directory — the walk stops at the first `.git`.

### Config Search Order

1. `--config` flag (explicit path)
2. `DTCTL_CONFIG` environment variable (explicit path)
3. `.dtctl.yaml` in the current directory or any parent (walks up to root)
4. Global config (`$XDG_CONFIG_HOME/dtctl/config` — `~/.config/dtctl/config` on Linux, `~/Library/Application Support/dtctl/config` on macOS, `%LOCALAPPDATA%\dtctl\config` on Windows)

> **Security: local configs cannot run commands.** Because a `.dtctl.yaml` is
> auto-discovered by walking up from your current directory, it is treated as
> **untrusted** -- the same threat model as a checked-out repo, an unpacked
> tarball, or a shared work dir. dtctl therefore **ignores command aliases and
> apply hooks** found in an auto-discovered local `.dtctl.yaml`, printing a
> warning to stderr when it does. These code-execution keys are honored **only**
> from the global config (`$XDG_CONFIG_HOME/dtctl/config`, see
> [Config Search Order](#config-search-order) for the per-OS default) or a
> config you point at explicitly with `--config` or `DTCTL_CONFIG`. A local
> config may still define
> contexts, tokens, and other preferences. As an additional safeguard, an alias
> can never shadow a built-in command (e.g. `get`, `apply`, `version`)
> regardless of where it is defined.

#### Trusting a prepared workspace with `DTCTL_CONFIG`

When you control the config file -- for example an automation harness that
generates a clean working directory with its own `.dtctl.yaml`, skills, and
hooks -- export `DTCTL_CONFIG` so dtctl treats that file as an explicit, trusted
config, exactly like `--config`:

```bash
export DTCTL_CONFIG="$PWD/.dtctl.yaml"
```

This honors the file's aliases and apply hooks without touching the invocation
(an agent can keep running `dtctl apply` unchanged), and it **skips
auto-discovery entirely** -- a stray `.dtctl.yaml` elsewhere on disk can never
shadow the file you named. Because it points at one specific file rather than
blanket-trusting whatever is discovered, it does not reopen the
untrusted-working-directory threat model above. Point it only at a config you
trust, and prefer scoping it to the session (e.g. exported by the harness or a
per-repo setup script) rather than your global shell profile.

### View the resolved configuration

To display the effective configuration after all files and overrides are merged:

```bash
dtctl config view
```

## Safety Levels

Safety levels provide **client-side** protection against accidental destructive operations:

| Level | Description |
|-------|-------------|
| `readonly` | No modifications allowed |
| `readwrite-mine` | Modify your own resources only |
| `readwrite-all` | Modify all resources (default) |
| `dangerously-unrestricted` | All operations including bucket deletion |

```bash
dtctl config set-context prod \
  --environment "https://prod.apps.dynatrace.com" \
  --token-ref prod-token \
  --safety-level readonly
```

View context details including safety level:

```bash
dtctl config describe-context prod
```

Safety levels are client-side only. For actual security, configure your API tokens with minimum required scopes.

## Apply Hooks

Apply hooks run external commands around `dtctl apply`:

- **Pre-apply** (`pre-apply`): runs **before** the resource is sent to the API. Receives the processed JSON on stdin and can reject the apply (non-zero exit aborts).
- **Post-apply** (`post-apply`): runs **after** a successful apply. Receives the apply result as JSON on stdin. Useful for cleanup, notifications, or writing metadata to disk. A non-zero exit is reported as a warning -- the resource is already persisted.

Both hooks are invoked the same way: the command string is tokenized using POSIX-style shell quoting (so `"path with spaces"` and `'quoted args'` are honoured) and executed directly -- there is **no shell interpretation** of the command line itself. The resource type and source file are appended as the final two positional arguments. When the input is piped (`dtctl apply -f -`), the source file argument is `<stdin>`.

### Configuration

Hooks are configured globally in `preferences` or per-context:

```yaml
# global config, e.g. ~/.config/dtctl/config on Linux,
# ~/Library/Application Support/dtctl/config on macOS,
# or %LOCALAPPDATA%\dtctl\config on Windows
preferences:
  hooks:
    pre-apply:  "node /opt/dtctl-hooks/validate.js"
    post-apply: "bash /opt/dtctl-hooks/notify.sh"

contexts:
  - name: production
    context:
      environment: https://abc12345.apps.dynatrace.com
      token-ref: prod-token
      hooks:
        pre-apply: "opa eval --bundle /policies -i /dev/stdin"
  - name: dev
    context:
      environment: https://dev.apps.dynatrace.com
      token-ref: dev-token
      hooks:
        pre-apply:  "none"  # explicitly disable the global hook
        post-apply: "none"
```

Per-context hooks take precedence over global hooks. The special value `"none"` disables the global hook for a specific context.

> **Security: hooks are ignored in local configs.** Apply hooks are honored only
> from the global config (`$XDG_CONFIG_HOME/dtctl/config`, see
> [Config Search Order](#config-search-order)) or a config named explicitly
> with `--config` or `DTCTL_CONFIG`. Hooks defined in an auto-discovered local
> `.dtctl.yaml` (whether in
> `preferences` or a context) are **ignored**, since a local config from an
> untrusted working directory must not be able to run commands. dtctl prints a
> warning to stderr when it ignores them. (The hook values remain in the file
> untouched -- they are simply never executed -- so editing the config elsewhere
> never destroys them.) See [Config Search Order](#config-search-order).

### Hook Contract

| Aspect | Pre-apply | Post-apply |
|--------|-----------|------------|
| **Invocation** | direct exec of the tokenized command, with `<resource-type>` and `<source-file>` appended as args | same |
| **$1** | Resource type (e.g., `dashboard`, `workflow`, `slo`) | same |
| **$2** | Source filename from `-f` | same |
| **Stdin** | Processed resource JSON (YAML→JSON + template rendering applied) | Apply result JSON (array of per-resource result objects: `action`, `resourceType`, `id`, `name`, etc.) |
| **Stdout** | Always forwarded to the user's stdout | Forwarded to the user's stdout |
| **Stderr** | Always forwarded to the user's stderr | Always forwarded to the user's stderr |
| **Exit 0** | Proceed with apply | Reported as success |
| **Exit non-zero** | Abort apply, show stdout+stderr | Warning only -- apply already succeeded; stdout+stderr are shown and a warning line is printed |
| **Timeout** | 30 seconds | 30 seconds |
| **Dry-run** | Pre-apply runs (validates before the preview) | Post-apply is skipped |
| **Array apply (partial failure)** | Runs once on the full array | Runs once on the resources that *were* persisted, even when later items in the batch fail |

Because the command is tokenized but then executed directly (no `sh -c`), pipes, redirections, glob expansion, and environment-variable expansion in the command string itself are **not** supported -- put any shell logic inside the script the hook invokes.

> **Agent mode (`--agent` / `-A`).** When dtctl writes its JSON envelope to stdout, both pre-apply and post-apply hook output is redirected to stderr automatically so the envelope on stdout stays clean for machine consumers. Use stderr for any human-readable hook diagnostics.
>
> **Shell positional parameters in config values.** The config loader expands `$VAR` / `${VAR}` against the process environment but preserves shell positional parameters (`$1`, `$2`, `$@`, …) verbatim. You can write `pre-apply: "bash validate.sh \"$1\" \"$2\""` and have those tokens reach the hook unchanged.

### Writing Hooks

**Pre-apply** -- reject dashboards without a title:

```bash
#!/bin/bash
# validate.sh
resource_type="$1"

if [ "$resource_type" = "dashboard" ]; then
  title=$(cat | jq -r '.title // empty')
  if [ -z "$title" ]; then
    echo "Error: dashboard must have a title" >&2
    exit 1
  fi
fi
```

**Post-apply** -- delete the source file after a successful dashboard deploy, forcing the user to re-download before the next edit:

```bash
#!/bin/bash
# cleanup.sh
resource_type="$1"
source_file="$2"

if [ "$resource_type" = "dashboard" ] && [ -f "$source_file" ]; then
  rm -- "$source_file"
  id=$(cat | jq -r '.[0].id')
  echo "Deployed dashboard $id. Local file removed; re-download with 'dtctl get dashboard $id' before the next edit."
fi
```

### Usage

```bash
dtctl apply -f dashboard.yaml            # pre- and post-apply both run
dtctl apply -f dashboard.yaml --no-hooks # skip both hooks
dtctl apply -f dashboard.yaml --dry-run  # pre-apply runs, post-apply is skipped
dtctl apply -f dashboard.yaml -v         # verbose: logs hook command and duration
```

## Result Spill

`dtctl query` can spill a large result to a local file (see the `dtctl inspect`
command in [COMMANDS.md](COMMANDS.md)) and return a compact summary
instead of the rows. Defaults can be set globally or per-context under a
`spill:` section:

```yaml
# global config, e.g. ~/.config/dtctl/config on Linux,
# ~/Library/Application Support/dtctl/config on macOS,
# or %LOCALAPPDATA%\dtctl\config on Windows (or under a specific context)
spill:
  mode: auto            # auto | always | never  (overrides the agent/non-agent default)
  dir: ~/.cache/dtctl/results   # base directory for spilled files
  format: jsonl         # jsonl | json | csv | parquet
  threshold: 50KB       # size of the result as printed that triggers a spill
  ttl: 24h              # how long spilled files are kept before pruning
```

Environment overrides (handy for containers/CI):

```bash
export DTCTL_SPILL=never                 # auto | always | never — kill switch for disk writes
export DTCTL_SPILL_DIR=/mnt/scratch      # write spills to a mounted volume
```

Precedence (highest wins): **flag → environment → context config → global config →
built-in default**. A user-chosen `dir` (or `DTCTL_SPILL_DIR` / `--spill-to`) is
written outside the managed cache and opts out of its TTL pruning and per-context
partitioning -- you own that file's lifetime.

## Query Limits

Every query dtctl runs can carry per-query caps -- the same knobs `dtctl query`
exposes as flags. Setting them in the config makes them apply to *every* query
in a context, including generated ones, scripts, and agent-driven runs, which a
flag you have to remember to type does not.

```yaml
# global config, e.g. ~/.config/dtctl/config on Linux,
# ~/Library/Application Support/dtctl/config on macOS,
# or %LOCALAPPDATA%\dtctl\config on Windows
query-limits:                 # global defaults
  scan-limit-gbytes: 500      # cap the data scanned (PARTIAL result beyond it)
  max-result-records: 5000    # cap returned records (0 = server default, ~1000)
  max-result-bytes: 10485760  # cap result size in bytes
  sampling-ratio: 0           # DQL sampling on log/span fetches (0 = off)

contexts:
  - name: production
    context:
      environment: https://abc12345.apps.dynatrace.com
      token-ref: production
      query-limits:
        scan-limit-gbytes: 50   # tighter ceiling here; other limits inherited
  - name: sandbox
    context:
      environment: https://sandbox.apps.dynatrace.com
      token-ref: sandbox
      # no query-limits block: inherits the global defaults
```

Precedence (highest wins): **flag -> context config -> global config -> server
default**. The per-context block overrides the global one **per field**, so a
context can tighten one limit without restating the others.

A limit is only ever applied when it is set: a config without a `query-limits`
block behaves exactly as it did before the section existed, and an explicit
`--default-scan-limit-gbytes 0` still means "use the server default" rather than
inheriting the configured ceiling.

Because zero means "defer to the next layer" at every config layer, the merge
only ever tightens: a context can lower a global ceiling but cannot lift one,
and a negative value is rejected rather than silently ignored. Lifting a
configured limit is a command-line decision:

```bash
dtctl query 'fetch logs, from:now()-30d | summarize count()' --no-query-limits
```

`--no-query-limits` drops the config layers only -- limit flags you pass on the
same command line still apply, so `--no-query-limits --max-result-records 42`
means "ignore the config, use 42".

Two caveats worth knowing:

- **`scan-limit-gbytes` is a brake, not a filter.** Exceeding it returns a
  PARTIAL result rather than an error. dtctl says so on every affected query
  (and, with `-v`, names the configured limits it applied), but a script that
  ignores the warning will read a truncated answer as a complete one.
- **`sampling-ratio` makes results approximate** for every query in the context,
  not just the wide ones -- counts come back extrapolated. Prefer setting it per
  query unless approximate answers are what the whole context is for.

Limits apply to `dtctl query` and `dtctl wait query`. `dtctl inventory` keeps its
own discovery cap (`--scan-limit-gbytes`, default 25).

## Command Aliases

Create shortcuts for frequently used commands.

### Simple Aliases

```bash
dtctl alias set wf "get workflows"
dtctl wf
# Expands to: dtctl get workflows
```

### Parameterized Aliases

Use `$1`-`$9` for positional parameters:

```bash
dtctl alias set logs-errors "query 'fetch logs | filter status=\$1 | limit 100'"
dtctl logs-errors ERROR
# Expands to: dtctl query 'fetch logs | filter status=ERROR | limit 100'
```

### Shell Aliases

Prefix with `!` to execute through the system shell (enables pipes and external tools):

```bash
dtctl alias set wf-names "!dtctl get workflows -o json | jq -r '.workflows[].title'"
dtctl wf-names
```

### Import and Export

Share aliases with your team:

```bash
dtctl alias export -f team-aliases.yaml
dtctl alias import -f team-aliases.yaml
```

### Managing Aliases

```bash
dtctl alias list         # List all aliases
dtctl alias delete wf    # Delete an alias
```

### Alias Safety

Aliases cannot shadow built-in commands:

```bash
dtctl alias set get "query 'fetch logs'"
# Error: alias name "get" conflicts with built-in command
```

This guard is also enforced at **resolution** time: even an alias written
directly into a config file (bypassing `alias set`) can never override a
built-in -- dtctl ignores it and runs the real command, warning on stderr.

Aliases are honored only from the global config or a config named explicitly
with `--config` or `DTCTL_CONFIG`.
Aliases in an auto-discovered local `.dtctl.yaml` are **ignored** for security
(see [Config Search Order](#config-search-order)), so `alias export` / `import`
should target the global config, not a per-project file.

## Command Profiles

Command profiles restrict **which commands dtctl exposes** to a named subset. A
profile shapes every discovery surface at once -- `--help`, the
[`dtctl commands`](AGENT_MODE.md#command-catalog) catalog, and shell completion -- and hard-blocks
invocation of anything outside the set.

The motivating use case is embedding dtctl in AI agents. When only a slice of the
CLI is relevant -- say an investigation agent that needs `query` and Davis
analyzers but never `auth login` or the cloud-provisioning verbs -- a large,
mostly-irrelevant command menu confuses the agent. A profile trims the surface to
what matters, from configuration alone. No fork, one binary.

> **Profiles are a convenience, not a security boundary.** Like
> [safety levels](#profiles-vs-safety-levels), they are client-side. A determined
> caller can unset `DTCTL_PROFILE` or edit the config. For real restriction,
> scope the API token.

### Quick start

Bind a built-in profile to a context so an embedded agent inherits the reduced
surface with zero flags:

```bash
dtctl config set-context prod-agent \
  --environment https://abc12345.apps.dynatrace.com \
  --token-ref prod-token \
  --profile query \
  --safety-level readonly

# Now the agent only sees the query surface:
dtctl commands            # catalog reflects the profile
dtctl --help              # help reflects the profile
dtctl auth login          # blocked with a clear error
```

Or select a profile for a single controlled environment via the environment
variable, which takes precedence over any context binding:

```bash
DTCTL_PROFILE=query dtctl commands
```

### Built-in profiles

| Profile | Surface (plus always-available commands) |
|---------|------------------------------------------|
| `full` | Everything (the default; today's behavior) |
| `query` | `query`, `get analyzers`, `describe analyzer`, `exec analyzer`, `verify analyzer` |
| `investigate` | `query`, `logs`, `get`, `find`, `describe` |

Profile names are deliberately **topical**, never permission words like
`readonly` -- that axis belongs to safety levels (see below). If a profile should
also forbid writes, pair it with `--safety-level readonly` on the context.

### Defining your own

Profiles live in the config file under a top-level `profiles` map. A profile is a
`description` plus a flat `commands` **allowlist**:

```yaml
profiles:
  triage:
    description: Read-only incident triage for on-call agents
    commands:
      - query
      - logs
      - get slos      # only SLOs from the get verb, not all of `get`
      - describe

contexts:
  - name: oncall-agent
    context:
      environment: https://abc12345.apps.dynatrace.com
      token-ref: oncall-token
      profile: triage
      safety-level: readonly
```

Matching rules:

- Each `commands` entry is a **command-path prefix**: a verb (`query`), a
  resource (`get workflows`), or a full path. An entry matches a command when it
  equals or is a segment-prefix of that command's path, so listing a parent verb
  (`describe`) includes its whole subtree.
- **Default-deny**: anything not matched (and not always-available) is masked.
  There is no denylist -- you always list what is *allowed*. Adding a new command
  to dtctl never silently widens an existing profile.
- To allow a parent but only some children, list the specific child paths
  (`commands: [get analyzers, get slos]` rather than `commands: [get]`).

User-defined profiles take precedence over a built-in preset of the same name.

### Always-available commands

Only two commands are allowed regardless of profile -- the irreducible core that
lets an agent discover its surface and a user get help:

- `commands` (and `commands howto`) -- the machine-readable catalog agents
  bootstrap from
- `help`

Everything else is subject to the allowlist, **including `config`, `ctx`,
`completion`, and `version`**. This is deliberate: `config`/`ctx` can rotate
credentials and switch environments, so a locked-down agent profile must be able
to withhold them. If a profile needs any of these, list it explicitly (e.g.
`commands: [query, config]`).

### Selecting the active profile

Precedence, highest first:

```
DTCTL_PROFILE env  >  context-bound profile  >  none (= full)
```

- **`DTCTL_PROFILE`** -- for products that wrap the binary in a controlled
  environment.
- **Context binding** (`--profile` on `set-context`) -- the recommended path for
  embedding; agents inherit the surface with zero flags.
- **None** -- the full command tree (default, fully backward compatible).

There is no global default-profile setting: a config-wide profile is a footgun
(set once, forgotten, every invocation silently restricted). Activation is always
explicit per-environment or scoped to a context.

### What a blocked invocation looks like

```
$ DTCTL_PROFILE=query dtctl auth login
Error: command "auth login" is not available in profile "query"

  This profile exposes a reduced command set. Run 'dtctl commands' to see
  what is available, or unset DTCTL_PROFILE to use the full CLI.
```

In [agent mode](AGENT_MODE.md) the same block is reported as a structured error
with `code: "profile_blocked"`. The `dtctl commands` catalog also advertises the
active `profile` and `safety_level` so an agent can see both constraints at once.

Masks **compose**: a command has to survive every active mask to be invocable, and
the code you get back names the mask that stopped it. In server mode, for
example, the per-request profile and the service's own host-only-command mask
both apply, so `config` reports `unsupported_in_service` no matter which profile
is active.

### Profiles vs. safety levels

Profiles and [safety levels](#safety-levels) are **orthogonal axes**
that compose on a context:

| | Safety level | Profile |
|---|---|---|
| Question | "What may this command *do*?" | "Which commands *exist* here?" |
| Axis | Permission / blast radius | Topic / surface |
| Effect on help & catalog | None -- blocks at run time | Removes the command entirely |

> A profile decides which commands are on the menu; the safety level decides what
> those commands are allowed to do.

Set **both** on a context to express, e.g., "this agent only sees `query`/Davis
analyzers **and** can never mutate."

A third axis is configurable on a context too: the
**[stability floor](STABILITY.md#choosing-what-this-environment-accepts)** asks
"which commands *carry a contract* here?" and removes anything whose invocation
and output dtctl does not promise to keep. It answers a different question again
-- not permission, not topic, but *how durable* the surface is -- so automation
that must keep working across releases pins it even when the profile and the
safety level are already set:

```bash
dtctl config set-context prod-agent --min-stability stable
```

Three further axes exist when dtctl is not a local terminal process -- they
belong to the *environment* it runs in, not to your configuration, and they
matter because an agent has to tell every block apart:

| Axis | Question | Chosen by | Agent code (see [AGENT_MODE.md](AGENT_MODE.md#error-responses)) |
|---|---|---|---|
| Safety level | "What may this command *do*?" | context, `--safety-level`, or the request | `safety_blocked` |
| Profile | "Which commands *exist* for this caller?" | `DTCTL_PROFILE`, context, or the request | `profile_blocked` |
| Stability floor | "Which commands *carry a contract* here?" | `DTCTL_MIN_STABILITY`, context, or the request | `stability_blocked` |
| Deprecated surface | "Would this still work after the next removal?" | `DTCTL_NO_DEPRECATED`, context, or the request | `deprecated_surface` |
| Environment | "Which commands *make sense* here at all?" | the embedding environment -- server mode removes host-only commands (`config`, `ctx`, `auth`, …) | `unsupported_in_service` |
| Capability | "Which *host abilities* may this process use?" | the embedding host -- plugins, aliases, hooks, editors, and browser opens are all off for embedded callers | `capability_disabled` |

The environment and capability axes are not configurable from a context: a service
sets them once for its whole process, and they compose with whatever profile,
floor and safety level a request carries. See
[Stability](STABILITY.md) for what each tier promises, how to admit a single
below-floor entry without lowering the floor, and what `DTCTL_NO_DEPRECATED`
does.
