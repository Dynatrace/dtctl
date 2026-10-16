package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// chdirWithLocalConfig puts the test in a directory holding a .dtctl.yaml and
// points XDG_CONFIG_HOME at an empty config home — the state a developer is in
// right after `dtctl config init`. Returns the project dir and the global
// config path.
func chdirWithLocalConfig(t *testing.T) (projectDir, globalPath string) {
	t.Helper()
	// NOT parallel: os.Chdir and XDG_CONFIG_HOME are process-global.
	projectDir = t.TempDir()
	xdgHome := t.TempDir()

	// Registered before t.Setenv so it runs *after* the env var is restored:
	// reloading while XDG_CONFIG_HOME still points at the temp dir would leave
	// xdg caching a path that t.TempDir is about to delete, breaking every
	// later test in the package.
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	xdg.Reload()

	local := `apiVersion: dtctl.io/v1
kind: Config
current-context: my-environment
contexts:
    - name: my-environment
      context:
        environment: ${DT_ENVIRONMENT_URL}
        token-ref: my-token
`
	if err := os.WriteFile(filepath.Join(projectDir, config.LocalConfigName), []byte(local), 0600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	origWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return projectDir, filepath.Join(xdgHome, "dtctl", "config")
}

// TestSetContextGlobalWritesGlobalConfig verifies that --global creates the
// binding in the global config and leaves a discovered .dtctl.yaml untouched.
// Without it, the two commands `config init` tells the user to run both land
// in the local file, so the global binding the local file needs to resolve a
// credential never gets created.
func TestSetContextGlobalWritesGlobalConfig(t *testing.T) {
	projectDir, globalPath := chdirWithLocalConfig(t)
	localPath := filepath.Join(projectDir, config.LocalConfigName)

	before, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read local config: %v", err)
	}

	if err := setContext(context.Background(), "my-environment", contextSettings{
		environment: "https://abc12345.apps.dynatrace.com",
		tokenRef:    "my-token",
		global:      true,
	}); err != nil {
		t.Fatalf("setContext(global=true) error: %v", err)
	}

	globalData, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("global config was not written: %v", err)
	}
	for _, want := range []string{"https://abc12345.apps.dynatrace.com", "my-token"} {
		if !strings.Contains(string(globalData), want) {
			t.Errorf("global config missing %q; file is:\n%s", want, globalData)
		}
	}

	after, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read local config after: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("--global modified the local config:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestSetContextWithoutGlobalWritesLocalConfig pins the default: writes still
// target the discovered .dtctl.yaml, so --global is opt-in and existing
// project-local workflows are unchanged.
func TestSetContextWithoutGlobalWritesLocalConfig(t *testing.T) {
	projectDir, globalPath := chdirWithLocalConfig(t)
	localPath := filepath.Join(projectDir, config.LocalConfigName)

	if err := setContext(context.Background(), "my-environment", contextSettings{
		environment: "https://abc12345.apps.dynatrace.com",
		tokenRef:    "my-token",
	}); err != nil {
		t.Fatalf("setContext(global=false) error: %v", err)
	}

	localData, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read local config: %v", err)
	}
	if !strings.Contains(string(localData), "https://abc12345.apps.dynatrace.com") {
		t.Errorf("local config was not updated; file is:\n%s", localData)
	}
	if _, err := os.Stat(globalPath); !os.IsNotExist(err) {
		t.Errorf("global config should not exist without --global (stat err = %v)", err)
	}
}

// TestSetContextUsageTrackingRefusesLocalConfig: only the global config can opt
// a context into the usage ledger, so turning it on in a discovered .dtctl.yaml
// is refused (and the file left alone), while --global and turning it off work.
func TestSetContextUsageTrackingRefusesLocalConfig(t *testing.T) {
	projectDir, globalPath := chdirWithLocalConfig(t)
	localPath := filepath.Join(projectDir, config.LocalConfigName)
	before, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read local config: %v", err)
	}

	on, off := true, false
	err = setContext(context.Background(), "my-environment", contextSettings{usageTracking: &on})
	if err == nil || !strings.Contains(err.Error(), "--global") {
		t.Fatalf("setContext(--usage-tracking) on a local config: err = %v, want a refusal pointing at --global", err)
	}
	if after, _ := os.ReadFile(localPath); string(after) != string(before) {
		t.Errorf("refused write modified the local config:\n%s", after)
	}

	if err := setContext(context.Background(), "my-environment", contextSettings{usageTracking: &off}); err != nil {
		t.Errorf("turning tracking off in a local config: %v", err)
	}

	if err := setContext(context.Background(), "my-environment", contextSettings{
		environment:   "https://abc12345.apps.dynatrace.com",
		tokenRef:      "my-token",
		global:        true,
		usageTracking: &on,
	}); err != nil {
		t.Fatalf("setContext(--usage-tracking --global): %v", err)
	}
	globalData, err := os.ReadFile(globalPath)
	if err != nil || !strings.Contains(string(globalData), "usage-tracking: true") {
		t.Errorf("global config should carry usage-tracking: true (err %v):\n%s", err, globalData)
	}
}
