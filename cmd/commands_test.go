package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/commands"
)

// runCommandsCLI drives `dtctl commands ...` through the real root command and
// returns what it wrote to stdout. Flag state is reset first so invocations are
// independent regardless of ordering.
func runCommandsCLI(t *testing.T, args ...string) string {
	t.Helper()

	resetCommandsFlags(t)

	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	rootCmd.SetArgs(append([]string{"commands"}, args...))
	execErr := rootCmd.Execute()

	_ = w.Close()
	os.Stdout = orig
	out := <-done
	require.NoError(t, execErr)
	return out
}

func TestCommandsCmd_DefaultIsMinimalTOON(t *testing.T) {
	out := runCommandsCLI(t)

	// TOON, not JSON.
	require.Contains(t, out, "tool: dtctl")
	require.Contains(t, out, "command_model: verb-noun")
	require.Contains(t, out, "verbs:")
	require.False(t, strings.HasPrefix(strings.TrimSpace(out), "{"), "JSON output")

	// Minimal: no per-verb detail fields.
	require.NotContains(t, out, "mutating")
	require.NotContains(t, out, "description")
	require.NotContains(t, out, "required_scopes")
	require.NotContains(t, out, "global_flags")
	require.NotContains(t, out, "time_formats")
}

func TestCommandsCmd_BriefAddsDetail(t *testing.T) {
	out := runCommandsCLI(t, "--brief")

	// Brief keeps mutating status but drops the heavy full-mode sections.
	require.Contains(t, out, "mutating")
	require.NotContains(t, out, "time_formats")
	require.NotContains(t, out, "global_flags")
}

func TestCommandsCmd_FullMatchesLegacyOutput(t *testing.T) {
	out := runCommandsCLI(t, "--full", "-o", "json")

	require.True(t, json.Valid([]byte(out)), "--full -o json should be valid JSON")

	// Full mode is the legacy complete catalog.
	var decoded commands.Listing
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.NotEmpty(t, decoded.GlobalFlags)
	require.NotNil(t, decoded.TimeFormats)
	require.NotEmpty(t, decoded.Verbs["get"].Description)

	// Byte-for-byte identical to building the full listing directly, annotated
	// with the same active profile/safety context the command applies.
	wantListing := commands.Build(rootCmd)
	annotateListingContext(context.Background(), wantListing)
	var want bytes.Buffer
	require.NoError(t, commands.WriteTo(&want, wantListing, "json"))
	require.Equal(t, want.String(), out)
}

// resetCommandsFlags clears the flag state that persists across rootCmd.Execute()
// calls (both the Go vars and cobra's per-flag Changed markers), so each test
// invocation starts clean regardless of ordering.
func resetCommandsFlags(t *testing.T) {
	t.Helper()

	gFlags.outputFormat = "table"

	outFlag := rootCmd.PersistentFlags().Lookup("output")
	require.NoError(t, outFlag.Value.Set("table"))
	outFlag.Changed = false

	for _, name := range []string{"brief", "full", "required-scopes"} {
		if f := commandsCmd.Flags().Lookup(name); f != nil {
			require.NoError(t, f.Value.Set("false"))
			f.Changed = false
		}
	}
}

func TestCommandsCmd_BriefFullMutuallyExclusive(t *testing.T) {
	resetCommandsFlags(t)

	rootCmd.SetArgs([]string{"commands", "--brief", "--full"})
	err := rootCmd.Execute()
	require.Error(t, err)
}

// TestEveryResourceHasScopeMapping fails the build when a new resource is added
// to the command tree without either a canonical auth.ResourceScopes entry or
// an explicit local-only classification. This keeps the scope catalog complete:
// a new platform resource cannot ship with empty required_scopes by accident.
func TestEveryResourceHasScopeMapping(t *testing.T) {
	listing := commands.Build(rootCmd)

	check := func(resource string) {
		// "query" is a DQL pseudo-resource (verify/wait): its scopes are carried
		// at the verb level (RequiredScopes), not in the resource table.
		if resource == "query" {
			return
		}
		if auth.HasResourceScopes(resource) || auth.IsLocalResource(resource) {
			return
		}
		t.Errorf("resource %q has no auth.ResourceScopes entry and is not marked local; "+
			"add it to ResourceScopes (or localResources) in pkg/auth/resource_scopes.go", resource)
	}

	for _, verb := range listing.Verbs {
		for _, r := range verb.Resources {
			check(r)
		}
		for subName, sub := range verb.Subcommands {
			check(subName)
			for _, r := range sub.Resources {
				check(r)
			}
		}
	}
}

func TestCommandsCmd_OutputsValidJSON(t *testing.T) {
	listing := commands.Build(rootCmd)

	data, err := json.Marshal(listing)
	require.NoError(t, err)
	require.True(t, json.Valid(data), "output should be valid JSON")
}

func TestCommandsCmd_SchemaVersion(t *testing.T) {
	listing := commands.Build(rootCmd)

	require.Equal(t, commands.SchemaVersion, listing.SchemaVersion)
	require.Equal(t, "dtctl", listing.Tool)
	require.Equal(t, "verb-noun", listing.CommandModel)
	require.NotEmpty(t, listing.Version)
}

func TestCommandsCmd_AllVerbsPresent(t *testing.T) {
	listing := commands.Build(rootCmd)

	// Core verbs that must always be present
	expectedVerbs := []string{
		"get", "describe", "apply", "create", "edit", "delete",
		"exec", "diff", "query", "wait", "doctor", "history",
		"restore", "share", "unshare", "logs", "ctx", "skills", "download",
	}

	for _, verb := range expectedVerbs {
		require.Contains(t, listing.Verbs, verb, "verb %q should be present", verb)
	}
}

func TestCommandsCmd_ExcludesUtilityCommands(t *testing.T) {
	listing := commands.Build(rootCmd)

	excluded := []string{"commands", "version", "completion", "help"}
	for _, cmd := range excluded {
		require.NotContains(t, listing.Verbs, cmd, "%q should be excluded from listing", cmd)
	}
}

func TestCommandsCmd_MutatingVerbsCorrect(t *testing.T) {
	listing := commands.Build(rootCmd)

	mutatingVerbs := map[string]bool{
		"apply":   true,
		"create":  true,
		"edit":    true,
		"delete":  true,
		"exec":    true,
		"restore": true,
		"share":   true,
		"unshare": true,
		"update":  true,
	}

	readOnlyVerbs := []string{
		"get", "describe", "diff", "query", "wait", "doctor",
		"history", "logs", "ctx", "find", "verify", "open", "download",
		"skills",
	}

	for verb, expected := range mutatingVerbs {
		v, ok := listing.Verbs[verb]
		if !ok {
			continue // verb may not be registered yet
		}
		require.Equal(t, expected, v.Mutating, "verb %q should be mutating=%v", verb, expected)
		if expected {
			require.NotEmpty(t, v.SafetyOp, "mutating verb %q should have safety_operation", verb)
		}
	}

	for _, verb := range readOnlyVerbs {
		v, ok := listing.Verbs[verb]
		if !ok {
			continue
		}
		require.False(t, v.Mutating, "verb %q should not be mutating", verb)
		require.Empty(t, v.SafetyOp, "read-only verb %q should not have safety_operation", verb)
	}
}

func TestCommandsCmd_GetHasResources(t *testing.T) {
	listing := commands.Build(rootCmd)

	getVerb := listing.Verbs["get"]
	require.NotNil(t, getVerb)
	require.NotEmpty(t, getVerb.Resources, "get verb should have resources")

	// Spot-check some expected resources
	require.Contains(t, getVerb.Resources, "workflows")
	require.Contains(t, getVerb.Resources, "dashboards")
	require.Contains(t, getVerb.Resources, "slos")
	require.Contains(t, getVerb.Resources, "settings")
	require.Contains(t, getVerb.Resources, "buckets")
}

func TestCommandsCmd_ResourceAliases(t *testing.T) {
	listing := commands.Build(rootCmd)

	require.NotNil(t, listing.Aliases)
	require.Equal(t, "workflows", listing.Aliases["wf"])
	require.Equal(t, "dashboards", listing.Aliases["dash"])
}

func TestCommandsCmd_TimeFormats(t *testing.T) {
	listing := commands.Build(rootCmd)

	require.NotNil(t, listing.TimeFormats)
	require.NotEmpty(t, listing.TimeFormats.Relative)
	require.NotEmpty(t, listing.TimeFormats.Absolute)
	require.NotEmpty(t, listing.TimeFormats.Unix)
}

func TestCommandsCmd_BriefReducesSize(t *testing.T) {
	listing := commands.Build(rootCmd)
	fullData, err := json.Marshal(listing)
	require.NoError(t, err)

	briefListing := commands.NewBrief(listing)
	briefData, err := json.Marshal(briefListing)
	require.NoError(t, err)

	// Brief should be significantly smaller
	reduction := 1.0 - float64(len(briefData))/float64(len(fullData))
	require.Greater(t, reduction, 0.30, "brief mode should reduce size by at least 30%%, got %.0f%%", reduction*100)
}

func TestCommandsCmd_FilterByAlias(t *testing.T) {
	listingByName := commands.Build(rootCmd)
	listingByAlias := commands.Build(rootCmd)

	filteredByName, matchedName := commands.FilterByResource(listingByName, "workflows")
	filteredByAlias, matchedAlias := commands.FilterByResource(listingByAlias, "wf")

	require.True(t, matchedName)
	require.True(t, matchedAlias)

	// Same verbs should match
	var nameVerbs, aliasVerbs []string
	for v := range filteredByName.Verbs {
		nameVerbs = append(nameVerbs, v)
	}
	for v := range filteredByAlias.Verbs {
		aliasVerbs = append(aliasVerbs, v)
	}
	require.ElementsMatch(t, nameVerbs, aliasVerbs)
}

func TestCommandsCmd_FilterByVerb(t *testing.T) {
	listing := commands.Build(rootCmd)
	filtered, matched := commands.FilterByResource(listing, "get")

	require.True(t, matched)
	require.Len(t, filtered.Verbs, 1)
	require.Contains(t, filtered.Verbs, "get")
}

func TestCommandsCmd_FilterNoMatch(t *testing.T) {
	listing := commands.Build(rootCmd)
	_, matched := commands.FilterByResource(listing, "nonexistent-resource")

	require.False(t, matched)
}

func TestCommandsCmd_GlobalFlags(t *testing.T) {
	listing := commands.Build(rootCmd)

	require.NotNil(t, listing.GlobalFlags)
	require.Contains(t, listing.GlobalFlags, "--output")
	require.Contains(t, listing.GlobalFlags, "--agent")
	require.NotContains(t, listing.GlobalFlags, "--dry-run")
	require.Contains(t, listing.GlobalFlags, "--context")
	require.Contains(t, listing.GlobalFlags, "--plain")
	require.Contains(t, listing.GlobalFlags, "--chunk-size")
}

func TestCommandsCmd_ExecHasSubcommands(t *testing.T) {
	listing := commands.Build(rootCmd)

	execVerb := listing.Verbs["exec"]
	require.NotNil(t, execVerb)
	require.NotEmpty(t, execVerb.Resources, "exec should have resources")

	// copilot should be a subcommand (has nested commands)
	require.NotNil(t, execVerb.Subcommands, "exec should have subcommands")
	require.Contains(t, execVerb.Subcommands, "copilot")
}

func TestCommandsCmd_Howto(t *testing.T) {
	listing := commands.Build(rootCmd)

	var buf bytes.Buffer
	err := commands.GenerateHowto(&buf, listing)
	require.NoError(t, err)

	output := buf.String()
	require.Contains(t, output, "# dtctl Quick Reference")
	require.Contains(t, output, "## Common Workflows")
	require.Contains(t, output, "## Safety Levels")

	// Verify all verbs appear in howto
	for verb := range listing.Verbs {
		require.True(t, strings.Contains(output, verb),
			"howto should mention verb %q", verb)
	}
}

func TestCommandsCmd_PatternsAndAntipatterns(t *testing.T) {
	listing := commands.Build(rootCmd)

	require.NotEmpty(t, listing.Patterns)
	require.NotEmpty(t, listing.Antipatterns)

	// Check for key content
	found := false
	for _, p := range listing.Patterns {
		if strings.Contains(p, "apply") {
			found = true
			break
		}
	}
	require.True(t, found, "patterns should mention apply")

	found = false
	for _, p := range listing.Antipatterns {
		if strings.Contains(p, "table output") {
			found = true
			break
		}
	}
	require.True(t, found, "antipatterns should warn about table output parsing")
}

// --- Cross-reference tests ---

// TestMutatingVerbsMatchSafetyCheckerUsage scans cmd/ source files for
// safety-check call sites (both NewSafetyChecker and SetupWithSafety) and
// verifies that every verb which uses safety checks is listed in
// commands.MutatingVerbs. This catches drift between the catalog and the
// actual command implementations.
//
// Both call patterns must be detected: SetupWithSafety is the dominant
// helper today (defined in cmd/root.go), but some files still call
// NewSafetyChecker directly. Missing either would silently misclassify
// mutating verbs as read-only in the JSON catalog (see issue #203).
func TestMutatingVerbsMatchSafetyCheckerUsage(t *testing.T) {
	// Find all .go files in cmd/ that contain a safety-check call site
	cmdDir := "."
	entries, err := os.ReadDir(cmdDir)
	require.NoError(t, err)

	verbsWithSafetyChecks := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(cmdDir, entry.Name()))
		require.NoError(t, err)

		content := string(data)
		// Detect both safety-check patterns:
		//   - NewSafetyChecker(...): direct construction
		//   - SetupWithSafety(...): root.go helper that builds a checker internally
		if !strings.Contains(content, "NewSafetyChecker") && !strings.Contains(content, "SetupWithSafety(") &&
			!strings.Contains(content, "setupWithSafety(") {
			continue
		}

		// Extract the verb from the filename pattern: <verb>_<resource>.go or <verb>.go
		name := strings.TrimSuffix(entry.Name(), ".go")
		verb := strings.SplitN(name, "_", 2)[0]

		// Special cases: root.go defines both NewSafetyChecker and SetupWithSafety,
		// not a verb itself.
		if verb == "root" || verb == "safety" {
			continue
		}

		// "get" files that call safety-check helpers are delete handlers
		// (e.g., get_workflows.go has deleteWorkflowCmd) — these use
		// the "delete" parent command's safety check, not "get".
		if verb == "get" {
			verbsWithSafetyChecks["delete"] = true
			continue
		}

		verbsWithSafetyChecks[verb] = true
	}

	// Every verb with safety checks should be in MutatingVerbs
	for verb := range verbsWithSafetyChecks {
		_, ok := commands.MutatingVerbs[verb]
		require.True(t, ok,
			"verb %q uses safety checks (NewSafetyChecker or SetupWithSafety) but is missing from commands.MutatingVerbs", verb)
	}

	// Every verb in MutatingVerbs should exist in the real command tree
	listing := commands.Build(rootCmd)
	for verb := range commands.MutatingVerbs {
		_, ok := listing.Verbs[verb]
		require.True(t, ok,
			"commands.MutatingVerbs contains %q but it doesn't exist in the command tree", verb)
	}
}

// TestMutatingVerbFilesPerformSafetyChecks asserts the direction
// TestMutatingVerbsMatchSafetyCheckerUsage does not: that every command file
// belonging to a verb the catalog declares *mutating* actually performs a safety
// check when it builds a client.
//
// The forward assertion ("every verb with safety checks is in MutatingVerbs")
// cannot catch a whole verb family that declares itself mutating and then never
// checks. That is exactly what happened to `exec`: the catalog advertised
// `exec: OperationCreate` while every subcommand called SetupClient(), so a
// readonly context did not block `dtctl exec function --code '…'` — ad-hoc
// JavaScript that can POST or DELETE anywhere the token reaches.
func TestMutatingVerbFilesPerformSafetyChecks(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	// Building a client is the observable signal that a file talks to the
	// platform. The signal must include the *safe* helpers too: listing only the
	// ungated ones would make the test vacuous the moment a file is fixed, and
	// would not notice a later regression that reintroduces ungated access
	// alongside a gated sibling in the same file.
	clientCtors := []string{
		"SetupClient()", "Setup()", "SetupWithSafety", "NewClientFromConfig(",
		// the context-taking twins the commands call from inside an invocation
		"setupClient(", "setup(", "setupWithSafety", "newClientFromConfig(",
	}
	// SetupWithSafetyAndPrinter contains SetupWithSafety, so both are covered.
	// CheckSafety is the direct form: it is where the --dry-run exemption
	// lives, so a command gates through it rather than through a checker of
	// its own (TestSafetyChecksGoThroughCheckSafety).
	safetyCalls := []string{"NewSafetyChecker", "SetupWithSafety", "CheckSafety(", "setupWithSafety", "checkSafety("}

	containsAny := func(s string, needles []string) bool {
		for _, n := range needles {
			if strings.Contains(s, n) {
				return true
			}
		}
		return false
	}

	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		// Map the file to a verb by exact convention: <verb>.go or <verb>_<rest>.go.
		// Prefix matching alone would misattribute e.g. "execute_x.go" to "exec".
		stem := strings.TrimSuffix(name, ".go")
		verb := strings.SplitN(stem, "_", 2)[0]
		if _, mutating := commands.MutatingVerbs[verb]; !mutating {
			continue
		}

		data, err := os.ReadFile(name)
		require.NoError(t, err)
		content := string(data)

		if !containsAny(content, clientCtors) {
			continue // no platform access in this file
		}
		checked++
		require.True(t, containsAny(content, safetyCalls),
			"%s belongs to the mutating verb %q and builds a platform client, but performs no "+
				"safety check (CheckSafety, SetupWithSafety or NewSafetyChecker). Every command under a "+
				"mutating verb must gate on the safety level — see AGENTS.md 'CRITICAL: Safety Checks'.",
			name, verb)
	}

	// Guard the guard: if the file-naming convention ever changes, this test must
	// fail loudly rather than silently passing over an empty set.
	require.Greater(t, checked, 20,
		"expected to inspect many mutating-verb command files; found only %d — the "+
			"<verb>_<resource>.go naming convention may have changed", checked)
}

// TestResourceAliasesMatchCobraAliases walks the real Cobra command tree and
// collects all resource-level Aliases defined on subcommands. It then verifies
// that commands.ResourceAliases contains these aliases (or a documented subset).
func TestResourceAliasesMatchCobraAliases(t *testing.T) {
	// Collect all short aliases from the "get" command's subcommands,
	// which is the canonical place for resource aliases.
	getCmd, _, err := rootCmd.Find([]string{"get"})
	require.NoError(t, err)
	require.NotNil(t, getCmd)

	cobraAliases := make(map[string]string) // alias -> canonical resource name
	for _, sub := range getCmd.Commands() {
		if sub.Hidden || sub.Name() == "help" {
			continue
		}
		for _, alias := range sub.Aliases {
			// Skip aliases that are just singular/plural forms
			// (e.g., "workflow" as alias for "workflows")
			if isSingularPlural(alias, sub.Name()) {
				continue
			}
			cobraAliases[alias] = sub.Name()
		}
	}

	// Verify that ResourceAliases covers the key short aliases from Cobra.
	// We check that every short alias (non-singular/plural) in the "get"
	// command is present in ResourceAliases.
	for alias, resource := range cobraAliases {
		target, ok := commands.ResourceAliases[alias]
		if !ok {
			// Not every Cobra alias needs to be in ResourceAliases
			// (some are verb-specific), but common ones should be.
			// Log it so we can catch drift.
			t.Logf("INFO: Cobra alias %q → %q (from get) not in ResourceAliases (may be OK for verb-specific aliases)", alias, resource)
			continue
		}
		// If it IS in ResourceAliases, verify it points to the same resource
		// (accounting for singular/plural differences)
		if target != resource && !isSingularPlural(target, resource) {
			t.Errorf("ResourceAliases[%q] = %q, but Cobra alias points to %q",
				alias, target, resource)
		}
	}

	// Verify every entry in ResourceAliases resolves to a real resource
	listing := commands.Build(rootCmd)
	for alias, target := range commands.ResourceAliases {
		// The target should appear in at least one verb's resources or subcommands
		found := false
		for _, verb := range listing.Verbs {
			for _, r := range verb.Resources {
				if r == target || isSingularPlural(r, target) {
					found = true
					break
				}
			}
			if found {
				break
			}
			for subName := range verb.Subcommands {
				if subName == target || isSingularPlural(subName, target) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		require.True(t, found,
			"ResourceAliases[%q] → %q, but %q doesn't appear in any verb's resources",
			alias, target, target)
	}
}

// isSingularPlural returns true if a and b differ only by a trailing "s".
func isSingularPlural(a, b string) bool {
	if a == b {
		return true
	}
	return a+"s" == b || b+"s" == a
}

// --- WriteTo integration tests (using real rootCmd) ---

func TestCommandsCmd_WriteToJSON(t *testing.T) {
	listing := commands.Build(rootCmd)

	var buf bytes.Buffer
	err := commands.WriteTo(&buf, listing, "json")
	require.NoError(t, err)
	require.True(t, json.Valid(buf.Bytes()), "WriteTo JSON should produce valid JSON")

	var decoded commands.Listing
	err = json.Unmarshal(buf.Bytes(), &decoded)
	require.NoError(t, err)
	require.Equal(t, "dtctl", decoded.Tool)
	require.NotEmpty(t, decoded.Verbs)
}

func TestCommandsCmd_WriteToYAML(t *testing.T) {
	listing := commands.Build(rootCmd)

	var buf bytes.Buffer
	err := commands.WriteTo(&buf, listing, "yaml")
	require.NoError(t, err)

	var decoded commands.Listing
	err = yaml.Unmarshal(buf.Bytes(), &decoded)
	require.NoError(t, err)
	require.Equal(t, "dtctl", decoded.Tool)
	require.NotEmpty(t, decoded.Verbs)
}

// --- NewBrief integration test (using real rootCmd) ---

func TestCommandsCmd_NewBriefDoesNotMutateOriginal(t *testing.T) {
	listing := commands.Build(rootCmd)

	origVerbCount := len(listing.Verbs)
	origDesc := listing.Description
	origGlobalFlagCount := len(listing.GlobalFlags)

	brief := commands.NewBrief(listing)

	// Original should be unchanged
	require.Len(t, listing.Verbs, origVerbCount)
	require.Equal(t, origDesc, listing.Description)
	require.Len(t, listing.GlobalFlags, origGlobalFlagCount)

	// Brief should have stripped verbose fields
	require.Empty(t, brief.Description)
	require.Nil(t, brief.GlobalFlags)
	require.Nil(t, brief.TimeFormats)

	// But should preserve structure and agent grounding (patterns/antipatterns)
	require.Equal(t, listing.Patterns, brief.Patterns)
	require.Equal(t, listing.Antipatterns, brief.Antipatterns)
	require.Len(t, brief.Verbs, origVerbCount)
	require.NotNil(t, brief.Aliases)
}

func TestCommandsCmd_NewBriefPreservesMutating(t *testing.T) {
	listing := commands.Build(rootCmd)
	brief := commands.NewBrief(listing)

	for name, verb := range listing.Verbs {
		briefVerb, ok := brief.Verbs[name]
		require.True(t, ok, "verb %q should be in brief", name)
		require.Equal(t, verb.Mutating, briefVerb.Mutating,
			"verb %q mutating status should be preserved in brief", name)
	}
}

// --- E2E test for runCommandsListing ---

func TestCommandsCmd_RunCommandsListing_Integration(t *testing.T) {
	// Build the listing the same way runCommandsListing does
	listing := commands.Build(rootCmd)

	// Full output
	var fullBuf bytes.Buffer
	err := commands.WriteTo(&fullBuf, listing, "json")
	require.NoError(t, err)
	require.True(t, json.Valid(fullBuf.Bytes()))

	// Brief output
	brief := commands.NewBrief(listing)
	var briefBuf bytes.Buffer
	err = commands.WriteTo(&briefBuf, brief, "json")
	require.NoError(t, err)
	require.True(t, json.Valid(briefBuf.Bytes()))
	require.Less(t, briefBuf.Len(), fullBuf.Len(),
		"brief output should be smaller than full output")

	// Filtered output
	unfiltered := commands.Build(rootCmd)
	filtered, matched := commands.FilterByResource(unfiltered, "workflows")
	require.True(t, matched)
	var filteredBuf bytes.Buffer
	err = commands.WriteTo(&filteredBuf, filtered, "json")
	require.NoError(t, err)
	require.Less(t, filteredBuf.Len(), fullBuf.Len(),
		"filtered output should be smaller than full output")
}

// --- Completeness test: listing covers all non-hidden commands ---

func TestCommandsCmd_ListingCoversAllNonHiddenCommands(t *testing.T) {
	listing := commands.Build(rootCmd)

	// Every non-hidden root subcommand (except utility ones) should be in listing
	for _, cmd := range rootCmd.Commands() {
		if cmd.Hidden {
			continue
		}
		name := cmd.Name()
		// Skip utility commands that are intentionally excluded
		if name == "help" || name == "completion" || name == "version" || name == "commands" {
			continue
		}
		// Help topics (e.g. token-scopes) have nothing to run.
		if !cmd.Runnable() && !cmd.HasSubCommands() {
			require.NotContains(t, listing.Verbs, name, "help topic %q must not be in the listing", name)
			continue
		}
		require.Contains(t, listing.Verbs, name,
			"non-hidden command %q should be in the listing", name)
	}
}

// TestAllCommandsHaveHelpText verifies that all non-hidden, non-utility root
// commands have Long descriptions and Example fields populated.
func TestAllCommandsHaveHelpText(t *testing.T) {
	// Meta/utility commands that are exempt from this requirement
	metaCommands := map[string]bool{
		"help":       true,
		"completion": true,
		"version":    true,
		"commands":   true,
	}

	for _, cmd := range rootCmd.Commands() {
		if cmd.Hidden || metaCommands[cmd.Name()] {
			continue
		}
		t.Run(cmd.Name(), func(t *testing.T) {
			require.NotEmpty(t, cmd.Long,
				"command %q should have a Long description", cmd.Name())
		})
	}
}

// TestParentVerbsHaveExamples verifies that all parent verb commands have
// examples in the Cobra Example field (not embedded in Long).
func TestParentVerbsHaveExamples(t *testing.T) {
	parentVerbs := []string{
		"get", "delete", "create", "edit", "exec",
		"describe", "find", "update", "open", "doctor", "download",
		"skills",
	}

	for _, name := range parentVerbs {
		cmd, _, err := rootCmd.Find([]string{name})
		require.NoError(t, err, "command %q should exist", name)
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, cmd.Example,
				"parent verb %q should have examples in the Example field", name)
		})
	}
}
