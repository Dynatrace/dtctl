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

// hostProfile is a synthetic host allowlist.
func hostProfile() engine.ProfileDefinition {
	return engine.ProfileDefinition{
		Name:        "host-agent",
		Description: "synthetic host allowlist",
		Commands:    []string{"query", "get analyzers"},
	}
}

// hostEngines are a serialized and a concurrent engine, both under def.
func hostEngines(def engine.ProfileDefinition) map[string]*engine.Engine {
	return map[string]*engine.Engine{
		"serialized": engine.New(engine.Limits{}, engine.WithProfileDefinition(def)),
		"concurrent": engine.New(engine.Limits{}, engine.WithProfileDefinition(def), engine.WithConcurrentExecution(2)),
	}
}

// TestProfileDefinition_BlocksOutsideCommand: a command outside the profile,
// run or asked for help, is refused with profile_blocked before any platform
// call, without suggesting to unset DTCTL_PROFILE, which the caller cannot do.
func TestProfileDefinition_BlocksOutsideCommand(t *testing.T) {
	for name, e := range hostEngines(hostProfile()) {
		for _, command := range []string{"get buckets", "help get buckets"} {
			t.Run(name+"/"+command, func(t *testing.T) {
				var calls atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(srv.Close)

				res, err := e.Execute(context.Background(), engine.Request{
					Command:        command + " --agent",
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
}

// TestProfileDefinition_ShapesTheCatalog: `dtctl commands`, compact and full,
// lists exactly what the definition allows and names the profile.
func TestProfileDefinition_ShapesTheCatalog(t *testing.T) {
	for name, e := range hostEngines(hostProfile()) {
		for _, command := range []string{"commands -o json", "commands --full -o json"} {
			t.Run(name+"/"+command, func(t *testing.T) {
				res, err := e.Execute(context.Background(), engine.Request{
					Command:        command,
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
}

// TestProfileDefinition_ParseResolvesAliases: Parse uses the profile too, and
// an alias is allowed or masked like the command it stands for.
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

// TestProfileDefinition_RequestCannotReplaceIt: a request naming another
// profile, by preset or in Env, is an error; naming the engine's own is not.
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

// TestProfileDefinition_IsCopied: changing the definition after New has no
// effect.
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

// TestProfileDefinition_NormalizesEntries: stray whitespace in an entry is
// ignored.
func TestProfileDefinition_NormalizesEntries(t *testing.T) {
	e := engine.New(engine.Limits{}, engine.WithProfileDefinition(
		engine.ProfileDefinition{Name: "p", Commands: []string{"  get   analyzers "}}))
	_, err := e.Parse(context.Background(), engine.ParseRequest{Command: "get analyzers"})
	require.NoError(t, err)
}

// TestProfileDefinition_InvalidIsRejected: an invalid definition fails
// Validate, and every Execute and Parse of its engine returns that error (not
// a *UsageError: it is not the caller's mistake).
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
// and leaves only the always-available commands.
func TestProfileDefinition_EmptyAllowlistKeepsTheCatalog(t *testing.T) {
	e := engine.New(engine.Limits{}, engine.WithProfileDefinition(engine.ProfileDefinition{Name: "nothing"}))

	_, err := e.Parse(context.Background(), engine.ParseRequest{Command: "commands"})
	require.NoError(t, err)

	_, err = e.Parse(context.Background(), engine.ParseRequest{Command: "query 'fetch logs'"})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, "profile_blocked", ue.Code)
}

// TestPresetProfileKeepsTheUnsetHint: a preset is not pinned, so its error
// still suggests unsetting DTCTL_PROFILE.
func TestPresetProfileKeepsTheUnsetHint(t *testing.T) {
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "get buckets --agent",
		EnvironmentURL: unroutable,
		Token:          "t",
		Profile:        "query",
	})
	require.NoError(t, err)
	out := string(res.Stdout) + string(res.Stderr)
	require.Contains(t, out, "profile_blocked")
	require.Contains(t, out, "unset DTCTL_PROFILE")

	_, err = engine.Parse(context.Background(), engine.ParseRequest{Command: "get buckets", Profile: "query"})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Contains(t, ue.Suggestions, "unset DTCTL_PROFILE to use the full CLI")
}
