package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
)

// withGrantedScopes overrides grantedScopesFunc for one test.
func withGrantedScopes(t *testing.T, granted []string, known bool) {
	t.Helper()
	orig := grantedScopesFunc
	grantedScopesFunc = func(context.Context) ([]string, bool) { return granted, known }
	t.Cleanup(func() { grantedScopesFunc = orig })
}

const smartscapeLogQuery = "fetch logs | fieldsAdd svc = getNodeName(dt.smartscape.service) | limit 5"

func TestDQLScopePrecheck_FailsFastOnMissingScope(t *testing.T) {
	withGrantedScopes(t, []string{"storage:logs:read", "storage:entities:read", "storage:buckets:read"}, true)

	err := dqlScopePrecheck(context.Background(), smartscapeLogQuery)

	var scopeErr *ScopeError
	require.ErrorAs(t, err, &scopeErr)
	require.Equal(t, []string{"storage:smartscape:read"}, scopeErr.Missing)
	require.Equal(t, []string{"storage:logs:read", "storage:smartscape:read"}, scopeErr.Required)
	require.Equal(t, "getNodeName() needs storage:smartscape:read, which this token lacks", err.Error())

	detail := errorToDetail(context.Background(), err)
	require.Equal(t, "insufficient_scope", detail.Code)
	require.Equal(t, []string{"storage:smartscape:read"}, detail.MissingScopes)
	require.Equal(t, err.Error(), detail.Message)
	joined := strings.Join(detail.Suggestions, "\n")
	// The entity alternative is only offered because the token can read entities.
	require.Contains(t, joined, "dt.entity.service")
	require.Contains(t, joined, "storage:logs:read")
	require.Equal(t, client.ExitPermissionError, exitCodeForError(err))
}

func TestDQLScopePrecheck_EntityAlternativeNeedsEntityScope(t *testing.T) {
	withGrantedScopes(t, []string{"storage:logs:read"}, true)

	detail := errorToDetail(context.Background(), dqlScopePrecheck(context.Background(), smartscapeLogQuery))

	require.Equal(t, "insufficient_scope", detail.Code)
	joined := strings.Join(detail.Suggestions, "\n")
	require.NotContains(t, joined, "fetch dt.entity.service",
		"do not steer the agent to a table this token cannot read either")
	require.Contains(t, joined, "service.name")
}

func TestDQLScopePrecheck_SeveralMissingScopes(t *testing.T) {
	withGrantedScopes(t, []string{"storage:events:read"}, true)

	err := dqlScopePrecheck(context.Background(), smartscapeLogQuery)

	var scopeErr *ScopeError
	require.ErrorAs(t, err, &scopeErr)
	require.Equal(t, []string{"storage:logs:read", "storage:smartscape:read"}, scopeErr.Missing)
	require.Equal(t,
		"this token lacks scopes this query needs: fetch logs needs storage:logs:read; getNodeName() needs storage:smartscape:read",
		err.Error())
}

// Every case below must let the query run: blocking a query that would have
// worked is worse than having no precheck.
func TestDQLScopePrecheck_Abstains(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		granted []string
		known   bool
	}{
		{name: "token scopes not introspectable", query: smartscapeLogQuery, known: false},
		{name: "all required scopes granted", query: smartscapeLogQuery,
			granted: []string{"storage:logs:read", "storage:smartscape:read"}, known: true},
		{name: "query needs no scope the analysis knows", query: "fetch dt.system.data_objects",
			granted: []string{"storage:logs:read"}, known: true},
		{name: "wildcard grant cannot be compared", query: smartscapeLogQuery,
			granted: []string{"storage:*"}, known: true},
		{name: "single-quoted string (rejected by Grail, which dtctl turns into a quoting hint)",
			query: "fetch dt.system.events | filter content == '| fetch logs'", granted: []string{"storage:events:read"}, known: true},
		{name: "keyword only in a string literal", query: `fetch dt.system.events | filter content == "getNodeName(x)"`,
			granted: []string{"storage:logs:read"}, known: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withGrantedScopes(t, tt.granted, tt.known)
			require.NoError(t, dqlScopePrecheck(context.Background(), tt.query))
		})
	}
}

func TestDQLScopePrecheck_SkippedForSessionInvocations(t *testing.T) {
	// An embedded invocation carries its own credentials; the precheck must not
	// consult the host's token store on its behalf.
	withGrantedScopes(t, []string{"storage:logs:read"}, true)
	orig := runSession
	runSession = &Session{}
	t.Cleanup(func() { runSession = orig })

	require.NoError(t, dqlScopePrecheck(context.Background(), smartscapeLogQuery))
}

func TestErrorToDetail_NotAuthorizedForTable(t *testing.T) {
	tests := []struct {
		name        string
		err         *sdkquery.QueryError
		wantMissing []string
	}{
		{
			name: "table names in the arguments",
			err: &sdkquery.QueryError{StatusCode: 403, Message: "not authorized",
				ErrorType: "NOT_AUTHORIZED_FOR_TABLE", Arguments: []string{"logs", "spans"}},
			wantMissing: []string{"storage:logs:read", "storage:spans:read"},
		},
		{
			name: "scope spelled out in the detail",
			err: &sdkquery.QueryError{StatusCode: 403, Message: "NOT_AUTHORIZED_FOR_TABLE",
				ErrorType: "NOT_AUTHORIZED_FOR_TABLE", Detail: "missing scope storage:smartscape:read"},
			wantMissing: []string{"storage:smartscape:read"},
		},
		{
			name:        "marker only in the message, the observed production shape",
			err:         &sdkquery.QueryError{StatusCode: 403, Message: "NOT_AUTHORIZED_FOR_TABLE", Arguments: []string{"`bizevents`"}},
			wantMissing: []string{"storage:bizevents:read"},
		},
		{
			name:        "table the mapping does not know",
			err:         &sdkquery.QueryError{StatusCode: 403, ErrorType: "NOT_AUTHORIZED_FOR_TABLE", Message: "not authorized", Arguments: []string{"some.table"}},
			wantMissing: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := fmt.Errorf("query failed: %w", tt.err)
			detail := errorToDetail(context.Background(), wrapped)
			require.Equal(t, "insufficient_scope", detail.Code)
			require.Equal(t, tt.wantMissing, detail.MissingScopes)
			require.Equal(t, 403, detail.StatusCode)
			require.NotEmpty(t, detail.Suggestions)
			require.Equal(t, client.ExitPermissionError, exitCodeForError(wrapped))
		})
	}
}

func TestErrorToDetail_NotAuthorizedForSmartscapeSuggestsAlternative(t *testing.T) {
	err := &sdkquery.QueryError{StatusCode: 403, ErrorType: "NOT_AUTHORIZED_FOR_TABLE",
		Message: "not authorized", Detail: "storage:smartscape:read"}
	joined := strings.Join(errorToDetail(context.Background(), err).Suggestions, "\n")
	require.Contains(t, joined, "service.name")
}

func TestErrorToDetail_OtherQueryErrorsKeepTheirCode(t *testing.T) {
	err := &sdkquery.QueryError{StatusCode: 400, ErrorType: "UNKNOWN_DATA_OBJECT", Message: "nope"}
	require.Equal(t, "unknown_data_object", errorToDetail(context.Background(), err).Code)
	require.Equal(t, client.ExitError, exitCodeForError(err))
}

func TestGrantedScopes_PartialListIsUnknown(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	origCfg := cfgFile(context.Background())
	gFlags.cfgFile = configPath
	t.Cleanup(func() { gFlags.cfgFile = origCfg })
	cfg := config.NewConfig()
	cfg.SetContext("test", "https://example.invalid", "test-oauth")
	cfg.CurrentContext = "test"
	require.NoError(t, cfg.SaveTo(configPath))

	withStubbedSessionStatus(t, &SessionStatus{IsOAuth: true, GrantedScopes: []string{"storage:logs:read"}})
	scopes, known := grantedScopes(context.Background())
	require.True(t, known)
	require.Equal(t, []string{"storage:logs:read"}, scopes)

	// Read back from the access token's claim: a subset, so it proves nothing.
	withStubbedSessionStatus(t, &SessionStatus{IsOAuth: true, GrantedScopes: []string{"storage:logs:read"}, grantedScopesPartial: true})
	_, known = grantedScopes(context.Background())
	require.False(t, known)
}
