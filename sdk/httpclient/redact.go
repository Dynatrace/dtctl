package httpclient

import (
	"encoding/json"
	"strings"
)

// sensitiveBodyFields lists JSON keys (case-insensitive) whose values are
// secret material and must never appear in debug output: credential vault
// payloads carry certificates, passwords and tokens in the request body.
var sensitiveBodyFields = map[string]bool{
	"password":     true,
	"token":        true,
	"certificate":  true,
	"secret":       true,
	"clientsecret": true,
	"privatekey":   true,
	"accesstoken":  true,
	"refreshtoken": true,
}

// RedactBodyForDebug returns body with the values of sensitive JSON fields
// replaced by "[REDACTED]", at any depth. A body that is not JSON is returned
// unchanged; a JSON body that holds a sensitive field is re-encoded.
func RedactBodyForDebug(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return body
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return body
	}
	if !redactValue(v) {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "[REDACTED: body could not be re-encoded]"
	}
	return string(out)
}

func redactValue(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if sensitiveBodyFields[strings.ToLower(k)] {
				if s, ok := child.(string); ok && s == "" {
					continue
				}
				t[k] = "[REDACTED]"
				changed = true
				continue
			}
			if redactValue(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range t {
			if redactValue(child) {
				changed = true
			}
		}
	}
	return changed
}
