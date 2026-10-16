package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// launchEditor opens path in the user's editor, resolving $EDITOR, then the
// config preference, then vim. It is the single subprocess gateway for every
// edit command and enforces the Editor capability, so embedded callers (which
// grant no capabilities) can never spawn an editor.
func launchEditor(ctx context.Context, preferredEditor, path string) error {
	if !currentCaps(ctx).Editor {
		return &CapabilityError{Feature: "interactive editing (edit)"}
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = preferredEditor
	}
	if editor == "" {
		editor = "vim"
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return fmt.Errorf("no editor configured")
	}
	editorCmd := exec.Command(parts[0], append(parts[1:], path)...)
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = currentStdout(ctx)
	editorCmd.Stderr = currentStderr(ctx)
	if err := editorCmd.Run(); err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}
	return nil
}

// editCmd represents the edit command
var editCmd = newEditCmd()

func newEditCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "edit",
		Short: "Edit a resource",
		Long: `Edit a resource interactively using your default editor ($EDITOR).

Fetches the current resource definition, opens it in your editor as YAML, and
applies the changes when the file is saved and closed. If the editor exits
without changes or with a non-zero exit code, the update is cancelled.

The editor is determined by the EDITOR environment variable (defaults to vi).
Blocked by safety level if the current context is set to 'readonly'.

Supported resources:
  workflows (wf)          dashboards (dash, db)     notebooks (nb)
  settings                aws monitoring             azure monitoring
  gcp monitoring`,
		Example: `  # Edit a workflow in your default editor
  dtctl edit workflow my-workflow

  # Edit a dashboard by name
  dtctl edit dashboard "My Dashboard"

  # Edit a settings object by ID
  dtctl edit setting <object-id>

  # Edit an AWS monitoring configuration
  dtctl edit aws monitoring --name "my-aws-monitoring"

  # Edit an Azure monitoring configuration by ID
  dtctl edit azure monitoring <id>`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(editCmd)

	editCmd.AddCommand(editWorkflowCmd)
	editCmd.AddCommand(editDashboardCmd)
	editCmd.AddCommand(editNotebookCmd)
	editCmd.AddCommand(editDocumentCmd)
	editCmd.AddCommand(editSettingCmd)
	editCmd.AddCommand(editSegmentCmd)
	editCmd.AddCommand(editAnomalyDetectorCmd)

	editCmd.AddCommand(editAWSProviderCmd)
	editAWSProviderCmd.AddCommand(editAWSMonitoringCmd)

	editCmd.AddCommand(editAzureProviderCmd)
	editAzureProviderCmd.AddCommand(editAzureMonitoringCmd)

	editCmd.AddCommand(editGCPProviderCmd)
	editGCPProviderCmd.AddCommand(editGCPMonitoringCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
