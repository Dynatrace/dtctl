package segment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdksegment "github.com/Dynatrace/dtctl/sdk/api/segment"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// ErrNotFound is returned when a segment is not found (HTTP 404).
var ErrNotFound = sdksegment.ErrNotFound

// Handler handles Grail filter segment resources.
// It delegates HTTP calls to the SDK handler and wraps them with DQL↔AST conversion.
type Handler struct {
	sdk *sdksegment.Handler
}

// NewHandler creates a new segment handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdksegment.NewHandler(httpclient.Wrap(c.HTTP()))}
}

// FilterSegment is the read model for a Grail filter segment.
// It extends the SDK type with VariablesDisplay for CLI table output.
type FilterSegment struct {
	UID               string     `json:"uid" table:"UID"`
	Name              string     `json:"name" table:"NAME"`
	Description       string     `json:"description,omitempty" table:"DESCRIPTION,wide"`
	IsPublic          bool       `json:"isPublic" table:"PUBLIC"`
	VariablesDisplay  string     `json:"-" yaml:"-" table:"VARIABLES,wide"`
	Owner             string     `json:"owner,omitempty" table:"OWNER,wide"`
	Version           int        `json:"version,omitempty" table:"-"`
	IsReadyMade       bool       `json:"isReadyMade,omitempty" table:"-"`
	Includes          []Include  `json:"includes,omitempty" table:"-"`
	Variables         *Variables `json:"variables,omitempty" table:"-"`
	AllowedOperations []string   `json:"allowedOperations,omitempty" table:"-"`
}

// Include represents a single include rule within a segment.
type Include = sdksegment.Include

// Variables holds the variable configuration for a segment.
type Variables = sdksegment.Variables

// FilterSegmentList represents a list of filter segments.
// The filter-segments API does not support pagination; all segments are
// returned in a single response.
type FilterSegmentList struct {
	FilterSegments []FilterSegment `json:"filterSegments"`
	TotalCount     int             `json:"totalCount,omitempty"`
}

// fromSDKSegment converts an SDK FilterSegment to the CLI FilterSegment.
func fromSDKSegment(s *sdksegment.FilterSegment) FilterSegment {
	return FilterSegment{
		UID:               s.UID,
		Name:              s.Name,
		Description:       s.Description,
		IsPublic:          s.IsPublic,
		Owner:             s.Owner,
		Version:           s.Version,
		IsReadyMade:       s.IsReadyMade,
		Includes:          s.Includes,
		Variables:         s.Variables,
		AllowedOperations: s.AllowedOperations,
	}
}

// List lists all filter segments.
// Variables are requested so the wide table view can show whether each segment
// requires variable bindings.
func (h *Handler) List() (*FilterSegmentList, error) {
	sdkResult, err := h.sdk.List(context.Background(), "VARIABLES")
	if err != nil {
		return nil, err
	}

	result := &FilterSegmentList{
		TotalCount: len(sdkResult.FilterSegments),
	}
	result.FilterSegments = make([]FilterSegment, len(sdkResult.FilterSegments))
	for i, s := range sdkResult.FilterSegments {
		seg := fromSDKSegment(&s)
		// Convert AST filters to human-readable DQL for display
		convertIncludesForDisplay(&seg)
		seg.VariablesDisplay = variablesDisplay(seg.Variables)
		result.FilterSegments[i] = seg
	}

	return result, nil
}

// variablesDisplay returns a human-readable summary of a segment's variables.
func variablesDisplay(v *Variables) string {
	if v == nil || v.Type == "" {
		return ""
	}
	return "Yes"
}

// Get gets a specific filter segment by UID.
func (h *Handler) Get(uid string) (*FilterSegment, error) {
	sdkResult, err := h.sdk.Get(context.Background(), uid, "INCLUDES", "VARIABLES")
	if err != nil {
		return nil, err
	}

	seg := fromSDKSegment(sdkResult)
	// Convert AST filters to human-readable DQL for display
	convertIncludesForDisplay(&seg)

	return &seg, nil
}

// Create creates a new filter segment from raw JSON/YAML bytes.
func (h *Handler) Create(data []byte) (*FilterSegment, error) {
	// Convert DQL filters to AST for the API
	converted, err := convertIncludesForAPI(data)
	if err != nil {
		return nil, fmt.Errorf("failed to convert filter expressions: %w", err)
	}

	sdkResult, err := h.sdk.Create(context.Background(), converted)
	if err != nil {
		return nil, err
	}

	seg := fromSDKSegment(sdkResult)
	return &seg, nil
}

// Update updates an existing filter segment.
// The version parameter is required for optimistic locking.
func (h *Handler) Update(uid string, version int, data []byte) error {
	// Convert DQL filters to AST for the API
	converted, err := convertIncludesForAPI(data)
	if err != nil {
		return fmt.Errorf("failed to convert filter expressions: %w", err)
	}

	return h.sdk.Update(context.Background(), uid, version, converted)
}

// Delete deletes a filter segment by UID.
func (h *Handler) Delete(uid string) error {
	return h.sdk.Delete(context.Background(), uid)
}

// GetRaw gets a segment as pretty-printed JSON bytes (for edit command).
// Note: the returned JSON contains DQL filter expressions (not raw API AST)
// because it delegates to Get, which converts AST filters to DQL.
func (h *Handler) GetRaw(uid string) ([]byte, error) {
	seg, err := h.Get(uid)
	if err != nil {
		return nil, err
	}

	return json.MarshalIndent(seg, "", "  ")
}

// IsNotFound returns true if the error indicates a segment was not found (404).
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// ---------------------------------------------------------------------------
// Filter format conversion (DQL ↔ AST)
// ---------------------------------------------------------------------------

// convertIncludesForAPI converts include filters from DQL to AST before
// sending to the API. It operates on raw JSON bytes so it works with both
// create and update payloads. If a filter is already JSON AST (starts with
// '{'), it is passed through unchanged.
//
// The function preserves the original JSON field order by splicing the
// converted includes array back into the original bytes rather than
// re-marshaling the entire payload through a map (which would alphabetize
// keys).
func convertIncludesForAPI(data []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	includesRaw, ok := payload["includes"]
	if !ok {
		return data, nil // no includes field — pass through unchanged
	}

	var includes []map[string]json.RawMessage
	if err := json.Unmarshal(includesRaw, &includes); err != nil {
		return nil, fmt.Errorf("failed to parse includes: %w", err)
	}

	changed := false
	for i, inc := range includes {
		filterRaw, ok := inc["filter"]
		if !ok {
			continue
		}
		var filter string
		if err := json.Unmarshal(filterRaw, &filter); err != nil {
			continue // not a string — skip
		}

		ast, err := FilterToAST(filter)
		if err != nil {
			return nil, fmt.Errorf("include[%d]: %w", i, err)
		}
		if ast != filter {
			newFilterJSON, err := json.Marshal(ast)
			if err != nil {
				return nil, fmt.Errorf("include[%d]: failed to marshal AST: %w", i, err)
			}
			inc["filter"] = newFilterJSON
			changed = true
		}
	}

	if !changed {
		return data, nil
	}

	// Re-marshal only the includes array, then splice it into the original
	// JSON to preserve field order of the top-level object.
	newIncludes, err := json.Marshal(includes)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal includes: %w", err)
	}

	return replaceJSONField(data, "includes", newIncludes)
}

// replaceJSONField replaces the value of a top-level JSON object field while
// preserving the original field order and formatting.
func replaceJSONField(data []byte, field string, newValue json.RawMessage) ([]byte, error) {
	keyPattern := []byte(`"` + field + `"`)

	idx := bytes.Index(data, keyPattern)
	if idx < 0 {
		return nil, fmt.Errorf("field %q not found in JSON", field)
	}

	valueStart := idx + len(keyPattern)
	for valueStart < len(data) && (data[valueStart] == ' ' || data[valueStart] == '\t' || data[valueStart] == '\n' || data[valueStart] == '\r' || data[valueStart] == ':') {
		valueStart++
	}
	if valueStart >= len(data) {
		return nil, fmt.Errorf("field %q: unexpected end of JSON after key", field)
	}

	valueEnd, err := findJSONValueEnd(data, valueStart)
	if err != nil {
		return nil, fmt.Errorf("field %q: %w", field, err)
	}

	var buf bytes.Buffer
	buf.Grow(valueStart + len(newValue) + (len(data) - valueEnd))
	buf.Write(data[:valueStart])
	buf.Write(newValue)
	buf.Write(data[valueEnd:])
	return buf.Bytes(), nil
}

// findJSONValueEnd returns the byte offset just past the JSON value starting
// at data[start].
func findJSONValueEnd(data []byte, start int) (int, error) {
	if start >= len(data) {
		return 0, fmt.Errorf("unexpected end of JSON")
	}

	switch data[start] {
	case '{', '[':
		return findJSONBalancedEnd(data, start)
	case '"':
		return findJSONStringEnd(data, start)
	default:
		i := start
		for i < len(data) {
			switch data[i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return i, nil
			}
			i++
		}
		return i, nil
	}
}

func findJSONBalancedEnd(data []byte, start int) (int, error) {
	open := data[start]
	var close byte
	if open == '{' {
		close = '}'
	} else {
		close = ']'
	}

	depth := 1
	i := start + 1
	for i < len(data) && depth > 0 {
		switch data[i] {
		case '"':
			end, err := findJSONStringEnd(data, i)
			if err != nil {
				return 0, err
			}
			i = end
			continue
		case open:
			depth++
		case close:
			depth--
		}
		i++
	}
	if depth != 0 {
		return 0, fmt.Errorf("unbalanced JSON starting at offset %d", start)
	}
	return i, nil
}

func findJSONStringEnd(data []byte, start int) (int, error) {
	i := start + 1
	for i < len(data) {
		if data[i] == '\\' {
			i += 2
			continue
		}
		if data[i] == '"' {
			return i + 1, nil
		}
		i++
	}
	return 0, fmt.Errorf("unterminated JSON string starting at offset %d", start)
}

// convertIncludesForDisplay converts include filters from AST to
// human-readable DQL after receiving from the API.
func convertIncludesForDisplay(seg *FilterSegment) {
	for i := range seg.Includes {
		dql, err := FilterFromAST(seg.Includes[i].Filter)
		if err != nil {
			continue // leave as-is if conversion fails
		}
		seg.Includes[i].Filter = dql
	}
}
