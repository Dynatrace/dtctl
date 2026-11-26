package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// redisRows is an environment that runs a cache under the name a manifest in
// the repository deploys it as, and nothing under the repository's own name.
var redisRows = map[string]string{
	"spans": `{` + flowCluster + `"k8s.namespace.name":"shared-infra","k8s.workload.name":"redis-cache","records":"900",` +
		`"serviceCount":"0","services":[],"processGroupCount":"0","processGroups":[]}`,
}

// chdirCheckoutWithRedis is a repository whose build file names it checkout
// and which also holds the manifest of the cache it deploys beside itself.
func chdirCheckoutWithRedis(t *testing.T) {
	t.Helper()
	writeRepoFiles(t, chdirTestRepo(t, ""), map[string]string{
		"go.mod": "module example.invalid/checkout\n\ngo 1.22\n",
		"deploy/redis.yaml": `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: redis-cache
spec:
  template:
    spec:
      containers:
        - name: redis
          image: redis:7.2
`,
	})
}

// A candidate found only by a name the repository holds without its being
// the unit's own is shown and never proposed: no verdict of match, no line
// to run in any output, and one note that says why and how to confirm it.
// Confirmed with the term, the same candidate is a match with its line.
func TestRepoScopeDiscoverIndirectEvidenceProposesNoLine(t *testing.T) {
	newDQLRecorder(t, "readonly", rowsByObject(redisRows))
	chdirCheckoutWithRedis(t)
	const (
		note = "candidate 1 has no set line: it is named only by a manifest or chart that is not named like the unit, " +
			"so nothing shows that it runs this code; if it does, run discover again with --term redis-cache"
		line = "dtctl --context c repo-scope set redis-cache --cluster-uid " + flowClusterUID + " --namespace shared-infra --workload redis-cache"
	)

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--plain"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, stdout, "\nVerdict: ambiguous: one candidate, named only by a manifest or chart that is not named like the unit\n")
	require.Contains(t, stdout, "Nothing was written. No candidate is named by the unit itself (see the notes).\n")
	require.Contains(t, stdout, "\nNote: "+note+"\n")
	require.NotContains(t, stdout, "repo-scope set redis", "no line that saves the cache as this repository's scope")
	require.NotContains(t, stdout, "--workload redis-cache")

	for _, format := range []string{"json", "yaml", "toon"} {
		code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "-o", format}, RunOptions{})
		require.Zero(t, code, stderr)
		require.NotContains(t, stdout, "repo-scope set redis", format)
		require.NotContains(t, stdout, "setCommand", format)
		require.Contains(t, stdout, "--term redis-cache", format)
	}
	code, stdout, stderr = runRepoScoped([]string{"repo-scope", "discover", "-o", "json"}, RunOptions{})
	require.Zero(t, code, stderr)
	var reports []reposcope.DiscoveryReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &reports))
	require.Equal(t, reposcope.VerdictAmbiguous, reports[0].Verdict)
	require.Equal(t, "named only by a manifest or chart that is not named like the unit", reports[0].Candidates[0].Indirect)
	require.Contains(t, reports[0].Notes, note)

	report, suggestions := discoverReport(t)
	require.Equal(t, reposcope.VerdictAmbiguous, report.Verdict)
	require.Empty(t, report.Candidates[0].SetCommand)
	require.Contains(t, suggestions, note)
	for _, s := range suggestions {
		require.NotContains(t, s, "repo-scope set redis")
		require.NotContains(t, s, "run its setCommand", "there is none to run")
	}

	report, suggestions = discoverReport(t, "--term", "redis-cache")
	require.Equal(t, reposcope.VerdictMatch, report.Verdict)
	require.Empty(t, report.Candidates[0].Indirect)
	require.Equal(t, line, report.Candidates[0].SetCommand)
	require.Equal(t, "confirm with the user, then: "+line, suggestions[0])
}

// Two candidates that share a service name, both found by indirect names:
// the line that selects every candidate reporting the name is no better
// founded than a line for either, so it is not proposed.
func TestRepoScopeDiscoverIndirectEvidenceProposesNoSharedLine(t *testing.T) {
	row := func(namespace string) string {
		return `{"k8s.namespace.name":"` + namespace + `","service.name":"redis-cache","records":"10",` +
			`"serviceCount":"0","services":[],"processGroupCount":"0","processGroups":[]}`
	}
	newDQLRecorder(t, "readonly", rowsByObject(map[string]string{"spans": row("shared-infra") + "," + row("staging")}))
	chdirCheckoutWithRedis(t)

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "-o", "json"}, RunOptions{})
	require.Zero(t, code, stderr)
	var reports []reposcope.DiscoveryReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &reports))
	require.Len(t, reports[0].Candidates, 2)
	require.NotContains(t, stdout, "repo-scope set redis")
	require.NotContains(t, stdout, "--service-name redis-cache")
}

// The same line is proposed when one of the candidates reporting the name is
// the unit's own workload: the name is then tied to this code by more than
// the manifest that spells it.
func TestRepoScopeDiscoverSharedLineNeedsOneDirectCandidate(t *testing.T) {
	const tail = `"service.name":"redis-cache","records":"10","serviceCount":"0","services":[],"processGroupCount":"0","processGroups":[]}`
	newDQLRecorder(t, "readonly", rowsByObject(map[string]string{
		"spans": `{"k8s.workload.name":"checkout",` + tail + `,{"k8s.namespace.name":"shared-infra",` + tail}))
	chdirCheckoutWithRedis(t)

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "-o", "json"}, RunOptions{})
	require.Zero(t, code, stderr)
	var reports []reposcope.DiscoveryReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &reports))
	require.Len(t, reports[0].Candidates, 2)
	require.Empty(t, reports[0].Candidates[0].Indirect, "the workload is named by go.mod")
	require.NotEmpty(t, reports[0].Candidates[1].Indirect)
	for _, c := range reports[0].Candidates {
		require.Empty(t, c.SetCommand)
	}
	require.Contains(t, stdout, "the line that does: dtctl --context c repo-scope set redis-cache --service-name redis-cache")
}
