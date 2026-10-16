package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/adrg/xdg"
)

func isolateDataDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(EnvTokenStorage, "")
	t.Setenv(EnvDisableKeyring, "")
	xdg.Reload()
	t.Cleanup(xdg.Reload)
}

// stubKeyringProbe models the keyring the consent is checked against.
func stubKeyringProbe(t *testing.T, err error) {
	t.Helper()
	orig := checkKeyring
	t.Cleanup(func() { checkKeyring = orig })
	checkKeyring = func() error { return err }
}

var errNoSecretService = errors.New("keyring probe failed: The name org.freedesktop.secrets was not provided by any .service files")

func TestIsKeyringAbsent(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"no secret service", errors.New("keyring probe failed: The name org.freedesktop.secrets was not provided by any .service files"), true},
		{"no session bus", errors.New("keyring probe failed: dbus: couldn't determine address of session bus"), true},
		{"locked collection", errors.New("keyring probe failed: " + ErrMsgCollectionUnlock), false},
		{"disabled", errors.New("keyring disabled via " + EnvDisableKeyring + " environment variable"), false},
		{"other failure", errors.New("keyring probe failed: boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsKeyringAbsent(tt.err); got != tt.want {
				t.Errorf("IsKeyringAbsent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileTokenStorageConsent(t *testing.T) {
	isolateDataDir(t)
	stubKeyringProbe(t, errNoSecretService)

	if IsFileTokenStorage() {
		t.Fatal("file storage must be off before any choice is made")
	}
	if err := PersistFileTokenStorage(); err != nil {
		t.Fatal(err)
	}
	if !IsFileTokenStorage() {
		t.Error("persisted consent should enable file storage")
	}
	info, err := os.Stat(FileTokenStorageConsentPath())
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not model POSIX permission bits.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("consent marker mode = %o, want 600", perm)
	}

	t.Run("env keyring overrides consent", func(t *testing.T) {
		t.Setenv(EnvTokenStorage, "keyring")
		if IsFileTokenStorage() {
			t.Error("DTCTL_TOKEN_STORAGE=keyring must win over the persisted choice")
		}
	})
	t.Run("disabled keyring ignores consent", func(t *testing.T) {
		t.Setenv(EnvDisableKeyring, "1")
		if IsFileTokenStorage() {
			t.Error("embedded sessions (keyring disabled) must not pick up host consent")
		}
	})
	t.Run("env file works without consent", func(t *testing.T) {
		_ = os.Remove(FileTokenStorageConsentPath())
		t.Setenv(EnvTokenStorage, "file")
		if !IsFileTokenStorage() {
			t.Error("DTCTL_TOKEN_STORAGE=file must still enable file storage")
		}
	})
}

// A remembered choice covers "no keyring here", nothing else: a locked or broken
// keyring must surface as an error, and a keyring that appears later is used.
func TestFileTokenStorageConsent_OnlyWhileKeyringAbsent(t *testing.T) {
	isolateDataDir(t)
	if err := PersistFileTokenStorage(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"absent", errNoSecretService, true},
		{"locked", errors.New("keyring probe failed: " + ErrMsgCollectionUnlock), false},
		{"broken", errors.New("keyring probe failed: boom"), false},
		{"installed later", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubKeyringProbe(t, tt.err)
			if got := IsFileTokenStorage(); got != tt.want {
				t.Errorf("IsFileTokenStorage() = %v, want %v", got, tt.want)
			}
		})
	}
	t.Run("explicit env file still forces files", func(t *testing.T) {
		stubKeyringProbe(t, nil)
		t.Setenv(EnvTokenStorage, "file")
		if !IsFileTokenStorage() {
			t.Error("DTCTL_TOKEN_STORAGE=file is an explicit override and must not depend on the probe")
		}
	})
}

func TestPersistFileTokenStorage_TightensExistingMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not model POSIX permission bits")
	}
	isolateDataDir(t)
	path := FileTokenStorageConsentPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PersistFileTokenStorage(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("pre-existing marker mode = %o, want 600", perm)
	}
}
