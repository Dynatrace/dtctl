package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/usage"
)

// usageStateDir points the XDG state directory (where the usage ledger lives)
// at a fresh temp dir for the test and returns it.
func usageStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", dir)
	xdg.Reload()
	t.Cleanup(xdg.Reload)
	return dir
}

// usageQueryServer answers every DQL execute with a finished result that
// scanned a known volume, and counts the executions.
func usageQueryServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var executions atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/platform/storage/query/v1/query:execute" {
			executions.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"SUCCEEDED","result":{"records":[{"n":1}],
			"metadata":{"grail":{"scannedBytes":3221225472,"scannedRecords":1200,"scannedDataPoints":0}}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &executions
}

func usageConfig(env string, tracking bool) string {
	cfg := `apiVersion: v1
kind: Config
current-context: dev
contexts:
  - name: dev
    context:
      environment: ` + env + `
      token-ref: dev
`
	if tracking {
		cfg += "      usage-tracking: true\n"
	}
	cfg += `  - name: other
    context:
      environment: https://other.example.invalid
      token-ref: dev
tokens:
  - name: dev
    token: dt0s16.SYNTHETIC.TOKEN
`
	return cfg
}

func runUsageCLI(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(argv, RunOptions{Stdout: &stdout, Stderr: &stderr})
	return code, stdout.String(), stderr.String()
}

func decodeUsageRows(t *testing.T, out string) []usageRow {
	t.Helper()
	var rows []usageRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows), "output: %s", out)
	return rows
}

// TestUsageTrackingOffWritesNothing is the opt-in guarantee: a context without
// usage-tracking runs queries and dtctl leaves no file, not even a directory.
func TestUsageTrackingOffWritesNothing(t *testing.T) {
	state := usageStateDir(t)
	srv, executions := usageQueryServer(t)
	isolatedConfig(t, usageConfig(srv.URL, false))

	code, _, stderr := runUsageCLI(t, "query", "fetch logs", "-o", "json", "--plain")
	require.Zero(t, code, stderr)
	require.EqualValues(t, 1, executions.Load())

	code, out, stderr := runUsageCLI(t, "usage", "-o", "json", "--plain")
	require.Zero(t, code, stderr)
	rows := decodeUsageRows(t, out)
	require.Len(t, rows, 3)
	for _, r := range rows {
		require.Equal(t, "dev", r.Context)
		require.Zero(t, r.Queries)
		require.False(t, r.Tracking)
	}
	require.Contains(t, stderr, `usage tracking is off for context "dev"`)
	require.Contains(t, stderr, "dtctl config set-context dev --usage-tracking")

	_, err := os.Stat(state)
	require.True(t, os.IsNotExist(err), "tracking off must not create %s (err=%v)", state, err)
}

func TestUsageTrackingOnRecordsEveryQuery(t *testing.T) {
	state := usageStateDir(t)
	srv, _ := usageQueryServer(t)
	isolatedConfig(t, usageConfig(srv.URL, true))

	for i := 0; i < 2; i++ {
		code, _, stderr := runUsageCLI(t, "query", "fetch logs", "-o", "json", "--plain")
		require.Zero(t, code, stderr)
	}

	ledgerPath := filepath.Join(state, "dtctl", usage.FileName)
	raw, err := os.ReadFile(ledgerPath)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "fetch logs", "the ledger must never store query text")
	require.NotContains(t, string(raw), "SYNTHETIC", "the ledger must never store tokens")
	require.NotContains(t, string(raw), srv.URL, "the ledger must never store the environment URL")

	code, out, stderr := runUsageCLI(t, "usage", "-o", "json", "--plain")
	require.Zero(t, code, stderr)
	rows := decodeUsageRows(t, out)
	require.Len(t, rows, 3)
	require.Equal(t, []string{"today", "7d", "30d"}, []string{rows[0].Period, rows[1].Period, rows[2].Period})
	for _, r := range rows {
		require.EqualValues(t, 2, r.Queries, r.Period)
		require.EqualValues(t, 2*3221225472, r.ScannedBytes)
		require.EqualValues(t, 2400, r.ScannedRecords)
		require.Equal(t, "6.00 GiB", r.Scanned)
		require.True(t, r.Tracking)
	}
	require.NotContains(t, stderr, "tracking is off")

	// Another context has its own tally: switching with --context records
	// nothing for "dev" when that context is not opted in.
	code, out, _ = runUsageCLI(t, "usage", "--context", "other", "-o", "json", "--plain")
	require.Zero(t, code)
	require.Zero(t, decodeUsageRows(t, out)[0].Queries)
}

func TestUsageTableOutput(t *testing.T) {
	usageStateDir(t)
	srv, _ := usageQueryServer(t)
	isolatedConfig(t, usageConfig(srv.URL, true))

	code, _, stderr := runUsageCLI(t, "query", "fetch logs", "-o", "json", "--plain")
	require.Zero(t, code, stderr)

	code, out, stderr := runUsageCLI(t, "usage")
	require.Zero(t, code, stderr)
	for _, want := range []string{"CONTEXT", "PERIOD", "QUERIES", "SCANNED", "RECORDS", "today", "7d", "30d", "3.00 GiB"} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, strings.ToLower(out), "$", "scan volume must never be shown as a currency")
	require.Contains(t, stderr, "Counts only DQL queries run by dtctl on this machine")
	require.Contains(t, stderr, "BILLING_USAGE_EVENT")
	require.Contains(t, stderr, "Usage file:")
}

func TestUsageAgentEnvelope(t *testing.T) {
	usageStateDir(t)
	srv, _ := usageQueryServer(t)
	isolatedConfig(t, usageConfig(srv.URL, false))

	code, out, _ := runUsageCLI(t, "usage", "--agent")
	require.Zero(t, code)
	var env struct {
		OK      bool       `json:"ok"`
		Result  []usageRow `json:"result"`
		Context struct {
			Verb        string   `json:"verb"`
			Resource    string   `json:"resource"`
			Warnings    []string `json:"warnings"`
			Suggestions []string `json:"suggestions"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &env), out)
	require.True(t, env.OK)
	require.Len(t, env.Result, 3)
	require.Equal(t, "usage", env.Context.Verb)
	require.NotEmpty(t, env.Context.Warnings)
	joined := strings.Join(env.Context.Suggestions, "\n")
	require.Contains(t, joined, "--usage-tracking")
	require.Contains(t, joined, "BILLING_USAGE_EVENT")
}

func TestUsageAllContextsAndReset(t *testing.T) {
	state := usageStateDir(t)
	srv, _ := usageQueryServer(t)
	isolatedConfig(t, usageConfig(srv.URL, true))

	code, _, stderr := runUsageCLI(t, "query", "fetch logs", "-o", "json", "--plain")
	require.Zero(t, code, stderr)
	// A context that has history but no longer exists in the config still shows.
	ledger := usage.New(filepath.Join(state, "dtctl", usage.FileName))
	require.NoError(t, ledger.Record("gone", usage.Sample{ScannedBytes: 1}))

	code, out, _ := runUsageCLI(t, "usage", "--all-contexts", "-o", "json", "--plain")
	require.Zero(t, code)
	rows := decodeUsageRows(t, out)
	var contexts []string
	for _, r := range rows {
		if r.Period == "today" {
			contexts = append(contexts, r.Context)
		}
	}
	require.Equal(t, []string{"dev", "gone"}, contexts, "tracked contexts plus any with history, sorted")

	// --plain skips the prompt, as for every other confirmation.
	code, _, stderr = runUsageCLI(t, "usage", "reset", "--plain")
	require.Zero(t, code, stderr)
	require.Contains(t, stderr, `Cleared the local usage tally of context "dev"`)
	days, err := ledger.Read()
	require.NoError(t, err)
	require.Equal(t, []string{"gone"}, usage.Contexts(days))

	code, out, _ = runUsageCLI(t, "usage", "reset", "--all-contexts", "--agent")
	require.Zero(t, code)
	require.Contains(t, out, `"removed":true`)
	_, err = os.Stat(ledger.Path())
	require.True(t, os.IsNotExist(err))

	code, _, stderr = runUsageCLI(t, "usage", "reset", "--all-contexts", "--plain")
	require.Zero(t, code)
	require.Contains(t, stderr, "Nothing to clear")
}

func TestUsageResetPromptDeclined(t *testing.T) {
	state := usageStateDir(t)
	isolatedConfig(t, usageConfig("https://dev.example.invalid", true))
	ledger := usage.New(filepath.Join(state, "dtctl", usage.FileName))
	require.NoError(t, ledger.Record("dev", usage.Sample{}))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"usage", "reset"}, RunOptions{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("n\n")})
	require.Zero(t, code)
	require.Contains(t, stdout.String(), "[y/N]")
	require.Contains(t, stderr.String(), "Reset cancelled")
	days, err := ledger.Read()
	require.NoError(t, err)
	require.Equal(t, []string{"dev"}, usage.Contexts(days), "a declined reset must keep the data")
}

func TestSetContextUsageTrackingFlag(t *testing.T) {
	isolatedConfig(t, usageConfig("https://dev.example.invalid", false))
	path := os.Getenv("DTCTL_CONFIG")

	code, _, stderr := runUsageCLI(t, "config", "set-context", "dev", "--usage-tracking", "--plain")
	require.Zero(t, code, stderr)
	require.Contains(t, stderr, "Usage tracking is on")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "usage-tracking: true")

	// An update that does not mention the flag keeps it.
	code, _, stderr = runUsageCLI(t, "config", "set-context", "dev", "--description", "x", "--plain")
	require.Zero(t, code, stderr)
	raw, _ = os.ReadFile(path)
	require.Contains(t, string(raw), "usage-tracking: true")

	code, out, _ := runUsageCLI(t, "ctx", "describe", "dev", "--plain")
	require.Zero(t, code)
	require.Contains(t, out, "tracked locally")

	code, _, stderr = runUsageCLI(t, "ctx", "set", "dev", "--usage-tracking=false", "--plain")
	require.Zero(t, code, stderr)
	require.Contains(t, stderr, "Usage tracking is off")
	raw, _ = os.ReadFile(path)
	require.NotContains(t, string(raw), "usage-tracking")
}

// TestUsageTrackingNeedsHostCapability: an embedded invocation (which grants
// no host capabilities) records nothing, even for an opted-in context.
func TestUsageTrackingNeedsHostCapability(t *testing.T) {
	state := usageStateDir(t)
	srv, executions := usageQueryServer(t)
	isolatedConfig(t, usageConfig(srv.URL, true))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"query", "fetch logs", "-o", "json", "--plain"},
		RunOptions{Stdout: &stdout, Stderr: &stderr, Capabilities: &Capabilities{}})
	require.Zero(t, code, stderr.String())
	require.EqualValues(t, 1, executions.Load())
	_, err := os.Stat(state)
	require.True(t, os.IsNotExist(err), "no HostUsageLedger capability, no ledger (err=%v)", err)
}
