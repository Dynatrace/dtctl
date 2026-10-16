package cmd

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/watch"
)

var getBreakpointsCmd = newGetBreakpointsCmd()

func newGetBreakpointsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "breakpoints",
		Short: "List all breakpoints in the current workspace",
		RunE:  runGetBreakpoints,
	}
	markLiveDebuggerExperimental(c)
	return c
}

// getCmd represents the get command
var getCmd = newGetCmd(&getListLimit, &getListFields)

func newGetCmd(limit *int, fields *string) *cobra.Command {
	c := &cobra.Command{
		Use:   "get",
		Short: "Display one or many resources",
		Long: `Display one or many resources from the Dynatrace platform.

When called with a resource type only, lists all resources of that type.
When called with a resource type and ID or name, retrieves that specific resource.
Results can be filtered with --mine and formatted with -o (json, yaml, wide, chart).

Supported resources:
  workflows (wf)              dashboards (dash, db)   notebooks (nb)
  scheduling-rules (sr)       slos                    slo-templates
  settings                    settings-schemas        buckets (bkt)
  apps                        functions               intents
  notifications               users                   groups
  edgeconnect (ec)            sdk-versions            analyzers
  copilot-skills              lookup-tables (lu)      trash
  workflow-executions (wfe)   wfe-task-result         extensions (ext)
  extension-configs (extcfg)  documents (doc)         anomaly-detectors (ad)
  hub-extensions              hub-extension-releases  apis
  environment                 license                 license-settings

Use 'dtctl get <resource> --help' for resource-specific options.`,
		Example: `  # List all workflows
  dtctl get workflows

  # Get a specific workflow by name or ID
  dtctl get workflow my-workflow

  # List workflows as JSON
  dtctl get workflows -o json

  # List only your own workflows
  dtctl get workflows --mine

  # Watch workflows for changes in real-time
  dtctl get workflows --watch

  # List with wide output (extra columns)
  dtctl get workflows -o wide`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	addGetListShapeFlags(c, limit, fields)
	return c
}

// executeWithWatch wraps a fetcher function with watch mode support
func executeWithWatch(cmd *cobra.Command, fetcher watch.ResourceFetcher, printer interface{}) error {
	watchMode, _ := cmd.Flags().GetBool("watch")
	if !watchMode {
		return nil
	}
	if !currentCaps(cmdContext(cmd)).LongRunningStreams {
		return &CapabilityError{Feature: "watch mode"}
	}

	interval, _ := cmd.Flags().GetDuration("interval")
	watchOnly, _ := cmd.Flags().GetBool("watch-only")

	if interval < time.Second {
		interval = time.Second
	}

	cfg, err := loadConfig(cmdContext(cmd))
	if err != nil {
		return err
	}

	c, err := newClientFromConfig(cmdContext(cmd), cfg)
	if err != nil {
		return err
	}

	basePrinter := printer.(output.Printer)
	watchPrinter := output.NewWatchPrinterWithWriter(basePrinter, currentStdout(cmdContext(cmd)), output.ColorEnabled())

	watcher := watch.NewWatcher(watch.WatcherOptions{
		Interval:    interval,
		Client:      c,
		Fetcher:     fetcher,
		Printer:     watchPrinter,
		ShowInitial: !watchOnly,
	})

	// NotifyContext rather than Notify plus a goroutine: stop() deregisters the
	// handler when the watch ends, so an embedder running many requests does
	// not accumulate one parked goroutine and one SIGTERM handler per watch.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return watcher.Start(ctx)
}

// addWatchFlags adds watch-related flags to a command
func addWatchFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("watch", false, "Watch for changes")
	cmd.Flags().Duration("interval", 2*time.Second, "Polling interval (minimum: 1s)")
	cmd.Flags().Bool("watch-only", false, "Only show changes, not initial state")
}

func init() {
	rootCmd.AddCommand(getCmd)

	// Get subcommands (command definitions live in get_*.go files)
	getCmd.AddCommand(getWorkflowsCmd)
	getCmd.AddCommand(getSchedulingRulesCmd)
	getCmd.AddCommand(getWorkflowExecutionsCmd)
	getCmd.AddCommand(getWfeTaskResultCmd)
	getCmd.AddCommand(getDashboardsCmd)
	getCmd.AddCommand(getNotebooksCmd)
	getCmd.AddCommand(getTrashCmd)
	getCmd.AddCommand(getSLOsCmd)
	getCmd.AddCommand(getSLOTemplatesCmd)
	getCmd.AddCommand(getNotificationsCmd)
	getCmd.AddCommand(getBucketsCmd)
	getCmd.AddCommand(getLookupsCmd)
	getCmd.AddCommand(getAppsCmd)
	getCmd.AddCommand(getFunctionsCmd)
	getCmd.AddCommand(getIntentsCmd)
	getCmd.AddCommand(getEdgeConnectsCmd)
	getCmd.AddCommand(getUsersCmd)
	getCmd.AddCommand(getGroupsCmd)
	getCmd.AddCommand(getSDKVersionsCmd)
	getCmd.AddCommand(getAnalyzersCmd)
	getCmd.AddCommand(getCopilotSkillsCmd)
	getCmd.AddCommand(getSettingsSchemasCmd)
	getCmd.AddCommand(getSettingsCmd)
	getCmd.AddCommand(getBreakpointsCmd)
	getCmd.AddCommand(getSnapshotsCmd)
	getCmd.AddCommand(getExtensionsCmd)
	getCmd.AddCommand(getExtensionConfigsCmd)
	getCmd.AddCommand(getDocumentsCmd)
	getCmd.AddCommand(getSegmentsCmd)
	getCmd.AddCommand(getAnomalyDetectorsCmd)
	getCmd.AddCommand(getHubExtensionsCmd)
	getCmd.AddCommand(getHubExtensionReleasesCmd)
	getCmd.AddCommand(getAPIsCmd)
	getCmd.AddCommand(getEnvironmentCmd)
	getCmd.AddCommand(getLicenseCmd)
	getCmd.AddCommand(getLicenseSettingsCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
