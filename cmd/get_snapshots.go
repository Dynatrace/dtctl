package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
)

var getSnapshotsCmd = newGetSnapshotsCmd()

func newGetSnapshotsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "snapshots <breakpoint>",
		Short: "Get Live Debugger snapshots for a breakpoint",
		Long: `Fetch application snapshots captured by a Live Debugger breakpoint.

BREAKPOINT can be specified as:
  - filename:line  e.g. OrderController.java:306
  - stable rule ID e.g. dtctl-rule-5bfb45a29fce7a46

The command looks up the breakpoint's internal DQL ID, then executes:
  fetch application.snapshots | filter breakpoint.id == toUid("<id>")

Use --decode-snapshots to decode snapshot payloads (same as dtctl query).
Use -o json / -o yaml for structured output.`,
		Example: `  # Get snapshots for a breakpoint by location
  dtctl get snapshots OrderController.java:306

  # Get snapshots by stable rule ID
  dtctl get snapshots dtctl-rule-5bfb45a29fce7a46

  # Decode snapshot payloads into readable fields
  dtctl get snapshots OrderController.java:306 --decode-snapshots

  # Output as JSON
  dtctl get snapshots OrderController.java:306 -o json

  # Limit number of results
  dtctl get snapshots OrderController.java:306 --limit 50

  # Scope to a time window
  dtctl get snapshots OrderController.java:306 \
    --default-timeframe-start 2024-01-01T00:00:00Z \
    --default-timeframe-end   2024-01-02T00:00:00Z`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Snapshots print through the DQL executor, not the get printer, so
			// the get-wide --fields would be silently ignored here.
			if listShapeFields(cmdContext(cmd)) != "" {
				return fmt.Errorf("--fields is not supported by get snapshots; project fields in DQL instead: dtctl query 'fetch application.snapshots | fields ...'")
			}
			cfg, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			ctx, ctxErr := cfg.CurrentContextObj()
			if ctxErr != nil {
				return ctxErr
			}

			handler, err := livedebugger.NewHandler(c, ctx.Environment)
			if err != nil {
				return err
			}

			workspaceResp, workspaceID, err := handler.GetOrCreateWorkspace(currentProjectPath())
			if err != nil {
				if isDebugVerbose(cmdContext(cmd)) {
					_ = printGraphQLResponse(cmdContext(cmd), "getOrCreateWorkspaceV2", workspaceResp)
				}
				return err
			}

			workspaceRulesResp, err := handler.GetWorkspaceRules(workspaceID)
			if err != nil {
				return err
			}

			rules, err := livedebugger.ExtractWorkspaceRules(workspaceRulesResp)
			if err != nil {
				return err
			}

			identifier := args[0]
			matchedRules, _, _, err := resolveBreakpointRulesForEdit(rules, identifier)
			if err != nil {
				return err
			}
			if len(matchedRules) == 0 {
				return fmt.Errorf("no breakpoint found with identifier %q", identifier)
			}

			rule := matchedRules[0]
			if rule.ImmutableID == "" {
				return fmt.Errorf("breakpoint %q has no snapshot ID", identifier)
			}
			dqlID := padBreakpointID(rule.ImmutableID)
			dqlQuery := fmt.Sprintf(`fetch application.snapshots | filter breakpoint.id == toUid("%s")`, dqlID)
			if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
				dqlQuery += fmt.Sprintf(" | limit %d", limit)
			}

			fmt.Fprintln(currentStderr(cmdContext(cmd)), "Note: Snapshots from the latest version of the breakpoint will be shown")

			decodeVal, _ := cmd.Flags().GetString("decode-snapshots")
			var decodeMode exec.DecodeMode
			if cmd.Flags().Changed("decode-snapshots") {
				switch decodeVal {
				case "", "simplified":
					decodeMode = exec.DecodeSimplified
				case "full":
					decodeMode = exec.DecodeFull
				default:
					return fmt.Errorf("unsupported --decode-snapshots value %q (use \"simplified\" or \"full\")", decodeVal)
				}
			}

			maxResultRecords, _ := cmd.Flags().GetInt64("max-result-records")
			defaultTimeframeStart, _ := cmd.Flags().GetString("default-timeframe-start")
			defaultTimeframeEnd, _ := cmd.Flags().GetString("default-timeframe-end")
			noProgress, _ := cmd.Flags().GetBool("no-progress")

			metadataFields, metadataDefaulted, err := resolveMetadataFlag(cmd, agentMode(cmdContext(cmd)))
			if err != nil {
				return err
			}

			executor := newDQLExecutorFromConfig(cmdContext(cmd), cfg, c)

			cancelCtx, stop := signal.NotifyContext(cmdContext(cmd), os.Interrupt, syscall.SIGTERM)
			defer stop()

			opts := exec.DQLExecuteOptions{
				OutputFormat:          outputFormat(cmdContext(cmd)),
				AgentMode:             agentMode(cmdContext(cmd)),
				Decode:                decodeMode,
				MaxResultRecords:      maxResultRecords,
				DefaultTimeframeStart: defaultTimeframeStart,
				DefaultTimeframeEnd:   defaultTimeframeEnd,
				MetadataFields:        metadataFields,
				MetadataDefaulted:     metadataDefaulted,
				Verbose:               verbosity(cmdContext(cmd)) > 0,
				ShowProgress:          !noProgress,
			}

			return executor.ExecuteWithContext(cancelCtx, dqlQuery, opts)
		},
	}
	c.Flags().Int("limit", 0, "maximum number of snapshots to return (0 = no limit)")
	c.Flags().String("decode-snapshots", "", `decode Live Debugger snapshot payloads in query results
bare --decode-snapshots simplifies variant wrappers to plain values;
--decode-snapshots=full preserves the full decoded tree with type annotations`)
	c.Flags().Lookup("decode-snapshots").NoOptDefVal = "simplified"
	c.Flags().Int64("max-result-records", 0, "maximum number of result records to return (0 = use default, typically 1000)")
	c.Flags().String("default-timeframe-start", "", "query timeframe start timestamp (ISO-8601/RFC3339, e.g., '2022-04-20T12:10:04.123Z')")
	c.Flags().String("default-timeframe-end", "", "query timeframe end timestamp (ISO-8601/RFC3339, e.g., '2022-04-20T13:10:04.123Z')")
	c.Flags().Bool("no-progress", false, "disable the live progress bar shown on stderr for long queries")
	c.Flags().StringP("metadata", "M", "", `include query metadata in output (use = for field selection)
bare --metadata or -M shows all fields; --metadata=field1,field2 selects specific fields`)
	c.Flags().Lookup("metadata").NoOptDefVal = "all"
	markLiveDebuggerExperimental(c)
	return c
}
