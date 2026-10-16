package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

const (
	// Change both values when Cluster direct central create is customer-released.
	centralEnrichmentDefault = false
	centralEnrichmentHidden  = true
)

// createCmd represents the create command
var createCmd = newCreateCmd()

func newCreateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "create",
		Short: "Create resources from files",
		Long: `Create a new resource on the Dynatrace platform from a YAML or JSON file.

Reads a resource definition from a file and creates it. If the resource already
exists, the command fails — use 'dtctl apply' for create-or-update semantics.

For most workflows, 'dtctl apply -f <file>' is preferred over 'create' because
apply is idempotent (creates if new, updates if existing).

Supported resources:
  workflows (wf)          dashboards (dash, db)     notebooks (nb)
  slos                    settings                  buckets (bkt)
  edgeconnect (ec)        lookup-tables (lu)        extensions (ext)
  scheduling-rules (sr)`,
		Example: `  # Create a workflow from a YAML file
  dtctl create workflow -f workflow.yaml

  # Create a dashboard from JSON
  dtctl create dashboard -f dashboard.json

  # Create a settings object
  dtctl create settings -f settings.yaml

  # Preview what would be created
  dtctl create workflow -f workflow.yaml --dry-run`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

// centralEnrichmentIntent maps the --central-enrichment flag onto the value
// sent as useIngestEnrichmentConfig. That property is nullable in the extension
// schema, so nil ("let the backend choose") must stay distinguishable from an
// explicit false ("keep legacy enrichment").
//
// While the flag is hidden and defaults to false, an untouched flag means the
// caller has no opinion and the field is omitted. Once centralEnrichmentDefault
// flips to true at customer release the value is always sent explicitly, so
// --central-enrichment=false remains a working opt-out instead of silently
// falling back to the backend default.
func centralEnrichmentIntent(cmd *cobra.Command, enabled bool) *bool {
	if !centralEnrichmentDefault && !cmd.Flags().Changed("central-enrichment") {
		return nil
	}
	return &enabled
}

func addCentralEnrichmentFlag(command *cobra.Command, target *bool) {
	command.Flags().BoolVar(target, "central-enrichment", centralEnrichmentDefault, "Use central enrichment configuration")
	if centralEnrichmentHidden {
		_ = command.Flags().MarkHidden("central-enrichment")
	}
}

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.AddCommand(createWorkflowCmd)
	createCmd.AddCommand(createSchedulingRuleCmd)
	createCmd.AddCommand(createNotebookCmd)
	createCmd.AddCommand(createDashboardCmd)
	createCmd.AddCommand(createDocumentCmd)
	createCmd.AddCommand(createSettingsCmd)
	createCmd.AddCommand(createSLOCmd)
	createCmd.AddCommand(createBucketCmd)
	createCmd.AddCommand(createLookupCmd)
	createCmd.AddCommand(createEdgeConnectCmd)
	createCmd.AddCommand(createBreakpointCmd)
	createCmd.AddCommand(createSegmentCmd)
	createCmd.AddCommand(createAnomalyDetectorCmd)
	createCmd.AddCommand(createExtensionCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
