package version

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// sdk/ is a second Go module in this repo. In-tree builds resolve it through the
// `replace ... => ./sdk` directive in the root go.mod — but Go ignores `replace`
// directives of a module it is consuming as a dependency, so an external caller
// (`go get github.com/Dynatrace/dtctl` to import pkg/engine) resolves the
// `require` line literally, against the tag `sdk/vX.Y.Z`.
//
// That require line held the placeholder `v0.0.0-00010101000000-000000000000`
// for the module's whole life, which made the root module unimportable
// ("invalid version: unknown revision 000000000000") without anyone noticing:
// every in-repo build, and all of CI, went through the replace. This test is the
// missing signal.
var (
	sdkRequireRe = regexp.MustCompile(
		`(?m)^\s*github\.com/Dynatrace/dtctl/sdk\s+(\S+)\s*//\s*x-release-please-version\s*$`)
	sdkReplaceRe = regexp.MustCompile(
		`(?m)^replace\s+github\.com/Dynatrace/dtctl/sdk\s+=>\s+\./sdk\s*$`)
	versionVarRe = regexp.MustCompile(
		`(?m)^var Version = "([^"]+)"\s*//\s*x-release-please-version\s*$`)
	// vX.Y.Z with an optional semver prerelease of dot-separated alphanumeric
	// identifiers, so release candidates (v0.36.0-rc.1) are accepted — release-please
	// writes those into go.mod verbatim, and sdk/vX.Y.Z-rc.N is a valid module tag.
	//
	// Hyphens are deliberately disallowed *inside* the prerelease, even though semver
	// permits them: that is the one thing separating a release tag from a Go
	// pseudo-version. It is what keeps the placeholder this test exists to catch
	// (v0.0.0-00010101000000-000000000000) and real pseudo-versions
	// (v0.37.1-0.20260101120000-abcdef123456) from slipping through.
	releaseTagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$`)
)

func TestSDKRequireIsResolvable(t *testing.T) {
	goMod := readRepoFile(t, "go.mod")

	m := sdkRequireRe.FindStringSubmatch(goMod)
	require.NotNil(t, m,
		"go.mod must require github.com/Dynatrace/dtctl/sdk on a single line carrying the "+
			"`// x-release-please-version` annotation. Without the annotation release-please stops "+
			"bumping the version on release, and the require silently rots behind the sdk/ tags.")

	require.Regexp(t, releaseTagRe, m[1],
		"the sdk require must name a released tag (sdk/vX.Y.Z, optionally -rc.N), not a "+
			"pseudo-version. A pseudo-version or the `v0.0.0-00010101000000-000000000000` "+
			"placeholder builds fine in-repo (the replace directive covers it) but makes this "+
			"module impossible to import: `go get github.com/Dynatrace/dtctl` fails with "+
			"`invalid version: unknown revision`.")

	require.Regexp(t, sdkReplaceRe, goMod,
		"go.mod must keep `replace github.com/Dynatrace/dtctl/sdk => ./sdk`. It is ignored "+
			"downstream, but without it local builds and CI would compile against the last released "+
			"sdk tag instead of the in-tree sdk/ sources.")

	// The release workflow mirrors every release tag vX.Y.Z into sdk/vX.Y.Z at
	// the same commit (.github/workflows/release.yml, job tag-sdk), so the two
	// versions are equal by construction. Checking it here means a hand-edit of
	// either file fails the build rather than the next `go get`.
	vm := versionVarRe.FindStringSubmatch(readRepoFile(t, filepath.Join("pkg", "version", "version.go")))
	require.NotNil(t, vm, "version.go must declare the annotated Version variable")
	require.Equal(t, "v"+vm[1], m[1],
		"the sdk require must match the CLI version: the release workflow tags sdk/vX.Y.Z at the "+
			"same commit as vX.Y.Z, so any other value names a tag that does not exist")
}

// readRepoFile reads a path relative to the repository root.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel))
	require.NoError(t, err)
	return string(data)
}
