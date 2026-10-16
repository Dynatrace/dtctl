package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

const testSchedulingRuleID = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"

// newSchedulingRuleMockServer serves the one rule the delete path reads before
// it decides anything, and counts DELETEs so a test can assert none happened.
func newSchedulingRuleMockServer(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var deletes int64

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules/"+testSchedulingRuleID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			atomic.AddInt64(&deletes, 1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       testSchedulingRuleID,
			"title":    "Business Hours",
			"ruleType": "rrule",
			"rrule":    map[string]any{"freq": "WEEKLY", "datestart": "2026-01-05"},
		})
	})
	mux.HandleFunc("/platform/automation/v1/scheduling-rules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       testSchedulingRuleID,
			"title":    "Business Hours",
			"ruleType": "rrule",
		})
	})
	// Ownership resolution is best-effort; an unauthenticated answer is fine.
	mux.HandleFunc("/platform/metadata/v1/user", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &deletes
}

// setupSchedulingRuleCmdTest points the CLI at srv and restores every global
// flag the command reads.
func setupSchedulingRuleCmdTest(t *testing.T, srv *httptest.Server, agent, dry bool) {
	t.Helper()
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")

	origCfg, origFormat, origAgent := cfgFile(context.Background()), outputFormat(context.Background()), agentMode(context.Background())
	origDry, origPlain := dryRun(context.Background()), plainMode(context.Background())
	t.Cleanup(func() {
		gFlags.cfgFile, gFlags.outputFormat, gFlags.agentMode = origCfg, origFormat, origAgent
		gFlags.dryRun, gFlags.plainMode = origDry, origPlain
		resetCmdFlags(deleteSchedulingRuleCmd)
	})

	gFlags.cfgFile = filepath.Join(t.TempDir(), "config")
	gFlags.outputFormat = "table"
	gFlags.agentMode = agent
	gFlags.dryRun = dry
	_ = deleteSchedulingRuleCmd.Flags().Set("yes", "true") // skip the interactive confirmation
	gFlags.plainMode = false

	cfg := config.NewConfig()
	// dangerously-unrestricted keeps the safety gate out of the way: these tests
	// are about dry-run and output shape, not about ownership.
	cfg.SetContextWithOptions("test", srv.URL, "test-token", &config.ContextOptions{
		SafetyLevel: config.SafetyLevelDangerouslyUnrestricted,
	})
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(cfgFile(context.Background())); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
}

// captureSchedulingRuleOutput runs fn with stdout and stderr redirected and
// returns them separately: the agent envelope goes to stdout, while
// output.PrintInfo/PrintSuccess write human text to stderr.
func captureSchedulingRuleOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW

	drain := func(r *os.File) <-chan string {
		ch := make(chan string, 1)
		go func() {
			var buf bytes.Buffer
			_, _ = io.Copy(&buf, r)
			ch <- buf.String()
		}()
		return ch
	}
	outCh, errCh := drain(outR), drain(errR)

	fn()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = origOut, origErr
	return <-outCh, <-errCh
}

// --dry-run promises to describe the action without performing it. The command
// reaches DELETE unless the dry-run branch short-circuits first, and no unit
// test covered that before.
func TestDeleteSchedulingRuleDryRunIssuesNoDelete(t *testing.T) {
	srv, deletes := newSchedulingRuleMockServer(t)
	setupSchedulingRuleCmdTest(t, srv, false, true)

	_, stderr := captureSchedulingRuleOutput(t, func() {
		if err := deleteSchedulingRuleCmd.RunE(deleteSchedulingRuleCmd, []string{testSchedulingRuleID}); err != nil {
			t.Fatalf("delete scheduling-rule --dry-run: %v", err)
		}
	})

	if got := atomic.LoadInt64(deletes); got != 0 {
		t.Errorf("DELETE requests = %d, want 0 under --dry-run", got)
	}
	if !strings.Contains(stderr, "would delete") {
		t.Errorf("output did not announce the planned deletion: %q", stderr)
	}
}

// Without --dry-run the same command must actually delete, so the guard above
// cannot pass by disabling delete outright.
func TestDeleteSchedulingRuleIssuesDelete(t *testing.T) {
	srv, deletes := newSchedulingRuleMockServer(t)
	setupSchedulingRuleCmdTest(t, srv, false, false)

	_, _ = captureSchedulingRuleOutput(t, func() {
		if err := deleteSchedulingRuleCmd.RunE(deleteSchedulingRuleCmd, []string{testSchedulingRuleID}); err != nil {
			t.Fatalf("delete scheduling-rule: %v", err)
		}
	})

	if got := atomic.LoadInt64(deletes); got != 1 {
		t.Errorf("DELETE requests = %d, want 1", got)
	}
}

// Agent mode must emit the {ok,result,context} envelope on every exit path,
// including the dry-run one, or an agent cannot parse the outcome.
func TestDeleteSchedulingRuleDryRunAgentEnvelope(t *testing.T) {
	srv, deletes := newSchedulingRuleMockServer(t)
	setupSchedulingRuleCmdTest(t, srv, true, true)

	out, _ := captureSchedulingRuleOutput(t, func() {
		if err := deleteSchedulingRuleCmd.RunE(deleteSchedulingRuleCmd, []string{testSchedulingRuleID}); err != nil {
			t.Fatalf("delete scheduling-rule --agent --dry-run: %v", err)
		}
	})

	if got := atomic.LoadInt64(deletes); got != 0 {
		t.Errorf("DELETE requests = %d, want 0 under --dry-run", got)
	}
	env := assertAgentEnvelope(t, out)
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %v", env["result"])
	}
	if result["status"] != "dry-run" {
		t.Errorf("result.status = %v, want dry-run", result["status"])
	}
}

func TestCreateSchedulingRuleDryRunAgentEnvelope(t *testing.T) {
	srv, _ := newSchedulingRuleMockServer(t)
	setupSchedulingRuleCmdTest(t, srv, true, true)

	ruleFile := filepath.Join(t.TempDir(), "rule.yaml")
	if err := os.WriteFile(ruleFile, []byte("title: Business Hours\nruleType: rrule\nrrule:\n  freq: WEEKLY\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := createSchedulingRuleCmd.Flags().Set("file", ruleFile); err != nil {
		t.Fatalf("set --file: %v", err)
	}
	t.Cleanup(func() { resetFlagSet(createSchedulingRuleCmd.Flags()) })

	out, _ := captureSchedulingRuleOutput(t, func() {
		if err := createSchedulingRuleCmd.RunE(createSchedulingRuleCmd, nil); err != nil {
			t.Fatalf("create scheduling-rule --agent --dry-run: %v", err)
		}
	})

	env := assertAgentEnvelope(t, out)
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %v", env["result"])
	}
	if result["dryRun"] != true {
		t.Errorf("result.dryRun = %v, want true", result["dryRun"])
	}
}

func TestCreateSchedulingRuleAgentEnvelope(t *testing.T) {
	srv, _ := newSchedulingRuleMockServer(t)
	setupSchedulingRuleCmdTest(t, srv, true, false)

	ruleFile := filepath.Join(t.TempDir(), "rule.yaml")
	if err := os.WriteFile(ruleFile, []byte("title: Business Hours\nruleType: rrule\nrrule:\n  freq: WEEKLY\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := createSchedulingRuleCmd.Flags().Set("file", ruleFile); err != nil {
		t.Fatalf("set --file: %v", err)
	}
	t.Cleanup(func() { resetFlagSet(createSchedulingRuleCmd.Flags()) })

	out, _ := captureSchedulingRuleOutput(t, func() {
		if err := createSchedulingRuleCmd.RunE(createSchedulingRuleCmd, nil); err != nil {
			t.Fatalf("create scheduling-rule --agent: %v", err)
		}
	})

	env := assertAgentEnvelope(t, out)
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %v", env["result"])
	}
	if result["id"] != testSchedulingRuleID {
		t.Errorf("result.id = %v, want %s", result["id"], testSchedulingRuleID)
	}
}

// assertAgentEnvelope parses agent-mode output and checks the required shape.
func assertAgentEnvelope(t *testing.T, out string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("agent output is not JSON: %v\n%s", err, out)
	}
	if _, ok := env["ok"]; !ok {
		t.Errorf("envelope has no \"ok\" field: %s", out)
	}
	if _, ok := env["result"]; !ok {
		t.Errorf("envelope has no \"result\" field: %s", out)
	}
	if _, ok := env["context"]; !ok {
		t.Errorf("envelope has no \"context\" field: %s", out)
	}
	return env
}
