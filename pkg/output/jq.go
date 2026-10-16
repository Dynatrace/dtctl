package output

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/itchyny/gojq"
)

// JQShapeMismatchCode is the envelope error code for a well-formed --jq filter
// that addressed a key its input does not have. It is stable so an agent can
// branch on it rather than pattern-matching the message.
const JQShapeMismatchCode = "jq_shape_mismatch"

// JQError is a --jq failure that carries a stable envelope code and
// self-correcting suggestions, so agent mode can surface both verbatim instead
// of classifying a bare message.
type JQError struct {
	Code        string
	Message     string
	Suggestions []string
}

func (e *JQError) Error() string { return e.Message }

// IsStructuredOutputFormat reports whether a format can represent arbitrary
// JSON values emitted by jq. auto counts: it chooses its encoding after the
// filter has run.
func IsStructuredOutputFormat(format string) bool {
	if IsAutoFormat(format) {
		return true
	}
	switch format {
	case "json", "yaml", "yml", "toon":
		return true
	default:
		return false
	}
}

// NormalizeJQOutputFormat promotes non-structured formats to json when --jq is used.
func NormalizeJQOutputFormat(format string) string {
	if IsStructuredOutputFormat(format) {
		return format
	}
	return "json"
}

// ApplyJQ transforms input using the provided jq filter.
// If filter is empty, input is returned unchanged.
//
// Two outcomes have to stay distinguishable to a machine consumer, because jq
// spells both of them `null` and a caller that cannot tell them apart reads a
// wrong filter as a verified negative:
//
//   - The filter selected nothing — `empty`, or a `select(...)` no element
//     satisfies. That is a genuine empty selection, and it returns an empty
//     list: the natural zero of the multi-output case.
//   - The filter addressed a key the input does not have. That is a shape
//     mismatch, and it returns an error naming the keys the input actually
//     carries, so the mistake is self-correcting without a second round trip.
func ApplyJQ(filter string, input interface{}) (interface{}, error) {
	if filter == "" {
		return input, nil
	}

	query, err := gojq.Parse(filter)
	if err != nil {
		return nil, fmt.Errorf("invalid --jq filter: %w", err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("invalid --jq filter: %w", err)
	}

	generic, err := toGeneric(input)
	if err != nil {
		return nil, fmt.Errorf("failed to apply --jq filter: %w", err)
	}

	iter := code.Run(generic)
	results := make([]interface{}, 0, 1)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if runErr, ok := v.(error); ok {
			// "cannot iterate over: null" is the same missing-key mistake as a path resolving to null.
			if key := leadingJQKey(filter); key != "" && !hasJQKey(generic, key) {
				if _, isObj := generic.(map[string]interface{}); isObj {
					return nil, jqShapeMismatch(filter, generic)
				}
			}
			return nil, fmt.Errorf("failed to apply --jq filter: %w", runErr)
		}
		results = append(results, v)
	}

	switch len(results) {
	case 0:
		return []interface{}{}, nil
	case 1:
		if results[0] == nil {
			return nil, jqShapeMismatch(filter, generic)
		}
		return results[0], nil
	default:
		return results, nil
	}
}

// jqShapeMismatch builds the error for a filter that resolved to null. The
// message names the keys the input actually has, and when the filter reaches for
// `.result` on an input that has no such key it calls out the specific confusion
// behind that mistake: --jq runs on the result payload, never on the agent
// envelope that wraps it.
func jqShapeMismatch(filter string, input interface{}) error {
	suggestions := []string{
		"inspect the filter input first: --jq 'keys'",
		"drop --jq to see the whole object",
	}
	if strings.Contains(filter, ".result") && !hasJQKey(input, "result") {
		suggestions = append([]string{
			"--jq applies to the result payload, not to the --agent envelope that wraps it: drop the leading '.result' and filter on the keys above (on query: '.records', not '.result.records')",
		}, suggestions...)
	}
	return &JQError{
		Code: JQShapeMismatchCode,
		Message: fmt.Sprintf(
			"--jq filter %q resolved to null: %s. That is a shape mismatch, not an empty result — a filter that selects nothing returns []",
			filter, describeJQInput(input)),
		Suggestions: suggestions,
	}
}

var leadingKeyRe = regexp.MustCompile(`^\s*\.([A-Za-z_][A-Za-z0-9_]*)`)

// leadingJQKey is the first object key a filter addresses, or "".
func leadingJQKey(filter string) string {
	if m := leadingKeyRe.FindStringSubmatch(filter); m != nil {
		return m[1]
	}
	return ""
}

// hasJQKey reports whether input is an object carrying key.
func hasJQKey(input interface{}, key string) bool {
	obj, ok := input.(map[string]interface{})
	if !ok {
		return false
	}
	_, ok = obj[key]
	return ok
}

// describeJQInput renders the shape of a filter's input in one clause, naming
// an object's keys so a wrong path can be corrected from the error alone.
func describeJQInput(input interface{}) string {
	switch v := input.(type) {
	case map[string]interface{}:
		if len(v) == 0 {
			return "the filter input is an empty object"
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "the filter input is an object with keys [" + strings.Join(keys, ", ") + "]"
	case []interface{}:
		return fmt.Sprintf("the filter input is an array of %d element(s)", len(v))
	case nil:
		return "the filter input is null"
	case string:
		return "the filter input is a string"
	case bool:
		return "the filter input is a boolean"
	case float64:
		return "the filter input is a number"
	default:
		return fmt.Sprintf("the filter input is of type %T", v)
	}
}

// CompiledJQ is a jq program parsed and compiled once for repeated, per-record
// execution in a streaming filter (e.g. `dtctl inspect --jq` over a whole
// spilled file). ApplyJQ re-parses and re-compiles on every call, which is fine
// for a one-shot post-filter but quadratic when run over millions of records;
// CompiledJQ pays that cost once.
type CompiledJQ struct {
	code *gojq.Code
}

// CompileJQ parses and compiles a jq program for streaming, per-record use. The
// returned *CompiledJQ is safe to reuse across records (gojq does not mutate the
// compiled code). A parse/compile failure is reported as an invalid-filter error.
func CompileJQ(filter string) (*CompiledJQ, error) {
	query, err := gojq.Parse(filter)
	if err != nil {
		return nil, fmt.Errorf("invalid --jq filter: %w", err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("invalid --jq filter: %w", err)
	}
	return &CompiledJQ{code: code}, nil
}

// RunRecord runs the compiled program over a single record and returns every
// value it emits, in order. A record that yields no output — a `select(...)`
// that does not match, or `empty` — returns an empty slice: that is exactly how
// filtering drops a row. A runtime error from the program (e.g. a type error)
// is returned so the caller can surface it rather than silently skipping rows.
//
// The record is normalised through encoding/json first so values produced by any
// reader (Parquet/CSV typed columns, time values) run cleanly through gojq, which
// only accepts nil/bool/int/float64/string/[]any/map[string]any inputs.
func (c *CompiledJQ) RunRecord(rec map[string]interface{}) ([]interface{}, error) {
	generic, err := toGeneric(rec)
	if err != nil {
		return nil, fmt.Errorf("failed to apply --jq filter: %w", err)
	}
	iter := c.code.Run(generic)
	var out []interface{}
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if runErr, ok := v.(error); ok {
			return nil, fmt.Errorf("failed to apply --jq filter: %w", runErr)
		}
		out = append(out, v)
	}
	return out, nil
}
