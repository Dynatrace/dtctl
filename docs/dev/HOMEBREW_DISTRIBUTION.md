# Homebrew Distribution

Spec for distributing `dtctl` via Homebrew using a custom tap.

## Status

- **Current**: Binary downloads from GitHub Releases only
- **Goal**: `brew install dynatrace-oss/tap/dtctl`
- **Tap repo**: <https://github.com/dynatrace-oss/homebrew-tap>

## Overview

Homebrew uses "taps" (third-party repos) to distribute packages. GoReleaser
has built-in support for generating and pushing a Cask to a tap repo on every
release.

We use `homebrew_casks` (not the deprecated `brews`/Formulas) because:

- GoReleaser deprecated `brews` in v2.10 in favor of `homebrew_casks`
- Casks are the recommended distribution mechanism for pre-built binaries
- Since `dtctl` binaries are not code-signed/notarized, a post-install hook
  strips the macOS quarantine attribute automatically

User experience after implementation:

```bash
brew install dynatrace-oss/tap/dtctl

# Upgrades work automatically:
brew upgrade dtctl
```

## Implementation Plan

### Step 1: Initialize the tap repository

The tap repo exists at <https://github.com/dynatrace-oss/homebrew-tap>.

> **Note:** Homebrew requires the `homebrew-` prefix on the GitHub repo name.
> Users reference it as `dynatrace-oss/tap` in `brew` commands — Homebrew
> adds the `homebrew-` prefix automatically when resolving the repo.

Repository structure:

```text
dynatrace-oss/homebrew-tap/
  README.md
  Casks/
    .gitkeep        # Placeholder -- GoReleaser creates dtctl.rb on first release
```

GoReleaser will create `Casks/dtctl.rb` automatically on the first release.

### Step 2: Create a GitHub App for tap pushes

GoReleaser needs a token with write access to `dynatrace-oss/homebrew-tap`.
The default `GITHUB_TOKEN` from GitHub Actions is scoped to the current repo
only, so a separate token is required.

We use a **GitHub App** (not a PAT) because:

- Owned by the org, not a personal account (no bus factor)
- Narrowly scoped permissions (`Contents: read+write` on `homebrew-tap` only)
- Short-lived installation tokens (auto-rotated per workflow run)
- Shows up as a bot in commit history

Setup:

1. Create a GitHub App in the `dynatrace-oss` org:
   - Name: e.g., `dtctl-homebrew-tap`
   - Permissions: `Contents: read+write` (repository)
   - No webhook needed (uncheck "Active" under Webhook)
2. Install the app on the `homebrew-tap` repo only
3. Store two secrets in `Dynatrace/dtctl` repo settings:
   - `HOMEBREW_TAP_APP_ID` — the App ID (from the app's settings page)
   - `HOMEBREW_TAP_APP_PRIVATE_KEY` — a generated private key (PEM format)

The release workflow uses `actions/create-github-app-token` to exchange
these credentials for a short-lived installation token at runtime.

### Step 3: Update `.goreleaser.yaml`

Use `homebrew_casks` (not the deprecated `brews`):

```yaml
homebrew_casks:
  - repository:
      owner: dynatrace-oss
      name: homebrew-tap
      token: "{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}"
    homepage: https://github.com/Dynatrace/dtctl
    description: >-
      kubectl-inspired CLI for Dynatrace - manage workflows, dashboards,
      SLOs, queries, and more from your terminal
    skip_upload: auto
    completions:
      bash: completions/dtctl.bash
      zsh: completions/dtctl.zsh
      fish: completions/dtctl.fish
    hooks:
      post:
        install: |
          if OS.mac?
            system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/dtctl"]
          end
```

Key decisions:

- **`homebrew_casks`** -- The non-deprecated GoReleaser mechanism (since v2.10).
  Generates a Cask file in `Casks/` directory.
- **`skip_upload: auto`** -- GoReleaser skips the tap push for pre-release
  tags. This prevents unstable versions from being published to Homebrew.
- **Post-install hook** -- Strips the macOS quarantine attribute since dtctl
  binaries are not code-signed. This prevents the "cannot be opened because
  Apple cannot verify" error.
- **No `directory` key** -- Defaults to `Casks/`, the standard Homebrew
  convention for casks.

Also fix other deprecations in the same change:

- `archives.format` -> `archives.formats` (list)
- `archives.format_overrides.format` -> `formats` (list)
- `snapshot.name_template` -> `snapshot.version_template`

### Step 4: Update the release workflow

Add the GitHub App token generation step and pass it to GoReleaser in
`.github/workflows/release.yml`:

```yaml
      - name: Generate Homebrew tap token
        id: app-token
        uses: actions/create-github-app-token@v2
        with:
          app-id: ${{ secrets.HOMEBREW_TAP_APP_ID }}
          private-key: ${{ secrets.HOMEBREW_TAP_APP_PRIVATE_KEY }}
          owner: dynatrace-oss
          repositories: homebrew-tap

      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          HOMEBREW_TAP_GITHUB_TOKEN: ${{ steps.app-token.outputs.token }}
```

The `create-github-app-token` action exchanges the App ID + private key for a
short-lived installation token scoped to `homebrew-tap`. This token is passed
to GoReleaser which uses it to push the generated cask file.

### Step 5: Update README and docs

- **README.md** -- Add `brew install dynatrace-oss/tap/dtctl` as the primary
  install method.
- **docs/INSTALLATION.md** -- Add Homebrew section before binary download.
- **docs/dev/IMPLEMENTATION_STATUS.md** -- Check the `Homebrew tap` box.

### Step 6: Test locally before merging

Verify the cask generation works before cutting a release:

```bash
# 1. Generate a snapshot release (no upload)
make release-snapshot

# 2. Inspect the generated cask
cat dist/homebrew_casks/Casks/dtctl.rb

# 3. Verify the cask is valid Ruby
ruby -c dist/homebrew_casks/Casks/dtctl.rb

# 4. Optionally test with a local tap
mkdir -p /tmp/test-tap/Casks
cp dist/homebrew_casks/Casks/dtctl.rb /tmp/test-tap/Casks/
brew tap --force local/tap /tmp/test-tap
brew install local/tap/dtctl
dtctl version
brew uninstall dtctl
brew untap local/tap
```

### Step 7: First release with Homebrew

1. Merge the goreleaser + workflow changes to `main`
2. Tag a new release: `git tag v0.X.0 && git push origin v0.X.0`
3. Verify:
   - GitHub Release is created with binaries
   - `dynatrace-oss/homebrew-tap` has a new commit with `Casks/dtctl.rb`
   - `brew install dynatrace-oss/tap/dtctl` works on a clean machine

## Generated Cask

For reference, GoReleaser will generate something like this:

```ruby
cask "dtctl" do
  version "0.12.0"
  sha256 "abc123..."

  url "https://github.com/Dynatrace/dtctl/releases/download/v0.12.0/dtctl_0.12.0_darwin_arm64.tar.gz"
  name "dtctl"
  desc "kubectl-inspired CLI for Dynatrace - manage workflows, dashboards, SLOs, queries, and more from your terminal"
  homepage "https://github.com/Dynatrace/dtctl"

  binary "dtctl"

  postflight do
    if OS.mac?
      system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/dtctl"]
    end
  end
end
```

## Maintenance

- **Cask updates are automatic** -- every tagged release triggers GoReleaser,
  which pushes an updated cask.
- **Pre-releases are skipped** -- `skip_upload: auto` ensures only stable
  releases are published.
- **No manual cask editing** -- The cask is fully generated. Don't edit
  `dtctl.rb` in the tap repo by hand.

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| GitHub App secrets not set | Token generation step fails, release does not proceed |
| Pre-release tag (e.g., `v1.0.0-rc1`) | Cask not uploaded (`skip_upload: auto`) |
| Fork creates a tag | No app secrets available, token step fails |
| Tap repo doesn't exist | GoReleaser fails the cask step |
| Casks/dtctl.rb already exists | GoReleaser overwrites it (normal behavior) |

## Out of Scope (for now)

- **homebrew-core submission** -- Requires meaningful user base and follows a
  different process (PR to Homebrew/homebrew-core). Consider later.
- **Code signing / notarization** -- Would remove the need for the xattr hook.
  Requires an Apple Developer Program membership ($99/year).
- **Linux-specific package managers** (apt/yum) -- Separate effort, tracked in
  IMPLEMENTATION_STATUS.md.
- **Scoop (Windows)** -- GoReleaser supports this too, but separate effort.
- **Docker image** -- Related but separate distribution channel.

## Checklist

- [x] Initialize `dynatrace-oss/homebrew-tap` with README and empty `Casks/` dir
- [ ] Create GitHub App (`dtctl-homebrew-tap`) with `Contents: read+write`
- [ ] Install app on `homebrew-tap` repo only
- [ ] Store `HOMEBREW_TAP_APP_ID` and `HOMEBREW_TAP_APP_PRIVATE_KEY` secrets in `Dynatrace/dtctl`
- [x] Add `homebrew_casks` section to `.goreleaser.yaml`
- [x] Fix all GoReleaser deprecation warnings (formats, version_template)
- [x] Add GitHub App token generation to `.github/workflows/release.yml`
- [x] Test with `make release-snapshot` locally
- [x] Update README.md with Homebrew install instructions
- [x] Update docs/INSTALLATION.md
- [ ] Tag a release and verify end-to-end
- [x] Update IMPLEMENTATION_STATUS.md
