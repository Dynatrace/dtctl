package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// isBuiltinCommand returns true if name matches any registered Cobra command.
func isBuiltinCommand(name string) bool {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == name {
			return true
		}
		for _, alias := range cmd.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

var aliasCmd = newAliasCmd()

func newAliasCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "alias",
		Short: "Manage command aliases",
		Long: `Create, list, and delete shorthand names for dtctl commands.

Aliases expand before command parsing, so they work exactly like typing
the full command. Use positional parameters ($1, $2, ...) for reusable
templates, or prefix with ! for shell expansion.

Examples:
  # Simple alias
  dtctl alias set prod-wf "get workflows --context=production"
  dtctl prod-wf

  # Parameterized alias
  dtctl alias set wf 'get workflow $1 --context=production'
  dtctl wf my-workflow-id

  # Shell alias (pipes, jq, etc.)
  dtctl alias set wf-count '!dtctl get workflows -o json | jq length'
  dtctl wf-count`,
	}
	stability.MarkStable(c)
	return c
}

var aliasSetCmd = newAliasSetCmd()

func newAliasSetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set <name> <expansion>",
		Short: "Create or update an alias",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, expansion := args[0], args[1]

			cfg, err := loadConfigRaw(cmdContext(cmd))
			if err != nil {
				return err
			}

			if err := cfg.SetAlias(name, expansion, isBuiltinCommand); err != nil {
				return err
			}

			if err := saveConfig(cmdContext(cmd), cfg); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Alias %q set to %q", name, expansion)
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

var aliasListCmd = newAliasListCmd()

func newAliasListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "list",
		Short:   "List all aliases",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigRaw(cmdContext(cmd))
			if err != nil {
				return err
			}

			entries := cfg.ListAliases()
			if len(entries) == 0 {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "No aliases configured.")
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "Use 'dtctl alias set <name> <command>' to create one.")
				return nil
			}

			printer := newPrinterCtx(cmdContext(cmd))
			return printer.PrintList(entries)
		},
	}
	stability.MarkStable(c)
	return c
}

var aliasDeleteCmd = newAliasDeleteCmd()

func newAliasDeleteCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "delete <name> [name...]",
		Short:   "Delete one or more aliases",
		Aliases: []string{"rm"},
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigRaw(cmdContext(cmd))
			if err != nil {
				return err
			}

			for _, name := range args {
				if err := cfg.DeleteAlias(name); err != nil {
					return err
				}
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Alias %q deleted", name)
			}

			return saveConfig(cmdContext(cmd), cfg)
		},
	}
	stability.MarkStable(c)
	return c
}

var aliasExportCmd = newAliasExportCmd()

func newAliasExportCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "export",
		Short: "Export aliases to a YAML file",
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")

			cfg, err := loadConfigRaw(cmdContext(cmd))
			if err != nil {
				return err
			}

			if len(cfg.Aliases) == 0 {
				return fmt.Errorf("no aliases to export")
			}

			if err := cfg.ExportAliases(file); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Exported %d alias(es) to %s", len(cfg.Aliases), file)
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "output file path")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

var aliasImportCmd = newAliasImportCmd()

func newAliasImportCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "import",
		Short: "Import aliases from a YAML file",
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			overwrite, _ := cmd.Flags().GetBool("overwrite")

			cfg, err := loadConfigRaw(cmdContext(cmd))
			if err != nil {
				return err
			}

			conflicts, err := cfg.ImportAliases(file, overwrite, isBuiltinCommand)
			if err != nil {
				return err
			}

			if len(conflicts) > 0 && !overwrite {
				output.FprintWarning(currentStderr(cmdContext(cmd)), "Skipped %d existing alias(es): %s",
					len(conflicts), strings.Join(conflicts, ", "))
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Use --overwrite to replace existing aliases.")
			}

			if err := saveConfig(cmdContext(cmd), cfg); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Aliases imported successfully.")
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "input file path")
	c.Flags().Bool("overwrite", false, "overwrite existing aliases")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

func init() {
	rootCmd.AddCommand(aliasCmd)

	aliasCmd.AddCommand(aliasSetCmd)
	aliasCmd.AddCommand(aliasListCmd)
	aliasCmd.AddCommand(aliasDeleteCmd)
	aliasCmd.AddCommand(aliasExportCmd)
	aliasCmd.AddCommand(aliasImportCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
