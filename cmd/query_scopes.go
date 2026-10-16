package cmd

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
)

// notAuthorizedForTable is the Grail error a query gets when the token cannot
// read a table it touches. It arrives as errorType, or only as the message.
const notAuthorizedForTable = "NOT_AUTHORIZED_FOR_TABLE"

const smartscapeReadScope = "storage:smartscape:read"

// dqlScopePrecheck refuses a query, before it is sent, when the query provably
// needs a storage scope the active token was not granted.
//
// It only blocks on certainty. The requirement is exec.RequiredStorageScopes,
// a lower bound; the granted scopes must be introspectable (an OAuth token —
// platform and API tokens are opaque, so they always proceed and a denial comes
// back from Grail as usual). Every other doubt resolves to "run the query".
func dqlScopePrecheck(ctx context.Context, query string) error {
	// An embedded invocation carries its own credentials; reading the host's
	// token store on its behalf would compare against the wrong token.
	if currentSession(ctx) != nil {
		return nil
	}
	needs := exec.RequiredStorageScopes(query)
	if len(needs) == 0 {
		return nil
	}
	granted, known := grantedScopesFunc(ctx)
	if !known {
		return nil
	}
	have := make(map[string]bool, len(granted))
	for _, g := range granted {
		if strings.Contains(g, "*") {
			// A pattern grant cannot be compared by name, so it proves nothing.
			return nil
		}
		have[g] = true
	}

	var required, missing, reasons []string
	for _, n := range needs {
		required = append(required, n.Scope)
		if have[n.Scope] {
			continue
		}
		missing = append(missing, n.Scope)
		reasons = append(reasons, strings.Join(n.Because, ", ")+" needs "+n.Scope)
	}
	if len(missing) == 0 {
		return nil
	}

	reason := "this token lacks scopes this query needs: " + strings.Join(reasons, "; ")
	if len(reasons) == 1 {
		reason = reasons[0] + ", which this token lacks"
	}
	return &ScopeError{
		Verb:     "query",
		Required: required,
		Granted:  granted,
		Missing:  missing,
		Reason:   reason,
		Advice:   dqlScopeAdvice(missing, granted),
	}
}

// dqlScopeAdvice is what an agent can do about missing storage scopes. granted
// is nil when the token's scopes are not known.
func dqlScopeAdvice(missing, granted []string) []string {
	var s []string
	if containsString(missing, smartscapeReadScope) {
		if containsString(granted, "storage:entities:read") {
			s = append(s, "this token cannot read Smartscape: instead of smartscapeNodes/smartscapeEdges/getNodeName()/getNodeField(), "+
				"use fields already on the records (e.g. service.name, dt.entity.service) or fetch dt.entity.service")
		} else {
			s = append(s, "this token cannot read Smartscape: drop smartscapeNodes/smartscapeEdges/getNodeName()/getNodeField() "+
				"and use fields already on the records (e.g. service.name)")
		}
	}
	if readable := readableStorageScopes(granted); len(readable) > 0 {
		s = append(s, "this token can read: "+strings.Join(readable, ", "))
	}
	if len(missing) > 0 {
		s = append(s,
			"do not retry or fan out queries that need "+strings.Join(missing, ", ")+": they fail the same way until the token changes",
			"re-create your token with: "+strings.Join(missing, ", "),
		)
	} else {
		s = append(s, "this token cannot read a table this query uses: check its storage:*:read scopes with 'dtctl auth status'; do not retry unchanged")
	}
	return append(s, "see 'dtctl commands howto' for token scope guidance")
}

// readableStorageScopes are the storage read scopes among granted, sorted.
func readableStorageScopes(granted []string) []string {
	var out []string
	for _, g := range granted {
		if strings.HasPrefix(g, "storage:") && strings.HasSuffix(g, ":read") {
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// isNotAuthorizedForTable reports whether err is Grail refusing a table the
// token cannot read.
func isNotAuthorizedForTable(err error) (*sdkquery.QueryError, bool) {
	var q *sdkquery.QueryError
	if !errors.As(err, &q) {
		return nil, false
	}
	if q.ErrorType == notAuthorizedForTable || strings.Contains(q.Message, notAuthorizedForTable) {
		return q, true
	}
	return nil, false
}

// notAuthorizedForTableDetail renders a runtime NOT_AUTHORIZED_FOR_TABLE as the
// same insufficient_scope envelope the precheck produces, naming the missing
// scopes when the error says which tables (or scopes) were refused.
func notAuthorizedForTableDetail(q *sdkquery.QueryError) *output.ErrorDetail {
	missing := exec.StorageScopesInText(q.Message + " " + q.Detail + " " + strings.Join(q.Arguments, " "))
	if len(missing) == 0 {
		seen := map[string]bool{}
		for _, a := range q.Arguments {
			if scope, ok := exec.StorageScopeForDataObject(strings.Trim(a, "`\" ")); ok && !seen[scope] {
				seen[scope] = true
				missing = append(missing, scope)
			}
		}
		sort.Strings(missing)
	}
	return &output.ErrorDetail{
		Code:          "insufficient_scope",
		Message:       q.Error(),
		StatusCode:    q.StatusCode,
		MissingScopes: missing,
		Suggestions:   dqlScopeAdvice(missing, nil),
	}
}
