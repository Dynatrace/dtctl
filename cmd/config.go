package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// loadConfigRaw loads configuration respecting the --config flag but WITHOUT applying
// runtime overrides like --context. This is used for configuration management commands.
func loadConfigRaw(ctx context.Context) (*config.Config, error) {
	if cfgFile(ctx) != "" {
		return config.LoadFrom(cfgFile(ctx))
	}
	return config.Load()
}

// loadRawConfig loads the configuration without expanding environment variables,
// mirroring the path selection logic of LoadConfig/saveConfig.
func loadRawConfig(ctx context.Context) (*config.Config, error) {
	if cfgFile(ctx) != "" {
		return config.LoadFromWithoutExpansion(cfgFile(ctx))
	}
	return config.LoadWithoutExpansion()
}

// saveConfig saves configuration respecting the --config flag, DTCTL_CONFIG, and
// local config presence. The path selection MUST mirror the read precedence used
// by loadConfigRaw/loadRawConfig (and config.Load) so that config-management
// commands round-trip the same file they loaded — otherwise a mutation read from
// DTCTL_CONFIG would be written to a different file (global or a local
// .dtctl.yaml), silently clobbering it.
func saveConfig(ctx context.Context, cfg *config.Config) error {
	if cfgFile(ctx) != "" {
		return cfg.SaveTo(cfgFile(ctx))
	}
	// An explicit config named in the environment is trusted like --config and
	// short-circuits local discovery, so writes must target it too.
	if envPath := getenv(ctx, config.EnvConfig); envPath != "" {
		return cfg.SaveTo(envPath)
	}
	// If a local config exists, save to it
	if local := config.FindLocalConfig(); local != "" {
		return cfg.SaveTo(local)
	}
	// Fall back to default global location
	return cfg.Save()
}

// loadConfigForWrite loads the file a config-management command should modify.
// With global=true that is always the user-level config, so a discovered
// .dtctl.yaml cannot capture a write that was meant to create the global
// binding a local config depends on; otherwise it reads what loadConfigRaw
// would. The global read skips expansion so a ${VAR} in the global file
// survives the round-trip too.
func loadConfigForWrite(ctx context.Context, global bool) (*config.Config, error) {
	if global {
		return config.LoadFromWithoutExpansion(config.DefaultConfigPath())
	}
	return loadConfigRaw(ctx)
}

// saveConfigForWrite mirrors loadConfigForWrite so a load-modify-save cycle
// round-trips a single file.
func saveConfigForWrite(ctx context.Context, cfg *config.Config, global bool) error {
	if global {
		return cfg.Save()
	}
	return saveConfig(ctx, cfg)
}

// warnLocalWriteTarget tells the user when a config write just landed in an
// auto-discovered .dtctl.yaml. Such a file cannot hold credentials and only
// resolves one through a context in the global config that binds the same
// environment, so a write meant to set up access has to go to the global
// config — which is what --global is for.
func warnLocalWriteTarget(ctx context.Context, what string) {
	if cfgFile(ctx) != "" || getenv(ctx, config.EnvConfig) != "" {
		return
	}
	local := config.FindLocalConfig()
	if local == "" {
		return
	}
	output.FprintWarning(currentStderr(ctx), "%s written to the local config %s, not your global config", what, local)
	output.FprintHint(currentStderr(ctx), "A local .dtctl.yaml cannot hold credentials; it resolves one through a global context that binds the same environment. Re-run with --global to write there.")
}

// configCmd represents the config command
var configCmd = newConfigCmd()

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Manage dtctl configuration",
		Long:  `View and modify dtctl configuration including contexts and credentials.`,
	}
	stability.MarkStable(c)
	return c
}

// configViewCmd represents the config view command
var configViewCmd = newConfigViewCmd()

func newConfigViewCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "view",
		Short: "Display the current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return err
			}

			printer := newPrinterCtx(cmdContext(cmd))
			return printer.Print(cfg)
		},
	}
	stability.MarkStable(c)
	return c
}

// configInitCmd creates a .dtctl.yaml template in the current directory
var configInitCmd = newConfigInitCmd()

func newConfigInitCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "init",
		Short: "Create a .dtctl.yaml template in the current directory",
		Long: `Create a project-local .dtctl.yaml configuration template.

This creates a .dtctl.yaml file in the current directory with example
configuration that can be customized for your project. Project-local
configuration takes precedence over global configuration.

Examples:
  # Create .dtctl.yaml in current directory
  dtctl config init

  # Create .dtctl.yaml with a specific context pre-set
  dtctl config init --context production

In an auto-discovered .dtctl.yaml the environment URL supports ${VAR_NAME}
expansion (e.g. ${DT_ENVIRONMENT_URL}); it is validated as a Dynatrace host
at runtime. Inline tokens are rejected — use 'token-ref' pointing to a context
in your global config. For CI, point DTCTL_CONFIG at a trusted file instead.

Note that once a .dtctl.yaml exists, config writes target it rather than your
global config. Pass --global to 'config set-context' / 'config set-credentials'
to create the global binding this file needs.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if .dtctl.yaml already exists
			configPath := config.LocalConfigName
			if _, err := os.Stat(configPath); err == nil {
				force, _ := cmd.Flags().GetBool("force")
				if !force {
					return fmt.Errorf("%s already exists. Use --force to overwrite", configPath)
				}
			}

			// Get context from flag if provided
			contextName, _ := cmd.Flags().GetString("context")

			// Create template config
			template := createLocalConfigTemplate(contextName)

			// Write to file
			data, err := yaml.Marshal(template)
			if err != nil {
				return fmt.Errorf("failed to marshal config template: %w", err)
			}

			if err := os.WriteFile(configPath, data, 0600); err != nil {
				return fmt.Errorf("failed to write %s: %w", configPath, err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Created %s", configPath)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "\nSet DT_ENVIRONMENT_URL to your Dynatrace environment URL (or edit the file directly).")
			output.FprintInfo(currentStderr(cmdContext(cmd)), "'token-ref' must match a context in your GLOBAL config that binds the same environment.")
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Create that binding with --global, or the writes land in this file instead:")
			// Print the names the template actually used, so the commands can be
			// pasted as-is (--context is optional and the token-ref is fixed).
			name, tokenRef := template.Contexts[0].Name, template.Contexts[0].Context.TokenRef
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  dtctl config set-context %s --global \\", name)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "    --environment \"$DT_ENVIRONMENT_URL\" --token-ref %s", tokenRef)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  dtctl config set-credentials %s --global --token dt0c01.xxx", tokenRef)
			return nil
		},
	}
	c.Flags().String("context", "", "context name to use in template (default: my-environment)")
	c.Flags().Bool("force", false, "overwrite existing .dtctl.yaml")
	stability.MarkStable(c)
	return c
}

// createLocalConfigTemplate generates a template config for local projects
func createLocalConfigTemplate(contextName string) *config.Config {
	if contextName == "" {
		contextName = "my-environment"
	}

	// Inline tokens are rejected in local configs; the environment URL supports
	// ${VAR} expansion (expanded and validated at runtime in NewClientFromConfig).
	return &config.Config{
		APIVersion:     "dtctl.io/v1",
		Kind:           "Config",
		CurrentContext: contextName,
		Contexts: []config.NamedContext{
			{
				Name: contextName,
				Context: config.Context{
					Environment: "${DT_ENVIRONMENT_URL}",
					TokenRef:    "my-token",
					SafetyLevel: config.SafetyLevelReadWriteAll,
					Description: "Project environment",
				},
			},
		},
		Preferences: config.Preferences{
			Output: "table",
		},
	}
}

// ContextListItem is a flattened view of a context for table display
type ContextListItem struct {
	Current     string `table:"CURRENT"`
	Name        string `table:"NAME"`
	Environment string `table:"ENVIRONMENT"`
	SafetyLevel string `table:"SAFETY-LEVEL"`
	Profile     string `table:"PROFILE,wide"`
	Description string `table:"DESCRIPTION,wide"`
}

// configGetContextsCmd lists all contexts
var configGetContextsCmd = newConfigGetContextsCmd()

func newConfigGetContextsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "get-contexts",
		Short: "List all available contexts",
		Long: `List all available contexts with their safety levels.

Examples:
  # List contexts
  dtctl config get-contexts

  # List contexts with descriptions
  dtctl config get-contexts -o wide
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listContexts(cmdContext(cmd))
		},
	}
	stability.MarkStable(c)
	return c
}

// configCurrentContextCmd shows the current context
var configCurrentContextCmd = newConfigCurrentContextCmd()

func newConfigCurrentContextCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "current-context",
		Short: "Display the current context",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return err
			}
			fmt.Fprintln(currentStdout(cmdContext(cmd)), cfg.CurrentContext)
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

// configUseContextCmd switches to a different context
var configUseContextCmd = newConfigUseContextCmd()

func newConfigUseContextCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "use-context <context-name>",
		Short: "Switch to a different context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return useContext(cmdContext(cmd), args[0])
		},
	}
	stability.MarkStable(c)
	return c
}

// configSetContextCmd creates or updates a context
var configSetContextCmd = newConfigSetContextCmd()

func newConfigSetContextCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-context <context-name>",
		Short: "Set a context entry in the config",
		Long: `Create or update a context with connection and safety settings.

Safety Levels (from safest to most permissive):
  readonly                  - No modifications allowed (production monitoring)
  readwrite-mine            - Create/update/delete own resources only
  readwrite-all             - Modify all resources, no bucket deletion (default)
  dangerously-unrestricted  - All operations including bucket deletion

Note: Safety levels are client-side checks to prevent accidental mistakes.
For actual security, configure your API token with appropriate scopes.

Examples:
  # Create a production read-only context
  dtctl config set-context prod-viewer \
    --environment https://prod.dynatrace.com \
    --token-ref prod-token \
    --safety-level readonly

  # Create a context for team collaboration
  dtctl config set-context staging \
    --environment https://staging.dynatrace.com \
    --token-ref staging-token \
    --safety-level readwrite-all \
    --description "Staging environment"

  # Bind a command profile so an embedded agent only sees a reduced surface
  # (compose with --safety-level to also constrain what those commands may do)
  dtctl config set-context prod-agent \
    --environment https://prod.dynatrace.com \
    --token-ref prod-token \
    --profile query \
    --safety-level readonly

  # Keep a local tally of the queries dtctl runs here and the data they
  # scan (off by default; see 'dtctl usage')
  dtctl config set-context prod-viewer --usage-tracking
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setContext(cmdContext(cmd), args[0], contextSettingsFromFlags(cmd))
		},
	}
	stability.MarkStable(c)
	addContextFlags(c)
	return c
}

// configSetCredentialsCmd sets credentials for a context
var configSetCredentialsCmd = newConfigSetCredentialsCmd()

func newConfigSetCredentialsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-credentials <name>",
		Short: "Set credentials in the config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			token, _ := cmd.Flags().GetString("token")
			global, _ := cmd.Flags().GetBool("global")

			cfg, err := loadConfigForWrite(cmdContext(cmd), global)
			if err != nil {
				cfg = config.NewConfig()
			}

			if err := cfg.SetToken(name, token); err != nil {
				return err
			}

			if err := saveConfigForWrite(cmdContext(cmd), cfg, global); err != nil {
				return err
			}

			if config.IsKeyringAvailable() {
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Credentials %q stored securely in %s", name, config.KeyringBackend())
			} else {
				output.FprintWarning(currentStderr(cmdContext(cmd)), "Credentials %q set (stored in plaintext, keyring not available)", name)
			}
			if !global {
				warnLocalWriteTarget(cmdContext(cmd), fmt.Sprintf("Credential reference %q", name))
			}
			return nil
		},
	}
	c.Flags().String("token", "", "API token")
	c.Flags().Bool("global", false, "write to the global config instead of a discovered .dtctl.yaml")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "token")
	return c
}

// configDeleteCredentialsCmd removes a stored credential
var configDeleteCredentialsCmd = newConfigDeleteCredentialsCmd()

func newConfigDeleteCredentialsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "delete-credentials <name>",
		Aliases: []string{"rm-credentials"},
		Short:   "Delete stored credentials",
		Long: `Delete stored credentials.

Removes the credential from the OS keyring (or the file-based token store) and
drops its entry from the config file, including every cached OAuth access and
refresh token derived from it.

This is the supported way to remove a credential — it is what an automated
teardown step should call. Do not use OS keychain tooling ('security' on macOS,
'secret-tool' on Linux, 'cmdkey' on Windows) to remove or inspect dtctl
credentials: those commands reach far beyond dtctl's own entries, and their
read verbs print secret material.

To confirm a credential is gone, run 'dtctl auth status', which reports whether
a token is present without printing it.

Examples:
  # Remove a credential
  dtctl config delete-credentials incident-token

  # Remove a context and its credential in one step
  dtctl config delete-context incident --delete-credentials

  # Check what a teardown script would remove, without removing it
  dtctl config delete-credentials incident-token --dry-run
`,
		Args: cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) != 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			cfg, err := loadConfigRaw(cmdContext(cmd))
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var names []string
			for _, nt := range cfg.Tokens {
				names = append(names, nt.Name)
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return deleteCredentials(cmd, args[0])
		},
	}
	stability.MarkStable(c)
	return c
}

// deleteCredentials removes a stored credential and every cached derivative.
func deleteCredentials(cmd *cobra.Command, name string) error {
	// loadRawConfig, not loadConfigRaw: this command rewrites the config file,
	// and the expanding loader would resolve every ${VAR} in it and save the
	// resolved values back — writing a *different* credential's secret into the
	// file in plaintext. See CONFIG_CONTRACT.md, "Write rules".
	cfg, err := loadRawConfig(cmdContext(cmd))
	if err != nil {
		return err
	}

	// Collected before the delete: a context naming this credential is worth
	// flagging, but not worth refusing over. A credential may legitimately be
	// removed while a context still points at it (the context is simply
	// unusable until a new one is stored), and refusing would push callers back
	// to the OS keychain tooling this command exists to replace.
	var referencedBy []string
	for _, nc := range cfg.Contexts {
		if nc.Context.TokenRef == name {
			referencedBy = append(referencedBy, nc.Name)
		}
	}

	// Honored here even though the rest of the config surface ignores it: this
	// command is meant to be called from teardown scripts, and a --dry-run that
	// silently destroys a credential is the worst kind of surprise.
	if dryRun(cmdContext(cmd)) {
		report := newDryRunReport(cmd).
			Linef("Dry run: would delete credentials %q", name).
			Detail("credentials", "%s", name)
		if len(referencedBy) > 0 {
			report.Field("Referenced by context(s)", "%s", strings.Join(referencedBy, ", "))
		}
		return report.Print()
	}

	if err := cfg.DeleteToken(name); err != nil {
		return fmt.Errorf("failed to delete credentials %q: %w", name, err)
	}

	if err := saveConfig(cmdContext(cmd), cfg); err != nil {
		return err
	}

	output.FprintSuccess(currentStderr(cmdContext(cmd)), "Credentials %q deleted", name)
	if len(referencedBy) > 0 {
		output.FprintWarning(currentStderr(cmdContext(cmd)), "Context(s) %s still reference %q; store a new credential or delete the context",
			strings.Join(referencedBy, ", "), name)
	}
	return nil
}

// configSetCmd sets a configuration value
var configSetCmd = newConfigSetCmd()

func newConfigSetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Long: `Set a configuration value such as preferences.

Supported keys:
  - preferences.editor: Set the default editor for edit commands
  - development.<feature>: Enable or disable a development-tier feature
    (value: on/off). Development features are unfinished, carry no stability
    guarantees, and are not registered at all until enabled. Run
    'dtctl config list-development' to see them.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			value := args[1]

			cfg, err := loadConfigRaw(cmdContext(cmd))
			if err != nil {
				// Create new config if it doesn't exist
				cfg = config.NewConfig()
			}

			switch {
			case key == "preferences.editor":
				cfg.Preferences.Editor = value
			case strings.HasPrefix(key, "development."):
				if err := setDevelopmentKey(cfg, key, value); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown configuration key %q", key)
			}

			if err := saveConfig(cmdContext(cmd), cfg); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Configuration %q set to %q", key, value)
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

// configMigrateTokensCmd migrates tokens from config file to OS keyring
var configMigrateTokensCmd = newConfigMigrateTokensCmd()

func newConfigMigrateTokensCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "migrate-tokens",
		Short: "Migrate tokens from config file to OS keyring",
		Long: `Migrate plaintext tokens from the config file to the secure OS keyring.

This command moves tokens stored in ~/.config/dtctl/config to:
  - macOS: Keychain
  - Linux: Secret Service (GNOME Keyring, KWallet)
  - Windows: Credential Manager

After migration, tokens are removed from the config file and stored securely.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !config.IsKeyringAvailable() {
				return fmt.Errorf("keyring not available on this system. Tokens will remain in config file")
			}

			cfg, err := loadConfigRaw(cmdContext(cmd))
			if err != nil {
				return err
			}

			migrated, err := config.MigrateTokensToKeyring(cfg)
			if err != nil {
				return err
			}

			if migrated == 0 {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "No tokens to migrate (already migrated or none configured)")
				return nil
			}

			if err := saveConfig(cmdContext(cmd), cfg); err != nil {
				return fmt.Errorf("failed to save config after migration: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Migrated %d token(s) to %s", migrated, config.KeyringBackend())
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

// configDescribeContextCmd shows detailed info about a context
var configDescribeContextCmd = newConfigDescribeContextCmd()

func newConfigDescribeContextCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "describe-context <context-name>",
		Aliases: []string{"desc-ctx"},
		Short:   "Show detailed information about a context",
		Long: `Show detailed information about a context including its safety level and settings.

Examples:
  # Describe the current context
  dtctl config describe-context $(dtctl config current-context)

  # Describe a specific context
  dtctl config describe-context production
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return describeContext(cmdContext(cmd), args[0])
		},
	}
	stability.MarkStable(c)
	return c
}

// configDeleteContextCmd deletes a context from the configuration
var configDeleteContextCmd = newConfigDeleteContextCmd()

func newConfigDeleteContextCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "delete-context <context-name>",
		Aliases: []string{"rm-ctx"},
		Short:   "Delete a context from the config",
		Long: `Delete a context from the configuration.

If the deleted context is the current context, the current-context will be cleared.
You will need to use 'dtctl config use-context' to set a new current context.

By default the credential the context references is left in place, because a
token ref can be shared between contexts. Pass --delete-credentials to remove
it as well, or remove it separately with 'dtctl config delete-credentials'.

Examples:
  # Delete a context, keeping its credential
  dtctl config delete-context old-env

  # Delete a context and the credential it references
  dtctl config delete-context staging --delete-credentials
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deleteCredential, _ := cmd.Flags().GetBool("delete-credentials")
			return deleteContext(cmd, args[0], deleteCredential)
		},
	}
	c.Flags().Bool("delete-credentials", false,
		"also delete the credential the context references (leaves it in place otherwise)")
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(configCmd)

	configCmd.AddCommand(configViewCmd)
	configCmd.AddCommand(configInitCmd)
	configCmd.AddCommand(configGetContextsCmd)
	configCmd.AddCommand(configCurrentContextCmd)
	configCmd.AddCommand(configUseContextCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configSetContextCmd)
	configCmd.AddCommand(configSetCredentialsCmd)
	configCmd.AddCommand(configDeleteCredentialsCmd)
	configCmd.AddCommand(configMigrateTokensCmd)
	configCmd.AddCommand(configDescribeContextCmd)
	configCmd.AddCommand(configDeleteContextCmd)
	configCmd.AddCommand(configListDevelopmentCmd)

}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
