package version

// Version is the current version of dtctl.
// This is the fallback for builds without -ldflags (e.g. `go install`); release
// builds override it via GoReleaser. The annotation keeps it in sync with the
// release tag automatically via release-please's generic updater — do not edit
// by hand.
var Version = "0.43.0" // x-release-please-version

// Commit is the git commit hash (set at build time)
var Commit = "unknown"

// Date is the build date (set at build time)
var Date = "unknown"
