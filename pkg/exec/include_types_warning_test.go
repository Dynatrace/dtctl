package exec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/output"
)

const includeTypesInert = "--include-types has no effect"

// runPrintResults runs printResults and returns what it wrote to stdout and
// stderr.
func runPrintResults(t *testing.T, result *DQLQueryResponse, opts DQLExecuteOptions) (stdout, stderr []byte) {
	t.Helper()
	e := &DQLExecutor{}
	var printErr error
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			printErr = e.printResults("fetch logs", result, opts)
		})
	})
	if printErr != nil {
		t.Fatalf("printResults: %v", printErr)
	}
	return stdout, stderr
}

// TestIncludeTypesInertWarning pins which formats warn: every format with its
// own case in printResults drops the type block, and only the document-shaped
// ones reaching the default branch carry it (#435).
func TestIncludeTypesInertWarning(t *testing.T) {
	inert := []string{"table", "wide", "csv", "jsonl", "parquet", "chart", "sparkline", "spark", "barchart", "bar", "braille", "br"}
	for _, f := range inert {
		w := includeTypesInertWarning(f, 1)
		if !strings.HasPrefix(w, includeTypesInert+" with "+f+" output") {
			t.Errorf("-o %s: warning = %q, want it to name the format", f, w)
		}
	}
	if w := includeTypesInertWarning("table", 1); w != "--include-types has no effect with table output (types are emitted for -o json/yaml/toon)" {
		t.Errorf("table warning = %q", w)
	}
	if w := includeTypesInertWarning("parquet", 1); !strings.Contains(w, "Parquet schema") {
		t.Errorf("parquet warning should point at the schema, got %q", w)
	}
	// auto is only still unresolved here under --jq, where the filter input
	// carries the block.
	for _, f := range []string{"json", "yaml", "yml", "toon", "auto", ""} {
		if w := includeTypesInertWarning(f, 1); w != "" {
			t.Errorf("-o %s carries the type block but warned: %q", f, w)
		}
	}
}

// TestPrintResults_IncludeTypesWarnsWhenFormatDropsTypes checks the warning
// reaches stderr for a format that cannot carry the block, and that stdout is
// byte-identical to the same run without --include-types.
func TestPrintResults_IncludeTypesWarnsWhenFormatDropsTypes(t *testing.T) {
	for _, format := range []string{"table", "wide", "csv", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			result, _, _ := typedResult()
			without, _ := runPrintResults(t, result, DQLExecuteOptions{OutputFormat: format, IncludeTypes: true})
			result, _, _ = typedResult()
			with, stderr := runPrintResults(t, result, DQLExecuteOptions{OutputFormat: format, IncludeTypes: true, EmitTypes: true, TypesRequested: true})

			want := "Warning: --include-types has no effect with " + format + " output (types are emitted for -o json/yaml/toon)"
			if !strings.Contains(string(stderr), want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
			if !bytes.Equal(with, without) {
				t.Errorf("stdout changed by --include-types:\nwith:    %q\nwithout: %q", with, without)
			}
			if bytes.Contains(with, []byte(includeTypesInert)) {
				t.Error("the warning belongs on stderr, not stdout")
			}
		})
	}
}

// TestPrintResults_IncludeTypesNoWarningWhenFormatCarriesTypes checks the
// formats that do emit the block stay quiet and still carry it.
func TestPrintResults_IncludeTypesNoWarningWhenFormatCarriesTypes(t *testing.T) {
	for _, format := range []string{"json", "yaml", "toon"} {
		t.Run(format, func(t *testing.T) {
			result, _, _ := typedResult()
			stdout, stderr := runPrintResults(t, result, DQLExecuteOptions{OutputFormat: format, IncludeTypes: true, EmitTypes: true, TypesRequested: true})
			if bytes.Contains(stderr, []byte(includeTypesInert)) {
				t.Errorf("-o %s carries the types block but warned: %q", format, stderr)
			}
			if !bytes.Contains(stdout, []byte("types")) || !bytes.Contains(stdout, []byte("indexRange")) {
				t.Errorf("-o %s did not emit the types block:\n%s", format, stdout)
			}
		})
	}
}

// TestPrintResults_IncludeTypesWarningCases covers the resolution paths that
// decide the effective format: --jq, -o auto, agent mode, and a run that
// requested types only internally.
func TestPrintResults_IncludeTypesWarningCases(t *testing.T) {
	cases := []struct {
		name     string
		records  []map[string]interface{}
		opts     DQLExecuteOptions
		wantWarn bool
	}{
		{
			name:     "--jq promotes table to json, whose filter input carries types",
			opts:     DQLExecuteOptions{OutputFormat: "table", JQFilter: ".types", IncludeTypes: true, EmitTypes: true, TypesRequested: true},
			wantWarn: false,
		},
		{
			name:     "-o auto that picks csv warns",
			opts:     DQLExecuteOptions{OutputFormat: "auto", IncludeTypes: true, EmitTypes: true, TypesRequested: true},
			wantWarn: true,
		},
		{
			name:     "-o auto that picks yaml does not warn",
			records:  []map[string]interface{}{{"host": "web-01", "count": "3"}},
			opts:     DQLExecuteOptions{OutputFormat: "auto", IncludeTypes: true, EmitTypes: true, TypesRequested: true},
			wantWarn: false,
		},
		{
			name:     "agent mode with an explicit -o csv warns",
			opts:     DQLExecuteOptions{OutputFormat: "csv", AgentMode: true, IncludeTypes: true, EmitTypes: true, TypesRequested: true},
			wantWarn: true,
		},
		{
			name:     "agent mode -o table is wrapped in the envelope, which carries types",
			opts:     DQLExecuteOptions{OutputFormat: "table", AgentMode: true, IncludeTypes: true, EmitTypes: true, TypesRequested: true},
			wantWarn: false,
		},
		{
			name:     "--typed requests types internally and must not warn",
			opts:     DQLExecuteOptions{OutputFormat: "csv", IncludeTypes: true, Typed: true},
			wantWarn: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, _, _ := typedResult()
			if c.records != nil {
				result.Result.Records = c.records
			}
			_, stderr := runPrintResults(t, result, c.opts)
			if got := bytes.Contains(stderr, []byte(includeTypesInert)); got != c.wantWarn {
				t.Errorf("warned = %v, want %v; stderr = %q", got, c.wantWarn, stderr)
			}
		})
	}
}

// TestPrintResults_IncludeTypesChartFollowsWhatIsPrinted pins the chart case
// to what reaches stdout: with rows the chart branch prints them alone (even
// its not-chartable JSON fallback has no "types"), so it warns; with no rows
// it prints the raw API response, whose own types block is there, so it does
// not.
func TestPrintResults_IncludeTypesChartFollowsWhatIsPrinted(t *testing.T) {
	t.Run("rows: warns, and the fallback carries no types", func(t *testing.T) {
		result, _, _ := typedResult()
		stdout, stderr := runPrintResults(t, result, DQLExecuteOptions{OutputFormat: "chart", IncludeTypes: true, EmitTypes: true, TypesRequested: true})
		if !bytes.Contains(stderr, []byte(includeTypesInert+" with chart output")) {
			t.Errorf("stderr = %q, want the chart warning", stderr)
		}
		if bytes.Contains(stdout, []byte("indexRange")) {
			t.Errorf("chart output carried the types block, so the warning is wrong:\n%s", stdout)
		}
	})
	t.Run("no rows: the raw-response fallback carries types, no warning", func(t *testing.T) {
		result, _, _ := typedResult()
		result.Result.Records = nil
		stdout, stderr := runPrintResults(t, result, DQLExecuteOptions{OutputFormat: "chart", IncludeTypes: true, EmitTypes: true, TypesRequested: true})
		if bytes.Contains(stderr, []byte(includeTypesInert)) {
			t.Errorf("warned although the output carries types: %q", stderr)
		}
		if !bytes.Contains(stdout, []byte("indexRange")) {
			t.Errorf("expected the raw response's types block on stdout:\n%s", stdout)
		}
	})
}

// TestPrintResults_IncludeTypesSpilledEnvelopeCarriesTypes covers the spill
// path, which returns before the warning: its envelope carries result.types
// (on the manifest) whatever the display or file format, so --include-types
// has an effect there and there is nothing to warn about. That includes an
// explicit -o csv in agent mode, which warns only when it falls through inline.
func TestPrintResults_IncludeTypesSpilledEnvelopeCarriesTypes(t *testing.T) {
	for _, agent := range []bool{true, false} {
		t.Run(fmt.Sprintf("agent=%v", agent), func(t *testing.T) {
			result, _, _ := typedResult()
			opts := DQLExecuteOptions{
				OutputFormat:   "csv",
				AgentMode:      agent,
				IncludeTypes:   true,
				EmitTypes:      true,
				TypesRequested: true,
				Spill:          SpillOptions{Mode: SpillAlways, Dir: t.TempDir()},
			}
			stdout, stderr := runPrintResults(t, result, opts)
			if bytes.Contains(stderr, []byte(includeTypesInert)) {
				t.Errorf("warned although the spilled envelope carries types: %q", stderr)
			}
			var env struct {
				Result struct {
					Kind  string          `json:"kind"`
					Types json.RawMessage `json:"types"`
				} `json:"result"`
			}
			if err := json.Unmarshal(stdout, &env); err != nil {
				t.Fatalf("not a JSON envelope: %v\n%s", err, stdout)
			}
			if env.Result.Kind != output.KindResultFile {
				t.Fatalf("kind = %q, want %q", env.Result.Kind, output.KindResultFile)
			}
			if !bytes.Contains(env.Result.Types, []byte("indexRange")) {
				t.Errorf("spilled envelope has no result.types:\n%s", stdout)
			}
		})
	}
}
