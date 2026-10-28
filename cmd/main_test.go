package cmd

import (
	"context"
	"errors"
	"os"
	"testing"

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
func TestMain(m *testing.M) {
	if err := os.Setenv(config.EnvDisableKeyring, "1"); err != nil {
		panic(err)
	}
	authBrowserFlowFunc = func(context.Context, *auth.OAuthFlow) (*auth.TokenSet, error) {
		return nil, errors.New("browser login is disabled in tests")
	}
	os.Exit(m.Run())
}
