package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/prompt"
	"github.com/Dynatrace/dtctl/pkg/resources/appengine"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
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

			return printer.Print(versions)
		},
	}
	stability.MarkStable(c)
	return c
}

// getRuntimeTypeDefinitionsCmd retrieves the TypeScript type definitions of an SDK package
var getRuntimeTypeDefinitionsCmd = newGetRuntimeTypeDefinitionsCmd()

func newGetRuntimeTypeDefinitionsCmd() *cobra.Command {
	var packageName string
	var majorVersion int
	c := &cobra.Command{
		Use:     "runtime-type-definitions",
		Aliases: []string{"runtime-type-definition"},
		Short:   "Get the TypeScript type definitions of an SDK package",
		Long: `Get the TypeScript type definitions (.d.ts) of a package available in the
function executor runtime, for a given major version.

Examples:
  dtctl get runtime-type-definitions --package-name @dynatrace-sdk/client-automation --major-version 8
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(packageName) == "" {
				return fmt.Errorf("--package-name must not be empty")
			}
			if majorVersion < 0 {
				return fmt.Errorf("--major-version must be a non-negative integer, got %d", majorVersion)
			}

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := appengine.NewFunctionHandler(c)

			defs, err := handler.GetRuntimeTypeDefinitions(packageName, majorVersion)
			if err != nil {
				return err
			}

			// The default view is the declaration itself: it is what the command
			// exists to return, and a table cell cannot hold a multi-line .d.ts.
			// Agent mode leaves the format at "table", so it is excluded here and
			// gets the structured envelope.
			ctx := cmdContext(cmd)
			if !agentMode(ctx) && (outputFormat(ctx) == "" || outputFormat(ctx) == "table") {
				text := defs.TypeDefinitions
				if !strings.HasSuffix(text, "\n") {
					text += "\n"
				}
				_, err := io.WriteString(currentStdoutOr(ctx, cmd.OutOrStdout()), text)
				return err
			}

			return printer.Print(defs)
		},
	}
	c.Flags().StringVar(&packageName, "package-name", "", "SDK package name, e.g. @dynatrace-sdk/client-automation (required)")
	c.Flags().IntVar(&majorVersion, "major-version", 0, "major version of the package, e.g. 8 (required)")
	_ = c.MarkFlagRequired("package-name")
	_ = c.MarkFlagRequired("major-version")
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
	markPrompts(c, "yes", "dry-run")
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
