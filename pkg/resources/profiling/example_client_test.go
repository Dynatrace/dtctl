package profiling_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adrg/xdg"

	"github.com/Dynatrace/dtctl/sdk/session"
)

// The Example's client must send the token its own file carries, whatever the
// host's credential stores hold under the same name: an example that asked
// them would read a developer's login on every `go test`.
func TestExampleClient_IgnoresHostCredentialStores(t *testing.T) {
	var got string
	env := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
	}))
	defer env.Close()

	// A host store that holds a login under the example's token name.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	xdg.Reload()
	t.Cleanup(xdg.Reload)
	t.Setenv(session.EnvTokenStorage, "file")
	tm, err := session.NewTokenManager(session.OAuthConfigFromEnvironmentURL(env.URL, "", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := tm.SaveToken("example", &session.TokenSet{AccessToken: "host-store-token", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(exampleConfig(env.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := exampleClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.HTTP().R().Get("/"); err != nil {
		t.Fatal(err)
	}
	if want := "Bearer dt0c01.EXAMPLE"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

// The Example must leave the process environment as it found it: DTCTL_CONFIG
// is the developer's, and unsetting it would lose it for every later test.
func TestExample_LeavesTheEnvironmentAlone(t *testing.T) {
	t.Setenv("DTCTL_CONFIG", "/nonexistent/keep-me.yaml")
	Example()
	if got := os.Getenv("DTCTL_CONFIG"); got != "/nonexistent/keep-me.yaml" {
		t.Errorf("DTCTL_CONFIG = %q after the Example, want it unchanged", got)
	}
}
