package cmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/version"
)

func TestSplitPluginArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantWords []string
		wantRest  []string
	}{
		{
			name:      "bare plugin command",
			args:      []string{"foo", "bar", "baz"},
			wantWords: []string{"foo", "bar", "baz"},
		},
		{
			name:      "leading value-taking flag is consumed by dtctl",
			args:      []string{"--context", "prod", "foo", "bar"},
			wantWords: []string{"foo", "bar"},
		},
		{
			name:      "leading inline flag",
			args:      []string{"--context=prod", "foo"},
			wantWords: []string{"foo"},
		},
		{
			name:      "words stop at the first trailing flag",
			args:      []string{"foo", "bar", "--baz", "qux"},
			wantWords: []string{"foo", "bar"},
			wantRest:  []string{"--baz", "qux"},
		},
		{
			name:      "leading boolean short flags",
			args:      []string{"-v", "foo"},
			wantWords: []string{"foo"},
		},
		{
			name:      "value-taking short flag consumes its value",
			args:      []string{"-o", "json", "foo"},
			wantWords: []string{"foo"},
		},
		{
			name:     "flags only, no command words",
			args:     []string{"--plain"},
			wantRest: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			words, rest := splitPluginArgs(tt.args)
			if strings.Join(words, " ") != strings.Join(tt.wantWords, " ") {
				t.Errorf("words = %v, want %v", words, tt.wantWords)
			}
			if strings.Join(rest, " ") != strings.Join(tt.wantRest, " ") {
				t.Errorf("rest = %v, want %v", rest, tt.wantRest)
			}
		})
	}
}

func TestIsBuiltinCommandName(t *testing.T) {
	if !isBuiltinCommandName("get") {
		t.Error("get must be recognized as built-in")
	}
	if isBuiltinCommandName("no-such-command") {
		t.Error("unknown names must not be built-in")
	}
}

func TestPluginEnv(t *testing.T) {
	t.Setenv("DTCTL_CONTEXT", "inherited")

	env := pluginEnv([]string{"--context", "prod", "--plain", "myplug"})

	get := func(key string) (string, int) {
		val, count := "", 0
		for _, e := range env {
			if strings.HasPrefix(e, key+"=") {
				val = strings.TrimPrefix(e, key+"=")
				count++
			}
		}
		return val, count
	}

	if v, n := get("DTCTL_CONTEXT"); v != "prod" || n != 1 {
		t.Errorf("DTCTL_CONTEXT = %q (%d entries), want prod exactly once (flag overrides inherited env)", v, n)
	}
	if v, _ := get("DTCTL_CALLER_VERSION"); v != version.Version {
		t.Errorf("DTCTL_CALLER_VERSION = %q, want %q", v, version.Version)
	}
	if v, _ := get("DTCTL_PLAIN"); v != "1" {
		t.Errorf("DTCTL_PLAIN = %q, want 1", v)
	}
	if v, _ := get("DTCTL_CONFIG"); v == "" {
		t.Error("DTCTL_CONFIG must always be set")
	}
	// No secrets: the environment must not gain token material beyond what
	// was already inherited.
	for _, e := range env {
		if strings.HasPrefix(e, "DTCTL_TOKEN") {
			t.Errorf("unexpected token material in plugin env: %s", e)
		}
	}
}

// The documented credential variables must be stripped from the plugin
// environment even when the caller exported them (PLUGIN_CONVENTIONS.md:
// "No tokens from dtctl").
func TestPluginEnv_StripsCredentialEnvVars(t *testing.T) {
	t.Setenv("DTCTL_TOKEN", "dt0s16.SECRET")
	t.Setenv("DT_API_TOKEN", "dt0c01.SECRET")
	t.Setenv("DTCTL_ACCOUNT_TOKEN", "dt0s16.SECRET")
	t.Setenv("UNRELATED_VAR", "kept")

	env := pluginEnv([]string{"--plain"})

	joined := strings.Join(env, "\n")
	// Scan for the planted credential *values*, not a bare substring like
	// "SECRET": the plugin env inherits the host environment, and any machine
	// exporting e.g. AWS_SECRET_ACCESS_KEY would trip a substring match on the
	// variable's name alone.
	for _, forbidden := range []string{
		"DTCTL_TOKEN=", "DT_API_TOKEN=", "DTCTL_ACCOUNT_TOKEN=",
		"dt0s16.SECRET", "dt0c01.SECRET",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("credential material leaked into plugin env (%s)", forbidden)
		}
	}
	if !strings.Contains(joined, "UNRELATED_VAR=kept") {
		t.Error("non-credential environment must be inherited")
	}
}

// Contract values derive only from dtctl's own leading flags — a flag in the
// plugin's argument list must not be reflected into the env contract.
func TestPluginEnv_IgnoresPluginOwnFlags(t *testing.T) {
	// Simulate `dtctl deploy --config foo.yaml --context prod`: dispatch
	// passes only the leading flags (none here) to pluginEnv.
	args := []string{"deploy", "--config", "foo.yaml", "--context", "prod"}
	words, rest := splitPluginArgs(args)
	leading := args[:len(args)-len(words)-len(rest)]

	env := pluginEnv(leading)

	for _, e := range env {
		if strings.HasPrefix(e, "DTCTL_CONFIG=") && strings.Contains(e, "foo.yaml") {
			t.Errorf("plugin's own --config leaked into DTCTL_CONFIG: %s", e)
		}
		if e == "DTCTL_CONTEXT=prod" {
			t.Errorf("plugin's own --context leaked into DTCTL_CONTEXT")
		}
	}
}

func TestPluginEnv_AgentImpliesPlain(t *testing.T) {
	env := pluginEnv([]string{"-A", "myplug"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "DTCTL_AGENT=1") {
		t.Error("DTCTL_AGENT=1 missing for -A")
	}
	if !strings.Contains(joined, "DTCTL_PLAIN=1") {
		t.Error("agent mode must imply DTCTL_PLAIN=1")
	}
}

func TestOverrideEnv_ReplacesExisting(t *testing.T) {
	env := overrideEnv([]string{"A=1", "B=2", "A=3"}, "A", "9")
	joined := strings.Join(env, ";")
	if strings.Count(joined, "A=") != 1 || !strings.Contains(joined, "A=9") {
		t.Errorf("overrideEnv = %v, want single A=9", env)
	}
}

// A found-but-unexecutable plugin must fail with the structured JSON envelope
// in agent mode — machine consumers parse stdout, not stderr.
func TestTryPluginDispatch_ExecFailureIsStructuredInAgentMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ENOEXEC-style exec failure is not portable to Windows")
	}
	dir := t.TempDir()
	// Executable permission but no shebang and not a binary: LookPath accepts
	// it, exec fails with ENOEXEC.
	if err := os.WriteFile(filepath.Join(dir, "dtctl-failplug"), []byte("not a program\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code, handled := tryPluginDispatch(context.Background(), []string{"-A", "failplug"})
	os.Stdout = old
	_ = w.Close()
	out, _ := io.ReadAll(r)

	if !handled || code != 1 {
		t.Fatalf("dispatch = (%d, %v), want (1, true)", code, handled)
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("agent-mode exec failure must emit a JSON envelope on stdout, got %q: %v", out, err)
	}
	if envelope.OK || envelope.Error.Message == "" {
		t.Errorf("envelope = %+v, want ok=false with a message", envelope)
	}
}

func TestTryPluginDispatch_BuiltinAndUnknownAreNotDispatched(t *testing.T) {
	// Point PATH at an empty dir: no plugins can be found, and built-in names
	// must be rejected before lookup anyway.
	t.Setenv("PATH", t.TempDir())
	if _, handled := tryPluginDispatch(context.Background(), []string{"get", "nosuchresource"}); handled {
		t.Error("built-in command names must never dispatch to plugins")
	}
	if _, handled := tryPluginDispatch(context.Background(), []string{"definitely-not-a-plugin"}); handled {
		t.Error("unknown names without a matching binary must not dispatch")
	}
}
