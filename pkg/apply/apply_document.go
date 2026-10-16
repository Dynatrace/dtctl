package apply

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

// applyDocument applies a document resource of any type (dashboard, notebook,
// or a custom type such as "launchpad" or "acme:config").
//
// docType is the concrete document type. When empty it is resolved from the
// payload's "type" field, allowing round-trip apply of documents exported with
// 'dtctl get document -o yaml'.
func (a *Applier) applyDocument(data []byte, docType string, opts ApplyOptions) (ApplyResult, error) {
	// Parse to check for ID and name
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse document JSON: %w", err)
	}

	// Resolve the document type from the payload when not supplied by the caller.
	if docType == "" {
		docType, _ = doc["type"].(string)
	}
	if docType == "" {
		return nil, fmt.Errorf("document type is required: use --type or include a \"type\" field in the payload")
	}

	// Extract and validate content - handle round-trippable format from 'get' command
	contentData, name, description, validationWarnings := extractDocumentContent(doc, docType)

	// Labels come from --label flags (opts.Labels) when supplied; otherwise they
	// ride along in the exported document ('get document -o yaml') so a round-trip
	// apply preserves (and can update) them. An empty opts.Labels (the flag's
	// zero value when --label is absent) falls back to the payload.
	labels := opts.Labels
	if len(labels) == 0 {
		labels = extractDocumentLabels(doc)
	}

	// Show validation warnings on stderr and collect for result
	var resultWarnings []string
	for _, w := range validationWarnings {
		a.stderrWarn(&resultWarnings, "%s", w)
	}

	// Count tiles/sections for feedback
	tileCount := countDocumentItems(contentData, docType)

	handler := document.NewHandler(a.client)

	id, hasID := doc["id"].(string)
	if !hasID || id == "" {
		// No ID provided. Update semantics require a target ID.
		if opts.RequireExisting {
			return nil, fmt.Errorf("cannot update %s without an id: provide --id or include an \"id\" field", docType)
		}

		// No ID provided - create new document
		// Safety check for create operation
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		if name == "" {
			name = fmt.Sprintf("Untitled %s", docType)
		}

		result, err := handler.Create(document.CreateRequest{
			Name:        name,
			Type:        docType,
			Description: description,
			Content:     contentData,
			Labels:      labels,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create %s: %w", docType, err)
		}

		// Use name from input if result doesn't have it
		resultName := result.Name
		if resultName == "" {
			resultName = name
		}
		resultID := result.ID
		if resultID == "" {
			resultID = "(ID not returned)"
		}

		// File had no id field before this apply — stamp it back or hint.
		if resultID != "(ID not returned)" {
			a.applyWriteBack(a.sourceFile, resultID, docType, opts.WriteID, false, &resultWarnings)
		}

		return a.buildDocumentResult(ActionCreated, docType, resultID, resultName, tileCount, resultWarnings, labels), nil
	}

	// Check if document exists
	metadata, err := handler.GetMetadata(id)
	if err != nil {
		// A nil lookupErr means the document is genuinely absent (404). Anything
		// else leaves existence unknown and must not reach create.
		lookupErr := lookupError(docType, id, err)

		// Update semantics: do not silently create a document that does not exist.
		if opts.RequireExisting {
			if lookupErr == nil {
				return nil, fmt.Errorf("%s %q not found (use 'dtctl create document' to create it)", docType, id)
			}
			return nil, fmt.Errorf("cannot verify %s %q for update: %w", docType, id, err)
		}

		if lookupErr != nil {
			return nil, lookupErr
		}

		// Document doesn't exist, create it
		// Safety check for create operation
		if err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			return nil, err
		}

		if name == "" {
			name = fmt.Sprintf("Untitled %s", docType)
		}

		// The Documents API rejects UUID-formatted IDs during creation.
		// If the ID is a UUID (e.g., from an export), create without it and let the API generate a new ID.
		createID := id
		if isUUID(id) {
			createID = ""
			a.stderrWarn(&resultWarnings, "Creating new %s (UUID IDs cannot be reused across tenants)", docType)
		}

		result, err := handler.Create(document.CreateRequest{
			ID:          createID,
			Name:        name,
			Type:        docType,
			Description: description,
			Content:     contentData,
			Labels:      labels,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create %s: %w", docType, err)
		}

		// Use name from input if result doesn't have it
		resultName := result.Name
		if resultName == "" {
			resultName = name
		}
		resultID := result.ID
		if resultID == "" {
			resultID = id
		}

		// For the UUID case the API generated a fresh id — stamp it if requested.
		// For the non-UUID case the file already carries the id field, so neither
		// the write-back nor the hint is needed (applyWriteBack treats it as a no-op).
		fileAlreadyHasID := !isUUID(id) // non-UUID id was in the file and is preserved
		a.applyWriteBack(a.sourceFile, resultID, docType, opts.WriteID, fileAlreadyHasID, &resultWarnings)

		return a.buildDocumentResult(ActionCreated, docType, resultID, resultName, tileCount, resultWarnings, labels), nil
	}

	// Safety check for update operation - determine ownership from metadata
	ownership := a.determineOwnership(metadata.Owner)
	if err := a.checkSafety(safety.OperationUpdate, ownership); err != nil {
		return nil, err
	}

	// Show diff if requested
	if opts.ShowDiff {
		existingDoc, err := handler.Get(id)
		if err == nil && len(existingDoc.Content) > 0 {
			a.showJSONDiff(existingDoc.Content, contentData, docType)
		}
	}

	// Update the existing document (including metadata/labels if provided).
	result, err := handler.UpdateDocument(id, metadata.Version, document.UpdateRequest{
		Content:             contentData,
		ContentType:         "application/json",
		Name:                name,
		Description:         description,
		Labels:              labels,
		CreateSnapshot:      opts.CreateSnapshot,
		SnapshotDescription: opts.SnapshotDescription,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to apply %s: %w", docType, err)
	}

	// Use name from input/metadata if result doesn't have it
	resultName := result.Name
	if resultName == "" {
		resultName = name
	}
	if resultName == "" {
		resultName = metadata.Name
	}
	resultID := result.ID
	if resultID == "" {
		resultID = id
	}

	return a.buildDocumentResult(ActionUpdated, docType, resultID, resultName, tileCount, resultWarnings, labels), nil
}

// buildDocumentResult constructs the appropriate document result type based on docType.
// Custom document types (anything other than dashboard/notebook) map to the generic
// DocumentApplyResult so the emitted resourceType reflects the real type. labels are
// surfaced on the custom-document result so a round-trip apply confirms what was set.
func (a *Applier) buildDocumentResult(action, docType, id, name string, itemCount int, warnings, labels []string) ApplyResult {
	switch docType {
	case "notebook":
		return &NotebookApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       action,
				ResourceType: "notebook",
				ID:           id,
				Name:         name,
				Warnings:     warnings,
			},
			URL:          a.documentURL(docType, id),
			SectionCount: itemCount,
		}
	case "dashboard":
		return &DashboardApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       action,
				ResourceType: "dashboard",
				ID:           id,
				Name:         name,
				Warnings:     warnings,
			},
			URL:       a.documentURL(docType, id),
			TileCount: itemCount,
		}
	default:
		return &DocumentApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       action,
				ResourceType: docType,
				ID:           id,
				Name:         name,
				Warnings:     warnings,
			},
			URL:    a.documentURL(docType, id),
			Labels: labels,
		}
	}
}

// extractDocumentLabels pulls a "labels" string array from a parsed document
// payload (as produced by 'dtctl get document -o json'). Non-string entries are
// skipped; nil is returned when no usable labels are present so the update path
// treats it as "no change".
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

// countDocumentItems counts tiles (for dashboards) or sections (for notebooks)
func countDocumentItems(contentData []byte, docType string) int {
	var content map[string]interface{}
	if err := json.Unmarshal(contentData, &content); err != nil {
		return 0
	}

	if docType == "dashboard" {
		if tiles, ok := content["tiles"].([]interface{}); ok {
			return len(tiles)
		}
	} else if docType == "notebook" {
		if sections, ok := content["sections"].([]interface{}); ok {
			return len(sections)
		}
	}
	return 0
}

// itemName returns the item name for a document type: tiles for dashboards,
// sections for notebooks, and a generic "items" for custom document types
// (whose content shape is unknown).
func itemName(docType string) string {
	switch docType {
	case "dashboard":
		return "tiles"
	case "notebook":
		return "sections"
	default:
		return "items"
	}
}

// showJSONDiff displays a simple diff between two JSON documents
func (a *Applier) showJSONDiff(oldData, newData []byte, resourceType string) {
	// Pretty-print both for comparison
	var oldPretty, newPretty bytes.Buffer
	if err := json.Indent(&oldPretty, oldData, "", "  "); err != nil {
		return
	}
	if err := json.Indent(&newPretty, newData, "", "  "); err != nil {
		return
	}

	oldLines := strings.Split(oldPretty.String(), "\n")
	newLines := strings.Split(newPretty.String(), "\n")

	fmt.Fprintf(a.stderrW(), "\n--- existing %s\n+++ new %s\n", resourceType, resourceType)

	// Simple line-by-line diff
	maxLines := len(oldLines)
	if len(newLines) > maxLines {
		maxLines = len(newLines)
	}

	changes := 0
	for i := 0; i < maxLines; i++ {
		var oldLine, newLine string
		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}

		if oldLine != newLine {
			if oldLine != "" {
				fmt.Fprintf(a.stderrW(), "- %s\n", oldLine)
			}
			if newLine != "" {
				fmt.Fprintf(a.stderrW(), "+ %s\n", newLine)
			}
			changes++
		}
	}

	if changes == 0 {
		fmt.Fprintln(a.stderrW(), "(no changes)")
	}
	fmt.Fprintln(a.stderrW())
}

// documentURL returns the UI URL for a document
func (a *Applier) documentURL(docType, id string) string {
	// e.g., https://abc12345.apps.dynatrace.com -> https://abc12345.apps.dynatrace.com/ui/apps/dynatrace.dashboards/dashboard/<id>
	// Empty for custom document types, which have no known viewer app.
	return document.UIURL(a.baseURL, docType, id)
}

// dryRunDocument performs dry-run validation for a document of any type
// (dashboard, notebook, or a custom type).
func (a *Applier) dryRunDocument(docType string, doc map[string]interface{}) (ApplyResult, error) {
	id, _ := doc["id"].(string)

	// Use the same extraction/validation logic as apply
	contentData, name, _, warnings := extractDocumentContent(doc, docType)
	if name == "" {
		name = fmt.Sprintf("Untitled %s", docType)
	}

	// Count tiles/sections
	tileCount := countDocumentItems(contentData, docType)

	// Check if document exists to determine create vs update
	action := ActionCreated
	var existingName string
	if id != "" {
		handler := document.NewHandler(a.client)
		metadata, err := handler.GetMetadata(id)
		if err != nil {
			// Same rule as apply: only a 404 means create. Otherwise the dry run
			// would promise a create that the apply behind it refuses.
			if lookupErr := lookupError(docType, id, err); lookupErr != nil {
				return nil, lookupErr
			}
		} else {
			action = ActionUpdated
			existingName = metadata.Name
		}
	}

	var url string
	if id != "" {
		url = a.documentURL(docType, id)
	}

	return &DryRunResult{
		ApplyResultBase: ApplyResultBase{
			Action:       action,
			ResourceType: docType,
			ID:           id,
			Name:         name,
			Warnings:     warnings,
		},
		URL:             url,
		ItemCount:       tileCount,
		ItemType:        itemName(docType),
		ExistingName:    existingName,
		ValidationWarns: warnings,
	}, nil
}
