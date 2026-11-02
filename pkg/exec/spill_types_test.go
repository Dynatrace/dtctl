package exec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/output"
	sdkquery "github.com/Dynatrace/dtctl/sdk/api/query"
)

// typedResult is a small result whose API response carries a DQL type block.
// "region" holds one value in every row, so compaction hoists it into constant,
// and "note" is null throughout, so compaction drops it — the types block must
// still describe every column the API typed.
func typedResult() (*DQLQueryResponse, []map[string]interface{}, []sdkquery.ColumnTypes) {
	records := []map[string]interface{}{
		{"host": "web-01", "count": "3", "region": "eu", "note": nil},
		{"host": "web-02", "count": "5", "region": "eu", "note": nil},
		{"host": "web-03", "count": "8", "region": "eu", "note": nil},
	}
	types := []sdkquery.ColumnTypes{{
		IndexRange: []int{0, 2},
		Mappings: map[string]sdkquery.ColumnType{
			"host":   {Type: "string"},
			"count":  {Type: "long"},
			"region": {Type: "string"},
			"note":   {Type: "string"},
		},
	}}
	return &DQLQueryResponse{Result: &DQLResult{Records: records, Types: types}}, records, types
}

// agentDefaultOpts mirrors what `dtctl query -A --include-types` resolves to:
// -o auto, --compact, --metadata=minimal and the field cap, with the spill
// threshold that keeps a small result inline.
func agentDefaultOpts(t *testing.T, format string) DQLExecuteOptions {
	t.Helper()
	return DQLExecuteOptions{
		OutputFormat:        format,
		AutoFormatByDefault: format == "auto",
		AgentMode:           true,
		IncludeTypes:        true,
		EmitTypes:           true,
		Compact:             true,
		MetadataFields:      []string{"minimal"},
		MetadataDefaulted:   true,
		MaxFieldChars:       500,
		Spill:               SpillOptions{Mode: SpillAuto, Threshold: 50 * 1024, Dir: t.TempDir()},
	}
}

// envelopeTypes runs printResults and returns the envelope's result.kind and
// result.types (nil when the key is absent).
func envelopeTypes(t *testing.T, result *DQLQueryResponse, opts DQLExecuteOptions) (string, []sdkquery.ColumnTypes) {
	t.Helper()
	e := &DQLExecutor{}
	var printErr error
	out := captureStdout(t, func() {
		printErr = e.printResults("fetch logs", result, opts)
	})
	if printErr != nil {
		t.Fatalf("printResults: %v", printErr)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Kind  string                 `json:"kind"`
			Types []sdkquery.ColumnTypes `json:"types"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("output is not a JSON envelope: %v\n%s", err, out)
	}
	if !env.OK {
		t.Fatalf("envelope ok=false:\n%s", out)
	}
	return env.Result.Kind, env.Result.Types
}

// Regression for #433: -A --include-types dropped the type block because agent
// output goes through the spill-aware envelope, which never carried it.
func TestAgentEnvelope_IncludeTypes_Inline(t *testing.T) {
	for _, format := range []string{"auto", "json", "toon"} {
		t.Run(format, func(t *testing.T) {
			result, _, want := typedResult()
			kind, got := envelopeTypes(t, result, agentDefaultOpts(t, format))
			if kind != output.KindRecords {
				t.Fatalf("kind = %q, want %q", kind, output.KindRecords)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("result.types = %#v, want %#v", got, want)
			}
		})
	}
}

func TestAgentEnvelope_IncludeTypes_Spilled(t *testing.T) {
	result, _, want := typedResult()
	opts := agentDefaultOpts(t, "auto")
	opts.Spill.Mode = SpillAlways
	kind, got := envelopeTypes(t, result, opts)
	if kind != output.KindResultFile {
		t.Fatalf("kind = %q, want %q", kind, output.KindResultFile)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("result.types = %#v, want %#v", got, want)
	}
}

func TestAgentEnvelope_IncludeTypes_SummaryOnly(t *testing.T) {
	result, _, want := typedResult()
	opts := agentDefaultOpts(t, "auto")
	// A spill dir under a regular file can never be created.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.Spill = SpillOptions{Mode: SpillAlways, Dir: filepath.Join(f, "nope")}
	kind, got := envelopeTypes(t, result, opts)
	if kind != output.KindSummaryOnly {
		t.Fatalf("kind = %q, want %q", kind, output.KindSummaryOnly)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("result.types = %#v, want %#v", got, want)
	}
}

// A --max-output-bytes cut keeps fewer rows but the same result, so the block
// still describes it (indexRange indexes the full result, like next_offset).
func TestAgentEnvelope_IncludeTypes_SurvivesOutputBudget(t *testing.T) {
	result, _, want := typedResult()
	// Enough rows that the budget keeps some and cuts the rest.
	var records []map[string]interface{}
	for i := range 100 {
		records = append(records, map[string]interface{}{"host": fmt.Sprintf("web-%02d", i), "count": fmt.Sprint(i), "region": "eu", "note": nil})
	}
	result.Result.Records = records
	opts := agentDefaultOpts(t, "json")
	opts.Spill.Mode = SpillNever
	opts.MaxOutputBytes = 2000
	e := &DQLExecutor{}
	out := captureStdout(t, func() {
		if err := e.printResults("fetch logs", result, opts); err != nil {
			t.Fatalf("printResults: %v", err)
		}
	})
	var env struct {
		Result struct {
			Records []map[string]interface{} `json:"records"`
			Types   []sdkquery.ColumnTypes   `json:"types"`
		} `json:"result"`
		Context output.ResponseContext `json:"context"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if !env.Context.Truncated || len(env.Result.Records) == 0 || len(env.Result.Records) >= len(records) {
		t.Fatalf("expected the budget to cut rows, got %d rows truncated=%v:\n%s", len(env.Result.Records), env.Context.Truncated, out)
	}
	if !reflect.DeepEqual(env.Result.Types, want) {
		t.Errorf("result.types = %#v, want %#v", env.Result.Types, want)
	}
	if int64(len(out)) > opts.MaxOutputBytes {
		t.Errorf("envelope is %d bytes, over the %d budget — the types block was not measured:\n%s", len(out), opts.MaxOutputBytes, out)
	}
}

// The block is emitted only for an explicit --include-types: --typed and
// Parquet request the types internally (IncludeTypes) without EmitTypes.
func TestAgentEnvelope_TypesOmittedWithoutIncludeTypes(t *testing.T) {
	for _, mode := range []SpillMode{SpillAuto, SpillAlways} {
		t.Run(string(mode), func(t *testing.T) {
			result, _, _ := typedResult()
			opts := agentDefaultOpts(t, "json")
			opts.EmitTypes = false
			opts.Spill.Mode = mode
			if _, got := envelopeTypes(t, result, opts); got != nil {
				t.Errorf("result.types = %#v, want absent", got)
			}
		})
	}
}
