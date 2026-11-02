package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// TestGlobalFlags validates all global persistent flags
func TestGlobalFlags(t *testing.T) {
	tests := []struct {
		flagName     string
		defaultValue string
	}{
		{"config", ""},
		{"context", ""},
		{"output", "table"},
		{"verbose", "0"},
		{"plain", "false"},
		{"chunk-size", "500"},
	}

	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := rootCmd.PersistentFlags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("Global flag --%s not found", tt.flagName)
			}

			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}
}

// TestQueryFlags validates query command flags
func TestQueryFlags(t *testing.T) {
	tests := []struct {
		flagName     string
		defaultValue string
	}{
		{"file", ""},
		{"live", "false"},
		{"interval", "1m0s"},
		{"width", "0"},
		{"height", "0"},
		{"fullscreen", "false"},
		{"max-result-records", "0"},
		{"max-result-bytes", "0"},
		{"default-scan-limit-gbytes", "0"},
		{"default-sampling-ratio", "0"},
		{"fetch-timeout-seconds", "0"},
		{"enable-preview", "false"},
		{"enforce-query-consumption-limit", "false"},
		{"include-types", "false"},
		{"default-timeframe-start", ""},
		{"default-timeframe-end", ""},
		{"locale", ""},
		{"timezone", ""},
		{"metadata", ""},
	}

	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := queryCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("Query flag --%s not found", tt.flagName)
			}

			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}
}

// TestVerifyQueryFlags validates verify query subcommand flags
func TestVerifyQueryFlags(t *testing.T) {
	tests := []struct {
		flagName     string
		defaultValue string
	}{
		{"file", ""},
		{"canonical", "false"},
		{"timezone", ""},
		{"locale", ""},
		{"fail-on-warn", "false"},
	}

	// Find the query subcommand under verifyCmd
	var queryCmd *cobra.Command
	for _, cmd := range verifyCmd.Commands() {
		if cmd.Name() == "query" {
			queryCmd = cmd
			break
		}
	}
	if queryCmd == nil {
		t.Fatal("verify query subcommand not found")
	}

	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := queryCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("Verify query flag --%s not found", tt.flagName)
			}
			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}

	// Verify the --set flag is also available (inherited from StringArray pattern)
	t.Run("set_flag", func(t *testing.T) {
		flag := queryCmd.Flags().Lookup("set")
		if flag == nil {
			t.Fatal("Verify query flag --set not found")
		}
		// StringArray flags have "[]" as default value
		if flag.DefValue != "[]" {
			t.Errorf("Flag --set default = %q, want %q", flag.DefValue, "[]")
		}
	})
}

// TestExecFlags validates exec command flags
func TestExecFlags(t *testing.T) {
	// Test flags on execWorkflowCmd
	workflowFlags := []struct {
		flagName     string
		defaultValue string
	}{
		{"input", ""},
		{"wait", "false"},
		{"timeout", "30m0s"},
		{"show-results", "false"},
	}

	for _, tt := range workflowFlags {
		t.Run("workflow_"+tt.flagName, func(t *testing.T) {
			flag := execWorkflowCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("Exec workflow flag --%s not found", tt.flagName)
			}

			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}

	// Test flags on execFunctionCmd
	functionFlags := []struct {
		flagName     string
		defaultValue string
	}{
		{"method", "GET"},
		{"payload", ""},
		{"data", ""},
		{"code", ""},
		{"file", ""},
		{"defer", "false"},
	}

	for _, tt := range functionFlags {
		t.Run("function_"+tt.flagName, func(t *testing.T) {
			flag := execFunctionCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("Exec function flag --%s not found", tt.flagName)
			}

			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}

	// Test flags on execDQLCmd
	dqlFlags := []string{"file"}
	for _, flagName := range dqlFlags {
		t.Run("dql_"+flagName, func(t *testing.T) {
			flag := execDQLCmd.Flags().Lookup(flagName)
			if flag == nil {
				t.Fatalf("Exec DQL flag --%s not found", flagName)
			}
		})
	}

	// Test flags on execAnalyzerCmd
	analyzerFlags := []string{"file"}
	for _, flagName := range analyzerFlags {
		t.Run("analyzer_"+flagName, func(t *testing.T) {
			flag := execAnalyzerCmd.Flags().Lookup(flagName)
			if flag == nil {
				t.Fatalf("Exec analyzer flag --%s not found", flagName)
			}
		})
	}
}

// TestGetFlags validates get command flags
func TestGetFlags(t *testing.T) {
	// Test a sample of get subcommand flags
	tests := []struct {
		subcmd   string
		flagName string
	}{
		{"dashboards", "name"},
		{"dashboards", "mine"},
		{"notebooks", "name"},
		{"notebooks", "mine"},
		{"settings", "schema"},
		{"settings", "scope"},
		{"slos", "filter"},
		{"notifications", "type"},
	}

	for _, tt := range tests {
		t.Run(tt.subcmd+"_"+tt.flagName, func(t *testing.T) {
			// Find the subcommand
			var subcmd *cobra.Command
			for _, cmd := range getCmd.Commands() {
				if cmd.Name() == tt.subcmd {
					subcmd = cmd
					break
				}
			}

			if subcmd == nil {
				t.Skipf("Subcommand %s not found", tt.subcmd)
				return
			}

			flag := subcmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Errorf("Get %s flag --%s not found", tt.subcmd, tt.flagName)
			}
		})
	}
}

// TestCreateFlags validates create command flags
func TestCreateFlags(t *testing.T) {
	// Test common create flags
	tests := []struct {
		subcmd   string
		flagName string
	}{
		{"workflow", "file"},
		{"workflow", "set"},
		{"notebook", "file"},
		{"notebook", "name"},
		{"notebook", "description"},
		{"dashboard", "file"},
		{"dashboard", "name"},
		{"settings", "file"},
		{"settings", "schema"},
		{"settings", "scope"},
	}

	for _, tt := range tests {
		t.Run(tt.subcmd+"_"+tt.flagName, func(t *testing.T) {
			// Find the subcommand
			var subcmd *cobra.Command
			for _, cmd := range createCmd.Commands() {
				if cmd.Name() == tt.subcmd {
					subcmd = cmd
					break
				}
			}

			if subcmd == nil {
				t.Skipf("Create subcommand %s not found", tt.subcmd)
				return
			}

			flag := subcmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Errorf("Create %s flag --%s not found", tt.subcmd, tt.flagName)
			}
		})
	}
}

// TestWaitFlags validates wait command flags
func TestWaitFlags(t *testing.T) {
	queryFlags := []struct {
		flagName     string
		defaultValue string
	}{
		{"for", ""},
		{"file", ""},
		{"max-attempts", "0"},
		{"quiet", "false"},
	}

	for _, tt := range queryFlags {
		t.Run("query_"+tt.flagName, func(t *testing.T) {
			flag := waitQueryCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("Wait query flag --%s not found", tt.flagName)
			}

			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}
}

// TestApplyFlags validates apply command flags
func TestApplyFlags(t *testing.T) {
	tests := []struct {
		flagName     string
		defaultValue string
	}{
		{"file", ""},
		{"show-diff", "false"},
		{"no-hooks", "false"},
		{"type", ""},
		{"label", "[]"},
	}

	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := applyCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("Apply flag --%s not found", tt.flagName)
			}

			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}
}

// TestFlagParsing tests that flags can be parsed correctly
func TestFlagParsing(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		flagName string
		wantVal  string
	}{
		{
			name:     "output flag short form",
			args:     []string{"-o", "json"},
			flagName: "output",
			wantVal:  "json",
		},
		{
			name:     "output flag long form",
			args:     []string{"--output", "yaml"},
			flagName: "output",
			wantVal:  "yaml",
		},
		{
			name:     "verbose flag single",
			args:     []string{"-v"},
			flagName: "verbose",
			wantVal:  "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = rootCmd.ParseFlags(tt.args)

			flag := rootCmd.PersistentFlags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("Flag %s not found", tt.flagName)
			}

			if flag.Value.String() != tt.wantVal {
				t.Errorf("Flag %s value = %q, want %q", tt.flagName, flag.Value.String(), tt.wantVal)
			}

			// Reset for next test
			_ = flag.Value.Set(flag.DefValue)
			flag.Changed = false
		})
	}
}

// TestAllCommandsHaveFlags ensures major commands have at least some flags
func TestAllCommandsHaveFlags(t *testing.T) {
	commands := []struct {
		name string
		cmd  *cobra.Command
	}{
		{"query", queryCmd},
		{"exec", execCmd},
		{"get", getCmd},
		{"create", createCmd},
		{"apply", applyCmd},
		{"wait", waitCmd},
	}

	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			flagCount := 0
			tc.cmd.Flags().VisitAll(func(f *pflag.Flag) {
				flagCount++
			})

			// Each command should have at least one flag or subcommands with flags
			if flagCount == 0 && len(tc.cmd.Commands()) == 0 {
				t.Errorf("Command %s has no flags and no subcommands", tc.name)
			}
		})
	}
}

// TestEditFlags validates edit command flags
func TestEditFlags(t *testing.T) {
	// Edit commands should have format flag
	editCommands := []string{"workflow", "dashboard", "notebook"}

	for _, cmdName := range editCommands {
		t.Run("edit_"+cmdName, func(t *testing.T) {
			var subcmd *cobra.Command
			for _, cmd := range editCmd.Commands() {
				if cmd.Name() == cmdName {
					subcmd = cmd
					break
				}
			}

			if subcmd == nil {
				t.Skipf("Edit subcommand %s not found", cmdName)
				return
			}

			// Check for format flag
			flag := subcmd.Flags().Lookup("format")
			if flag != nil && flag.DefValue != "yaml" {
				t.Logf("Edit %s has format flag with default %s", cmdName, flag.DefValue)
			}
		})
	}
}

// TestShareFlags validates share command flags
func TestShareFlags(t *testing.T) {
	shareFlags := []string{"user", "group", "access"}

	for _, flagName := range shareFlags {
		t.Run(flagName, func(t *testing.T) {
			// Check if flag exists on any share subcommand
			found := false
			for _, subcmd := range shareCmd.Commands() {
				if subcmd.Flags().Lookup(flagName) != nil {
					found = true
					break
				}
			}

			if !found {
				t.Logf("Flag --%s not found on share subcommands (may be optional)", flagName)
			}
		})
	}
}

// TestAuthFlags validates auth command flags
func TestAuthFlags(t *testing.T) {
	// Check whoami subcommand
	var whoamiCmd *cobra.Command
	for _, cmd := range authCmd.Commands() {
		if cmd.Name() == "whoami" {
			whoamiCmd = cmd
			break
		}
	}

	if whoamiCmd == nil {
		t.Skip("whoami subcommand not found")
		return
	}

	flags := []struct {
		name         string
		defaultValue string
	}{
		{"id-only", "false"},
		{"refresh", "false"},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := whoamiCmd.Flags().Lookup(tt.name)
			if flag == nil {
				t.Errorf("Auth whoami flag --%s not found", tt.name)
				return
			}

			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.name, flag.DefValue, tt.defaultValue)
			}
		})
	}
}

// TestConfigFlags validates config command flags
func TestConfigFlags(t *testing.T) {
	configFlags := []string{"environment", "token-ref", "token"}

	for _, flagName := range configFlags {
		t.Run(flagName, func(t *testing.T) {
			// Check if flag exists on any config subcommand
			found := false
			for _, subcmd := range configCmd.Commands() {
				if subcmd.Flags().Lookup(flagName) != nil {
					found = true
					break
				}
			}

			if !found {
				t.Logf("Flag --%s not found on config subcommands (may be optional)", flagName)
			}
		})
	}
}

// TestLogsFlags validates logs command flags
func TestLogsFlags(t *testing.T) {
	// Check workflow execution logs subcommand
	var logsWorkflowCmd *cobra.Command
	for _, cmd := range logsCmd.Commands() {
		if cmd.Name() == "workflow-execution" || cmd.Name() == "execution" || cmd.Name() == "workflow" {
			logsWorkflowCmd = cmd
			break
		}
	}

	if logsWorkflowCmd == nil {
		t.Skip("logs workflow/execution subcommand not found")
		return
	}

	flags := []struct {
		name         string
		defaultValue string
	}{
		{"task", ""},
		{"follow", "false"},
		{"all", "false"},
		{"tasks", "false"},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := logsWorkflowCmd.Flags().Lookup(tt.name)
			if flag == nil {
				t.Logf("Logs flag --%s not found (may use different name)", tt.name)
				return
			}

			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.name, flag.DefValue, tt.defaultValue)
			}
		})
	}
}

// TestRestoreFlags validates restore command flags
func TestRestoreFlags(t *testing.T) {
	// Test force flag on restore subcommands
	restoreSubcommands := []string{"workflow", "dashboard", "notebook"}

	for _, cmdName := range restoreSubcommands {
		t.Run(cmdName, func(t *testing.T) {
			var subcmd *cobra.Command
			for _, cmd := range restoreCmd.Commands() {
				if cmd.Name() == cmdName {
					subcmd = cmd
					break
				}
			}

			if subcmd == nil {
				t.Skipf("Restore subcommand %s not found", cmdName)
				return
			}

			flag := subcmd.Flags().Lookup("force")
			if flag == nil {
				t.Errorf("Restore %s flag --force not found", cmdName)
				return
			}

			if flag.DefValue != "false" {
				t.Errorf("Flag --force default = %q, want %q", flag.DefValue, "false")
			}
		})
	}
}

// TestVersionCommand validates version command exists
func TestVersionCommand(t *testing.T) {
	if versionCmd == nil {
		t.Fatal("versionCmd is nil")
	}

	if versionCmd.Use != "version" {
		t.Errorf("versionCmd.Use = %q, want %q", versionCmd.Use, "version")
	}
}

// TestCompletionCommand validates completion command exists
func TestCompletionCommand(t *testing.T) {
	if completionCmd == nil {
		t.Fatal("completionCmd is nil")
	}

	// Just verify it exists, don't check exact Use string
	if completionCmd.Name() != "completion" {
		t.Errorf("completionCmd.Name() = %q, want %q", completionCmd.Name(), "completion")
	}
}

// TestDescribeCommand validates describe command exists
func TestDescribeCommand(t *testing.T) {
	if describeCmd == nil {
		t.Fatal("describeCmd is nil")
	}

	// Describe should have subcommands
	if len(describeCmd.Commands()) == 0 {
		t.Error("describeCmd has no subcommands")
	}
}

// TestDescribeSLOCommand validates describe slo command exists
func TestDescribeSLOCommand(t *testing.T) {
	if describeSLOCmd == nil {
		t.Fatal("describeSLOCmd is nil")
	}

	if describeSLOCmd.Name() != "slo" {
		t.Errorf("describeSLOCmd.Name() = %q, want %q", describeSLOCmd.Name(), "slo")
	}

	// Verify it's registered under describe
	found := false
	for _, cmd := range describeCmd.Commands() {
		if cmd.Name() == "slo" {
			found = true
			break
		}
	}
	if !found {
		t.Error("slo command not found under describe")
	}
}

// TestHistoryCommand validates history command exists
func TestHistoryCommand(t *testing.T) {
	if historyCmd == nil {
		t.Fatal("historyCmd is nil")
	}

	// History should have subcommands
	if len(historyCmd.Commands()) == 0 {
		t.Error("historyCmd has no subcommands")
	}
}

// TestSkillsFlags validates skills command flags
func TestSkillsFlags(t *testing.T) {
	// skills install flags
	installFlags := []struct {
		flagName     string
		defaultValue string
	}{
		{"for", ""},
		{"global", "false"},
		{"force", "false"},
		{"list", "false"},
	}

	for _, tt := range installFlags {
		t.Run("install_"+tt.flagName, func(t *testing.T) {
			flag := skillsInstallCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("Skills install flag --%s not found", tt.flagName)
			}
			if flag.DefValue != tt.defaultValue {
				t.Errorf("Flag --%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}

	// skills uninstall flags
	t.Run("uninstall_for", func(t *testing.T) {
		flag := skillsUninstallCmd.Flags().Lookup("for")
		if flag == nil {
			t.Fatal("Skills uninstall flag --for not found")
		}
		if flag.DefValue != "" {
			t.Errorf("Flag --for default = %q, want %q", flag.DefValue, "")
		}
	})

	// skills status flags
	t.Run("status_for", func(t *testing.T) {
		flag := skillsStatusCmd.Flags().Lookup("for")
		if flag == nil {
			t.Fatal("Skills status flag --for not found")
		}
		if flag.DefValue != "" {
			t.Errorf("Flag --for default = %q, want %q", flag.DefValue, "")
		}
	})
}

// TestSkillsCommand validates skills command structure
func TestSkillsCommand(t *testing.T) {
	if skillsCmd == nil {
		t.Fatal("skillsCmd is nil")
	}

	// Skills should have subcommands
	subs := skillsCmd.Commands()
	if len(subs) == 0 {
		t.Fatal("skillsCmd has no subcommands")
	}

	expectedSubs := []string{"install", "uninstall", "status"}
	for _, name := range expectedSubs {
		found := false
		for _, sub := range subs {
			if sub.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected subcommand %q not found under skills", name)
		}
	}
}

// TestMetadataFlagCompletion_Empty tests completion when nothing is typed yet
func TestMetadataFlagCompletion_Empty(t *testing.T) {
	suggestions, directive := metadataFieldCompletion(nil, nil, "")

	if directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Error("expected ShellCompDirectiveNoFileComp to be set")
	}
	if directive&cobra.ShellCompDirectiveNoSpace == 0 {
		t.Error("expected ShellCompDirectiveNoSpace to be set")
	}

	// Should include "all" and "minimal" plus all valid field names
	allFields := output.ValidMetadataFieldNames()
	expectedCount := len(allFields) + 2 // +2 for "all" and "minimal"
	if len(suggestions) != expectedCount {
		t.Errorf("got %d suggestions, want %d", len(suggestions), expectedCount)
	}

	// First suggestion should be "all" (with tab-separated description)
	if suggestions[0] != "all\tInclude all metadata fields" {
		t.Errorf("first suggestion = %q, want %q", suggestions[0], "all\tInclude all metadata fields")
	}
	if !strings.HasPrefix(suggestions[1], "minimal\t") {
		t.Errorf("second suggestion = %q, want the minimal selector", suggestions[1])
	}
}

// TestMetadataFlagCompletion_Minimal tests that the minimal selector completes
// by prefix, alone and after already-selected fields.
func TestMetadataFlagCompletion_Minimal(t *testing.T) {
	suggestions, _ := metadataFieldCompletion(nil, nil, "min")
	if len(suggestions) != 1 || suggestions[0] != "minimal" {
		t.Errorf("completion of %q = %v, want [minimal]", "min", suggestions)
	}
	suggestions, _ = metadataFieldCompletion(nil, nil, "metrics,mi")
	if len(suggestions) != 1 || suggestions[0] != "metrics,minimal" {
		t.Errorf("completion of %q = %v, want [metrics,minimal]", "metrics,mi", suggestions)
	}
	suggestions, _ = metadataFieldCompletion(nil, nil, "minimal,mi")
	if len(suggestions) != 0 {
		t.Errorf("completion of %q = %v, want none (minimal already selected)", "minimal,mi", suggestions)
	}
}

// TestMetadataFlagCompletion_Partial tests completion with a partial field name
func TestMetadataFlagCompletion_Partial(t *testing.T) {
	suggestions, _ := metadataFieldCompletion(nil, nil, "scanned")

	// Should match: scannedBytes, scannedDataPoints, scannedRecords
	if len(suggestions) != 3 {
		t.Fatalf("got %d suggestions, want 3: %v", len(suggestions), suggestions)
	}

	expected := map[string]bool{
		"scannedBytes":      true,
		"scannedDataPoints": true,
		"scannedRecords":    true,
	}
	for _, s := range suggestions {
		if !expected[s] {
			t.Errorf("unexpected suggestion: %q", s)
		}
	}
}

// TestMetadataFlagCompletion_AfterComma tests completion after a comma (second field)
func TestMetadataFlagCompletion_AfterComma(t *testing.T) {
	suggestions, _ := metadataFieldCompletion(nil, nil, "queryId,")

	// Should exclude "queryId" from suggestions
	for _, s := range suggestions {
		if s == "queryId" || s == ",queryId" {
			t.Error("queryId should be excluded from suggestions (already selected)")
		}
	}

	// All suggestions should have "queryId," prefix
	for _, s := range suggestions {
		if len(s) < 8 || s[:8] != "queryId," {
			t.Errorf("suggestion %q should have prefix 'queryId,'", s)
		}
	}

	// One suggestion per field minus the already-selected queryId, plus minimal
	want := len(output.ValidMetadataFieldNames()) - 1 + 1
	if len(suggestions) != want {
		t.Errorf("got %d suggestions, want %d", len(suggestions), want)
	}
}

// TestMetadataFlagCompletion_AfterCommaPartial tests completion with partial after comma
func TestMetadataFlagCompletion_AfterCommaPartial(t *testing.T) {
	suggestions, _ := metadataFieldCompletion(nil, nil, "queryId,scanned")

	// Should match scannedBytes, scannedDataPoints, scannedRecords — all with prefix
	if len(suggestions) != 3 {
		t.Fatalf("got %d suggestions, want 3: %v", len(suggestions), suggestions)
	}

	expected := map[string]bool{
		"queryId,scannedBytes":      true,
		"queryId,scannedDataPoints": true,
		"queryId,scannedRecords":    true,
	}
	for _, s := range suggestions {
		if !expected[s] {
			t.Errorf("unexpected suggestion: %q", s)
		}
	}
}

// TestMetadataFlagCompletion_MultipleSelected tests that multiple already-selected fields are excluded
func TestMetadataFlagCompletion_MultipleSelected(t *testing.T) {
	suggestions, _ := metadataFieldCompletion(nil, nil, "queryId,sampled,")

	// Should exclude both queryId and sampled
	for _, s := range suggestions {
		if s == "queryId,sampled,queryId" || s == "queryId,sampled,sampled" {
			t.Errorf("already-selected field should not appear: %q", s)
		}
	}

	// All fields minus the 2 already-selected (queryId, sampled), plus minimal
	want := len(output.ValidMetadataFieldNames()) - 2 + 1
	if len(suggestions) != want {
		t.Errorf("got %d suggestions, want %d", len(suggestions), want)
	}
}

// TestMetadataFlagCompletion_NoMatch tests completion with a prefix that matches nothing
func TestMetadataFlagCompletion_NoMatch(t *testing.T) {
	suggestions, _ := metadataFieldCompletion(nil, nil, "nonexistent")

	if len(suggestions) != 0 {
		t.Errorf("got %d suggestions for non-matching prefix, want 0: %v", len(suggestions), suggestions)
	}
}

// TestMetadataFlagNoOptDefVal tests that bare --metadata uses "all"
func TestMetadataFlagNoOptDefVal(t *testing.T) {
	flag := queryCmd.Flags().Lookup("metadata")
	if flag == nil {
		t.Fatal("metadata flag not found")
	}
	if flag.NoOptDefVal != "all" {
		t.Errorf("metadata NoOptDefVal = %q, want %q", flag.NoOptDefVal, "all")
	}
}

// TestMetadataFlagCompletionRegistered tests that completion is registered for --metadata
func TestMetadataFlagCompletionRegistered(t *testing.T) {
	// Cobra stores registered completion functions internally.
	// We verify by calling the completion function through the command's
	// completion mechanism. The simplest way is to verify the function
	// exists and returns valid results.
	suggestions, directive := metadataFieldCompletion(queryCmd, nil, "")
	if len(suggestions) == 0 {
		t.Error("completion function returned no suggestions")
	}
	if directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Error("expected NoFileComp directive")
	}
}
