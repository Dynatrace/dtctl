package engine_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/engine"
)

// recordingEnv answers every call with {} and records it as "METHOD /path".
type recordingEnv struct {
	*httptest.Server
	mu    sync.Mutex
	calls []string
}

func newRecordingEnv(t *testing.T) *recordingEnv {
	t.Helper()
	env := &recordingEnv{}
	env.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.mu.Lock()
		env.calls = append(env.calls, r.Method+" "+r.URL.Path)
		env.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(env.Close)
	return env
}

func (e *recordingEnv) received() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

// dashboardManifest is a dashboard that `apply -f` creates with a POST to the
// document API.
var dashboardManifest = map[string][]byte{
	"d.yaml": []byte("name: d\ntype: dashboard\ncontent:\n  tiles: {}\n  version: \"1\"\n"),
}

// runUnder runs command under def at readwrite-all, so only the profile can
// stop it.
func runUnder(t *testing.T, def engine.ProfileDefinition, command string) (*engine.Result, []string) {
	t.Helper()
	env := newRecordingEnv(t)
	res, err := engine.New(engine.Limits{}, engine.WithProfileDefinition(def)).Execute(context.Background(), engine.Request{
		Command:        command,
		EnvironmentURL: env.URL,
		Token:          "t",
		SafetyLevel:    "readwrite-all",
		Files:          dashboardManifest,
	})
	require.NoError(t, err)
	return res, env.received()
}

// TestProfileDefinition_AncestorRunsNoBody: a parent allowed only to reach an
// entry is blocked before any platform call, whatever its arguments. Its help
// still works.
func TestProfileDefinition_AncestorRunsNoBody(t *testing.T) {
	for _, tc := range []struct {
		entry, parent string
		runs          []string
	}{
		{"apply extension-config", "apply", []string{"apply -f d.yaml", "apply"}},
		{"exec copilot nl2dql", "exec copilot", []string{`exec copilot "list my hosts"`, "exec copilot"}},
		{"exec copilot dql2nl", "exec copilot", []string{`exec copilot "list my hosts"`}},
		{"exec copilot document-search", "exec copilot", []string{`exec copilot "list my hosts"`}},
		{"get gcp connections principal", "get gcp connections", []string{"get gcp connections", "get gcp connections my-connection"}},
	} {
		def := engine.ProfileDefinition{Name: "p", Commands: []string{tc.entry}}
		for _, command := range tc.runs {
			t.Run(tc.entry+"/"+command, func(t *testing.T) {
				res, calls := runUnder(t, def, command+" --agent")
				out := string(res.Stdout) + string(res.Stderr)
				require.NotZero(t, res.ExitCode, "output: %s", out)
				require.Contains(t, out, "profile_blocked")
				require.Contains(t, out, `command \"`+tc.parent+`\" is not available in profile \"p\"`)
				require.Empty(t, calls, "the blocked command reached the platform")
			})
		}
		for _, command := range []string{tc.parent + " --help", "help " + tc.parent} {
			t.Run(tc.entry+"/"+command, func(t *testing.T) {
				res, calls := runUnder(t, def, command)
				require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
				require.Contains(t, string(res.Stdout), strings.Fields(tc.entry)[len(strings.Fields(tc.parent))],
					"the help lists the allowed subcommand")
				require.Empty(t, calls)
			})
		}
	}
}

// TestProfileDefinition_AncestorKeepsItsEntriesReachable: the entry below such
// a parent still runs and stays in the catalog.
func TestProfileDefinition_AncestorKeepsItsEntriesReachable(t *testing.T) {
	def := engine.ProfileDefinition{Name: "p", Commands: []string{"get gcp connections principal", "exec copilot nl2dql"}}

	for _, command := range []string{"get gcp connections principal", `exec copilot nl2dql "list my hosts"`} {
		t.Run(command, func(t *testing.T) {
			res, calls := runUnder(t, def, command+" --agent")
			require.NotContains(t, string(res.Stdout)+string(res.Stderr), "profile_blocked")
			require.NotEmpty(t, calls, "the allowed entry did not reach the platform")
		})
	}

	res, _ := runUnder(t, def, "commands -o json")
	require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
	require.Contains(t, string(res.Stdout), `"principal"`)
	require.Contains(t, string(res.Stdout), `"nl2dql"`)
}

// TestProfileDefinition_ListedParentRunsItsBody: a listed parent still runs,
// whether or not its subcommand is listed too.
func TestProfileDefinition_ListedParentRunsItsBody(t *testing.T) {
	for _, commands := range [][]string{
		{"apply", "apply extension-config"},
		{"apply"},
	} {
		t.Run(strings.Join(commands, ","), func(t *testing.T) {
			res, calls := runUnder(t, engine.ProfileDefinition{Name: "p", Commands: commands}, "apply -f d.yaml --agent")
			require.NotContains(t, string(res.Stdout)+string(res.Stderr), "profile_blocked")
			require.Contains(t, calls, "POST /platform/document/v1/documents")
		})
	}
}

// TestProfileDefinition_PureGroupIsUnchanged: a group that only refuses a
// missing subcommand behaves as it does without a profile.
func TestProfileDefinition_PureGroupIsUnchanged(t *testing.T) {
	res, calls := runUnder(t, engine.ProfileDefinition{Name: "p", Commands: []string{"get gcp connections principal"}}, "get gcp --agent")
	out := string(res.Stdout) + string(res.Stderr)
	require.NotZero(t, res.ExitCode)
	require.NotContains(t, out, "profile_blocked")
	require.Contains(t, out, "requires a resource type")
	require.Empty(t, calls)
}

// TestProfileDefinition_ParseRefusesAncestorBody: Parse blocks the same parent
// Execute does.
func TestProfileDefinition_ParseRefusesAncestorBody(t *testing.T) {
	e := engine.New(engine.Limits{}, engine.WithProfileDefinition(
		engine.ProfileDefinition{Name: "p", Commands: []string{"apply extension-config"}}))

	_, err := e.Parse(context.Background(), engine.ParseRequest{Command: "apply -f d.yaml"})
	var ue *engine.UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, "profile_blocked", ue.Code)

	got, err := e.Parse(context.Background(), engine.ParseRequest{Command: "apply extension-config my-ext -f c.yaml"})
	require.NoError(t, err)
	require.Equal(t, "apply", got.Verb)
	require.Equal(t, "extension-config", got.Resource)
}

// TestProfileDefinition_CatalogDropsParentArguments: the catalog does not list
// arguments for a blocked parent. A run without the profile lists them again.
func TestProfileDefinition_CatalogDropsParentArguments(t *testing.T) {
	requiredArgs := func(t *testing.T, e *engine.Engine) []string {
		t.Helper()
		res, err := e.Execute(context.Background(), engine.Request{
			Command: "commands --full -o json", EnvironmentURL: unroutable, Token: "t",
		})
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
		var catalog struct {
			Verbs map[string]struct {
				RequiredArgs []string `json:"required_args"`
			} `json:"verbs"`
		}
		require.NoError(t, json.Unmarshal(res.Stdout, &catalog), "stdout: %s", res.Stdout)
		return catalog.Verbs["apply"].RequiredArgs
	}

	for name, e := range hostEngines(engine.ProfileDefinition{Name: "p", Commands: []string{"apply extension-config"}}) {
		t.Run(name, func(t *testing.T) {
			require.Empty(t, requiredArgs(t, e))
			require.Equal(t, []string{"file"}, requiredArgs(t, engine.New(engine.Limits{})))
		})
	}
}
