package template

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
)

// ParseSetFlags parses --set flags into a map
// Format: key=value
func ParseSetFlags(setFlags []string) (map[string]interface{}, error) {
	vars := make(map[string]interface{})

	for _, flag := range setFlags {
		parts := strings.SplitN(flag, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid --set format: %q (expected key=value)", flag)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			return nil, fmt.Errorf("empty key in --set flag: %q", flag)
		}

		vars[key] = value
	}

	return vars, nil
}

// RenderTemplate renders a template string with the provided variables
// Uses Go's text/template syntax with support for default values.
//
// Input the template cannot use is an error, not output: a variable the
// template reads that has no value and no default (it would render as
// "<no value>"), and a variable that is given but never read (a typo in
// --set). Both errors name the variable.
func RenderTemplate(templateStr string, vars map[string]interface{}) (string, error) {
	// Create custom function map with 'default' function
	funcMap := template.FuncMap{
		"default": func(defaultVal interface{}, value ...interface{}) interface{} {
			// If no value provided or value is empty/zero, return default
			if len(value) == 0 {
				return defaultVal
			}
			v := value[0]
			if v == nil || v == "" {
				return defaultVal
			}
			return v
		},
	}

	// Parse the template with missingkey=zero (so variables evaluate to zero value)
	tmpl, err := template.New("query").Funcs(funcMap).Option("missingkey=zero").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	if err := checkVariables(tmpl, vars); err != nil {
		return "", err
	}

	// Execute the template
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// ContainsTemplate checks if a string contains template syntax
func ContainsTemplate(str string) bool {
	return strings.Contains(str, "{{") && strings.Contains(str, "}}")
}

// ValidateTemplate checks if a template is valid and returns required variables
// This is a best-effort function that may not catch all cases
func ValidateTemplate(templateStr string) ([]string, error) {
	// Create custom function map with 'default' function
	funcMap := template.FuncMap{
		"default": func(defaultVal interface{}, value ...interface{}) interface{} {
			// If no value provided or value is empty/zero, return default
			if len(value) == 0 {
				return defaultVal
			}
			v := value[0]
			if v == nil || v == "" {
				return defaultVal
			}
			return v
		},
	}

	// Try to parse the template to validate syntax
	_, err := template.New("validate").Funcs(funcMap).Parse(templateStr)
	if err != nil {
		return nil, fmt.Errorf("invalid template syntax: %w", err)
	}

	// Extract variable names from template
	// Note: This is a simplified approach - Go's template package doesn't
	// provide a built-in way to extract all variable names
	var vars []string
	seen := make(map[string]bool)

	// Simple regex-like extraction of {{.VarName}} patterns
	inTemplate := false
	var currentVar strings.Builder

	for i := 0; i < len(templateStr); i++ {
		if i < len(templateStr)-1 && templateStr[i] == '{' && templateStr[i+1] == '{' {
			inTemplate = true
			currentVar.Reset()
			i++ // Skip next {
			continue
		}

		if inTemplate && i < len(templateStr)-1 && templateStr[i] == '}' && templateStr[i+1] == '}' {
			inTemplate = false
			varName := strings.TrimSpace(currentVar.String())

			// Extract variable name (format: {{.VarName}} or {{.VarName | default "value"}})
			if strings.HasPrefix(varName, ".") {
				varName = strings.TrimPrefix(varName, ".")
				// Remove any function calls like "| default"
				if idx := strings.Index(varName, " "); idx != -1 {
					varName = varName[:idx]
				}
				if idx := strings.Index(varName, "|"); idx != -1 {
					varName = strings.TrimSpace(varName[:idx])
				}

				if varName != "" && !seen[varName] {
					vars = append(vars, varName)
					seen[varName] = true
				}
			}

			i++ // Skip next }
			continue
		}

		if inTemplate {
			currentVar.WriteByte(templateStr[i])
		}
	}

	return vars, nil
}

// VariableError reports a variable the template cannot use: one it reads
// without a value or default (Missing), or one that was given but never read
// (Unused). The CLI maps it to a usage error.
type VariableError struct {
	Missing []string
	Unused  []string
	msg     string
}

func (e *VariableError) Error() string { return e.msg }

// checkVariables compares the variables the template reads with the ones given.
func checkVariables(tmpl *template.Template, vars map[string]interface{}) error {
	u := &usage{used: map[string]bool{}, required: map[string]bool{}}
	for _, t := range tmpl.Templates() {
		if t.Tree != nil && t.Tree.Root != nil {
			u.walk(t.Tree.Root, true)
		}
	}

	var missing, unused []string
	for name := range u.required {
		if _, ok := vars[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range vars {
		if !u.used[name] {
			unused = append(unused, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unused)

	if len(missing) > 0 {
		return &VariableError{Missing: missing, msg: fmt.Sprintf("template variable %s has no value and no default; "+
			"pass --set %s=<value> or give it a default ({{.%s | default \"...\"}})",
			quoteNames(missing), missing[0], missing[0])}
	}
	if len(unused) > 0 {
		return &VariableError{Unused: unused, msg: fmt.Sprintf("--set variable %s is not used by the template (check the spelling)", quoteNames(unused))}
	}
	return nil
}

func quoteNames(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// usage records which top-level variables a template reads. A variable is
// required when at least one read has no default.
type usage struct {
	used     map[string]bool
	required map[string]bool
}

// walk visits n. rooted is true while dot is the root map; inside range and
// with bodies dot is rebound, so only $-rooted reads count there.
func (u *usage) walk(n parse.Node, rooted bool) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			u.walk(c, rooted)
		}
	case *parse.ActionNode:
		u.walk(n.Pipe, rooted)
	case *parse.IfNode:
		u.walk(n.Pipe, rooted)
		u.walk(n.List, rooted)
		u.walk(n.ElseList, rooted)
	case *parse.RangeNode:
		u.walk(n.Pipe, rooted)
		u.walk(n.List, false)
		u.walk(n.ElseList, rooted)
	case *parse.WithNode:
		u.walk(n.Pipe, rooted)
		u.walk(n.List, false)
		u.walk(n.ElseList, rooted)
	case *parse.TemplateNode:
		u.walk(n.Pipe, rooted)
	case *parse.PipeNode:
		if n == nil {
			return
		}
		hasDefault := false
		var names []string
		var args func(p *parse.PipeNode)
		args = func(p *parse.PipeNode) {
			for _, cmd := range p.Cmds {
				if len(cmd.Args) >= 3 {
					// index . "key" / index $ "key": a read of the root map.
					if id, ok := cmd.Args[0].(*parse.IdentifierNode); ok && id.Ident == "index" {
						if key, ok := cmd.Args[2].(*parse.StringNode); ok && isRoot(cmd.Args[1], rooted) {
							names = append(names, key.Text)
						}
					}
				}
				for _, arg := range cmd.Args {
					switch a := arg.(type) {
					case *parse.IdentifierNode:
						if a.Ident == "default" {
							hasDefault = true
						}
					case *parse.FieldNode:
						if rooted && len(a.Ident) > 0 {
							names = append(names, a.Ident[0])
						}
					case *parse.VariableNode:
						if len(a.Ident) > 1 && a.Ident[0] == "$" {
							names = append(names, a.Ident[1])
						}
					case *parse.ChainNode:
						if f, ok := a.Node.(*parse.FieldNode); ok && rooted && len(f.Ident) > 0 {
							names = append(names, f.Ident[0])
						}
					case *parse.PipeNode:
						args(a)
					}
				}
			}
		}
		args(n)
		for _, name := range names {
			u.used[name] = true
			if !hasDefault {
				u.required[name] = true
			}
		}
	}
}

// isRoot reports whether n is the root data: dot while rooted, or $.
func isRoot(n parse.Node, rooted bool) bool {
	switch n := n.(type) {
	case *parse.DotNode:
		return rooted
	case *parse.VariableNode:
		return len(n.Ident) == 1 && n.Ident[0] == "$"
	}
	return false
}
