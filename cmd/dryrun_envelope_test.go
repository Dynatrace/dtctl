package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/resources/anomalydetector"
)

// These tests cover the dry runs issue #514 left without a plan in agent mode:
// exec api's aligned preview, and the dry runs that wrote their plan to stderr
// with output.PrintInfo, so agent mode got nothing on stdout at all. Each one
// is pinned twice: the human rendering, byte for byte as it was, and the
// envelope agent mode now receives in its place.

// dryRunEnvelope is the agent-mode shape every dry run emits.
type dryRunEnvelope struct {
	OK     bool `json:"ok"`
	Result struct {
		DryRun   bool              `json:"dry_run"`
		Verb     string            `json:"verb"`
		Resource string            `json:"resource"`
		Details  map[string]string `json:"details"`
		Payload  json.RawMessage   `json:"payload"`
		Message  string            `json:"message"`
	} `json:"result"`
}

// runDryRun runs a real invocation with both streams captured and restores the
// flag globals Run sets, like runCLI: a --dry-run or --agent left behind would
// reshape whichever test runs next.
func runDryRun(t *testing.T, stdin io.Reader, argv ...string) (code int, stdout, stderr string) {
	t.Helper()
	origDryRun, origAgentMode, origFormat := gFlags.dryRun, gFlags.agentMode, gFlags.outputFormat
	t.Cleanup(func() {
		gFlags.dryRun, gFlags.agentMode, gFlags.outputFormat = origDryRun, origAgentMode, origFormat
		for _, name := range []string{"dry-run", "agent", "no-agent"} {
			if f := rootCmd.PersistentFlags().Lookup(name); f != nil {
				_ = f.Value.Set("false")
				f.Changed = false
			}
		}
	})
	var out, errOut bytes.Buffer
	code = Run(argv, RunOptions{Stdin: stdin, Stdout: &out, Stderr: &errOut})
	return code, out.String(), errOut.String()
}

// decodeDryRun asserts stdout is exactly one dry-run envelope and returns it.
func decodeDryRun(t *testing.T, stdout string) dryRunEnvelope {
	t.Helper()
	var env dryRunEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdout), &env),
		"agent-mode stdout must be a single JSON document, got:\n%s", stdout)
	require.True(t, env.OK, "a dry run succeeded; it is not an error")
	require.True(t, env.Result.DryRun)
	return env
}

func TestExecAPIDryRun_HumanPreviewUnchanged(t *testing.T) {
	newMockEnvironmentAt(t, "readonly")

	code, out, errOut := runDryRun(t, nil,
		"exec", "api", "/platform/widget/v1/widgets", "-X", "POST", "-d", `{"name":"x"}`,
		"-H", "X-Api-Key: super-secret", "--dry-run", "--no-agent")
	require.Zero(t, code, errOut)

	// The aligned layout of output.DescribeKV, with color off as it is on a
	// pipe, then a blank line and the closing note.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Equal(t, "Method:       POST", lines[0])
	require.Equal(t, "Path:         /platform/widget/v1/widgets", lines[1])
	require.Equal(t, "Headers:      X-Api-Key: <redacted>", lines[2])
	require.Equal(t, "Body:         12 bytes", lines[3])
	require.Equal(t, "Gated as:     create", lines[4])
	require.True(t, strings.HasPrefix(lines[5], "Because:      "), lines[5])
	require.True(t, strings.HasPrefix(lines[6], "Safety:       BLOCKED in this context — "), lines[6])
	require.Equal(t, []string{"", "  Nothing was sent (--dry-run)."}, lines[7:])
}

func TestExecAPIDryRun_AgentModeEmitsEnvelope(t *testing.T) {
	env := newMockEnvironmentAt(t, "readonly")

	_, human, _ := runDryRun(t, nil,
		"exec", "api", "/platform/widget/v1/widgets", "-X", "POST", "-d", `{"name":"x"}`,
		"-H", "X-Api-Key: super-secret", "--dry-run", "--no-agent")

	code, out, errOut := runDryRun(t, nil,
		"exec", "api", "/platform/widget/v1/widgets", "-X", "POST", "-d", `{"name":"x"}`,
		"-H", "X-Api-Key: super-secret", "--dry-run", "--agent")
	require.Zero(t, code, errOut)

	plan := decodeDryRun(t, out)
	require.Equal(t, "exec", plan.Result.Verb)
	require.Equal(t, "api", plan.Result.Resource)
	require.Equal(t, "POST", plan.Result.Details["method"])
	require.Equal(t, "/platform/widget/v1/widgets", plan.Result.Details["path"])
	require.Equal(t, "create", plan.Result.Details["gated_as"])
	require.Equal(t, "12 bytes", plan.Result.Details["body"])
	require.Contains(t, plan.Result.Details["safety"], "BLOCKED in this context")
	require.NotEmpty(t, plan.Result.Details["because"])

	// The envelope is no more willing to leak than the preview: the header is
	// redacted, and the body is reported by size only.
	require.Equal(t, "X-Api-Key: <redacted>", plan.Result.Details["headers"])
	require.NotContains(t, out, "super-secret")
	require.Empty(t, plan.Result.Payload)

	// Same lines as the human preview, without styling.
	require.Equal(t, strings.TrimRight(human, "\n"), plan.Result.Message)

	require.False(t, env.called(http.MethodPost, "/platform/widget/v1/widgets"),
		"a dry run must not send the request, however it renders")
}

func TestExecAPIDryRun_NativeCommandIsItsOwnDetail(t *testing.T) {
	newMockEnvironmentAt(t, "readonly")

	// /platform/automation/ is covered natively by the workflow commands.
	code, out, errOut := runDryRun(t, nil,
		"exec", "api", "/platform/automation/v1/workflows", "--dry-run", "--agent")
	require.Zero(t, code, errOut)

	plan := decodeDryRun(t, out)
	native := plan.Result.Details["native"]
	require.NotEmpty(t, native)
	require.NotContains(t, native, "(preferred)",
		"the detail names the command; the annotation belongs to the human line")
	require.Contains(t, plan.Result.Message, native+" (preferred)")
}

// TestCloudMonitoringToggleDryRuns covers enable/disable aws|gcp|azure
// monitoring. They return before resolving anything, so no endpoint is needed.
// The human plan has always gone to stderr, and still does; agent mode now gets
// it on stdout as an envelope, where it previously got nothing.
func TestCloudMonitoringToggleDryRuns(t *testing.T) {
	cases := []struct {
		argv    []string
		verb    string
		human   string
		details map[string]string
	}{
		{
			argv: []string{"enable", "aws", "monitoring", "--name", "mon-1", "--roleArn", "arn:aws:iam::123456789012:role/r"},
			verb: "enable",
			human: "Dry run: would resolve AWS monitoring config \"mon-1\"\n" +
				"Dry run: would update linked AWS connection roleArn=\"arn:aws:iam::123456789012:role/r\"\n" +
				"Dry run: would enable monitoring config and all credentials\n",
			details: map[string]string{"monitoring_config": "mon-1", "role_arn": "arn:aws:iam::123456789012:role/r"},
		},
		{
			argv: []string{"enable", "gcp", "monitoring", "mon-1", "--serviceAccountId", "sa@example.invalid"},
			verb: "enable",
			human: "Dry run: would resolve GCP monitoring config \"mon-1\"\n" +
				"Dry run: would update linked GCP connection with service account \"sa@example.invalid\"\n" +
				"Dry run: would enable monitoring config and all credentials\n",
			details: map[string]string{"monitoring_config": "mon-1", "service_account_id": "sa@example.invalid"},
		},
		{
			argv: []string{"enable", "azure", "monitoring", "--name", "mon-1", "--directoryId", "dir-1", "--applicationId", "app-1"},
			verb: "enable",
			human: "Dry run: would resolve Azure monitoring config \"mon-1\"\n" +
				"Dry run: would update linked Azure connection directoryId=\"dir-1\" applicationId=\"app-1\"\n" +
				"Dry run: would enable monitoring config and all credentials\n",
			details: map[string]string{"monitoring_config": "mon-1", "directory_id": "dir-1", "application_id": "app-1"},
		},
		{
			argv: []string{"disable", "aws", "monitoring", "--name", "mon-1"},
			verb: "disable",
			human: "Dry run: would resolve AWS monitoring config \"mon-1\"\n" +
				"Dry run: would disable monitoring config and all credentials\n",
			details: map[string]string{"monitoring_config": "mon-1"},
		},
		{
			argv: []string{"disable", "gcp", "monitoring", "mon-1"},
			verb: "disable",
			human: "Dry run: would resolve GCP monitoring config \"mon-1\"\n" +
				"Dry run: would disable monitoring config and all credentials\n",
			details: map[string]string{"monitoring_config": "mon-1"},
		},
		{
			argv: []string{"disable", "azure", "monitoring", "--name", "mon-1"},
			verb: "disable",
			human: "Dry run: would resolve Azure monitoring config \"mon-1\"\n" +
				"Dry run: would disable monitoring config and all credentials\n",
			details: map[string]string{"monitoring_config": "mon-1"},
		},
	}

	for _, tc := range cases {
		t.Run(strings.Join(tc.argv[:2], " "), func(t *testing.T) {
			env := newMockEnvironmentAt(t, "readonly")

			code, out, errOut := runDryRun(t, nil, append(tc.argv, "--dry-run", "--no-agent")...)
			require.Zero(t, code, errOut)
			require.Empty(t, out, "the human plan has always been on stderr")
			// GCP commands add a preview notice on stderr; the plan is what follows.
			require.True(t, strings.HasSuffix(errOut, tc.human), "stderr:\n%s", errOut)

			code, out, errOut = runDryRun(t, nil, append(tc.argv, "--dry-run", "--agent")...)
			require.Zero(t, code, errOut)
			plan := decodeDryRun(t, out)
			require.Equal(t, tc.verb, plan.Result.Verb)
			require.Equal(t, tc.details, plan.Result.Details)
			require.Equal(t, strings.TrimRight(tc.human, "\n"), plan.Result.Message)
			require.NotContains(t, errOut, "Dry run:", "agent mode must not also print the plan as prose")

			require.Zero(t, env.requestCount(), "these dry runs send nothing at all")
		})
	}
}

const syntheticDashboardYAML = `name: Synthetic Dashboard
description: A synthetic dashboard
type: dashboard
content:
  version: 15
  tiles:
    "0": {type: markdown, content: one}
    "1": {type: markdown, content: two}
  layouts:
    "0": {x: 0, y: 0, w: 4, h: 2}
    "1": {x: 4, y: 0, w: 4, h: 2}
`

func TestCreateDocumentDryRun_BothRenderings(t *testing.T) {
	newMockEnvironmentAt(t, "readonly")

	code, out, errOut := runDryRun(t, strings.NewReader(syntheticDashboardYAML),
		"create", "dashboard", "-f", "-", "--dry-run", "--no-agent")
	require.Zero(t, code, errOut)
	require.Empty(t, out)
	human := "Dry run: would create dashboard\n" +
		"  Name: Synthetic Dashboard\n" +
		"  Description: A synthetic dashboard\n" +
		"  Tiles: 2\n" +
		"\n" +
		"Document structure validated successfully\n"
	require.Equal(t, human, errOut)

	code, out, errOut = runDryRun(t, strings.NewReader(syntheticDashboardYAML),
		"create", "dashboard", "-f", "-", "--dry-run", "--agent")
	require.Zero(t, code, errOut)
	plan := decodeDryRun(t, out)
	require.Equal(t, "create", plan.Result.Verb)
	require.Equal(t, "dashboard", plan.Result.Resource)
	require.Equal(t, map[string]string{
		"type":        "dashboard",
		"name":        "Synthetic Dashboard",
		"description": "A synthetic dashboard",
		"tiles":       "2",
	}, plan.Result.Details)
	require.Equal(t, strings.TrimRight(human, "\n"), plan.Result.Message)
}

const syntheticDetectorYAML = `title: Synthetic detector
description: synthetic
enabled: true
source: synthetic
analyzer:
  name: dt.statistics.GenericStaticThresholdAnalyzer
  input:
    - key: query
      value: timeseries avg(dt.host.cpu.usage)
    - key: threshold
      value: "90"
    - key: alertCondition
      value: ABOVE
`

func TestCreateAnomalyDetectorDryRun_VerdictInEnvelope(t *testing.T) {
	t.Run("validation passed", func(t *testing.T) {
		env := newMockEnvironmentAt(t, "readonly")
		env.respond(http.MethodPost, anomalydetector.SettingsAPI, mockResponse{
			status: http.StatusOK, contentType: "application/json", body: `[{"code":200}]`,
		})

		code, out, errOut := runDryRun(t, strings.NewReader(syntheticDetectorYAML),
			"create", "anomaly-detector", "-f", "-", "--dry-run", "--no-agent")
		require.Zero(t, code, errOut)
		require.Empty(t, out)
		require.True(t, strings.HasPrefix(errOut, "Dry run: would create anomaly detector\n---\n{"), errOut)
		require.Contains(t, errOut, "---\n")
		require.True(t, strings.HasSuffix(errOut, "Schema validation passed\n"), errOut)

		code, out, errOut = runDryRun(t, strings.NewReader(syntheticDetectorYAML),
			"create", "anomaly-detector", "-f", "-", "--dry-run", "--agent")
		require.Zero(t, code, errOut)
		plan := decodeDryRun(t, out)
		require.Equal(t, "anomaly-detector", plan.Result.Resource)
		require.Equal(t, "passed", plan.Result.Details["schema_validation"])

		// The payload is the body that would be sent, as JSON, so an agent can
		// diff it against the real request.
		var body map[string]any
		require.NoError(t, json.Unmarshal(plan.Result.Payload, &body))
		require.Equal(t, "builtin:davis.anomaly-detectors", body["schemaId"])
		require.True(t, strings.HasPrefix(plan.Result.Message, "Dry run: would create anomaly detector\n---\n"))
		require.Empty(t, errOut, "the verdict is in the envelope; nothing needs saying on stderr")
	})

	t.Run("validation unavailable", func(t *testing.T) {
		env := newMockEnvironmentAt(t, "readonly")
		env.respond(http.MethodPost, anomalydetector.SettingsAPI, mockResponse{
			status: http.StatusServiceUnavailable, body: `{"error":{"code":503}}`,
		})

		code, out, errOut := runDryRun(t, strings.NewReader(syntheticDetectorYAML),
			"create", "anomaly-detector", "-f", "-", "--dry-run", "--agent")
		require.Zero(t, code, errOut)
		plan := decodeDryRun(t, out)
		require.True(t, strings.HasPrefix(plan.Result.Details["schema_validation"], "skipped: "),
			plan.Result.Details["schema_validation"])
		require.Contains(t, errOut, "schema validation skipped", "a skipped check is still worth a warning")
	})

	t.Run("definition rejected", func(t *testing.T) {
		env := newMockEnvironmentAt(t, "readonly")
		env.respond(http.MethodPost, anomalydetector.SettingsAPI, mockResponse{
			status: http.StatusBadRequest, contentType: "application/json",
			body: `{"error":{"code":400,"message":"synthetic rejection"}}`,
		})

		// A human still sees the body before the error, as before.
		code, _, errOut := runDryRun(t, strings.NewReader(syntheticDetectorYAML),
			"create", "anomaly-detector", "-f", "-", "--dry-run", "--no-agent")
		require.NotZero(t, code)
		require.True(t, strings.HasPrefix(errOut, "Dry run: would create anomaly detector\n"), errOut)

		// An agent gets one document: the error, not a plan followed by an error.
		code, out, _ := runDryRun(t, strings.NewReader(syntheticDetectorYAML),
			"create", "anomaly-detector", "-f", "-", "--dry-run", "--agent")
		require.NotZero(t, code)
		var resp struct {
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &resp), out)
		require.False(t, resp.OK)
		require.NotContains(t, out, `"dry_run"`)
	})
}

func TestAccountDeleteTokenDryRun_BothRenderings(t *testing.T) {
	newMockEnvironmentAt(t, "readonly")
	t.Setenv("DTCTL_DEVELOPMENT", "account")

	code, out, errOut := runDryRun(t, nil, "account", "delete", "token", "tok-1", "--dry-run", "--no-agent")
	require.Zero(t, code, errOut)
	require.Empty(t, out)
	require.Equal(t, "Dry run: would delete platform token \"tok-1\"\n", errOut)

	code, out, errOut = runDryRun(t, nil, "account", "delete", "token", "tok-1", "--dry-run", "--agent")
	require.Zero(t, code, errOut)
	plan := decodeDryRun(t, out)
	// The tree alone would report the group ("account") as the verb.
	require.Equal(t, "delete", plan.Result.Verb)
	require.Equal(t, "token", plan.Result.Resource)
	require.Equal(t, map[string]string{"id": "tok-1"}, plan.Result.Details)
}

func TestDryRunReport_KVAndStderr(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"exec", "api"})
	require.NoError(t, err)

	build := func() *dryRunReport {
		return newDryRunReport(cmd).
			KV("Method:", 14, "%s", "GET").
			KV("A-very-long-label:", 4, "%s", "v").
			Linef("note")
	}

	t.Run("human KV lines align like DescribeKV", func(t *testing.T) {
		withAgentMode(t, false)
		out := captureScopeStdout(t, func() { require.NoError(t, build().Print()) })
		require.Equal(t, "Method:       GET\nA-very-long-label: v\nnote\n", out)
	})

	t.Run("OnStderr keeps stdout clean for humans", func(t *testing.T) {
		withAgentMode(t, false)
		out := captureScopeStdout(t, func() { require.NoError(t, build().OnStderr().Print()) })
		require.Empty(t, out)
	})

	t.Run("agent envelope goes to stdout even with OnStderr", func(t *testing.T) {
		withAgentMode(t, true)
		out := captureScopeStdout(t, func() { require.NoError(t, build().OnStderr().Print()) })
		plan := decodeDryRun(t, out)
		require.Equal(t, map[string]string{"method": "GET", "a_very_long_label": "v"}, plan.Result.Details)
		require.Equal(t, "Method:       GET\nA-very-long-label: v\nnote", plan.Result.Message)
	})
}
