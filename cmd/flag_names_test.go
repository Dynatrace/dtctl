package cmd

import (
	"bytes"
	"regexp"
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// kebabFlagName is the one spelling every flag name has: lower-case words
// joined by single hyphens, starting with a letter.
var kebabFlagName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// flagNameExemptions lists flags that predate the rule and are not kebab-case.
// Each needs a reason; the list can only shrink.
var flagNameExemptions = map[string]string{}

// TestEveryFlagNameIsKebabCase walks the whole command tree, hidden commands
// and flags included, and holds every flag name (and so every alias, which is
// a flag of its own) to kebab-case. The aws, azure and gcp commands used
// camelCase and spelling aliases until 1.0 (#525).
func TestEveryFlagNameIsKebabCase(t *testing.T) {
	seen := map[string]bool{}
	var bad []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			if kebabFlagName.MatchString(f.Name) {
				return
			}
			key := c.CommandPath() + " --" + f.Name
			if _, ok := flagNameExemptions[f.Name]; ok {
				seen[f.Name] = true
				return
			}
			bad = append(bad, key)
		}
		c.Flags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	sort.Strings(bad)
	require.Empty(t, bad, "flag names must match %s", kebabFlagName)
	for name := range flagNameExemptions {
		require.True(t, seen[name], "exemption --%s no longer matches a flag; remove it", name)
	}
}

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
