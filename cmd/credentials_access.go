package cmd

import (
	"errors"
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// credentialAccessDenied replaces a bare 403 from the Credential vault API with
// one that names the missing scope and how to get it.
func credentialAccessDenied(err error, operation string, access auth.Access) error {
	if err == nil || !isPermissionDenied(err) {
		return err
	}
	msg := "access denied, and the server returned no reason"
	var apiErr *httpclient.APIError
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		msg = apiErr.Message
		if apiErr.Details != "" {
			msg += " - " + apiErr.Details
		}
	}

	scopes := auth.ScopesForResource("credential", access)
	scope := "credential-vault:entries:read"
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	return &diagnostic.Error{
		Operation:  operation,
		StatusCode: 403,
		Message:    msg,
		Suggestions: []string{
			fmt.Sprintf("The token lacks %s", scope),
			"OAuth context: run 'dtctl auth login' again; every safety level requests the read scope, the write scope needs readwrite-all or dangerously-unrestricted",
			fmt.Sprintf("Platform-token context: create the token with %s, then 'dtctl config set-credentials'", scope),
			"Re-run the same command with --check-scopes to see what the active token carries",
		},
		Err: err,
	}
}
