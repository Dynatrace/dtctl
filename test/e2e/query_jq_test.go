//go:build integration
// +build integration

package e2e

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/exec"
	"github.com/Dynatrace/dtctl/test/integration"
)

// jqEnvelope is the agent-mode response as a --jq consumer sees it.
type jqEnvelope struct {
	OK       bool             `json:"ok"`
	Result   json.RawMessage  `json:"result"`
	Error    *jqEnvelopeError `json:"error"`
	Context  map[string]any   `json:"context"`
	Metadata map[string]any   `json:"metadata"`
}

type jqEnvelopeError struct {
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions"`
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written. ExecuteWithOptions renders straight to os.Stdout, so this is how the
// test sees the bytes a real agent would parse.
func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// TestQueryAgentJQ_LiveEnvelope drives `query --agent --jq` end to end against a
// real tenant: real Grail response -> printResults -> agent envelope. It is the
// regression test for #413, where a filter written against the envelope came
// back as a confident `null` with exit code 0 and was read as a verified
// negative during a live investigation.
//
// The queries stay on core Grail data objects (logs) because the integration
// tenant is Grail-only.
func TestQueryAgentJQ_LiveEnvelope(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	const query = "fetch logs, from:now()-1h | fields timestamp | limit 2"

	t.Run("correct filter keeps the envelope", func(t *testing.T) {
		var runErr error
		out := captureStdout(t, func() {
			runErr = executor.ExecuteWithOptions(query, exec.DQLExecuteOptions{
				OutputFormat: "json",
				AgentMode:    true,
				JQFilter:     ".records",
			})
		})
		if runErr != nil {
			t.Fatalf("ExecuteWithOptions() error = %v", runErr)
		}

		var resp jqEnvelope
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("output is not a JSON envelope: %v\n%s", err, out)
		}
		// `ok` is the field a consumer tests to tell success from failure; the
		// pre-fix query path dropped it entirely under --jq.
		if !resp.OK {
			t.Errorf("ok = false, want true; output: %s", out)
		}
		if resp.Context == nil {
			t.Errorf("context missing from the envelope: %s", out)
		} else if resp.Context["verb"] != "query" {
			t.Errorf("context.verb = %v, want query", resp.Context["verb"])
		}

		var records []map[string]any
		if err := json.Unmarshal(resp.Result, &records); err != nil {
			t.Fatalf("result is not the filtered record list: %v\n%s", err, resp.Result)
		}
		t.Logf("✓ envelope preserved, result carried %d filtered record(s)", len(records))
	})

	t.Run("wrong shape errors instead of returning null", func(t *testing.T) {
		var runErr error
		out := captureStdout(t, func() {
			runErr = executor.ExecuteWithOptions(query, exec.DQLExecuteOptions{
				OutputFormat: "json",
				AgentMode:    true,
				JQFilter:     ".result.records",
			})
		})
		if runErr == nil {
			t.Fatalf("ExecuteWithOptions() returned nil error for a wrong-shape filter; stdout: %s", out)
		}
		if len(out) != 0 {
			t.Errorf("nothing should reach stdout on a filter error, got: %s", out)
		}
		// The keys of the real payload must be in the message — that is what
		// makes the mismatch self-correcting without another round trip.
		if !strings.Contains(runErr.Error(), "records") {
			t.Errorf("error does not name the payload keys: %v", runErr)
		}
		t.Logf("✓ wrong-shape filter failed loudly: %v", runErr)
	})

	t.Run("empty result is an empty list, not an error", func(t *testing.T) {
		var runErr error
		out := captureStdout(t, func() {
			runErr = executor.ExecuteWithOptions("fetch logs, from:now()-1h | limit 0", exec.DQLExecuteOptions{
				OutputFormat: "json",
				AgentMode:    true,
				JQFilter:     ".records",
			})
		})
		if runErr != nil {
			t.Fatalf("ExecuteWithOptions() error = %v on an empty result", runErr)
		}

		var resp jqEnvelope
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("output is not a JSON envelope: %v\n%s", err, out)
		}
		if !resp.OK {
			t.Errorf("ok = false, want true for a genuinely empty result: %s", out)
		}
		if strings.TrimSpace(string(resp.Result)) != "[]" {
			t.Errorf("result = %s, want []", resp.Result)
		}
		t.Logf("✓ empty result filtered to [] with ok=true")
	})

	t.Run("non-matching select is an empty list, not an error", func(t *testing.T) {
		var runErr error
		out := captureStdout(t, func() {
			runErr = executor.ExecuteWithOptions(query, exec.DQLExecuteOptions{
				OutputFormat: "json",
				AgentMode:    true,
				JQFilter:     `.records[] | select(.timestamp == "never")`,
			})
		})
		if runErr != nil {
			t.Fatalf("ExecuteWithOptions() error = %v for a non-matching select", runErr)
		}

		var resp jqEnvelope
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("output is not a JSON envelope: %v\n%s", err, out)
		}
		if strings.TrimSpace(string(resp.Result)) != "[]" {
			t.Errorf("result = %s, want [] for a select that matched nothing", resp.Result)
		}
		t.Logf("✓ non-matching select filtered to [], distinguishable from a shape mismatch")
	})

	t.Run("metadata stays reachable and preserved", func(t *testing.T) {
		var runErr error
		out := captureStdout(t, func() {
			runErr = executor.ExecuteWithOptions(query, exec.DQLExecuteOptions{
				OutputFormat:   "json",
				AgentMode:      true,
				JQFilter:       ".records",
				MetadataFields: []string{"executionTimeMilliseconds"},
			})
		})
		if runErr != nil {
			t.Fatalf("ExecuteWithOptions() error = %v", runErr)
		}

		var resp jqEnvelope
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("output is not a JSON envelope: %v\n%s", err, out)
		}
		// A filter that narrowed down to the rows must not drop metadata: the
		// envelope keeps it as a sibling of result, as it does without --jq.
		if resp.Metadata == nil {
			t.Errorf("envelope metadata missing after a .records filter: %s", out)
		}
		t.Logf("✓ metadata preserved next to result")
	})
}
