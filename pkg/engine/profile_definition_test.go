package engine_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/engine"
)

// hostProfile is a host-defined allowlist, as an embedding service keeps on
// its own side of the engine.
func hostProfile() engine.ProfileDefinition {
	return engine.ProfileDefinition{
		Name:        "host-agent",
		Description: "synthetic host allowlist",
		Commands:    []string{"query", "get analyzers"},
	}
}

// hostEngines are the two ways a host runs the engine — one invocation at a
// time on the shared command tree, and concurrently on a tree per invocation —
// both under def.
func hostEngines(def engine.ProfileDefinition) map[string]*engine.Engine {
	return map[string]*engine.Engine{
		"serialized": engine.New(engine.Limits{}, engine.WithProfileDefinition(def)),
		"concurrent": engine.New(engine.Limits{}, engine.WithProfileDefinition(def), engine.WithConcurrentExecution(2)),
	}
}

// TestProfileDefinition_BlocksOutsideCommand: a command outside a host-defined
// profile is refused with profile_blocked, nothing reaches the platform, and
// the error does not tell the caller to unset DTCTL_PROFILE: the host pinned
// the profile, so that is not something the caller can do.
func TestProfileDefinition_BlocksOutsideCommand(t *testing.T) {
	for name, e := range hostEngines(hostProfile()) {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(srv.Close)

			res, err := e.Execute(context.Background(), engine.Request{
				Command:        "get buckets --agent",
				EnvironmentURL: srv.URL,
				Token:          "t",
			})
			require.NoError(t, err)
			require.NotZero(t, res.ExitCode)
			out := string(res.Stdout) + string(res.Stderr)
			require.Contains(t, out, "profile_blocked")
			require.Contains(t, out, "host-agent")
			require.NotContains(t, out, "unset")
			require.Zero(t, calls.Load(), "a blocked command must not call the platform")
		})
	}
}

// TestProfileDefinition_ShapesTheCatalog: `dtctl commands` lists exactly what
// the definition allows, and names the profile.
func TestProfileDefinition_ShapesTheCatalog(t *testing.T) {
	for name, e := range hostEngines(hostProfile()) {
		t.Run(name, func(t *testing.T) {
			res, err := e.Execute(context.Background(), engine.Request{
				Command:        "commands -o json",
				EnvironmentURL: unroutable,
				Token:          "t",
			})
			require.NoError(t, err)
			require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)

			var catalog struct {
				Profile string `json:"profile"`
				Verbs   map[string]struct {
					Resources []string `json:"resources"`
				} `json:"verbs"`
			}
			require.NoError(t, json.Unmarshal(res.Stdout, &catalog), "stdout: %s", res.Stdout)
			require.Equal(t, "host-agent", catalog.Profile)

			verbs := map[string][]string{}
			for name, v := range catalog.Verbs {
				verbs[name] = v.Resources
			}
			require.Equal(t, map[string][]string{"get": {"analyzers"}, "query": nil}, verbs)
		})
	}
}

// TestProfileDefinition_ParseResolvesAliases: Parse runs under the engine's
// profile too. An alias of an allowed command is the allowed command, and an
// alias of a masked one stays masked.
func TestProfileDefinition_ParseResolvesAliases(t *testing.T) {
	e := engine.New(engine.Limits{}, engine.WithProfileDefinition(hostProfile()))

	got, err := e.Parse(context.Background(), engine.ParseRequest{Command: "get az"})
	require.NoError(t, err)
	require.Equal(t, "get", got.Verb)
	require.Equal(t, "analyzers", got.Resource)

	_, err = e.Parse(context.Background(), engine.ParseRequest{Command: "get wf"})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, "profile_blocked", ue.Code)
}

// TestProfileDefinition_RequestCannotReplaceIt: a request that names another
// profile — by preset, or by a DTCTL_PROFILE entry in Env, "full" included —
// is a host bug, refused before anything runs. Naming the engine's own profile
// in Env is harmless.
func TestProfileDefinition_RequestCannotReplaceIt(t *testing.T) {
	e := engine.New(engine.Limits{}, engine.WithProfileDefinition(hostProfile()))

	for name, tc := range map[string]struct {
		preset string
		env    map[string]string
	}{
		"preset":           {preset: "query"},
		"env full":         {env: map[string]string{"DTCTL_PROFILE": "full"}},
		"env other preset": {env: map[string]string{"DTCTL_PROFILE": "investigate"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.Parse(context.Background(), engine.ParseRequest{
				Command: "get buckets", Profile: tc.preset, Env: tc.env,
			})
			require.ErrorContains(t, err, "host-agent")
			var ue *engine.UsageError
			require.NotErrorAs(t, err, &ue)

			res, err := e.Execute(context.Background(), engine.Request{
				Command: "get buckets", EnvironmentURL: unroutable, Token: "t",
				Profile: tc.preset, Env: tc.env,
			})
			require.ErrorContains(t, err, "host-agent")
			require.Nil(t, res)
		})
	}

	_, err := e.Parse(context.Background(), engine.ParseRequest{
		Command: "get buckets",
		Env:     map[string]string{"DTCTL_PROFILE": "host-agent"},
	})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, "profile_blocked", ue.Code)
}

// TestProfileDefinition_IsCopied: the engine keeps its own copy, so a host
// that changes its definition afterwards changes nothing the engine runs.
func TestProfileDefinition_IsCopied(t *testing.T) {
	def := hostProfile()
	e := engine.New(engine.Limits{}, engine.WithProfileDefinition(def))
	def.Name = "changed"
	def.Commands[1] = "get workflows"

	got, err := e.Parse(context.Background(), engine.ParseRequest{Command: "get analyzers"})
	require.NoError(t, err)
	require.Equal(t, "analyzers", got.Resource)

	_, err = e.Parse(context.Background(), engine.ParseRequest{Command: "get workflows"})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Contains(t, ue.Message, "host-agent")
}

// TestProfileDefinition_NormalizesEntries: an entry with stray whitespace
// still names the command it reads as.
func TestProfileDefinition_NormalizesEntries(t *testing.T) {
	e := engine.New(engine.Limits{}, engine.WithProfileDefinition(
		engine.ProfileDefinition{Name: "p", Commands: []string{"  get   analyzers "}}))
	_, err := e.Parse(context.Background(), engine.ParseRequest{Command: "get analyzers"})
	require.NoError(t, err)
}

// TestProfileDefinition_InvalidIsRejected: a definition that would resolve to
// some other surface than the one written down fails Validate, and an engine
// built with it runs nothing: every Execute and Parse returns the validation
// error (never a *UsageError, since it is not the caller's mistake) rather than
// running without a profile.
func TestProfileDefinition_InvalidIsRejected(t *testing.T) {
	for name, def := range map[string]engine.ProfileDefinition{
		"no name":         {Commands: []string{"query"}},
		"reserved full":   {Name: "full", Commands: []string{"query"}},
		"name with space": {Name: "host agent"},
		"blank entry":     {Name: "p", Commands: []string{"query", "  "}},
		"flag token":      {Name: "p", Commands: []string{"query --dql"}},
		"leading dtctl":   {Name: "p", Commands: []string{"dtctl query"}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, def.Validate())

			e := engine.New(engine.Limits{}, engine.WithProfileDefinition(def))
			res, err := e.Execute(context.Background(), engine.Request{
				Command:        "query 'fetch logs'",
				EnvironmentURL: unroutable,
				Token:          "t",
			})
			require.EqualError(t, err, def.Validate().Error())
			require.Nil(t, res)

			_, err = e.Parse(context.Background(), engine.ParseRequest{Command: "query 'fetch logs'"})
			require.EqualError(t, err, def.Validate().Error())
			var ue *engine.UsageError
			require.NotErrorAs(t, err, &ue)
		})
	}

	require.NoError(t, hostProfile().Validate())
}

// TestProfileDefinition_EmptyAllowlistKeepsTheCatalog: an empty list is valid
// and leaves only the always-available discovery commands.
func TestProfileDefinition_EmptyAllowlistKeepsTheCatalog(t *testing.T) {
	e := engine.New(engine.Limits{}, engine.WithProfileDefinition(engine.ProfileDefinition{Name: "nothing"}))

	_, err := e.Parse(context.Background(), engine.ParseRequest{Command: "commands"})
	require.NoError(t, err)

	_, err = e.Parse(context.Background(), engine.ParseRequest{Command: "query 'fetch logs'"})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, "profile_blocked", ue.Code)
}
