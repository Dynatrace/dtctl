package cmd

import "testing"

func TestEnableCommandRegistration(t *testing.T) {
	enableCmd, _, err := rootCmd.Find([]string{"enable"})
	if err != nil {
		t.Fatalf("expected enable command to exist, got error: %v", err)
	}
	if enableCmd == nil || enableCmd.Name() != "enable" {
		t.Fatalf("expected enable command to exist")
	}
}

func TestEnableGCPMonitoringCommandRegistration(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"enable", "gcp", "monitoring"})
	if err != nil {
		t.Fatalf("expected enable gcp monitoring command to exist, got error: %v", err)
	}
	if cmd == nil || cmd.Name() != "monitoring" {
		t.Fatalf("expected enable gcp monitoring command to exist")
	}
}

func TestEnableGCPMonitoringFlags(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"enable", "gcp", "monitoring"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, flag := range []string{"name", "service-account-id"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("expected flag --%s to be registered on enable gcp monitoring", flag)
		}
	}
}

func TestEnableAzureMonitoringCommandRegistration(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"enable", "azure", "monitoring"})
	if err != nil {
		t.Fatalf("expected enable azure monitoring command to exist, got error: %v", err)
	}
	if cmd == nil || cmd.Name() != "monitoring" {
		t.Fatalf("expected enable azure monitoring command to exist")
	}
}

func TestEnableAzureMonitoringFlags(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"enable", "azure", "monitoring"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, flag := range []string{"name", "directory-id", "application-id"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("expected flag --%s to be registered on enable azure monitoring", flag)
		}
	}
}
