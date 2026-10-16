package cmd

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
)

const classicPipelinesTranslateEndpoint = "/platform/openpipeline/v1/classic-pipelines/translate"

// captureStdout (defined in breakpoint_output_test.go) runs a function with
// os.Stdout redirected to a pipe and returns what was written.

func TestTranslateClassicPipelinesCmd_Success(t *testing.T) {
	var gotConfiguration string
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		classicPipelinesTranslateEndpoint: func(w http.ResponseWriter, r *http.Request) {
			gotConfiguration = r.URL.Query().Get("configuration")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":{"id":"pipe-1"},"withWarning":false}`))
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	origAgent := agentMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
		gFlags.agentMode = origAgent
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true
	gFlags.agentMode = false

	testutil.ResetCommandFlags(translateClassicPipelinesCmd)

	if err := translateClassicPipelinesCmd.RunE(translateClassicPipelinesCmd, []string{"logs"}); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if gotConfiguration != "logs" {
		t.Errorf("configuration sent = %q, want %q", gotConfiguration, "logs")
	}
}

func TestTranslateClassicPipelinesCmd_PassesFlags(t *testing.T) {
	gotParams := map[string]string{}
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		classicPipelinesTranslateEndpoint: func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			for _, p := range []string{"configuration", "includeSampleData", "skipDisabledRules", "skipBuiltinProcessingRules"} {
				gotParams[p] = q.Get(p)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":{},"withWarning":false}`))
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true

	testutil.ResetCommandFlags(translateClassicPipelinesCmd)
	_ = translateClassicPipelinesCmd.Flags().Set("include-sample-data", "true")
	_ = translateClassicPipelinesCmd.Flags().Set("skip-disabled-rules", "false")
	_ = translateClassicPipelinesCmd.Flags().Set("skip-builtin-processing-rules", "true")

	if err := translateClassicPipelinesCmd.RunE(translateClassicPipelinesCmd, []string{"bizevents"}); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}

	want := map[string]string{
		"configuration":              "bizevents",
		"includeSampleData":          "true",
		"skipDisabledRules":          "false",
		"skipBuiltinProcessingRules": "true",
	}
	for p, w := range want {
		if gotParams[p] != w {
			t.Errorf("query %q = %q, want %q", p, gotParams[p], w)
		}
	}
}

func TestTranslateClassicPipelinesCmd_InvalidScope(t *testing.T) {
	// An invalid scope must fail fast without making an HTTP call.
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		classicPipelinesTranslateEndpoint: func(w http.ResponseWriter, _ *http.Request) {
			t.Error("endpoint should not be called for an invalid scope")
			w.WriteHeader(http.StatusOK)
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true

	testutil.ResetCommandFlags(translateClassicPipelinesCmd)

	err := translateClassicPipelinesCmd.RunE(translateClassicPipelinesCmd, []string{"metrics"})
	if err == nil {
		t.Fatal("expected an error for an invalid scope")
	}
	if !strings.Contains(err.Error(), "invalid configuration scope") {
		t.Errorf("error = %q, want it to mention invalid configuration scope", err.Error())
	}
}

// runWithOutput sets up a mock server returning body, runs the command for the
// given output format, and returns captured stdout.
func runWithOutput(t *testing.T, format, body string, args []string) string {
	t.Helper()
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		classicPipelinesTranslateEndpoint: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	origAgent := agentMode(context.Background())
	origFormat := outputFormat(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
		gFlags.agentMode = origAgent
		gFlags.outputFormat = origFormat
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = false
	gFlags.agentMode = false
	gFlags.outputFormat = format

	testutil.ResetCommandFlags(translateClassicPipelinesCmd)

	return captureStdout(t, func() {
		if err := translateClassicPipelinesCmd.RunE(translateClassicPipelinesCmd, args); err != nil {
			t.Fatalf("RunE() error = %v", err)
		}
	})
}

func TestTranslateClassicPipelinesCmd_DefaultPrintsValueOnly(t *testing.T) {
	out := runWithOutput(t, "table",
		`{"value":{"id":"pipe-1","processors":[]},"withWarning":false}`,
		[]string{"logs"})

	// Default output is the pipeline document (value) as indented JSON — not
	// the full result, so withWarning must not appear.
	if !strings.Contains(out, `"id": "pipe-1"`) {
		t.Errorf("default output missing indented value; got:\n%s", out)
	}
	if strings.Contains(out, "withWarning") {
		t.Errorf("default output should not include withWarning; got:\n%s", out)
	}
}

func TestTranslateClassicPipelinesCmd_JSONPrintsValueOnly(t *testing.T) {
	out := runWithOutput(t, "json",
		`{"value":{"id":"pipe-1"},"withWarning":true}`,
		[]string{"logs"})

	// -o json emits the pipeline document (value) directly so it is applyable
	// via the Settings API; withWarning is surfaced out-of-band, not in stdout.
	if !strings.Contains(out, `"id": "pipe-1"`) {
		t.Errorf("json output missing value; got:\n%s", out)
	}
	if strings.Contains(out, "withWarning") {
		t.Errorf("json output should not include withWarning; got:\n%s", out)
	}
}

func TestTranslateClassicPipelinesCmd_YAMLRendersStructured(t *testing.T) {
	out := runWithOutput(t, "yaml",
		`{"value":{"id":"pipe-1","processors":[{"type":"fieldsAdd"}]},"withWarning":false}`,
		[]string{"bizevents"})

	// -o yaml renders the document structurally (keys as YAML, not an escaped
	// JSON string) and directly — no {value, withWarning} envelope wrapper — so
	// the file is applyable as-is.
	if !strings.Contains(out, "id: pipe-1") {
		t.Errorf("yaml output not structured; got:\n%s", out)
	}
	if strings.Contains(out, "value:") || strings.Contains(out, "withWarning") {
		t.Errorf("yaml output should be the bare document, not the envelope; got:\n%s", out)
	}
}

func TestTranslateClassicPipelinesCmd_TOONRendersStructured(t *testing.T) {
	out := runWithOutput(t, "toon",
		`{"value":{"id":"pipe-1","processors":[{"type":"fieldsAdd"}]},"withWarning":false}`,
		[]string{"logs"})

	// -o toon must be honored (rendered as TOON), not silently downgraded to
	// JSON. TOON emits bare, unquoted keys (`id: pipe-1`); the JSON fallback
	// would emit a quoted key (`"id": "pipe-1"`). Like the other formats it
	// renders the bare document, not the {value, withWarning} envelope.
	if !strings.Contains(out, "id: pipe-1") {
		t.Errorf("toon output not rendered as TOON; got:\n%s", out)
	}
	if strings.Contains(out, `"id"`) {
		t.Errorf("toon output looks like JSON (downgraded); got:\n%s", out)
	}
	if strings.Contains(out, "value:") || strings.Contains(out, "withWarning") {
		t.Errorf("toon output should be the bare document, not the envelope; got:\n%s", out)
	}
}

func TestTranslateClassicPipelinesCmd_NullValue(t *testing.T) {
	// A scope with no Classic pipeline configured yields a null document; the
	// command must succeed and emit a consistent null on stdout (the human note
	// goes to stderr).
	out := runWithOutput(t, "json",
		`{"value":null,"withWarning":false}`,
		[]string{"logs"})

	if strings.TrimSpace(out) != "null" {
		t.Errorf("null value output = %q, want %q", strings.TrimSpace(out), "null")
	}
}

func TestTranslateClassicPipelinesCmd_AgentMode(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		classicPipelinesTranslateEndpoint: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":{"id":"pipe-2"},"withWarning":true}`))
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	origAgent := agentMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
		gFlags.agentMode = origAgent
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true
	gFlags.agentMode = true

	testutil.ResetCommandFlags(translateClassicPipelinesCmd)

	out := captureStdout(t, func() {
		if err := translateClassicPipelinesCmd.RunE(translateClassicPipelinesCmd, []string{"logs"}); err != nil {
			t.Fatalf("RunE() error = %v", err)
		}
	})

	// The agent envelope wraps the translated document under "result" and
	// carries the partial-translation warning and an apply suggestion in its
	// context (withWarning is surfaced here, not in the result payload).
	for _, want := range []string{
		`"ok":true`,
		`"pipe-2"`,
		`"warnings"`,
		"manual rewrite",
		`"suggestions"`,
		"builtin:openpipeline.logs.pipelines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agent envelope missing %q; got:\n%s", want, out)
		}
	}
}
