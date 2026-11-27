package diff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/pkg/vfs"
)

type Differ struct {
	options DiffOptions
	vfsEnv  vfs.Env
}

type DiffOptions struct {
	Format         DiffFormat
	IgnoreMetadata bool
	IgnoreOrder    bool
	ContextLines   int
	Colorize       bool
	Semantic       bool
}

type DiffFormat string

const (
	DiffFormatUnified    DiffFormat = "unified"
	DiffFormatSideBySide DiffFormat = "side-by-side"
	DiffFormatJSONPatch  DiffFormat = "json-patch"
	DiffFormatSemantic   DiffFormat = "semantic"
)

type DiffResult struct {
	HasChanges bool
	Changes    []Change
	Summary    DiffSummary
	Patch      string
	LeftLabel  string
	RightLabel string
}

type Change struct {
	Path string
	// Pointer is Path as unescaped reference tokens (map keys and array
	// indexes), the form a JSON patch (RFC 6901) is built from. Path is for
	// display and is ambiguous when a key contains '.', '[' or '/'.
	Pointer   []string
	Operation ChangeOperation
	OldValue  interface{}
	NewValue  interface{}
	Context   []string
}

type ChangeOperation string

const (
	ChangeOpAdd     ChangeOperation = "add"
	ChangeOpRemove  ChangeOperation = "remove"
	ChangeOpReplace ChangeOperation = "replace"
)

type DiffSummary struct {
	Added    int
	Removed  int
	Modified int
	Impact   ImpactLevel
}

type ImpactLevel string

const (
	ImpactLow      ImpactLevel = "low"
	ImpactMedium   ImpactLevel = "medium"
	ImpactHigh     ImpactLevel = "high"
	ImpactCritical ImpactLevel = "critical"
)

func NewDiffer(opts DiffOptions) *Differ {
	return &Differ{options: opts}
}

// WithVFS sets the filesystem and stdin CompareFiles reads from. The zero Env
// keeps the process's.
func (d *Differ) WithVFS(env vfs.Env) *Differ {
	d.vfsEnv = env
	return d
}

func (d *Differ) Compare(left, right interface{}, leftLabel, rightLabel string) (*DiffResult, error) {
	leftNorm := normalize(left, d.options.IgnoreMetadata, d.options.IgnoreOrder)
	rightNorm := normalize(right, d.options.IgnoreMetadata, d.options.IgnoreOrder)

	changes := computeDiff("", nil, leftNorm, rightNorm)

	result := &DiffResult{
		HasChanges: len(changes) > 0,
		Changes:    changes,
		Summary:    computeSummary(changes),
		LeftLabel:  leftLabel,
		RightLabel: rightLabel,
	}

	formatter := d.getFormatter()
	patch, err := formatter.Format(result)
	if err != nil {
		return nil, fmt.Errorf("failed to format diff: %w", err)
	}
	result.Patch = patch

	return result, nil
}

func (d *Differ) CompareFiles(leftPath, rightPath string) (*DiffResult, error) {
	left, err := d.parseFile(leftPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse left file: %w", err)
	}

	right, err := d.parseFile(rightPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse right file: %w", err)
	}

	return d.Compare(left, right, leftPath, rightPath)
}

// CompareData compares two already-read YAML or JSON documents. Callers that
// resolve their own inputs (e.g. stdin for `-f -`) use it instead of
// CompareFiles; the labels name each side in the output.
func (d *Differ) CompareData(leftData, rightData []byte, leftLabel, rightLabel string) (*DiffResult, error) {
	left, err := parseData(leftData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse left file: %w", err)
	}

	right, err := parseData(rightData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse right file: %w", err)
	}

	return d.Compare(left, right, leftLabel, rightLabel)
}

func (d *Differ) getFormatter() Formatter {
	if d.options.Semantic {
		return &SemanticFormatter{}
	}

	switch d.options.Format {
	case DiffFormatSideBySide:
		return &SideBySideFormatter{
			width:    120,
			colorize: d.options.Colorize,
		}
	case DiffFormatJSONPatch:
		return &JSONPatchFormatter{}
	case DiffFormatSemantic:
		return &SemanticFormatter{}
	default:
		return &UnifiedFormatter{
			contextLines: d.options.ContextLines,
			colorize:     d.options.Colorize,
		}
	}
}

func (d *Differ) parseFile(path string) (interface{}, error) {
	var data []byte
	var err error

	if path == "-" {
		return nil, fmt.Errorf("stdin not yet implemented")
	}

	data, err = d.vfsEnv.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return parseData(data)
}

func parseData(data []byte) (interface{}, error) {
	var result interface{}
	if err := yaml.Unmarshal(data, &result); err == nil {
		return result, nil
	}

	if err := json.Unmarshal(data, &result); err == nil {
		return result, nil
	}

	return nil, fmt.Errorf("file is not valid YAML or JSON")
}

func computeDiff(path string, ptr []string, left, right interface{}) []Change {
	var changes []Change

	if reflect.DeepEqual(left, right) {
		return changes
	}

	leftMap, leftIsMap := left.(map[string]interface{})
	rightMap, rightIsMap := right.(map[string]interface{})

	if leftIsMap && rightIsMap {
		allKeys := make(map[string]bool)
		for k := range leftMap {
			allKeys[k] = true
		}
		for k := range rightMap {
			allKeys[k] = true
		}

		for k := range allKeys {
			newPath := k
			newPtr := appendToken(ptr, k)
			if path != "" {
				newPath = path + "." + k
			}

			leftVal, leftExists := leftMap[k]
			rightVal, rightExists := rightMap[k]

			switch {
			case !leftExists:
				changes = append(changes, Change{
					Path:      newPath,
					Pointer:   newPtr,
					Operation: ChangeOpAdd,
					NewValue:  rightVal,
				})
			case !rightExists:
				changes = append(changes, Change{
					Path:      newPath,
					Pointer:   newPtr,
					Operation: ChangeOpRemove,
					OldValue:  leftVal,
				})
			default:
				changes = append(changes, computeDiff(newPath, newPtr, leftVal, rightVal)...)
			}
		}
		return changes
	}

	leftSlice, leftIsSlice := left.([]interface{})
	rightSlice, rightIsSlice := right.([]interface{})

	if leftIsSlice && rightIsSlice {
		maxLen := len(leftSlice)
		if len(rightSlice) > maxLen {
			maxLen = len(rightSlice)
		}

		for i := 0; i < maxLen; i++ {
			newPath := fmt.Sprintf("%s[%d]", path, i)
			newPtr := appendToken(ptr, strconv.Itoa(i))

			switch {
			case i >= len(leftSlice):
				changes = append(changes, Change{
					Path:      newPath,
					Pointer:   newPtr,
					Operation: ChangeOpAdd,
					NewValue:  rightSlice[i],
				})
			case i >= len(rightSlice):
				changes = append(changes, Change{
					Path:      newPath,
					Pointer:   newPtr,
					Operation: ChangeOpRemove,
					OldValue:  leftSlice[i],
				})
			default:
				changes = append(changes, computeDiff(newPath, newPtr, leftSlice[i], rightSlice[i])...)
			}
		}
		return changes
	}

	if !reflect.DeepEqual(left, right) {
		changes = append(changes, Change{
			Path:      path,
			Pointer:   ptr,
			Operation: ChangeOpReplace,
			OldValue:  left,
			NewValue:  right,
		})
	}

	return changes
}

// appendToken returns a copy of ptr with tok added, so sibling changes never
// share a backing array.
func appendToken(ptr []string, tok string) []string {
	out := make([]string, 0, len(ptr)+1)
	out = append(out, ptr...)
	return append(out, tok)
}

func computeSummary(changes []Change) DiffSummary {
	summary := DiffSummary{}

	for _, change := range changes {
		switch change.Operation {
		case ChangeOpAdd:
			summary.Added++
		case ChangeOpRemove:
			summary.Removed++
		case ChangeOpReplace:
			summary.Modified++
		}
	}

	summary.Impact = calculateImpact(summary)
	return summary
}

func calculateImpact(summary DiffSummary) ImpactLevel {
	total := summary.Added + summary.Removed + summary.Modified

	if total == 0 {
		return ImpactLow
	}

	if summary.Removed > 5 || total > 20 {
		return ImpactHigh
	}

	if summary.Removed > 0 || total > 10 {
		return ImpactMedium
	}

	return ImpactLow
}
