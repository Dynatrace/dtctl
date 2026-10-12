package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/diagnostic"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// ctxCmd is a top-level shortcut for context management.
// It provides quick access to the most common context operations
// without the "config" prefix.
var ctxCmd = newCtxCmd()

func newCtxCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ctx [context-name]",
		Short: "Manage contexts (shortcut for config context commands)",
		Long: `Quick context management without the "config" prefix.

When called without arguments, lists all contexts.
When called with a context name, switches to that context.

Examples:
  # List all contexts
  dtctl ctx

  # Switch to a context
  dtctl ctx production

  # Show current context
  dtctl ctx current

  # Describe a context
  dtctl ctx describe production

  # Create or update a context (also switches to it)
  dtctl ctx set staging --environment https://staging.example.com

  # Delete a context
  dtctl ctx delete old-env
`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) != 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var names []string
			for _, nc := range cfg.Contexts {
				names = append(names, nc.Name)
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				// No args: list contexts (same as config get-contexts)
				return listContexts(cmdContext(cmd))
			}

			// One arg: switch to that context (same as config use-context)
			return useContext(cmdContext(cmd), args[0])
		},
	}
	stability.MarkStable(c)
	return c
}

// ctxTokenCmd prints the resolved token for the current (or named) context.
// It prints the raw credential only — not a full Authorization header value
// (no "Bearer "/"Api-Token " scheme prefix), so callers must add the scheme themselves.
// OAuth tokens are auto-refreshed if expired, so the printed value matches what
// dtctl itself sends.
var ctxTokenCmd = newCtxTokenCmd()

func newCtxTokenCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "token [context-name]",
		Short: "Print the resolved token for a context",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return err
			}
			name := cfg.CurrentContext
			if len(args) == 1 {
				name = args[0]
			}
			nc, err := cfg.GetContext(name)
			if err != nil {
				return err
			}
			tok, err := client.GetTokenForContext(cfg, nc.Context.Environment, nc.Context.TokenRef)
			if err != nil {
				return err
			}
			fmt.Fprintln(currentStdout(cmdContext(cmd)), tok)
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

// ctxCurrentCmd shows the current context name
var ctxCurrentCmd = newCtxCurrentCmd()

func newCtxCurrentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "current",
		Short: "Display the current context name",
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

// ctxDescribeCmd shows detailed context information
var ctxDescribeCmd = newCtxDescribeCmd()

func newCtxDescribeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "describe <context-name>",
		Short: "Show detailed information about a context",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) != 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var names []string
			for _, nc := range cfg.Contexts {
				names = append(names, nc.Name)
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return describeContext(cmdContext(cmd), args[0])
		},
	}
	stability.MarkStable(c)
	return c
}

// ctxSetCmd creates or updates a context and activates it
var ctxSetCmd = newCtxSetCmd()

func newCtxSetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set <context-name>",
		Short: "Create or update a context and set it as current",
		Long: `Create or update a context with connection and safety settings.

The context is always set as the current context after being created or updated.
To switch to an existing context without changing its settings, use: dtctl ctx <name>

Safety Levels (from safest to most permissive):
  readonly                  - No modifications allowed
  readwrite-mine            - Create/update/delete own resources only
  readwrite-all             - Modify all resources, no bucket deletion (default)
  dangerously-unrestricted  - All operations including bucket deletion

Examples:
  dtctl ctx set prod --environment https://prod.example.com --safety-level readonly
  dtctl ctx set staging --environment https://staging.example.com --token-ref my-token
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

// ctxDeleteCmd deletes a context
var ctxDeleteCmd = newCtxDeleteCmd()

func newCtxDeleteCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "delete <context-name>",
		Aliases: []string{"rm"},
		Short:   "Delete a context",
		Args:    cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) != 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var names []string
			for _, nc := range cfg.Contexts {
				names = append(names, nc.Name)
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
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

// listContexts lists all available contexts (shared logic)
func listContexts(ctx context.Context) error {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return err
	}

	var items []ContextListItem
	for _, nc := range cfg.Contexts {
		current := ""
		if nc.Name == cfg.CurrentContext {
			current = "*"
		}
		// Show what is resolved and enforced, not what the file happens to
		// say: a local .dtctl.yaml may name its environment through ${VAR}
		// and has its declared safety level clamped to the global binding.
		items = append(items, ContextListItem{
			Current:     current,
			Name:        nc.Name,
			Environment: nc.Context.Environment,
			SafetyLevel: cfg.EffectiveSafetyLevelFor(&nc.Context).String(),
			Profile:     nc.Context.Profile,
			Description: nc.Context.Description,
		})
	}

	printer := newPrinterCtx(ctx)
	return printer.PrintList(items)
}

// useContext switches to a named context (shared logic)
func useContext(ctx context.Context, name string) error {
	cfg, err := loadConfigRaw(ctx)
	if err != nil {
		return err
	}

	found := false
	for _, nc := range cfg.Contexts {
		if nc.Name == name {
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("context %q not found", name)
	}

	cfg.CurrentContext = name

	if err := saveConfig(ctx, cfg); err != nil {
		return err
	}

	output.FprintSuccess(currentStderr(ctx), "Switched to context %q", name)
	return nil
}

// describeContext shows detailed info about a named context (shared logic)
func describeContext(ctx context.Context, name string) error {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return err
	}

	var found *config.NamedContext
	for i := range cfg.Contexts {
		if cfg.Contexts[i].Name == name {
			found = &cfg.Contexts[i]
			break
		}
	}

	if found == nil {
		return fmt.Errorf("context %q not found", name)
	}

	isCurrent := found.Name == cfg.CurrentContext
	currentMark := ""
	if isCurrent {
		currentMark = " (current)"
	}

	// Report the enforced level, which for a local .dtctl.yaml is clamped to
	// the global binding of its token-ref rather than what the file declares.
	level := cfg.EffectiveSafetyLevelFor(&found.Context)

	const w = 14
	output.FprintDescribeKV(currentStdout(ctx), "Name:", w, "%s%s", found.Name, currentMark)
	output.FprintDescribeKV(currentStdout(ctx), "Environment:", w, "%s", found.Context.Environment)
	output.FprintDescribeKV(currentStdout(ctx), "Token-Ref:", w, "%s", found.Context.TokenRef)
	output.FprintDescribeKV(currentStdout(ctx), "Safety Level:", w, "%s", level)

	switch level {
	case config.SafetyLevelReadOnly:
		fmt.Fprintf(currentStdout(ctx), "%*s(No modifications allowed)\n", w, "")
	case config.SafetyLevelReadWriteMine:
		fmt.Fprintf(currentStdout(ctx), "%*s(Create/update/delete own resources)\n", w, "")
	case config.SafetyLevelReadWriteAll:
		fmt.Fprintf(currentStdout(ctx), "%*s(Modify all resources, no bucket deletion)\n", w, "")
	case config.SafetyLevelDangerouslyUnrestricted:
		fmt.Fprintf(currentStdout(ctx), "%*s(All operations including bucket deletion)\n", w, "")
	}

	if found.Context.Profile != "" {
		output.FprintDescribeKV(currentStdout(ctx), "Profile:", w, "%s", found.Context.Profile)
		fmt.Fprintf(currentStdout(ctx), "%*s(Restricts the visible command surface)\n", w, "")
	}

	if found.Context.Description != "" {
		output.FprintDescribeKV(currentStdout(ctx), "Description:", w, "%s", found.Context.Description)
	}

	if cfg.UsageTrackingOn(found.Name) {
		output.FprintDescribeKV(currentStdout(ctx), "Usage:", w, "%s", "tracked locally (see 'dtctl usage')")
	}

	return nil
}

// contextSettings is the set of context fields `ctx set` / `config set-context`
// accept. Grouped into a struct rather than passed positionally because the
// two commands share the whole list and it keeps growing — the stability floor
// is the third axis to land on a context, after safety level and profile.
type contextSettings struct {
	environment string
	tokenRef    string
	safetyLevel string
	description string
	profile     string
	// minStability is the context's stability floor. Empty leaves it unset,
	// which resolves to the default floor.
	minStability string
	// stabilityExceptions are individual commands and flags admitted below the
	// floor. nil leaves the existing list untouched (the flag was not passed);
	// a non-nil empty slice clears it.
	stabilityExceptions []string
	// usageTracking switches the context's local usage ledger on or off. nil
	// leaves it untouched (the flag was not passed).
	usageTracking *bool
	// global writes to the global config instead of a discovered .dtctl.yaml.
	global bool
}

// contextSettingsFromFlags reads the shared context flags off a command. Both
// `ctx set` and `config set-context` declare the same flag set, so both read it
// the same way.
func contextSettingsFromFlags(cmd *cobra.Command) contextSettings {
	s := contextSettings{}
	s.environment, _ = cmd.Flags().GetString("environment")
	s.tokenRef, _ = cmd.Flags().GetString("token-ref")
	s.safetyLevel, _ = cmd.Flags().GetString("safety-level")
	s.description, _ = cmd.Flags().GetString("description")
	s.profile, _ = cmd.Flags().GetString("profile")
	s.minStability, _ = cmd.Flags().GetString("min-stability")
	s.global, _ = cmd.Flags().GetBool("global")
	if cmd.Flags().Changed("usage-tracking") {
		on, _ := cmd.Flags().GetBool("usage-tracking")
		s.usageTracking = &on
	}
	if cmd.Flags().Changed("stability-exception") {
		vals, _ := cmd.Flags().GetStringArray("stability-exception")
		// Non-nil even when empty: `--stability-exception ""` is how a caller
		// withdraws every exception, which must be distinguishable from not
		// having passed the flag at all.
		s.stabilityExceptions = make([]string, 0, len(vals))
		for _, v := range vals {
			if v = strings.TrimSpace(v); v != "" {
				s.stabilityExceptions = append(s.stabilityExceptions, v)
			}
		}
	}
	return s
}

// addContextFlags declares the shared context flags on a command.
func addContextFlags(cmd *cobra.Command) {
	cmd.Flags().String("environment", "", "environment URL")
	// Required only for a new context (an update keeps the stored URL), so
	// only an explicitly empty value is rejected here.
	rejectEmptyFlag(cmd, "environment")
	cmd.Flags().String("token-ref", "", "token reference name")
	cmd.Flags().String("safety-level", "", "safety level (readonly, readwrite-mine, readwrite-all, dangerously-unrestricted)")
	cmd.Flags().String("description", "", "human-readable description for this context")
	cmd.Flags().String("profile", "", "command profile to bind (restricts the visible command surface; e.g. query, investigate, full)")
	cmd.Flags().String("min-stability", "", "stability floor: weakest contract a command or flag may offer here (stable, experimental)")
	cmd.Flags().StringArray("stability-exception", nil, "admit one below-floor command or flag (repeatable; e.g. 'inventory' or 'query --decode-snapshots')")
	cmd.Flags().Bool("global", false, "write to the global config instead of a discovered .dtctl.yaml")
	cmd.Flags().Bool("usage-tracking", false, "keep a local tally of the DQL queries dtctl runs in this context and the data they scan (see 'dtctl usage'; --usage-tracking=false turns it off)")
	stability.MarkFlag(cmd, "usage-tracking", stability.Experimental, "0.43.0")
	registerFlagCompletion(cmd, "profile", completeProfileNames)
	registerFlagCompletion(cmd, "min-stability", completeStabilityLevels)
}

// setContext creates or updates a named context (shared logic)
func setContext(ctx context.Context, name string, s contextSettings) error {
	environment := s.environment
	tokenRef := s.tokenRef
	safetyLevel := s.safetyLevel
	description := s.description
	profile := s.profile
	global := s.global
	cfg, err := loadConfigForWrite(ctx, global)
	if err != nil {
		cfg = config.NewConfig()
	}

	// Check if this is an update
	isUpdate := false
	for _, nc := range cfg.Contexts {
		if nc.Name == name {
			isUpdate = true
			if environment == "" {
				environment = nc.Context.Environment
			}
			break
		}
	}

	if !isUpdate && environment == "" {
		return fmt.Errorf("--environment is required for new contexts")
	}

	// Warn about potentially wrong environment URLs
	if environment != "" {
		if problems := diagnostic.CheckEnvironmentURL(environment); len(problems) > 0 {
			for _, p := range problems {
				output.FprintWarning(currentStderr(ctx), "%s", p.Message)
				if p.SuggestedURL != "" {
					output.FprintHint(currentStderr(ctx), "Did you mean: %s", p.SuggestedURL)
				}
			}
			fmt.Fprintln(currentStderr(ctx))
		}
	}

	if safetyLevel != "" {
		level := config.SafetyLevel(safetyLevel)
		if !level.IsValid() {
			return fmt.Errorf("invalid safety level %q. Valid values: readonly, readwrite-mine, readwrite-all, dangerously-unrestricted", safetyLevel)
		}
	}

	// Warn (don't fail) on a profile name that is not currently resolvable: the
	// profile may be defined later, or in a different config file. A soft warning
	// catches the common typo without blocking legitimate ahead-of-time binding.
	if profile != "" && profile != config.ProfileFull && !cfg.ProfileExists(profile) {
		output.FprintWarning(currentStderr(ctx), "profile %q is not defined yet; define it under 'profiles:' or it will error when the context is used", profile)
	}

	// An invalid floor is a hard error, not a silent fallback: for a floor,
	// falling back would *widen* the surface the context accepts.
	minStability, err := config.ParseStabilityLevel(s.minStability)
	if err != nil {
		return err
	}
	if _, err := stability.ParseExceptions(s.stabilityExceptions); err != nil {
		return err
	}

	// The usage ledger writes to the user's disk, so only the global config can
	// opt in (see Config.UsageTrackingOn); turning it off locally is harmless.
	if s.usageTracking != nil && *s.usageTracking && cfg.IsLocal() {
		return fmt.Errorf("usage tracking can only be turned on in the global config, not the local config %s; re-run with --global", cfg.LocalConfigPath())
	}

	opts := &config.ContextOptions{
		SafetyLevel:         config.SafetyLevel(safetyLevel),
		Description:         description,
		Profile:             profile,
		MinStability:        minStability,
		StabilityExceptions: s.stabilityExceptions,
		UsageTracking:       s.usageTracking,
	}

	cfg.SetContextWithOptions(name, environment, tokenRef, opts)

	// Always activate the context that was just created or updated.
	// "ctx set" is the canonical way to configure a context, so the natural
	// expectation is that the named context becomes current afterward.
	cfg.CurrentContext = name

	if err := saveConfigForWrite(ctx, cfg, global); err != nil {
		return err
	}

	if isUpdate {
		output.FprintSuccess(currentStderr(ctx), "Context %q updated and set as current", name)
	} else {
		output.FprintSuccess(currentStderr(ctx), "Context %q created and set as current", name)
	}
	if s.usageTracking != nil {
		if *s.usageTracking {
			output.FprintInfo(currentStderr(ctx), "Usage tracking is on for %q: dtctl keeps a local per-day tally of its queries and the data they scan. View it with 'dtctl usage'.", name)
		} else {
			output.FprintInfo(currentStderr(ctx), "Usage tracking is off for %q. Tallies already recorded are kept; clear them with 'dtctl usage reset'.", name)
		}
	}
	if !global {
		warnLocalWriteTarget(ctx, fmt.Sprintf("Context %q", name))
	}
	return nil
}

// deleteContext deletes a named context (shared logic).
//
// deleteCredential also removes the credential the context references. It is
// opt-in because a token ref can be shared between contexts, but the default
// leaves a usable credential behind, so that case is called out explicitly —
// a silently orphaned token is what sends callers to the OS keychain tooling.
func deleteContext(cmd *cobra.Command, name string, deleteCredential bool) error {
	// loadRawConfig, not loadConfigRaw: this command rewrites the config file,
	// and the expanding loader would resolve every ${VAR} in it and save the
	// resolved values back — writing credentials into the file in plaintext.
	// See CONFIG_CONTRACT.md, "Write rules".
	cfg, err := loadRawConfig(cmdContext(cmd))
	if err != nil {
		return err
	}

	var tokenRef string
	found := false
	for _, nc := range cfg.Contexts {
		if nc.Name == name {
			tokenRef = nc.Context.TokenRef
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("context %q not found", name)
	}

	// Validated ahead of the dry-run return so a preview reports the same
	// refusal the real run would, rather than promising a deletion that fails.
	if deleteCredential && tokenRef != "" {
		sharedWith := 0
		for _, nc := range cfg.Contexts {
			if nc.Name != name && nc.Context.TokenRef == tokenRef {
				sharedWith++
			}
		}
		if sharedWith > 0 {
			return fmt.Errorf("credential %q is shared with %d other context(s); "+
				"delete those first, or run 'dtctl config delete-credentials %s' to remove it for all of them",
				tokenRef, sharedWith, tokenRef)
		}
	}

	if dryRun(cmdContext(cmd)) {
		report := newDryRunReport(cmd).
			Linef("Dry run: would delete context %q", name).
			Detail("context", "%s", name)
		if deleteCredential && tokenRef != "" {
			report.
				Linef("Would also delete credentials %q", tokenRef).
				Detail("credentials", "%s", tokenRef)
		}
		return report.Print()
	}

	if deleteCredential && tokenRef != "" {
		if err := cfg.DeleteToken(tokenRef); err != nil {
			return fmt.Errorf("failed to delete credential %q: %w", tokenRef, err)
		}
	}

	if err := cfg.DeleteContext(name); err != nil {
		return err
	}

	if cfg.CurrentContext == name {
		cfg.CurrentContext = ""
		output.FprintWarning(currentStderr(cmdContext(cmd)), "Deleted the current context. Use 'dtctl ctx <name>' to set a new one.")
	}

	if err := saveConfig(cmdContext(cmd), cfg); err != nil {
		return err
	}

	output.FprintSuccess(currentStderr(cmdContext(cmd)), "Context %q deleted", name)

	switch {
	case tokenRef == "":
		// Nothing was referenced, so nothing can be left behind.
	case deleteCredential:
		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Credentials %q deleted", tokenRef)
	default:
		output.FprintInfo(currentStderr(cmdContext(cmd)), "Credentials %q were kept. Remove them with 'dtctl config delete-credentials %s'.", tokenRef, tokenRef)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(ctxCmd)

	ctxCmd.AddCommand(ctxTokenCmd)
	ctxCmd.AddCommand(ctxCurrentCmd)
	ctxCmd.AddCommand(ctxDescribeCmd)
	ctxCmd.AddCommand(ctxSetCmd)
	ctxCmd.AddCommand(ctxDeleteCmd)

}

// completeStabilityLevels provides shell completion for a --min-stability flag.
// Development is deliberately absent: it is not a floor anyone sets, it is the
// tier a feature must be opted into individually.
func completeStabilityLevels(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return []string{
		string(config.StabilityStable),
		string(config.StabilityExperimental),
	}, cobra.ShellCompDirectiveNoFileComp
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
