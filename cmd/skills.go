package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/skills"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/suggest"
)

// skillsCmd is the parent command for AI assistant skill management.
var skillsCmd = newSkillsCmd()

func newSkillsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "skills",
		Short: "Manage AI coding assistant skill files",
		Long: `Manage dtctl skill files for AI coding assistants.

Skill files teach your AI assistant how to use dtctl effectively.
Follows the agentskills.io open standard for skill installation.
Supported agents: claude, codex, copilot, cursor, junie, kiro, opencode, openclaw.

Use --cross-client to install to the shared ~/.agents/skills/ directory,
which is automatically discovered by any agentskills.io-compatible agent.`,
		Example: `  # Auto-detect agent and install skill file
  dtctl skills install

  # Install for a specific agent
  dtctl skills install --for claude

  # Install to cross-client shared directory
  dtctl skills install --cross-client

  # Install cross-client globally (~/.agents/skills/dtctl)
  dtctl skills install --cross-client --global

  # List all supported agents
  dtctl skills install --list

  # Check what's installed
  dtctl skills status`,
		RunE: requireSkillsSubcommand,
	}
	stability.MarkStable(c)
	return c
}

// requireSkillsSubcommand returns a helpful error when no subcommand is given.
// Unlike requireSubcommand (which says "resource type"), this uses wording
// appropriate for a utility command whose subcommands are verbs.
func requireSkillsSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		var subs []string
		for _, sub := range cmd.Commands() {
			if sub.IsAvailableCommand() {
				subs = append(subs, sub.Name())
			}
		}
		return fmt.Errorf("requires a subcommand\n\nAvailable subcommands:\n  %s\n\nUsage:\n  %s <subcommand> [flags]",
			strings.Join(subs, "\n  "), cmd.CommandPath())
	}

	subcommands := collectSubcommands(cmd)
	if suggestion := suggest.FindClosest(args[0], subcommands); suggestion != nil {
		return fmt.Errorf("unknown subcommand %q, did you mean %q?", args[0], suggestion.Value)
	}

	return fmt.Errorf("unknown subcommand %q\nRun '%s --help' for available subcommands", args[0], cmd.CommandPath())
}

// skillsInstallCmd installs skill files for an AI coding assistant.
var skillsInstallCmd = newSkillsInstallCmd()

func newSkillsInstallCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "install",
		Short: "Install skill file for an AI coding assistant",
		Long: `Install the dtctl skill directory for the specified AI coding assistant.

Skills are installed as directories following the agentskills.io standard:
  <agent-config>/skills/dtctl/SKILL.md     (main skill document)
  <agent-config>/skills/dtctl/references/   (reference files loaded on demand)

If no agent is specified with --for, the command auto-detects the current
agent from environment variables. Use --global to install to the user-wide
location instead of the project directory.

Use --cross-client to install to the shared .agents/skills/ directory defined
by the agentskills.io convention. Skills installed here are automatically
discovered by any compatible agent without needing per-agent installation.

Examples:
  # Auto-detect and install
  dtctl skills install

  # Install for Claude Code
  dtctl skills install --for claude

  # Install to cross-client shared directory (project-local)
  dtctl skills install --cross-client

  # Install cross-client globally (~/.agents/skills/dtctl)
  dtctl skills install --cross-client --global

  # Install globally
  dtctl skills install --for claude --global

  # Overwrite existing installation
  dtctl skills install --for claude --force

  # List supported agents
  dtctl skills install --list`,
		RunE: runSkillsInstall,
	}
	c.Flags().String("for", "", "install for a specific agent (claude, codex, copilot, cursor, junie, kiro, opencode, openclaw)")
	c.Flags().Bool("cross-client", false, "install to the shared .agents/skills/ directory (agentskills.io convention)")
	c.Flags().Bool("global", false, "install to user-wide location instead of project directory")
	c.Flags().Bool("force", false, "overwrite existing files without prompting")
	c.Flags().Bool("list", false, "list all supported agents")
	stability.MarkStable(c)
	registerFlagCompletion(c, "for", agentCompletionFunc)
	return c
}

// skillsUninstallCmd removes installed skill files.
var skillsUninstallCmd = newSkillsUninstallCmd()

func newSkillsUninstallCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove installed skill files",
		Long: `Remove dtctl skill files installed for an AI coding assistant.

If no agent is specified with --for, the command auto-detects the current
agent. Removes skill directories from both project-local and global locations.

Use --cross-client to remove skills from the shared .agents/skills/ directory.

Examples:
  # Auto-detect and uninstall
  dtctl skills uninstall

  # Uninstall for a specific agent
  dtctl skills uninstall --for claude

  # Uninstall from cross-client directory
  dtctl skills uninstall --cross-client`,
		RunE: runSkillsUninstall,
	}
	c.Flags().String("for", "", "uninstall for a specific agent")
	c.Flags().Bool("cross-client", false, "uninstall from the shared .agents/skills/ directory")
	stability.MarkStable(c)
	registerFlagCompletion(c, "for", agentCompletionFunc)
	return c
}

// skillsStatusCmd shows the installation state of skill files.
var skillsStatusCmd = newSkillsStatusCmd()

func newSkillsStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show installation status of skill files",
		Long: `Show the current installation status of dtctl skill files.

Checks both project-local and global locations for all supported agents,
including the cross-client shared directory (.agents/skills/).

Examples:
  # Check all agents (including cross-client)
  dtctl skills status

  # Check a specific agent
  dtctl skills status --for claude

  # Check cross-client directory only
  dtctl skills status --for cross-client`,
		RunE: runSkillsStatus,
	}
	c.Flags().String("for", "", "check status for a specific agent (or \"cross-client\")")
	stability.MarkStable(c)
	registerFlagCompletion(c, "for", agentCompletionFunc)
	return c
}

// agentCompletionFunc provides shell completion for the --for flag.
// Includes cross-client as a valid completion for status queries.
func agentCompletionFunc(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	completions := []string{
		skills.CrossClientAgent.Name + "\t" + skills.CrossClientAgent.DisplayName,
	}
	for _, a := range skills.AllAgents() {
		completions = append(completions, a.Name+"\t"+a.DisplayName)
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	rootCmd.AddCommand(skillsCmd)

	skillsCmd.AddCommand(skillsInstallCmd)
	skillsCmd.AddCommand(skillsUninstallCmd)
	skillsCmd.AddCommand(skillsStatusCmd)

}

// skillsInstallAgentResult is the structured result for agent-mode output.
type skillsInstallAgentResult struct {
	Action string `json:"action"`
	Agent  string `json:"agent"`
	Path   string `json:"path"`
	Scope  string `json:"scope"`
}

// skillsUninstallAgentResult is the structured result for agent-mode output.
type skillsUninstallAgentResult struct {
	Agent   string   `json:"agent"`
	Removed []string `json:"removed"`
}

// skillsStatusAgentEntry is a single agent's status for agent-mode output.
type skillsStatusAgentEntry struct {
	Agent     string `json:"agent"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Scope     string `json:"scope,omitempty"`
}

// skillsListAgentEntry is a single agent's info for agent-mode --list output.
type skillsListAgentEntry struct {
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	ProjectPath    string `json:"project_path"`
	SupportsGlobal bool   `json:"supports_global"`
}

// runSkillsInstall installs skill files.
func runSkillsInstall(cmd *cobra.Command, _ []string) error {
	listFlag, _ := cmd.Flags().GetBool("list")
	if listFlag {
		return runSkillsList(cmdContext(cmd))
	}

	forFlag, _ := cmd.Flags().GetString("for")
	crossClientFlag, _ := cmd.Flags().GetBool("cross-client")
	globalFlag, _ := cmd.Flags().GetBool("global")
	forceFlag, _ := cmd.Flags().GetBool("force")

	if crossClientFlag && forFlag != "" {
		return fmt.Errorf("--cross-client and --for cannot be used together")
	}

	var agent skills.Agent
	if crossClientFlag {
		agent = skills.CrossClientAgent
	} else {
		var err error
		agent, err = resolveAgent(forFlag)
		if err != nil {
			return err
		}
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to determine working directory: %w", err)
	}

	result, err := skills.Install(agent, baseDir, globalFlag, forceFlag)
	if err != nil {
		return err
	}

	scope := "project"
	if result.Global {
		scope = "global"
	}

	// Best-effort nudge toward the Dynatrace for AI domain skills; it never
	// affects the outcome of the install.
	companionHint := ""
	if !skills.CompanionInstalled(agent, baseDir, result.Path) {
		companionHint = "For DQL and observability domain skills, also install Dynatrace for AI: " + skills.CompanionInstallCommand
	}

	printer := newPrinterCtx(cmdContext(cmd))
	if ap := enrichAgent(printer, "install", "skills"); ap != nil {
		suggestions := []string{
			"Run 'dtctl skills status' to verify installation",
		}
		if companionHint != "" {
			suggestions = append(suggestions, companionHint)
		}
		ap.SetSuggestions(suggestions)
	}

	if agentMode(cmdContext(cmd)) {
		action := "installed"
		if result.Replaced {
			action = "updated"
		}
		return printer.Print(skillsInstallAgentResult{
			Action: action,
			Agent:  result.Agent.Name,
			Path:   result.Path,
			Scope:  scope,
		})
	}

	if result.Replaced {
		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Updated %s skill: %s", result.Agent.DisplayName, result.Path)
	} else {
		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Installed %s skill: %s", result.Agent.DisplayName, result.Path)
	}
	output.FprintInfo(currentStderr(cmdContext(cmd)), "Scope: %s", scope)
	if companionHint != "" {
		output.FprintHint(currentStderr(cmdContext(cmd)), "%s", companionHint)
	}

	return nil
}

// runSkillsList lists all supported agents.
func runSkillsList(ctx context.Context) error {
	printer := newPrinterCtx(ctx)
	allAgents := skills.AllAgents()
	if ap := enrichAgent(printer, "list", "skills"); ap != nil {
		ap.SetTotal(len(allAgents) + 1) // +1 for cross-client
	}

	if agentMode(ctx) {
		var entries []skillsListAgentEntry
		// Cross-client entry first
		entries = append(entries, skillsListAgentEntry{
			Name:           skills.CrossClientAgent.Name,
			DisplayName:    skills.CrossClientAgent.DisplayName,
			ProjectPath:    skills.CrossClientAgent.ProjectPath,
			SupportsGlobal: skills.CrossClientAgent.GlobalPath != "",
		})
		for _, a := range allAgents {
			entries = append(entries, skillsListAgentEntry{
				Name:           a.Name,
				DisplayName:    a.DisplayName,
				ProjectPath:    a.ProjectPath,
				SupportsGlobal: a.GlobalPath != "",
			})
		}
		return printer.PrintList(entries)
	}

	fmt.Fprintln(currentStdout(ctx), "Supported agents:")
	// Cross-client entry first
	fmt.Fprintf(currentStdout(ctx), "  %-14s %s (supports --global)\n", skills.CrossClientAgent.Name, skills.CrossClientAgent.DisplayName)
	fmt.Fprintf(currentStdout(ctx), "                 Project path: %s\n", skills.CrossClientAgent.ProjectPath)
	for _, a := range allAgents {
		globalNote := ""
		if a.GlobalPath != "" {
			globalNote = " (supports --global)"
		}
		fmt.Fprintf(currentStdout(ctx), "  %-14s %s%s\n", a.Name, a.DisplayName, globalNote)
		fmt.Fprintf(currentStdout(ctx), "                 Project path: %s\n", a.ProjectPath)
	}
	return nil
}

// runSkillsUninstall removes installed skill files.
func runSkillsUninstall(cmd *cobra.Command, _ []string) error {
	forFlag, _ := cmd.Flags().GetString("for")
	crossClientFlag, _ := cmd.Flags().GetBool("cross-client")

	if crossClientFlag && forFlag != "" {
		return fmt.Errorf("--cross-client and --for cannot be used together")
	}

	var agent skills.Agent
	if crossClientFlag {
		agent = skills.CrossClientAgent
	} else {
		var err error
		agent, err = resolveAgent(forFlag)
		if err != nil {
			return err
		}
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to determine working directory: %w", err)
	}

	removed, err := skills.Uninstall(agent, baseDir)
	if err != nil {
		return err
	}

	printer := newPrinterCtx(cmdContext(cmd))
	if ap := enrichAgent(printer, "uninstall", "skills"); ap != nil {
		ap.SetSuggestions([]string{
			"Run 'dtctl skills status' to verify removal",
		})
	}

	if agentMode(cmdContext(cmd)) {
		return printer.Print(skillsUninstallAgentResult{
			Agent:   agent.Name,
			Removed: removed,
		})
	}

	if len(removed) == 0 {
		output.FprintInfo(currentStderr(cmdContext(cmd)), "No %s skill files found to remove.", agent.DisplayName)
		return nil
	}

	for _, path := range removed {
		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Removed: %s", path)
	}

	return nil
}

// runSkillsStatus shows installation status.
func runSkillsStatus(cmd *cobra.Command, _ []string) error {
	baseDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to determine working directory: %w", err)
	}

	forFlag, _ := cmd.Flags().GetString("for")

	printer := newPrinterCtx(cmdContext(cmd))
	ap := enrichAgent(printer, "status", "skills")

	if forFlag != "" {
		agent, err := resolveAgent(forFlag)
		if err != nil {
			return err
		}

		result := skills.Status(agent, baseDir)
		if agentMode(cmdContext(cmd)) {
			return printer.Print(statusToAgentEntry(result))
		}
		// Detect agent only for human-readable "(detected via ...)" annotation
		detectedAgent, detected := skills.DetectAgent()
		printStatus(cmdContext(cmd), result, detectedAgent, detected)
		return nil
	}

	// Show all agents
	results := skills.StatusAll(baseDir)

	if agentMode(cmdContext(cmd)) {
		var entries []skillsStatusAgentEntry
		for _, r := range results {
			entries = append(entries, statusToAgentEntry(r))
		}
		if ap != nil {
			ap.SetTotal(len(entries))
		}
		return printer.PrintList(entries)
	}

	// Detect agent only for human-readable "(detected via ...)" annotation
	detectedAgent, detected := skills.DetectAgent()
	anyInstalled := false
	for _, r := range results {
		if r.Installed {
			anyInstalled = true
			printStatus(cmdContext(cmd), r, detectedAgent, detected)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "")
		}
	}

	if !anyInstalled {
		output.FprintInfo(currentStderr(cmdContext(cmd)), "No skill files installed.")
		output.FprintInfo(currentStderr(cmdContext(cmd)), "Run 'dtctl skills install' to get started.")
	}

	return nil
}

// statusToAgentEntry converts a StatusResult to the agent-mode JSON entry.
func statusToAgentEntry(r *skills.StatusResult) skillsStatusAgentEntry {
	entry := skillsStatusAgentEntry{
		Agent:     r.Agent.Name,
		Installed: r.Installed,
	}
	if r.Installed {
		entry.Path = r.Path
		scope := "project"
		if r.Global {
			scope = "global"
		}
		entry.Scope = scope
	}
	return entry
}

// printStatus prints a single agent's status in human-readable format.
func printStatus(ctx context.Context, r *skills.StatusResult, detectedAgent skills.Agent, detected bool) {
	suffix := ""
	if detected && detectedAgent.Name == r.Agent.Name && r.Agent.EnvVar != "" {
		suffix = fmt.Sprintf(" (detected via %s env)", r.Agent.EnvVar)
	}
	output.FprintInfo(currentStderr(ctx), "Agent:     %s%s", r.Agent.DisplayName, suffix)

	if r.Installed {
		scope := "project"
		if r.Global {
			scope = "global"
		}
		output.FprintInfo(currentStderr(ctx), "Installed: %s (%s)", r.Path, scope)
	} else {
		output.FprintInfo(currentStderr(ctx), "Installed: no")
	}
}

// resolveAgent resolves the target agent from --for flag or auto-detection.
func resolveAgent(forFlag string) (skills.Agent, error) {
	if forFlag != "" {
		agent, ok := skills.FindAgent(forFlag)
		if !ok {
			return skills.Agent{}, fmt.Errorf(
				"unknown agent %q\nSupported agents: %s",
				forFlag, strings.Join(skills.SupportedAgents(), ", "),
			)
		}
		return agent, nil
	}

	// Auto-detect
	agent, detected := skills.DetectAgent()
	if !detected {
		return skills.Agent{}, fmt.Errorf(
			"no AI agent detected\nUse --for to specify an agent: %s",
			strings.Join(skills.SupportedAgents(), ", "),
		)
	}

	return agent, nil
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
