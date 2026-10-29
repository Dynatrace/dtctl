package diagnostic

import "fmt"

// SettingsDenial describes a 403 from the Settings 2.0 objects API
// (/platform/classic/environment-api/v2/settings/objects).
type SettingsDenial struct {
	// Operation is the user-facing action, rendered as "Failed to <Operation>",
	// e.g. `create gcp_connection` or `update anomaly detector "vu9U…"`.
	Operation string
	// Permission is the OAuth scope and IAM permission name the operation needs,
	// i.e. settings:objects:read or settings:objects:write.
	Permission string
	// SchemaID is the settings schema the object belongs to. It is the condition
	// an IAM policy statement is usually written against, so it goes into the
	// suggested statement verbatim. May be empty when the operation targets an
	// existing object whose schema is not known at the call site.
	SchemaID string
	// Body is the raw response body. Never discard it — the server's reason for
	// the denial is in here, and hiding it is what turned issue #344 into a
	// support case.
	Body string
	// ExistingObject is true for operations on an object that already exists
	// (get, update, delete). Settings objects carry an owner, so those can be
	// denied for a reason no policy on the schema fixes.
	ExistingObject bool
}

// SettingsForbidden builds the 403 error for a Settings 2.0 object operation.
//
// It is the Settings-API counterpart of the Grail-file 403 handling added for
// github.com/Dynatrace/dtctl#344, and exists for the same reason: the OAuth
// scope in the token and the IAM permission of the same name are two
// independent gates, so "access denied" without the server's explanation reads
// like a token problem even when the token is fine. Per the settings service
// definition, settings:objects:read and settings:objects:write are IAM
// permissions conditioned on settings:schemaId, settings:schemaGroup and
// settings:scope among others — which is why access to one schema says nothing
// about access to another.
//
// This lives in diagnostic rather than next to a handler because four resource
// packages (aws/azure/gcp connections, anomaly detectors) share the Settings
// API and would otherwise each carry a copy. The Grail-file equivalent stays in
// pkg/resources/lookup, which is its only caller.
func SettingsForbidden(d SettingsDenial) *Error {
	// Operation carries the "what", Message the server's "why" — Error renders
	// them as "Failed to <operation> (HTTP 403): <message>", so repeating the
	// operation in both would read twice.
	msg := ServerErrorMessage(d.Body)
	if msg == "" {
		msg = "access denied, and the server returned no reason"
	}

	policyStatement := fmt.Sprintf("ALLOW %s", d.Permission)
	if d.SchemaID != "" {
		policyStatement += fmt.Sprintf(" WHERE settings:schemaId = %q", d.SchemaID)
	}
	policyStatement += ";"

	suggestions := []string{
		fmt.Sprintf("The OAuth scope and the IAM permission are separate gates, both named %q — a granted scope alone is not enough", d.Permission),
		"Check the scope gate first: re-run the same command with --check-scopes",
		fmt.Sprintf("If that reports \"ok\", or \"unknown\" because the token is not introspectable, the tenant's IAM policy is missing the permission. Add: %s", policyStatement),
		"Policy permissions can be conditioned on settings:schemaId, settings:schemaGroup and settings:scope, so access to one schema or scope does not imply access to another",
	}
	if d.ExistingObject {
		suggestions = append(suggestions,
			"Settings objects have an owner. Acting on an object created by someone else can additionally require settings:objects:admin")
	}

	return &Error{
		Operation:   d.Operation,
		StatusCode:  403,
		Message:     msg,
		Suggestions: suggestions,
	}
}
