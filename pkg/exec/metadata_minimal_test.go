package exec

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// resultWithFullGrailMetadata carries every field the issue (#577) calls out as
// noise for an agent: the query echoed twice, locale, timezone, DQL version,
// query id and the analysis window.
func resultWithFullGrailMetadata() (*DQLQueryResponse, []map[string]interface{}) {
	resp, records := resultWithScanStats()
	g := resp.Metadata.Grail
	g.Query = "fetch logs | limit 3"
	g.CanonicalQuery = "fetch logs\n| limit 3"
	g.Locale = "und"
	g.Timezone = "Z"
	g.DQLVersion = "V1_0"
	return resp, records
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func envelopeContext(t *testing.T, resp output.Response) map[string]interface{} {
	t.Helper()
	js, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	ctx, ok := parsed["context"].(map[string]interface{})
	if !ok {
		t.Fatalf("envelope has no context object:\n%s", js)
	}
	return ctx
}

var spillDebugKeys = []string{"threshold_bytes", "measured_bytes", "measured_encoding"}

func TestBuildSpillResponse_MinimalMetadataInline(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MetadataFields: []string{"minimal"},
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	// An explicit window: the analysis timeframe is what the agent asked for,
	// so echoing it back is noise.
	resp, handled, err := e.buildSpillResponse("fetch logs, from:now()-1h | limit 3", result, records, "json", opts)
	if err != nil || !handled {
		t.Fatalf("buildSpillResponse: handled=%v err=%v", handled, err)
	}

	m := metadataMap(t, resp)
	got := sortedKeys(m)
	want := []string{"executionTimeMilliseconds", "scannedBytes"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("minimal metadata keys = %v, want %v", got, want)
	}

	ctx := envelopeContext(t, resp)
	if ctx["decided"] != "inline" {
		t.Errorf("decided = %v, want inline (it is documented as always present)", ctx["decided"])
	}
	for _, k := range spillDebugKeys {
		if _, ok := ctx[k]; ok {
			t.Errorf("context.%s should be omitted for an inline minimal result: %v", k, ctx)
		}
	}
}

func TestBuildSpillResponse_MinimalMetadataDefaultWindowKeepsTimeframe(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MetadataFields: []string{"minimal"},
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	// No from:/to:/timeframe: and no --default-timeframe-*: the query ran on the
	// server's default window, which the agent cannot know without being told.
	resp, _, err := e.buildSpillResponse("fetch logs | limit 3", result, records, "json", opts)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	m := metadataMap(t, resp)
	if _, ok := m["analysisTimeframe"]; !ok {
		t.Errorf("analysisTimeframe should be kept when the default window applied: %v", m)
	}

	opts.DefaultTimeframeStart = "2026-01-01T00:00:00Z"
	resp, _, err = e.buildSpillResponse("fetch logs | limit 3", result, records, "json", opts)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	if _, ok := metadataMap(t, resp)["analysisTimeframe"]; ok {
		t.Error("analysisTimeframe should be dropped when --default-timeframe-start set the window")
	}
}

func TestBuildSpillResponse_MinimalVerboseKeepsSpillDebug(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MetadataFields: []string{"minimal"},
		Verbose:        true,
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, _, err := e.buildSpillResponse("fetch logs, from:now()-1h", result, records, "json", opts)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	ctx := envelopeContext(t, resp)
	for _, k := range spillDebugKeys {
		if _, ok := ctx[k]; !ok {
			t.Errorf("context.%s should be present under -v: %v", k, ctx)
		}
	}
}

func TestBuildSpillResponse_MinimalSpilledKeepsSpillDebug(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MetadataFields: []string{"minimal"},
		Spill:          SpillOptions{Mode: SpillAlways, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs, from:now()-1h", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("expected spilled: spilled=%v err=%v", spilled, err)
	}
	ctx := envelopeContext(t, resp)
	for _, k := range spillDebugKeys {
		if _, ok := ctx[k]; !ok {
			t.Errorf("context.%s explains why the result spilled and must stay: %v", k, ctx)
		}
	}
	if _, ok := metadataMap(t, resp)["queryId"]; ok {
		t.Error("spilled minimal metadata should not carry queryId")
	}
}

// -M=all (the opt-out from agent mode's minimal default) restores the complete
// metadata block and the spill-decision provenance, with no opt-out suggestion.
func TestBuildSpillResponse_AllMetadataUnchanged(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MetadataFields: []string{"all"},
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, _, err := e.buildSpillResponse("fetch logs, from:now()-1h", result, records, "json", opts)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	m := metadataMap(t, resp)
	for _, k := range []string{"query", "canonicalQuery", "locale", "timezone", "dqlVersion", "queryId", "analysisTimeframe"} {
		if _, ok := m[k]; !ok {
			t.Errorf("full metadata lost %q: %v", k, m)
		}
	}
	ctx := envelopeContext(t, resp)
	for _, k := range spillDebugKeys {
		if _, ok := ctx[k]; !ok {
			t.Errorf("context.%s should be present without minimal: %v", k, ctx)
		}
	}
	if hasMetadataDefaultSuggestion(resp.Context) {
		t.Errorf("-M=all dropped nothing, want no opt-out suggestion: %v", resp.Context.Suggestions)
	}
}

// -o json with --jq in agent mode goes through printAgentJQ: both the filter
// input and the envelope's metadata honor the minimal selection.
func TestPrintResults_AgentJQ_MinimalMetadata(t *testing.T) {
	e := &DQLExecutor{}
	result, _ := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		OutputFormat:   "json",
		AgentMode:      true,
		JQFilter:       ".metadata",
		MetadataFields: []string{"minimal"},
	}
	var printErr error
	out := captureStdout(t, func() {
		printErr = e.printResults("fetch logs, from:now()-1h", result, opts)
	})
	if printErr != nil {
		t.Fatalf("printResults: %v", printErr)
	}
	var resp struct {
		Result   map[string]interface{} `json:"result"`
		Metadata map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	want := "executionTimeMilliseconds,scannedBytes"
	if got := strings.Join(sortedKeys(resp.Result), ","); got != want {
		t.Errorf("--jq .metadata = %v, want keys %s", resp.Result, want)
	}
	if got := strings.Join(sortedKeys(resp.Metadata), ","); got != want {
		t.Errorf("envelope metadata = %v, want keys %s", resp.Metadata, want)
	}
}

// Outside the envelope (-o json --no-agent -M=minimal) the same lean block is
// emitted next to the records.
func TestPrintResults_JSONMinimalMetadata(t *testing.T) {
	e := &DQLExecutor{}
	result, _ := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		OutputFormat:   "json",
		MetadataFields: []string{"minimal"},
	}
	var printErr error
	out := captureStdout(t, func() {
		printErr = e.printResults("fetch logs, from:now()-1h", result, opts)
	})
	if printErr != nil {
		t.Fatalf("printResults: %v", printErr)
	}
	var parsed struct {
		Metadata map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if got := strings.Join(sortedKeys(parsed.Metadata), ","); got != "executionTimeMilliseconds,scannedBytes" {
		t.Errorf("metadata = %v, want only executionTimeMilliseconds,scannedBytes", parsed.Metadata)
	}
}

// DQL parameter names are case-insensitive and allow whitespace before the
// colon, and a window keyword inside a string literal or comment does not set
// the window.
func TestUsesDefaultWindow(t *testing.T) {
	tests := []struct {
		query string
		want  bool
	}{
		{"fetch logs | limit 3", true},
		{"fetch logs, from:now()-1h", false},
		{"fetch logs, FROM:now()-1h", false},
		{"fetch logs, From : now()-1h", false},
		{"fetch logs, to:now()", false},
		{"fetch logs, timeframe:\"2026-01-01T00:00:00Z/2026-01-02T00:00:00Z\"", false},
		{"timeseries avg(dt.host.cpu.usage), from:-2h", false},
		{`fetch logs | filter message == "from:"`, true},
		{`fetch logs | filter contains(content, "to: someone")`, true},
		{"fetch logs // from:now()-1d\n| limit 3", true},
		{`fetch logs | filter url == "http://example.invalid" | limit 1`, true},
		{"fetch logs | fieldsAdd auto:1", true},
		{"fetch logs /* from:now() */ | limit 3", true},
		{"fetch logs /* note */, from:now()-1h", false},
		{`fetch logs | filter message == """from:now()"""`, true},
		{"fetch logs | fieldsAdd `from:x` = 1", true},
		// An unterminated literal cannot be stripped reliably: keep the window.
		{`fetch logs | filter message == "from:`, true},
	}
	for _, tt := range tests {
		if got := usesDefaultWindow(tt.query, DQLExecuteOptions{}); got != tt.want {
			t.Errorf("usesDefaultWindow(%q) = %v, want %v", tt.query, got, tt.want)
		}
	}
	if usesDefaultWindow("fetch logs", DQLExecuteOptions{DefaultTimeframeEnd: "2026-01-01T00:00:00Z"}) {
		t.Error("--default-timeframe-end names a window")
	}
}

func hasMetadataDefaultSuggestion(ctx *output.ResponseContext) bool {
	for _, s := range ctx.Suggestions {
		if strings.Contains(s, "-M=all") {
			return true
		}
	}
	return false
}

// When agent mode applied minimal by default and it dropped something, one
// short suggestion names the opt-out; an explicit -M=minimal gets none.
func TestBuildSpillResponse_DefaultMinimalSuggestion(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithFullGrailMetadata()
	base := DQLExecuteOptions{
		AgentMode:      true,
		MetadataFields: []string{"minimal"},
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}

	defaulted := base
	defaulted.MetadataDefaulted = true
	resp, _, err := e.buildSpillResponse("fetch logs, from:now()-1h", result, records, "json", defaulted)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	if !hasMetadataDefaultSuggestion(resp.Context) {
		t.Errorf("default minimal dropped metadata but no -M=all suggestion: %v", resp.Context.Suggestions)
	}
	n := 0
	for _, s := range resp.Context.Suggestions {
		if strings.Contains(s, "-M=all") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want exactly one -M=all suggestion, got %d: %v", n, resp.Context.Suggestions)
	}

	resp, _, err = e.buildSpillResponse("fetch logs, from:now()-1h", result, records, "json", base)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	if hasMetadataDefaultSuggestion(resp.Context) {
		t.Errorf("explicit -M=minimal should not carry the opt-out suggestion: %v", resp.Context.Suggestions)
	}

	spilled := defaulted
	spilled.Spill.Mode = SpillAlways
	resp, _, err = e.buildSpillResponse("fetch logs, from:now()-1h", result, records, "json", spilled)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	if !hasMetadataDefaultSuggestion(resp.Context) {
		t.Errorf("spilled default minimal dropped metadata but no -M=all suggestion: %v", resp.Context.Suggestions)
	}
}

// Nothing dropped (only the minimal fields exist, and -v keeps the spill
// measurements): no suggestion.
func TestBuildSpillResponse_DefaultMinimalNothingDroppedNoSuggestion(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	result.Metadata.Grail = &GrailMetadata{ExecutionTimeMilliseconds: 12, ScannedBytes: 34}
	opts := DQLExecuteOptions{
		AgentMode:         true,
		MetadataFields:    []string{"minimal"},
		MetadataDefaulted: true,
		Verbose:           true,
		Spill:             SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, _, err := e.buildSpillResponse("fetch logs, from:now()-1h", result, records, "json", opts)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	if hasMetadataDefaultSuggestion(resp.Context) {
		t.Errorf("nothing was dropped, want no suggestion: %v", resp.Context.Suggestions)
	}
}

// The inline spill measurements alone count as dropped output.
func TestBuildSpillResponse_DefaultMinimalSpillDebugDroppedSuggests(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	result.Metadata.Grail = &GrailMetadata{ExecutionTimeMilliseconds: 12, ScannedBytes: 34}
	opts := DQLExecuteOptions{
		AgentMode:         true,
		MetadataFields:    []string{"minimal"},
		MetadataDefaulted: true,
		Spill:             SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, _, err := e.buildSpillResponse("fetch logs, from:now()-1h", result, records, "json", opts)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	if !hasMetadataDefaultSuggestion(resp.Context) {
		t.Errorf("spill measurements were dropped, want the suggestion: %v", resp.Context.Suggestions)
	}
}

func TestPrintResults_AgentJQ_DefaultMinimalSuggestion(t *testing.T) {
	e := &DQLExecutor{}
	result, _ := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		OutputFormat:      "json",
		AgentMode:         true,
		JQFilter:          ".records",
		MetadataFields:    []string{"minimal"},
		MetadataDefaulted: true,
	}
	var printErr error
	out := captureStdout(t, func() {
		printErr = e.printResults("fetch logs, from:now()-1h", result, opts)
	})
	if printErr != nil {
		t.Fatalf("printResults: %v", printErr)
	}
	var resp struct {
		Context  *output.ResponseContext `json:"context"`
		Metadata map[string]interface{}  `json:"metadata"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if !hasMetadataDefaultSuggestion(resp.Context) {
		t.Errorf("--jq path: want the -M=all suggestion: %v", resp.Context.Suggestions)
	}
	if _, ok := resp.Metadata["queryId"]; ok {
		t.Errorf("--jq path: default minimal should not carry queryId: %v", resp.Metadata)
	}
}

// Agent mode combines the minimal metadata default with the auto encoding:
// the rows are auto-encoded (context.format names the choice) while metadata
// and context are trimmed, and each default names only its own opt-out.
func TestBuildSpillResponse_DefaultMinimalWithAutoEncoding(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		AgentMode:         true,
		OutputFormat:      output.FormatAuto,
		MetadataFields:    []string{"minimal"},
		MetadataDefaulted: true,
		Spill:             SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, handled, err := e.buildSpillResponse("fetch logs, from:now()-1h", result, records, output.FormatAuto, opts)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if resp.Context.Format != "csv" {
		t.Errorf("context.format = %q, want csv for flat rows", resp.Context.Format)
	}
	if enc, ok := resp.Result.(*output.InlineRecordsEncoded); !ok || enc.Encoding != "csv" {
		t.Errorf("result = %#v, want csv-encoded inline records", resp.Result)
	}
	if got := strings.Join(sortedKeys(metadataMap(t, resp)), ","); got != "executionTimeMilliseconds,scannedBytes" {
		t.Errorf("metadata keys = %s, want the minimal set", got)
	}
	ctx := envelopeContext(t, resp)
	for _, k := range spillDebugKeys {
		if _, ok := ctx[k]; ok {
			t.Errorf("context.%s should be omitted under the minimal default: %v", k, ctx)
		}
	}
	if !hasMetadataDefaultSuggestion(resp.Context) {
		t.Errorf("want the -M=all suggestion: %v", resp.Context.Suggestions)
	}
}

// -M=all under the auto encoding opts out of the metadata default only: the
// rows stay auto-encoded while the full metadata and the spill provenance
// come back.
func TestBuildSpillResponse_AllMetadataWithAutoEncoding(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithFullGrailMetadata()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		OutputFormat:   output.FormatAuto,
		MetadataFields: []string{"all"},
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, handled, err := e.buildSpillResponse("fetch logs, from:now()-1h", result, records, output.FormatAuto, opts)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if resp.Context.Format != "csv" {
		t.Errorf("context.format = %q, want csv for flat rows", resp.Context.Format)
	}
	m := metadataMap(t, resp)
	for _, k := range []string{"query", "canonicalQuery", "locale", "timezone", "dqlVersion", "queryId", "analysisTimeframe"} {
		if _, ok := m[k]; !ok {
			t.Errorf("full metadata lost %q: %v", k, m)
		}
	}
	ctx := envelopeContext(t, resp)
	for _, k := range spillDebugKeys {
		if _, ok := ctx[k]; !ok {
			t.Errorf("context.%s should be present with -M=all: %v", k, ctx)
		}
	}
	if hasMetadataDefaultSuggestion(resp.Context) {
		t.Errorf("-M=all dropped nothing, want no opt-out suggestion: %v", resp.Context.Suggestions)
	}
}
