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

// hostProfile is a host-defined allowlist, as a service like dt-control keeps
// on its own side of the engine.
func hostProfile() *engine.ProfileDefinition {
	return &engine.ProfileDefinition{
		Name:        "host-agent",
		Description: "synthetic host allowlist",
		Commands:    []string{"query", "get analyzers"},
	}
}

// TestExecute_ProfileDefinitionBlocksOutsideCommand: a command outside a
// host-defined profile is refused with profile_blocked, and nothing reaches
// the platform.
func TestExecute_ProfileDefinitionBlocksOutsideCommand(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:           "get buckets --agent",
		EnvironmentURL:    srv.URL,
		Token:             "t",
		ProfileDefinition: hostProfile(),
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	require.Contains(t, string(res.Stdout)+string(res.Stderr), "profile_blocked")
	require.Contains(t, string(res.Stdout)+string(res.Stderr), "host-agent")
	require.Zero(t, calls.Load(), "a blocked command must not call the platform")
}

// TestExecute_ProfileDefinitionShapesTheCatalog: `dtctl commands` lists
// exactly what the definition allows, and names the profile.
func TestExecute_ProfileDefinitionShapesTheCatalog(t *testing.T) {
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:           "commands -o json",
		EnvironmentURL:    unroutable,
		Token:             "t",
		ProfileDefinition: hostProfile(),
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
}

// TestParse_ProfileDefinitionResolvesAliases: an alias of an allowed command
// is the allowed command, and an alias of a masked one stays masked.
func TestParse_ProfileDefinitionResolvesAliases(t *testing.T) {
	got, err := engine.Parse(context.Background(), engine.ParseRequest{
		Command:           "get az",
		ProfileDefinition: hostProfile(),
	})
	require.NoError(t, err)
	require.Equal(t, "get", got.Verb)
	require.Equal(t, "analyzers", got.Resource)

	_, err = engine.Parse(context.Background(), engine.ParseRequest{
		Command:           "get wf",
		ProfileDefinition: hostProfile(),
	})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, "profile_blocked", ue.Code)
}

// TestParse_ProfileDefinitionWinsOverEnv: a DTCTL_PROFILE entry in Env cannot
// select a different profile — in particular not "full" — in place of the
// definition.
func TestParse_ProfileDefinitionWinsOverEnv(t *testing.T) {
	for _, env := range []string{"full", "investigate"} {
		_, err := engine.Parse(context.Background(), engine.ParseRequest{
			Command:           "get buckets",
			ProfileDefinition: hostProfile(),
			Env:               map[string]string{"DTCTL_PROFILE": env},
		})
		var ue *engine.UsageError
		require.ErrorAs(t, err, &ue, "DTCTL_PROFILE=%s", env)
		require.Equal(t, "profile_blocked", ue.Code)
	}
}

// TestParse_ProfileDefinitionNormalizesEntries: an entry with stray
// whitespace still names the command it reads as.
func TestParse_ProfileDefinitionNormalizesEntries(t *testing.T) {
	_, err := engine.Parse(context.Background(), engine.ParseRequest{
		Command:           "get analyzers",
		ProfileDefinition: &engine.ProfileDefinition{Name: "p", Commands: []string{"  get   analyzers "}},
	})
	require.NoError(t, err)
}

// TestProfileDefinition_InvalidIsRejected: a definition that would resolve to
// some other surface than the one written down is a Go error before anything
// runs, for Execute and Parse alike.
func TestProfileDefinition_InvalidIsRejected(t *testing.T) {
	for name, tc := range map[string]struct {
		preset string
		def    *engine.ProfileDefinition
	}{
		"no name":         {def: &engine.ProfileDefinition{Commands: []string{"query"}}},
		"reserved full":   {def: &engine.ProfileDefinition{Name: "full", Commands: []string{"query"}}},
		"name with space": {def: &engine.ProfileDefinition{Name: "host agent"}},
		"blank entry":     {def: &engine.ProfileDefinition{Name: "p", Commands: []string{"query", "  "}}},
		"preset and def":  {preset: "query", def: hostProfile()},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := engine.Execute(context.Background(), engine.Request{
				Command:           "query 'fetch logs'",
				EnvironmentURL:    unroutable,
				Token:             "t",
				Profile:           tc.preset,
				ProfileDefinition: tc.def,
			})
			require.Error(t, err)
			require.Nil(t, res)

			_, err = engine.Parse(context.Background(), engine.ParseRequest{
				Command:           "query 'fetch logs'",
				Profile:           tc.preset,
				ProfileDefinition: tc.def,
			})
			require.Error(t, err)
			var ue *engine.UsageError
			require.NotErrorAs(t, err, &ue)
		})
	}
}

// TestProfileDefinition_EmptyAllowlistKeepsTheCatalog: an empty list is valid
// and leaves only the always-available discovery commands.
func TestProfileDefinition_EmptyAllowlistKeepsTheCatalog(t *testing.T) {
	def := &engine.ProfileDefinition{Name: "nothing"}
	_, err := engine.Parse(context.Background(), engine.ParseRequest{Command: "commands", ProfileDefinition: def})
	require.NoError(t, err)

	_, err = engine.Parse(context.Background(), engine.ParseRequest{Command: "query 'fetch logs'", ProfileDefinition: def})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, "profile_blocked", ue.Code)
}
