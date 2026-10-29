package diagnostic

import (
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// The defect behind #476 was a 403 that reported "access denied" and threw the
// server's explanation away. Pin both halves: the reason reaches the user, and
// the error is typed so it exits with the permission code.
func TestSettingsForbiddenSurfacesServerReason(t *testing.T) {
	err := SettingsForbidden(SettingsDenial{
		Operation:  "create gcp_connection",
		Permission: "settings:objects:write",
		SchemaID:   "builtin:hyperscaler-authentication.connections.gcp",
		Body:       `{"error":{"code":403,"message":"No permission to write settings object."}}`,
	})

	if err.StatusCode != 403 {
		t.Errorf("StatusCode = %d, want 403", err.StatusCode)
	}
	if got := err.ExitCode(); got != client.ExitPermissionError {
		t.Errorf("ExitCode() = %d, want %d", got, client.ExitPermissionError)
	}

	msg := err.Error()
	if !strings.Contains(msg, "Failed to create gcp_connection (HTTP 403): No permission to write settings object.") {
		t.Errorf("unexpected message: %q", msg)
	}

	joined := strings.Join(err.Suggestions, "\n")
	if !strings.Contains(joined, "--check-scopes") {
		t.Errorf("suggestions do not point at the scope check: %q", joined)
	}
	if !strings.Contains(joined, `ALLOW settings:objects:write WHERE settings:schemaId = "builtin:hyperscaler-authentication.connections.gcp";`) {
		t.Errorf("suggestions do not include the IAM policy statement: %q", joined)
	}
	// Ownership is only a factor for an object that already exists.
	if strings.Contains(joined, "settings:objects:admin") {
		t.Errorf("create suggested the ownership bypass: %q", joined)
	}
}

// Settings objects carry an owner, so update/delete/get can be denied for a
// reason no policy on the schema fixes.
func TestSettingsForbiddenExistingObjectMentionsOwnership(t *testing.T) {
	err := SettingsForbidden(SettingsDenial{
		Operation:      `delete anomaly detector "vu9U3hXa3q0"`,
		Permission:     "settings:objects:write",
		SchemaID:       "builtin:davis.anomaly-detectors",
		Body:           "boom",
		ExistingObject: true,
	})

	joined := strings.Join(err.Suggestions, "\n")
	if !strings.Contains(joined, "settings:objects:admin") {
		t.Errorf("suggestions omit the ownership bypass: %q", joined)
	}
}

// A 403 with no body must still produce the scope-vs-policy guidance.
func TestSettingsForbiddenWithEmptyBody(t *testing.T) {
	err := SettingsForbidden(SettingsDenial{
		Operation:  "list aws_connections",
		Permission: "settings:objects:read",
		SchemaID:   "builtin:hyperscaler-authentication.connections.aws",
	})

	msg := err.Error()
	if !strings.Contains(msg, "Failed to list aws_connections (HTTP 403): access denied, and the server returned no reason") {
		t.Errorf("unexpected message: %q", msg)
	}
	if !strings.Contains(msg, "separate gates") {
		t.Errorf("guidance missing from message: %q", msg)
	}
}

// An unknown schema must not produce a policy statement with an empty
// condition — `WHERE settings:schemaId = ""` would match nothing if pasted.
func TestSettingsForbiddenWithoutSchemaIDOmitsCondition(t *testing.T) {
	err := SettingsForbidden(SettingsDenial{
		Operation:  "delete gcp_connection",
		Permission: "settings:objects:write",
		Body:       "boom",
	})

	joined := strings.Join(err.Suggestions, "\n")
	if !strings.Contains(joined, "Add: ALLOW settings:objects:write;") {
		t.Errorf("unconditioned policy statement missing: %q", joined)
	}
	if strings.Contains(joined, "settings:schemaId =") {
		t.Errorf("suggested a condition with no schema to condition on: %q", joined)
	}
}

// A body captured from a live Settings API 403 (writing to a schema that needs
// an internal permission). The platform's envelope must parse, and
// this particular denial is the scope gate rather than the policy gate — which
// is why "check the scope gate first" comes before the policy statement.
func TestSettingsForbiddenParsesLiveServerBody(t *testing.T) {
	const live = `{"error":{"code":403,"message":"write access to builtin:event-trigger.rule not allowed without settings:internal.event-trigger.rule:write token scope"}}`

	err := SettingsForbidden(SettingsDenial{
		Operation:  "create settings object",
		Permission: "settings:objects:write",
		SchemaID:   "builtin:event-trigger.rule",
		Body:       live,
	})

	if !strings.Contains(err.Error(), "write access to builtin:event-trigger.rule not allowed without settings:internal.event-trigger.rule:write token scope") {
		t.Errorf("live 403 body not surfaced verbatim: %q", err.Error())
	}
	if err.Suggestions[1] != "Check the scope gate first: re-run the same command with --check-scopes" {
		t.Errorf("scope check is not the first actionable suggestion: %q", err.Suggestions[1])
	}
}
