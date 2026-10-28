# Go module path after the move to the Dynatrace org

**Decision:** the module paths change with the repository transfer.

| Module | Before | After |
|---|---|---|
| CLI / engine | `github.com/dynatrace-oss/dtctl` | `github.com/Dynatrace/dtctl` |
| SDK | `github.com/dynatrace-oss/dtctl/sdk` | `github.com/Dynatrace/dtctl/sdk` |

## Why

- A module path is what importers type. Keeping the old path would make every
  `go get` depend on GitHub's repository redirect, and a path that names an
  organization the repository no longer lives in is a trap for the next move.
- A path change breaks importers, so it ships in `v1.0.0`, the one release where
  a break is free (see [Stability Tiers](../STABILITY.md)).
- Tags carry the module path declared in their own `go.mod`. Releases up to and
  including `v0.44.x` keep the old path and stay resolvable under it; the first
  release built from this change is the first to declare the new one.

## Consequences

- The root `require` of `github.com/Dynatrace/dtctl/sdk` must name a real
  `sdk/vX.Y.Z` tag whose `go.mod` declares the **new** path. Until the first such
  release exists, in-repo builds are green only because of the `replace`; the
  release-please version bump supplies the real tag. `.github/workflows/release.yml`
  keeps mirroring each `vX.Y.Z` into `sdk/vX.Y.Z`. Guard:
  `go test ./pkg/version/ -run TestSDKRequireIsResolvable`.
- Embedders update their imports from `dynatrace-oss` to `Dynatrace` (case matters).
- `dynatrace-oss/tap`, `dynatrace-oss/homebrew-tap` and other sibling
  repositories are separate repositories and are unchanged.

## GitHub Pages

GitHub Pages URLs do not redirect on transfer. The only remaining reference to
`dynatrace-oss.github.io` is `docs/site/_config.yml`, the redirect-stub site that
keeps pre-move links such as `/dtctl/docs/<slug>/` resolving. It must stay up
until those links are retired, and the Pages shutdown is ordered **after** the
transfer.
