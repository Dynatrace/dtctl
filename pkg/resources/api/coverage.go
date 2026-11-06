package api

import (
	"maps"
	"strings"
)

// Coverage records what dtctl already offers for an API base path.
type Coverage struct {
	// Resource is the short label shown in the DTCTL column of `dtctl get apis`.
	Resource string
	// Command is the command a caller should prefer, spelled as they would type
	// it. It is deliberately not derived from Resource: not every wrapped API is
	// reached through `get <resource>` — some have a verb of their own
	// (`dtctl query`), some live under a nested exec subcommand, and the plural
	// is not always the resource name. A suggestion that does not run is worse
	// than none, because it teaches a caller that dtctl's advice is unreliable.
	Command string
}

// nativeCoverage maps a platform API base path to what already wraps it.
//
// It powers two things: the DTCTL column of `dtctl get apis` (and its inverse,
// `--uncovered`, which is a contribution backlog derived from the environment
// rather than hand-maintained), and the runtime notice `exec api` prints when a
// caller reaches for the passthrough on a path that already has a native
// command. That notice is what keeps the escape hatch self-deprecating.
//
// Every Command here is asserted against the real command tree by a test in
// cmd/, so an entry naming a command that no longer exists fails the build
// rather than misdirecting a caller.
var nativeCoverage = map[string]Coverage{
	"/platform/automation/v1":                   {"workflow", "dtctl get workflows"},
	"/platform/automation/v1/scheduling-rules":  {"scheduling-rule", "dtctl get scheduling-rules"},
	"/platform/document/v1":                     {"document", "dtctl get documents"},
	"/platform/slo/v1":                          {"slo", "dtctl get slos"},
	"/platform/storage/query/v1":                {"query", "dtctl query"},
	"/platform/storage/management/v1":           {"bucket", "dtctl get buckets"},
	"/platform/storage/filter-segments/v1":      {"segment", "dtctl get segments"},
	"/platform/storage/resource-store/v1":       {"lookup", "dtctl get lookups"},
	"/platform/classic/environment-api/v2":      {"settings", "dtctl get settings"},
	"/platform/credential-vault/v1":             {"credential", "dtctl get credentials"},
	"/platform/extensions/v2":                   {"extension", "dtctl get extensions"},
	"/platform/hub/v1":                          {"hub-extension", "dtctl get hub-extensions"},
	"/platform/iam/v1":                          {"user", "dtctl get users"},
	"/platform/notification/v2":                 {"notification", "dtctl get notifications"},
	"/platform/openpipeline/v1":                 {"preview-processor", "dtctl exec preview-processor"},
	"/platform/davis/analyzers/v1":              {"analyzer", "dtctl get analyzers"},
	"/platform/davis/copilot/v1":                {"copilot", "dtctl exec copilot"},
	"/platform/app-engine/registry/v1":          {"app", "dtctl get apps"},
	"/platform/app-engine/app-functions/v1":     {"function", "dtctl get functions"},
	"/platform/app-engine/function-executor/v1": {"function", "dtctl exec function"},
	"/platform/app-engine/edge-connect/v1":      {"edgeconnect", "dtctl get edgeconnect"},
	"/platform/dob/graphql":                     {"breakpoint", "dtctl get breakpoints"},
	// Two map consumers, two granularities. NativeResourceFor does an exact lookup
	// on the API catalog's published base path (/platform/management/v1) — the base
	// path entry below makes --uncovered show this API as covered. NativeCoverageForPath
	// uses longest-prefix, so the three per-endpoint entries always win over the base
	// path and point exec api callers at the right command per sub-path.
	"/platform/management/v1":                              {"environment", "dtctl get environment"},
	"/platform/management/v1/environment":                  {"environment", "dtctl get environment"},
	"/platform/management/v1/environment/license":          {"license", "dtctl get license"},
	"/platform/management/v1/environment/license/settings": {"license-settings", "dtctl get license-settings"},
}

// NativeResourceFor returns the dtctl resource covering an API base path, or ""
// when the API has no native command.
func NativeResourceFor(basePath string) string {
	if basePath == "" {
		return ""
	}
	return nativeCoverage[strings.TrimSuffix(basePath, "/")].Resource
}

// NativeCoverageForPath returns the coverage for a concrete request path,
// matching on the longest base path that prefixes it. It is what `exec api` uses
// to notice that a caller is bypassing a native command.
func NativeCoverageForPath(requestPath string) (basePath string, cov Coverage) {
	for base, c := range nativeCoverage {
		if !strings.HasPrefix(requestPath, base) {
			continue
		}
		// Require a segment boundary so /platform/storage/query/v1 does not claim
		// /platform/storage/query/v1beta.
		rest := requestPath[len(base):]
		if rest != "" && !strings.HasPrefix(rest, "/") {
			continue
		}
		if len(base) > len(basePath) {
			basePath, cov = base, c
		}
	}
	return basePath, cov
}

// CoveredBasePaths returns every base path with native coverage. Used by the
// drift test that keeps the map honest.
func CoveredBasePaths() map[string]Coverage {
	out := make(map[string]Coverage, len(nativeCoverage))
	maps.Copy(out, nativeCoverage)
	return out
}
