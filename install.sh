#!/bin/sh
# Install dtctl (Dynatrace CLI) on Linux and macOS.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Dynatrace/dtctl/main/install.sh | sh
#
# Environment variables:
#   DTCTL_INSTALL_DIR  Override install directory (default: ~/.local/bin on Linux, /usr/local/bin on macOS)
#   GITHUB_TOKEN       GitHub personal access token to avoid API rate-limiting (optional)

set -e

REPO_OWNER="dynatrace-oss"
REPO_NAME="dtctl"

# Detect OS
OS="$(uname -s)"
case "$OS" in
    Linux)  os="linux" ;;
    Darwin) os="darwin" ;;
    *)      echo "Error: unsupported OS: $OS"; exit 1 ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)   arch="amd64" ;;
    aarch64|arm64)   arch="arm64" ;;
    *)               echo "Error: unsupported architecture: $ARCH"; exit 1 ;;
esac

# Default install directory
if [ -n "$DTCTL_INSTALL_DIR" ]; then
    install_dir="$DTCTL_INSTALL_DIR"
elif [ "$os" = "darwin" ]; then
    install_dir="/usr/local/bin"
else
    install_dir="$HOME/.local/bin"
fi

# Fetch latest release tag
echo "Fetching latest release..."
if [ -n "$GITHUB_TOKEN" ]; then
    api_response=$(curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" "https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest")
else
    api_response=$(curl -fsSL "https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest")
fi
tag=$(printf '%s' "$api_response" | grep '"tag_name"' | sed 's/.*"tag_name": *"//;s/".*//')
if [ -z "$tag" ]; then
    echo "Error: could not determine latest release."
    exit 1
fi
version="${tag#v}"
echo "Latest release: $tag"

# Download
asset="dtctl_${version}_${os}_${arch}.tar.gz"
url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${tag}/${asset}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

echo "Downloading ${asset}..."
if [ -n "$GITHUB_TOKEN" ]; then
    curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" "$url" -o "${tmpdir}/${asset}"
else
    curl -fsSL "$url" -o "${tmpdir}/${asset}"
fi

# Extract
echo "Extracting..."
tar -xzf "${tmpdir}/${asset}" -C "$tmpdir"

# Install
mkdir -p "$install_dir"
if [ -w "$install_dir" ]; then
    mv "${tmpdir}/dtctl" "$install_dir/dtctl"
else
    echo "Installing to ${install_dir} (requires sudo)..."
    sudo mv "${tmpdir}/dtctl" "$install_dir/dtctl"
fi
chmod +x "$install_dir/dtctl"

# macOS: remove quarantine attribute
if [ "$os" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
    xattr -d com.apple.quarantine "$install_dir/dtctl" 2>/dev/null || true
fi

# Verify
echo ""
"$install_dir/dtctl" version
echo ""
echo "Installed to ${install_dir}/dtctl"

# Check PATH
case ":$PATH:" in
    *":${install_dir}:"*) ;;
    *)
        echo ""
        echo "NOTE: ${install_dir} is not in your PATH."
        echo "Add it by running:"
        echo ""
        echo "  export PATH=\"\$PATH:${install_dir}\""
        echo ""
        echo "To make it permanent, add that line to ~/.bashrc, ~/.zshrc, or ~/.profile."
        ;;
esac

echo ""
echo "Quick setup:"
echo "  dtctl auth login --context my-env --environment \"https://YOUR_ENV.apps.dynatrace.com\""
