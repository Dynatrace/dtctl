package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/resources/awsconnection"
)

// The cloud-integration dry runs resolve the object they would change before
// they print anything, so they are only reachable with an environment that
// answers. These tests give them the first coverage they have had: the human
// lines they print, and the agent envelope they now emit instead of prose on
// stdout.

// runCLI runs a real invocation through Run and restores the flag globals it
// sets. rootCmd and those globals are package state: a --dry-run left behind
// here turns a later test's real run into a preview, which is exactly how this
// file first broke the config teardown tests.
func runCLI(t *testing.T, argv ...string) (int, string) {
	t.Helper()
	origDryRun, origAgentMode, origFormat := dryRun(context.Background()), agentMode(context.Background()), outputFormat(context.Background())
	t.Cleanup(func() {
		gFlags.dryRun, gFlags.agentMode, gFlags.outputFormat = origDryRun, origAgentMode, origFormat
		for _, name := range []string{"dry-run", "agent", "no-agent"} {
			if f := rootCmd.PersistentFlags().Lookup(name); f != nil {
				_ = f.Value.Set("false")
				f.Changed = false
			}
		}
	})
	return captureRun(t, argv, RunOptions{})
}

// respondWithAWSConnection makes FindByName resolve to one role-based
// connection named "conn-1".
func respondWithAWSConnection(m *mockEnvironment) {
	m.respond(http.MethodGet, awsconnection.SettingsAPI, mockResponse{
		status:      http.StatusOK,
		contentType: "application/json",
		body: `{"items":[{"objectId":"obj-aws-1","schemaId":"` + awsconnection.SchemaID + `",
			"value":{"name":"conn-1","type":"awsRoleBasedAuthentication",
			"awsRoleBasedAuthentication":{"roleArn":"arn:aws:iam::1:role/old","consumers":["metrics"]}}}],
			"totalCount":1}`,
	})
}

func TestUpdateAWSConnectionDryRun_HumanLinesUnchanged(t *testing.T) {
	m := newMockEnvironmentAt(t, "readwrite")
	respondWithAWSConnection(m)

	code, out := runCLI(t,
		"update", "aws", "connection",
		"--name", "conn-1",
		"--roleArn", "arn:aws:iam::1:role/new",
		"--dry-run", "--no-agent")
	require.Zero(t, code, out)

	require.Equal(t,
		"Dry run: would update AWS connection obj-aws-1\nRole ARN: arn:aws:iam::1:role/new\n",
		out)

	// A dry run must not write, however it renders.
	require.False(t, m.called(http.MethodPut, awsconnection.SettingsAPI+"/obj-aws-1"),
		"a dry run must not send the update")
}

func TestUpdateAWSConnectionDryRun_AgentModeEmitsEnvelope(t *testing.T) {
	m := newMockEnvironmentAt(t, "readwrite")
	respondWithAWSConnection(m)

	code, out := runCLI(t,
		"update", "aws", "connection",
		"--name", "conn-1",
		"--roleArn", "arn:aws:iam::1:role/new",
		"--dry-run", "--agent")
	require.Zero(t, code, out)

	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			DryRun   bool              `json:"dry_run"`
			Verb     string            `json:"verb"`
			Resource string            `json:"resource"`
			Details  map[string]string `json:"details"`
			Message  string            `json:"message"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp),
		"agent-mode stdout must be a single JSON document, got:\n%s", out)

	require.True(t, resp.OK)
	require.True(t, resp.Result.DryRun)
	require.Equal(t, "update", resp.Result.Verb)
	require.Equal(t, "obj-aws-1", resp.Result.Details["object_id"])
	require.Equal(t, "arn:aws:iam::1:role/new", resp.Result.Details["role_arn"])
	require.Contains(t, resp.Result.Message, "Dry run: would update AWS connection obj-aws-1")

	require.False(t, m.called(http.MethodPut, awsconnection.SettingsAPI+"/obj-aws-1"))
}

func TestCreateAWSConnectionDryRun_BothRenderings(t *testing.T) {
	newMockEnvironmentAt(t, "readwrite")

	code, human := runCLI(t,
		"create", "aws", "connection",
		"--name", "conn-new",
		"--roleArn", "arn:aws:iam::1:role/r",
		"--dry-run", "--no-agent")
	require.Zero(t, code, human)
	require.Equal(t,
		"Dry run: would create AWS connection\nName: conn-new\nRole ARN: arn:aws:iam::1:role/r\n",
		human)

	code, agent := runCLI(t,
		"create", "aws", "connection",
		"--name", "conn-new",
		"--roleArn", "arn:aws:iam::1:role/r",
		"--dry-run", "--agent")
	require.Zero(t, code, agent)

	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			DryRun  bool              `json:"dry_run"`
			Details map[string]string `json:"details"`
			Message string            `json:"message"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(agent), &resp), agent)
	require.True(t, resp.OK)
	require.True(t, resp.Result.DryRun)
	require.Equal(t, "conn-new", resp.Result.Details["name"])
	require.Equal(t, "arn:aws:iam::1:role/r", resp.Result.Details["role_arn"])

	// Both renderings carry the same lines; only the wrapping differs.
	require.Equal(t, strings.TrimRight(human, "\n"), resp.Result.Message)
}
