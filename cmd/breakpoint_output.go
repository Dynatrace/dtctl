package cmd

import "context"

type breakpointCommandOutput struct {
	Message string `json:"message" yaml:"message" table:"MESSAGE"`
}

func printBreakpointMessage(ctx context.Context, verb, message string) error {
	printer := newPrinterCtx(ctx)
	_ = enrichAgent(printer, verb, "breakpoint")
	return printer.Print(breakpointCommandOutput{Message: message})
}
