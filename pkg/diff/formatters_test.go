package diff

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestUnifiedFormatter_Format(t *testing.T) {
	tests := []struct {
		name    string
		result  *DiffResult
		wantErr bool
		checks  []string
	}{
		{
			name: "no changes",
			result: &DiffResult{
				HasChanges: false,
				Changes:    []Change{},
			},
			wantErr: false,
			checks:  []string{},
		},
		{
			name: "field changed",
			result: &DiffResult{
				HasChanges: true,
				LeftLabel:  "left",
				RightLabel: "right",
				Changes: []Change{
					{
						Path:      "key",
						Operation: ChangeOpReplace,
						OldValue:  "old",
						NewValue:  "new",
					},
				},
			},
			wantErr: false,
			checks:  []string{"---", "+++", "- key:", "+ key:"},
		},
		{
			name: "field added",
			result: &DiffResult{
				HasChanges: true,
				LeftLabel:  "left",
				RightLabel: "right",
				Changes: []Change{
					{
						Path:      "newkey",
						Operation: ChangeOpAdd,
						NewValue:  "value",
					},
				},
			},
			wantErr: false,
			checks:  []string{"+ newkey:"},
		},
		{
			name: "field removed",
			result: &DiffResult{
				HasChanges: true,
				LeftLabel:  "left",
				RightLabel: "right",
				Changes: []Change{
					{
						Path:      "oldkey",
						Operation: ChangeOpRemove,
						OldValue:  "value",
					},
				},
			},
			wantErr: false,
			checks:  []string{"- oldkey:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &UnifiedFormatter{
				contextLines: 3,
				colorize:     false,
			}

			got, err := f.Format(tt.result)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnifiedFormatter.Format() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			for _, check := range tt.checks {
				if !strings.Contains(got, check) {
					t.Errorf("UnifiedFormatter.Format() output missing %q, got:\n%s", check, got)
				}
			}
		})
	}
}

func TestJSONPatchFormatter_Format(t *testing.T) {
	tests := []struct {
		name    string
		result  *DiffResult
		wantErr bool
		checks  []string
	}{
		{
			name: "no changes",
			result: &DiffResult{
				HasChanges: false,
				Changes:    []Change{},
			},
			wantErr: false,
			checks:  []string{"[]"},
		},
		{
			name: "replace operation",
			result: &DiffResult{
				HasChanges: true,
				Changes: []Change{
					{
						Path:      "key",
						Operation: ChangeOpReplace,
						NewValue:  "new",
					},
				},
			},
			wantErr: false,
			checks:  []string{`"op"`, `"replace"`, `"path"`, `"/key"`, `"value"`},
		},
		{
			name: "add operation",
			result: &DiffResult{
				HasChanges: true,
				Changes: []Change{
					{
						Path:      "newkey",
						Operation: ChangeOpAdd,
						NewValue:  "value",
					},
				},
			},
			wantErr: false,
			checks:  []string{`"op"`, `"add"`, `"path"`, `"/newkey"`},
		},
		{
			name: "remove operation",
			result: &DiffResult{
				HasChanges: true,
				Changes: []Change{
					{
						Path:      "oldkey",
						Operation: ChangeOpRemove,
					},
				},
			},
			wantErr: false,
			checks:  []string{`"op"`, `"remove"`, `"path"`, `"/oldkey"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &JSONPatchFormatter{}

			got, err := f.Format(tt.result)
			if (err != nil) != tt.wantErr {
				t.Errorf("JSONPatchFormatter.Format() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			for _, check := range tt.checks {
				if !strings.Contains(got, check) {
					t.Errorf("JSONPatchFormatter.Format() output missing %q, got:\n%s", check, got)
				}
			}
		})
	}
}

func TestSemanticFormatter_Format(t *testing.T) {
	tests := []struct {
		name    string
		result  *DiffResult
		wantErr bool
		checks  []string
	}{
		{
			name: "no changes",
			result: &DiffResult{
				HasChanges: false,
				Changes:    []Change{},
			},
			wantErr: false,
			checks:  []string{"No changes detected"},
		},
		{
			name: "with changes",
			result: &DiffResult{
				HasChanges: true,
				LeftLabel:  "left",
				RightLabel: "right",
				Changes: []Change{
					{
						Path:      "key",
						Operation: ChangeOpReplace,
						OldValue:  "old",
						NewValue:  "new",
					},
				},
				Summary: DiffSummary{
					Modified: 1,
					Impact:   ImpactLow,
				},
			},
			wantErr: false,
			checks:  []string{"Changes:", "Summary:", "Impact:", "low"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &SemanticFormatter{}

			got, err := f.Format(tt.result)
			if (err != nil) != tt.wantErr {
				t.Errorf("SemanticFormatter.Format() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			for _, check := range tt.checks {
				if !strings.Contains(got, check) {
					t.Errorf("SemanticFormatter.Format() output missing %q, got:\n%s", check, got)
				}
			}
		})
	}
}

func TestSideBySideFormatter_Format(t *testing.T) {
	result := &DiffResult{
		HasChanges: true,
		LeftLabel:  "left",
		RightLabel: "right",
		Changes: []Change{
			{
				Path:      "key",
				Operation: ChangeOpReplace,
				OldValue:  "old",
				NewValue:  "new",
			},
		},
	}

	f := &SideBySideFormatter{
		width:    120,
		colorize: false,
	}

	got, err := f.Format(result)
	if err != nil {
		t.Errorf("SideBySideFormatter.Format() error = %v", err)
		return
	}

	if !strings.Contains(got, "|") {
		t.Errorf("SideBySideFormatter.Format() should contain separator '|'")
	}

	if !strings.Contains(got, "left") || !strings.Contains(got, "right") {
		t.Errorf("SideBySideFormatter.Format() should contain labels")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{
			name:   "no truncation needed",
			s:      "short",
			maxLen: 10,
			want:   "short",
		},
		{
			name:   "truncate with ellipsis",
			s:      "this is a very long string",
			maxLen: 10,
			want:   "this is...",
		},
		{
			name:   "truncate very short",
			s:      "hello",
			maxLen: 2,
			want:   "he",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.s, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    interface{}
		want string
	}{
		{
			name: "string value",
			v:    "hello",
			want: `"hello"`,
		},
		{
			name: "integer value",
			v:    42,
			want: "42",
		},
		{
			name: "boolean value",
			v:    true,
			want: "true",
		},
		{
			name: "nil value",
			v:    nil,
			want: "<nil>",
		},
		{
			name: "map value",
			v:    map[string]interface{}{"key": "value"},
			want: `{"key":"value"}`,
		},
		{
			name: "array value",
			v:    []interface{}{"a", "b", "c"},
			want: `["a","b","c"]`,
		},
		{
			name: "nested map",
			v:    map[string]interface{}{"outer": map[string]interface{}{"inner": "value"}},
			want: `{"outer":{"inner":"value"}}`,
		},
		{
			name: "empty map",
			v:    map[string]interface{}{},
			want: `{}`,
		},
		{
			name: "empty array",
			v:    []interface{}{},
			want: `[]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatValue(tt.v)
			if got != tt.want {
				t.Errorf("formatValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSideBySideFormatter_AllOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		operation ChangeOperation
		path      string
		oldValue  interface{}
		newValue  interface{}
		checks    []string
	}{
		{
			name:      "add operation",
			operation: ChangeOpAdd,
			path:      "newField",
			newValue:  "newValue",
			checks:    []string{"newField", "newValue", "|"},
		},
		{
			name:      "remove operation",
			operation: ChangeOpRemove,
			path:      "oldField",
			oldValue:  "oldValue",
			checks:    []string{"oldField", "oldValue", "|"},
		},
		{
			name:      "replace operation",
			operation: ChangeOpReplace,
			path:      "field",
			oldValue:  "before",
			newValue:  "after",
			checks:    []string{"field", "before", "after", "|"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := &DiffResult{
				HasChanges: true,
				LeftLabel:  "old",
				RightLabel: "new",
				Changes: []Change{
					{
						Path:      tt.path,
						Operation: tt.operation,
						OldValue:  tt.oldValue,
						NewValue:  tt.newValue,
					},
				},
			}

			f := &SideBySideFormatter{
				width:    120,
				colorize: false,
			}

			got, err := f.Format(result)
			if err != nil {
				t.Fatalf("SideBySideFormatter.Format() error = %v", err)
			}

			for _, check := range tt.checks {
				if !strings.Contains(got, check) {
					t.Errorf("SideBySideFormatter.Format() output missing %q, got:\n%s", check, got)
				}
			}
		})
	}
}

func TestSideBySideFormatter_NoChanges(t *testing.T) {
	t.Parallel()

	result := &DiffResult{
		HasChanges: false,
		Changes:    []Change{},
	}

	f := &SideBySideFormatter{
		width:    120,
		colorize: false,
	}

	got, err := f.Format(result)
	if err != nil {
		t.Errorf("SideBySideFormatter.Format() error = %v", err)
	}

	if got != "" {
		t.Errorf("SideBySideFormatter.Format() with no changes should return empty string, got: %q", got)
	}
}

func TestSemanticFormatter_AllOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		operation ChangeOperation
		path      string
		oldValue  interface{}
		newValue  interface{}
		symbol    string
	}{
		{
			name:      "add shows plus",
			operation: ChangeOpAdd,
			path:      "newField",
			newValue:  "value",
			symbol:    "+",
		},
		{
			name:      "remove shows minus",
			operation: ChangeOpRemove,
			path:      "oldField",
			oldValue:  "value",
			symbol:    "-",
		},
		{
			name:      "replace shows tilde",
			operation: ChangeOpReplace,
			path:      "field",
			oldValue:  "old",
			newValue:  "new",
			symbol:    "~",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := &DiffResult{
				HasChanges: true,
				LeftLabel:  "old",
				RightLabel: "new",
				Changes: []Change{
					{
						Path:      tt.path,
						Operation: tt.operation,
						OldValue:  tt.oldValue,
						NewValue:  tt.newValue,
					},
				},
				Summary: DiffSummary{
					Modified: 1,
					Impact:   ImpactLow,
				},
			}

			f := &SemanticFormatter{}
			got, err := f.Format(result)
			if err != nil {
				t.Fatalf("SemanticFormatter.Format() error = %v", err)
			}

			if !strings.Contains(got, tt.symbol) {
				t.Errorf("SemanticFormatter.Format() should contain symbol %q, got:\n%s", tt.symbol, got)
			}

			if !strings.Contains(got, tt.path) {
				t.Errorf("SemanticFormatter.Format() should contain path %q, got:\n%s", tt.path, got)
			}
		})
	}
}

func TestJSONPatchUsesRFC6901Pointers(t *testing.T) {
	differ := NewDiffer(DiffOptions{Format: DiffFormatJSONPatch})
	left := map[string]interface{}{
		"tasks": []interface{}{map[string]interface{}{"name": "a"}, map[string]interface{}{"name": "b"}},
		"a/b":   "x",
		"c~d":   "y",
		"e.f":   "z",
	}
	right := map[string]interface{}{
		"tasks": []interface{}{map[string]interface{}{"name": "a"}, map[string]interface{}{"name": "B"}, "new"},
		"a/b":   "x2",
		"c~d":   "y2",
		"e.f":   "z2",
	}
	result, err := differ.Compare(left, right, "l", "r")
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&JSONPatchFormatter{}).Format(result)
	if err != nil {
		t.Fatal(err)
	}
	var patch []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &patch); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, op := range patch {
		got[op["path"].(string)] = true
	}
	for _, want := range []string{"/tasks/1/name", "/tasks/2", "/a~1b", "/c~0d", "/e.f"} {
		if !got[want] {
			t.Errorf("missing pointer %q in %v", want, got)
		}
	}
	for p := range got {
		if strings.Contains(p, "[") {
			t.Errorf("pointer %q still uses bracket indexes", p)
		}
	}
}

// applyPatch applies add/remove/replace operations with RFC 6901 pointers.
func applyPatch(t *testing.T, doc interface{}, patch []map[string]interface{}) interface{} {
	t.Helper()
	for _, op := range patch {
		path := op["path"].(string)
		if path == "" {
			doc = op["value"]
			continue
		}
		var toks []string
		for _, tok := range strings.Split(path[1:], "/") {
			tok = strings.ReplaceAll(tok, "~1", "/")
			toks = append(toks, strings.ReplaceAll(tok, "~0", "~"))
		}
		doc = applyAt(t, doc, toks, op["op"].(string), op["value"])
	}
	return doc
}

func applyAt(t *testing.T, node interface{}, toks []string, op string, value interface{}) interface{} {
	t.Helper()
	tok := toks[0]
	switch n := node.(type) {
	case map[string]interface{}:
		if len(toks) == 1 {
			if op == "remove" {
				delete(n, tok)
			} else {
				n[tok] = value
			}
			return n
		}
		n[tok] = applyAt(t, n[tok], toks[1:], op, value)
		return n
	case []interface{}:
		i, err := strconv.Atoi(tok)
		if err != nil || i < 0 || i > len(n) || (op != "add" && i == len(n)) {
			t.Fatalf("pointer index %q out of range for array of %d", tok, len(n))
		}
		if len(toks) > 1 {
			n[i] = applyAt(t, n[i], toks[1:], op, value)
			return n
		}
		switch op {
		case "remove":
			return append(n[:i], n[i+1:]...)
		case "add":
			return append(n[:i], append([]interface{}{value}, n[i:]...)...)
		default:
			n[i] = value
			return n
		}
	}
	t.Fatalf("cannot descend into %T with %v", node, toks)
	return nil
}

func TestJSONPatchAppliesToTheLeftDocument(t *testing.T) {
	cases := map[string][2]string{
		"shrink":      {`{"l":["a","b","c","d"]}`, `{"l":["a"]}`},
		"grow":        {`{"l":["a"]}`, `{"l":["a","b","c"]}`},
		"nested":      {`{"t":[{"n":"a"},{"n":"b"},{"n":"c"}]}`, `{"t":[{"n":"a"},{"n":"B"}]}`},
		"escaped key": {`{"a/b":[1,2,3],"c~d":1}`, `{"a/b":[1],"c~d":2}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var left, right interface{}
			if err := json.Unmarshal([]byte(c[0]), &left); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(c[1]), &right); err != nil {
				t.Fatal(err)
			}
			result, err := NewDiffer(DiffOptions{Format: DiffFormatJSONPatch}).Compare(left, right, "l", "r")
			if err != nil {
				t.Fatal(err)
			}
			out, err := (&JSONPatchFormatter{}).Format(result)
			if err != nil {
				t.Fatal(err)
			}
			var patch []map[string]interface{}
			if err := json.Unmarshal([]byte(out), &patch); err != nil {
				t.Fatal(err)
			}
			got := applyPatch(t, left, patch)
			if !reflect.DeepEqual(got, right) {
				t.Errorf("patched %v, want %v (patch %s)", got, right, out)
			}
		})
	}
}
