package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// newTestRoot creates a minimal Cobra command tree for testing.
func newTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "dtctl"}
	root.PersistentFlags().StringP("output", "o", "table", "output format")
	root.PersistentFlags().BoolP("agent", "A", false, "agent mode")
	root.PersistentFlags().Bool("dry-run", false, "preview")

	// get verb with resources
	get := &cobra.Command{Use: "get", Short: "List resources"}
	get.AddCommand(&cobra.Command{Use: "workflows", Short: "List workflows", Aliases: []string{"wf"}, Run: run})
	get.AddCommand(&cobra.Command{Use: "dashboards", Short: "List dashboards", Aliases: []string{"dash", "db"}, Run: run})
	get.AddCommand(&cobra.Command{Use: "notebooks", Short: "List notebooks", Aliases: []string{"nb"}, Run: run})
	root.AddCommand(get)

	// describe verb
	describe := &cobra.Command{Use: "describe", Short: "Show details"}
	describe.AddCommand(&cobra.Command{Use: "workflow", Short: "Describe workflow", Run: run})
	describe.AddCommand(&cobra.Command{Use: "dashboard", Short: "Describe dashboard", Run: run})
	root.AddCommand(describe)

	// apply verb (mutating, with flags)
	apply := &cobra.Command{Use: "apply", Short: "Apply configuration", Run: run}
	apply.Flags().StringP("file", "f", "", "YAML/JSON file path")
	_ = apply.MarkFlagRequired("file")
	apply.Flags().StringArray("set", nil, "Template variables")
	apply.Flags().Bool("show-diff", false, "Show diff")
	root.AddCommand(apply)

	// delete verb (mutating, with resources)
	del := &cobra.Command{Use: "delete", Short: "Delete resources"}
	del.AddCommand(&cobra.Command{Use: "workflow", Short: "Delete a workflow", Run: run})
	del.AddCommand(&cobra.Command{Use: "dashboard", Short: "Delete a dashboard", Run: run})
	root.AddCommand(del)

	// exec verb (mutating, with nested subcommands)
	exec := &cobra.Command{Use: "exec", Short: "Execute commands"}
	exec.AddCommand(&cobra.Command{Use: "workflow", Short: "Run a workflow", Run: run})
	copilot := &cobra.Command{Use: "copilot", Short: "Chat with copilot"}
	copilot.AddCommand(&cobra.Command{Use: "nl2dql", Short: "NL to DQL", Run: run})
	copilot.AddCommand(&cobra.Command{Use: "dql2nl", Short: "DQL to NL", Run: run})
	exec.AddCommand(copilot)
	root.AddCommand(exec)

	// doctor (read-only, no resources)
	root.AddCommand(&cobra.Command{Use: "doctor", Short: "Health check", Run: run})

	// commands (should be excluded)
	root.AddCommand(&cobra.Command{Use: "commands", Short: "List commands"})

	// version (should be excluded)
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print version"})

	// completion (should be excluded)
	root.AddCommand(&cobra.Command{Use: "completion", Short: "Shell completions"})

	// help is auto-added by Cobra

	return root
}

func TestBuild_SchemaVersion(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	require.Equal(t, SchemaVersion, listing.SchemaVersion)
	require.Equal(t, "dtctl", listing.Tool)
	require.Equal(t, "verb-noun", listing.CommandModel)
}

func TestBuildVerbs_MatchesBuild(t *testing.T) {
	root := newTestRoot()
	require.Equal(t, Build(root).Verbs, BuildVerbs(root))
}

func TestBuild_RequiredScopes(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	// Access is derived per verb.
	require.Equal(t, "read", listing.Verbs["get"].Access)
	require.Equal(t, "delete", listing.Verbs["delete"].Access)
	require.Equal(t, "run", listing.Verbs["exec"].Access)

	// Per-(verb,resource) scopes are materialized in full mode.
	getScopes := listing.Verbs["get"].RequiredScopesByResource
	require.Equal(t, []string{"automation:workflows:read"}, getScopes["workflows"])
	require.Equal(t, []string{"document:documents:read"}, getScopes["dashboards"])

	// delete documents uses the distinct :delete scope; workflows fall back to write.
	delScopes := listing.Verbs["delete"].RequiredScopesByResource
	require.Equal(t, []string{"document:documents:delete"}, delScopes["dashboard"])
	require.Equal(t, []string{"automation:workflows:write"}, delScopes["workflow"])

	// exec uses run scopes.
	require.Equal(t, []string{"automation:workflows:run"}, listing.Verbs["exec"].RequiredScopesByResource["workflow"])

	// Top-level canonical table is populated for referenced resources.
	require.Contains(t, listing.ResourceScopes, "workflow")
	require.Equal(t, []string{"automation:workflows:read"}, listing.ResourceScopes["workflow"].Read)
}

func TestNewBrief_ScopesCompact(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)
	brief := NewBrief(listing)

	// Brief keeps access + the canonical table, drops the materialized map.
	require.Equal(t, "delete", brief.Verbs["delete"].Access)
	require.Nil(t, brief.Verbs["delete"].RequiredScopesByResource)
	require.NotEmpty(t, brief.ResourceScopes)
	require.Contains(t, brief.ResourceScopes, "workflow")
}

func TestRequiredScopesUnionAndForResource(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	// Verb-filtered union: delete only yields delete-access scopes.
	delOnly, _ := FilterByResource(listing, "delete")
	require.ElementsMatch(t,
		[]string{"document:documents:delete", "automation:workflows:write"},
		RequiredScopesUnion(delOnly),
	)

	// Resource-narrowed union: workflows across all verbs in the test tree
	// (get=read, describe=read, delete=write, exec=run).
	require.ElementsMatch(t,
		[]string{"automation:workflows:read", "automation:workflows:write", "automation:workflows:run"},
		RequiredScopesForResource(listing, "wf"),
	)
}

func TestBuild_GlobalFlags(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	require.NotNil(t, listing.GlobalFlags)
	require.Contains(t, listing.GlobalFlags, "--output")
	require.Contains(t, listing.GlobalFlags, "--agent")
	require.Contains(t, listing.GlobalFlags, "--dry-run")

	outputFlag := listing.GlobalFlags["--output"]
	require.Equal(t, "string", outputFlag.Type)
	require.Equal(t, "table", outputFlag.Default)
}

func TestBuild_HiddenCommands(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	require.NotContains(t, listing.Verbs, "commands", "commands should be excluded from listing")
	require.NotContains(t, listing.Verbs, "version", "version should be excluded from listing")
	require.NotContains(t, listing.Verbs, "completion", "completion should be excluded from listing")
	require.NotContains(t, listing.Verbs, "help", "help should be excluded from listing")
}

func TestBuild_VerbsPresent(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	expectedVerbs := []string{"get", "describe", "apply", "delete", "exec", "doctor"}
	for _, v := range expectedVerbs {
		require.Contains(t, listing.Verbs, v, "verb %q should be present", v)
	}
}

func TestBuild_MutatingVerbs(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	tests := []struct {
		verb     string
		mutating bool
		safetyOp string
	}{
		{"get", false, ""},
		{"describe", false, ""},
		{"doctor", false, ""},
		{"apply", true, "OperationCreate"},
		{"delete", true, "OperationDelete"},
		{"exec", true, "OperationCreate"},
	}

	for _, tt := range tests {
		t.Run(tt.verb, func(t *testing.T) {
			verb, ok := listing.Verbs[tt.verb]
			require.True(t, ok, "verb %q should exist", tt.verb)
			require.Equal(t, tt.mutating, verb.Mutating, "verb %q mutating", tt.verb)
			require.Equal(t, tt.safetyOp, verb.SafetyOp, "verb %q safety_operation", tt.verb)
		})
	}
}

func TestBuild_Resources(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	getVerb := listing.Verbs["get"]
	require.ElementsMatch(t, []string{"workflows", "dashboards", "notebooks"}, getVerb.Resources)

	deleteVerb := listing.Verbs["delete"]
	require.ElementsMatch(t, []string{"workflow", "dashboard"}, deleteVerb.Resources)
}

func TestBuild_VerbFlags(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	applyVerb := listing.Verbs["apply"]
	require.NotNil(t, applyVerb.Flags)
	require.Contains(t, applyVerb.Flags, "-f/--file")
	require.Contains(t, applyVerb.Flags, "--show-diff")
	require.Contains(t, applyVerb.Flags, "--set")

	fileFlag := applyVerb.Flags["-f/--file"]
	require.Equal(t, "string", fileFlag.Type)
}

// A flag's tier is carried only when it is weaker than stable; declaring a flag
// stable (stability.MarkFlagStable) must not add a field the catalog documents
// as omitted for stable.
func TestBuild_FlagStabilityOmitsStable(t *testing.T) {
	root := newTestRoot()
	apply, _, err := root.Find([]string{"apply"})
	require.NoError(t, err)
	stability.MarkFlagStable(apply, "set")
	stability.MarkFlag(apply, "show-diff", stability.Experimental, "0.38.0")

	flags := Build(root).Verbs["apply"].Flags
	require.Empty(t, flags["--set"].Stability)
	require.Empty(t, flags["--set"].StabilitySince)
	require.Equal(t, "experimental", flags["--show-diff"].Stability)
	require.Equal(t, "0.38.0", flags["--show-diff"].StabilitySince)
}

func TestBuild_NestedSubcommands(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	execVerb := listing.Verbs["exec"]
	require.Contains(t, execVerb.Resources, "workflow")
	require.NotNil(t, execVerb.Subcommands)
	require.Contains(t, execVerb.Subcommands, "copilot")

	copilot := execVerb.Subcommands["copilot"]
	require.NotNil(t, copilot.Subcommands)
	require.Contains(t, copilot.Subcommands, "nl2dql")
	require.Contains(t, copilot.Subcommands, "dql2nl")
}

func TestBuild_ResourceAliases(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	require.NotNil(t, listing.Aliases)
	require.Equal(t, "workflows", listing.Aliases["wf"])
	require.Equal(t, "dashboards", listing.Aliases["dash"])
	require.Equal(t, "dashboards", listing.Aliases["db"])
	require.Equal(t, "notebooks", listing.Aliases["nb"])
}

func TestBuild_TimeFormats(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	require.NotNil(t, listing.TimeFormats)
	require.NotEmpty(t, listing.TimeFormats.Relative)
	require.NotEmpty(t, listing.TimeFormats.Absolute)
	require.NotEmpty(t, listing.TimeFormats.Unix)
}

func TestBuild_PatternsAndAntipatterns(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	require.NotEmpty(t, listing.Patterns)
	require.NotEmpty(t, listing.Antipatterns)
}

func TestNewBrief_FullBehavior(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	brief := NewBrief(listing)

	// Stripped fields
	require.Empty(t, brief.Description)
	require.Nil(t, brief.GlobalFlags)
	require.Nil(t, brief.TimeFormats)

	// Preserved fields
	require.Equal(t, listing.Patterns, brief.Patterns)
	require.Equal(t, listing.Antipatterns, brief.Antipatterns)
	require.Equal(t, SchemaVersion, brief.SchemaVersion)
	require.Equal(t, "dtctl", brief.Tool)
	require.NotEmpty(t, brief.Verbs)
	require.NotNil(t, brief.Aliases)

	// Verb descriptions stripped but mutating preserved
	applyVerb := brief.Verbs["apply"]
	require.Empty(t, applyVerb.Description)
	require.True(t, applyVerb.Mutating, "mutating should be preserved in brief mode")
	require.Empty(t, applyVerb.SafetyOp, "safety_operation should be stripped in brief mode")

	getVerb := brief.Verbs["get"]
	require.False(t, getVerb.Mutating, "read-only verb should remain false")
}

func TestFilterByResource(t *testing.T) {
	tests := []struct {
		name        string
		filter      string
		expectMatch bool
		expectVerbs []string
	}{
		{
			name:        "filter by resource name",
			filter:      "workflows",
			expectMatch: true,
			// Matches "workflows" in get, and "workflow" (singular match) in describe/delete/exec
			expectVerbs: []string{"get", "describe", "delete", "exec"},
		},
		{
			name:        "filter by singular resource",
			filter:      "workflow",
			expectMatch: true,
			// Matches "workflow" in describe/delete/exec, and "workflows" (plural match) in get
			expectVerbs: []string{"get", "delete", "describe", "exec"},
		},
		{
			name:        "filter by alias",
			filter:      "wf",
			expectMatch: true,
			// Alias resolves to "workflows", same result as filtering by "workflows"
			expectVerbs: []string{"get", "describe", "delete", "exec"},
		},
		{
			name:        "filter by verb name",
			filter:      "get",
			expectMatch: true,
			expectVerbs: []string{"get"},
		},
		{
			name:        "filter by nonexistent resource",
			filter:      "nonexistent",
			expectMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRoot()
			listing := Build(root)

			filtered, matched := FilterByResource(listing, tt.filter)
			require.Equal(t, tt.expectMatch, matched)

			if tt.expectMatch {
				var verbNames []string
				for name := range filtered.Verbs {
					verbNames = append(verbNames, name)
				}
				require.ElementsMatch(t, tt.expectVerbs, verbNames)
			}
		})
	}
}

func TestResolveAlias(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"wf", "workflows"},
		{"dash", "dashboards"},
		{"db", "dashboards"},
		{"nb", "notebooks"},
		{"bkt", "buckets"},
		{"ec", "edgeconnect"},
		{"fn", "functions"},
		{"func", "functions"},
		{"workflows", "workflows"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			require.Equal(t, tt.expected, ResolveAlias(tt.input))
		})
	}
}

func TestFlagTypeName(t *testing.T) {
	root := &cobra.Command{Use: "test"}
	root.Flags().String("s", "", "string flag")
	root.Flags().Bool("b", false, "bool flag")
	root.Flags().Int("i", 0, "int flag")
	root.Flags().StringSlice("ss", nil, "string slice flag")
	root.Flags().Duration("d", 0, "duration flag")

	tests := []struct {
		flagName string
		expected string
	}{
		{"s", "string"},
		{"b", "boolean"},
		{"i", "integer"},
		{"ss", "string[]"},
		{"d", "duration"},
	}

	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			f := root.Flags().Lookup(tt.flagName)
			require.NotNil(t, f)
			require.Equal(t, tt.expected, flagTypeName(f))
		})
	}
}

func TestParseRequiredArgs(t *testing.T) {
	tests := []struct {
		use      string
		expected []string
	}{
		{"apply -f <file>", []string{"file"}},
		{"describe <resource> <id>", []string{"resource", "id"}},
		{"get", nil},
		{"query [dql-string]", nil}, // brackets, not angles
	}

	for _, tt := range tests {
		t.Run(tt.use, func(t *testing.T) {
			result := parseRequiredArgs(tt.use)
			if tt.expected == nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestBuild_HiddenCobraCommand(t *testing.T) {
	root := &cobra.Command{Use: "dtctl"}
	root.AddCommand(&cobra.Command{Use: "visible", Short: "Visible", Run: run})
	hidden := &cobra.Command{Use: "secret", Short: "Secret", Hidden: true}
	root.AddCommand(hidden)

	listing := Build(root)
	require.Contains(t, listing.Verbs, "visible")
	require.NotContains(t, listing.Verbs, "secret")
}

// --- NewBrief tests ---

func TestNewBrief_DoesNotMutateOriginal(t *testing.T) {
	root := newTestRoot()
	original := Build(root)

	// Capture original values
	origDesc := original.Description
	origGlobalFlagCount := len(original.GlobalFlags)
	origPatternCount := len(original.Patterns)
	origApplyDesc := original.Verbs["apply"].Description

	brief := NewBrief(original)

	// Original should be unchanged
	require.Equal(t, origDesc, original.Description, "original Description should be unchanged")
	require.Len(t, original.GlobalFlags, origGlobalFlagCount, "original GlobalFlags should be unchanged")
	require.Len(t, original.Patterns, origPatternCount, "original Patterns should be unchanged")
	require.Equal(t, origApplyDesc, original.Verbs["apply"].Description, "original verb description should be unchanged")

	// Brief should have stripped verbose fields
	require.Empty(t, brief.Description)
	require.Nil(t, brief.GlobalFlags)
	require.Nil(t, brief.TimeFormats)

	// ...but retain patterns/antipatterns: they are the primary agent grounding
	// and are cheap, so brief mode keeps them.
	require.Equal(t, original.Patterns, brief.Patterns, "brief should retain patterns")
	require.Equal(t, original.Antipatterns, brief.Antipatterns, "brief should retain antipatterns")
}

// TestNewBrief_RetainsSmartscapePatterns guards the bootstrap path: agents
// learn Smartscape DQL from the patterns/antipatterns arrays, and the documented
// bootstrap command is `dtctl commands --brief -o json`. If brief mode ever drops
// these arrays again, the guidance becomes invisible to agents.
func TestNewBrief_RetainsSmartscapePatterns(t *testing.T) {
	brief := NewBrief(Build(newTestRoot()))

	joined := strings.Join(append(append([]string{}, brief.Patterns...), brief.Antipatterns...), "\n")
	require.Contains(t, joined, "smartscapeNodes", "brief output must surface Smartscape guidance")
	require.Contains(t, joined, `dedup type`, "brief output must surface the node-type discovery query")
}

func TestNewBrief_PreservesMutatingStatus(t *testing.T) {
	root := newTestRoot()
	original := Build(root)
	brief := NewBrief(original)

	// Mutating verbs
	require.True(t, brief.Verbs["apply"].Mutating, "apply should be mutating in brief")
	require.True(t, brief.Verbs["delete"].Mutating, "delete should be mutating in brief")
	require.True(t, brief.Verbs["exec"].Mutating, "exec should be mutating in brief")

	// Non-mutating verbs
	require.False(t, brief.Verbs["get"].Mutating, "get should not be mutating in brief")
	require.False(t, brief.Verbs["describe"].Mutating, "describe should not be mutating in brief")
	require.False(t, brief.Verbs["doctor"].Mutating, "doctor should not be mutating in brief")
}

func TestNewBrief_PreservesResources(t *testing.T) {
	root := newTestRoot()
	original := Build(root)
	brief := NewBrief(original)

	require.ElementsMatch(t,
		original.Verbs["get"].Resources,
		brief.Verbs["get"].Resources,
		"resources should be preserved in brief")
}

func TestNewBrief_PreservesAliases(t *testing.T) {
	root := newTestRoot()
	original := Build(root)
	brief := NewBrief(original)

	require.Equal(t, original.Aliases, brief.Aliases, "aliases should be preserved in brief")
}

func TestNewBrief_StripsVerbDescriptions(t *testing.T) {
	root := newTestRoot()
	original := Build(root)
	brief := NewBrief(original)

	for name, verb := range brief.Verbs {
		require.Empty(t, verb.Description, "verb %q description should be empty in brief", name)
		require.Empty(t, verb.SafetyOp, "verb %q safety_operation should be empty in brief", name)
	}
}

func TestNewBrief_SimplifiesFlags(t *testing.T) {
	root := newTestRoot()
	original := Build(root)
	brief := NewBrief(original)

	applyVerb := brief.Verbs["apply"]
	require.NotNil(t, applyVerb.Flags)

	// File flag is required — should have "(required)" as description
	for _, f := range applyVerb.Flags {
		require.Empty(t, f.Default, "defaults should be stripped in brief flags")
	}

	// Check that required flags get special description
	fileFlag := applyVerb.Flags["-f/--file"]
	require.NotNil(t, fileFlag)
	require.Equal(t, "(required)", fileFlag.Description, "required flags should be marked")
	require.Equal(t, "string", fileFlag.Type, "type should be preserved")
}

func TestNewBrief_PreservesSubcommands(t *testing.T) {
	root := newTestRoot()
	original := Build(root)
	brief := NewBrief(original)

	execBrief := brief.Verbs["exec"]
	require.NotNil(t, execBrief.Subcommands, "exec subcommands should be preserved in brief")
	require.Contains(t, execBrief.Subcommands, "copilot")

	copilotBrief := execBrief.Subcommands["copilot"]
	require.NotNil(t, copilotBrief.Subcommands)
	require.Contains(t, copilotBrief.Subcommands, "nl2dql")
	require.Contains(t, copilotBrief.Subcommands, "dql2nl")
}

func TestNewBrief_SmallerThanFull(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	fullData, err := json.Marshal(listing)
	require.NoError(t, err)

	brief := NewBrief(listing)
	briefData, err := json.Marshal(brief)
	require.NoError(t, err)

	require.Less(t, len(briefData), len(fullData),
		"brief output (%d bytes) should be smaller than full (%d bytes)",
		len(briefData), len(fullData))
}

// --- singularize tests ---

func TestSingularize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"workflows", "workflow"},
		{"dashboards", "dashboard"},
		{"notebooks", "notebook"},
		{"buckets", "bucket"},
		{"slos", "slo"},
		{"workflow", "workflow"},   // already singular
		{"dashboard", "dashboard"}, // already singular
		{"edgeconnect", "edgeconnect"},
		{"settings", "setting"},
		{"", ""},            // empty string
		{"s", ""},           // just "s"
		{"ss", "s"},         // double s
		{"status", "statu"}, // not perfect, but consistent
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			require.Equal(t, tt.expected, singularize(tt.input))
		})
	}
}

func TestContainsResource_SingularPluralMatching(t *testing.T) {
	verb := &Verb{
		Resources: []string{"workflows", "dashboards"},
	}

	// Should match both singular and plural
	require.True(t, containsResource(verb, "workflows"), "exact match on plural")
	require.True(t, containsResource(verb, "workflow"), "singular matches plural resource")
	require.True(t, containsResource(verb, "dashboards"), "exact match on plural")
	require.True(t, containsResource(verb, "dashboard"), "singular matches plural resource")

	// Should not match unrelated
	require.False(t, containsResource(verb, "notebooks"))
	require.False(t, containsResource(verb, "unknown"))
}

func TestContainsResource_Subcommands(t *testing.T) {
	verb := &Verb{
		Subcommands: map[string]*Verb{
			"copilot": {Description: "Chat"},
		},
	}

	require.True(t, containsResource(verb, "copilot"))
	require.False(t, containsResource(verb, "unknown"))
}

// --- WriteTo tests ---

func TestWriteTo_JSON(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	var buf bytes.Buffer
	err := WriteTo(&buf, listing, "json")
	require.NoError(t, err)

	output := buf.Bytes()
	require.True(t, json.Valid(output), "WriteTo JSON should produce valid JSON")

	// Decode and verify key fields
	var decoded Listing
	err = json.Unmarshal(output, &decoded)
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, decoded.SchemaVersion)
	require.Equal(t, "dtctl", decoded.Tool)
	require.NotEmpty(t, decoded.Verbs)
}

func TestWriteTo_YAML(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	var buf bytes.Buffer
	err := WriteTo(&buf, listing, "yaml")
	require.NoError(t, err)

	output := buf.String()
	require.NotEmpty(t, output)

	// Should be valid YAML
	var decoded Listing
	err = yaml.Unmarshal(buf.Bytes(), &decoded)
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, decoded.SchemaVersion)
	require.Equal(t, "dtctl", decoded.Tool)
	require.NotEmpty(t, decoded.Verbs)
}

func TestWriteTo_YML(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	var buf bytes.Buffer
	err := WriteTo(&buf, listing, "yml")
	require.NoError(t, err)

	// Should produce identical output to "yaml"
	var buf2 bytes.Buffer
	err = WriteTo(&buf2, listing, "yaml")
	require.NoError(t, err)

	require.Equal(t, buf.String(), buf2.String(), "yml and yaml should produce identical output")
}

func TestWriteTo_DefaultIsJSON(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	var jsonBuf bytes.Buffer
	err := WriteTo(&jsonBuf, listing, "json")
	require.NoError(t, err)

	var defaultBuf bytes.Buffer
	err = WriteTo(&defaultBuf, listing, "table") // non-json/yaml defaults to JSON
	require.NoError(t, err)

	require.Equal(t, jsonBuf.String(), defaultBuf.String(),
		"unknown format should default to JSON")
}

func TestWriteTo_JSONIsPrettyPrinted(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	var buf bytes.Buffer
	err := WriteTo(&buf, listing, "json")
	require.NoError(t, err)

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	require.Greater(t, len(lines), 1, "JSON output should be multi-line (pretty-printed)")

	// Check indentation
	indented := false
	for _, line := range lines {
		if strings.HasPrefix(line, "  ") {
			indented = true
			break
		}
	}
	require.True(t, indented, "JSON should use 2-space indentation")
}

func TestWriteTo_YAMLIsPrettyPrinted(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	var buf bytes.Buffer
	err := WriteTo(&buf, listing, "yaml")
	require.NoError(t, err)

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	require.Greater(t, len(lines), 1, "YAML output should be multi-line")

	// Check it uses 2-space indentation (set by enc.SetIndent(2))
	indented := false
	for _, line := range lines {
		if strings.HasPrefix(line, "  ") {
			indented = true
			break
		}
	}
	require.True(t, indented, "YAML should use 2-space indentation")
}

// --- Golden/snapshot tests ---

func TestBuild_JSONRoundTrip(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	// Marshal to JSON
	data, err := json.Marshal(listing)
	require.NoError(t, err)

	// Unmarshal back
	var decoded Listing
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Key structural checks
	require.Equal(t, listing.SchemaVersion, decoded.SchemaVersion)
	require.Equal(t, listing.Tool, decoded.Tool)
	require.Equal(t, listing.CommandModel, decoded.CommandModel)
	require.Len(t, decoded.Verbs, len(listing.Verbs))

	for name, verb := range listing.Verbs {
		decodedVerb, ok := decoded.Verbs[name]
		require.True(t, ok, "verb %q should survive round trip", name)
		require.Equal(t, verb.Mutating, decodedVerb.Mutating, "verb %q mutating", name)
		require.ElementsMatch(t, verb.Resources, decodedVerb.Resources, "verb %q resources", name)
	}
}

func TestBuild_YAMLRoundTrip(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	// Marshal to YAML via WriteTo
	var buf bytes.Buffer
	err := WriteTo(&buf, listing, "yaml")
	require.NoError(t, err)

	// Unmarshal back
	var decoded Listing
	err = yaml.Unmarshal(buf.Bytes(), &decoded)
	require.NoError(t, err)

	require.Equal(t, listing.SchemaVersion, decoded.SchemaVersion)
	require.Equal(t, listing.Tool, decoded.Tool)
	require.Len(t, decoded.Verbs, len(listing.Verbs))

	for name, verb := range listing.Verbs {
		decodedVerb, ok := decoded.Verbs[name]
		require.True(t, ok, "verb %q should survive YAML round trip", name)
		require.Equal(t, verb.Mutating, decodedVerb.Mutating, "verb %q mutating", name)
	}
}

func TestBuild_BriefJSONRoundTrip(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)
	brief := NewBrief(listing)

	data, err := json.Marshal(brief)
	require.NoError(t, err)

	var decoded Listing
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Brief-specific checks
	require.Empty(t, decoded.Description)
	require.Nil(t, decoded.GlobalFlags)
	require.Nil(t, decoded.TimeFormats)
	require.Equal(t, listing.Patterns, decoded.Patterns)
	require.Equal(t, listing.Antipatterns, decoded.Antipatterns)

	for name, verb := range brief.Verbs {
		decodedVerb := decoded.Verbs[name]
		require.Equal(t, verb.Mutating, decodedVerb.Mutating)
		require.ElementsMatch(t, verb.Resources, decodedVerb.Resources)
	}
}

// --- Error path tests ---

// errWriter always returns an error on Write.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errTestWrite
}

var errTestWrite = fmt.Errorf("simulated write error")

func TestWriteTo_JSONWriteError(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	err := WriteTo(errWriter{}, listing, "json")
	require.Error(t, err)
}

func TestWriteTo_YAMLWriteError(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	err := WriteTo(errWriter{}, listing, "yaml")
	require.Error(t, err)
}

// --- NewMinimal tests ---

func TestNewMinimal_KeepsStructureOnly(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)
	m := NewMinimal(listing)

	// Header fields carried over.
	require.Equal(t, listing.SchemaVersion, m.SchemaVersion)
	require.Equal(t, "dtctl", m.Tool)
	require.Equal(t, "verb-noun", m.CommandModel)
	require.Equal(t, listing.Aliases, m.Aliases)

	// Every verb is present with just resources + subcommands.
	require.Len(t, m.Verbs, len(listing.Verbs))
	get := m.Verbs["get"]
	require.NotNil(t, get)
	require.ElementsMatch(t, listing.Verbs["get"].Resources, get.Resources)

	// Nested subcommands survive.
	exec := m.Verbs["exec"]
	require.NotNil(t, exec)
	require.Contains(t, exec.Subcommands, "copilot")
	require.Contains(t, exec.Subcommands["copilot"].Subcommands, "nl2dql")
}

func TestNewMinimal_DoesNotMutateOriginal(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	origVerbCount := len(listing.Verbs)
	origDesc := listing.Verbs["get"].Description
	origGlobalFlags := len(listing.GlobalFlags)

	_ = NewMinimal(listing)

	require.Len(t, listing.Verbs, origVerbCount)
	require.Equal(t, origDesc, listing.Verbs["get"].Description)
	require.Len(t, listing.GlobalFlags, origGlobalFlags)
	require.NotNil(t, listing.TimeFormats)
}

func TestNewMinimal_SmallerThanBrief(t *testing.T) {
	root := newTestRoot()
	listing := Build(root)

	briefData, err := json.Marshal(NewBrief(listing))
	require.NoError(t, err)
	minimalData, err := json.Marshal(NewMinimal(listing))
	require.NoError(t, err)

	require.Less(t, len(minimalData), len(briefData),
		"minimal output (%d bytes) should be smaller than brief (%d bytes)",
		len(minimalData), len(briefData))
}

func TestNewMinimal_JSONRoundTrip(t *testing.T) {
	root := newTestRoot()
	m := NewMinimal(Build(root))

	var buf bytes.Buffer
	require.NoError(t, WriteValue(&buf, m, "json"))
	require.True(t, json.Valid(buf.Bytes()))

	var decoded Minimal
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	require.Equal(t, "dtctl", decoded.Tool)
	require.NotEmpty(t, decoded.Verbs)
}

// --- WriteValue tests ---

func TestWriteValue_TOON(t *testing.T) {
	root := newTestRoot()
	m := NewMinimal(Build(root))

	var buf bytes.Buffer
	require.NoError(t, WriteValue(&buf, m, "toon"))
	require.NotEmpty(t, buf.String())
	require.Contains(t, buf.String(), "tool: dtctl")
}

func TestWriteValue_DefaultIsJSON(t *testing.T) {
	root := newTestRoot()
	m := NewMinimal(Build(root))

	var jsonBuf, defaultBuf bytes.Buffer
	require.NoError(t, WriteValue(&jsonBuf, m, "json"))
	require.NoError(t, WriteValue(&defaultBuf, m, "table"))
	require.Equal(t, jsonBuf.String(), defaultBuf.String(),
		"unknown format should default to JSON")
}
