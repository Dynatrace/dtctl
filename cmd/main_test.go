package cmd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
)

// TestMain keeps the cmd test binary away from the real OS keyring.
//
// Many tests here build a Config and call SetToken with placeholder references
// such as "test-token" or "my-token". Config.SetToken writes through to the OS
// keyring whenever one is reachable, so without this guard a plain
// `go test ./...` stores dummy credentials in the developer's macOS Keychain,
// Secret Service, or Windows Credential Manager — and on macOS can block the
// run behind a GUI keychain prompt.
//
// Individual tests opt back in with optInRepoScope, which restores this
// default when they finish.
//
// It also replaces the interactive browser login. Tests that drive `auth login`
// past the keyring gate would otherwise open the developer's browser against
// the real SSO with a placeholder environment, and wait for the redirect.
//
// Finally, it keeps the dtctl checkout's own repo scope file out of the run.
// The package runs from cmd/, inside this repository, so a query test that
// forgot to chdir into a temporary repository would be scoped by a file a
// maintainer keeps there, or would write one. Default scoping is off for the
// package (the tests that exercise it call optInRepoScope), and the run
// fails if the file was created, changed or removed while it ran. A file that
// was there before and is still there unchanged is the maintainer's own.
func TestMain(m *testing.M) {
	if err := os.Setenv(config.EnvDisableKeyring, "1"); err != nil {
		panic(err)
	}
	if err := os.Setenv(noRepoScopeEnv, "1"); err != nil {
		panic(err)
	}
	authBrowserFlowFunc = func(context.Context, *auth.OAuthFlow) (*auth.TokenSet, error) {
		return nil, errors.New("browser login is disabled in tests")
	}
	scopeFile, _ := filepath.Abs(filepath.Join("..", reposcope.FileName))
	before := fileDigest(scopeFile)
	code := m.Run()
	if after := fileDigest(scopeFile); after != before {
		fmt.Fprintf(os.Stderr, "FAIL: the tests changed %s (%s before, %s after); a test that reaches the repo scope file must chdir into a temporary repository\n",
			scopeFile, before, after)
		code = 1
	}
	os.Exit(code)
}

// fileDigest is the sha256 of the file at path, "absent" when there is none,
// or the error that kept it from being read.
func fileDigest(path string) string {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "absent"
	case err != nil:
		return err.Error()
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// scopeFileRewriteEnv makes TestMainHelperRewritesScopeFile act; only
// TestMainGuardsTheCheckoutScopeFile sets it, in a copy of this binary it runs.
const scopeFileRewriteEnv = "DTCTL_TEST_REWRITE_SCOPE_FILE"

// TestMainHelperRewritesScopeFile is not a test on its own:
// TestMainGuardsTheCheckoutScopeFile runs it in a child process to rewrite the
// scope file.
func TestMainHelperRewritesScopeFile(t *testing.T) {
	if os.Getenv(scopeFileRewriteEnv) == "" {
		t.Skip("run by TestMainGuardsTheCheckoutScopeFile")
	}
	require.NoError(t, os.WriteFile(filepath.Join("..", reposcope.FileName), []byte("environments: {}\n"), 0o644))
}

// A maintainer's own scope file in the checkout does not fail the run; a run
// that rewrites it does. Both run this test binary again, from the cmd/ of a
// directory laid out like the checkout.
func TestMainGuardsTheCheckoutScopeFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cmd")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, reposcope.FileName), []byte("environments:\n  example.invalid: []\n"), 0o644))

	run := func(pattern string, env ...string) (int, string) {
		t.Helper()
		c := exec.Command(os.Args[0], "-test.run="+pattern, "-test.count=1")
		c.Dir = dir
		c.Env = append(os.Environ(), env...)
		out, err := c.CombinedOutput()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), string(out)
		}
		require.NoError(t, err)
		return 0, string(out)
	}

	code, out := run("^$")
	require.Zero(t, code, "an unchanged scope file is the maintainer's own: %s", out)

	code, out = run("^TestMainHelperRewritesScopeFile$", scopeFileRewriteEnv+"=1")
	require.NotZero(t, code, out)
	require.Regexp(t, `FAIL: the tests changed \S*`+regexp.QuoteMeta(string(filepath.Separator)+reposcope.FileName)+` \(sha256:\w+ before, sha256:\w+ after\)`, out)
}
