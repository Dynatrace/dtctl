package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getAppsCmd retrieves App Engine apps
var getAppsCmd = newGetAppsCmd()

func newGetAppsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "apps [id]",
		Aliases: []string{"app"},
		Short:   "Get App Engine apps",
		Long: `Get installed App Engine apps.

Examples:
  # List all apps
  dtctl get apps

  # Get a specific app
  dtctl get app my.custom-app

  # Output as JSON
  dtctl get apps -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := appengine.NewHandler(c)

			// Get specific app if ID provided
			if len(args) > 0 {
				app, err := handler.GetApp(args[0])
				if err != nil {
					return err
				}
				return printer.Print(app)
			}

			// List all apps
			list, err := handler.ListApps()
			if err != nil {
				return err
			}

			return printer.PrintList(list.Apps)
		},
	}
	stability.MarkStable(c)
	return c
}

// getSDKVersionsCmd retrieves SDK versions for the function executor
var getSDKVersionsCmd = newGetSDKVersionsCmd()

func newGetSDKVersionsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "sdk-versions",
		Aliases: []string{"sdk-version"},
		Short:   "Get available SDK versions for function execution",
		Long: `Get available SDK versions for the function executor.

Examples:
  # List all SDK versions
  dtctl get sdk-versions

  # Output as JSON
  dtctl get sdk-versions -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := appengine.NewFunctionHandler(c)

			versions, err := handler.GetSDKVersions()
			if err != nil {
				return err
			}

			return printer.PrintList(versions.Versions)
		},
	}
	stability.MarkStable(c)
	return c
}

// deleteAppCmd deletes an app
var deleteAppCmd = newDeleteAppCmd()

func newDeleteAppCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "app <app-id>",
		Aliases: []string{"apps"},
		Short:   "Uninstall an App Engine app",
		Long: `Uninstall an App Engine app by ID.

Examples:
  # Uninstall an app
  dtctl delete app my.custom-app

  # Uninstall without confirmation
  dtctl delete app my.custom-app -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID := args[0]

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := appengine.NewHandler(c)

			// Get app for confirmation
			app, err := handler.GetApp(appID)
			if err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "app", app.Name, appID)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "app", app.Name, appID) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.DeleteApp(appID); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "App %q uninstall initiated", appID)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

func init() {
	// Delete confirmation flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
