package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/resources/gcpmonitoringconfig"
)

func TestCloudMonitoringCentralEnrichmentFlagsAreVisibleAndEnabledByDefault(t *testing.T) {
	commands := []struct {
		name string
		cmd  *cobra.Command
	}{
		{name: "aws", cmd: createAWSMonitoringConfigCmd},
		{name: "azure", cmd: createAzureMonitoringConfigCmd},
		{name: "gcp", cmd: createGCPMonitoringConfigCmd},
	}

	for _, test := range commands {
		t.Run(test.name, func(t *testing.T) {
			flag := test.cmd.Flags().Lookup("central-enrichment")
			if flag == nil {
				t.Fatal("central-enrichment flag is not registered")
			}
			if flag.Hidden {
				t.Error("central-enrichment flag must be visible so the legacy opt-out is discoverable")
			}
			if flag.DefValue != "true" {
				t.Errorf("central-enrichment default = %q, want true", flag.DefValue)
			}
		})
	}
}

func TestCentralEnrichmentIntent(t *testing.T) {
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "monitoring"}
		var target bool
		addCentralEnrichmentFlag(cmd, &target)
		return cmd
	}

	t.Run("untouched flag sends central", func(t *testing.T) {
		intent := centralEnrichmentIntent(newCmd(), centralEnrichmentDefault)
		if intent == nil || !*intent {
			t.Errorf("intent = %v, want true", intent)
		}
	})

	t.Run("explicit true is sent", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("central-enrichment", "true"); err != nil {
			t.Fatalf("Set() error = %v", err)
		}
		intent := centralEnrichmentIntent(cmd, true)
		if intent == nil || !*intent {
			t.Errorf("intent = %v, want true", intent)
		}
	})

	// With the default on, an explicit --central-enrichment=false is the only
	// way to ask for legacy enrichment, so it must be sent rather than omitted.
	t.Run("explicit false is sent", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("central-enrichment", "false"); err != nil {
			t.Fatalf("Set() error = %v", err)
		}
		intent := centralEnrichmentIntent(cmd, false)
		if intent == nil || *intent {
			t.Errorf("intent = %v, want false", intent)
		}
	})
}

func TestCloudMonitoringCreatePayloadCentralIntent(t *testing.T) {
	central := true
	for _, test := range []struct {
		name    string
		central bool
		payload any
	}{
		{
			name: "aws legacy default",
			payload: buildAWSMonitoringConfig("aws", "1", awsmonitoringconfig.Credential{},
				[]string{"eu-central-1"}, nil, nil),
		},
		{
			name:    "aws central",
			central: true,
			payload: buildAWSMonitoringConfig("aws", "1", awsmonitoringconfig.Credential{},
				[]string{"eu-central-1"}, nil, &central),
		},
		{
			name:    "azure legacy default",
			payload: buildAzureMonitoringConfig("azure", "1", azuremonitoringconfig.Credential{}, nil, nil, nil),
		},
		{
			name:    "azure central",
			central: true,
			payload: buildAzureMonitoringConfig("azure", "1", azuremonitoringconfig.Credential{}, nil, nil, &central),
		},
		{
			name:    "gcp legacy default",
			payload: buildGCPMonitoringConfig("gcp", "1", gcpmonitoringconfig.Credential{}, nil, nil, nil),
		},
		{
			name:    "gcp central",
			central: true,
			payload: buildGCPMonitoringConfig("gcp", "1", gcpmonitoringconfig.Credential{}, nil, nil, &central),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			text := string(encoded)
			present := strings.Contains(text, `"useIngestEnrichmentConfig":true`)
			if present != test.central {
				t.Errorf("central intent presence = %v, want %v: %s", present, test.central, text)
			}
			if strings.Contains(text, "ingestEnrichmentMigrationProcessed") {
				t.Errorf("payload contains backend-owned processed marker: %s", text)
			}
		})
	}
}
