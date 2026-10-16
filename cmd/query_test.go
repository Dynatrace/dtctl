package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
)

func TestIsSupportedQueryOutputFormat(t *testing.T) {
	tests := []struct {
		name   string
		format string
		want   bool
	}{
		{name: "default", format: "", want: true},
		{name: "json", format: "json", want: true},
		{name: "yaml alias", format: "yml", want: true},
		{name: "chart", format: "chart", want: true},
		{name: "spark alias", format: "spark", want: true},
		{name: "bar alias", format: "bar", want: true},
		{name: "braille alias", format: "br", want: true},
		{name: "toon", format: "toon", want: true},
		{name: "jsonl", format: "jsonl", want: true},
		{name: "parquet", format: "parquet", want: true},
		{name: "auto", format: "auto", want: true},
		{name: "trimmed and mixed case", format: " Json ", want: true},
		{name: "unsupported", format: "xml", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSupportedQueryOutputFormat(tt.format); got != tt.want {
				t.Fatalf("isSupportedQueryOutputFormat(%q) = %v, want %v", tt.format, got, tt.want)
			}
		})
	}
}

func TestFormatRequiresIncludeTypes(t *testing.T) {
	// Parquet must auto-request DQL types so its schema is faithful (a "long"
	// becomes INT64, not a value-inferred DOUBLE). Other formats must not.
	tests := []struct {
		format string
		want   bool
	}{
		{"parquet", true},
		{" Parquet ", true},
		{"PARQUET", true},
		{"jsonl", false},
		{"json", false},
		{"csv", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			if got := formatRequiresIncludeTypes(tt.format); got != tt.want {
				t.Errorf("formatRequiresIncludeTypes(%q) = %v, want %v", tt.format, got, tt.want)
			}
		})
	}
}

func TestParseSegmentFlags(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []exec.FilterSegmentRef
		wantErr bool
		errMsg  string
	}{
		{
			name:  "single segment",
			input: []string{"seg-uid-1"},
			want:  []exec.FilterSegmentRef{{ID: "seg-uid-1"}},
		},
		{
			name:  "multiple segments",
			input: []string{"seg-uid-1", "seg-uid-2", "seg-uid-3"},
			want: []exec.FilterSegmentRef{
				{ID: "seg-uid-1"},
				{ID: "seg-uid-2"},
				{ID: "seg-uid-3"},
			},
		},
		{
			name:  "trims whitespace",
			input: []string{"  seg-uid-1  "},
			want:  []exec.FilterSegmentRef{{ID: "seg-uid-1"}},
		},
		{
			name:    "empty string rejected",
			input:   []string{""},
			wantErr: true,
		},
		{
			name:    "whitespace-only rejected",
			input:   []string{"  "},
			wantErr: true,
		},
		{
			name:  "empty slice",
			input: []string{},
			want:  nil,
		},
		// Inline variable tests
		{
			name:  "single variable",
			input: []string{"seg-1?host=HOST-001"},
			want: []exec.FilterSegmentRef{
				{ID: "seg-1", Variables: []exec.FilterSegmentVariable{
					{Name: "host", Values: []string{"HOST-001"}},
				}},
			},
		},
		{
			name:  "multiple values comma-separated",
			input: []string{"seg-1?host=HOST-001,HOST-002"},
			want: []exec.FilterSegmentRef{
				{ID: "seg-1", Variables: []exec.FilterSegmentVariable{
					{Name: "host", Values: []string{"HOST-001", "HOST-002"}},
				}},
			},
		},
		{
			name:  "multiple variables with ampersand",
			input: []string{"seg-1?host=HOST-001&ns=production"},
			want: []exec.FilterSegmentRef{
				{ID: "seg-1", Variables: []exec.FilterSegmentVariable{
					{Name: "host", Values: []string{"HOST-001"}},
					{Name: "ns", Values: []string{"production"}},
				}},
			},
		},
		{
			name:  "segment name with spaces and variables",
			input: []string{"My Segment?host=HOST-001"},
			want: []exec.FilterSegmentRef{
				{ID: "My Segment", Variables: []exec.FilterSegmentVariable{
					{Name: "host", Values: []string{"HOST-001"}},
				}},
			},
		},
		{
			name:    "empty segment ID before ?",
			input:   []string{"?host=val"},
			wantErr: true,
			errMsg:  "segment ID must not be empty",
		},
		{
			name:    "empty variables after ?",
			input:   []string{"seg-1?"},
			wantErr: true,
			errMsg:  "expected variables after '?'",
		},
		{
			name:    "missing equals in variable",
			input:   []string{"seg-1?hostvalue"},
			wantErr: true,
			errMsg:  "expected VARIABLE=VALUE",
		},
		{
			name:    "empty variable name",
			input:   []string{"seg-1?=value"},
			wantErr: true,
			errMsg:  "variable name must not be empty",
		},
		{
			name:    "empty variable value",
			input:   []string{"seg-1?host="},
			wantErr: true,
			errMsg:  `variable "host" value must not be empty`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSegmentFlags(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSegmentFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseSegmentFlags() got %d refs, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].ID != tt.want[i].ID {
					t.Errorf("ref[%d].ID = %q, want %q", i, got[i].ID, tt.want[i].ID)
				}
				if len(got[i].Variables) != len(tt.want[i].Variables) {
					t.Fatalf("ref[%d] has %d variables, want %d", i, len(got[i].Variables), len(tt.want[i].Variables))
				}
				for j := range tt.want[i].Variables {
					if got[i].Variables[j].Name != tt.want[i].Variables[j].Name {
						t.Errorf("ref[%d].Variables[%d].Name = %q, want %q", i, j, got[i].Variables[j].Name, tt.want[i].Variables[j].Name)
					}
					if len(got[i].Variables[j].Values) != len(tt.want[i].Variables[j].Values) {
						t.Errorf("ref[%d].Variables[%d] has %d values, want %d", i, j, len(got[i].Variables[j].Values), len(tt.want[i].Variables[j].Values))
						continue
					}
					for k := range tt.want[i].Variables[j].Values {
						if got[i].Variables[j].Values[k] != tt.want[i].Variables[j].Values[k] {
							t.Errorf("ref[%d].Variables[%d].Values[%d] = %q, want %q", i, j, k, got[i].Variables[j].Values[k], tt.want[i].Variables[j].Values[k])
						}
					}
				}
			}
		})
	}
}

func TestParseSegmentsFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []exec.FilterSegmentRef
		wantErr bool
	}{
		{
			name:    "simple segment list",
			content: "- id: seg-1\n- id: seg-2\n",
			want: []exec.FilterSegmentRef{
				{ID: "seg-1"},
				{ID: "seg-2"},
			},
		},
		{
			name: "segment with variables",
			content: `- id: seg-1
  variables:
    - name: host
      values:
        - HOST-001
        - HOST-002
- id: seg-2
`,
			want: []exec.FilterSegmentRef{
				{
					ID: "seg-1",
					Variables: []exec.FilterSegmentVariable{
						{Name: "host", Values: []string{"HOST-001", "HOST-002"}},
					},
				},
				{ID: "seg-2"},
			},
		},
		{
			name:    "missing id field",
			content: "- variables:\n    - name: x\n      values: [a]\n",
			wantErr: true,
		},
		{
			name:    "invalid YAML",
			content: "not: a: valid: yaml: [[[",
			wantErr: true,
		},
		{
			name:    "empty file",
			content: "",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "segments.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			got, err := parseSegmentsFile(context.Background(), path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSegmentsFile() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseSegmentsFile() got %d refs, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].ID != tt.want[i].ID {
					t.Errorf("ref[%d].ID = %q, want %q", i, got[i].ID, tt.want[i].ID)
				}
				if len(got[i].Variables) != len(tt.want[i].Variables) {
					t.Errorf("ref[%d] has %d variables, want %d", i, len(got[i].Variables), len(tt.want[i].Variables))
					continue
				}
				for j := range got[i].Variables {
					if got[i].Variables[j].Name != tt.want[i].Variables[j].Name {
						t.Errorf("ref[%d].Variables[%d].Name = %q, want %q", i, j, got[i].Variables[j].Name, tt.want[i].Variables[j].Name)
					}
					if len(got[i].Variables[j].Values) != len(tt.want[i].Variables[j].Values) {
						t.Errorf("ref[%d].Variables[%d] has %d values, want %d", i, j, len(got[i].Variables[j].Values), len(tt.want[i].Variables[j].Values))
					}
				}
			}
		})
	}
}

func TestParseSegmentsFile_NotFound(t *testing.T) {
	_, err := parseSegmentsFile(context.Background(), "/nonexistent/path/segments.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestMergeSegmentRefs(t *testing.T) {
	tests := []struct {
		name     string
		flagRefs []exec.FilterSegmentRef
		fileRefs []exec.FilterSegmentRef
		wantIDs  []string
	}{
		{
			name:     "flags only",
			flagRefs: []exec.FilterSegmentRef{{ID: "a"}, {ID: "b"}},
			fileRefs: nil,
			wantIDs:  []string{"a", "b"},
		},
		{
			name:     "file only",
			flagRefs: nil,
			fileRefs: []exec.FilterSegmentRef{{ID: "x"}, {ID: "y"}},
			wantIDs:  []string{"x", "y"},
		},
		{
			name:     "file wins on conflict",
			flagRefs: []exec.FilterSegmentRef{{ID: "a"}, {ID: "b"}},
			fileRefs: []exec.FilterSegmentRef{{ID: "b", Variables: []exec.FilterSegmentVariable{{Name: "v", Values: []string{"1"}}}}},
			wantIDs:  []string{"b", "a"},
		},
		{
			name:     "deduplicates by ID",
			flagRefs: []exec.FilterSegmentRef{{ID: "a"}, {ID: "a"}},
			fileRefs: nil,
			wantIDs:  []string{"a"},
		},
		{
			name:     "both empty",
			flagRefs: nil,
			fileRefs: nil,
			wantIDs:  nil,
		},
		{
			name:     "preserves file order then flag order",
			flagRefs: []exec.FilterSegmentRef{{ID: "c"}, {ID: "d"}},
			fileRefs: []exec.FilterSegmentRef{{ID: "a"}, {ID: "b"}},
			wantIDs:  []string{"a", "b", "c", "d"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeSegmentRefs(tt.flagRefs, tt.fileRefs)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("mergeSegmentRefs() got %d refs, want %d", len(got), len(tt.wantIDs))
			}
			for i, id := range tt.wantIDs {
				if got[i].ID != id {
					t.Errorf("merged[%d].ID = %q, want %q", i, got[i].ID, id)
				}
			}
		})
	}
}

func TestMergeSegmentRefs_FileWinsWithVariables(t *testing.T) {
	flagRefs := []exec.FilterSegmentRef{{ID: "seg-1"}}
	fileRefs := []exec.FilterSegmentRef{
		{ID: "seg-1", Variables: []exec.FilterSegmentVariable{
			{Name: "host", Values: []string{"HOST-001"}},
		}},
	}

	got := mergeSegmentRefs(flagRefs, fileRefs)
	if len(got) != 1 {
		t.Fatalf("expected 1 merged ref, got %d", len(got))
	}
	if len(got[0].Variables) != 1 {
		t.Fatalf("expected file entry to win with 1 variable, got %d", len(got[0].Variables))
	}
	if got[0].Variables[0].Name != "host" {
		t.Errorf("variable name = %q, want %q", got[0].Variables[0].Name, "host")
	}
}

func TestParseSegmentVarFlags(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    map[string][]exec.FilterSegmentVariable
		wantErr bool
		errMsg  string
	}{
		{
			name:  "single variable single value",
			input: []string{"seg-1:host=HOST-001"},
			want: map[string][]exec.FilterSegmentVariable{
				"seg-1": {{Name: "host", Values: []string{"HOST-001"}}},
			},
		},
		{
			name:  "single variable multiple values",
			input: []string{"seg-1:host=HOST-001,HOST-002"},
			want: map[string][]exec.FilterSegmentVariable{
				"seg-1": {{Name: "host", Values: []string{"HOST-001", "HOST-002"}}},
			},
		},
		{
			name:  "multiple variables same segment",
			input: []string{"seg-1:host=HOST-001", "seg-1:ns=production"},
			want: map[string][]exec.FilterSegmentVariable{
				"seg-1": {
					{Name: "host", Values: []string{"HOST-001"}},
					{Name: "ns", Values: []string{"production"}},
				},
			},
		},
		{
			name:  "multiple segments",
			input: []string{"seg-1:host=HOST-001", "seg-2:env=prod"},
			want: map[string][]exec.FilterSegmentVariable{
				"seg-1": {{Name: "host", Values: []string{"HOST-001"}}},
				"seg-2": {{Name: "env", Values: []string{"prod"}}},
			},
		},
		{
			name:  "duplicate variable name merges values",
			input: []string{"seg-1:host=HOST-001", "seg-1:host=HOST-002"},
			want: map[string][]exec.FilterSegmentVariable{
				"seg-1": {{Name: "host", Values: []string{"HOST-001", "HOST-002"}}},
			},
		},
		{
			name:  "trims whitespace",
			input: []string{"  seg-1 : host = HOST-001 "},
			want: map[string][]exec.FilterSegmentVariable{
				"seg-1": {{Name: "host", Values: []string{"HOST-001"}}},
			},
		},
		{
			name:  "value containing equals sign",
			input: []string{"seg-1:filter=a=b"},
			want: map[string][]exec.FilterSegmentVariable{
				"seg-1": {{Name: "filter", Values: []string{"a=b"}}},
			},
		},
		{
			name:    "empty string rejected",
			input:   []string{""},
			wantErr: true,
			errMsg:  "must not be empty",
		},
		{
			name:    "missing colon rejected",
			input:   []string{"seg-1-host=value"},
			wantErr: true,
			errMsg:  "expected format",
		},
		{
			name:    "missing equals rejected",
			input:   []string{"seg-1:hostvalue"},
			wantErr: true,
			errMsg:  "expected VARIABLE=VALUE",
		},
		{
			name:    "empty segment ID rejected",
			input:   []string{":host=value"},
			wantErr: true,
			errMsg:  "segment ID must not be empty",
		},
		{
			name:    "empty variable name rejected",
			input:   []string{"seg-1:=value"},
			wantErr: true,
			errMsg:  "variable name must not be empty",
		},
		{
			name:    "empty value rejected",
			input:   []string{"seg-1:host="},
			wantErr: true,
			errMsg:  "variable value must not be empty",
		},
		{
			name:  "empty slice",
			input: []string{},
			want:  map[string][]exec.FilterSegmentVariable{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSegmentVarFlags(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSegmentVarFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseSegmentVarFlags() got %d segments, want %d", len(got), len(tt.want))
			}
			for segID, wantVars := range tt.want {
				gotVars, ok := got[segID]
				if !ok {
					t.Fatalf("missing segment %q in result", segID)
				}
				if len(gotVars) != len(wantVars) {
					t.Fatalf("segment %q: got %d vars, want %d", segID, len(gotVars), len(wantVars))
				}
				for i := range wantVars {
					if gotVars[i].Name != wantVars[i].Name {
						t.Errorf("segment %q var[%d].Name = %q, want %q", segID, i, gotVars[i].Name, wantVars[i].Name)
					}
					if len(gotVars[i].Values) != len(wantVars[i].Values) {
						t.Errorf("segment %q var[%d] has %d values, want %d", segID, i, len(gotVars[i].Values), len(wantVars[i].Values))
						continue
					}
					for j := range wantVars[i].Values {
						if gotVars[i].Values[j] != wantVars[i].Values[j] {
							t.Errorf("segment %q var[%d].Values[%d] = %q, want %q", segID, i, j, gotVars[i].Values[j], wantVars[i].Values[j])
						}
					}
				}
			}
		})
	}
}

func TestApplySegmentVars(t *testing.T) {
	tests := []struct {
		name    string
		refs    []exec.FilterSegmentRef
		varMap  map[string][]exec.FilterSegmentVariable
		origIDs map[string]string
		wantErr bool
		errMsg  string
		check   func(t *testing.T, refs []exec.FilterSegmentRef)
	}{
		{
			name:   "no vars is a no-op",
			refs:   []exec.FilterSegmentRef{{ID: "seg-1"}},
			varMap: nil,
			check: func(t *testing.T, refs []exec.FilterSegmentRef) {
				if len(refs[0].Variables) != 0 {
					t.Errorf("expected no variables, got %d", len(refs[0].Variables))
				}
			},
		},
		{
			name: "applies variable by resolved ID",
			refs: []exec.FilterSegmentRef{{ID: "resolved-uid"}},
			varMap: map[string][]exec.FilterSegmentVariable{
				"resolved-uid": {{Name: "host", Values: []string{"H1"}}},
			},
			origIDs: map[string]string{"resolved-uid": "My Segment"},
			check: func(t *testing.T, refs []exec.FilterSegmentRef) {
				if len(refs[0].Variables) != 1 {
					t.Fatalf("expected 1 variable, got %d", len(refs[0].Variables))
				}
				if refs[0].Variables[0].Name != "host" {
					t.Errorf("variable name = %q, want %q", refs[0].Variables[0].Name, "host")
				}
			},
		},
		{
			name: "applies variable by original name",
			refs: []exec.FilterSegmentRef{{ID: "resolved-uid"}},
			varMap: map[string][]exec.FilterSegmentVariable{
				"My Segment": {{Name: "host", Values: []string{"H1"}}},
			},
			origIDs: map[string]string{"resolved-uid": "My Segment"},
			check: func(t *testing.T, refs []exec.FilterSegmentRef) {
				if len(refs[0].Variables) != 1 {
					t.Fatalf("expected 1 variable, got %d", len(refs[0].Variables))
				}
			},
		},
		{
			name: "CLI vars override file vars for same name",
			refs: []exec.FilterSegmentRef{
				{ID: "seg-1", Variables: []exec.FilterSegmentVariable{
					{Name: "host", Values: []string{"OLD"}},
				}},
			},
			varMap: map[string][]exec.FilterSegmentVariable{
				"seg-1": {{Name: "host", Values: []string{"NEW"}}},
			},
			origIDs: map[string]string{},
			check: func(t *testing.T, refs []exec.FilterSegmentRef) {
				if len(refs[0].Variables) != 1 {
					t.Fatalf("expected 1 variable, got %d", len(refs[0].Variables))
				}
				if refs[0].Variables[0].Values[0] != "NEW" {
					t.Errorf("expected CLI value to override, got %q", refs[0].Variables[0].Values[0])
				}
			},
		},
		{
			name: "adds new variables alongside existing ones",
			refs: []exec.FilterSegmentRef{
				{ID: "seg-1", Variables: []exec.FilterSegmentVariable{
					{Name: "host", Values: []string{"H1"}},
				}},
			},
			varMap: map[string][]exec.FilterSegmentVariable{
				"seg-1": {{Name: "ns", Values: []string{"prod"}}},
			},
			origIDs: map[string]string{},
			check: func(t *testing.T, refs []exec.FilterSegmentRef) {
				if len(refs[0].Variables) != 2 {
					t.Fatalf("expected 2 variables, got %d", len(refs[0].Variables))
				}
			},
		},
		{
			name: "error on unknown segment",
			refs: []exec.FilterSegmentRef{{ID: "seg-1"}},
			varMap: map[string][]exec.FilterSegmentVariable{
				"nonexistent": {{Name: "host", Values: []string{"H1"}}},
			},
			origIDs: map[string]string{},
			wantErr: true,
			errMsg:  "not specified via --segment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origIDs := tt.origIDs
			if origIDs == nil {
				origIDs = map[string]string{}
			}
			got, err := applySegmentVars(tt.refs, tt.varMap, origIDs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("applySegmentVars() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
				return
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

// TestAgentResultFormat pins the agent-mode default: with no -o, agent mode
// resolves to -o auto; any explicit -o wins, and outside agent mode nothing
// changes.
func TestAgentResultFormat(t *testing.T) {
	origFormat, origAgent := outputFormat(context.Background()), agentMode(context.Background())
	flag := rootCmd.PersistentFlags().Lookup("output")
	origChanged := flag.Changed
	defer func() { gFlags.outputFormat, gFlags.agentMode, flag.Changed = origFormat, origAgent, origChanged }()

	cases := []struct {
		name        string
		agent       bool
		changed     bool
		format      string
		want        string
		wantDefault bool
	}{
		{"agent mode, no -o", true, false, "table", "auto", true},
		{"agent mode, explicit -o json", true, true, "json", "json", false},
		{"agent mode, explicit -o toon", true, true, "toon", "toon", false},
		{"not agent mode", false, false, "table", "table", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gFlags.agentMode, flag.Changed, gFlags.outputFormat = c.agent, c.changed, c.format
			got, byDefault := agentResultFormat(context.Background())
			if got != c.want || byDefault != c.wantDefault {
				t.Errorf("agentResultFormat() = (%q, %v), want (%q, %v)", got, byDefault, c.want, c.wantDefault)
			}
		})
	}
}

// TestAgentModeDefaultsCompose pins that the two agent-mode defaults stack: with
// no flags, query resolves to -o auto AND compacts; --compact=false restores
// the uncompacted -o auto output; an explicit -o csv keeps the rows verbatim
// (a CSV has no place for a constant map) without a warning.
func TestAgentModeDefaultsCompose(t *testing.T) {
	origFormat, origAgent := outputFormat(context.Background()), agentMode(context.Background())
	flag := rootCmd.PersistentFlags().Lookup("output")
	origChanged := flag.Changed
	defer func() { gFlags.outputFormat, gFlags.agentMode, flag.Changed = origFormat, origAgent, origChanged }()

	cases := []struct {
		name        string
		args        []string
		outputFlag  string // "" = no -o
		wantFormat  string
		wantDefault bool
		wantCompact bool
	}{
		{name: "no flags", wantFormat: "auto", wantDefault: true, wantCompact: true},
		{name: "--compact=false", args: []string{"--compact=false"}, wantFormat: "auto", wantDefault: true, wantCompact: false},
		{name: "explicit -o json", outputFlag: "json", wantFormat: "json", wantCompact: true},
		{name: "explicit -o csv", outputFlag: "csv", wantFormat: "csv", wantCompact: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gFlags.agentMode = true
			gFlags.outputFormat, flag.Changed = "table", false
			if c.outputFlag != "" {
				gFlags.outputFormat, flag.Changed = c.outputFlag, true
			}
			cmd := &cobra.Command{Use: "q"}
			cmd.Flags().Bool("compact", false, "")
			if err := cmd.Flags().Parse(c.args); err != nil {
				t.Fatal(err)
			}
			format, byDefault := agentResultFormat(context.Background())
			compact, warn := resolveQueryCompact(cmd, agentMode(context.Background()), format)
			if format != c.wantFormat || byDefault != c.wantDefault || compact != c.wantCompact || warn != "" {
				t.Errorf("got (format %q, byDefault %v, compact %v, warn %q), want (%q, %v, %v, \"\")",
					format, byDefault, compact, warn, c.wantFormat, c.wantDefault, c.wantCompact)
			}
		})
	}
}

func TestResolveQueryCompact(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		agent    bool
		format   string
		want     bool
		wantWarn bool
	}{
		// Agent mode is token-optimal by default; outside it nothing changes.
		{name: "agent mode defaults on", agent: true, format: "", want: true},
		{name: "agent mode toon defaults on", agent: true, format: "toon", want: true},
		{name: "agent mode yaml defaults on", agent: true, format: "yaml", want: true},
		{name: "agent mode auto defaults on", agent: true, format: "auto", want: true},
		{name: "plain auto opt-in", args: []string{"--compact"}, format: "AUTO", want: true},
		{name: "plain auto defaults off", format: "auto", want: false},
		{name: "agent mode opt-in", args: []string{"--compact"}, agent: true, format: "", want: true},
		{name: "agent mode toon opt-in", args: []string{"--compact"}, agent: true, format: "toon", want: true},
		{name: "agent mode explicit off", args: []string{"--compact=false"}, agent: true, format: "json", want: false},
		{name: "plain json defaults off", format: "json", want: false},
		{name: "plain json opt-in", args: []string{"--compact"}, format: "json", want: true},
		{name: "plain yaml opt-in", args: []string{"--compact"}, format: "yaml", want: true},
		// A table has no place for a constant map; say so instead of silently ignoring it.
		{name: "plain table opt-in warns", args: []string{"--compact"}, format: "table", want: false, wantWarn: true},
		{name: "csv opt-in warns", args: []string{"--compact"}, agent: true, format: "csv", want: false, wantWarn: true},
		// The agent-mode default is silent where it cannot apply.
		{name: "agent csv default is silently off", agent: true, format: "csv", want: false},
		{name: "agent jsonl default is silently off", agent: true, format: "jsonl", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "q"}
			cmd.Flags().Bool("compact", false, "")
			if err := cmd.Flags().Parse(tt.args); err != nil {
				t.Fatal(err)
			}
			got, warn := resolveQueryCompact(cmd, tt.agent, tt.format)
			if got != tt.want {
				t.Errorf("compact = %v, want %v", got, tt.want)
			}
			if (warn != "") != tt.wantWarn {
				t.Errorf("warning = %q, wantWarn %v", warn, tt.wantWarn)
			}
		})
	}
}
