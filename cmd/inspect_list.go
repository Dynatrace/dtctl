package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/inspect"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// inspectListIncompatibleFlags are the file-primitive flags that cannot be
// combined with --list: --list enumerates a directory and takes no <file>, so a
// primitive (which operates on one file) is a contradiction.
var inspectListIncompatibleFlags = []string{
	"schema", "stats", "sample", "head", "tail", "page", "offset", "limit", "fields",
}

// runInspectList implements `dtctl inspect --list`: it enumerates the spilled
// files visible in the active context's managed partition (or in a user-chosen
// spill dir) and emits them with their sidecar provenance, so an agent that has
// lost a file handle (the original spill envelope aged out of context) can
// recover it from disk instead of re-querying Grail. It is local-only — no
// client, no auth — and never reads across contexts (it lists only the active
// context's partition, the read-side mirror of the D9 write partitioning).
func runInspectList(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return inspect.BadFlags(
			"--list takes no <file> argument",
			"run 'dtctl inspect --list' to enumerate spilled files, then inspect one by its path",
		)
	}
	for _, name := range inspectListIncompatibleFlags {
		if cmd.Flags().Changed(name) {
			return inspect.BadFlags(
				"--list cannot be combined with --"+name,
				"run 'dtctl inspect --list' alone, then inspect a listed file with --"+name,
			)
		}
	}

	// Local command: config is loaded best-effort, only to learn the active
	// context (for the partition to list) and the configured spill dir.
	cfg, _ := loadConfig(cmdContext(cmd))
	opts, err := resolveSpillOptions(cmd, cfg)
	if err != nil {
		return err
	}

	contextName := ""
	if cfg != nil {
		_, contextName = spillProvenance(cfg)
	}

	var warnings []string
	dir, managed, berr := output.SpillBaseDir(opts.Dir)
	if berr != nil {
		// No writable/known spill location resolves — there is nothing to list.
		// Emit an empty listing with a note rather than an error: "nothing spilled"
		// is a valid, useful answer for a handle-recovery probe.
		return emitInspectList(cmdContext(cmd), dir, managed, nil, []string{"no spill location is configured or writable, so no spilled files could be listed"})
	}
	if managed {
		// Mirror the write-side partitioning (D9): list only the active context's
		// subdirectory, never the whole cache.
		if contextName == "" {
			// No active context could be determined (missing/unusable config), so
			// SanitizeContextName falls back to the "default" partition — which is
			// NOT where files spilled under a named context live. Warn so an empty
			// listing isn't mistaken for "nothing has been spilled".
			warnings = append(warnings, "the active context could not be determined (no usable config), so only the default partition is listed; spills made under a named context are not shown — set a context with 'dtctl ctx use <name>' and retry")
		}
		dir = filepath.Join(dir, output.SanitizeContextName(contextName))
	} else {
		warnings = append(warnings, "listing a user-chosen spill dir (DTCTL_SPILL_DIR / spill.dir): it is not context-partitioned, so entries may span contexts")
	}

	entries, lerr := output.ListSpillFiles(dir)
	if lerr != nil {
		return fmt.Errorf("failed to list spill dir %q: %w", dir, lerr)
	}

	missing := 0
	for _, e := range entries {
		if e.SidecarMissing {
			missing++
		}
	}
	if missing > 0 {
		warnings = append(warnings, fmt.Sprintf("%d listed file(s) have no manifest — their query and sampling provenance are unknown", missing))
	}

	return emitInspectList(cmdContext(cmd), dir, managed, entries, warnings)
}

// emitInspectList renders a KindFileList listing. Agent mode emits the
// discriminated envelope (opaque to pre-inspect consumers, D31); human / scripted
// mode prints the nested structure (a file list does not table well, so a
// tabular format defaults to JSON, mirroring the --schema/--stats summary path).
func emitInspectList(ictx context.Context, dir string, managed bool, entries []output.SpillFileEntry, warnings []string) error {
	list := &output.SpillList{Kind: output.KindFileList, Dir: dir, Managed: managed, Files: entries}
	total := len(entries)

	if agentMode(ictx) {
		ctx := &output.ResponseContext{
			Verb:        "inspect",
			Total:       &total,
			Warnings:    warnings,
			Suggestions: inspectListSuggestions(entries),
		}
		if jqFilter(ictx) != "" {
			ap := output.NewAgentPrinter(currentStdout(ictx), ctx)
			ap.SetResultFormat(outputFormat(ictx))
			ap.SetJQFilter(jqFilter(ictx))
			return ap.Print(list)
		}
		resp := output.Response{
			OK:              true,
			EnvelopeVersion: output.EnvelopeVersion,
			Result:          list,
			Context:         ctx,
		}
		return output.EncodeEnvelope(currentStdout(ictx), resp)
	}

	printInspectWarnings(ictx, warnings)
	format := outputFormat(ictx)
	if jqFilter(ictx) != "" {
		format = output.NormalizeJQOutputFormat(format)
	}
	switch format {
	case "", "table", "wide":
		format = "json"
	}
	p := newPrinterOpts(ictx, output.PrinterOptions{Format: format, Writer: currentStdout(ictx), JQFilter: jqFilter(ictx)})
	return p.Print(list)
}

// inspectListSuggestions returns the follow-up hints for a listing: the call an
// agent makes next is row access on a recovered handle (D28: no third-party tool
// is named).
func inspectListSuggestions(entries []output.SpillFileEntry) []string {
	if len(entries) == 0 {
		return []string{
			"# no spilled files in this context yet — run a query large enough to spill (it returns a file handle), or lower --spill-threshold",
		}
	}
	return []string{
		"# recover a handle by path, then read its rows, e.g. dtctl inspect " + entries[0].Path + " --head 20",
		"# re-derive a file's summary with --schema/--stats if its manifest is also out of context",
	}
}
