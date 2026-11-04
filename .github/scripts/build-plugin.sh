#!/bin/sh
# Assemble the Claude Code plugin zip from the GoReleaser output in dist/.
# Usage: .github/scripts/build-plugin.sh <tag>   ->  dist/dtctl-plugin.zip
# Claude Code puts bin/ on PATH for its shell tools; bin/dtctl (wrapper) picks the
# binary of the host. The version in plugin.json is what makes Claude Code update.
set -eu
tag=$1; v=${tag#v}
out=$(mktemp -d)
mkdir -p "$out/bin" "$out/skills"
pick() { # pick <os> <arch> -> the single GoReleaser binary for that platform
  set -- dist/dtctl_"$1"_"$2"*/dtctl"$3"
  [ "$#" -eq 1 ] && [ -f "$1" ] || { echo "expected exactly one binary: $*" >&2; exit 1; }
  echo "$1"
}
for p in linux:amd64 linux:arm64 darwin:amd64 darwin:arm64; do
  os=${p%:*}; arch=${p#*:}
  mkdir "$out/bin/$os-$arch"
  cp "$(pick "$os" "$arch" "")" "$out/bin/$os-$arch/dtctl"
done
# Windows amd64 goes straight to bin/ so that dtctl.exe resolves on PATH.
cp "$(pick windows amd64 .exe)" "$out/bin/dtctl.exe"
cp plugin/bin/dtctl "$out/bin/"
chmod 0755 "$out"/bin/dtctl "$out"/bin/*/dtctl
mkdir "$out/.claude-plugin"
jq --arg v "$v" '.version = $v' plugin/.claude-plugin/plugin.json > "$out/.claude-plugin/plugin.json"
# The skill without embed.go, which only exists for the Go embed of the binary.
cp -r skills/dtctl "$out/skills/"
rm -f "$out/skills/dtctl/embed.go"
cp LICENSE NOTICE "$out/"
(cd "$out" && zip -qr "$OLDPWD/dist/dtctl-plugin.zip" .)
rm -rf "$out"
echo "dist/dtctl-plugin.zip for $tag"
