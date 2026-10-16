// Package commands builds a machine-readable catalog of dtctl's command tree.
// It walks the Cobra command hierarchy and produces a structured listing that
// AI agents and MCP servers can use for automated tool registration.
package commands

import (
	"encoding/json"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	toon "github.com/toon-format/toon-go"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/plugin"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/version"
)

// SchemaVersion is incremented on breaking changes to the listing structure.
// v2 adds per-verb `access`, per-(verb,resource) `required_scopes_by_resource`,
// per-verb `required_scopes` (DQL verbs), and a top-level `resource_scopes`
// canonical table.
// v3 adds the stability axis: per-verb and per-flag `stability` /
// `stability_since`, per-verb `deprecated`, and the top-level `min_stability` /
// `stability_exceptions` that describe the active floor.
const SchemaVersion = 3

// Listing is the top-level output of `dtctl commands`.
type Listing struct {
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	Tool          string `json:"tool" yaml:"tool"`
	Version       string `json:"version" yaml:"version"`
	Description   string `json:"description,omitempty" yaml:"description,omitempty"`
	CommandModel  string `json:"command_model" yaml:"command_model"`
	// Profile is the active command profile shaping this catalog, if any. Omitted
	// (empty) when the full command tree is exposed. Advertising it lets an agent
	// see that it is looking at a reduced surface. See COMMAND_PROFILES_DESIGN.md.
	Profile string `json:"profile,omitempty" yaml:"profile,omitempty"`
	// SafetyLevel is the effective safety level of the active context, the
	// orthogonal permission axis. Surfaced alongside Profile so both active
	// constraints are visible at once.
	SafetyLevel string `json:"safety_level,omitempty" yaml:"safety_level,omitempty"`
	// MinStability is the active stability floor: the weakest contract a command
	// or flag may offer and still be usable here. Surfaced so an agent can tell
	// "this command does not exist" from "this command exists but is below the
	// floor this deployment accepts". Omitted when nothing is filtered.
	MinStability string `json:"min_stability,omitempty" yaml:"min_stability,omitempty"`
	// StabilityExceptions are the individual commands and flags admitted below
	// the floor, each rendered as it is written in config
	// ("query --decode-snapshots").
	// Without them a reader of min_stability could not explain why a
	// below-floor command is present in the tree.
	StabilityExceptions []string          `json:"stability_exceptions,omitempty" yaml:"stability_exceptions,omitempty"`
	GlobalFlags         map[string]*Flag  `json:"global_flags,omitempty" yaml:"global_flags,omitempty"`
	Verbs               map[string]*Verb  `json:"verbs" yaml:"verbs"`
	Aliases             map[string]string `json:"resource_aliases,omitempty" yaml:"resource_aliases,omitempty"`
	// ResourceScopes is the canonical (resource, access) → scopes table. Agents
	// can derive any command's required scopes from this table plus each verb's
	// access, which is what --brief relies on.
	ResourceScopes map[string]auth.AccessScopes `json:"resource_scopes,omitempty" yaml:"resource_scopes,omitempty"`
	TimeFormats    *TimeFormats                 `json:"time_formats,omitempty" yaml:"time_formats,omitempty"`
	Patterns       []string                     `json:"patterns,omitempty" yaml:"patterns,omitempty"`
	Antipatterns   []string                     `json:"antipatterns,omitempty" yaml:"antipatterns,omitempty"`
	// Plugins are the dtctl-* executables discovered on PATH. Each runs as
	// `dtctl <name>` (exec convention); agents cannot see them any other way,
	// so the catalog is their discovery surface. Flags and behavior are the
	// plugin's own — dtctl only knows the name and binary.
	Plugins []plugin.Plugin `json:"plugins,omitempty" yaml:"plugins,omitempty"`
}

// Verb represents a top-level verb (get, describe, apply, ...).
type Verb struct {
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Mutating    bool     `json:"mutating" yaml:"mutating"`
	SafetyOp    string   `json:"safety_operation,omitempty" yaml:"safety_operation,omitempty"`
	Access      string   `json:"access,omitempty" yaml:"access,omitempty"`
	Resources   []string `json:"resources,omitempty" yaml:"resources,omitempty"`
	// ResourceStability maps a resource to its tier, for the resources whose
	// tier is not the default `stable`. Resources are rendered as a bare name
	// list to keep the catalog compact, so there is nowhere on the resource
	// itself to carry a contract — yet `get breakpoints` being experimental is
	// exactly what an agent must not learn by having its automation break.
	// Omitted entirely when every resource under the verb is stable, which is
	// the overwhelming majority, so the common case costs nothing.
	ResourceStability map[string]string `json:"resource_stability,omitempty" yaml:"resource_stability,omitempty"`
	// Stability is the effective stability tier (the weakest along the path from
	// root), omitted when it is the default `stable`. An agent that reads no
	// `stability` key is looking at surface it may safely automate against.
	Stability string `json:"stability,omitempty" yaml:"stability,omitempty"`
	// StabilitySince is the dtctl version at which the command entered that
	// tier, so a caller can judge how long it has sat there.
	StabilitySince string `json:"stability_since,omitempty" yaml:"stability_since,omitempty"`
	// Deprecated carries the scheduled-removal note for a still-stable command
	// ("since 0.40.0, remove in 1.0.0, use `get workflows`").
	Deprecated string `json:"deprecated,omitempty" yaml:"deprecated,omitempty"`
	// RequiredScopes is the scope list for verbs whose scopes are not
	// per-resource (e.g. `query`/`verify`/`wait`, which read Grail via DQL).
	RequiredScopes []string `json:"required_scopes,omitempty" yaml:"required_scopes,omitempty"`
	// RequiredScopesByResource maps each resource the verb operates on to the
	// scopes required for this verb's access level. Materialized in full mode so
	// agents need zero client-side computation; omitted in --brief.
	RequiredScopesByResource map[string][]string `json:"required_scopes_by_resource,omitempty" yaml:"required_scopes_by_resource,omitempty"`
	Flags                    map[string]*Flag    `json:"flags,omitempty" yaml:"flags,omitempty"`
	// ResourceFlags maps each resource to the names of its own flags. Names
	// only, to keep the full catalog small: `dtctl <verb> <resource> --help`
	// gives types and descriptions. Omitted in --brief.
	ResourceFlags map[string][]string `json:"resource_flags,omitempty" yaml:"resource_flags,omitempty"`
	RequiredArgs  []string            `json:"required_args,omitempty" yaml:"required_args,omitempty"`
	Subcommands   map[string]*Verb    `json:"subcommands,omitempty" yaml:"subcommands,omitempty"`
}

// Flag describes a CLI flag.
type Flag struct {
	Type        string `json:"type" yaml:"type"`
	Default     string `json:"default,omitempty" yaml:"default,omitempty"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Stability is the flag's own declared tier, omitted when it is the default
	// `stable`. A flag may make a weaker promise than its command — that is how
	// a new idea ships without inventing a new command — so the tier is carried
	// per flag rather than inherited from the verb.
	Stability string `json:"stability,omitempty" yaml:"stability,omitempty"`
	// StabilitySince is the dtctl version at which the flag entered that tier.
	StabilitySince string `json:"stability_since,omitempty" yaml:"stability_since,omitempty"`
}

// TimeFormats describes the time input formats dtctl accepts.
type TimeFormats struct {
	Relative []string `json:"relative" yaml:"relative"`
	Absolute string   `json:"absolute" yaml:"absolute"`
	Unix     string   `json:"unix" yaml:"unix"`
}

// MutatingVerbs maps verb names to their safety operation type.
// Verbs not listed here are read-only (mutating: false).
//
// This map must be kept in sync with actual safety-check call sites in cmd/
// (both NewSafetyChecker and SetupWithSafety helpers). The
// TestMutatingVerbsMatchSafetyCheckerUsage test in cmd/commands_test.go
// cross-references this map against the real command tree to detect drift.
var MutatingVerbs = map[string]string{
	"apply":   "OperationCreate",
	"create":  "OperationCreate",
	"edit":    "OperationUpdate",
	"delete":  "OperationDelete",
	"restore": "OperationUpdate",
	"share":   "OperationUpdate",
	"claim":   "OperationCreate",
	"unshare": "OperationUpdate",
	"update":  "OperationUpdate",
	"exec":    "OperationCreate", // semantically mutating (runs workflows, functions)
	"enable":  "OperationUpdate", // PUTs updated monitoring/credential config to the tenant
	"disable": "OperationUpdate", // PUTs updated monitoring config with enabled=false
}

// ResourceAliases are the standard resource aliases built into dtctl.
//
// This map must be kept in sync with Cobra command Aliases fields in cmd/.
// The TestResourceAliasesMatchCobraAliases test in cmd/commands_test.go
// cross-references this map against the real command tree to detect drift.
var ResourceAliases = map[string]string{
	"wf":   "workflows",
	"sr":   "scheduling-rules",
	"dash": "dashboards",
	"db":   "dashboards",
	"nb":   "notebooks",
	"bkt":  "buckets",
	"ec":   "edgeconnect",
	"fn":   "functions",
	"func": "functions",
	"ad":   "anomaly-detectors",
}

// hiddenCommands are commands excluded from the listing (internal, utility).
var hiddenCommands = map[string]bool{
	"help":       true,
	"completion": true,
	"commands":   true, // self-referential — not useful in the catalog
	"version":    true, // utility, not an operational command
}

// unadvertisedResources are `<verb> <resource>` leaves that are hidden on the
// command line but still documented in the full catalog.
//
// The two states cobra offers — visible or hidden — are both wrong for an escape
// hatch. Visible puts it in --help and in the compact catalogs an agent
// bootstraps from, next to the native commands it must not replace. Hidden makes
// it undiscoverable, which pushes a caller who genuinely needs it toward
// something worse (an ad-hoc AppEngine function with a bearer token). So it is
// omitted from `--brief`/minimal and present in `--full`: findable by a caller
// who goes looking, invisible to one who is browsing.
var unadvertisedResources = map[string]bool{
	"exec api": true,
}

// advertisedResources drops the unadvertised leaves of a verb, for the catalog
// projections that are meant to be browsed rather than searched.
func advertisedResources(verb string, resources []string) []string {
	var out []string
	for _, r := range resources {
		if unadvertisedResources[verb+" "+r] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// stabilityOfAdvertised narrows a verb's resource-stability map to the
// resources the caller is actually shown. Without the narrowing, an
// unadvertised resource would keep a tier entry after being filtered out of
// the name list, advertising the very command the filter exists to hide.
// Returns nil when nothing is left, so the field is omitted rather than empty.
func stabilityOfAdvertised(verb string, v *Verb) map[string]string {
	if len(v.ResourceStability) == 0 {
		return nil
	}
	out := make(map[string]string, len(v.ResourceStability))
	for _, r := range advertisedResources(verb, v.Resources) {
		if lvl, ok := v.ResourceStability[r]; ok {
			out[r] = lvl
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// patterns are recommended usage patterns for AI agents.
var defaultPatterns = []string{
	"Use 'dtctl apply -f' for idempotent resource management",
	"Use 'dtctl diff' before 'dtctl apply' to preview changes",
	"Use 'dtctl query' for ad-hoc DQL queries, not resource-specific flags",
	"Use '--dry-run' to validate apply operations without executing",
	"Use '--agent' for JSON output with operational metadata",
	"Use 'dtctl wait' in CI/CD to poll for conditions",
	"Always specify '--context' in automation scripts",
	"Discover the APIs an environment publishes with 'dtctl get apis'; 'dtctl describe api <name> --operation \"<METHOD> <path>\"' gives the parameters, body schema, and required scope",
	"Query Smartscape topology nodes with: dtctl query 'smartscapeNodes \"<TYPE>\" | limit 50'",
	"Discover all Smartscape node types in the tenant with: dtctl query 'smartscapeNodes \"*\" | dedup type | fields type'",
}

// antipatterns are common mistakes agents should avoid.
//
// Two of these name 'exec api', which unadvertisedResources keeps out of the
// resource lists. That is deliberate and not a contradiction: the resource list
// is where an escape hatch tempts a caller away from a native command, whereas an
// antipattern carries the constraint along with the name — the only form in which
// it should be discovered.
var defaultAntipatterns = []string{
	"Don't use 'dtctl create' followed by 'dtctl edit' — use 'dtctl apply -f' instead",
	"Don't parse table output — use '-o json' or '--agent'",
	"Don't hardcode resource IDs — use 'dtctl get' to discover them",
	"Don't skip 'dtctl diff' before 'dtctl apply' in production contexts",
	"Don't guess Smartscape node type IDs — run 'smartscapeNodes \"*\" | dedup type | fields type' first to enumerate valid types",
	"Don't call a platform API by writing JavaScript for 'dtctl exec function --code' — use 'dtctl exec api <path>', which derives what the request does from the API's specification and gates it like any mutating command",
	"Don't reach for 'dtctl exec api' when a native command exists, and don't script against it — it is an escape hatch for APIs dtctl doesn't wrap, and repeated use means that API wants a dedicated command",
}

var defaultTimeFormats = &TimeFormats{
	Relative: []string{"1h", "30m", "7d", "5min"},
	Absolute: "RFC3339 (e.g., 2024-01-15T10:00:00Z)",
	Unix:     "Unix timestamp (e.g., 1705312800)",
}

// Build walks the Cobra command tree rooted at root and returns a Listing.
func Build(root *cobra.Command) *Listing {
	verbs := buildVerbs(root)
	listing := &Listing{
		SchemaVersion:  SchemaVersion,
		Tool:           "dtctl",
		Version:        version.Version,
		Description:    "kubectl-inspired CLI for the Dynatrace platform",
		CommandModel:   "verb-noun",
		GlobalFlags:    buildGlobalFlags(root),
		Verbs:          verbs,
		Aliases:        ResourceAliases,
		ResourceScopes: collectResourceScopes(verbs),
		TimeFormats:    defaultTimeFormats,
		Patterns:       defaultPatterns,
		Antipatterns:   defaultAntipatterns,
		Plugins:        plugin.Discover(os.Getenv("PATH"), commandNames(root)),
	}
	return listing
}

// commandNames collects the built-in command names and aliases for plugin
// shadow warnings.
func commandNames(root *cobra.Command) map[string]bool {
	names := make(map[string]bool)
	for _, c := range root.Commands() {
		names[c.Name()] = true
		for _, a := range c.Aliases {
			names[a] = true
		}
	}
	return names
}

// buildGlobalFlags extracts the persistent flags from the root command.
func buildGlobalFlags(root *cobra.Command) map[string]*Flag {
	flags := make(map[string]*Flag)
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		// Skip internal/debug flags
		if f.Hidden {
			return
		}
		key := "--" + f.Name
		flags[key] = &Flag{
			Type:        flagTypeName(f),
			Default:     f.DefValue,
			Description: f.Usage,
		}
	})
	return flags
}

// buildVerbs iterates over the root's direct subcommands and maps them as verbs.
func buildVerbs(root *cobra.Command) map[string]*Verb {
	verbs := make(map[string]*Verb)
	for _, cmd := range root.Commands() {
		name := cmd.Name()
		if hiddenCommands[name] || cmd.Hidden || isHelpTopic(cmd) {
			continue
		}

		verb := &Verb{
			Description: cmd.Short,
		}
		annotateStability(verb, cmd)

		// Determine mutating status
		if safetyOp, ok := MutatingVerbs[name]; ok {
			verb.Mutating = true
			verb.SafetyOp = safetyOp
		}

		// Derive the access level (read/write/delete/run) from the verb.
		verb.Access = string(auth.AccessForVerb(name, verb.SafetyOp))

		// Extract resources (subcommands of the verb)
		subs := cmd.Commands()
		if len(subs) > 0 {
			var resources []string
			var resourceStability map[string]string
			subcommands := make(map[string]*Verb)
			resourceFlags := make(map[string][]string)

			for _, sub := range subs {
				if sub.Name() == "help" || isHelpTopic(sub) {
					continue
				}
				// A hidden leaf is normally absent from the catalog too. An
				// unadvertised one is the exception: the full catalog is the only
				// place it is documented.
				if sub.Hidden && !unadvertisedResources[name+" "+sub.Name()] {
					continue
				}

				subName := sub.Name()

				if nested := listedChildren(sub); len(nested) > 0 {
					// Nested subcommand (e.g., exec copilot -> nl2dql, dql2nl, ...)
					subcommands[subName] = buildNestedVerb(name, sub, nested)
				} else {
					// Treat as a resource
					resources = append(resources, subName)
					if lvl := stability.Effective(sub); lvl != stability.Stable {
						if resourceStability == nil {
							resourceStability = map[string]string{}
						}
						resourceStability[subName] = string(lvl)
					}
					if names := flagNames(sub); len(names) > 0 {
						resourceFlags[subName] = names
					}
				}
			}

			if len(resources) > 0 {
				verb.Resources = resources
				verb.ResourceStability = resourceStability
			}
			if len(resourceFlags) > 0 {
				verb.ResourceFlags = resourceFlags
			}
			if len(subcommands) > 0 {
				verb.Subcommands = subcommands
			}
		}

		// Extract verb-level flags (local flags, not from subcommands)
		verbFlags := collectLocalFlags(cmd)
		if len(verbFlags) > 0 {
			verb.Flags = verbFlags
		}

		// Extract required args from Use string (e.g., "apply -f <file>")
		if args := parseRequiredArgs(cmd.Use); len(args) > 0 {
			verb.RequiredArgs = args
		}

		populateVerbScopes(name, verb)

		verbs[name] = verb
	}
	return verbs
}

// buildNestedVerb describes a subcommand that has subcommands of its own, at
// any depth. Every level inherits the verb's mutating status: `enable aws
// monitoring` mutates exactly as `enable` does.
//
// A leaf child lists its flags by name in the parent's resource_flags, like a
// verb's resources do, instead of as full flag objects.
//
// children is cmd's listedChildren, which the caller has already computed to
// decide that cmd nests at all.
func buildNestedVerb(verbName string, cmd *cobra.Command, children []*cobra.Command) *Verb {
	v := newNestedNode(verbName, cmd)
	if flags := collectLocalFlags(cmd); len(flags) > 0 {
		v.Flags = flags
	}
	v.Subcommands = make(map[string]*Verb, len(children))
	for _, child := range children {
		if nested := listedChildren(child); len(nested) > 0 {
			v.Subcommands[child.Name()] = buildNestedVerb(verbName, child, nested)
			continue
		}
		v.Subcommands[child.Name()] = newNestedNode(verbName, child)
		if names := flagNames(child); len(names) > 0 {
			if v.ResourceFlags == nil {
				v.ResourceFlags = make(map[string][]string)
			}
			v.ResourceFlags[child.Name()] = names
		}
	}
	return v
}

// newNestedNode returns a subcommand entry that carries the verb's mutating
// status, access level and the scopes its name maps to.
func newNestedNode(verbName string, cmd *cobra.Command) *Verb {
	v := &Verb{Description: cmd.Short}
	if safetyOp, ok := MutatingVerbs[verbName]; ok {
		v.Mutating = true
		v.SafetyOp = safetyOp
	}
	v.Access = string(auth.AccessForVerb(verbName, v.SafetyOp))
	if scopes := auth.ScopesForResource(cmd.Name(), auth.Access(v.Access)); len(scopes) > 0 {
		v.RequiredScopes = append([]string(nil), scopes...)
	}
	annotateStability(v, cmd)
	return v
}

// isHelpTopic reports whether cmd is a cobra help topic (such as
// `token-scopes`): documentation with nothing to run and nothing the catalog
// would list beneath it.
//
// The children are counted with listedChildren rather than cobra's
// HasSubCommands, which counts hidden ones. A group whose every child is masked
// — by a command profile, by the stability floor, or by being hidden outright —
// has as little to run as a help topic does, and listing it as a resource would
// advertise a command that only prints its own help.
func isHelpTopic(cmd *cobra.Command) bool {
	return !cmd.Runnable() && len(listedChildren(cmd)) == 0
}

// listedChildren returns the subcommands of cmd that belong in the catalog:
// visible, not cobra's generated `help`, and not a help topic.
//
// One predicate for every level. Asking the question two ways — a hand-rolled
// loop at the verb level and cobra's HasAvailableSubCommands below it — let the
// same command be a resource at one depth and a nested verb at another, and
// left help topics in the catalog everywhere except the top.
func listedChildren(cmd *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.Hidden || c.Name() == "help" || isHelpTopic(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// flagNames returns the sorted keys of cmd's own flags, in the form the flags
// maps use ("--mine", "-y/--yes"). It describes nothing else about them, so it
// visits the flags directly rather than building the objects collectLocalFlags
// would and throwing them away.
func flagNames(cmd *cobra.Command) []string {
	var names []string
	visitOwnFlags(cmd, func(f *pflag.Flag) {
		names = append(names, flagKey(f))
	})
	sort.Strings(names)
	return names
}

// flagKey is a flag's key in the catalog's flag maps: "--mine", or
// "-y/--yes" when it has a shorthand.
func flagKey(f *pflag.Flag) string {
	if f.Shorthand != "" {
		return "-" + f.Shorthand + "/--" + f.Name
	}
	return "--" + f.Name
}

// dqlScopeVerbs read Grail data via DQL; their scopes attach to the verb rather
// than to a managed resource.
var dqlScopeVerbs = map[string]bool{"query": true, "verify": true, "wait": true, "inventory": true}

// populateVerbScopes fills in a verb's required scopes from the canonical
// auth.ResourceScopes table: per-resource for resource-bearing verbs, and a flat
// DQL scope list for query-style verbs.
func populateVerbScopes(name string, verb *Verb) {
	if dqlScopeVerbs[name] {
		verb.RequiredScopes = append([]string(nil), auth.QueryScopes...)
	}

	if len(verb.Resources) == 0 {
		return
	}
	access := auth.Access(verb.Access)
	byResource := make(map[string][]string)
	for _, r := range verb.Resources {
		scopes := auth.ScopesForResource(r, access)
		if len(scopes) == 0 && access != auth.AccessRead {
			// A resource nested under a mutating verb can itself be read-only
			// (e.g. `share get` under the mutating `share` verb): such a
			// resource has no scopes at the verb's own access level by
			// construction, so falling back to its Read scopes reports what
			// it actually requires instead of silently omitting it, which
			// would wrongly read as "needs no token scopes at all". A
			// genuinely mutating resource never hits this fallback -- it
			// always has scopes at the verb's access level.
			scopes = auth.ScopesForResource(r, auth.AccessRead)
		}
		if len(scopes) > 0 {
			byResource[r] = scopes
		}
	}
	if len(byResource) > 0 {
		verb.RequiredScopesByResource = byResource
	}
}

// collectResourceScopes returns the subset of the canonical ResourceScopes table
// covering the resources referenced by the given verbs (including subcommands).
// Returns nil when no referenced resource has a scope mapping.
func collectResourceScopes(verbs map[string]*Verb) map[string]auth.AccessScopes {
	table := make(map[string]auth.AccessScopes)
	add := func(resource string) {
		for _, key := range []string{resource, singularize(resource)} {
			if as, ok := auth.ResourceScopes[key]; ok {
				table[key] = as
				return
			}
		}
	}
	var addVerb func(v *Verb)
	addVerb = func(v *Verb) {
		for _, r := range v.Resources {
			add(r)
		}
		for subName, sub := range v.Subcommands {
			add(subName)
			addVerb(sub)
		}
	}
	for _, verb := range verbs {
		addVerb(verb)
	}
	if len(table) == 0 {
		return nil
	}
	return table
}

// collectLocalFlags describes a command's own non-hidden flags, leaving out the
// persistent flags it inherits.
func collectLocalFlags(cmd *cobra.Command) map[string]*Flag {
	flags := make(map[string]*Flag)
	visitOwnFlags(cmd, func(f *pflag.Flag) {
		fl := &Flag{
			Type:        flagTypeName(f),
			Default:     f.DefValue,
			Description: f.Usage,
		}

		// Mark as required if annotated
		if ann := f.Annotations; ann != nil {
			if _, ok := ann[cobra.BashCompOneRequiredFlag]; ok {
				fl.Required = true
			}
		}

		// Read the tier off the flag rather than looking it up by name:
		// stability.OfFlag searches cmd.Flags(), which does not hold the
		// command's own persistent flags until cobra merges them — the merge
		// this collector exists to avoid.
		// A flag declared stable (stability.MarkFlagStable) is omitted like an
		// undeclared one: the field is documented as absent for stable.
		if lvl := stability.OfFlagValue(f); lvl != stability.Undeclared && lvl != stability.Stable {
			fl.Stability = string(lvl)
			fl.StabilitySince = stability.SinceFlagValue(f)
		}

		flags[flagKey(f)] = fl
	})
	return flags
}

// visitOwnFlags invokes fn for each non-hidden flag a command declares itself,
// once per flag, skipping the persistent flags it inherits.
//
// It reads Flags and PersistentFlags directly. cobra's LocalFlags and
// InheritedFlags merge the parents' persistent flags into the command as a side
// effect, and doing that to the live command tree leaks state between
// invocations.
func visitOwnFlags(cmd *cobra.Command, fn func(*pflag.Flag)) {
	seen := make(map[string]bool)
	visit := func(f *pflag.Flag) {
		if f.Hidden || seen[f.Name] || inheritedFlag(cmd, f) {
			return
		}
		seen[f.Name] = true
		fn(f)
	}
	cmd.Flags().VisitAll(visit)
	cmd.PersistentFlags().VisitAll(visit)
}

// inheritedFlag reports whether f is an ancestor's persistent flag rather than
// one cmd declares itself.
//
// The comparison is by identity, not by name: a command may redefine a name an
// ancestor already uses persistently, and cobra then binds the command's own
// flag (`dtctl diff --context` is an int line count, not the global string
// context). Matching on the name alone would drop every such flag from the
// catalog. Only the nearest ancestor that declares the name matters — that is
// the one cobra would have merged in.
func inheritedFlag(cmd *cobra.Command, f *pflag.Flag) bool {
	for p := cmd.Parent(); p != nil; p = p.Parent() {
		if pf := p.PersistentFlags().Lookup(f.Name); pf != nil {
			return pf == f
		}
	}
	return false
}

// parseRequiredArgs extracts angle-bracket args from a Use string.
// e.g., "apply -f <file>" → ["file"]
func parseRequiredArgs(use string) []string {
	var args []string
	for _, part := range strings.Fields(use) {
		if strings.HasPrefix(part, "<") && strings.HasSuffix(part, ">") {
			args = append(args, strings.Trim(part, "<>"))
		}
	}
	return args
}

// flagTypeName returns a human-readable type name for a flag.
func flagTypeName(f *pflag.Flag) string {
	switch f.Value.Type() {
	case "string":
		return "string"
	case "bool":
		return "boolean"
	case "int", "int32", "int64":
		return "integer"
	case "float32", "float64":
		return "number"
	case "stringSlice", "stringArray":
		return "string[]"
	case "duration":
		return "duration"
	case "count":
		return "integer"
	default:
		return f.Value.Type()
	}
}

// Minimal is an ultra-compact overview of the command tree: just verbs, their
// resources, their stability, and nested subcommands. It carries no
// descriptions, flags, scopes, or mutating status. It is the default `dtctl commands` output — a quick map of
// what exists, since most verb-noun commands are self-explanatory. Use --brief
// or --full for progressively more detail.
type Minimal struct {
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	Tool          string `json:"tool" yaml:"tool"`
	Version       string `json:"version" yaml:"version"`
	CommandModel  string `json:"command_model" yaml:"command_model"`
	// Profile and SafetyLevel advertise the active command profile and effective
	// safety level (the two constraints shaping the surface), so agents see them
	// even in the minimal overview. Omitted when unconstrained.
	Profile     string `json:"profile,omitempty" yaml:"profile,omitempty"`
	SafetyLevel string `json:"safety_level,omitempty" yaml:"safety_level,omitempty"`
	// MinStability and StabilityExceptions advertise the third constraint, the
	// stability floor, for the same reason: an agent bootstrapping from the
	// compact catalog must be able to tell "this command does not exist" from
	// "this command is below the contract this deployment accepts".
	MinStability        string                  `json:"min_stability,omitempty" yaml:"min_stability,omitempty"`
	StabilityExceptions []string                `json:"stability_exceptions,omitempty" yaml:"stability_exceptions,omitempty"`
	Verbs               map[string]*MinimalVerb `json:"verbs" yaml:"verbs"`
	Aliases             map[string]string       `json:"resource_aliases,omitempty" yaml:"resource_aliases,omitempty"`
	// Examples are runnable starting points, listed only when their command is in the catalog.
	Examples []string `json:"examples,omitempty" yaml:"examples,omitempty"`
}

// minimalExamples are read-only, environment-independent starting points.
var minimalExamples = []string{
	`dtctl query 'fetch logs, from:now()-24h | filter loglevel == "ERROR" | summarize count(), by:{dt.service.name} | sort ` + "`count()`" + ` desc | limit 10'`,
	`dtctl get workflow-executions --started-since 7d`,
	`dtctl get slos  # definitions; dtctl exec slo <id> evaluates one`,
	`dtctl describe <resource> <id>`,
}

// MinimalVerb is a verb reduced to its resources and nested subcommands.
type MinimalVerb struct {
	Resources []string `json:"resources,omitempty" yaml:"resources,omitempty"`
	// Stability is carried even here: this is the documented agent bootstrap
	// path, and "may change in any release" is not something an agent should
	// have to discover by having its automation break. Omitted for `stable`.
	Stability string `json:"stability,omitempty" yaml:"stability,omitempty"`
	// ResourceStability is carried here for the same reason as Stability: a
	// resource-style command (`get breakpoints`) has no other slot in which to
	// state its contract.
	ResourceStability map[string]string       `json:"resource_stability,omitempty" yaml:"resource_stability,omitempty"`
	Subcommands       map[string]*MinimalVerb `json:"subcommands,omitempty" yaml:"subcommands,omitempty"`
}

// NewMinimal returns an ultra-compact overview of the command tree. The original
// listing is not modified.
func NewMinimal(l *Listing) *Minimal {
	m := &Minimal{
		SchemaVersion:       l.SchemaVersion,
		Tool:                l.Tool,
		Version:             l.Version,
		CommandModel:        l.CommandModel,
		Profile:             l.Profile,
		SafetyLevel:         l.SafetyLevel,
		MinStability:        l.MinStability,
		StabilityExceptions: l.StabilityExceptions,
		Verbs:               make(map[string]*MinimalVerb, len(l.Verbs)),
		Aliases:             l.Aliases,
	}
	for name, v := range l.Verbs {
		m.Verbs[name] = newMinimalVerb(name, v)
	}
	for _, ex := range minimalExamples {
		if exampleAvailable(ex, m.Verbs) {
			m.Examples = append(m.Examples, ex)
		}
	}
	return m
}

// exampleAvailable reports whether an example's command is in the catalog.
func exampleAvailable(ex string, verbs map[string]*MinimalVerb) bool {
	f := strings.Fields(ex)
	if len(f) < 2 {
		return false
	}
	v, ok := verbs[f[1]]
	if !ok {
		return false
	}
	if len(v.Resources) == 0 || len(f) < 3 || strings.HasPrefix(f[2], "<") {
		return true
	}
	if slices.Contains(v.Resources, f[2]) {
		return true
	}
	_, sub := v.Subcommands[f[2]]
	return sub
}

// newMinimalVerb strips a verb down to its resources and nested subcommands.
func newMinimalVerb(verb string, v *Verb) *MinimalVerb {
	mv := &MinimalVerb{
		Resources:         advertisedResources(verb, v.Resources),
		Stability:         v.Stability,
		ResourceStability: stabilityOfAdvertised(verb, v),
	}
	if len(v.Subcommands) > 0 {
		mv.Subcommands = make(map[string]*MinimalVerb, len(v.Subcommands))
		for name, sub := range v.Subcommands {
			mv.Subcommands[name] = newMinimalVerb(verb+" "+name, sub)
		}
	}
	return mv
}

// NewBrief returns a copy of the listing with verbose fields stripped for
// reduced token count. It preserves mutating status since agents always need it.
// The original listing is not modified.
func NewBrief(l *Listing) *Listing {
	brief := &Listing{
		SchemaVersion: l.SchemaVersion,
		Tool:          l.Tool,
		Version:       l.Version,
		CommandModel:  l.CommandModel,
		Profile:       l.Profile,
		SafetyLevel:   l.SafetyLevel,
		// The floor and its exceptions are three short strings; dropping them
		// from the brief catalog would hide a constraint the agent is subject
		// to on the very path it is told to bootstrap from.
		MinStability:        l.MinStability,
		StabilityExceptions: l.StabilityExceptions,
		Verbs:               make(map[string]*Verb, len(l.Verbs)),
		Aliases:             l.Aliases,
		// Retain patterns/antipatterns: they are the primary grounding agents
		// rely on after bootstrapping with `dtctl commands --brief -o json`, and
		// cost only a handful of tokens. Dropping them here would make the
		// guidance invisible on the documented bootstrap path.
		Patterns:     l.Patterns,
		Antipatterns: l.Antipatterns,
		// Keep the compact canonical table; agents derive a command's scopes
		// from resource_scopes[resource][verb.access] without the materialized
		// per-command map.
		ResourceScopes: l.ResourceScopes,
		// Plugins are invisible to agents outside this catalog — keep them.
		Plugins: l.Plugins,
	}

	for name, verb := range l.Verbs {
		bv := &Verb{
			Mutating:  verb.Mutating,
			Access:    verb.Access,
			Resources: advertisedResources(name, verb.Resources),
			// Retained for the same reason as the verb's own Stability below.
			ResourceStability: stabilityOfAdvertised(name, verb),
			// DQL scopes are not derivable from resource_scopes, so retain them.
			RequiredScopes: verb.RequiredScopes,
			// Retained for the same reason as mutating status: an agent always
			// needs to know it before acting.
			Stability:  verb.Stability,
			Deprecated: verb.Deprecated,
		}

		bv.Flags = briefFlags(verb.Flags)

		if verb.Subcommands != nil {
			bv.Subcommands = make(map[string]*Verb, len(verb.Subcommands))
			for subName, sub := range verb.Subcommands {
				bv.Subcommands[subName] = newBriefSubcommand(sub)
			}
		}

		brief.Verbs[name] = bv
	}

	return brief
}

// newBriefSubcommand strips a subcommand, and its subcommands at any depth,
// to what --brief keeps: mutating status, access, scopes and flag types.
func newBriefSubcommand(sub *Verb) *Verb {
	bs := &Verb{
		Mutating:          sub.Mutating,
		Access:            sub.Access,
		Resources:         sub.Resources,
		ResourceStability: sub.ResourceStability,
		RequiredScopes:    sub.RequiredScopes,
		Stability:         sub.Stability,
		Deprecated:        sub.Deprecated,
	}
	bs.Flags = briefFlags(sub.Flags)
	if sub.Subcommands != nil {
		bs.Subcommands = make(map[string]*Verb, len(sub.Subcommands))
		for name, nested := range sub.Subcommands {
			bs.Subcommands[name] = newBriefSubcommand(nested)
		}
	}
	return bs
}

// briefFlags strips a flag map to what --brief keeps: the type, whether the
// flag is required, and its stability tier.
//
// Stability survives the strip for the same reason mutating status does: it is
// a constraint on whether the agent may act, not a description it can do
// without. StabilitySince does not — that is history, and history is what
// --full is for.
func briefFlags(flags map[string]*Flag) map[string]*Flag {
	if flags == nil {
		return nil
	}
	out := make(map[string]*Flag, len(flags))
	for k, f := range flags {
		bf := &Flag{Type: f.Type, Stability: f.Stability}
		if f.Required {
			bf.Description = "(required)"
		}
		out[k] = bf
	}
	return out
}

// FilterByResource returns a new listing containing only verbs that operate on
// the given resource name. The name is matched against resources, subcommands,
// and aliases. Returns the filtered listing and true if any verbs matched.
// The original listing is not modified.
func FilterByResource(l *Listing, name string) (*Listing, bool) {
	// Resolve alias
	resolved := name
	if target, ok := ResourceAliases[name]; ok {
		resolved = target
	}

	var filteredVerbs map[string]*Verb

	// Check if it's a verb name first
	if verb, ok := l.Verbs[name]; ok {
		filteredVerbs = map[string]*Verb{name: verb}
	} else {
		// Filter to verbs that contain the resource
		filteredVerbs = make(map[string]*Verb)
		for verbName, verb := range l.Verbs {
			if containsResource(verb, resolved) || containsResource(verb, name) {
				filteredVerbs[verbName] = verb
			}
		}
	}

	if len(filteredVerbs) == 0 {
		return nil, false
	}

	result := &Listing{
		SchemaVersion:  l.SchemaVersion,
		Tool:           l.Tool,
		Version:        l.Version,
		Description:    l.Description,
		CommandModel:   l.CommandModel,
		GlobalFlags:    l.GlobalFlags,
		Verbs:          filteredVerbs,
		Aliases:        l.Aliases,
		ResourceScopes: collectResourceScopes(filteredVerbs),
		TimeFormats:    l.TimeFormats,
		Patterns:       l.Patterns,
		Antipatterns:   l.Antipatterns,
	}
	return result, true
}

// containsResource checks if a verb or its subcommands reference a resource name.
// It handles singular/plural matching by normalizing both the query and each
// resource name to a common stem (stripping trailing "s"). This avoids fragile
// "append s" heuristics that break for irregular plurals.
func containsResource(verb *Verb, name string) bool {
	// Check resources list
	for _, r := range verb.Resources {
		if sameResource(r, name) {
			return true
		}
	}
	// Check subcommands, at any depth — the catalog nests as deeply as the
	// command tree does (`get gcp connections principal`).
	for subName, sub := range verb.Subcommands {
		if sameResource(subName, name) {
			return true
		}
		if containsResource(sub, name) {
			return true
		}
	}
	return false
}

// sameResource reports whether two catalog names denote the same resource,
// normalizing both to a common stem so "workflows" matches "workflow".
func sameResource(a, b string) bool {
	return a == b || singularize(a) == singularize(b)
}

// singularize returns a basic singular form by stripping a trailing "s".
// This is intentionally simple — dtctl resource names follow the convention
// of using "s" for plurals (workflows/workflow, dashboards/dashboard).
func singularize(name string) string {
	if strings.HasSuffix(name, "s") {
		return strings.TrimSuffix(name, "s")
	}
	return name
}

// ResolveAlias resolves a resource alias to its canonical name.
// Returns the original name if no alias exists.
func ResolveAlias(name string) string {
	if target, ok := ResourceAliases[name]; ok {
		return target
	}
	return name
}

// RequiredScopesUnion returns the sorted, de-duplicated union of every scope
// required by the verbs in the listing (per-resource scopes, DQL scopes, and
// subcommand scopes). Combined with FilterByResource it yields the minimal
// token scope set for a filtered command set, e.g. the scopes a CI pipeline
// that only runs `get`/`apply` on workflows needs.
func RequiredScopesUnion(l *Listing) []string {
	seen := map[string]bool{}
	add := func(scopes []string) {
		for _, s := range scopes {
			seen[s] = true
		}
	}
	var walk func(v *Verb)
	walk = func(v *Verb) {
		add(v.RequiredScopes)
		for _, scopes := range v.RequiredScopesByResource {
			add(scopes)
		}
		for _, sub := range v.Subcommands {
			walk(sub)
		}
	}
	for _, v := range l.Verbs {
		walk(v)
	}

	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// RequiredScopesForResource returns the sorted union of scopes needed to run
// every verb in the listing against a single resource — the minimal token scope
// set for a pipeline that only touches that resource. The resource name may be
// an alias or plural; it is resolved against the canonical table.
func RequiredScopesForResource(l *Listing, resource string) []string {
	resolved := ResolveAlias(resource)
	seen := map[string]bool{}
	for _, verb := range l.Verbs {
		if !containsResource(verb, resolved) && !containsResource(verb, resource) {
			continue
		}
		for _, s := range auth.ScopesForResource(resolved, auth.Access(verb.Access)) {
			seen[s] = true
		}
		// A name that matches only below the verb (`create aws connection`) has
		// no entry of its own in the canonical table. Reporting nothing for it
		// would answer "this needs no scopes" about a command that needs the
		// ones its enclosing subcommand declares, so take those instead.
		addNestedScopes(verb, resolved, resource, nil, seen)
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// addNestedScopes records the scopes needed by every subcommand of v, at any
// depth, whose name denotes the wanted resource. A node that declares no scopes
// of its own takes those of its nearest enclosing node that does: `create aws
// connection` is reached through `create aws`, and that is where the
// hyperscaler scopes are declared.
func addNestedScopes(v *Verb, resolved, raw string, inherited []string, seen map[string]bool) {
	for name, sub := range v.Subcommands {
		scopes := inherited
		if len(sub.RequiredScopes) > 0 {
			scopes = sub.RequiredScopes
		}
		if sameResource(name, resolved) || sameResource(name, raw) {
			for _, s := range scopes {
				seen[s] = true
			}
		}
		addNestedScopes(sub, resolved, raw, scopes, seen)
	}
}

// WriteTo writes the listing to w in the given format ("json", "yaml"/"yml", or "toon").
// Any other format value defaults to JSON.
func WriteTo(w io.Writer, l *Listing, format string) error {
	return WriteValue(w, l, format)
}

// WriteValue serializes any value to w in the given format ("json", "yaml"/"yml",
// or "toon"). Any other format value defaults to JSON. It is used to emit the
// minimal/brief/full catalog variants, which are distinct types.
func WriteValue(w io.Writer, v any, format string) error {
	switch format {
	case "yaml", "yml":
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(v); err != nil {
			return err
		}
		return enc.Close()
	case "toon":
		// Round-trip through JSON to get a generic representation that
		// respects json struct tags, then encode as TOON.
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var generic any
		if err := json.Unmarshal(b, &generic); err != nil {
			return err
		}
		data, err := toon.Marshal(generic)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
}

// annotateStability copies a command's effective stability tier and deprecation
// note onto its catalog entry. The default tier is left empty rather than
// written out as "stable": a machine consumer should be able to treat the
// presence of the key as "read the guarantee before automating this".
func annotateStability(verb *Verb, cmd *cobra.Command) {
	if lvl := stability.Effective(cmd); lvl != stability.Stable {
		verb.Stability = string(lvl)
		verb.StabilitySince = stability.Since(cmd)
	}
	if d, ok := stability.DeprecationOf(cmd); ok {
		verb.Deprecated = d.Note()
	}
}
