package output

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestApplyJQ_SingleResult(t *testing.T) {
	in := map[string]interface{}{"name": "alpha", "id": 42}
	out, err := ApplyJQ(".name", in)
	if err != nil {
		t.Fatalf("ApplyJQ failed: %v", err)
	}

	if out != "alpha" {
		t.Fatalf("expected filtered value 'alpha', got: %#v", out)
	}
}

func TestApplyJQ_MultiResult(t *testing.T) {
	in := []map[string]interface{}{
		{"name": "alpha", "id": "1"},
		{"name": "beta", "id": "2"},
	}
	out, err := ApplyJQ(".[] | {name: .name}", in)
	if err != nil {
		t.Fatalf("ApplyJQ failed: %v", err)
	}

	want := []interface{}{
		map[string]interface{}{"name": "alpha"},
		map[string]interface{}{"name": "beta"},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("unexpected result:\nwant: %#v\ngot:  %#v", want, out)
	}
}

func TestApplyJQ_InvalidFilter(t *testing.T) {
	errInput := map[string]interface{}{"name": "alpha"}
	_, err := ApplyJQ(".[", errInput)
	if err == nil {
		t.Fatal("expected invalid jq filter error")
	}
	if !strings.Contains(err.Error(), "invalid --jq filter") {
		t.Fatalf("expected invalid filter error, got: %v", err)
	}
}

func TestCompileJQ_RunRecord(t *testing.T) {
	prog, err := CompileJQ(`select(.status == 500)`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Matching record passes through.
	out, err := prog.RunRecord(map[string]interface{}{"host": "web-02", "status": 500})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 emitted value, got %d (%#v)", len(out), out)
	}

	// Non-matching record is dropped (empty output is how filtering works).
	out, err = prog.RunRecord(map[string]interface{}{"host": "web-01", "status": 200})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("want 0 emitted values for a non-match, got %#v", out)
	}
}

func TestCompileJQ_Invalid(t *testing.T) {
	if _, err := CompileJQ(`select(`); err == nil {
		t.Fatal("expected a compile error for an invalid program")
	} else if !strings.Contains(err.Error(), "invalid --jq filter") {
		t.Fatalf("err = %v, want invalid --jq filter", err)
	}
}

func TestCompileJQ_RunRecord_Reusable(t *testing.T) {
	// A compiled program must be safe to run over many records in sequence.
	prog, err := CompileJQ(`{h: .host}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, host := range []string{"a", "b", "c"} {
		out, err := prog.RunRecord(map[string]interface{}{"host": host})
		if err != nil {
			t.Fatalf("run %s: %v", host, err)
		}
		if len(out) != 1 {
			t.Fatalf("host %s: want 1 value, got %#v", host, out)
		}
		obj, ok := out[0].(map[string]interface{})
		if !ok || obj["h"] != host {
			t.Fatalf("host %s: got %#v", host, out[0])
		}
	}
}

func TestNormalizeJQOutputFormat(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "json", want: "json"},
		{in: "yaml", want: "yaml"},
		{in: "yml", want: "yml"},
		{in: "toon", want: "toon"},
		{in: "table", want: "json"},
		{in: "csv", want: "json"},
		{in: "", want: "json"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := NormalizeJQOutputFormat(tt.in); got != tt.want {
				t.Fatalf("NormalizeJQOutputFormat(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyJQ_NullResolutionIsAnError(t *testing.T) {
	// The `query` payload shape: a filter written for the agent envelope
	// (".result.records") addresses a key that does not exist here, and jq
	// answers null. That must surface as an error naming the real keys, not as
	// a result indistinguishable from an empty one (#413).
	in := map[string]interface{}{
		"records":  []interface{}{map[string]interface{}{"display_id": "P-1"}},
		"metadata": map[string]interface{}{"grail": map[string]interface{}{}},
	}

	out, err := ApplyJQ(".result.records", in)
	if err == nil {
		t.Fatalf("expected an error for a filter resolving to null, got result: %#v", out)
	}

	var jqErr *JQError
	if !errors.As(err, &jqErr) {
		t.Fatalf("err = %T (%v), want *JQError", err, err)
	}
	if jqErr.Code != JQShapeMismatchCode {
		t.Errorf("code = %q, want %q", jqErr.Code, JQShapeMismatchCode)
	}
	// Naming the available keys is what makes the mismatch self-correcting.
	if !strings.Contains(jqErr.Message, "[metadata, records]") {
		t.Errorf("message does not name the input keys: %q", jqErr.Message)
	}
	if !strings.Contains(jqErr.Message, ".result.records") {
		t.Errorf("message does not echo the filter: %q", jqErr.Message)
	}
	// A filter reaching for .result on a payload without it gets the specific
	// envelope-vs-payload hint first.
	if len(jqErr.Suggestions) == 0 || !strings.Contains(jqErr.Suggestions[0], "result payload") {
		t.Errorf("suggestions = %#v, want the envelope-vs-payload hint first", jqErr.Suggestions)
	}
}

func TestApplyJQ_MissingKeyNamesArrayShape(t *testing.T) {
	// A list payload (e.g. `plugin list`) reports its shape as an array, so the
	// caller learns to index rather than to key into it. (Keying straight into
	// an array — ".result" here — is already a gojq runtime error; the null case
	// is a miss one level down.)
	in := []interface{}{map[string]interface{}{"name": "alpha"}}

	_, err := ApplyJQ(".[0].title", in)
	if err == nil {
		t.Fatal("expected an error for a missing key on an array element")
	}
	if !strings.Contains(err.Error(), "array of 1 element(s)") {
		t.Errorf("message does not describe the array shape: %v", err)
	}
}

func TestApplyJQ_EmptySelectionIsNotAnError(t *testing.T) {
	// A filter that selects nothing is a genuine empty result, and must stay
	// distinguishable from the shape mismatch above: it yields [], not null.
	in := map[string]interface{}{"records": []interface{}{
		map[string]interface{}{"status": 200},
	}}

	out, err := ApplyJQ(".records[] | select(.status == 500)", in)
	if err != nil {
		t.Fatalf("ApplyJQ failed for an empty selection: %v", err)
	}
	if !reflect.DeepEqual(out, []interface{}{}) {
		t.Fatalf("out = %#v, want an empty list", out)
	}
}

func TestApplyJQ_ExistingEmptyListPassesThrough(t *testing.T) {
	// An empty rows array is a real answer, not a mismatch.
	in := map[string]interface{}{"records": []interface{}{}}

	out, err := ApplyJQ(".records", in)
	if err != nil {
		t.Fatalf("ApplyJQ failed: %v", err)
	}
	if !reflect.DeepEqual(out, []interface{}{}) {
		t.Fatalf("out = %#v, want an empty list", out)
	}
}

func TestDescribeJQInput(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  string
	}{
		{"object", map[string]interface{}{"b": 1, "a": 2}, "object with keys [a, b]"},
		{"empty object", map[string]interface{}{}, "an empty object"},
		{"array", []interface{}{1, 2, 3}, "array of 3 element(s)"},
		{"null", nil, "is null"},
		{"string", "x", "is a string"},
		{"bool", true, "is a boolean"},
		{"number", float64(3), "is a number"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeJQInput(tt.input); !strings.Contains(got, tt.want) {
				t.Fatalf("describeJQInput(%#v) = %q, want it to contain %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestApplyJQ_IteratingAMissingKeyIsAShapeMismatch pins that iterating a missing key is a shape mismatch.
func TestApplyJQ_IteratingAMissingKeyIsAShapeMismatch(t *testing.T) {
	in := map[string]interface{}{"records": []interface{}{map[string]interface{}{"n": 1}}}
	_, err := ApplyJQ(".result.records[] | .n", in)
	var jqErr *JQError
	if !errors.As(err, &jqErr) || jqErr.Code != JQShapeMismatchCode {
		t.Fatalf("err = %v, want a %s error", err, JQShapeMismatchCode)
	}
	if len(jqErr.Suggestions) == 0 || !strings.Contains(jqErr.Suggestions[0], "result payload") {
		t.Errorf("suggestions = %#v, want the envelope-vs-payload hint first", jqErr.Suggestions)
	}

	// A real type error on a key that exists stays a plain error.
	_, err = ApplyJQ(".records[] | .n[]", in)
	if err == nil || errors.As(err, &jqErr) {
		t.Fatalf("err = %v, want a plain jq error", err)
	}
}
