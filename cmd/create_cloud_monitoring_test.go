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

func TestCloudMonitoringCentralEnrichmentFlagsAreHiddenAndDisabledByDefault(t *testing.T) {
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
			if !flag.Hidden {
				t.Error("central-enrichment flag must remain hidden before customer release")
			}
			if flag.DefValue != "false" {
				t.Errorf("central-enrichment default = %q, want false", flag.DefValue)
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

	t.Run("untouched flag is omitted", func(t *testing.T) {
		if intent := centralEnrichmentIntent(newCmd(), centralEnrichmentDefault); intent != nil {
			t.Errorf("intent = %v, want nil so the backend keeps its own default", *intent)
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

	// The rollout switch documented in create.go flips the default to true. An
	// explicit --central-enrichment=false has to stay a working opt-out at that
	// point, which it only does if "false" is distinguishable from "unset".
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
