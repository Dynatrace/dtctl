package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/apply"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// updateDocumentCmd updates an existing document of any type from a file.
//
// It is the update-only counterpart to 'create document': it accepts the same
// --type + --id handling but fails instead of creating when the target document
// does not exist. For create-or-update semantics use 'dtctl apply' instead.
var updateDocumentCmd = newUpdateDocumentCmd()

func newUpdateDocumentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "document -f <file> --id <id> [--type <type>]",
		Short: "Update an existing document of any type from a file",
		Long: `Update an existing document from a YAML or JSON file.

This is the update-only counterpart to 'create document'. It works for any
document type — dashboard, notebook, launchpad, or custom app documents such as
acme:config — and completes the round-trip that 'create document' starts:

  dtctl get document acme-config -o yaml > doc.yaml
  # edit doc.yaml...
  dtctl update document -f doc.yaml                # type + id read from the file

Use -o yaml (not -o json) for the export: JSON output is wrapped in a result
envelope that this command cannot read back; YAML is emitted as the plain
document.

The document type is resolved from --type, falling back to the "type" field in
the payload. The target ID is resolved from --id, falling back to the "id" field
in the payload. Unlike 'apply', this command fails if the document does not
already exist, so a typo in the ID never silently creates a new document.

Labels are set with repeatable --label flags, or carried in the payload under a
"labels" array (as produced by 'dtctl get document -o yaml'). Providing labels
replaces the document's entire label set; omitting them leaves labels unchanged.
Labels cannot be cleared, only replaced.

Updates overwrite the document's content in place. Pass --create-snapshot to keep
the pre-update state as a snapshot, recoverable via 'dtctl history document' and
'dtctl restore document'. The API keeps at most 50 snapshots per document and
allows at most 5 per document per minute.

Examples:
  # Round-trip update (type and id come from the exported file)
  dtctl update document -f doc.yaml

  # Update a custom document by explicit type and id
  dtctl update document -f content.json --type acme:config --id acme-config

  # Replace the document's labels
  dtctl update document -f doc.yaml --label team-a --label env:prod

  # Preview the change without applying it
  dtctl update document -f doc.yaml --dry-run

  # Show what changed
  dtctl update document -f doc.yaml --show-diff

  # Keep the previous content as a snapshot before overwriting it
  dtctl update document -f doc.yaml --create-snapshot

  # Snapshot with a description
  dtctl update document -f doc.yaml --create-snapshot --snapshot-description "before Q3 rework"

See also:
  dtctl create document --help   # create a new document of any type
  dtctl apply --help             # create-or-update semantics
`,
		Aliases: []string{"doc"},
		RunE:    updateDocumentRunE,
	}
	c.Flags().StringP("file", "f", "", "file containing the document definition, or - for stdin (required)")
	c.Flags().String("type", "", "document type (e.g. launchpad, acme:config); read from payload if not provided")
	c.Flags().String("id", "", "ID of the document to update; read from payload if not provided")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	c.Flags().StringArray("label", []string{}, "classification label to set (repeatable); replaces the document's labels (cannot be cleared, only replaced). Falls back to labels in the payload")
	c.Flags().Bool("create-snapshot", false, "snapshot the document's current state before updating it, so the previous version stays available via 'dtctl history document'/'dtctl restore document'")
	c.Flags().String("snapshot-description", "", "description for the snapshot created by --create-snapshot (max 128 characters)")
	c.Flags().Bool("dry-run", false, "preview the update without applying it")
	c.Flags().Bool("show-diff", false, "show a diff of the change")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

// updateDocumentRunE updates a document by delegating to the shared applier with
// update-only semantics (RequireExisting). Reusing the applier keeps document
// content extraction, ownership-aware safety checks, dry-run, and diff rendering
// identical to 'apply'.
func updateDocumentRunE(cmd *cobra.Command, _ []string) error {
	file, _ := cmd.Flags().GetString("file")
	docType, _ := cmd.Flags().GetString("type")
	id, _ := cmd.Flags().GetString("id")
	setFlags, _ := cmd.Flags().GetStringArray("set")
	labels, _ := cmd.Flags().GetStringArray("label")
	dryRunFlag, _ := cmd.Flags().GetBool("dry-run")
	showDiff, _ := cmd.Flags().GetBool("show-diff")
	createSnapshot, _ := cmd.Flags().GetBool("create-snapshot")
	snapshotDescription, _ := cmd.Flags().GetString("snapshot-description")

	if err := validateSnapshotFlags(cmd); err != nil {
		return err
	}

	fileData, err := readFileFlag(cmdContext(cmd), "file", file)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	var templateVars map[string]interface{}
	if len(setFlags) > 0 {
		templateVars, err = template.ParseSetFlags(setFlags)
		if err != nil {
			return fmt.Errorf("invalid --set flag: %w", err)
		}
	}

	cfg, err := loadConfig(cmdContext(cmd))
	if err != nil {
		return err
	}
	c, err := newClientFromConfig(cmdContext(cmd), cfg)
	if err != nil {
		return err
	}

	applier := newApplier(cmdContext(cmd), c)
	if !dryRunFlag {
		checker, err := NewSafetyChecker(cfg)
		if err != nil {
			return err
		}
		applier = applier.WithSafetyChecker(checker)
	}

	results, err := applier.Apply(fileData, apply.ApplyOptions{
		TemplateVars:        templateVars,
		DryRun:              dryRunFlag,
		ShowDiff:            showDiff,
		OverrideID:          id,
		Type:                docType,
		Labels:              labels,
		RequireExisting:     true,
		CreateSnapshot:      createSnapshot,
		SnapshotDescription: snapshotDescription,
	})
	if err != nil {
		return err
	}

	printer := newPrinterCtx(cmdContext(cmd))
	enrichAgent(printer, "update", "document")
	if len(results) == 1 {
		return printer.Print(results[0])
	}
	items := make([]interface{}, len(results))
	for i, r := range results {
		items[i] = r
	}
	return printer.PrintList(items)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
