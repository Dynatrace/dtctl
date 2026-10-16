package cmd

import (
	"errors"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/diagnostic"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// adminAccessDenied builds the error for a 403 on a request made with
// --admin-access.
//
// Both APIs that have the flag refuse it whenever either of two gates is
// closed: the token must carry the admin scope (document:documents:admin for
// the Document API, automation:workflows:admin for the Automation API), and the
// tenant's IAM policy must grant the permission of the same name. The Document
// API says "Insufficient permissions to request admin-access", the Automation
// API "Only admins can request admin access." Before #375 was fixed no
// `dtctl auth login` requested the document scope at all, so an OAuth user with
// the right policy got this error with nothing to act on. The suggestions name
// both gates and, for the scope gate, what fixes it at the active context's
// safety level.
func adminAccessDenied(err error, cfg *config.Config, scope, operation string) error {
	msg := "access denied, and the server returned no reason"
	var apiErr *httpclient.APIError
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		msg = apiErr.Message
		if apiErr.Details != "" {
			// Keep the body: it carries the errorRef support asks for.
			msg += " - " + apiErr.Details
		}
	}

	suggestions := []string{
		fmt.Sprintf("--admin-access needs %q twice: as a scope in your token and as a permission in the tenant's IAM policy — a granted scope alone is not enough", scope),
	}

	level := config.DefaultSafetyLevel
	contextName := ""
	if cfg != nil {
		level = cfg.GetEffectiveSafetyLevel()
		contextName = cfg.CurrentContext
	}
	if loginRequestsScope(level, scope) {
		suggestions = append(suggestions,
			fmt.Sprintf("OAuth login: 'dtctl auth login' requests %s at the %s safety level. A session from an older dtctl does not carry it — run 'dtctl auth login' again to pick it up", scope, level))
	} else {
		suggestions = append(suggestions,
			fmt.Sprintf("OAuth login: the %s safety level of context %q does not request %s. Log in with a context at readwrite-all or dangerously-unrestricted to get it", level, contextName, scope))
	}
	suggestions = append(suggestions,
		fmt.Sprintf("Platform token: create it with the %s scope", scope),
		"Check the scope gate first: re-run the same command with --check-scopes",
		fmt.Sprintf("If that reports \"ok\", or \"unknown\" because the token is not introspectable, the tenant's IAM policy is missing the permission. Add: ALLOW %s;", scope),
	)

	return &diagnostic.Error{
		Operation:   operation,
		StatusCode:  403,
		Message:     msg,
		Suggestions: suggestions,
		Err:         err,
	}
}

// loginRequestsScope reports whether `dtctl auth login` at the given safety
// level asks for scope.
func loginRequestsScope(level config.SafetyLevel, scope string) bool {
	for _, s := range auth.GetScopesForSafetyLevel(level) {
		if s == scope {
			return true
		}
	}
	return false
}
