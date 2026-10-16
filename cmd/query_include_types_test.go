package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runQueryWithTypes runs `dtctl query` end to end against a server whose
// result carries a DQL type block, and returns stdout and stderr.
func runQueryWithTypes(t *testing.T, args ...string) (string, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/platform/storage/query/v1/query:execute" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"SUCCEEDED","result":{"records":[{"host":"web-01","count":"3"}],` +
			`"types":[{"indexRange":[0,0],"mappings":{"host":{"type":"string"},"count":{"type":"long"}}}]}}`))
	}))
	t.Cleanup(srv.Close)
	clearAgentEnvVars(t)
	t.Cleanup(func() { restorePristineTree(context.Background()) })

	var stdout, stderr bytes.Buffer
	code := Run(append([]string{"query", "fetch logs", "--plain"}, args...), RunOptions{
		Session: &Session{EnvironmentURL: srv.URL, Token: "t"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	require.Zero(t, code, "stderr: %s", stderr.String())
	return stdout.String(), stderr.String()
}

// The "has no effect" warning is for a user who asked for the type block and
// cannot get it in this format. --include-types=false declines the block, so
// it must not warn about not getting it.
func TestQueryIncludeTypesFalseDoesNotWarn(t *testing.T) {
	const inert = "--include-types has no effect"

	_, stderr := runQueryWithTypes(t, "-o", "table", "--include-types=false")
	require.NotContains(t, stderr, inert)

	// The same run with the flag true does warn, so the assertion above is
	// not passing merely because the warning never reaches stderr here.
	_, stderr = runQueryWithTypes(t, "-o", "table", "--include-types")
	require.True(t, strings.Contains(stderr, inert), "stderr: %s", stderr)
}
