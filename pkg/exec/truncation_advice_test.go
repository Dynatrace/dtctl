package exec

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// captureStderr redirects os.Stderr for the duration of fn and returns
// whatever was written.
func captureStderr(t *testing.T, fn func()) []byte {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	return <-done
}

const resultLimitMessage = "Your result has been limited to 1000."

func truncatedResponse() *DQLQueryResponse {
	return &DQLQueryResponse{
		State: "SUCCEEDED",
		Result: &DQLResult{
			Records: []map[string]interface{}{{"host.name": "h1", "count()": "7"}},
			Metadata: &DQLMetadata{Grail: &GrailMetadata{Notifications: []QueryNotification{{
				Severity:         "WARNING",
				NotificationType: "RESULT_LIMIT_RECORDS",
				Message:          resultLimitMessage,
			}}}},
		},
	}
}

// TestPrintResults_AgentEnvelopeCarriesNotifications: when the agent envelope
// is emitted, a query notification lives in context.warnings only. Printing it
// to stderr as well made a host that merges the two streams see the advice
// twice, once as prose it cannot parse.
func TestPrintResults_AgentEnvelopeCarriesNotifications(t *testing.T) {
	tests := []struct {
		name string
		opts DQLExecuteOptions
	}{
		{"inline records", DQLExecuteOptions{OutputFormat: "json", AgentMode: true}},
		{"jq", DQLExecuteOptions{OutputFormat: "json", AgentMode: true, JQFilter: ".records"}},
		{"-o auto", DQLExecuteOptions{OutputFormat: "auto", AgentMode: true}},
		{"spilled", DQLExecuteOptions{OutputFormat: "json", AgentMode: true,
			Spill: SpillOptions{Mode: SpillAlways, ToPath: "result.jsonl"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.opts.Spill.ToPath != "" {
				tt.opts.Spill.ToPath = t.TempDir() + "/" + tt.opts.Spill.ToPath
			}
			e := &DQLExecutor{}
			var stdout []byte
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() {
					if err := e.printResults("fetch logs | summarize count(), by:{host.name}", truncatedResponse(), tt.opts); err != nil {
						t.Errorf("printResults: %v", err)
					}
				})
			})

			if len(stderr) != 0 {
				t.Errorf("stderr = %q, want nothing: the envelope carries the notification", stderr)
			}
			var resp struct {
				Context *output.ResponseContext `json:"context"`
			}
			if err := json.Unmarshal(stdout, &resp); err != nil {
				t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout)
			}
			if resp.Context == nil || !contains(resp.Context.Warnings, resultLimitMessage) {
				t.Errorf("context.warnings = %v, want %q", resp.Context, resultLimitMessage)
			}
		})
	}
}

// TestPrintResults_NotificationsOnStderrWithoutEnvelope: where no envelope can
// carry the notification — human output, or an agent asking for raw CSV bytes —
// stderr stays the only channel and must keep it.
func TestPrintResults_NotificationsOnStderrWithoutEnvelope(t *testing.T) {
	tests := []struct {
		name string
		opts DQLExecuteOptions
	}{
		{"human table", DQLExecuteOptions{OutputFormat: "table"}},
		{"human json", DQLExecuteOptions{OutputFormat: "json"}},
		{"agent csv", DQLExecuteOptions{OutputFormat: "csv", AgentMode: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &DQLExecutor{}
			stderr := captureStderr(t, func() {
				_ = captureStdout(t, func() {
					if err := e.printResults("fetch logs", truncatedResponse(), tt.opts); err != nil {
						t.Errorf("printResults: %v", err)
					}
				})
			})
			if !strings.Contains(string(stderr), resultLimitMessage) {
				t.Errorf("stderr = %q, want the notification", stderr)
			}
		})
	}
}

func TestUnsortedSummarizeAdvice(t *testing.T) {
	truncated := []QueryNotification{{Severity: "WARNING", NotificationType: "RESULT_LIMIT_RECORDS", Message: resultLimitMessage}}

	tests := []struct {
		name          string
		query         string
		notifications []QueryNotification
		want          string // substring of the advice; "" means no advice
	}{
		{"unaliased aggregation", "fetch logs | summarize count(), by:{host.name}", truncated, "| sort `count()` desc | limit N"},
		{"aliased aggregation", "fetch logs | summarize c = count(), by:{host.name, k8s.pod.name}", truncated, "| sort c desc | limit N"},
		{"aggregation after by", "fetch spans | summarize by:{service.name}, p99 = percentile(duration, 99)", truncated, "| sort p99 desc | limit N"},
		{"nested commas", "fetch logs | summarize total = sum(toLong(coalesce(a, b))), by:{host.name}", truncated, "| sort total desc | limit N"},
		{"sort before summarize does not rank its output", "fetch logs | sort timestamp desc | summarize count(), by:{host.name}", truncated, "| sort `count()` desc"},
		{"multiline, trailing fields", "fetch logs\n| summarize n = count(), by:{host.name}\n| fields host.name, n", truncated, "| sort n desc | limit N"},

		{"already sorted", "fetch logs | summarize c = count(), by:{host.name} | sort c desc", truncated, ""},
		{"no by: yields one row", "fetch logs | summarize count()", truncated, ""},
		{"no summarize", "fetch logs | fields host.name", truncated, ""},
		{"not truncated", "fetch logs | summarize count(), by:{host.name}", nil, ""},
		{"scan limit is not a row cut", "fetch logs | summarize count(), by:{host.name}",
			[]QueryNotification{{Severity: "WARNING", NotificationType: "SCAN_LIMIT_GBYTES", Message: "stopped after 500 gigabytes"}}, ""},
		{"informational severity", "fetch logs | summarize count(), by:{host.name}",
			[]QueryNotification{{Severity: "INFO", NotificationType: "RESULT_LIMIT_RECORDS", Message: resultLimitMessage}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unsortedSummarizeAdvice(tt.query, tt.notifications)
			if tt.want == "" {
				if got != "" {
					t.Errorf("advice = %q, want none", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("advice = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// TestUnsortedSummarizeAdviceReachesBothAudiences: the envelope suggestion and
// the human stderr hint both carry the ranking advice.
func TestUnsortedSummarizeAdviceReachesBothAudiences(t *testing.T) {
	const query = "fetch logs | summarize count(), by:{host.name}"
	e := &DQLExecutor{}

	stdout := captureStdout(t, func() {
		_ = e.printResults(query, truncatedResponse(), DQLExecuteOptions{OutputFormat: "json", AgentMode: true})
	})
	var resp struct {
		Context *output.ResponseContext `json:"context"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout)
	}
	if resp.Context == nil || !strings.Contains(strings.Join(resp.Context.Suggestions, "\n"), "| sort `count()` desc | limit N") {
		t.Errorf("context.suggestions = %v, want the sort advice", resp.Context)
	}

	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			_ = e.printResults(query, truncatedResponse(), DQLExecuteOptions{OutputFormat: "table"})
		})
	})
	if !strings.Contains(string(stderr), "| sort `count()` desc | limit N") {
		t.Errorf("stderr = %q, want the sort advice", stderr)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
