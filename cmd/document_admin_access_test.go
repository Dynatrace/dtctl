package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/pkg/resources/document"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// adminAccessServer answers every document listing the way the Document API
// does when a --admin-access request lacks the scope or the IAM permission.
func adminAccessServer(t *testing.T) *document.Handler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("admin-access") == "true" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Insufficient permissions to request admin-access"}}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Forbidden"}}`))
	}))
	t.Cleanup(srv.Close)
	c, err := client.NewForTesting(srv.URL, "test-token")
	require.NoError(t, err)
	return document.NewHandler(c)
}

func configAtLevel(level config.SafetyLevel) *config.Config {
	cfg := config.NewConfig()
	cfg.SetContextWithOptions("test", "https://env.example.invalid", "",
		&config.ContextOptions{SafetyLevel: level})
	cfg.CurrentContext = "test"
	return cfg
}

// TestListDocuments_AdminAccessDeniedIsActionable pins #375's second half: the
// bare "API error (403)" gave an OAuth user nothing to act on. The error must
// keep the server's reason and the 403 status, and name the scope, the
// re-login, and the IAM policy gate.
func TestListDocuments_AdminAccessDeniedIsActionable(t *testing.T) {
	h := adminAccessServer(t)
	cfg := configAtLevel(config.SafetyLevelReadWriteAll)

	_, err := listDocuments(h, document.DocumentFilters{Type: "dashboard", AdminAccess: true}, cfg)
	require.Error(t, err)

	var diagErr *diagnostic.Error
	require.True(t, errors.As(err, &diagErr), "want a diagnostic.Error, got %T", err)
	require.Equal(t, 403, diagErr.StatusCode)
	require.Contains(t, diagErr.Message, "Insufficient permissions to request admin-access")
	require.True(t, isPermissionDenied(err))

	var apiErr *httpclient.APIError
	require.True(t, errors.As(err, &apiErr), "the SDK error must stay reachable via Unwrap")

	all := strings.Join(diagErr.Suggestions, "\n")
	require.Contains(t, all, "document:documents:admin")
	require.Contains(t, all, "run 'dtctl auth login' again")
	require.Contains(t, all, "--check-scopes")
	require.Contains(t, all, "ALLOW document:documents:admin;")
}

// A level whose login does not request the scope must say so rather than
// suggest a re-login that would come back without it again.
func TestListDocuments_AdminAccessDeniedNamesLevelWithoutScope(t *testing.T) {
	for _, level := range []config.SafetyLevel{config.SafetyLevelReadOnly, config.SafetyLevelReadWriteMine} {
		t.Run(string(level), func(t *testing.T) {
			h := adminAccessServer(t)
			_, err := listDocuments(h, document.DocumentFilters{AdminAccess: true}, configAtLevel(level))

			var diagErr *diagnostic.Error
			require.True(t, errors.As(err, &diagErr))
			all := strings.Join(diagErr.Suggestions, "\n")
			require.Contains(t, all, "the "+string(level)+" safety level")
			require.Contains(t, all, "does not request document:documents:admin")
			require.NotContains(t, all, "run 'dtctl auth login' again")
		})
	}
}

// Without --admin-access a 403 is some other denial; the admin-access advice
// would be wrong for it, so the error passes through untouched.
func TestListDocuments_PlainForbiddenIsUnchanged(t *testing.T) {
	h := adminAccessServer(t)

	_, err := listDocuments(h, document.DocumentFilters{Type: "dashboard"}, configAtLevel(config.SafetyLevelReadWriteAll))
	require.Error(t, err)

	var diagErr *diagnostic.Error
	require.False(t, errors.As(err, &diagErr), "a plain 403 must not get --admin-access advice")
	var apiErr *httpclient.APIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, 403, apiErr.StatusCode)
}
