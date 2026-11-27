package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRenamedCloudFlagsExplainThemselves holds that a removed camelCase cloud
// flag or alias fails as an unknown flag that names its replacement.
func TestRenamedCloudFlagsExplainThemselves(t *testing.T) {
	cases := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"create", "aws", "connection", "--name", "c", "--roleArn", "x"}, "unknown flag: --roleArn (renamed to --role-arn)"},
		{[]string{"create", "aws", "monitoring", "--featureSets", "x"}, "unknown flag: --featureSets (renamed to --feature-sets)"},
		{[]string{"update", "azure", "monitoring", "--featuresets", "x"}, "unknown flag: --featuresets (renamed to --feature-sets)"},
		{[]string{"update", "gcp", "monitoring", "--locationFiltering", "x"}, "unknown flag: --locationFiltering (renamed to --locations)"},
		{[]string{"update", "azure", "connection", "--applicationId", "x"}, "unknown flag: --applicationId (renamed to --application-id)"},
		{[]string{"update", "azure", "connection", "--applicationID", "x"}, "unknown flag: --applicationID (renamed to --application-id)"},
		{[]string{"update", "azure", "connection", "--aplicationID", "x"}, "unknown flag: --aplicationID (renamed to --application-id)"},
		{[]string{"update", "azure", "connection", "--directoryId", "x"}, "unknown flag: --directoryId (renamed to --directory-id)"},
		{[]string{"update", "azure", "connection", "--directoryID", "x"}, "unknown flag: --directoryID (renamed to --directory-id)"},
		{[]string{"create", "azure", "connection", "--clientSecret", "x"}, "unknown flag: --clientSecret (renamed to --client-secret)"},
		{[]string{"update", "gcp", "connection", "--serviceAccountId", "x"}, "unknown flag: --serviceAccountId (renamed to --service-account-id)"},
		{[]string{"update", "gcp", "connection", "--serviceaccountid", "x"}, "unknown flag: --serviceaccountid (renamed to --service-account-id)"},
		{[]string{"enable", "gcp", "monitoring", "--serviceAccountId", "x"}, "unknown flag: --serviceAccountId (renamed to --service-account-id)"},
	}
	for _, tc := range cases {
		t.Run(tc.wantErr, func(t *testing.T) {
			isolatedConfig(t, "contexts: []\n")
			var stderr bytes.Buffer
			code := Run(append(tc.args, "--no-agent"), RunOptions{Stderr: &stderr})
			require.NotZero(t, code)
			require.Contains(t, stderr.String(), tc.wantErr)
		})
	}
}

// TestRenamedCloudFlagHintOnlyWhereReplacementExists holds that a command which
// never took the flag gets cobra's plain error, not a pointer to a flag it
// rejects too.
func TestRenamedCloudFlagHintOnlyWhereReplacementExists(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")
	var stderr bytes.Buffer
	code := Run([]string{"get", "aws", "monitoring", "--featureSets", "x", "--no-agent"}, RunOptions{Stderr: &stderr})
	require.NotZero(t, code)
	require.Contains(t, stderr.String(), "unknown flag")
	require.NotContains(t, stderr.String(), "renamed to")
}

// TestRenamedCloudFlagHintStaysOffNonCloudCommands: `auth login` never had
// --clientSecret, so it must not be told the flag was renamed.
func TestRenamedCloudFlagHintStaysOffNonCloudCommands(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")
	var stderr bytes.Buffer
	code := Run([]string{"auth", "login", "--clientSecret", "x", "--no-agent"}, RunOptions{Stderr: &stderr})
	require.NotZero(t, code)
	require.Contains(t, stderr.String(), "unknown flag")
	require.NotContains(t, stderr.String(), "renamed to")
}

// TestRenamedCloudFlagHintIsStructuredInAgentMode: the agent envelope carries
// the replacement as a suggestion, like the --format hint.
func TestRenamedCloudFlagHintIsStructuredInAgentMode(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")
	var stdout bytes.Buffer
	code := Run([]string{"create", "aws", "connection", "--name", "c", "--roleArn", "x", "--agent"}, RunOptions{Stdout: &stdout})
	require.NotZero(t, code)
	require.Contains(t, stdout.String(), "unknown flag: --roleArn (renamed to --role-arn)")
	require.Contains(t, stdout.String(), `"suggestions"`)
	require.Contains(t, stdout.String(), "did you mean --role-arn?")
}
