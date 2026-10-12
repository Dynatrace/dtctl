package cmd

import (
	"context"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

type breakpointCommandOutput struct {
	Message string `json:"message" yaml:"message" table:"MESSAGE"`
}

func printBreakpointMessage(ctx context.Context, verb, message string) error {
	return printBreakpointMessageWith(ctx, verb, message, nil)
}

// printBreakpointMessageWith is printBreakpointMessage with suggestions for
// the agent envelope; the other formats print the message alone.
func printBreakpointMessageWith(ctx context.Context, verb, message string, suggestions []string) error {
	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, verb, "breakpoint"); ap != nil {
		ap.Context().Suggestions = append(ap.Context().Suggestions, suggestions...)
	}
	return printer.Print(breakpointCommandOutput{Message: message})
}

// printWorkspaceName tells a person reading a listing which workspace it
// comes from, on stderr, under --plain too; structured and agent output carry
// no such line.
func printWorkspaceName(ctx context.Context, workspace string) {
	if agentMode(ctx) {
		return
	}
	switch outputFormat(ctx) {
	case "", "table", "wide":
		output.FprintInfo(currentStderr(ctx), "workspace: %s", workspace)
	}
}

// inWorkspace ends a message about a change with the workspace it was made
// in, which depends on where the command ran (see workspaceName).
func inWorkspace(message, workspace string) string {
	return message + " (workspace " + workspace + ")"
}
