package cmd

import (
	"context"
	"slices"

	"github.com/Dynatrace/dtctl/pkg/output"
)

type breakpointCommandOutput struct {
	Message string `json:"message" yaml:"message" table:"MESSAGE"`
}

func printBreakpointMessage(ctx context.Context, verb, message string) error {
	return printBreakpointMessageWith(ctx, verb, message, nil)
}

// printBreakpointMessageWith is printBreakpointMessage with suggestions for
// the agent envelope; the other formats print the message alone. The
// envelope also carries what the command said on the way
// (sayBreakpointWarnings), in context.warnings.
func printBreakpointMessageWith(ctx context.Context, verb, message string, suggestions []string) error {
	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, verb, "breakpoint"); ap != nil {
		ap.Context().Suggestions = append(ap.Context().Suggestions, suggestions...)
		ap.Context().Warnings = append(ap.Context().Warnings, breakpointWarnings(ctx)...)
	}
	return printer.Print(breakpointCommandOutput{Message: message})
}

// breakpointWarningsKey is the context key of the lines a breakpoint command
// has said through sayBreakpointWarnings.
type breakpointWarningsKey struct{}

// sayBreakpointWarnings says what a command wants known before it asks or
// changes anything: each line goes to stderr now, and the context returned
// carries them, so the result printed through it repeats them in the agent
// envelope's context.warnings and an agent reads what a person saw.
func sayBreakpointWarnings(ctx context.Context, lines ...string) context.Context {
	if len(lines) == 0 {
		return ctx
	}
	stderr := currentStderr(ctx)
	for _, line := range lines {
		output.FprintInfo(stderr, "%s", line)
	}
	return context.WithValue(ctx, breakpointWarningsKey{}, append(slices.Clip(breakpointWarnings(ctx)), lines...))
}

// breakpointWarnings are the lines said on ctx so far.
func breakpointWarnings(ctx context.Context) []string {
	said, _ := ctx.Value(breakpointWarningsKey{}).([]string)
	return said
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
// in, which depends on where the command ran (see resolveBreakpointWorkspace).
func inWorkspace(message, workspace string) string {
	return message + " (workspace " + workspace + ")"
}
