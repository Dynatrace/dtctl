package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type Formatter interface {
	Format(result *DiffResult) (string, error)
}

type UnifiedFormatter struct {
	contextLines int
	colorize     bool
}

func (f *UnifiedFormatter) Format(result *DiffResult) (string, error) {
	if !result.HasChanges {
		return "", nil
	}

	var buf bytes.Buffer

	buf.WriteString(fmt.Sprintf("--- %s\n", result.LeftLabel))
	buf.WriteString(fmt.Sprintf("+++ %s\n", result.RightLabel))

	for _, change := range result.Changes {
		f.writeChange(&buf, change)
	}

	return buf.String(), nil
}

func (f *UnifiedFormatter) writeChange(buf *bytes.Buffer, change Change) {
	switch change.Operation {
	case ChangeOpAdd:
		buf.WriteString(fmt.Sprintf("+ %s: %v\n", change.Path, formatValue(change.NewValue)))
	case ChangeOpRemove:
		buf.WriteString(fmt.Sprintf("- %s: %v\n", change.Path, formatValue(change.OldValue)))
	case ChangeOpReplace:
		buf.WriteString(fmt.Sprintf("- %s: %v\n", change.Path, formatValue(change.OldValue)))
		buf.WriteString(fmt.Sprintf("+ %s: %v\n", change.Path, formatValue(change.NewValue)))
	}
}

type SideBySideFormatter struct {
	width    int
	colorize bool
}

func (f *SideBySideFormatter) Format(result *DiffResult) (string, error) {
	if !result.HasChanges {
		return "", nil
	}

	var buf bytes.Buffer
	colWidth := f.width / 2

	buf.WriteString(fmt.Sprintf("%-*s | %s\n", colWidth-3, result.LeftLabel, result.RightLabel))
	buf.WriteString(strings.Repeat("-", colWidth-1))
	buf.WriteString("|")
	buf.WriteString(strings.Repeat("-", colWidth-1))
	buf.WriteString("\n")

	for _, change := range result.Changes {
		f.writeChangeSideBySide(&buf, change, colWidth)
	}

	return buf.String(), nil
}

func (f *SideBySideFormatter) writeChangeSideBySide(buf *bytes.Buffer, change Change, colWidth int) {
	switch change.Operation {
	case ChangeOpAdd:
		left := ""
		right := fmt.Sprintf("%s: %v", change.Path, formatValue(change.NewValue))
		buf.WriteString(fmt.Sprintf("%-*s | %s\n", colWidth-3, truncate(left, colWidth-3), truncate(right, colWidth-3)))
	case ChangeOpRemove:
		left := fmt.Sprintf("%s: %v", change.Path, formatValue(change.OldValue))
		right := ""
		buf.WriteString(fmt.Sprintf("%-*s | %s\n", colWidth-3, truncate(left, colWidth-3), truncate(right, colWidth-3)))
	case ChangeOpReplace:
		left := fmt.Sprintf("%s: %v", change.Path, formatValue(change.OldValue))
		right := fmt.Sprintf("%s: %v", change.Path, formatValue(change.NewValue))
		buf.WriteString(fmt.Sprintf("%-*s | %s\n", colWidth-3, truncate(left, colWidth-3), truncate(right, colWidth-3)))
	}
}

type JSONPatchFormatter struct{}

func (f *JSONPatchFormatter) Format(result *DiffResult) (string, error) {
	if !result.HasChanges {
		return "[]", nil
	}

	patch := []map[string]interface{}{}

	for _, change := range result.Changes {
		op := map[string]interface{}{
			"op":   string(change.Operation),
			"path": jsonPointer(change),
		}

		if change.Operation != ChangeOpRemove {
			op["value"] = change.NewValue
		}

		patch = append(patch, op)
	}

	data, err := json.MarshalIndent(patch, "", "  ")
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// jsonPointer renders a change's location as an RFC 6901 JSON Pointer: each
// token is escaped ("~" to "~0", "/" to "~1") and prefixed with "/". A change
// built without Pointer tokens falls back to splitting its display Path.
func jsonPointer(change Change) string {
	tokens := change.Pointer
	if tokens == nil && change.Path != "" {
		path := arrayIndex.ReplaceAllString(change.Path, ".$1")
		tokens = strings.Split(path, ".")
	}
	var b strings.Builder
	for _, tok := range tokens {
		tok = strings.ReplaceAll(tok, "~", "~0")
		tok = strings.ReplaceAll(tok, "/", "~1")
		b.WriteString("/" + tok)
	}
	return b.String()
}

var arrayIndex = regexp.MustCompile(`\[(\d+)\]`)

type SemanticFormatter struct{}

func (f *SemanticFormatter) Format(result *DiffResult) (string, error) {
	if !result.HasChanges {
		return "No changes detected\n", nil
	}

	var buf bytes.Buffer

	buf.WriteString(fmt.Sprintf("Comparing: %s vs %s\n\n", result.LeftLabel, result.RightLabel))
	buf.WriteString("Changes:\n")

	for _, change := range result.Changes {
		switch change.Operation {
		case ChangeOpAdd:
			buf.WriteString(fmt.Sprintf("  + %s: %v\n", change.Path, formatValue(change.NewValue)))
		case ChangeOpRemove:
			buf.WriteString(fmt.Sprintf("  - %s: %v\n", change.Path, formatValue(change.OldValue)))
		case ChangeOpReplace:
			buf.WriteString(fmt.Sprintf("  ~ %s: %v → %v\n", change.Path, formatValue(change.OldValue), formatValue(change.NewValue)))
		}
	}

	buf.WriteString(fmt.Sprintf("\nSummary: %d modified, %d added, %d removed\n",
		result.Summary.Modified, result.Summary.Added, result.Summary.Removed))
	buf.WriteString(fmt.Sprintf("Impact: %s\n", result.Summary.Impact))

	return buf.String(), nil
}

func formatValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%q", val)
	case map[string]interface{}, []interface{}:
		data, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(data)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
