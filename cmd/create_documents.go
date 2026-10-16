package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// createDocumentCmd creates a document of any type from a file
var createDocumentCmd = newCreateDocumentCmd()

func newCreateDocumentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "document -f <file> --type <type>",
		Short: "Create a document of any type from a file",
		Long: `Create a new document from a YAML or JSON file.

The document type must be provided via --type or included as a "type" field in
the payload. This command works for any document type: dashboard, notebook,
launchpad, custom app documents, etc.

Labels can be attached with repeatable --label flags, or carried in the payload
under a "labels" array (as produced by 'dtctl get document -o yaml'). Because
the create API cannot set labels directly, they are applied with a follow-up
update immediately after creation.

Examples:
  # Create a launchpad document from JSON
  dtctl create document -f launchpad.json --type launchpad

  # Create from a payload that already contains a "type" field
  dtctl create document -f my-app-config.yaml

  # Create with classification labels
  dtctl create document -f config.yaml --type my-app:config --label team-a --label env:prod

  # Create with template variables
  dtctl create document -f config.yaml --type my-app:config --set env=prod

  # Dry run to preview
  dtctl create document -f launchpad.json --type launchpad --dry-run
`,
		Aliases: []string{"doc"},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Determine the document type
			docType, _ := cmd.Flags().GetString("type")

			// Read the input once: with -f - it is stdin, which cannot be re-read
			// by the shared helper after the type has been sniffed from it.
			file, _ := cmd.Flags().GetString("file")
			var fileData []byte
			if file != "" {
				var err error
				fileData, err = readFileFlag(cmdContext(cmd), "file", file)
				if err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}
			}

			// If no --type flag, try to read from file payload
			if docType == "" && fileData != nil {
				jsonData, err := format.ValidateAndConvert(fileData)
				if err == nil {
					var doc map[string]interface{}
					if err := json.Unmarshal(jsonData, &doc); err == nil {
						if t, ok := doc["type"].(string); ok && t != "" {
							docType = t
						}
					}
				}
			}

			if docType == "" {
				return fmt.Errorf("document type is required: use --type flag or include a \"type\" field in the payload")
			}

			return createDocumentFromData(cmd, docType, fileData)
		},
	}
	c.Flags().StringP("file", "f", "", "file containing document definition, or - for stdin (required)")
	c.Flags().String("type", "", "document type (e.g. launchpad, my-app:config); extracted from payload if not provided")
	c.Flags().String("name", "", "name for the document (extracted from content if not provided)")
	c.Flags().String("description", "", "description for the document")
	c.Flags().String("id", "", "custom ID for the document (auto-generated if not provided)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	c.Flags().StringArray("label", []string{}, "classification label to attach (repeatable); falls back to labels in the payload")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

// createNotebookCmd creates a notebook from a file
var createNotebookCmd = newCreateNotebookCmd()

func newCreateNotebookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "notebook -f <file>",
		Short: "Create a notebook from a file",
		Long: `Create a new notebook from a YAML or JSON file.

Examples:
  # Create a notebook from YAML
  dtctl create notebook -f notebook.yaml

  # Create with a specific name
  dtctl create notebook -f notebook.yaml --name "My Notebook"

  # Create with template variables
  dtctl create notebook -f notebook.yaml --set env=prod

  # Dry run to preview
  dtctl create notebook -f notebook.yaml --dry-run
`,
		Aliases: []string{"nb"},
		RunE:    createDocumentRunE("notebook"),
	}
	c.Flags().StringP("file", "f", "", "file containing notebook definition, or - for stdin (required)")
	c.Flags().String("name", "", "name for the notebook (extracted from content if not provided)")
	c.Flags().String("description", "", "description for the notebook")
	c.Flags().String("id", "", "custom ID for the notebook (auto-generated if not provided)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

// createDashboardCmd creates a dashboard from a file
var createDashboardCmd = newCreateDashboardCmd()

func newCreateDashboardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "dashboard -f <file>",
		Short: "Create a dashboard from a file",
		Long: `Create a new dashboard from a YAML or JSON file.

IMPORTANT: This command always creates a NEW dashboard, even if your file contains
an 'id' field. To update an existing dashboard, use 'dtctl apply' instead.

Workflow:
  - Create: Use this command to create new dashboards
  - Update: Use 'dtctl apply -f dashboard.yaml' to update existing dashboards
  - Delete: Use 'dtctl delete dashboard <id> -y' to delete

The create command will:
  1. Extract dashboard name and description from the file
  2. Create a new dashboard with a unique ID
  3. Return the new dashboard ID and URL

Examples:
  # Create a dashboard from YAML
  dtctl create dashboard -f dashboard.yaml

  # Create with a specific name (overrides name in file)
  dtctl create dashboard -f dashboard.yaml --name "My Dashboard"

  # Create with template variables
  dtctl create dashboard -f dashboard.yaml --set env=prod

  # Dry run to preview without creating
  dtctl create dashboard -f dashboard.yaml --dry-run

  # Provide a custom ID (useful for predictable IDs)
  dtctl create dashboard -f dashboard.yaml --id my.custom.dashboard-id

See also:
  dtctl apply --help    # For updating existing dashboards
  dtctl get dashboard --help    # For exporting dashboards
`,
		Aliases: []string{"db"},
		RunE:    createDocumentRunE("dashboard"),
	}
	c.Flags().StringP("file", "f", "", "file containing dashboard definition, or - for stdin (required)")
	c.Flags().String("name", "", "name for the dashboard (extracted from content if not provided)")
	c.Flags().String("description", "", "description for the dashboard")
	c.Flags().String("id", "", "custom ID for the dashboard (auto-generated if not provided)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

// createDocumentRunE returns a RunE function for creating documents of a specific type
func createDocumentRunE(docType string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		fileData, err := readFileFlag(cmdContext(cmd), "file", file)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}
		return createDocumentFromData(cmd, docType, fileData)
	}
}

// createDocumentFromData creates a document of docType from the already-read
// --file input.
func createDocumentFromData(cmd *cobra.Command, docType string, fileData []byte) error {
	name, _ := cmd.Flags().GetString("name")
	description, _ := cmd.Flags().GetString("description")
	id, _ := cmd.Flags().GetString("id")
	setFlags, _ := cmd.Flags().GetStringArray("set")
	labels, _ := cmd.Flags().GetStringArray("label")

	// Convert to JSON if needed
	jsonData, err := format.ValidateAndConvert(fileData)
	if err != nil {
		return fmt.Errorf("invalid file format: %w", err)
	}

	// Apply template rendering if variables provided
	if len(setFlags) > 0 {
		templateVars, err := template.ParseSetFlags(setFlags)
		if err != nil {
			return fmt.Errorf("invalid --set flag: %w", err)
		}
		rendered, err := template.RenderTemplate(string(jsonData), templateVars)
		if err != nil {
			return fmt.Errorf("template rendering failed: %w", err)
		}
		jsonData = []byte(rendered)
	}

	// Parse the document to extract content properly
	var doc map[string]interface{}
	if err := json.Unmarshal(jsonData, &doc); err != nil {
		return fmt.Errorf("failed to parse %s JSON: %w", docType, err)
	}

	// Extract content, name, description using the same logic as apply
	contentData, extractedName, extractedDesc, warnings := extractDocumentContent(doc, docType)

	// Fall back to labels embedded in the payload (e.g. from 'get -o yaml')
	// when none were supplied via --label.
	if len(labels) == 0 {
		labels = extractDocumentLabels(doc)
	}

	// Show validation warnings
	for _, w := range warnings {
		output.FprintWarning(currentStderr(cmdContext(cmd)), "%s", w)
	}

	// Use flag values if provided, otherwise use extracted values
	if name == "" {
		name = extractedName
	}
	if description == "" {
		description = extractedDesc
	}

	// Extract ID from document if not provided via flag
	if id == "" {
		if docID, ok := doc["id"].(string); ok && docID != "" {
			id = docID
		}
	}

	// Default name if still empty
	if name == "" {
		name = fmt.Sprintf("Untitled %s", docType)
	}

	// Count tiles/sections for feedback
	tileCount := countDocumentItems(contentData, docType)

	// Handle dry-run
	if dryRun(cmdContext(cmd)) {
		report := newDryRunReport(cmd).OnStderr().
			Linef("Dry run: would create %s", docType).
			Detail("type", "%s", docType).
			Linef("  Name: %s", name).
			Detail("name", "%s", name)
		if id != "" {
			report.Linef("  ID: %s", id).Detail("id", "%s", id)
		}
		if description != "" {
			report.Linef("  Description: %s", description).Detail("description", "%s", description)
		}
		if tileCount > 0 {
			report.Linef("  %s: %d", capitalize(itemName(docType)), tileCount).
				Detail(itemName(docType), "%d", tileCount)
		}
		if len(warnings) == 0 {
			report.Linef("").Linef("Document structure validated successfully")
		}
		return report.Print()
	}

	_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
	if err != nil {
		return err
	}

	handler := document.NewHandler(c)

	result, err := handler.Create(document.CreateRequest{
		ID:          id,
		Name:        name,
		Type:        docType,
		Description: description,
		Content:     contentData,
		Labels:      labels,
	})
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", docType, err)
	}

	// Use name from input if result doesn't have it
	resultName := result.Name
	if resultName == "" {
		resultName = name
	}
	resultID := result.ID
	if resultID == "" {
		resultID = id
		if resultID == "" {
			resultID = "(ID not returned)"
		}
	}

	// Improved output formatting for better visibility
	output.FprintSuccess(currentStderr(cmdContext(cmd)), "%s created", capitalize(docType))
	output.FprintInfo(currentStderr(cmdContext(cmd)), "  Name: %s", resultName)
	output.FprintInfo(currentStderr(cmdContext(cmd)), "  ID:   %s", resultID)
	if tileCount > 0 {
		output.FprintInfo(currentStderr(cmdContext(cmd)), "  %s: %d", capitalize(itemName(docType)), tileCount)
	}
	// Only document types with a known viewer app get a URL; for custom types
	// the app ID is unknown, so print none rather than a broken guess.
	if url := document.UIURL(c.BaseURL(), docType, result.ID); url != "" {
		output.FprintInfo(currentStderr(cmdContext(cmd)), "  URL:  %s", url)
	}
	return nil
}

// extractDocumentContent extracts the content from a document, handling various input formats
// Returns: contentData, name, description, warnings
func extractDocumentContent(doc map[string]interface{}, docType string) ([]byte, string, string, []string) {
	var warnings []string
	name, _ := doc["name"].(string)
	description, _ := doc["description"].(string)

	// Check if this is a "get" output format with nested content
	if content, hasContent := doc["content"]; hasContent {
		contentMap, isMap := content.(map[string]interface{})
		if isMap {
			// Check for double-nested content (common mistake)
			if innerContent, hasInner := contentMap["content"]; hasInner {
				if inner, ok := innerContent.(map[string]interface{}); ok {
					warnings = append(warnings, "detected double-nested content (.content.content) - using inner content")
					contentMap = inner
				}
			}

			// Validate structure based on document type
			if docType == "dashboard" {
				if _, hasTiles := contentMap["tiles"]; !hasTiles {
					warnings = append(warnings, "dashboard content has no 'tiles' field - dashboard may be empty")
				}
				if _, hasVersion := contentMap["version"]; !hasVersion {
					warnings = append(warnings, "dashboard content has no 'version' field")
				}
			} else if docType == "notebook" {
				if _, hasSections := contentMap["sections"]; !hasSections {
					warnings = append(warnings, "notebook content has no 'sections' field - notebook may be empty")
				}
			}

			contentData, _ := json.Marshal(contentMap)
			return contentData, name, description, warnings
		}
	}

	// No content field - the whole doc might be the content (direct format)
	// Check if it looks like dashboard/notebook content
	if docType == "dashboard" {
		if _, hasTiles := doc["tiles"]; hasTiles {
			// This is direct content format
			contentData, _ := json.Marshal(doc)
			return contentData, name, description, warnings
		}
		warnings = append(warnings, "document has no 'content' or 'tiles' field - structure may be incorrect")
	} else if docType == "notebook" {
		if _, hasSections := doc["sections"]; hasSections {
			// This is direct content format
			contentData, _ := json.Marshal(doc)
			return contentData, name, description, warnings
		}
		warnings = append(warnings, "document has no 'content' or 'sections' field - structure may be incorrect")
	}

	// Fall back to using the whole document as content
	contentData, _ := json.Marshal(doc)
	return contentData, name, description, warnings
}

// extractDocumentLabels pulls a "labels" string array from a parsed document
// payload (as produced by 'dtctl get document -o yaml'). Non-string entries are
// skipped. Returns nil when no usable labels are present.
func extractDocumentLabels(doc map[string]interface{}) []string {
	raw, ok := doc["labels"].([]interface{})
	if !ok {
		return nil
	}
	var labels []string
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			labels = append(labels, s)
		}
	}
	return labels
}

// countDocumentItems counts tiles (for dashboards) or sections (for notebooks)
func countDocumentItems(contentData []byte, docType string) int {
	var content map[string]interface{}
	if err := json.Unmarshal(contentData, &content); err != nil {
		return 0
	}

	if docType == "dashboard" {
		// Tiles can be either an array or a map/object
		if tiles, ok := content["tiles"].([]interface{}); ok {
			return len(tiles)
		}
		if tiles, ok := content["tiles"].(map[string]interface{}); ok {
			return len(tiles)
		}
	} else if docType == "notebook" {
		// Sections can be either an array or a map/object
		if sections, ok := content["sections"].([]interface{}); ok {
			return len(sections)
		}
		if sections, ok := content["sections"].(map[string]interface{}); ok {
			return len(sections)
		}
	}
	return 0
}

// itemName returns the item name for a document type (tiles for dashboards, sections for notebooks)
func itemName(docType string) string {
	if docType == "dashboard" {
		return "tiles"
	}
	return "sections"
}

// capitalize capitalizes the first letter of a string
func capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(string(s[0])) + s[1:]
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
