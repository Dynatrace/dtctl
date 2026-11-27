package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/config"
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
// Individual tests may still opt back in with t.Setenv, which restores this
// default when they finish.
//
// It also replaces the interactive browser login. Tests that drive `auth login`
// past the keyring gate would otherwise open the developer's browser against
// the real SSO with a placeholder environment, and wait for the redirect.
//
// And it gives the binary a data directory of its own. With the keyring off,
// tokens go to a file store under XDG_DATA_HOME, and the xdg package reads
// that variable once: a test that sets it with t.Setenv alone keeps using the
// path xdg read before. That path must not be the developer's, where a login
// or logout under test would write and delete real token files. A test that
// needs a directory to itself uses setXDGHome.
func TestMain(m *testing.M) {
	if err := os.Setenv(config.EnvDisableKeyring, "1"); err != nil {
		panic(err)
	}
	dataHome, err := os.MkdirTemp("", "dtctl-cmd-test-data-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_DATA_HOME", dataHome); err != nil {
		panic(err)
	}
	xdg.Reload()
	authBrowserFlowFunc = func(context.Context, *auth.OAuthFlow) (*auth.TokenSet, error) {
		return nil, errors.New("browser login is disabled in tests")
	}
	code := m.Run()
	// os.Exit runs no deferred call.
	_ = os.RemoveAll(dataHome)
	os.Exit(code)
}

// TestDataDirectoryIsNotTheDevelopersOwn: the data directory holds the file
// token store, so the test binary must have one of its own, also for a test
// that sets XDG_DATA_HOME and does not make the xdg package read it again.
func TestDataDirectoryIsNotTheDevelopersOwn(t *testing.T) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		t.Fatal("XDG_DATA_HOME is not set: TestMain must point it at a directory of the test binary")
	}
	// The variable alone proves nothing: it has to be TestMain's directory,
	// not one the developer's shell exported.
	if !strings.HasPrefix(filepath.Base(dataHome), "dtctl-cmd-test-data-") {
		t.Fatalf("XDG_DATA_HOME is %s, not a directory TestMain created", dataHome)
	}
	// An earlier test may have left xdg on a directory of its own.
	xdg.Reload()
	inside := func() bool { return strings.HasPrefix(config.DataDir(), dataHome+string(filepath.Separator)) }
	if !inside() {
		t.Fatalf("the data directory is %s, outside %s", config.DataDir(), dataHome)
	}

	t.Run("a test that sets the variable and nothing else", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		// xdg still holds the path it read before, which is the binary's.
		if !inside() {
			t.Fatalf("the data directory is %s, outside %s", config.DataDir(), dataHome)
		}
	})
}
