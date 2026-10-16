package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/settings"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getSettingsSchemasCmd retrieves settings schemas
var getSettingsSchemasCmd = newGetSettingsSchemasCmd()

func newGetSettingsSchemasCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "settings-schemas [schema-id]",
		Aliases: []string{"settings-schema", "schemas", "schema"},
		Short:   "Get settings schemas",
		Long: `Get available settings schemas.

Examples:
  # List all settings schemas
  dtctl get settings-schemas

  # Get a specific schema definition
  dtctl get settings-schema builtin:openpipeline.logs.pipelines

  # Output as JSON
  dtctl get settings-schemas -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := settings.NewHandler(c)

			// Get specific schema if ID provided
			if len(args) > 0 {
				schema, err := handler.GetSchema(args[0])
				if err != nil {
					return err
				}
				return printer.Print(schema)
			}

			// List all schemas
			list, err := handler.ListSchemas()
			if err != nil {
				return err
			}

			return printer.PrintList(list.Items)
		},
	}
	stability.MarkStable(c)
	return c
}

// getSettingsCmd retrieves settings objects
var getSettingsCmd = newGetSettingsCmd()

func newGetSettingsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "settings [object-id]",
		Aliases: []string{"setting"},
		Short:   "Get settings objects",
		Long: `Get settings objects for a schema, or a specific object by objectId.

Examples:
  # List settings objects for a schema
  dtctl get settings --schema builtin:openpipeline.logs.pipelines

  # List settings with a specific scope
  dtctl get settings --schema builtin:openpipeline.logs.pipelines --scope environment

  # Get a specific settings object by objectId
  dtctl get settings vu9U3hXa3q0AAAABABRidWlsdGluOnJ1bS53ZWIubmFtZQ...

  # Output as JSON
  dtctl get settings --schema builtin:openpipeline.logs.pipelines -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			schemaID, _ := cmd.Flags().GetString("schema")
			scope, _ := cmd.Flags().GetString("scope")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := settings.NewHandler(c)

			// Get specific object if ID provided
			if len(args) > 0 {
				obj, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.Print(obj)
			}

			// List objects for schema
			if schemaID == "" {
				return fmt.Errorf("--schema is required when listing settings objects")
			}

			list, err := handler.ListObjects(schemaID, scope, getChunkSize(cmdContext(cmd)))
			if err != nil {
				return err
			}

			return printer.PrintList(list.Items)
		},
	}
	c.Flags().String("schema", "", "Schema ID (required when listing settings objects)")
	c.Flags().String("scope", "", "Scope to filter settings (e.g., 'environment')")
	stability.MarkStable(c)
	// Required only when listing, so only an explicitly empty value is rejected.
	rejectEmptyFlag(c, "schema")
	return c
}

// deleteSettingsCmd deletes a settings object
var deleteSettingsCmd = newDeleteSettingsCmd()

func newDeleteSettingsCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:   "settings <object-id>",
		Short: "Delete a settings object",
		Long: `Delete a settings object by objectId.

Examples:
  # Delete by objectId
  dtctl delete settings vu9U3hXa3q0AAAABABRidWlsdGluOnJ1bS53ZWIubmFtZQ...

  # Delete without confirmation
  dtctl delete settings <object-id> -y
`,
		Aliases: []string{"setting"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			objectID := args[0]

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := settings.NewHandler(c)

			// Get current settings object for confirmation
			obj, err := handler.Get(objectID)
			if err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "settings object", obj.Summary, objectID)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				summary := obj.Summary
				if summary == "" {
					summary = obj.SchemaID
				}
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "settings object", summary, objectID) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.Delete(objectID); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Settings object %q deleted", objectID)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

func init() {
	// Settings flags
	// Delete settings flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
