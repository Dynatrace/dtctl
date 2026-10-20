# dtctl Troubleshooting

## Installation

Install from https://github.com/dynatrace-oss/dtctl. Verify with `dtctl version`.

```bash
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/arm64/arm64/')
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
TAG=$(curl -s -I -L https://github.com/dynatrace-oss/dtctl/releases/latest | tr -d '\r' | awk -F/ '/^location: /{print $NF}' | tail -n1)
TARBALL="dtctl_${TAG#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/dynatrace-oss/dtctl/releases/download/${TAG}/${TARBALL}"
mkdir -p /tmp/dtctl && cd /tmp/dtctl
curl -L "$URL" -o "$TARBALL"
tar -xzf "$TARBALL"
sudo mv dtctl /usr/local/bin/
dtctl version
```

If no sudo: place in `~/bin/` and ensure it's on PATH.

## Initial Setup

```bash
# Store credentials (use --token flag directly, NOT stdin piping)
dtctl config set-credentials "my-token" --token "$DYNATRACE_API_TOKEN"
dtctl config set-context "default" \
  --environment "$DYNATRACE_BASE_URL" \
  --token-ref "my-token"
dtctl config use-context "default"

# Verify the context loaded (local; works with any token type)
dtctl auth status --plain
```

**Note:** Always use `--token "$TOKEN"` directly. Stdin piping does not work reliably and stores corrupted values in the keychain.

## Teardown

To remove a context and the credential it created, in one step:

```bash
dtctl config delete-context "<name>" --delete-credentials
```

Separately, when the credential is shared between contexts or the context is
already gone:

```bash
dtctl config delete-credentials "<token-ref>"
```

To confirm the credential is gone, use `dtctl auth status` — it reports whether
a token is present without printing it:

```bash
dtctl auth status --plain --context "<name>"   # errors once the context is deleted
```

Never verify a deletion by reading secrets back: the OS keychain read verbs
print token material into the transcript, which is the opposite of what a
cleanup step is for. `dtctl auth status` and the delete command's own success
output are the only confirmation needed — do not go to the credential store
directly.

## Common Issues

### PARSE_ERROR_SINGLE_QUOTES / query filter returns nothing
DQL string literals require **double** quotes. Two frequent traps:

- `filter status == 'ERROR'` → `PARSE_ERROR_SINGLE_QUOTES` (single quotes are invalid).
- `filter status == ERROR` (unquoted) → silently returns **zero rows**; the
  bareword is read as a field reference, not the string `"ERROR"`.

Use double quotes for the value and pick a shell wrapper that preserves them:

```bash
# bash/zsh: single-quote the whole query
dtctl query 'fetch logs | filter status == "ERROR"'
```
```cmd
:: cmd.exe: escape the inner double quotes
dtctl query "fetch logs | filter status == \"ERROR\""
```
```powershell
# PowerShell: pipe a here-string to stdin. Do NOT pass it as an argument
# (`dtctl query @'...'@`) — Windows PowerShell 5.1 strips the inner quotes,
# which produces the silent zero-row case above. Never `-f - @'...'@`: that
# waits on stdin and looks like a hang.
@'
fetch logs | filter status == "ERROR"
'@ | dtctl query -o json
```

Quote-free everywhere: put the DQL in a file and run `dtctl query -f query.dql`.

If a Windows user reports empty results, have them re-run with `-vv` and compare
the `BODY:` query against what they typed — missing quotes confirm the shell,
not the query, is at fault.

### 401/403 Authentication Errors
```bash
# Re-store credentials
dtctl config set-credentials "my-token" --token "$TOKEN"

# Check auth context (token type, not identity)
dtctl auth status --plain

# Check permissions
dtctl auth can-i <verb> <resource>
```

> **Note:** `dtctl auth whoami` is not a connectivity check. It hits the platform
> metadata API and needs an OAuth/JWT token with `app-engine:apps:run`; a plain API
> token or read-scoped platform token returns a spurious 403 here even though read
> access works. Confirm connectivity with a real `dtctl get`/`dtctl query`.

### Wrong Tenant
```bash
dtctl config get-contexts --plain
dtctl config use-context <name>
```

### Safety Level Blocks
Safety levels are client-side protections: `readonly`, `readwrite-mine`, `readwrite-all`, `dangerously-unrestricted`. API token scopes determine actual permissions.

### dtctl Not Found
Ensure binary is on PATH. Check `~/bin/dtctl` or `/usr/local/bin/dtctl`.

### Corrupted Credential Entry
Re-store the credential; `set-credentials` overwrites the existing entry and
clears any cached OAuth tokens derived from it.

```bash
dtctl config set-credentials "<token-ref>" --token "$TOKEN"
```

If it still fails, remove the credential outright and store it again:

```bash
dtctl config delete-credentials "<token-ref>"
dtctl config set-credentials "<token-ref>" --token "$TOKEN"
```

Repair the entry with dtctl, never with OS keychain tooling — see
[Teardown](#teardown) above for why.

## Debugging

```bash
# Verbose output
dtctl <command> -v     # Details
dtctl <command> -vv    # Full HTTP debug (auth headers redacted)
```

## Platform Notes
- **macOS**: Keychain must be unlocked
- **Linux**: Requires gnome-keyring or similar
- **Windows**: Uses Credential Manager
