package anomalydetector

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// actorUUIDPattern mirrors the PATTERN constraint the schema puts on
// executionSettings.actor.
var actorUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidationUnavailableError reports that server-side validation could not be
// performed at all (offline, expired token, missing scope) — as opposed to the
// payload being rejected. Dry-run callers downgrade it to a warning: the
// payload passed local validation but was never checked against the schema.
type ValidationUnavailableError struct {
	Err error
}

func (e *ValidationUnavailableError) Error() string {
	return fmt.Sprintf("server-side validation unavailable: %v", e.Err)
}

func (e *ValidationUnavailableError) Unwrap() error { return e.Err }

// missing reports whether key is absent or explicitly null. YAML like
// "executionSettings:" with no body unmarshals to a present-but-nil entry,
// which the Settings API rejects the same way as an omitted field.
func missing(m map[string]any, key string) bool {
	v, ok := m[key]
	return !ok || v == nil
}

// normalizeValue fills in the fields that builtin:davis.anomaly-detectors
// declares non-nullable. The Settings API rejects a payload that omits any of
// them with "Must not be null" even though the schema carries a default, so a
// hand-written detector that leaves out "description" or "executionSettings"
// fails with an error pointing at a field the author never wrote (issue #369).
// Applying the schema defaults client-side keeps minimal definitions working.
//
// defaultActor, when non-empty, is used for executionSettings.actor if the
// input does not carry one — the identity the detector's queries run as.
func normalizeValue(value map[string]any, defaultActor string) {
	if value == nil {
		return
	}

	if missing(value, "description") {
		value["description"] = ""
	}
	if missing(value, "enabled") {
		value["enabled"] = true
	}
	if source, _ := value["source"].(string); source == "" {
		value["source"] = defaultSource
	}

	if analyzer, ok := value["analyzer"].(map[string]any); ok && missing(analyzer, "input") {
		analyzer["input"] = []map[string]any{}
	}

	if missing(value, "eventTemplate") {
		value["eventTemplate"] = map[string]any{"properties": []map[string]any{}}
	} else if et, ok := value["eventTemplate"].(map[string]any); ok && missing(et, "properties") {
		et["properties"] = []map[string]any{}
	}

	if missing(value, "executionSettings") {
		value["executionSettings"] = map[string]any{}
	}
	if es, ok := value["executionSettings"].(map[string]any); ok && defaultActor != "" {
		if actor, _ := es["actor"].(string); actor == "" {
			es["actor"] = defaultActor
		}
	}
}

// validateValue reports the problems that can be found without calling the API.
// It covers the constraints the schema states unconditionally (title length,
// analyzer name, actor format); everything else — including whether the
// environment insists on executionSettings.actor, which differs between
// environments — is left to Handler.ValidateCreate / ValidateUpdate.
func validateValue(value map[string]any) error {
	var problems []string

	title, _ := value["title"].(string)
	switch {
	case strings.TrimSpace(title) == "":
		problems = append(problems, "title is required")
	case len(title) < 2 || len(title) > 500:
		problems = append(problems, fmt.Sprintf("title must be 2 to 500 characters long (got %d)", len(title)))
	}

	analyzer, ok := value["analyzer"].(map[string]any)
	switch {
	case !ok:
		problems = append(problems, "analyzer is required and must be an object")
	default:
		if name, _ := analyzer["name"].(string); name == "" {
			problems = append(problems, "analyzer.name is required (fully qualified analyzer name, "+
				"e.g. dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer)")
		}
	}

	if es, ok := value["executionSettings"].(map[string]any); ok {
		if _, present := es["actor"]; present {
			actor, _ := es["actor"].(string)
			if !actorUUIDPattern.MatchString(actor) {
				problems = append(problems, fmt.Sprintf(
					"executionSettings.actor must be a UUID (got %q); it is the user or service user "+
						"the detector's queries run as — see 'dtctl auth whoami --id-only'", actor))
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid anomaly detector:\n  - %s", strings.Join(problems, "\n  - "))
}

// settingsErrorBody is the Settings API error envelope. POST returns one entry
// per submitted object, PUT returns a bare object.
type settingsErrorBody struct {
	Code  int `json:"code"`
	Error *struct {
		Code                 int    `json:"code"`
		Message              string `json:"message"`
		ConstraintViolations []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"constraintViolations"`
	} `json:"error"`
}

// describeSettingsError condenses a Settings API validation response into a
// field-oriented message. The raw body echoes the entire rejected payload back,
// which buries the one or two lines that say what is actually wrong (issue
// #369). Returns "" when the body has no recognizable violations, so callers
// can fall back to printing it verbatim.
func describeSettingsError(body []byte) string {
	var entries []settingsErrorBody
	if err := json.Unmarshal(body, &entries); err != nil {
		var single settingsErrorBody
		if err := json.Unmarshal(body, &single); err != nil {
			return ""
		}
		entries = []settingsErrorBody{single}
	}

	var problems []string
	var message string
	for _, entry := range entries {
		if entry.Error == nil {
			continue
		}
		if message == "" {
			message = entry.Error.Message
		}
		for _, violation := range entry.Error.ConstraintViolations {
			problems = append(problems, fmt.Sprintf("%s: %s", trimViolationPath(violation.Path), violation.Message))
		}
	}

	switch {
	case len(problems) > 0:
		detail := fmt.Sprintf("invalid anomaly detector:\n  - %s", strings.Join(problems, "\n  - "))
		if hint := violationHint(problems); hint != "" {
			detail += "\n" + hint
		}
		return detail
	case message != "":
		return fmt.Sprintf("invalid anomaly detector: %s", message)
	default:
		return ""
	}
}

// trimViolationPath strips the "<schemaId>/<index>/" prefix the Settings API
// puts in front of violation paths, leaving the field path the author wrote.
func trimViolationPath(path string) string {
	rest, ok := strings.CutPrefix(path, SchemaID+"/")
	if !ok || rest == "" {
		return path
	}
	// The remainder is "<index>/<field path>" for multi-object schemas.
	if _, field, found := strings.Cut(rest, "/"); found {
		return field
	}
	return rest
}

// violationHint returns extra guidance for violations that are hard to act on.
func violationHint(problems []string) string {
	for _, problem := range problems {
		if strings.HasPrefix(problem, "executionSettings") {
			return "  hint: executionSettings.actor is the UUID of the user or service user the detector's " +
				"queries run as. dtctl fills it with the authenticated identity when the definition omits it; " +
				"set it explicitly (see 'dtctl auth whoami --id-only') if that identity cannot be resolved."
		}
	}
	return ""
}

// PrepareCreateBody converts input data (flattened or raw Settings format) into
// the exact Settings API create body dtctl would POST, applying schema defaults
// and local validation. Exposed so dry-run can show and check the real payload
// instead of the raw input.
func (h *Handler) PrepareCreateBody(data []byte) (map[string]any, error) {
	return toAPIFormat(data, h.defaultActor)
}

// ValidateCreate asks the Settings API to validate a create payload without
// persisting it (POST ?validateOnly=true), so --dry-run reports the same
// verdict the real call would (issue #369).
func (h *Handler) ValidateCreate(data []byte) error {
	body, err := h.PrepareCreateBody(data)
	if err != nil {
		return err
	}

	resp, err := h.client.HTTP().R().
		SetQueryParam("validateOnly", "true").
		SetBody([]map[string]any{body}).
		Post(SettingsAPI)
	if err != nil {
		return &ValidationUnavailableError{Err: err}
	}
	return interpretValidationResponse(resp.StatusCode(), resp.Body())
}

// ValidateUpdate validates an update payload against an existing object without
// persisting it (PUT ?validateOnly=true).
func (h *Handler) ValidateUpdate(objectID string, data []byte) error {
	existing, err := h.Get(objectID)
	if err != nil {
		return &ValidationUnavailableError{Err: err}
	}

	value, err := h.prepareUpdateValue(data, existing)
	if err != nil {
		return err
	}

	resp, err := h.client.HTTP().R().
		SetQueryParam("validateOnly", "true").
		SetHeader("If-Match", existing.SchemaVersion).
		SetBody(map[string]any{"value": value}).
		Put(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(objectID)))
	if err != nil {
		return &ValidationUnavailableError{Err: err}
	}
	return interpretValidationResponse(resp.StatusCode(), resp.Body())
}

// interpretValidationResponse maps a validateOnly response to an error.
// A 400 means the payload is invalid; any other failure means validation did
// not happen and is reported as unavailable rather than as a rejection.
func interpretValidationResponse(status int, body []byte) error {
	switch {
	case status < 300:
		return nil
	case status == 400:
		if detail := describeSettingsError(body); detail != "" {
			return fmt.Errorf("%s", detail)
		}
		return fmt.Errorf("invalid anomaly detector: %s", body)
	default:
		return &ValidationUnavailableError{Err: fmt.Errorf("status %d: %s", status, body)}
	}
}
