package client_test

import (
	"testing"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/client"
)

func TestAccountBaseURLForEnvironment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		env  auth.Environment
		want string
	}{
		{auth.EnvironmentProd, "https://api.dynatrace.com"},
		{auth.EnvironmentDev, "https://api-dev.internal.dynatracelabs.com"},
		{auth.EnvironmentHard, "https://api-hardening.internal.dynatracelabs.com"},
	}
	for _, tt := range tests {
		got := client.AccountBaseURLForEnvironment(tt.env)
		if got != tt.want {
			t.Errorf("AccountBaseURLForEnvironment(%q) = %q, want %q", tt.env, got, tt.want)
		}
	}
}
