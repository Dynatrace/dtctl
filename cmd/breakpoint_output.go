package cmd

import "context"

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
