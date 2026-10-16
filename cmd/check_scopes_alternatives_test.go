package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/client"
)

// Endpoints that accept "one of the following scopes" (the Platform Management
// API: app-engine:apps:run | app-engine:functions:run |
// platform-management:environments:read) must pass the check for a token that
// holds only one of the alternatives, and still fail one that holds none.
func TestComputeScopeVerdict_Alternatives(t *testing.T) {
	required := []string{"app-engine:apps:run"}
	alternatives := [][]string{{"app-engine:functions:run"}, {"platform-management:environments:read"}}

	t.Run("only an alternative held is sufficient", func(t *testing.T) {
		withScopeState(t, true, false, "json", []string{"platform-management:environments:read"}, true)
		r := computeScopeVerdict(context.Background(), "get", "license", required, alternatives, scopeRequirementKnown)
		require.Equal(t, scopeStatusOK, r.Status)
		require.Empty(t, r.MissingScopes)
		require.Equal(t, []string{"platform-management:environments:read"}, r.SatisfiedBy)
		require.Equal(t, required, r.RequiredScopes, "the listed requirement is still reported")
		require.Equal(t, alternatives, r.AlternativeScopes)
	})

	t.Run("the listed scope held needs no alternative", func(t *testing.T) {
		withScopeState(t, true, false, "json", []string{"app-engine:apps:run"}, true)
		r := computeScopeVerdict(context.Background(), "get", "license", required, alternatives, scopeRequirementKnown)
		require.Equal(t, scopeStatusOK, r.Status)
		require.Empty(t, r.SatisfiedBy)
	})

	t.Run("none held is insufficient", func(t *testing.T) {
		withScopeState(t, true, false, "json", []string{"automation:workflows:read"}, true)
		r := computeScopeVerdict(context.Background(), "get", "license", required, alternatives, scopeRequirementKnown)
		require.Equal(t, scopeStatusInsufficient, r.Status)
		require.Equal(t, required, r.MissingScopes)
		require.Empty(t, r.SatisfiedBy)
		require.Contains(t, strings.Join(r.Suggestions, "\n"), "platform-management:environments:read",
			"the advice must name the alternatives, not only the listed scope")
	})

	t.Run("an alternative is all-of", func(t *testing.T) {
		withScopeState(t, true, false, "json", []string{"b"}, true)
		r := computeScopeVerdict(context.Background(), "get", "x", []string{"a"}, [][]string{{"b", "c"}}, scopeRequirementKnown)
		require.Equal(t, scopeStatusInsufficient, r.Status, "a partially held alternative satisfies nothing")
	})

	t.Run("opaque token names the alternatives", func(t *testing.T) {
		withScopeState(t, true, false, "json", nil, false)
		r := computeScopeVerdict(context.Background(), "get", "license", required, alternatives, scopeRequirementKnown)
		require.Equal(t, scopeStatusUnknown, r.Status)
		require.Contains(t, strings.Join(r.Suggestions, "\n"), "app-engine:functions:run")
	})
}

// The alternatives must reach the verdict through the real command tree, for
// every Platform Management command, not only through computeScopeVerdict.
func TestScopePreflight_CheckScopes_AcceptsAlternativeScope(t *testing.T) {
	for _, path := range [][]string{
		{"get", "environment"},
		{"describe", "environment"},
		{"get", "license"},
		{"describe", "license"},
		{"get", "license-settings"},
	} {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			cmd, _, err := rootCmd.Find(path)
			require.NoError(t, err)

			t.Run("alternative held", func(t *testing.T) {
				withScopeState(t, true, false, "json", []string{"platform-management:environments:read"}, true)
				var skip bool
				var preErr error
				out := captureScopeStdout(t, func() { skip, preErr = scopePreflight(cmd, nil) })
				require.True(t, skip)
				require.NoError(t, preErr, "a token the endpoint accepts must not fail the check")

				var res ScopeCheckResult
				require.NoError(t, json.Unmarshal([]byte(out), &res))
				require.Equal(t, scopeStatusOK, res.Status)
				require.Equal(t, []string{"platform-management:environments:read"}, res.SatisfiedBy)
			})

			t.Run("none held", func(t *testing.T) {
				withScopeState(t, true, false, "json", []string{"automation:workflows:read"}, true)
				var preErr error
				_ = captureScopeStdout(t, func() { _, preErr = scopePreflight(cmd, nil) })
				var silent *silentExitError
				require.ErrorAs(t, preErr, &silent)
				require.Equal(t, client.ExitPermissionError, silent.code)
			})

			t.Run("agent mode, none held", func(t *testing.T) {
				withScopeState(t, true, true, "json", []string{"automation:workflows:read"}, true)
				skip, preErr := scopePreflight(cmd, nil)
				require.False(t, skip)
				var scopeErr *ScopeError
				require.ErrorAs(t, preErr, &scopeErr)

				detail := errorToDetail(context.Background(), preErr)
				require.Equal(t, "insufficient_scope", detail.Code)
				require.Equal(t, []string{"app-engine:apps:run"}, detail.MissingScopes)
				require.Equal(t,
					[][]string{{"app-engine:functions:run"}, {"platform-management:environments:read"}},
					detail.AlternativeScopes, "the error envelope must carry the accepted alternatives")
				require.Contains(t, strings.Join(detail.Suggestions, "\n"), "platform-management:environments:read")
				require.Equal(t, client.ExitPermissionError, exitCodeForError(preErr))
			})

			t.Run("agent mode, alternative held", func(t *testing.T) {
				withScopeState(t, true, true, "json", []string{"app-engine:functions:run"}, true)
				var preErr error
				_ = captureScopeStdout(t, func() { _, preErr = scopePreflight(cmd, nil) })
				require.NoError(t, preErr)
			})
		})
	}
}

// The agent-mode auto-preflight builds its ScopeError itself; whatever
// alternatives it found must reach the envelope, and a command without any must
// not grow the field.
func TestScopeErrorEnvelopeCarriesAlternatives(t *testing.T) {
	alts := [][]string{{"b"}, {"c", "d"}}
	detail := errorToDetail(context.Background(), &ScopeError{Verb: "delete", Resource: "x", Required: []string{"a"},
		Missing: []string{"a"}, Alternatives: alts, Advice: insufficientScopeAdvice([]string{"a"}, alts)})
	require.Equal(t, alts, detail.AlternativeScopes)
	require.Contains(t, strings.Join(detail.Suggestions, "\n"), "b | c + d")

	plain := errorToDetail(context.Background(), &ScopeError{Verb: "delete", Resource: "x", Required: []string{"a"}, Missing: []string{"a"}})
	require.Nil(t, plain.AlternativeScopes)
}

// A resource without alternatives keeps its all-of requirement.
func TestAlternativesForInvocation_NoneForPlainResource(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"delete", "workflow"})
	require.NoError(t, err)
	require.Nil(t, alternativesForInvocation(cmd, "delete", "workflow"))
}
