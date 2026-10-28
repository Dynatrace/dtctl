# Installing dtctl

This guide covers installing dtctl on your system.

## Homebrew (Recommended — macOS and Linux)

```bash
brew install dynatrace-oss/tap/dtctl
```

This installs the binary and sets up shell completions (bash, zsh, fish) automatically.

To upgrade:

```bash
brew upgrade dtctl
```

To uninstall:

```bash
brew uninstall dtctl
brew untap dynatrace-oss/tap  # optional
```

## Shell Script (macOS and Linux)

If you don't use Homebrew, install with a single command:

```bash
curl -fsSL https://raw.githubusercontent.com/Dynatrace/dtctl/main/install.sh | sh
```

This downloads the latest release, extracts it to `~/.local/bin` (Linux) or `/usr/local/bin` (macOS), and verifies the installation.

Override the install directory with `DTCTL_INSTALL_DIR`:

```bash
curl -fsSL https://raw.githubusercontent.com/Dynatrace/dtctl/main/install.sh | DTCTL_INSTALL_DIR=~/bin sh
```

To update, re-run the same command.

## Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/Dynatrace/dtctl/main/install.ps1 | iex
```

This downloads the latest release, extracts it to `%LOCALAPPDATA%\dtctl`, and adds it to your PATH. Restart your terminal (or IDE) afterwards for the PATH change to take effect.

For detailed steps, manual install, PowerShell tips, quoting, and troubleshooting, see the dedicated [Windows installation guide](WINDOWS.md).

## Binary Download

**For most users**, download the pre-built binary for your platform:

1. **Download the latest release**: Visit the [releases page](https://github.com/Dynatrace/dtctl/releases/latest) and download the appropriate binary for your operating system and architecture.

2. **Extract and install**:

   **Linux/macOS:**
   ```bash
   # Extract the archive
   tar -xzf dtctl_*.tar.gz
   
   # Make it executable
   chmod +x dtctl
   
   # macOS only: Remove quarantine attribute (see Troubleshooting section for details)
   sudo xattr -r -d com.apple.quarantine dtctl
   
   # Move to a directory in your PATH
   sudo mv dtctl /usr/local/bin/
   
   # Verify installation
   dtctl version
   ```

3. **Next Steps**: See the [Quick Start Guide](QUICK_START.md) to configure your environment.

## Building from Source (Advanced)

**For developers** who want to build from source or contribute to the project.

### Prerequisites

- Go 1.24 or later
- Git
- Make

### Clone and Build

```bash
# Clone the repository
git clone https://github.com/Dynatrace/dtctl.git
cd dtctl

# Build the binary
make build

# Verify the build
./bin/dtctl version
```

Expected output:
```
dtctl version dev
commit: unknown
built: unknown
```

### Test the Binary

Try a few commands to ensure everything works:

```bash
# Show help
./bin/dtctl --help

# View available commands
./bin/dtctl get --help
./bin/dtctl query --help

# Run health check (after configuration)
./bin/dtctl doctor
```

### Installation Options (Source Builds)

### Option 1: Use from bin/ Directory

The simplest approach - use `./bin/dtctl` directly:

```bash
# From the project directory
./bin/dtctl config set-context my-env \
  --environment "https://YOUR_ENV.apps.dynatrace.com" \
  --token-ref my-token
```

### Option 2: Install to GOPATH

Install to your Go binary directory:

```bash
make install

# Verify
dtctl version
```

This installs to `$GOPATH/bin/dtctl` (typically `~/go/bin/dtctl`). Ensure `$GOPATH/bin` is in your `$PATH`.

```bash
# Add Go bin to PATH (zsh/bash)
export PATH="$PATH:$(go env GOPATH)/bin"
```

### Option 3: Copy to System PATH

Install system-wide:

```bash
# Linux/macOS
sudo cp bin/dtctl /usr/local/bin/

# Verify
dtctl version
```

### Option 4: Add to PATH

Add the bin directory to your PATH:

```bash
# Add to ~/.bashrc, ~/.zshrc, or ~/.profile
export PATH="$PATH:/path/to/dtctl/bin"

# Reload your shell
source ~/.bashrc  # or ~/.zshrc
```

## Shell Completion (Optional)

Enable tab completion for faster workflows.

### Bash

```bash
# Generate completion script
dtctl completion bash > /tmp/dtctl-completion.bash

# Test it
source /tmp/dtctl-completion.bash

# Make it permanent
sudo mkdir -p /etc/bash_completion.d
sudo cp /tmp/dtctl-completion.bash /etc/bash_completion.d/dtctl

# Reload your shell
source ~/.bashrc
```

### Zsh

```bash
# Create completions directory
mkdir -p ~/.zsh/completions

# Generate completion script
dtctl completion zsh > ~/.zsh/completions/_dtctl

# Add to your ~/.zshrc (if not already present)
echo 'fpath=(~/.zsh/completions $fpath)' >> ~/.zshrc
echo 'autoload -U compinit && compinit' >> ~/.zshrc

# Clear completion cache and reload
rm -f ~/.zcompdump*
source ~/.zshrc
```

**For oh-my-zsh users**: Place the completion file in `~/.oh-my-zsh/completions/_dtctl`:

```bash
mkdir -p ~/.oh-my-zsh/completions
dtctl completion zsh > ~/.oh-my-zsh/completions/_dtctl
rm -f ~/.zcompdump*
source ~/.zshrc
```

### Fish

```bash
# Create completions directory (if needed)
mkdir -p ~/.config/fish/completions

# Generate completion script
dtctl completion fish > ~/.config/fish/completions/dtctl.fish

# Reload shell
source ~/.config/fish/config.fish
```

### PowerShell

```powershell
# Temporary (current session)
dtctl completion powershell | Out-String | Invoke-Expression

# Permanent - add to your PowerShell profile
# First, find your profile location:
echo $PROFILE

# Then add this line to your profile:
dtctl completion powershell | Out-String | Invoke-Expression
```

## Verify Installation

After installation, verify everything works:

```bash
# Check version
dtctl version

# View help
dtctl --help

# Test tab completion (if enabled)
dtctl get <TAB><TAB>
```

## Updating dtctl

### If Installed via Homebrew

```bash
brew update
brew upgrade dtctl
```

### If Installed from Release

Download and install the latest release following the [Binary Download](#binary-download) steps above.

### If Built from Source

To update to the latest version:

```bash
# Navigate to the repository
cd /path/to/dtctl

# Pull latest changes
git pull

# Rebuild
make build

# Reinstall (if using Option 2 or 3)
make install
# or
sudo cp bin/dtctl /usr/local/bin/
```

## Uninstalling

To remove dtctl:

### Windows (PowerShell)

```powershell
# Remove binary + PATH entry
irm https://raw.githubusercontent.com/Dynatrace/dtctl/main/uninstall.ps1 | iex

# Optional: full cleanup (config/cache/data)
irm https://raw.githubusercontent.com/Dynatrace/dtctl/main/uninstall.ps1 -OutFile uninstall-dtctl.ps1
.\uninstall-dtctl.ps1 -RemoveConfig -RemoveCache -RemoveData
```

### macOS/Linux

```bash
# If installed via Homebrew
brew uninstall dtctl

# If installed via Option 2 (make install)
rm $GOPATH/bin/dtctl

# If installed via Option 3 (system-wide)
sudo rm /usr/local/bin/dtctl

# Remove configuration (optional)
rm -rf ~/.config/dtctl    # Linux
# or
rm -rf ~/Library/Application\ Support/dtctl    # macOS
```

## Next Steps

Now that dtctl is installed, see the [Quick Start Guide](QUICK_START.md) to learn how to:
- Configure your Dynatrace environment
- Execute commands
- Work with workflows, dashboards, DQL queries, and more

## Troubleshooting

### "command not found: dtctl"

The binary is not in your PATH. Either:
1. Use the full path: `./bin/dtctl` or `/path/to/dtctl/bin/dtctl`
2. Add the bin directory to your PATH (see Option 4 above)
3. Install to a directory already in PATH (see Options 2 or 3 above)

Check your PATH:
```bash
echo $PATH
```

### "permission denied"

Make the binary executable:
```bash
chmod +x bin/dtctl
```

### macOS: "Apple could not verify dtctl is free of malware"

When downloading pre-built binaries on macOS, you may see this security warning:

```
"dtctl" cannot be opened because Apple cannot verify that it is free of malware.
```

This is expected behavior for unsigned binaries. The dtctl releases are not code-signed with an Apple Developer ID certificate, which is common for open-source CLI tools.

**Option 1: Remove quarantine attribute (Recommended)**

```bash
# Remove the quarantine flag
sudo xattr -r -d com.apple.quarantine dtctl

# Then make it executable
chmod +x dtctl
```

**Option 2: Allow via System Settings**

1. Try to run `./dtctl`
2. When the warning appears, click "Cancel"
3. Open **System Settings > Privacy & Security**
4. Scroll to the Security section at the bottom
5. Click **"Allow Anyway"** next to the dtctl message
6. Try running `./dtctl` again and click **"Open"** when prompted

**Why does this happen?**

macOS Gatekeeper adds a `com.apple.quarantine` extended attribute to files downloaded from the internet. When you try to execute them, macOS checks for:
- Code signing by a registered Apple Developer ID
- Notarization by Apple (malware scanning)

Since dtctl is an open-source project and not signed/notarized, macOS blocks it by default. The workarounds above tell macOS you trust this binary.

**Is this safe?**

Yes, if you downloaded dtctl from the official [GitHub releases page](https://github.com/Dynatrace/dtctl/releases). Always verify:
- You're downloading from `github.com/Dynatrace/dtctl`
- The checksum matches (see `checksums.txt` in the release)

**Note**: If you build from source locally, this issue doesn't occur since the binary isn't quarantined.

### Build fails

Ensure you have the required prerequisites:
```bash
# Check Go version (needs 1.24+)
go version

# Check Make
make --version

# Try cleaning and rebuilding
make clean
make build
```

### Shell completion not working

After setting up completion:
1. Ensure you reloaded your shell or sourced the config file
2. Clear completion cache (Zsh: `rm -f ~/.zcompdump*`)
3. Verify the completion file exists in the correct location
4. Check file permissions: `ls -la ~/.zsh/completions/_dtctl`

## Getting Help

- **Quick Start**: See [QUICK_START.md](QUICK_START.md) for usage examples
- **API Reference**: See [dev/API_DESIGN.md](dev/API_DESIGN.md) for complete command reference
- **Architecture**: Read [dev/ARCHITECTURE.md](dev/ARCHITECTURE.md) for implementation details
- **Issues**: Report bugs at [GitHub Issues](https://github.com/dynatrace/dtctl/issues)

### macOS: "zsh: exec format error"

If you built the binary inside a Linux-based devcontainer (for example on an ARM container) and then try to run `bin/dtctl` natively on macOS, you may see:

```
zsh: exec format error: bin/dtctl
```

This happens because the compiled binary's OS/architecture don't match your host. To fix it, rebuild the binary for macOS on your host or produce a cross-compiled macOS binary.

Rebuild locally on macOS (recommended):

```bash
# From the project root
make clean
# Build for the host (native macOS build)
make build-host
# Or explicitly build for darwin/arm64
make build-darwin-arm64

# Run the built binary
./bin/dtctl-host version    # from `make build-host`
# or
./bin/dtctl-darwin-arm64 version
```

Cross-build from Linux (requires Go on the build machine):

```bash
# Create a darwin/arm64 binary from Linux
env GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o bin/dtctl-darwin-arm64 .
```

Notes:
- If you built the binary inside a container using a different OS (Linux) and then copied it to macOS, the binary won't run on macOS. Always build for the target OS/arch.
- For Apple Silicon (arm64) Macs, target `darwin/arm64`. For older Intel Macs target `darwin/amd64`.
- If you need universal binaries or native macOS toolchain features, build on macOS directly.

