package stability

import (
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/sdk/session"
)

// Registry is the set of development-tier features known to this binary,
// whether or not they are enabled. It exists so that a *disabled* feature can
// still be named — Cobra cannot attach an error to a command that was never
// added to the tree, so the unknown-command path consults this instead.
//
// It holds strings only: a feature key and the command paths it gates. A
// feature is one opt-in however many commands it spans (`actions` gates both
// `get actions` and `describe action`). No
// command tree, no config access. Enablement is decided by the caller and
// passed in, which keeps the package free of config imports and trivially
// testable.
type Registry struct {
	mu       sync.RWMutex
	features map[string][]string // feature key → command paths, in declaration order
}

// defaultRegistry is the process-wide registry. Development-tier features
// register into it from their package's init or wiring code.
var defaultRegistry = &Registry{features: map[string][]string{}}

// DefaultRegistry returns the process-wide development-feature registry.
func DefaultRegistry() *Registry { return defaultRegistry }

// Declare records that `feature` gates the command at `path` (space-joined,
// relative to root — e.g. "account" or "serve"). Declaring is independent of
// registering the command: a feature must be declared even when it is
// disabled, because that is the only way a disabled command can be named in a
// block message. Declaring a second path adds it to the feature; declaring a
// path again is a no-op.
func (r *Registry) Declare(feature, path string) {
	feature = normalizeKey(feature)
	if feature == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.features[feature], path) {
		r.features[feature] = append(r.features[feature], path)
	}
}

// Features returns the registered feature keys in sorted order.
func (r *Registry) Features() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.features))
	for k := range r.features {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Path returns the first command path a feature gates, and false when the
// feature is not registered. Use Paths for all of them.
func (r *Registry) Path(feature string) (string, bool) {
	paths := r.Paths(feature)
	if len(paths) == 0 {
		return "", false
	}
	return paths[0], true
}

// Paths returns every command path a feature gates, in declaration order, or
// nil when the feature is not registered.
func (r *Registry) Paths(feature string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.features[normalizeKey(feature)])
}

// FeatureForCommand returns the feature key gating the given command path (or
// any of its ancestors), and false when the path is not gated. Ancestor
// matching is what makes `dtctl account login` resolve to the `account`
// feature.
func (r *Registry) FeatureForCommand(path string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Longest match wins, so a feature gating a subtree does not shadow a more
	// specific one nested inside it.
	best, bestLen := "", -1
	for feature, paths := range r.features {
		for _, gated := range paths {
			if segmentPrefix(gated, path) && len(gated) > bestLen {
				best, bestLen = feature, len(gated)
			}
		}
	}
	return best, bestLen >= 0
}

// EnabledPaths returns every command path of every enabled feature, sorted. The
// stability floor uses them as implicit exceptions, so a feature the operator
// switched on is not blocked a moment later by the default floor.
func (r *Registry) EnabledPaths(enabledKeys map[string]bool) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var paths []string
	for feature, gated := range r.features {
		if enabledKeys[session.DevelopmentAll] || enabledKeys[normalizeKey(feature)] {
			paths = append(paths, gated...)
		}
	}
	sort.Strings(paths)
	return paths
}

// Enabled reports whether a feature is switched on, given the set of enabled
// keys resolved from config and the environment. The DevelopmentAll sentinel
// enables every registered feature at once.
func Enabled(feature string, enabledKeys map[string]bool) bool {
	if enabledKeys[session.DevelopmentAll] {
		return true
	}
	return enabledKeys[normalizeKey(feature)]
}

// Register declares a development feature, marks the command as
// development-tier, and adds it to the parent only when the feature is
// enabled. It is the one call a development-tier command should need:
//
//	stability.Register(rootCmd, accountCmd, "account", enabledKeys)
//
// Skipping registration — rather than hiding — is what makes the tier
// unreachable by accident: an invocation yields the ordinary unknown-command
// error, and the command is absent from help, completion and the catalog.
// Returns true when the command was registered.
func Register(parent, cmd *cobra.Command, feature string, enabledKeys map[string]bool) bool {
	MarkDevelopment(cmd, feature)
	path := strings.TrimSpace(strings.TrimPrefix(
		strings.TrimSpace(parentPath(parent)+" "+cmd.Name()), " "))
	defaultRegistry.Declare(feature, path)
	if !Enabled(feature, enabledKeys) {
		return false
	}
	parent.AddCommand(cmd)
	return true
}

// parentPath returns a parent command's path relative to its own root, so
// Register can compute the gated command's full path. Empty for the root.
func parentPath(parent *cobra.Command) string {
	root := parent
	for root.Parent() != nil {
		root = root.Parent()
	}
	return Path(parent, root)
}

// normalizeKey accepts the bare feature key ("account") or the config-style
// dotted form ("development.account") and returns the bare key.
func normalizeKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.TrimPrefix(key, "development.")
}

// segmentPrefix reports whether prefix matches the leading whole segments of
// path, so "account" matches "account login" but not an unrelated
// "accounting"-style command. Mirrors the profile allowlist's matcher.
func segmentPrefix(prefix, path string) bool {
	ps := strings.Fields(prefix)
	xs := strings.Fields(path)
	if len(ps) == 0 || len(ps) > len(xs) {
		return false
	}
	for i, seg := range ps {
		if xs[i] != seg {
			return false
		}
	}
	return true
}
