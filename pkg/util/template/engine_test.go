package template

import (
	"strings"
	"testing"
)

func TestParseSetFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   []string
		want    map[string]interface{}
		wantErr bool
	}{
		{
			name:  "single flag",
			flags: []string{"host=h-123"},
			want:  map[string]interface{}{"host": "h-123"},
		},
		{
			name:  "multiple flags",
			flags: []string{"host=h-123", "timerange=1h", "limit=100"},
			want: map[string]interface{}{
				"host":      "h-123",
				"timerange": "1h",
				"limit":     "100",
			},
		},
		{
			name:  "value with spaces",
			flags: []string{"message=hello world"},
			want:  map[string]interface{}{"message": "hello world"},
		},
		{
			name:  "value with equals sign",
			flags: []string{"filter=status=ERROR"},
			want:  map[string]interface{}{"filter": "status=ERROR"},
		},
		{
			name:    "invalid format - no equals",
			flags:   []string{"invalid"},
			wantErr: true,
		},
		{
			name:    "invalid format - empty key",
			flags:   []string{"=value"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSetFlags(tt.flags)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSetFlags() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				for k, v := range tt.want {
					if got[k] != v {
						t.Errorf("ParseSetFlags() got[%s] = %v, want %v", k, got[k], v)
					}
				}
			}
		})
	}
}

func TestRenderTemplate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		template string
		vars     map[string]interface{}
		want     string
		wantErr  bool
	}{
		{
			name:     "simple substitution",
			template: "fetch logs | filter host = \"{{.host}}\"",
			vars:     map[string]interface{}{"host": "h-123"},
			want:     "fetch logs | filter host = \"h-123\"",
		},
		{
			name:     "multiple variables",
			template: "fetch logs | filter host = \"{{.host}}\" | limit {{.limit}}",
			vars:     map[string]interface{}{"host": "h-123", "limit": "100"},
			want:     "fetch logs | filter host = \"h-123\" | limit 100",
		},
		{
			name:     "default value used when variable missing",
			template: "fetch logs | filter timestamp > now() - {{.timerange | default \"1h\"}}",
			vars:     map[string]interface{}{},
			want:     "fetch logs | filter timestamp > now() - 1h",
		},
		{
			name:     "default value not used when variable provided",
			template: "fetch logs | limit {{.limit | default 100}}",
			vars:     map[string]interface{}{"limit": "50"},
			want:     "fetch logs | limit 50",
		},
		{
			name:     "no variables",
			template: "fetch logs | limit 10",
			vars:     map[string]interface{}{},
			want:     "fetch logs | limit 10",
		},
		{
			name:     "missing variable is an error",
			template: "fetch logs | filter host = \"{{.host}}\"",
			vars:     map[string]interface{}{},
			wantErr:  true,
		},
		{
			name:     "invalid template syntax",
			template: "fetch logs | filter {{.unclosed",
			vars:     map[string]interface{}{},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RenderTemplate(tt.template, tt.vars)
			if (err != nil) != tt.wantErr {
				t.Errorf("RenderTemplate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("RenderTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContainsTemplate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		str  string
		want bool
	}{
		{
			name: "contains template",
			str:  "fetch logs | filter host = \"{{.host}}\"",
			want: true,
		},
		{
			name: "no template",
			str:  "fetch logs | limit 10",
			want: false,
		},
		{
			name: "only opening braces",
			str:  "fetch logs {{",
			want: false,
		},
		{
			name: "only closing braces",
			str:  "fetch logs }}",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ContainsTemplate(tt.str); got != tt.want {
				t.Errorf("ContainsTemplate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateTemplate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		template string
		wantVars []string
		wantErr  bool
	}{
		{
			name:     "single variable",
			template: "fetch logs | filter host = \"{{.host}}\"",
			wantVars: []string{"host"},
		},
		{
			name:     "multiple variables",
			template: "fetch logs | filter host = \"{{.host}}\" and status = \"{{.status}}\"",
			wantVars: []string{"host", "status"},
		},
		{
			name:     "variable with default",
			template: "fetch logs | limit {{.limit | default 100}}",
			wantVars: []string{"limit"},
		},
		{
			name:     "no variables",
			template: "fetch logs | limit 10",
			wantVars: []string{},
		},
		{
			name:     "invalid syntax",
			template: "fetch logs | filter {{.unclosed",
			wantErr:  true,
		},
		{
			name:     "variable with pipe and space",
			template: "{{.value | default \"test\"}}",
			wantVars: []string{"value"},
		},
		{
			name:     "variable with space before pipe",
			template: "{{.value  |  default \"test\"}}",
			wantVars: []string{"value"},
		},
		{
			name:     "duplicate variables",
			template: "{{.host}} and {{.host}} again",
			wantVars: []string{"host"}, // Should only return once
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotVars, err := ValidateTemplate(tt.template)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateTemplate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(gotVars) != len(tt.wantVars) {
					t.Errorf("ValidateTemplate() found %d vars, want %d", len(gotVars), len(tt.wantVars))
				}
				// Check all expected vars are present
				for _, wantVar := range tt.wantVars {
					found := false
					for _, gotVar := range gotVars {
						if gotVar == wantVar {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("ValidateTemplate() missing variable %q", wantVar)
					}
				}
			}
		})
	}
}

func TestRenderTemplate_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		template string
		vars     map[string]interface{}
		want     string
		wantErr  bool
	}{
		{
			name:     "nil variable value",
			template: "value: {{.nilval}}",
			vars:     map[string]interface{}{"nilval": nil},
			want:     "value: <no value>",
		},
		{
			name:     "empty string variable",
			template: "value: {{.empty}}",
			vars:     map[string]interface{}{"empty": ""},
			want:     "value: ",
		},
		{
			name:     "default with nil value",
			template: "{{.nilval | default \"fallback\"}}",
			vars:     map[string]interface{}{"nilval": nil},
			want:     "fallback",
		},
		{
			name:     "default with empty string",
			template: "{{.empty | default \"fallback\"}}",
			vars:     map[string]interface{}{"empty": ""},
			want:     "fallback",
		},
		{
			name:     "numeric value",
			template: "count: {{.count}}",
			vars:     map[string]interface{}{"count": 42},
			want:     "count: 42",
		},
		{
			name:     "boolean value",
			template: "enabled: {{.enabled}}",
			vars:     map[string]interface{}{"enabled": true},
			want:     "enabled: true",
		},
		{
			name:     "execute error - invalid action",
			template: "{{.value.invalid.chain}}",
			vars:     map[string]interface{}{"value": "string"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RenderTemplate(tt.template, tt.vars)
			if (err != nil) != tt.wantErr {
				t.Errorf("RenderTemplate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("RenderTemplate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseSetFlags_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   []string
		want    map[string]interface{}
		wantErr bool
		errMsg  string
	}{
		{
			name:  "empty slice",
			flags: []string{},
			want:  map[string]interface{}{},
		},
		{
			name:    "whitespace only key",
			flags:   []string{"  =value"},
			wantErr: true,
			errMsg:  "empty key",
		},
		{
			name:  "whitespace around key and value",
			flags: []string{"  key  =  value  "},
			want:  map[string]interface{}{"key": "value"},
		},
		{
			name:  "multiple equals signs in value",
			flags: []string{"equation=x=y=z"},
			want:  map[string]interface{}{"equation": "x=y=z"},
		},
		{
			name:  "empty value is valid",
			flags: []string{"key="},
			want:  map[string]interface{}{"key": ""},
		},
		{
			name:    "only equals sign",
			flags:   []string{"="},
			wantErr: true,
			errMsg:  "empty key",
		},
		{
			name:    "multiple flags with one invalid",
			flags:   []string{"valid=value", "invalid"},
			wantErr: true,
			errMsg:  "invalid --set format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSetFlags(tt.flags)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSetFlags() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" {
				if err != nil && !contains(err.Error(), tt.errMsg) {
					t.Errorf("ParseSetFlags() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("ParseSetFlags() returned %d items, want %d", len(got), len(tt.want))
				}
				for k, v := range tt.want {
					if got[k] != v {
						t.Errorf("ParseSetFlags() got[%s] = %v, want %v", k, got[k], v)
					}
				}
			}
		})
	}
}

func TestContainsTemplate_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		str  string
		want bool
	}{
		{
			name: "empty string",
			str:  "",
			want: false,
		},
		{
			name: "nested braces",
			str:  "{{{.value}}}",
			want: true,
		},
		{
			name: "braces in middle",
			str:  "start {{.var}} end",
			want: true,
		},
		{
			name: "multiple templates",
			str:  "{{.a}} and {{.b}}",
			want: true,
		},
		{
			name: "single opening brace",
			str:  "{",
			want: false,
		},
		{
			name: "single closing brace",
			str:  "}",
			want: false,
		},
		{
			name: "reversed braces",
			str:  "}}{{",
			want: true, // Still contains both {{ and }}
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ContainsTemplate(tt.str); got != tt.want {
				t.Errorf("ContainsTemplate(%q) = %v, want %v", tt.str, got, tt.want)
			}
		})
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestRenderTemplate_RejectsUnusableVariables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		template string
		vars     map[string]interface{}
		wantErr  string // substring naming the variable; "" means success
	}{
		{"missing variable is named", `a {{.host}} b {{.port}}`, map[string]interface{}{"port": "1"}, `"host"`},
		{"missing nested variable is named", `{{.cfg.name}}`, map[string]interface{}{}, `"cfg"`},
		{"unused --set variable is named", `a {{.host}}`, map[string]interface{}{"host": "h", "hots": "x"}, `"hots"`},
		{"unused variable with no placeholders", `plain text`, map[string]interface{}{"x": "1"}, `"x"`},
		{"default covers a missing variable", `{{.t | default "1h"}}`, map[string]interface{}{}, ""},
		{"default as function call", `{{default "1h" .t}}`, map[string]interface{}{}, ""},
		{"default does not excuse an unused --set", `{{.t | default "1h"}}`, map[string]interface{}{"u": "1"}, `"u"`},
		{"variable read only in a condition counts as used", `{{if .on}}x{{end}}`, map[string]interface{}{"on": "1"}, ""},
		{"index reads the root map", `{{index . "host-name"}}`, map[string]interface{}{"host-name": "p"}, ""},
		{"index with a missing key", `{{index $ "k"}}`, map[string]interface{}{}, `"k"`},
		{"root ref inside range counts", `{{range .items}}{{$.env}}{{end}}`, map[string]interface{}{"items": []string{"a"}, "env": "p"}, ""},
		{"root ref inside range, missing", `{{range .items}}{{$.env}}{{end}}`, map[string]interface{}{"items": []string{"a"}}, `"env"`},
		{"root ref inside with else", `{{with .a}}x{{else}}{{.b}}{{end}}`, map[string]interface{}{"a": "1"}, `"b"`},
		{"default once, bare once is required", `{{.t | default "1h"}} {{.t}}`, map[string]interface{}{}, `"t"`},
		{"variable read inside range body is not a variable", `{{range .items}}{{.name}}{{end}}`, map[string]interface{}{"items": []map[string]string{{"name": "a"}}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := RenderTemplate(tt.template, tt.vars)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("RenderTemplate() error = %v, want none", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("RenderTemplate() error = %v, want it to name %s", err, tt.wantErr)
			}
		})
	}
}
