package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/require"
)

const envRefTestSecret = "dt0s16.NOT-A-REAL-TOKEN.VALUE"

const envRefTestConfig = `apiVersion: v1
kind: Config
current-context: templated
contexts:
    - name: templated
      context:
        environment: ${DT_TEST_ENV_URL}
        token-ref: templated-token
    - name: plain
      context:
        environment: https://def67890.apps.dynatrace.com
        token-ref: plain-token
tokens:
    - name: templated-token
      token: ${DT_TEST_SECRET}
    - name: plain-token
      token: plain-value
aliases:
    old: get workflows
`

// TestConfigWritersPreserveEnvRefs is the end-to-end regression test for #697:
// every command that rewrites the config file must keep a ${VAR} reference as
// written, whether or not the variable is set at the time. Before the fix each
// of these wrote the resolved secret to disk, or "" when the variable was
// unset.
func TestConfigWritersPreserveEnvRefs(t *testing.T) {
	writers := [][]string{
		{"config", "use-context", "plain"},
		{"ctx", "plain"},
		{"config", "set-context", "templated", "--description", "updated"},
		{"config", "set-context", "new", "--environment", "https://new12345.apps.dynatrace.com"},
		{"config", "set-credentials", "plain-token", "--token", "replaced-value"},
		{"config", "set", "preferences.editor", "nano"},
		{"alias", "set", "wf", "get workflows"},
		{"alias", "delete", "old"},
		{"config", "delete-context", "plain"},
		{"config", "delete-credentials", "plain-token"},
	}
	for _, varsSet := range []bool{true, false} {
		for _, argv := range writers {
			name := strings.Join(argv, " ")
			if !varsSet {
				name += " (variables unset)"
			}
			t.Run(name, func(t *testing.T) {
				path := isolatedEnvRefConfig(t, varsSet)

				var stderr bytes.Buffer
				code := Run(argv, RunOptions{Stdout: &bytes.Buffer{}, Stderr: &stderr})
				require.Zero(t, code, "stderr: %s", stderr.String())

				data, err := os.ReadFile(path)
				require.NoError(t, err)
				got := string(data)
				require.NotContains(t, got, envRefTestSecret, "resolved secret written to the config file")
				require.Contains(t, got, "token: ${DT_TEST_SECRET}")
				require.Contains(t, got, "environment: ${DT_TEST_ENV_URL}")
			})
		}
	}
}

// TestConfigViewShowsTokenReference: `config view` prints a token loaded from
// ${VAR} as the reference, not as the secret it resolved to.
func TestConfigViewShowsTokenReference(t *testing.T) {
	isolatedEnvRefConfig(t, true)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"config", "view", "-o", "yaml"}, RunOptions{Stdout: &stdout, Stderr: &stderr})
	require.Zero(t, code, "stderr: %s", stderr.String())
	require.NotContains(t, stdout.String(), envRefTestSecret)
	require.Contains(t, stdout.String(), "${DT_TEST_SECRET}")
}

// isolatedEnvRefConfig installs envRefTestConfig as the DTCTL_CONFIG file, with
// the variables it references set or unset, and returns its path.
func isolatedEnvRefConfig(t *testing.T, varsSet bool) string {
	t.Helper()
	isolatedConfig(t, envRefTestConfig)
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	xdg.Reload()
	if varsSet {
		t.Setenv("DT_TEST_ENV_URL", "https://abc12345.apps.dynatrace.com")
		t.Setenv("DT_TEST_SECRET", envRefTestSecret)
	} else {
		for _, v := range []string{"DT_TEST_ENV_URL", "DT_TEST_SECRET"} {
			t.Setenv(v, "")
			require.NoError(t, os.Unsetenv(v))
		}
	}
	return os.Getenv("DTCTL_CONFIG")
}
