package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/commands"
	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// withScopeState overrides grantedScopesFunc and the relevant global flags for a
// test, restoring them afterwards.
func withScopeState(t *testing.T, check, agent bool, format string, granted []string, known bool) {
	t.Helper()
	origCheck, origAgent, origFormat, origFunc := gFlags.checkScopes, agentMode(context.Background()), outputFormat(context.Background()), grantedScopesFunc
	gFlags.checkScopes, gFlags.agentMode, gFlags.outputFormat = check, agent, format
	grantedScopesFunc = func(context.Context) ([]string, bool) { return granted, known }
	t.Cleanup(func() {
		gFlags.checkScopes, gFlags.agentMode, gFlags.outputFormat, grantedScopesFunc = origCheck, origAgent, origFormat, origFunc
	})
}

// captureStdout runs fn while capturing everything written to os.Stdout.
func captureScopeStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

func TestSubtractScopes(t *testing.T) {
	got := subtractScopes(
		[]string{"a", "b", "c"},
		[]string{"b"},
	)
	require.Equal(t, []string{"a", "c"}, got)

	require.Empty(t, subtractScopes([]string{"a"}, []string{"a", "x"}))
	require.Empty(t, subtractScopes(nil, []string{"a"}))
}

// TestMutatingCommandsUseRunE guards the auto-preflight's coverage: it wraps
// only commands with a non-nil RunE (see installScopePreflight). A mutating
// command implemented with Run instead of RunE would silently bypass the
// scope preflight, so fail the build if one ever appears.
func TestMutatingCommandsUseRunE(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		// Only runnable leaves matter.
		if c.Run == nil && c.RunE == nil {
			return
		}
		verb, _ := verbResource(c)
		if _, mutating := commands.MutatingVerbs[verb]; !mutating {
			return
		}
		require.Nil(t, c.Run,
			"mutating command %q uses Run; it must use RunE so installScopePreflight covers it", c.CommandPath())
		require.NotNil(t, c.RunE,
			"mutating command %q has no RunE; the scope preflight cannot wrap it", c.CommandPath())
	}
	walk(rootCmd)
}

func TestVerbResource(t *testing.T) {
	cases := []struct {
		args     []string
		wantVerb string
		wantRes  string
	}{
		{[]string{"delete", "workflow"}, "delete", "workflow"},
		{[]string{"get", "workflows"}, "get", "workflows"},
		{[]string{"query"}, "query", ""},
		{[]string{"wait", "query"}, "wait", "query"},
	}
	for _, c := range cases {
		cmd, _, err := rootCmd.Find(c.args)
		require.NoError(t, err, "find %v", c.args)
		verb, res := verbResource(cmd)
		require.Equal(t, c.wantVerb, verb, "verb for %v", c.args)
		require.Equal(t, c.wantRes, res, "resource for %v", c.args)
	}
}

func TestRequiredScopesFor(t *testing.T) {
	got, req := requiredScopesFor("delete", "workflow")
	require.Equal(t, scopeRequirementKnown, req)
	require.Equal(t, []string{"automation:workflows:write"}, got)

	got, req = requiredScopesFor("get", "workflows")
	require.Equal(t, scopeRequirementKnown, req)
	require.Equal(t, []string{"automation:workflows:read"}, got)

	// DQL verbs carry flat scopes at the verb level.
	got, req = requiredScopesFor("query", "")
	require.Equal(t, scopeRequirementKnown, req)
	require.Contains(t, got, "storage:logs:read")

	got, req = requiredScopesFor("wait", "query")
	require.Equal(t, scopeRequirementKnown, req)
	require.Contains(t, got, "storage:logs:read")

	// Local commands have no scope requirement.
	_, req = requiredScopesFor("ctx", "set")
	require.Equal(t, scopeRequirementNone, req)

	// `exec api` can reach any endpoint the environment publishes, each declaring
	// its own scope, so the catalog cannot hold the answer. This must be a distinct
	// verdict from "needs nothing" — see TestComputeScopeVerdict.
	_, req = requiredScopesFor("exec", "api")
	require.Equal(t, scopeRequirementPerCall, req)
}

func TestComputeScopeVerdict(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		withScopeState(t, true, false, "json", []string{"automation:workflows:write", "x"}, true)
		r := computeScopeVerdict(context.Background(), "delete", "workflow", []string{"automation:workflows:write"}, nil, scopeRequirementKnown)
		require.Equal(t, scopeStatusOK, r.Status)
		require.Empty(t, r.MissingScopes)
	})

	t.Run("insufficient", func(t *testing.T) {
		withScopeState(t, true, false, "json", []string{"automation:workflows:read"}, true)
		r := computeScopeVerdict(context.Background(), "delete", "workflow", []string{"automation:workflows:write"}, nil, scopeRequirementKnown)
		require.Equal(t, scopeStatusInsufficient, r.Status)
		require.Equal(t, []string{"automation:workflows:write"}, r.MissingScopes)
		require.NotEmpty(t, r.Suggestions)
	})

	t.Run("unknown", func(t *testing.T) {
		withScopeState(t, true, false, "json", nil, false)
		r := computeScopeVerdict(context.Background(), "delete", "workflow", []string{"automation:workflows:write"}, nil, scopeRequirementKnown)
		require.Equal(t, scopeStatusUnknown, r.Status)
		require.Empty(t, r.GrantedScopes)
		require.NotEmpty(t, r.Suggestions)
	})

	t.Run("no scopes required", func(t *testing.T) {
		withScopeState(t, true, false, "json", nil, false)
		r := computeScopeVerdict(context.Background(), "ctx", "set", nil, nil, scopeRequirementNone)
		require.Equal(t, scopeStatusOK, r.Status)
		require.Empty(t, r.RequiredScopes)
	})

	// A per-call requirement must not borrow the "no scopes required" verdict. The
	// two are indistinguishable to a caller reading `status: ok` — and for the one
	// command that can reach any endpoint the environment publishes, an ok verdict
	// that checked nothing is an assurance dtctl has no basis to give.
	t.Run("per-call requirement is unknown, never ok", func(t *testing.T) {
		withScopeState(t, true, false, "json", []string{"automation:workflows:write"}, true)
		r := computeScopeVerdict(context.Background(), "exec", "api", nil, nil, scopeRequirementPerCall)
		require.Equal(t, scopeStatusUnknown, r.Status,
			"a check that did not happen must not report ok")
		require.Empty(t, r.RequiredScopes)
		require.NotEmpty(t, r.Suggestions, "an unknown verdict must say how to find the answer")
		require.Contains(t, strings.Join(r.Suggestions, "\n"), "describe api")
	})
}

func TestScopePreflight_CheckScopes_OKSkipsAndPrints(t *testing.T) {
	withScopeState(t, true, false, "json", []string{"automation:workflows:write"}, true)
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)

	var skip bool
	var preErr error
	out := captureScopeStdout(t, func() { skip, preErr = scopePreflight(cmd, nil) })

	require.True(t, skip, "command body must be skipped")
	require.NoError(t, preErr)

	var res ScopeCheckResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Equal(t, scopeStatusOK, res.Status)
	require.Equal(t, "delete", res.Verb)
	require.Equal(t, "workflow", res.Resource)
}

func TestScopePreflight_CheckScopes_MissingExitsNonZero(t *testing.T) {
	withScopeState(t, true, false, "json", []string{"automation:workflows:read"}, true)
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)

	var skip bool
	var preErr error
	_ = captureScopeStdout(t, func() { skip, preErr = scopePreflight(cmd, nil) })

	require.True(t, skip)
	var silent *silentExitError
	require.ErrorAs(t, preErr, &silent)
	require.Equal(t, client.ExitPermissionError, silent.code)
}

func TestScopePreflight_AgentBlocksMutatingWhenMissing(t *testing.T) {
	withScopeState(t, false, true, "json", []string{"automation:workflows:read"}, true)
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)

	skip, preErr := scopePreflight(cmd, nil)
	require.False(t, skip)
	var scopeErr *ScopeError
	require.ErrorAs(t, preErr, &scopeErr)
	require.Equal(t, []string{"automation:workflows:write"}, scopeErr.Missing)

	// And it maps to the insufficient_scope envelope + ExitPermissionError.
	detail := errorToDetail(context.Background(), preErr)
	require.Equal(t, "insufficient_scope", detail.Code)
	require.Equal(t, []string{"automation:workflows:write"}, detail.MissingScopes)
	require.Equal(t, client.ExitPermissionError, exitCodeForError(preErr))
}

func TestScopePreflight_AgentProceedsWhenScopePresent(t *testing.T) {
	withScopeState(t, false, true, "json", []string{"automation:workflows:write"}, true)
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)

	skip, preErr := scopePreflight(cmd, nil)
	require.False(t, skip)
	require.NoError(t, preErr, "command should proceed when scope is present")
}

func TestScopePreflight_AgentProceedsWhenUnknown(t *testing.T) {
	// Opaque token: granted scopes not introspectable -> do not block.
	withScopeState(t, false, true, "json", nil, false)
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)

	skip, preErr := scopePreflight(cmd, nil)
	require.False(t, skip)
	require.NoError(t, preErr)
}

func TestScopePreflight_AgentIgnoresReadVerbs(t *testing.T) {
	// get is non-mutating: agent auto-preflight must not block it even if a
	// (hypothetical) scope were missing.
	withScopeState(t, false, true, "json", []string{}, true)
	cmd, _, err := rootCmd.Find([]string{"get", "workflows"})
	require.NoError(t, err)

	skip, preErr := scopePreflight(cmd, nil)
	require.False(t, skip)
	require.NoError(t, preErr)
}

func TestScopePreflight_CheckScopes_AgentWrapsOKInEnvelope(t *testing.T) {
	// --check-scopes in agent mode must emit the standard {ok,result,context}
	// envelope, not a bare verdict object.
	withScopeState(t, true, true, "json", []string{"automation:workflows:write"}, true)
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)

	var skip bool
	var preErr error
	out := captureScopeStdout(t, func() { skip, preErr = scopePreflight(cmd, nil) })
	require.True(t, skip)
	require.NoError(t, preErr)

	var resp struct {
		OK     bool             `json:"ok"`
		Result ScopeCheckResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.True(t, resp.OK)
	require.Equal(t, scopeStatusOK, resp.Result.Status)
	require.Equal(t, "delete", resp.Result.Verb)
}

func TestScopePreflight_CheckScopes_AgentMissingUsesErrorEnvelope(t *testing.T) {
	// --check-scopes in agent mode with a missing scope must route through the
	// ScopeError path: insufficient_scope error envelope + ExitPermissionError,
	// identical to the auto-preflight contract.
	withScopeState(t, true, true, "json", []string{"automation:workflows:read"}, true)
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)

	skip, preErr := scopePreflight(cmd, nil)
	require.False(t, skip)
	var scopeErr *ScopeError
	require.ErrorAs(t, preErr, &scopeErr)
	require.Equal(t, []string{"automation:workflows:write"}, scopeErr.Missing)

	detail := errorToDetail(context.Background(), preErr)
	require.Equal(t, "insufficient_scope", detail.Code)
	require.Equal(t, client.ExitPermissionError, exitCodeForError(preErr))
}

func TestScopePreflight_DisabledIsNoOp(t *testing.T) {
	withScopeState(t, false, false, "json", []string{"automation:workflows:read"}, true)
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)

	skip, preErr := scopePreflight(cmd, nil)
	require.False(t, skip)
	require.NoError(t, preErr)
}

// withFlagSet sets a flag on one of rootCmd's real commands for the duration of
// a test and restores it afterwards. rootCmd is shared package state, so a flag
// left set here would silently change the requirement every later test resolves.
func withFlagSet(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	require.NotNil(t, f, "flag %q is not registered on %q", name, cmd.CommandPath())
	// A list flag's Set appends, and its String() does not parse back, so it
	// is replaced and restored as a list.
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		orig, origChanged := sv.GetSlice(), f.Changed
		require.NoError(t, sv.Replace(strings.Split(value, ",")))
		f.Changed = true
		t.Cleanup(func() {
			require.NoError(t, sv.Replace(orig))
			f.Changed = origChanged
		})
		return
	}
	origValue, origChanged := f.Value.String(), f.Changed
	require.NoError(t, cmd.Flags().Set(name, value))
	t.Cleanup(func() {
		require.NoError(t, f.Value.Set(origValue))
		f.Changed = origChanged
	})
}

func TestScopesForInvocation_FlagAddsScope(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"get", "dashboards"})
	require.NoError(t, err)

	base, req := scopesForInvocation(cmd, "get", "dashboards")
	require.Equal(t, scopeRequirementKnown, req)
	require.NotContains(t, base, "document:documents:admin",
		"a plain get dashboards does not use admin access")

	// --admin-access lists documents as their effective owner. Without
	// document:documents:admin the API answers 403 "Insufficient permissions to
	// request admin-access", so the scope is a certainty whenever the flag is set.
	withFlagSet(t, cmd, "admin-access", "true")
	got, req := scopesForInvocation(cmd, "get", "dashboards")
	require.Equal(t, scopeRequirementKnown, req)
	require.Contains(t, got, "document:documents:admin")
	require.Subset(t, got, base, "flag scopes are additive, not a replacement")
}

func TestScopesForInvocation_FlagOffAddsNothing(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"get", "dashboards"})
	require.NoError(t, err)
	base, _ := scopesForInvocation(cmd, "get", "dashboards")

	// An explicit --admin-access=false is a set flag that is nonetheless off; it
	// must be treated like omitting it.
	withFlagSet(t, cmd, "admin-access", "false")
	got, _ := scopesForInvocation(cmd, "get", "dashboards")
	require.Equal(t, base, got)
}

// TestScopesForInvocation_FlagValueSelectsScopes: a `<flag>=<value>` entry
// applies only to that value. `share --environment link` works on environment
// shares; `--environment public` only sets isPrivate, so demanding the
// environment-share scopes there would block a call that does not need them.
func TestScopesForInvocation_FlagValueSelectsScopes(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"share", "document"})
	require.NoError(t, err)
	base, _ := scopesForInvocation(cmd, "share", "document")

	withFlagSet(t, cmd, "environment", "public")
	got, _ := scopesForInvocation(cmd, "share", "document")
	require.Equal(t, base, got)

	withFlagSet(t, cmd, "environment", "link")
	got, _ = scopesForInvocation(cmd, "share", "document")
	require.Contains(t, got, "document:environment-shares:write")
	require.Subset(t, got, base)

	// --environment is a list: link counts wherever it appears in it.
	withFlagSet(t, cmd, "environment", "public,link")
	got, _ = scopesForInvocation(cmd, "share", "document")
	require.Contains(t, got, "document:environment-shares:write")
}

func TestScopesForInvocation_UnlistedCommandIsUnchanged(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)
	got, req := scopesForInvocation(cmd, "delete", "workflow")
	want, wantReq := requiredScopesFor("delete", "workflow")
	require.Equal(t, want, got)
	require.Equal(t, wantReq, req)
}

func TestScopePreflight_FlagScopeReachesTheVerdict(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"get", "dashboards"})
	require.NoError(t, err)

	// A token with the read scope only is sufficient for get dashboards...
	withScopeState(t, true, true, "json", []string{"document:documents:read"}, true)
	out := captureScopeStdout(t, func() {
		skip, preErr := scopePreflight(cmd, nil)
		require.True(t, skip)
		require.NoError(t, preErr)
	})
	require.Contains(t, out, `"status": "ok"`)

	// ...and insufficient for the same command with --admin-access.
	withFlagSet(t, cmd, "admin-access", "true")
	skip, preErr := scopePreflight(cmd, nil)
	require.False(t, skip)
	var scopeErr *ScopeError
	require.ErrorAs(t, preErr, &scopeErr)
	require.Equal(t, []string{"document:documents:admin"}, scopeErr.Missing)
}

func TestFlagScopeRequirementsAreWellFormed(t *testing.T) {
	for key, byFlag := range flagScopeRequirements {
		path := strings.Fields(key)
		cmd, _, err := rootCmd.Find(path)
		require.NoError(t, err, "key %q does not resolve to a command", key)
		require.Equal(t, "dtctl "+key, cmd.CommandPath(),
			"key %q must name the leaf command exactly, not an alias or a parent", key)

		// scopesForInvocation only augments a catalog-known requirement, so an
		// entry on a command without one would be silently dropped.
		verb, resource := verbResource(cmd)
		_, req := requiredScopesFor(verb, resource)
		require.Equal(t, scopeRequirementKnown, req,
			"%q has no catalog scopes, so a flag scope on it would never be reported", key)

		for flagKey, scopes := range byFlag {
			name, _, _ := strings.Cut(flagKey, "=")
			require.NotNil(t, cmd.Flags().Lookup(name),
				"%q has no flag %q — the flag was renamed or removed", key, name)
			require.NotEmpty(t, scopes, "%q flag %q lists no scopes", key, name)
		}
	}
}

// TestFlagScopesAreRequestedAtLogin guards #375: `--admin-access` needed
// document:documents:admin, the preflight knew it, and yet no safety level asked
// for it at `dtctl auth login` — so an OAuth session could never carry it, however
// the user's IAM policy was set up. A flag scope that no login requests is a flag
// that cannot work with OAuth at all.
func TestFlagScopesAreRequestedAtLogin(t *testing.T) {
	requested := map[string]bool{}
	for _, level := range []config.SafetyLevel{
		config.SafetyLevelReadOnly,
		config.SafetyLevelReadWriteMine,
		config.SafetyLevelReadWriteAll,
		config.SafetyLevelDangerouslyUnrestricted,
	} {
		for _, s := range auth.GetScopesForSafetyLevel(level) {
			requested[s] = true
		}
	}
	for key, byFlag := range flagScopeRequirements {
		for name, scopes := range byFlag {
			for _, scope := range scopes {
				require.True(t, requested[scope],
					"%q --%s needs %q, but no safety level requests it at login", key, name, scope)
			}
		}
	}
}
