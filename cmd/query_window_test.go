package cmd

import (
	"encoding/json"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
)

// TestQueryWindowContext pins the window note, flagged when it is the default.
func TestQueryWindowContext(t *testing.T) {
	resp := func(start, end string) *exec.DQLQueryResponse {
		var r exec.DQLQueryResponse
		raw := `{"state":"SUCCEEDED","result":{"records":[],"metadata":{"grail":{"analysisTimeframe":{"start":"` + start + `","end":"` + end + `"}}}}}`
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		return &r
	}
	w := queryWindowContext(resp("2026-01-01T08:00:00Z", "2026-01-01T10:00:00Z"), queryNamesWindow("fetch bizevents | summarize n = count()"))
	if w == nil || w.Span != "2h" || w.Note == "" {
		t.Fatalf("default window = %+v, want span 2h and a note", w)
	}
	q := "fetch bizevents, from: now()-24h | summarize n = count()"
	w = queryWindowContext(resp("2025-12-31T10:00:00Z", "2026-01-01T10:00:00Z"), queryNamesWindow(q))
	if w == nil || w.Span != "24h" || w.Note != "" {
		t.Fatalf("named window = %+v, want span 24h and no note", w)
	}
	if queryNamesWindow("fetch logs // from: now()-7d") {
		t.Error("a from: inside a comment names no window")
	}
	for _, q := range []string{
		`fetch logs | fieldsAdd note = "from: example"`,
		"fetch logs | filter content == \"to: x // y\" | fieldsAdd `timeframe:` = 1",
		`fetch logs | filter content == "say \"from: x\""`,
	} {
		if queryNamesWindow(q) {
			t.Errorf("a window keyword inside a literal names no window: %s", q)
		}
	}
	if !queryNamesWindow(`fetch logs | filter content == "a // b", from: now()-7d`) {
		t.Error("a from: after a literal holding // still names the window")
	}
	if got := spanString(7 * 24 * 3600e9); got != "7d" {
		t.Errorf("spanString(7d) = %q", got)
	}
}
