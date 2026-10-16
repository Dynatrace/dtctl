package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/plugin"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// pluginCmd groups plugin management. dtctl follows the kubectl exec
// convention: any executable named dtctl-<name> on PATH runs as
// `dtctl <name>`. There is deliberately no installer and no registry — see
// docs/dev/PLUGIN_CONVENTIONS.md for the author-facing contract.
var pluginCmd = newPluginCmd()

func newPluginCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "plugin",
		Short: "Manage dtctl plugins (executables named dtctl-* on PATH)",
		Long: `dtctl supports kubectl-style exec plugins: any executable on PATH named
dtctl-<name> extends dtctl with the 'dtctl <name>' command. Multi-word
commands map to dash-joined names ('dtctl foo bar' runs dtctl-foo-bar,
falling back to dtctl-foo with 'bar' as an argument).

Built-in commands always win: a plugin can never shadow or override a
built-in dtctl command.

Plugins resolve configuration and credentials themselves — dtctl passes
context via environment variables (DTCTL_CONTEXT, DTCTL_CONFIG, DTCTL_AGENT,
DTCTL_PLAIN, DTCTL_CALLER_VERSION), never tokens or other secrets.

See docs/dev/PLUGIN_CONVENTIONS.md for the plugin author guide.`,
	}
	stability.MarkStable(c)
	return c
}

// pluginListCmd lists discovered plugins. Deliberately the entire management
// surface (v1): name, binary, and path for support triage, plus a warning
// when a plugin would be shadowed by a built-in command.
var pluginListCmd = newPluginListCmd()

func newPluginListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List dtctl plugins found on PATH",
		Example: `  # List all installed plugins
  dtctl plugin list

  # Machine-readable listing
  dtctl plugin list -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			plugins := plugin.Discover(os.Getenv("PATH"), builtinCommandNames())
			if plugins == nil {
				plugins = []plugin.Plugin{} // empty list, not null, in structured output
			}

			printer := newPrinterCtx(cmdContext(cmd))
			enrichAgent(printer, "plugin", "plugin")
			return printer.PrintList(plugins)
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	pluginCmd.AddCommand(pluginListCmd)
	rootCmd.AddCommand(pluginCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
