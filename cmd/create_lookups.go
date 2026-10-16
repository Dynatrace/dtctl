package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/lookup"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// createLookupCmd creates a lookup table
var createLookupCmd = newCreateLookupCmd()

func newCreateLookupCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "lookup -f <file> --path <path> --lookup-field <field>",
		Short: "Create a lookup table",
		Long: `Create a lookup table from a CSV file or manifest.

The lookup table is stored in Grail Resource Store and can be loaded in DQL queries
for data enrichment.

For CSV files, column headers are auto-detected and a DPL parse pattern is generated
automatically: one LD* matcher per column, so cells may be empty. Quoted cells that
contain the delimiter, CRLF line endings and rows with trailing cells omitted are
normalized before upload. Use --dry-run to see the detected pattern.

For non-CSV formats, use --parse-pattern to specify a custom Dynatrace Pattern Language
pattern. Note that a bare LD matcher requires at least one character: use LD* for
columns that can be empty, otherwise those rows are dropped without an error.

After the upload, the number of stored records is compared against the input: dtctl
warns when records were dropped and fails when the pattern matched nothing.

Examples:
  # Create from CSV (auto-detect headers)
  dtctl create lookup -f error_codes.csv \
    --path /lookups/grail/pm/error_codes \
    --lookup-field code \
    --display-name "Error Codes"

  # Create with description
  dtctl create lookup -f error_codes.csv \
    --path /lookups/grail/pm/error_codes \
    --lookup-field code \
    --description "HTTP error code descriptions"

  # Create with custom parse pattern (pipe-delimited)
  dtctl create lookup -f data.txt \
    --path /lookups/custom/data \
    --lookup-field id \
    --parse-pattern "LD*:id '|' LD*:name '|' LD*:value" \
    --skip-records 1

  # Create from stdin
  generate-codes | dtctl create lookup -f - \
    --path /lookups/grail/pm/error_codes \
    --lookup-field code

  # Create from manifest
  dtctl create lookup -f lookup-manifest.yaml

  # Dry run to preview
  dtctl create lookup -f error_codes.csv --path /lookups/test --lookup-field id --dry-run
`,
		Aliases: []string{"lkup", "lu"},
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			path, _ := cmd.Flags().GetString("path")
			lookupField, _ := cmd.Flags().GetString("lookup-field")
			displayName, _ := cmd.Flags().GetString("display-name")
			description, _ := cmd.Flags().GetString("description")
			parsePattern, _ := cmd.Flags().GetString("parse-pattern")
			skipRecords, _ := cmd.Flags().GetInt("skip-records")
			timezone, _ := cmd.Flags().GetString("timezone")
			locale, _ := cmd.Flags().GetString("locale")

			fileData, err := readLookupInput(cmdContext(cmd), file, invocationStdin(cmdContext(cmd)).isTerminal)
			if err != nil {
				return err
			}

			// Check if it's a manifest (YAML/JSON with apiVersion/kind)
			var manifest map[string]interface{}
			if err := json.Unmarshal(fileData, &manifest); err == nil {
				if _, hasKind := manifest["kind"]; hasKind {
					// It's a manifest - handle via apply command. The piped bytes
					// are consumed, so the caller has to pipe them again.
					if file == "-" {
						return fmt.Errorf("the piped input is a manifest -- pipe it to 'dtctl apply -f -' instead")
					}
					return fmt.Errorf("manifest files should be used with 'dtctl apply -f %s'", file)
				}
			}

			// Validate required flags for data files
			if path == "" {
				return fmt.Errorf("--path is required (e.g., /lookups/grail/pm/error_codes)")
			}
			if lookupField == "" {
				return fmt.Errorf("--lookup-field is required (name of the key field)")
			}

			// Build create request
			req := lookup.CreateRequest{
				FilePath:       path,
				DisplayName:    displayName,
				Description:    description,
				LookupField:    lookupField,
				ParsePattern:   parsePattern,
				SkippedRecords: skipRecords,
				Timezone:       timezone,
				Locale:         locale,
				DataContent:    fileData,
			}

			// Set defaults
			if req.Timezone == "" {
				req.Timezone = "UTC"
			}
			if req.Locale == "" {
				req.Locale = "en_US"
			}

			// Handle dry-run
			if dryRun(cmdContext(cmd)) {
				report := newDryRunReport(cmd).
					Linef("Dry run: would create lookup table").
					Field("Path", "%s", req.FilePath).
					Field("Lookup Field", "%s", req.LookupField)
				if req.DisplayName != "" {
					report.Field("Display Name", "%s", req.DisplayName)
				}
				if req.Description != "" {
					report.Field("Description", "%s", req.Description)
				}
				if req.ParsePattern != "" {
					return report.
						Field("Parse Pattern", "%s", req.ParsePattern).
						Field("File Size", "%d bytes", len(fileData)).
						Print()
				}

				prepared, err := lookup.PrepareCSV(fileData)
				if err != nil {
					return fmt.Errorf("failed to detect CSV pattern: %w", err)
				}
				report.
					Field("Parse Pattern", "%s (auto-detected)", prepared.Pattern).
					Field("Records", "%d", prepared.DataRecords)
				if prepared.Normalized {
					report.Linef("Note: CSV will be re-emitted with %s separators (quoted cells, padded rows or CRLF line endings)", prepared.Delimiter)
				}
				return report.Field("File Size", "%d bytes", len(prepared.Content)).Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := lookup.NewHandler(c)

			result, err := handler.Create(req)
			if err != nil {
				return fmt.Errorf("failed to create lookup table: %w", err)
			}

			// The upload API answers 2xx even when the parse pattern matched
			// nothing, so the counts have to be reconciled with the input before
			// this can be called a success (#471).
			warning, countErr := result.CheckRecordCount()

			if countErr == nil {
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Lookup table %q created", path)
			} else {
				output.FprintWarning(currentStderr(cmdContext(cmd)), "Lookup table %q created but no records were stored", path)
			}
			if result.InputRecords > 0 {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "  Records: %d of %d uploaded", result.Records, result.InputRecords)
			} else {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "  Records: %d", result.Records)
			}
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  Pattern Matches: %d", result.PatternMatches)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  File Size: %d bytes (%d uploaded)", result.FileSize, result.UploadedBytes)
			if result.DiscardedDuplicates > 0 {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "  Note: %d duplicate records were discarded", result.DiscardedDuplicates)
			}
			if warning != "" {
				output.FprintWarning(currentStderr(cmdContext(cmd)), "%s", warning)
			}
			if warning != "" || countErr != nil {
				output.FprintHint(currentStderr(cmdContext(cmd)), "Parse pattern used: %s", result.ParsePattern)
			}
			return countErr
		},
	}
	c.Flags().StringP("file", "f", "", "path to data file or manifest, or - for stdin (required)")
	c.Flags().String("path", "", "lookup file path (e.g., /lookups/grail/pm/error_codes)")
	c.Flags().String("lookup-field", "", "name of the lookup key field")
	c.Flags().String("display-name", "", "display name for the lookup table")
	c.Flags().String("description", "", "description of the lookup table")
	c.Flags().String("parse-pattern", "", "custom DPL parse pattern (auto-detected for CSV)")
	c.Flags().Int("skip-records", 0, "number of records to skip (e.g., 1 for CSV headers)")
	c.Flags().String("timezone", "UTC", "timezone for parsing time/date fields")
	c.Flags().String("locale", "en_US", "locale for parsing locale-specific data")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	// Not MarkFlagRequired: a manifest passed here gets the "use dtctl apply"
	// hint, which cobra's required-flag error would preempt. The command body
	// reports a flag left out; an explicitly empty value is rejected here.
	rejectEmptyFlag(c, "path")
	rejectEmptyFlag(c, "lookup-field")
	return c
}

// readLookupInput reads the lookup data named by --file through the shared
// readFileFlag convention: a user-supplied path, or "-" for the process stdin.
//
// stdinIsTerminal is a parameter rather than a probe inside this function so
// the interactive case is testable without a pty -- the same shape
// resolveQueryInput uses. On a terminal, reading would block until Ctrl+D and
// then upload nothing, which reads as a hung CLI.
//
// Empty input is rejected here so the message names the source. Passed on, it
// surfaces from the handler as "no data content specified", which blames the
// caller for omitting data they did supply.
func readLookupInput(ctx context.Context, file string, stdinIsTerminal bool) ([]byte, error) {
	if file == "-" && stdinIsTerminal {
		return nil, fmt.Errorf("--file - reads the lookup data from stdin, but stdin is a terminal -- nothing to read\n\nPipe the data in (generate-codes | dtctl create lookup -f - ...) or pass a file path (-f data.csv)")
	}

	// readFileFlagFrom rejects an empty pipe itself, naming stdin.
	data, err := readFileFlagFrom(ctx, "file", file, queryStdin{r: currentStdin(ctx), isTerminal: stdinIsTerminal})
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%s is empty", file)
	}
	return data, nil
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
