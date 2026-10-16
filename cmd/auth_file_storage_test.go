package cmd

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

var errNoSecretService = errors.New("keyring probe failed: The name org.freedesktop.secrets was not provided by any .service files")

// stubFileStorageConsent isolates the data dir and replaces the terminal and
// prompt hooks; it returns a counter for how often the user was asked.
func stubFileStorageConsent(t *testing.T, interactive, answer bool) *int {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(config.EnvTokenStorage, "")
	t.Setenv(config.EnvDisableKeyring, "")
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	origInteractive, origConfirm := authIsInteractiveFunc, authConfirmFunc
	t.Cleanup(func() { authIsInteractiveFunc, authConfirmFunc = origInteractive, origConfirm })

	asked := 0
	authIsInteractiveFunc = func(context.Context) bool { return interactive }
	authConfirmFunc = func(context.Context, string) bool { asked++; return answer }
	return &asked
}

func TestOfferFileTokenStorage_AcceptRemembersChoice(t *testing.T) {
	asked := stubFileStorageConsent(t, true, true)

	if !offerFileTokenStorage(context.Background(), errNoSecretService) {
		t.Fatal("accepting the prompt should enable file storage")
	}
	if *asked != 1 {
		t.Errorf("asked %d times, want 1", *asked)
	}
	if _, err := os.Stat(config.FileTokenStorageConsentPath()); err != nil {
		t.Errorf("choice was not persisted: %v", err)
	}

	// Whether a later login skips the question depends on the live keyring
	// probe (consent applies only while the keyring is absent), which is
	// covered with an injected probe in sdk/session.
}

func TestOfferFileTokenStorage_DeclineChangesNothing(t *testing.T) {
	stubFileStorageConsent(t, true, false)

	if offerFileTokenStorage(context.Background(), errNoSecretService) {
		t.Fatal("declining must not enable file storage")
	}
	if _, err := os.Stat(config.FileTokenStorageConsentPath()); err == nil {
		t.Error("nothing should be persisted after declining")
	}
}

func TestOfferFileTokenStorage_NeverSilent(t *testing.T) {
	tests := []struct {
		name        string
		interactive bool
		err         error
	}{
		{"non-interactive", false, errNoSecretService},
		{"locked keyring", true, errors.New("keyring probe failed: " + config.ErrMsgCollectionUnlock)},
		{"unknown failure", true, errors.New("keyring probe failed: boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asked := stubFileStorageConsent(t, tt.interactive, true)
			if offerFileTokenStorage(context.Background(), tt.err) {
				t.Error("must not fall back to file storage")
			}
			if *asked != 0 {
				t.Error("must not prompt")
			}
			if _, err := os.Stat(config.FileTokenStorageConsentPath()); err == nil {
				t.Error("must not persist anything")
			}
		})
	}
}

func TestTokenStorageUnavailableSuggestions_LeadWithTheFix(t *testing.T) {
	absent := tokenStorageUnavailableSuggestions(context.Background(), errNoSecretService, "box", "https://x.apps.dynatrace.com")
	if !strings.Contains(absent[0], "asked once") || !strings.Contains(absent[0], config.EnvTokenStorage) {
		t.Errorf("first suggestion should be the one-step fix, got %q", absent[0])
	}
	locked := tokenStorageUnavailableSuggestions(context.Background(), errors.New("keyring probe failed: "+config.ErrMsgCollectionUnlock), "box", "https://x.apps.dynatrace.com")
	if strings.Contains(locked[0], "asked once") {
		t.Errorf("a locked keyring is not 'no keyring': %q", locked[0])
	}
	if len(absent) > 5 {
		t.Errorf("suggestion list should stay short, got %d", len(absent))
	}
}
