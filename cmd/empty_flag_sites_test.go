package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
)

type flagSite struct {
	cmd  *cobra.Command
	flag string
}

func (s flagSite) String() string { return s.cmd.CommandPath() + " --" + s.flag }

// requiredNonEmptyFlags are the flags a command cannot run without (#567
// groups 1 and 2). They are marked required, so a flag left out fails the
// parse, and an explicitly empty value fails it too — with the usage code, not
// the runtime error a check in RunE used to produce.
var requiredNonEmptyFlags = []flagSite{
	// Group 1: marked required by #557; the RunE check was unreachable.
	{applyCmd, "file"},
	{applyExtensionConfigCmd, "file"},
	{createSettingsCmd, "file"},
	{createSettingsCmd, "schema"},
	{createSettingsCmd, "scope"},
	{createSchedulingRuleCmd, "file"},
	{createAzureMonitoringConfigCmd, "name"},
	{createAzureMonitoringConfigCmd, "credentials"},
	{createGCPMonitoringConfigCmd, "name"},
	{createGCPMonitoringConfigCmd, "credentials"},
	{createDocumentCmd, "file"},
	{createNotebookCmd, "file"},
	{createDashboardCmd, "file"},
	{createAnomalyDetectorCmd, "file"},
	{createLookupCmd, "file"},
	{createSLOCmd, "file"},
	{createWorkflowCmd, "file"},
	{updateDocumentCmd, "file"},
	{describeExtensionConfigCmd, "config-id"},
	{waitQueryCmd, "for"},

	// Group 2: always required, previously checked only in RunE.
	{createAWSConnectionCmd, "name"},
	{createAWSMonitoringConfigCmd, "name"},
	{createAWSMonitoringConfigCmd, "credentials"},
	{updateAWSConnectionCmd, "roleArn"},
	{createSegmentCmd, "file"},
	{execPreviewProcessorCmd, "file"},
	{downloadExtensionCmd, "version"},
	{aliasExportCmd, "file"},
	{aliasImportCmd, "file"},
	{configSetCredentialsCmd, "token"},
	{accountCreateTokenCmd, "name"},
	{accountCreateTokenCmd, "scope"},
}

// optionalNonEmptyFlags are required only in some modes, or are one of several
// alternatives (#567 group 3), so they must stay unmarked: marking one would
// break the invocations that do without it. An explicitly empty value is still
// rejected, and the command body keeps reporting the flag left out.
var optionalNonEmptyFlags = []flagSite{
	{createBucketCmd, "name"},
	{createBucketCmd, "table"},
	{createBucketCmd, "retention"},
	{createEdgeConnectCmd, "name"},
	{createGCPConnectionCmd, "name"},
	{ctxSetCmd, "environment"},
	{configSetContextCmd, "environment"},
	{authLoginCmd, "environment"},
	{getSettingsCmd, "schema"},
	{createExtensionCmd, "file"},
	{createExtensionCmd, "hub-extension"},
	{shareDocumentCmd, "user"},
	{shareDocumentCmd, "group"},
	{shareNotebookCmd, "user"},
	{shareDashboardCmd, "group"},
	{unshareDocumentCmd, "user"},
	{unshareDashboardCmd, "group"},
	{updateAWSMonitoringConfigCmd, "regions"},
	{updateAWSMonitoringConfigCmd, "featureSets"},
	{updateAzureConnectionCmd, "directoryId"},
	{updateAzureConnectionCmd, "directoryID"},
	{updateAzureConnectionCmd, "applicationId"},
	{updateAzureConnectionCmd, "applicationID"},
	{updateAzureConnectionCmd, "aplicationID"},
	{updateAzureConnectionCmd, "clientSecret"},
	{updateAzureMonitoringConfigCmd, "locationFiltering"},
	{updateAzureMonitoringConfigCmd, "featureSets"},
	{updateAzureMonitoringConfigCmd, "featuresets"},
	{updateGCPConnectionCmd, "serviceAccountId"},
	{updateGCPConnectionCmd, "serviceaccountid"},
	{updateGCPMonitoringConfigCmd, "locationFiltering"},
	{updateGCPMonitoringConfigCmd, "featureSets"},
	{updateGCPMonitoringConfigCmd, "featuresets"},
	{updateExtensionCmd, "version"},
	{execDQLCmd, "file"},
	{queryCmd, "file"},
	{verifyQueryCmd, "file"},
	{waitQueryCmd, "file"},
	{verifyOpenPipelineDQLProcessorCmd, "file"},
	{verifyOpenPipelineMatcherCmd, "file"},
	{execCopilotCmd, "file"},
	{execCopilotNl2DqlCmd, "file"},
	{execCopilotDql2NlCmd, "file"},
	{execAnalyzerCmd, "file"},
	{execAnalyzerCmd, "input"},
	{execAnalyzerCmd, "query"},
	{verifyAnalyzerCmd, "file"},
	// Always required, but deliberately not marked: cobra's required-flag
	// error would preempt a more useful message in RunE (the manifest hint,
	// the reason an unscoped window is not offered).
	{createLookupCmd, "path"},
	{createLookupCmd, "lookup-field"},
	{inventoryArrivalsCmd, "scope"},
}

// assertEmptyValueRejected parses `--flag <value>` on the site's command and
// asserts the parse fails the way #557 defined it: errEmptyFlagValue, the flag
// named, the usage exit code and validation_error in agent mode.
func assertEmptyValueRejected(t *testing.T, site flagSite, value string) {
	t.Helper()
	defer resetFlagSet(site.cmd.Flags())

	err := site.cmd.ParseFlags([]string{"--" + site.flag, value})
	if err == nil {
		t.Fatalf("--%s %q parsed; want it rejected", site.flag, value)
	}
	err = enhanceFlagError(site.cmd, err)
	if !errors.Is(err, errEmptyFlagValue) {
		t.Fatalf("error = %q, want it to wrap errEmptyFlagValue", err)
	}
	if want := "--" + site.flag + " "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want it to name the flag (prefix %q)", err, want)
	}
	if code := exitCodeForError(err); code != client.ExitUsageError {
		t.Errorf("exit code = %d, want %d", code, client.ExitUsageError)
	}
	if detail := errorToDetail(context.Background(), err); detail.Code != "validation_error" {
		t.Errorf("agent code = %q, want validation_error", detail.Code)
	}
}

func isMarkedRequired(cmd *cobra.Command, name string) bool {
	got := cmd.Flags().Lookup(name).Annotations[cobra.BashCompOneRequiredFlag]
	return len(got) > 0 && got[0] == "true"
}

// TestRequiredFlagsRejectEmptyValue covers #567 groups 1 and 2.
func TestRequiredFlagsRejectEmptyValue(t *testing.T) {
	for _, site := range requiredNonEmptyFlags {
		t.Run(site.String(), func(t *testing.T) {
			if !isMarkedRequired(site.cmd, site.flag) {
				t.Errorf("--%s is not marked required", site.flag)
			}
			assertEmptyValueRejected(t, site, "")
			assertEmptyValueRejected(t, site, " ")
		})
	}
}

// TestOptionalFlagsRejectEmptyValue covers #567 group 3.
func TestOptionalFlagsRejectEmptyValue(t *testing.T) {
	for _, site := range optionalNonEmptyFlags {
		t.Run(site.String(), func(t *testing.T) {
			if isMarkedRequired(site.cmd, site.flag) {
				t.Errorf("--%s is marked required; an invocation without it is valid", site.flag)
			}
			assertEmptyValueRejected(t, site, "")
		})
	}
}

// TestEmptyRepeatableFlagItemIsRejected: for a repeatable flag, one empty
// occurrence fails the parse even next to a valid one — `share --user ""`
// used to send an empty SSO ID to the API.
func TestEmptyRepeatableFlagItemIsRejected(t *testing.T) {
	defer resetFlagSet(shareDocumentCmd.Flags())

	err := shareDocumentCmd.ParseFlags([]string{"--user", "alice", "--user", ""})
	if !errors.Is(err, errEmptyFlagValue) {
		t.Fatalf("error = %v, want errEmptyFlagValue", err)
	}
}

// TestAccountTokenSeparatorOnlyScopeIsAUsageError: `--scope ,` is not blank,
// so the parse accepts it, but it names no scope. The command body reports it
// with the same usage error as `--scope ""`.
func TestAccountTokenSeparatorOnlyScopeIsAUsageError(t *testing.T) {
	defer resetFlagSet(accountCreateTokenCmd.Flags())

	if err := accountCreateTokenCmd.ParseFlags([]string{"--name", "ci", "--scope", ", ,"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	err := accountCreateTokenCmd.RunE(accountCreateTokenCmd, nil)
	if !errors.Is(err, errEmptyFlagValue) {
		t.Fatalf("error = %v, want errEmptyFlagValue", err)
	}
	if code := exitCodeForError(err); code != client.ExitUsageError {
		t.Errorf("exit code = %d, want %d", code, client.ExitUsageError)
	}
}

// TestEmptyFlagEnvelopeForBodyCheckedFlags runs whole invocations for sites
// whose RunE used to report the empty value (exit 1, agent code "error").
func TestEmptyFlagEnvelopeForBodyCheckedFlags(t *testing.T) {
	tests := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"create aws connection", createAWSConnectionCmd, []string{"create", "aws", "connection", "--name", ""}},
		{"config set-credentials", configSetCredentialsCmd, []string{"config", "set-credentials", "x", "--token", ""}},
		{"create bucket", createBucketCmd, []string{"create", "bucket", "--name", "", "--table", "logs", "--retention", "35"}},
		{"query file", queryCmd, []string{"query", "--file", ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				resetFlagSet(tt.cmd.Flags())
				resetFlagSet(rootCmd.PersistentFlags())
			})

			var code int
			out := captureStdout(t, func() { code = Run(append([]string{"--agent"}, tt.args...), RunOptions{}) })

			if code != client.ExitUsageError {
				t.Errorf("exit code = %d, want %d", code, client.ExitUsageError)
			}
			var resp struct {
				OK    bool `json:"ok"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatalf("stdout is not an envelope: %v\n%s", err, out)
			}
			if resp.OK || resp.Error.Code != "validation_error" {
				t.Errorf("envelope ok=%v code=%q, want ok=false code=validation_error", resp.OK, resp.Error.Code)
			}
		})
	}
}

// TestNonEmptySliceFlagResetClears: a wrapped repeatable flag must still reset
// through Replace. Set appends, so resetting it with Set would carry the
// previous invocation's items into the next one.
func TestNonEmptySliceFlagResetClears(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().StringArray("user", nil, "")
	rejectEmptyFlag(cmd, "user")

	if err := cmd.ParseFlags([]string{"--user", "a", "--user", "b"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got, _ := cmd.Flags().GetStringArray("user"); strings.Join(got, ",") != "a,b" {
		t.Fatalf("--user = %v, want [a b]", got)
	}
	resetFlagSet(cmd.Flags())

	if got, _ := cmd.Flags().GetStringArray("user"); len(got) != 0 {
		t.Errorf("after reset --user = %v, want it empty", got)
	}
	if err := cmd.ParseFlags([]string{"--user", "c"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got, _ := cmd.Flags().GetStringArray("user"); strings.Join(got, ",") != "c" {
		t.Errorf("next invocation --user = %v, want [c]", got)
	}
}
