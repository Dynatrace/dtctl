# Installing dtctl on Windows

This guide covers the Windows-specific details of running dtctl: PowerShell install and tips, shell completion, `cmd.exe`/WSL, and troubleshooting.

> For the general, cross-platform installation reference (all platforms, binary downloads, updating, uninstalling), see **[INSTALLATION.md](INSTALLATION.md)**.

## Quick Install (PowerShell)

Open PowerShell and run:

```powershell
irm https://raw.githubusercontent.com/Dynatrace/dtctl/main/install.ps1 | iex
```

This downloads the latest release, extracts it to `%LOCALAPPDATA%\dtctl`, and adds it to your PATH. Restart your terminal afterwards.

## Step-by-Step Install

If you prefer to install manually:

### 1. Download

Go to the [releases page](https://github.com/Dynatrace/dtctl/releases/latest) and download the Windows zip file for your architecture:

| Architecture    | File                              |
| --------------- | --------------------------------- |
| 64-bit (x86_64) | `dtctl_VERSION_windows_amd64.zip` |
| ARM64           | `dtctl_VERSION_windows_arm64.zip` |

Most Windows PCs use the **amd64** variant. ARM64 is for devices like the Surface Pro X or Copilot+ PCs with Snapdragon processors.

### 2. Extract

Right-click the downloaded zip file and select **Extract All**, or use PowerShell:

```powershell
Expand-Archive dtctl_*_windows_*.zip -DestinationPath "$env:LOCALAPPDATA\dtctl"
```

This places `dtctl.exe` in `%LOCALAPPDATA%\dtctl` (typically `C:\Users\<you>\AppData\Local\dtctl`).

### 3. Add to PATH

Add the directory to your user PATH so you can run `dtctl` from any terminal:

**Option A -- PowerShell (recommended):**

```powershell
$binPath = "$env:LOCALAPPDATA\dtctl"
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$binPath*") {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$binPath", 'User')
}
```

**Option B -- System Settings UI:**

1. Press `Win + R`, type `sysdm.cpl`, and press Enter
2. Go to the **Advanced** tab and click **Environment Variables**
3. Under **User variables**, select `Path` and click **Edit**
4. Click **New** and add: `%LOCALAPPDATA%\dtctl`
5. Click **OK** to save

After changing PATH, restart your terminal (or run `refreshenv` if you have Chocolatey installed).

### 4. Verify

```powershell
dtctl version
```

Expected output:

```
dtctl version 0.x.x
commit: abc1234
built: 2025-01-01T00:00:00Z
```

## Verify Download Integrity

Each release includes a `checksums.txt` file signed with [cosign](https://github.com/sigstore/cosign). To verify your download:

```powershell
# Download checksums
Invoke-WebRequest "https://github.com/Dynatrace/dtctl/releases/latest/download/checksums.txt" -OutFile checksums.txt

# Compute the SHA256 of your zip file
$hash = (Get-FileHash dtctl_*_windows_*.zip -Algorithm SHA256).Hash.ToLower()

# Check it matches
Select-String $hash checksums.txt
```

If the `Select-String` command prints a matching line, the download is intact.

## Building from Source

See **[INSTALLATION.md](INSTALLATION.md)** for build-from-source instructions (requires Go and Git). On Windows the build output is `bin\dtctl.exe`:

```powershell
go build -o bin\dtctl.exe .
.\bin\dtctl.exe version
```

## Shell Completion

### PowerShell

Tab completion makes dtctl much faster to use. Add this line to your PowerShell profile so it loads automatically:

```powershell
# Find your profile path
echo $PROFILE

# Edit it (creates the file if it doesn't exist)
if (!(Test-Path -Path $PROFILE)) { New-Item -ItemType File -Path $PROFILE -Force }
notepad $PROFILE
```

Add this line to the profile file:

```powershell
dtctl completion powershell | Out-String | Invoke-Expression
```

Save, close, and reopen PowerShell. You can now use `Tab` to complete commands:

```powershell
dtctl get <Tab>        # cycles through resource types
dtctl get workflows -o <Tab>   # cycles through output formats
```

### Git Bash / MSYS2

If you use Git Bash, you can set up bash completions:

```bash
# Generate completion script
dtctl completion bash > ~/.dtctl-completion.bash

# Add to your ~/.bashrc
echo 'source ~/.dtctl-completion.bash' >> ~/.bashrc
source ~/.bashrc
```

## Configuration

dtctl stores configuration and credentials under `%LOCALAPPDATA%\dtctl`:

| Item        | Path                          |
| ----------- | ----------------------------- |
| Config file | `%LOCALAPPDATA%\dtctl\config` |
| Cached data | `%LOCALAPPDATA%\dtctl\cache`  |
| Credentials | Windows Credential Manager    |

Credentials are stored securely in **Windows Credential Manager** (viewable via Control Panel > Credential Manager > Windows Credentials).

### Set Up Your First Environment

For the full configuration reference (contexts, credentials, safety levels, aliases, profiles), see **[CONFIGURATION.md](CONFIGURATION.md)**. The example below shows the Windows/PowerShell specifics.

```powershell
# OAuth login (recommended -- opens browser)
dtctl auth login --context my-env --environment "https://abc12345.apps.dynatrace.com"

# Or use a token
dtctl config set-context my-env `
  --environment "https://abc12345.apps.dynatrace.com" `
  --token-ref my-token

dtctl config set-credentials my-token `
  --token "dt0s16.XXXXXXXX.YYYYYYYY"

# Verify connectivity
dtctl doctor
```

Note: In PowerShell, use the backtick (`` ` ``) for line continuation instead of backslash (`\`).

## PowerShell Tips

### Quoting

DQL string literals **must** use double quotes (`"ERROR"`); single quotes are a syntax error. Getting those double quotes through PowerShell intact is the single most common Windows problem with dtctl, so it is worth understanding the mechanism.

#### The symptom: zero records, no error

```powershell
PS> dtctl query 'fetch logs | filter loglevel == "INFO" | limit 10' -o json
{
  "records": []
}
```

The same query returns rows in a Dynatrace notebook. Nothing is wrong with your query or your credentials — the double quotes never reached dtctl. Confirm it with `-vv`, which prints the request body dtctl actually sent:

```powershell
PS> dtctl query 'fetch logs | filter loglevel == "INFO" | limit 10' -vv
...
BODY:
{"query":"fetch logs | filter loglevel == INFO | limit 10", ...}
```

`loglevel == INFO` is *valid* DQL — it compares the `loglevel` field to a field named `INFO`. Both are unequal (or absent), so Grail returns an empty result and no error. That is why this fails silently instead of complaining.

dtctl detects this case by inspecting the raw command line and prints a warning before running the query, so you should not have to reach for `-vv`:

```
Warning: your shell stripped the double quotes from this query.
dtctl received: fetch logs | filter loglevel == INFO | limit 10
```

The query still runs — dtctl cannot know for certain that you meant a string literal — but the warning tells you why the result is empty.

#### Why it happens

**Windows PowerShell 5.1** (the blue `powershell.exe`, still the default on most Windows machines) re-quotes every argument before handing it to a native `.exe`, and it does **not** escape double quotes that are inside the argument. dtctl receives `"fetch logs | filter loglevel == "INFO" | limit 10"`, and the Windows command-line parser reads the inner quotes as end/start of quoting — so they vanish.

This affects **every** way of writing the query as an argument, including single-quoted strings and here-strings. A here-string is a plain string *value*, not a redirection like a bash heredoc, so `dtctl query @'...'@` still goes through argument passing and still loses the quotes.

PowerShell 7.3 and later fixed this: [`$PSNativeCommandArgumentPassing`](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_preference_variables#psnativecommandargumentpassing) defaults to `Windows` mode, which escapes arguments correctly for normal executables. Check which one you are on:

```powershell
$PSVersionTable.PSVersion
```

If that prints `5.1.x`, use one of the forms below. If it prints `7.3` or later, quoting an argument works as it does on Linux and macOS.

#### Fix 1: pipe the query in (works on every PowerShell version)

The pipeline sends bytes to stdin and never touches argument parsing, so quotes always survive. This is the recommended form:

```powershell
@'
fetch logs
| filter loglevel == "INFO"
| limit 10
'@ | dtctl query -o json
```

> Do **not** write `dtctl query -f - @'...'@`. `-f -` tells dtctl to read stdin while the here-string is passed as an argument, so the two disagree about where the query is. dtctl rejects that combination with a hint (older versions waited on the idle terminal and appeared to hang). Pipe it (`'@ | dtctl query`) instead, with or without `-f -`.

#### Fix 2: use a query file

```powershell
@'
fetch logs
| filter loglevel == "INFO"
| limit 10
'@ | Out-File -Encoding UTF8 query.dql

dtctl query -f query.dql
Get-Content query.dql | dtctl query   # equivalent
```

#### Fix 3: inline on one line (5.1 only)

If you must keep everything on one line in Windows PowerShell 5.1, escape the inner quotes with a backslash so the Windows parser hands them to dtctl:

```powershell
dtctl query 'fetch logs | filter loglevel == \"INFO\" | limit 10'
```

This is 5.1-specific: on PowerShell 7.3+ the backslashes are passed through literally and DQL rejects them. Prefer fix 1 or 2 in anything shared or scripted.

#### Summary

| Form                                       | PowerShell 5.1 | PowerShell 7.3+ |
| ------------------------------------------ | -------------- | --------------- |
| `@'...'@ \| dtctl query`                   | ✅ works       | ✅ works        |
| `dtctl query -f query.dql`                 | ✅ works       | ✅ works        |
| `dtctl query 'fetch ... "INFO"'`           | ❌ quotes lost | ✅ works        |
| `dtctl query @'...'@`                      | ❌ quotes lost | ✅ works        |
| `dtctl query -f - @'...'@`                 | ❌ rejected    | ❌ rejected     |
| `dtctl query 'fetch ... \"INFO\"'`         | ✅ works       | ❌ literal `\`  |

When results look suspicious, `-vv` shows the exact query dtctl sent — always check there first.

See [DQL Queries](resources/dql-queries.md#windows-powershell-quoting) for more examples.

### Line Continuation

PowerShell uses the backtick (`` ` ``) for line continuation, not backslash:

```powershell
# Correct (PowerShell)
dtctl config set-context my-env `
  --environment "https://abc12345.apps.dynatrace.com" `
  --token-ref my-token

# Wrong (bash syntax -- won't work in PowerShell)
dtctl config set-context my-env \
  --environment "https://abc12345.apps.dynatrace.com" \
  --token-ref my-token
```

### JSON Output and Piping

PowerShell works well with JSON output for scripting:

```powershell
# Parse JSON output
$workflows = dtctl get workflows -o json | ConvertFrom-Json

# Filter in PowerShell
$workflows | Where-Object { $_.title -like "*daily*" }

# Count resources
(dtctl get slos -o json | ConvertFrom-Json).Count
```

### Environment Variables

The environment URL lives in a context, not in an environment variable. Select a
context (and point at a config file) like this:

```powershell
# Temporary (current session)
$env:DTCTL_CONTEXT = "my-env"

# Persistent (current user)
[Environment]::SetEnvironmentVariable('DTCTL_CONTEXT', 'my-env', 'User')
```

Other variables dtctl reads: `DTCTL_CONFIG` (explicit config path), `DTCTL_TOKEN`
(token, supplied out of band), `DTCTL_OUTPUT`, `DTCTL_PROFILE`, and the spill
settings. See **[CONFIGURATION.md](CONFIGURATION.md)** for the full list.

## Windows Terminal and cmd.exe

### Windows Terminal

dtctl works in all Windows Terminal profiles (PowerShell, cmd.exe, Git Bash, WSL). For the best experience, use **Windows Terminal** with PowerShell and enable completions as described above.

### cmd.exe

dtctl runs in cmd.exe, but shell completion is not available. Use PowerShell or Git Bash for the full experience.

```cmd
rem Verify installation
dtctl version

rem Run a query (use double quotes only)
dtctl query "fetch logs | limit 5"
```

### WSL (Windows Subsystem for Linux)

If you use WSL, follow the [Linux installation instructions](INSTALLATION.md#homebrew-recommended--macos-and-linux) instead -- install the Linux binary inside your WSL distribution, not the Windows binary.

## Updating

Re-run the install script to update to the latest version:

```powershell
irm https://raw.githubusercontent.com/Dynatrace/dtctl/main/install.ps1 | iex
```

Or download the latest zip manually and extract to `%LOCALAPPDATA%\dtctl`.

## Uninstalling

```powershell
# Remove the binary
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\dtctl"

# Remove from PATH
$binPath = "$env:LOCALAPPDATA\dtctl"
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$newPath = ($userPath -split ';' | Where-Object { $_ -ne $binPath }) -join ';'
[Environment]::SetEnvironmentVariable('Path', $newPath, 'User')

# Remove configuration (optional -- also removes the binary if still present)
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\dtctl"

# Remove stored credentials (optional)
# Open Control Panel > Credential Manager > Windows Credentials
# Delete entries starting with "dtctl"
```

## Troubleshooting

### "dtctl is not recognized as an internal or external command"

The binary is not in your PATH. Either:

1. Use the full path: `& "$env:LOCALAPPDATA\dtctl\dtctl.exe"`
2. Add the bin directory to your PATH (see step 3 above)
3. Restart your terminal after modifying PATH

Check your PATH:

```powershell
$env:Path -split ';' | Select-String dtctl
```

### "Access is denied"

If extracting to `Program Files` or another protected location, run PowerShell as Administrator. Or install to `%LOCALAPPDATA%\dtctl\bin` (user-writable, no admin needed).

### Antivirus blocks dtctl.exe

Some antivirus software may flag unsigned binaries. You can:

1. Verify the checksum matches the official release (see [Verify Download Integrity](#verify-download-integrity))
2. Add an exclusion for `%LOCALAPPDATA%\dtctl\dtctl.exe` in your antivirus settings

### PowerShell execution policy blocks profile loading

If PowerShell completions don't load, your execution policy may block profile scripts:

```powershell
# Check current policy
Get-ExecutionPolicy

# Allow local scripts (recommended)
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
```

### WSL vs native Windows

If you have both WSL and native Windows installs, make sure you're running the right binary:

```powershell
# Check which dtctl you're running
Get-Command dtctl | Select-Object Source
```

Inside WSL, use `which dtctl` instead.

## Next Steps

- [Quick Start Guide](QUICK_START.md) -- configure your environment and run your first commands
- [Installation](INSTALLATION.md) -- cross-platform installation reference
- [Token Scopes](TOKEN_SCOPES.md) -- required API permissions
