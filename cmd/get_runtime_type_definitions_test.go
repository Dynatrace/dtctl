package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/cmd/testutil"
)

func TestGetRuntimeTypeDefinitions_RequiredFlags(t *testing.T) {
	c := newGetRuntimeTypeDefinitionsCmd()
	for _, name := range []string{"package-name", "major-version"} {
		f := c.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("expected --%s flag", name)
		}
		if _, ok := f.Annotations["cobra_annotation_bash_completion_one_required_flag"]; !ok {
			t.Errorf("--%s must be marked required", name)
		}
	}
}

func TestGetRuntimeTypeDefinitions_InvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"blank package", []string{"--package-name", " ", "--major-version", "1"}, "--package-name must not be empty"},
		{"negative version", []string{"--package-name", "@x/y", "--major-version", "-1"}, "--major-version must be a non-negative integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newGetRuntimeTypeDefinitionsCmd()
			c.SetArgs(tc.args)
			c.SilenceUsage, c.SilenceErrors = true, true
			err := c.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func runRuntimeTypeDefinitions(t *testing.T, format string, agent bool) string {
	t.Helper()
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/app-engine/function-executor/v1/runtime-type-definitions": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("packageName") != "@x/y" || r.URL.Query().Get("majorVersion") != "2" {
				t.Errorf("unexpected query: %v", r.URL.Query())
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"packageName": "@x/y", "version": "2.1.0", "fileName": "y.d.ts",
				"typeDefinitions": "declare module '@x/y' {\n  export const a: number;\n}",
			})
		},
	})
	defer ms.Close()
	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfg, origFmt, origPlain, origAgent := gFlags.cfgFile, gFlags.outputFormat, gFlags.plainMode, gFlags.agentMode
	defer func() {
		gFlags.cfgFile, gFlags.outputFormat, gFlags.plainMode, gFlags.agentMode = origCfg, origFmt, origPlain, origAgent
	}()
	gFlags.cfgFile, gFlags.outputFormat, gFlags.plainMode, gFlags.agentMode = configPath, format, true, agent

	c := newGetRuntimeTypeDefinitionsCmd()
	var out bytes.Buffer
	c.SetOut(&out)
	_ = c.Flags().Set("package-name", "@x/y")
	_ = c.Flags().Set("major-version", "2")
	// The raw-text view writes to the command's writer; the printers write to
	// the process stdout. Collect both.
	stdout := captureStdout(t, func() {
		if err := c.RunE(c, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	return out.String() + stdout
}

// The default view must be the declaration itself, not a summary table.
func TestGetRuntimeTypeDefinitions_DefaultPrintsDeclaration(t *testing.T) {
	want := "declare module '@x/y' {\n  export const a: number;\n}\n"
	for _, format := range []string{"table", ""} {
		if got := runRuntimeTypeDefinitions(t, format, false); got != want {
			t.Errorf("format %q: got %q, want %q", format, got, want)
		}
	}
}

// Agent mode leaves the format at "table"; it must still get the envelope, not raw text.
func TestGetRuntimeTypeDefinitions_AgentModeKeepsEnvelope(t *testing.T) {
	var got struct {
		OK     bool                   `json:"ok"`
		Result map[string]interface{} `json:"result"`
	}
	out := runRuntimeTypeDefinitions(t, "table", true)
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("agent output is not JSON: %v\n%s", err, out)
	}
	if !got.OK || got.Result["version"] != "2.1.0" {
		t.Errorf("unexpected envelope: %s", out)
	}
}

func TestGetRuntimeTypeDefinitions_JSONHasFullResponse(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal([]byte(runRuntimeTypeDefinitions(t, "json", false)), &got); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if got["version"] != "2.1.0" || got["fileName"] != "y.d.ts" || got["typeDefinitions"] == "" {
		t.Errorf("unexpected JSON: %v", got)
	}
}
