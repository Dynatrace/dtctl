package lookup

import (
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/diagnostic"
)

// forbiddenError builds the 403 error for a Resource Store file operation.
//
// A Grail file operation passes two independent gates that share a name: the
// OAuth scope in the token (e.g. storage:files:delete) and the IAM policy
// permission of the same name, which is additionally conditioned on
// storage:file-path. A token minted at --safety-level dangerously-unrestricted
// carries every storage:files:* scope, so a 403 here almost always means the
// policy gate, not the scope gate — but the old message ("access denied to
// delete file") read like a token problem and dropped the response body that
// said otherwise, which is how github.com/Dynatrace/dtctl#344 reached
// support as a missing-scope bug.
//
// verb is the user-facing operation ("delete"/"write"), permission the scope
// and IAM permission name, and cmdHint the command that verifies the scope gate.
func forbiddenError(verb, permission, cmdHint, path, body string) error {
	// Operation carries the "what", Message the server's "why" — diagnostic.Error
	// renders them as "Failed to <operation> (HTTP 403): <message>", so repeating
	// the path or verb in both would read twice.
	msg := diagnostic.ServerErrorMessage(body)
	if msg == "" {
		msg = "access denied, and the server returned no reason"
	}

	return &diagnostic.Error{
		Operation:  fmt.Sprintf("%s file %q", verb, path),
		StatusCode: 403,
		Message:    msg,
		Suggestions: []string{
			fmt.Sprintf("The OAuth scope and the Grail IAM permission are separate gates, both named %q — a granted scope alone is not enough", permission),
			fmt.Sprintf("Check the scope gate first: %s", cmdHint),
			fmt.Sprintf("If that reports \"ok\", or \"unknown\" because the token is not introspectable, the tenant's IAM policy is missing the permission. Add: ALLOW %s WHERE storage:file-path startsWith \"/lookups/\";", permission),
			"Policy permissions can be path-scoped, so access to one /lookups/ subtree does not imply access to another",
		},
	}
}
