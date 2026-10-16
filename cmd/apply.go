package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/apply"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/suggest"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// applyCmd represents the apply command
var applyCmd = newApplyCmd()

func newApplyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "apply -f <file>",
		Short: "Apply a configuration to create or update resources",
		Long: `Apply a configuration to create or update resources from YAML or JSON files.

The apply command reads a resource definition from a file and applies it to the
Dynatrace environment. Resources are updated if they already exist (based on ID).

How it works:
  - If the file contains an 'id' field and that resource exists: UPDATE
  - If the file contains an 'id' field but resource doesn't exist: CREATE with that ID
  - If the file has no 'id' field: CREATE with auto-generated ID

This is similar to 'kubectl apply' - use it to keep resources in sync with their
file definitions. For round-trip workflows, use 'dtctl get <resource> -o yaml' to
export, edit, and apply back.

Idempotent local workflow (--write-id):
  Use --write-id on first apply to stamp the generated ID back into the source file.
  Every subsequent apply will then update in place automatically — no manual ID
  tracking required.

    dtctl apply -f dashboard.yaml --write-id   # creates, stamps id into file
    dtctl apply -f dashboard.yaml              # updates the same dashboard

  If you forgot --write-id on the first run, use --id to recover without creating
  a duplicate:

    dtctl apply -f dashboard.yaml --write-id --id <id-from-first-run>

Template-driven deployments (--id):
  Keep a reusable template file (no ID) and supply the target ID externally.
  Ideal for CI pipelines or deploying the same template to a known resource.

    dtctl apply -f template.yaml --id $DASHBOARD_ID

Template variables can be used with the --set flag for reusable configurations,
making it easy to deploy the same resource across multiple environments.

Supported resource types:
  - Workflows (automation)
  - Scheduling rules (automation)
  - Dashboards
  - Notebooks
  - Documents of any type (launchpad, custom app documents, e.g. acme:config)
  - SLOs
  - Grail buckets
  - Settings objects
  - Extension monitoring configurations

Custom document types (--type):
  Documents exported with 'dtctl get document -o yaml' carry their "type" field
  and are detected automatically, so the export → edit → apply round-trip works
  for any document type. Use --type to apply a file that has no embedded type, or
  to force a specific type:

    dtctl get document acme-config -o yaml > doc.yaml   # includes "type"
    dtctl apply -f doc.yaml                              # updates, type auto-detected
    dtctl apply -f raw-content.json --type acme:config --id acme-config

  Use -o yaml (not -o json) for the round-trip: JSON output is wrapped in a
  result envelope that apply cannot read back; YAML is emitted as the plain
  document.

  Labels ride along in an exported document and are preserved on a round-trip
  apply. Override them with repeatable --label flags (this replaces the full
  set; labels cannot be cleared, only replaced):

    dtctl apply -f doc.yaml --label team-a --label env:prod

Version history (--create-snapshot):
  Document updates overwrite content in place. Pass --create-snapshot to have the
  API keep the pre-update state as a snapshot first, so it stays recoverable with
  'dtctl history document' and 'dtctl restore document':

    dtctl apply -f doc.yaml --create-snapshot
    dtctl apply -f doc.yaml --create-snapshot --snapshot-description "before Q3 rework"

  The flag applies to documents only and is a no-op when the document is created
  rather than updated. The API keeps at most 50 snapshots per document and rejects
  more than 5 snapshots per document per minute, so avoid it in tight apply loops.

Array input (bulk apply):
  Files containing an array of resources (e.g., from 'dtctl get settings --schema ...
  -o yaml') are applied element-by-element. Partial failures do not abort the batch;
  successful results are printed and a summary error reports any failures.

Examples:
  # Create a new dashboard and stamp the ID back into the file
  dtctl apply -f dashboard.yaml --write-id

  # Update existing dashboard (file exported with 'get' command includes ID)
  dtctl get dashboard my-dash -o yaml > dashboard.yaml
  # Edit dashboard.yaml...
  dtctl apply -f dashboard.yaml  # Updates the existing dashboard

  # Forgot --write-id on first run? Recover without creating a duplicate:
  dtctl apply -f dashboard.yaml --write-id --id <id-from-first-run>

  # CI/scripting: apply template to a known target resource
  dtctl apply -f template.yaml --id $DASHBOARD_ID

  # Update a settings object
  dtctl get settings <objectId> -o yaml > setting.yaml
  # Edit setting.yaml (modify the 'value' field)...
  dtctl apply -f setting.yaml  # Updates the existing setting

  # Bulk update settings (round-trip from get --schema)
  dtctl get settings --schema builtin:rum.web.enablement -o yaml > rum-settings.yaml
  # Edit rum-settings.yaml (modify values for specific applications)...
  dtctl apply -f rum-settings.yaml  # Updates all settings in the file

  # Apply with template variables
  dtctl apply -f dashboard.yaml --set environment=prod --set owner=team-a

  # Preview changes before applying
  dtctl apply -f notebook.yaml --dry-run

  # See what changed when updating
  dtctl apply -f dashboard.yaml --show-diff

  # Apply and get JSON output (for scripting/CI)
  dtctl apply -f dashboard.yaml -o json

  # Apply and get YAML output
  dtctl apply -f workflow.yaml -o yaml

  # Apply with wide table (includes URL for dashboards/notebooks)
  dtctl apply -f notebook.yaml -o wide

Note: The 'create' command always creates new resources. Use 'apply' to keep
resources in sync with their file definitions.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")

			setFlags, _ := cmd.Flags().GetStringArray("set")
			dryRunFlag, _ := cmd.Flags().GetBool("dry-run")
			showDiff, _ := cmd.Flags().GetBool("show-diff")
			noHooks, _ := cmd.Flags().GetBool("no-hooks")
			overrideID, _ := cmd.Flags().GetString("id")
			writeID, _ := cmd.Flags().GetBool("write-id")
			docType, _ := cmd.Flags().GetString("type")
			labels, _ := cmd.Flags().GetStringArray("label")
			createSnapshot, _ := cmd.Flags().GetBool("create-snapshot")
			snapshotDescription, _ := cmd.Flags().GetString("snapshot-description")
			shareEnvironment, _ := cmd.Flags().GetString("share-environment")

			if err := validateShareEnvironmentValue(shareEnvironment); err != nil {
				return err
			}

			if err := validateSnapshotFlags(cmd); err != nil {
				return err
			}

			// --write-id rewrites the input file in place; a pipe has nothing to
			// write back to (and a file literally named "-" is not the input).
			if file == "-" && writeID {
				return &suggest.FlagError{Flag: "write-id", Message: "--write-id cannot be used with -f - (stdin): there is no file to write the ID back to; save the input to a file first"}
			}

			// Read the file
			fileData, err := readFileFlag(cmdContext(cmd), "file", file)
			if err != nil {
				return fmt.Errorf("failed to read file: %w", err)
			}

			// Parse template variables
			var templateVars map[string]interface{}
			if len(setFlags) > 0 {
				templateVars, err = template.ParseSetFlags(setFlags)
				if err != nil {
					return fmt.Errorf("invalid --set flag: %w", err)
				}
			}

			// Load configuration
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return err
			}

			c, err := newClientFromConfig(cmdContext(cmd), cfg)
			if err != nil {
				return err
			}

			// Create applier with safety checker (safety checks happen inside applier
			// with proper ownership determination for updates)
			applier := newApplier(cmdContext(cmd), c)
			// The source file is the --write-id writeback target (and hook
			// context) — it must be set regardless of whether hooks are
			// configured, or the id never lands back in the file.
			applier = applier.WithSourceFile(sourceName(file))
			if !dryRunFlag {
				checker, err := NewSafetyChecker(cfg)
				if err != nil {
					return err
				}
				applier = applier.WithSafetyChecker(checker)
			}

			// Configure pre-apply and post-apply hooks
			if !noHooks {
				// Capability gate: hooks execute arbitrary configured commands.
				// Fail loudly when the config requests one the host forbids —
				// silently skipping a validation hook would be worse.
				if !currentCaps(cmdContext(cmd)).ApplyHooks && (cfg.GetPreApplyHook() != "" || cfg.GetPostApplyHook() != "") {
					return &CapabilityError{Feature: "apply hooks"}
				}
				if hookCmd := cfg.GetPreApplyHook(); hookCmd != "" {
					applier = applier.WithPreApplyHook(hookCmd).WithSourceFile(sourceName(file))
				}
				if hookCmd := cfg.GetPostApplyHook(); hookCmd != "" {
					applier = applier.WithPostApplyHook(hookCmd).WithSourceFile(sourceName(file))
				}
				// Hook output (stdout and stderr) always goes to stderr so that
				// stdout carries only the structured result — JSON, YAML, or table
				// — regardless of output mode, and regardless of hook success or failure.
				applier = applier.WithHookOutputs(currentStderr(cmdContext(cmd)), currentStderr(cmdContext(cmd)))
			}

			// Apply the resource
			opts := apply.ApplyOptions{
				TemplateVars:        templateVars,
				DryRun:              dryRunFlag,
				ShowDiff:            showDiff,
				OverrideID:          overrideID,
				WriteID:             writeID,
				Type:                docType,
				Labels:              labels,
				CreateSnapshot:      createSnapshot,
				SnapshotDescription: snapshotDescription,
			}

			results, applyErr := applier.Apply(fileData, opts)

			// For ListApplyError (partial batch failure), we still want to print
			// the successful results before returning the error.
			if applyErr != nil && len(results) == 0 {
				return applyErr
			}

			// Run environment sharing before printing so per-document "Shared X" stderr
			// lines appear adjacent to the apply output. Errors are collected rather than
			// returned immediately so the user always sees the apply results.
			var shareErr error
			if shareEnvironment != "" && !dryRunFlag {
				shareErr = ensureEnvironmentShareForResults(cmdContext(cmd), c, results, shareEnvironment)
			}

			// Print structured output using the global -o flag.
			// The concrete type (DashboardApplyResult, WorkflowApplyResult, DryRunResult, etc.)
			// determines which columns/fields appear in the output.
			printer := newPrinterCtx(cmdContext(cmd))

			// Enrich agent output with apply-specific context
			resourceType := ""
			if base := extractApplyBase(results[0]); base != nil {
				resourceType = base.ResourceType
			}
			if ap := enrichAgent(printer, "apply", resourceType); ap != nil {
				ap.SetTotal(len(results))
				suggestions := buildApplySuggestions(results)
				ap.SetSuggestions(suggestions)
				// Forward any warnings from apply results
				var warnings []string
				for _, r := range results {
					if base := extractApplyBase(r); base != nil && len(base.Warnings) > 0 {
						warnings = append(warnings, base.Warnings...)
					}
				}
				if len(warnings) > 0 {
					ap.SetWarnings(warnings)
				}
			}

			if len(results) == 1 {
				if err := printer.Print(results[0]); err != nil {
					return err
				}
			} else {
				// Multiple results (e.g., connection list apply) — use list output
				items := make([]interface{}, len(results))
				for i, r := range results {
					items[i] = r
				}
				if err := printer.PrintList(items); err != nil {
					return err
				}
			}
			if shareErr != nil {
				return fmt.Errorf("apply succeeded but environment share failed: %w", shareErr)
			}
			return applyErr
		},
	}
	c.Flags().StringP("file", "f", "", "file containing resource definition, or - for stdin (required)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	c.Flags().Bool("dry-run", false, "preview changes without applying")
	c.Flags().Bool("show-diff", false, "show diff of changes when updating existing resources")
	c.Flags().Bool("no-hooks", false, "skip pre-apply and post-apply hooks")
	c.Flags().String("id", "", "override or inject resource ID (use with --write-id to stamp ID into file)")
	c.Flags().Bool("write-id", false, "write the created resource ID back into the source file for idempotent future applies")
	c.Flags().String("type", "", "document type (e.g. launchpad, acme:config); forces the file to be applied as a document of this type")
	c.Flags().StringArray("label", []string{}, "document classification label (repeatable); replaces the document's labels, overriding any in the payload (labels cannot be cleared, only replaced)")
	c.Flags().Bool("create-snapshot", false, "snapshot the document's current state before updating it, so the previous version stays available via 'dtctl history'/'dtctl restore' (documents only)")
	c.Flags().String("snapshot-description", "", "description for the snapshot created by --create-snapshot (max 128 characters)")
	c.Flags().String("share-environment", "", "share the applied notebook/dashboard with everyone in the environment (values: 'read' or 'read-write'; bare --share-environment defaults to 'read')")
	c.Flags().Lookup("share-environment").NoOptDefVal = "read"
	// It bundles two things the Document API keeps apart, neither of them part
	// of the document apply writes: an environment share link (a grant to other
	// users) and isPrivate=false (a property of the document). `share document
	// --environment link|public` does each on its own.
	_ = c.Flags().MarkDeprecated("share-environment", shareEnvironmentDeprecation)
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

func init() {
	rootCmd.AddCommand(applyCmd)
}

// shareEnvironmentDeprecation is apply --share-environment's deprecation
// note. pflag prints it after "Flag --share-environment has been deprecated, ".
const shareEnvironmentDeprecation = "in 0.44.0, removal planned in 0.46.0: " +
	"apply the file, then use `dtctl share document <id> --environment link,public [--access read-write]`"

// validateShareEnvironmentValue rejects any --share-environment value outside
// the empty string, "read", or "read-write".
func validateShareEnvironmentValue(v string) error {
	switch v {
	case "", "read", "read-write":
		return nil
	default:
		return fmt.Errorf("invalid --share-environment value %q, must be 'read' or 'read-write'", v)
	}
}

// ensureEnvironmentShareForResults walks apply results and opens every notebook/dashboard to
// the environment the way `share document --environment link,public` does: an environment
// share link at the access level, and isPrivate=false. A link at another level is left
// alone; a document can have one per level. Other resource types are silently skipped —
// environment shares only apply to documents.
//
// Per-document failures do not abort the walk: we attempt a share for every eligible
// result and return a combined error at the end so multi-document applies are partially
// successful when possible.
func ensureEnvironmentShareForResults(ctx context.Context, c *client.Client, results []apply.ApplyResult, access string) error {
	handler := document.NewHandler(c)
	var errs []error
	for _, r := range results {
		base := extractApplyBase(r)
		if base == nil {
			continue
		}
		if base.ResourceType != "notebook" && base.ResourceType != "dashboard" {
			continue
		}
		if base.ID == "" {
			continue
		}
		if err := shareAppliedDocument(handler, base.ID, access); err != nil {
			output.FprintWarning(currentStderr(ctx), "failed to share %s %q with environment: %v", base.ResourceType, base.ID, err)
			errs = append(errs, fmt.Errorf("document %q: %w", base.ID, err))
			continue
		}
		output.FprintInfo(currentStderr(ctx), "Shared %s %q with environment (%s)", base.ResourceType, base.ID, access)
	}
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	ids := make([]string, 0, len(errs))
	for _, e := range errs {
		ids = append(ids, e.Error())
	}
	return fmt.Errorf("%d documents failed to share: %s", len(errs), strings.Join(ids, "; "))
}

// shareAppliedDocument ensures the document's environment share link at the
// access level, then makes it public: the link first, so a failure to make it
// public leaves the narrower of the two in place.
func shareAppliedDocument(handler *document.Handler, id, access string) error {
	if _, err := handler.EnsureEnvironmentLink(id, access); err != nil {
		return err
	}
	if err := handler.SetPrivate(id, false); err != nil {
		return fmt.Errorf("environment share created, but making the document public failed: %w", err)
	}
	return nil
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
