package main

import (
	"os"

	"github.com/Dynatrace/dtctl/cmd"
	"github.com/Dynatrace/dtctl/pkg/serve"
)

func main() {
	// Server mode is a development-tier feature, off by default. Dispatch has to
	// consult the opt-in here rather than leave it to the registration stage:
	// `dtctl serve ...` runs outside the normal command pipeline, because every
	// request the server accepts becomes an engine execution that must acquire
	// the per-invocation lock the pipeline would already be holding for the
	// serve command itself. See serve.Run.
	if serve.Enabled() && len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Exit(serve.Run(os.Args[2:]))
	}

	// serve lives outside package cmd (it imports pkg/engine, which imports
	// cmd); declare it here so it appears in help and the command catalog when
	// the opt-in is on — and so a disabled `dtctl serve` can still be named in
	// a block message. Registration itself is decided per invocation.
	cmd.AddDevelopmentCommand(serve.NewCommand(), serve.DevelopmentFeature)

	cmd.Execute()
}
