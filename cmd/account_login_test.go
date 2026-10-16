package cmd

import (
	"context"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// TestResolveUUIDNoDiscovery exercises the account-UUID resolution order used by
// `dtctl account login` and the account subcommands: explicit flag >
// DTCTL_ACCOUNT_UUID > context account-uuid. There is no auto-discovery.
func TestResolveUUIDNoDiscovery(t *testing.T) {
	t.Run("flag wins over env and config", func(t *testing.T) {
		t.Setenv("DTCTL_ACCOUNT_UUID", "env-uuid")
		ctx := &config.Context{AccountUUID: "cfg-uuid"}
		if got := resolveUUIDNoDiscovery(context.Background(), ctx, "flag-uuid"); got != "flag-uuid" {
			t.Errorf("uuid = %q, want flag-uuid", got)
		}
	})

	t.Run("env var when no flag", func(t *testing.T) {
		t.Setenv("DTCTL_ACCOUNT_UUID", "env-uuid")
		ctx := &config.Context{AccountUUID: "cfg-uuid"}
		if got := resolveUUIDNoDiscovery(context.Background(), ctx, ""); got != "env-uuid" {
			t.Errorf("uuid = %q, want env-uuid", got)
		}
	})

	t.Run("context config when no flag or env", func(t *testing.T) {
		t.Setenv("DTCTL_ACCOUNT_UUID", "")
		ctx := &config.Context{AccountUUID: "cfg-uuid"}
		if got := resolveUUIDNoDiscovery(context.Background(), ctx, ""); got != "cfg-uuid" {
			t.Errorf("uuid = %q, want cfg-uuid", got)
		}
	})

	t.Run("empty when nothing set", func(t *testing.T) {
		t.Setenv("DTCTL_ACCOUNT_UUID", "")
		ctx := &config.Context{}
		if got := resolveUUIDNoDiscovery(context.Background(), ctx, ""); got != "" {
			t.Errorf("uuid = %q, want empty", got)
		}
	})
}
