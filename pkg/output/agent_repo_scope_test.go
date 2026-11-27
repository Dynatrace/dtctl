package output

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// Without a repo scope the envelope has no repo_scope key, not a null one.
func TestRepoScopeContextAbsentWhenNil(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, NewAgentPrinter(&buf, &ResponseContext{Verb: "query"}).Print([]string{}))
	require.Equal(t, `{"ok":true,"result":[],"context":{"verb":"query"}}`+"\n", buf.String())
}

func TestRepoScopeContextShape(t *testing.T) {
	var buf bytes.Buffer
	ctx := &ResponseContext{Verb: "query", RepoScope: &RepoScope{
		Name:        "checkout",
		File:        ".dtctl-repo-scope.yaml",
		Environment: "abc12345.apps.dynatrace.example.invalid",
		Applied:     true,
		DataObject:  "logs",
		Filter:      `service.name == "checkout"`,
		Code:        "applied",
	}}
	require.NoError(t, NewAgentPrinter(&buf, ctx).Print([]string{}))
	require.Equal(t, `{"ok":true,"result":[],"context":{"verb":"query","repo_scope":{"name":"checkout",`+
		`"file":".dtctl-repo-scope.yaml","environment":"abc12345.apps.dynatrace.example.invalid","applied":true,`+
		`"data_object":"logs","filter":"service.name == \"checkout\"","code":"applied"}}}`+"\n", buf.String())

	// A scope that resolved but did not apply still names the file and the
	// environment, and says applied:false explicitly.
	buf.Reset()
	ctx.RepoScope = &RepoScope{File: ".dtctl-repo-scope.yaml", Environment: "127.0.0.1", Code: "no_entry", Reason: "no entry covers \".\""}
	require.NoError(t, NewAgentPrinter(&buf, ctx).Print([]string{}))
	require.Contains(t, buf.String(), `"repo_scope":{"file":".dtctl-repo-scope.yaml","environment":"127.0.0.1","applied":false,"code":"no_entry","reason":"no entry covers \".\""}`)
}
