package stability_test

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/cmd"
	"github.com/Dynatrace/dtctl/pkg/version"
)

// TestSinceVersionsNameNoReleaseBeyondTheNextOne is the tree-side half of the
// guard on the one part of the manifest a release can falsify on its own.
//
// A since-version is a claim about a release: `experimental since 0.39.0` tells
// a caller which version withdrew the guarantee. A declaration is written
// before that release exists, so it can name a release that never comes -- an
// extra release in between, a different bump than release-please's
// bump-minor-pre-major produced -- and the manifest would then be wrong in the
// direction that matters: it would understate how long the surface was still
// guaranteed.
//
// This test sees only the tree, so it checks what the tree alone can decide: a
// since-version must be a version, and it must be a release that has either
// already happened (at or below pkg/version.Version) or is the next one (the
// next patch or the next minor). Anything further out names a release nobody
// can know the number of yet.
//
// What it cannot decide is whether a since-version is *correct for the change
// that introduced it*, because that needs to know what the tree looked like
// before. An earlier version of this test tried -- it allowed only this release
// or the next one -- and so failed permanently on the first commit after every
// release, rejecting declarations that had shipped in exactly the release they
// name. That half lives in scripts/stability/check_compat.py (`make
// stability-compat`), which compares against the base branch: a declaration
// that is new or changed must name the next release rather than one that
// already shipped without it, a released one may not be rewritten, and one that
// names a not-yet-released version must still match the release being cut, so
// a differently-numbered release fails on release-please's own version-bump PR.
func TestSinceVersionsNameNoReleaseBeyondTheNextOne(t *testing.T) {
	current, err := parseVersion(version.Version)
	if err != nil {
		t.Fatalf("pkg/version.Version is not a release version: %v", err)
	}

	// release-please runs with bump-minor-pre-major, so below 1.0 the next
	// release is the next patch (fixes only) or the next minor (any feature).
	nextPatch := [3]int{current[0], current[1], current[2] + 1}
	nextMinor := [3]int{current[0], current[1] + 1, 0}

	for since, users := range cmd.StabilitySinceVersions() {
		v, err := parseVersion(since)
		if err != nil {
			t.Errorf("stability-since %q is not a version (declared by %s)", since, first(users))
			continue
		}
		if !versionLess(current, v) || v == nextPatch || v == nextMinor {
			continue
		}
		sort.Strings(users)
		shown := users
		if len(shown) > 8 {
			shown = append(shown[:8:8], fmt.Sprintf("and %d more", len(users)-8))
		}
		t.Errorf("%d declaration(s) name stability-since %s, but pkg/version.Version is %s.\n"+
			"A since-version must name a release that already happened or the next one "+
			"(%d.%d.%d or %d.%d.%d), because the badge is a claim about which release changed the contract.\n"+
			"Fix the declarations (cmd/stability_pre_1_0.go, cmd/breakpoint_helpers.go, "+
			"cmd/inventory.go, ...) and run `make stability-manifest`.\n"+
			"Declared by: %s",
			len(users), since, version.Version,
			nextPatch[0], nextPatch[1], nextPatch[2], nextMinor[0], nextMinor[1], nextMinor[2],
			strings.Join(shown, ", "))
	}
}

// versionLess reports whether a is an earlier release than b.
func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// parseVersion accepts X.Y.Z with an optional prerelease suffix, which is
// dropped: 0.39.0-rc.1 is a candidate for the 0.39.0 contract, not a release of
// its own.
func parseVersion(s string) ([3]int, error) {
	var out [3]int
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("%q is not X.Y.Z", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, fmt.Errorf("%q is not X.Y.Z", s)
		}
		out[i] = n
	}
	return out, nil
}

func first(s []string) string {
	if len(s) == 0 {
		return "(nothing)"
	}
	sort.Strings(s)
	return s[0]
}
