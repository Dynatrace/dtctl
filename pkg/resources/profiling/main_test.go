package profiling_test

import (
	"os"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/session"
)

// TestMain keeps the package's tests and its Example away from the OS keyring,
// as cmd's does.
func TestMain(m *testing.M) {
	if err := os.Setenv(session.EnvDisableKeyring, "1"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
