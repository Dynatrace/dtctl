package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/hook"
	"github.com/dynatrace-oss/dtctl/pkg/resources/anomalydetector"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azureconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpconnection"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

// uuidRegex matches UUID-formatted strings (the Documents API rejects these for ID during creation)
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// stderrWarn writes a note to stderr and appends it to the warnings slice.
func (a *Applier) stderrWarn(warnings *[]string, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(a.stderrW(), "Note: %s\n", msg)
	if warnings != nil {
		*warnings = append(*warnings, msg)
	}
}

// isUUID checks if a string is a UUID format
func isUUID(s string) bool {
	return uuidRegex.MatchString(s)
}

// Applier handles resource apply operations
type Applier struct {
	client        *client.Client
	baseURL       string
	safetyChecker *safety.Checker
	currentUserID string
	preApplyHook  string    // hook command (empty = no hook)
	postApplyHook string    // post-apply hook command (empty = no hook)
	sourceFile    string    // original filename for hook context
	hookStdout    io.Writer // where hook stdout is forwarded (nil = os.Stdout)
	hookStderr    io.Writer // where hook stderr is forwarded (nil = os.Stderr)
	stderr        io.Writer // where notes, diffs and instructions go (nil = os.Stderr)
	vfsEnv        vfs.Env   // file access for the id write-back (zero value = the process's)
}

// NewApplier creates a new applier
func NewApplier(c *client.Client) *Applier {
	currentUserID, _ := c.CurrentUserID() // Ignore error - will be empty string
	return &Applier{
		client:        c,
		baseURL:       c.BaseURL(),
		currentUserID: currentUserID,
	}
}

// WithStderr sets where the applier writes its notes, diffs and setup
// instructions. nil keeps os.Stderr.
func (a *Applier) WithStderr(w io.Writer) *Applier {
	a.stderr = w
	return a
}

// WithVFS sets the filesystem and stdin the applier's id write-back uses. The
// zero Env keeps the process's.
func (a *Applier) WithVFS(env vfs.Env) *Applier {
	a.vfsEnv = env
	return a
}

// stderrW returns the writer for notes, diffs and instructions.
func (a *Applier) stderrW() io.Writer {
	if a.stderr == nil {
		return os.Stderr
	}
	return a.stderr
}

// WithSafetyChecker sets the safety checker for the applier
func (a *Applier) WithSafetyChecker(checker *safety.Checker) *Applier {
	a.safetyChecker = checker
	return a
}

// WithPreApplyHook sets the pre-apply hook command.
// The command is parsed with POSIX-style shell quoting and executed
// directly (no `sh -c`); the resource type and source file are appended as
// the final two arguments. The processed JSON is piped on stdin.
func (a *Applier) WithPreApplyHook(command string) *Applier {
	a.preApplyHook = command
	return a
}

// WithPostApplyHook sets the post-apply hook command.
// Invocation is identical to the pre-apply hook (direct exec, resource type
// and source file appended as args). Stdin is the apply result JSON. The
// hook runs after a successful apply; a non-zero exit is treated as a
// warning (the resource is already persisted), not an error.
func (a *Applier) WithPostApplyHook(command string) *Applier {
	a.postApplyHook = command
	return a
}

// WithSourceFile sets the original filename (passed to hook as context).
// This is the file path from "dtctl apply -f <file>" — informational only.
func (a *Applier) WithSourceFile(filename string) *Applier {
	a.sourceFile = filename
	return a
}

// WithHookOutputs overrides where hook stdout and stderr are forwarded.
// Pass nil to keep the default (os.Stdout / os.Stderr).
//
// In agent mode, callers should route both to os.Stderr so that hook output
// does not corrupt the JSON envelope written to os.Stdout by the printer.
func (a *Applier) WithHookOutputs(stdout, stderr io.Writer) *Applier {
	a.hookStdout = stdout
	a.hookStderr = stderr
	return a
}

// hookStdoutWriter returns the configured writer for hook stdout (defaults to os.Stdout).
func (a *Applier) hookStdoutWriter() io.Writer {
	if a.hookStdout == nil {
		return os.Stdout
	}
	return a.hookStdout
}

// hookStderrWriter returns the configured writer for hook stderr (defaults to os.Stderr).
func (a *Applier) hookStderrWriter() io.Writer {
	if a.hookStderr == nil {
		return os.Stderr
	}
	return a.hookStderr
}

// checkSafety performs a safety check if a checker is configured
func (a *Applier) checkSafety(op safety.Operation, ownership safety.ResourceOwnership) error {
	if a.safetyChecker == nil {
		return nil // No checker configured, allow operation
	}
	return a.safetyChecker.CheckError(op, ownership)
}

// determineOwnership determines resource ownership given an owner ID
func (a *Applier) determineOwnership(resourceOwnerID string) safety.ResourceOwnership {
	return safety.DetermineOwnership(resourceOwnerID, a.currentUserID)
}

// ApplyOptions holds options for apply operation
type ApplyOptions struct {
	TemplateVars    map[string]interface{}
	DryRun          bool
	Force           bool
	ShowDiff        bool
	NoHooks         bool     // skip pre-apply hooks
	OverrideID      string   // override or inject resource ID (from --id flag)
	WriteID         bool     // write created resource ID back into the source file (from --write-id flag)
	Type            string   // document type override (from --type flag); forces generic document handling
	RequireExisting bool     // fail instead of creating when a document ID is missing (update semantics)
	Labels          []string // document labels (from --label flags); when non-empty, overrides labels in the payload

	// CreateSnapshot snapshots a document's current state before overwriting it
	// (from --create-snapshot). Documents only, and only on the update path:
	// there is nothing to snapshot when the document is being created.
	CreateSnapshot bool
	// SnapshotDescription describes the snapshot created by CreateSnapshot
	// (from --snapshot-description); ignored without CreateSnapshot.
	SnapshotDescription string
}

// ResourceType represents the type of resource
type ResourceType string

const (
	ResourceWorkflow              ResourceType = "workflow"
	ResourceSchedulingRule        ResourceType = "scheduling-rule"
	ResourceDashboard             ResourceType = "dashboard"
	ResourceNotebook              ResourceType = "notebook"
	ResourceSLO                   ResourceType = "slo"
	ResourceBucket                ResourceType = "bucket"
	ResourceSettings              ResourceType = "settings"
	ResourceAWSMonitoringConfig   ResourceType = "aws_monitoring_config"
	ResourceAzureConnection       ResourceType = "azure_connection"
	ResourceAzureMonitoringConfig ResourceType = "azure_monitoring_config"
	ResourceGCPConnection         ResourceType = "gcp_connection"
	ResourceGCPMonitoringConfig   ResourceType = "gcp_monitoring_config"
	ResourceExtensionConfig       ResourceType = "extension_config"
	ResourceSegment               ResourceType = "segment"
	ResourceAnomalyDetector       ResourceType = "anomaly_detector"
	ResourceDocument              ResourceType = "document"
	ResourceUnknown               ResourceType = "unknown"
)

// Apply applies a resource configuration from file.
// Returns a slice of results (most resource types return a single-element slice;
// connection resources may return multiple results when applying a list).
func (a *Applier) Apply(fileData []byte, opts ApplyOptions) ([]ApplyResult, error) {
	// Convert to JSON if needed
	jsonData, err := format.ValidateAndConvert(fileData)
	if err != nil {
		return nil, fmt.Errorf("invalid file format: %w", err)
	}

	// Apply template rendering if variables provided
	if len(opts.TemplateVars) > 0 {
		rendered, err := template.RenderTemplate(string(jsonData), opts.TemplateVars)
		if err != nil {
			return nil, fmt.Errorf("template rendering failed: %w", err)
		}
		jsonData = []byte(rendered)
	}

	// Determine resource type. An explicit --type (opts.Type) forces generic
	// document handling and bypasses content-based detection: the user has
	// declared the type, so we must not let a heuristic override it (e.g. a
	// custom document whose content happens to carry a "tasks" field).
	var resourceType ResourceType
	var isArray bool
	if opts.Type != "" {
		if bytes.HasPrefix(bytes.TrimSpace(jsonData), []byte("[")) {
			return nil, fmt.Errorf("--type cannot be used with array input")
		}
		resourceType = ResourceDocument
	} else {
		resourceType, isArray, err = detectResourceType(jsonData)
		if err != nil {
			return nil, err
		}
	}

	// Array input: split into individual elements and apply each one
	if isArray {
		return a.applyList(resourceType, jsonData, opts)
	}

	// Inject override ID if provided via --id flag.
	// This merges the ID into the JSON before any resource handler sees it,
	// so all resource types benefit uniformly.
	if opts.OverrideID != "" {
		jsonData, err = injectID(jsonData, opts.OverrideID)
		if err != nil {
			return nil, fmt.Errorf("failed to inject --id into resource: %w", err)
		}
	}

	// Run pre-apply hook (if configured and not skipped)
	if !opts.NoHooks && a.preApplyHook != "" {
		result, err := hook.RunPreApply(
			context.Background(),
			a.preApplyHook,
			string(resourceType),
			a.sourceFile,
			jsonData,
		)
		if err != nil {
			return nil, err
		}
		if result.Stdout != "" {
			fmt.Fprint(a.hookStdoutWriter(), result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprint(a.hookStderrWriter(), result.Stderr)
		}
		if result.ExitCode != 0 {
			return nil, &HookRejectedError{
				Command:  a.preApplyHook,
				ExitCode: result.ExitCode,
				Stdout:   result.Stdout,
				Stderr:   result.Stderr,
			}
		}
	}

	if opts.DryRun {
		result, err := a.dryRun(resourceType, jsonData, opts)
		if err != nil {
			return nil, err
		}
		return []ApplyResult{result}, nil
	}

	results, err := a.applySingle(resourceType, jsonData, opts)
	if err != nil {
		return nil, err
	}

	// Run post-apply hook (if configured and not skipped).
	// Any hook failure is surfaced as a warning on stderr — the resource
	// is already persisted, so it does not flip the overall exit status.
	a.runPostApplyHook(resourceType, results, opts)

	return results, nil
}

// runPostApplyHook runs the post-apply hook for a successful (or partially
// successful) apply, once per Apply() call. Hook stdout is forwarded to the
// configured stdout writer (os.Stdout by default; redirect via
// WithHookOutputs in agent mode); hook stderr goes to the stderr writer.
// A non-zero exit is reported as a warning but does not fail the apply.
//
// If results is empty, the hook is skipped: there is nothing for it to act on.
func (a *Applier) runPostApplyHook(resourceType ResourceType, results []ApplyResult, opts ApplyOptions) {
	if opts.NoHooks || a.postApplyHook == "" || opts.DryRun {
		return
	}
	if len(results) == 0 {
		return
	}

	// Marshal the apply result(s) as JSON for the hook's stdin. Always use
	// an array shape so the hook can iterate uniformly even for single
	// resources.
	stdinJSON, err := json.Marshal(results)
	if err != nil {
		fmt.Fprintf(a.hookStderrWriter(), "Warning: failed to marshal apply result for post-apply hook: %v\n", err)
		return
	}

	result, err := hook.RunPostApply(
		context.Background(),
		a.postApplyHook,
		string(resourceType),
		a.sourceFile,
		stdinJSON,
	)
	if err != nil {
		fmt.Fprintf(a.hookStderrWriter(), "Warning: post-apply hook failed to execute: %v\n", err)
		return
	}

	// Post-apply output is always shown to the user, success or failure.
	if result.Stdout != "" {
		fmt.Fprint(a.hookStdoutWriter(), result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(a.hookStderrWriter(), result.Stderr)
	}
	if result.ExitCode != 0 {
		fmt.Fprintf(a.hookStderrWriter(), "Warning: post-apply hook exited with code %d (resource was applied successfully)\n", result.ExitCode)
	}
}

// applySingle applies a single resource object and returns the result.
func (a *Applier) applySingle(resourceType ResourceType, jsonData []byte, opts ApplyOptions) ([]ApplyResult, error) {
	var result ApplyResult
	var err error

	// Connection resources can return multiple results
	switch resourceType {
	case ResourceAzureConnection:
		return a.applyAzureConnection(jsonData)
	case ResourceGCPConnection:
		return a.applyGCPConnection(jsonData)
	default:
		// All other resource types return a single result
	}

	// Apply single-result resource types
	switch resourceType {
	case ResourceWorkflow:
		result, err = a.applyWorkflow(jsonData, opts)
	case ResourceSchedulingRule:
		result, err = a.applySchedulingRule(jsonData, opts)
	case ResourceDashboard:
		result, err = a.applyDocument(jsonData, "dashboard", opts)
	case ResourceNotebook:
		result, err = a.applyDocument(jsonData, "notebook", opts)
	case ResourceDocument:
		// Generic document of any type. opts.Type wins when set (--type flag);
		// otherwise applyDocument resolves the type from the payload's "type" field.
		result, err = a.applyDocument(jsonData, opts.Type, opts)
	case ResourceSLO:
		result, err = a.applySLO(jsonData)
	case ResourceBucket:
		result, err = a.applyBucket(jsonData)
	case ResourceSettings:
		result, err = a.applySettings(jsonData)
	case ResourceAWSMonitoringConfig:
		result, err = a.applyAWSMonitoringConfig(jsonData)
	case ResourceAzureMonitoringConfig:
		result, err = a.applyAzureMonitoringConfig(jsonData)
	case ResourceGCPMonitoringConfig:
		result, err = a.applyGCPMonitoringConfig(jsonData)
	case ResourceExtensionConfig:
		result, err = a.applyExtensionConfig(jsonData)
	case ResourceSegment:
		result, err = a.applySegment(jsonData)
	case ResourceAnomalyDetector:
		result, err = a.applyAnomalyDetector(jsonData)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
	if err != nil {
		return nil, err
	}
	return []ApplyResult{result}, nil
}

// applyList splits a JSON array into individual elements and applies each one.
// It continues on error, collecting all results and returning a combined error
// summary so that a single failure does not abort the entire batch.
func (a *Applier) applyList(resourceType ResourceType, data []byte, opts ApplyOptions) ([]ApplyResult, error) {
	// --id flag makes no sense for arrays (which element gets the ID?)
	if opts.OverrideID != "" {
		return nil, fmt.Errorf("--id flag cannot be used with array input (array contains %s resources)", resourceType)
	}

	var elements []json.RawMessage
	if err := json.Unmarshal(data, &elements); err != nil {
		return nil, fmt.Errorf("failed to parse JSON array: %w", err)
	}

	// Run pre-apply hook once on the full array (if configured and not skipped)
	if !opts.NoHooks && a.preApplyHook != "" {
		result, err := hook.RunPreApply(
			context.Background(),
			a.preApplyHook,
			string(resourceType),
			a.sourceFile,
			data,
		)
		if err != nil {
			return nil, err
		}
		if result.Stdout != "" {
			fmt.Fprint(a.hookStdoutWriter(), result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprint(a.hookStderrWriter(), result.Stderr)
		}
		if result.ExitCode != 0 {
			return nil, &HookRejectedError{
				Command:  a.preApplyHook,
				ExitCode: result.ExitCode,
				Stdout:   result.Stdout,
				Stderr:   result.Stderr,
			}
		}
	}

	var results []ApplyResult
	var errors []string

	for i, elem := range elements {
		if opts.DryRun {
			r, err := a.dryRun(resourceType, elem, opts)
			if err != nil {
				errors = append(errors, fmt.Sprintf("item %d: %s", i+1, err))
				continue
			}
			results = append(results, r)
			continue
		}

		itemResults, err := a.applySingle(resourceType, elem, opts)
		if err != nil {
			errors = append(errors, fmt.Sprintf("item %d: %s", i+1, err))
			continue
		}
		results = append(results, itemResults...)
	}

	// Run post-apply hook for the resources that were actually persisted —
	// even if some items in the batch failed. Skipping it on partial failure
	// would leave notification/cleanup hooks unfired for resources the API
	// already accepted, which is the worst outcome for the user. The hook
	// is a no-op when results is empty (see runPostApplyHook).
	a.runPostApplyHook(resourceType, results, opts)

	if len(errors) > 0 {
		return results, &ListApplyError{
			Total:    len(elements),
			Failed:   len(errors),
			Messages: errors,
		}
	}

	return results, nil
}

// detectResourceType determines the resource type from JSON data.
// Returns the resource type and whether the input is an array of resources.
func detectResourceType(data []byte) (ResourceType, bool, error) {
	// Check for array input
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		var rawList []json.RawMessage
		if err := json.Unmarshal(data, &rawList); err != nil {
			return ResourceUnknown, false, fmt.Errorf("failed to parse JSON array: %w", err)
		}
		if len(rawList) == 0 {
			return ResourceUnknown, false, fmt.Errorf("empty array: cannot detect resource type")
		}
		// Detect type from the first element
		elemType, _, err := detectResourceType(rawList[0])
		if err != nil {
			return ResourceUnknown, false, fmt.Errorf("cannot detect resource type from array element: %w", err)
		}
		return elemType, true, nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ResourceUnknown, false, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Azure Connection detection (single object)
	if schema, ok := raw["schemaId"].(string); ok && schema == azureconnection.SchemaID {
		return ResourceAzureConnection, false, nil
	}
	if schema, ok := raw["schemaId"].(string); ok && schema == gcpconnection.SchemaID {
		return ResourceGCPConnection, false, nil
	}
	// Anomaly Detector detection (raw Settings format)
	if schema, ok := raw["schemaId"].(string); ok && schema == anomalydetector.SchemaID {
		return ResourceAnomalyDetector, false, nil
	}

	// Azure Monitoring Config detection
	if scope, ok := raw["scope"].(string); ok && scope == "integration-azure" {
		return ResourceAzureMonitoringConfig, false, nil
	}

	// GCP Monitoring Config detection
	if scope, ok := raw["scope"].(string); ok && scope == "integration-gcp" {
		return ResourceGCPMonitoringConfig, false, nil
	}

	// AWS Monitoring Config detection
	if scope, ok := raw["scope"].(string); ok && scope == "integration-aws" {
		return ResourceAWSMonitoringConfig, false, nil
	}

	// Check for explicit type field
	if typeField, ok := raw["type"].(string); ok {
		switch typeField {
		case "dashboard":
			return ResourceDashboard, false, nil
		case "notebook":
			return ResourceNotebook, false, nil
		case "extension_monitoring_config":
			return ResourceExtensionConfig, false, nil
		}
	}

	// Heuristic detection based on field presence
	// Workflows have a "tasks" field; "trigger" may be absent for manual triggers
	if _, hasTasks := raw["tasks"]; hasTasks {
		return ResourceWorkflow, false, nil
	}

	// Scheduling rules are identified by their mandatory ruleType discriminator,
	// which is unique to this resource and names the body field that follows.
	if rt, ok := raw["ruleType"].(string); ok {
		switch rt {
		case "rrule", "grouping", "fixed_offset", "relative_offset":
			return ResourceSchedulingRule, false, nil
		}
	}

	// Documents have "metadata" or "content" at root level
	if _, hasMetadata := raw["metadata"]; hasMetadata {
		// Further distinguish between dashboard, notebook, and custom types
		if typeField, ok := raw["type"].(string); ok && typeField != "" {
			switch typeField {
			case "dashboard":
				return ResourceDashboard, false, nil
			case "notebook":
				return ResourceNotebook, false, nil
			default:
				return ResourceDocument, false, nil // Custom document type
			}
		}
		return ResourceDashboard, false, nil // Default to dashboard for documents
	}

	// Check for direct content format (tiles for dashboard, sections for notebook)
	if _, hasTiles := raw["tiles"]; hasTiles {
		return ResourceDashboard, false, nil
	}
	if _, hasSections := raw["sections"]; hasSections {
		return ResourceNotebook, false, nil
	}

	// Also check for "content" field which contains the actual document
	if content, hasContent := raw["content"]; hasContent {
		if contentMap, ok := content.(map[string]interface{}); ok {
			if _, hasTiles := contentMap["tiles"]; hasTiles {
				return ResourceDashboard, false, nil
			}
			if _, hasSections := contentMap["sections"]; hasSections {
				return ResourceNotebook, false, nil
			}
		}
	}

	// SLOs have "criteria" and "name" fields (and optionally customSli or sliReference)
	if _, hasCriteria := raw["criteria"]; hasCriteria {
		if _, hasName := raw["name"]; hasName {
			// Check for SLO-specific fields
			if _, hasCustomSli := raw["customSli"]; hasCustomSli {
				return ResourceSLO, false, nil
			}
			if _, hasSliRef := raw["sliReference"]; hasSliRef {
				return ResourceSLO, false, nil
			}
			// If it has criteria and name but no tasks/trigger, it's likely an SLO
			if _, hasTasks := raw["tasks"]; !hasTasks {
				return ResourceSLO, false, nil
			}
		}
	}

	// Buckets have "bucketName" and "table" fields
	if _, hasBucketName := raw["bucketName"]; hasBucketName {
		if _, hasTable := raw["table"]; hasTable {
			return ResourceBucket, false, nil
		}
	}

	// Settings objects have "schemaId"/"schemaid", "scope", and "value" fields
	// Check both camelCase (API format) and lowercase (YAML format)
	var schemaIDValue string
	hasSchemaID := false
	if v, ok := raw["schemaId"].(string); ok {
		hasSchemaID = true
		schemaIDValue = v
	} else if v, ok := raw["schemaid"].(string); ok {
		hasSchemaID = true
		schemaIDValue = v
	}

	if hasSchemaID {
		if schemaIDValue == azureconnection.SchemaID {
			// This is a single Azure Connection (credential), not a list
			return ResourceAzureConnection, false, nil
		}
		if schemaIDValue == gcpconnection.SchemaID {
			return ResourceGCPConnection, false, nil
		}
		if schemaIDValue == anomalydetector.SchemaID {
			return ResourceAnomalyDetector, false, nil
		}
		if _, hasScope := raw["scope"]; hasScope {
			if _, hasValue := raw["value"]; hasValue {
				if scope, ok := raw["scope"].(string); ok && scope == "integration-gcp" {
					return ResourceGCPMonitoringConfig, false, nil
				}
				if scope, ok := raw["scope"].(string); ok && scope == "integration-azure" {
					return ResourceAzureMonitoringConfig, false, nil
				}
				if scope, ok := raw["scope"].(string); ok && scope == "integration-aws" {
					return ResourceAWSMonitoringConfig, false, nil
				}
				return ResourceSettings, false, nil
			}
		}
	}

	// A cloud connection exported before the field projection was fixed carries
	// only objectId and value — the Settings API list used to strip schemaId and
	// scope — which leaves the authentication type in the value as the
	// document's only marker. Without this, the flattened "type" field sends
	// such a file down the generic document path and an apply creates a document
	// named after the auth type instead of updating the connection.
	// See https://github.com/dynatrace-oss/dtctl/issues/509
	if valueMap, ok := raw["value"].(map[string]interface{}); ok {
		valueType, _ := valueMap["type"].(string)
		// The hyperscaler connection schemas are discriminated unions: the value
		// carries "type" *and* a sub-object named after it. Requiring both is
		// what keeps this fallback from claiming a foreign settings object whose
		// value merely happens to have a "type" of, say, "clientSecret" — the
		// connection appliers re-marshal the value through their own struct and
		// would silently drop every field that struct does not model.
		if _, hasDiscriminant := valueMap[valueType]; hasDiscriminant {
			switch valueType {
			case azureconnection.TypeFederatedIdentityCredential, azureconnection.TypeClientSecret:
				return ResourceAzureConnection, false, nil
			case gcpconnection.TypeServiceAccountImpersonation:
				return ResourceGCPConnection, false, nil
			case awsconnection.TypeRoleBased:
				// dtctl has no AWS connection applier: an AWS connection is a plain
				// Settings object, and the settings path updates it by objectId.
				return ResourceSettings, false, nil
			}
		}
	}

	// Anomaly Detector detection (flattened format): "analyzer" with "name" subfield + "eventTemplate"
	if analyzerRaw, hasAnalyzer := raw["analyzer"]; hasAnalyzer {
		if _, hasEventTemplate := raw["eventTemplate"]; hasEventTemplate {
			if analyzerMap, ok := analyzerRaw.(map[string]interface{}); ok {
				if _, hasName := analyzerMap["name"]; hasName {
					return ResourceAnomalyDetector, false, nil
				}
			}
		}
	}

	// Filter segments: "includes" + "isPublic" is a positive, segment-specific marker.
	// We also check for "name" since it's required, and exclude known overlapping resources.
	if _, hasIncludes := raw["includes"]; hasIncludes {
		if _, hasIsPublic := raw["isPublic"]; hasIsPublic {
			return ResourceSegment, false, nil
		}
		// Fallback: "includes" + "name" without workflow/bucket/SLO markers
		if _, hasName := raw["name"]; hasName {
			_, hasTasks := raw["tasks"]
			_, hasBucketName := raw["bucketName"]
			_, hasCriteria := raw["criteria"]
			if !hasTasks && !hasBucketName && !hasCriteria {
				return ResourceSegment, false, nil
			}
		}
	}

	// Generic document fallback: any object carrying a non-empty string "type"
	// field that did not match a more specific resource above is treated as a
	// custom document (e.g. "launchpad", "acme:config"). This mirrors what
	// 'create document' accepts and enables round-trip apply/update of any
	// document type. Kept last so all specific detectors take precedence.
	if typeField, ok := raw["type"].(string); ok && typeField != "" {
		return ResourceDocument, false, nil
	}

	return ResourceUnknown, false, fmt.Errorf("could not detect resource type from file content")
}

// dryRun validates what would be applied without actually applying.
// Returns a DryRunResult with structured information about the planned operation.
func (a *Applier) dryRun(resourceType ResourceType, data []byte, opts ApplyOptions) (ApplyResult, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// For documents, check if it would be create or update
	if resourceType == ResourceDashboard || resourceType == ResourceNotebook || resourceType == ResourceDocument {
		docType := string(resourceType)
		if resourceType == ResourceDocument {
			// Resolve the concrete document type: --type wins, else payload "type".
			docType = opts.Type
			if docType == "" {
				docType, _ = doc["type"].(string)
			}
		}
		return a.dryRunDocument(docType, doc)
	}

	// Extension monitoring configs have specific fields
	if resourceType == ResourceExtensionConfig {
		return a.dryRunExtensionConfig(doc)
	}

	// Anomaly detectors are validated against the schema server-side
	if resourceType == ResourceAnomalyDetector {
		return a.dryRunAnomalyDetector(data)
	}

	// Cloud resources resolve create vs update from "objectId" plus a lookup of
	// the live list by name, so each has its own dry run that repeats exactly
	// that resolution. See https://github.com/dynatrace-oss/dtctl/issues/509
	switch resourceType {
	case ResourceAWSMonitoringConfig:
		return a.dryRunAWSMonitoringConfig(data)
	case ResourceAzureMonitoringConfig:
		return a.dryRunAzureMonitoringConfig(data)
	case ResourceGCPMonitoringConfig:
		return a.dryRunGCPMonitoringConfig(data)
	case ResourceAzureConnection:
		return a.dryRunAzureConnection(doc)
	case ResourceGCPConnection:
		return a.dryRunGCPConnection(doc)
	case ResourceSettings:
		// Settings objects (and the legacy AWS connection exports routed here)
		// resolve create vs update by looking the objectId up, falling back to
		// a create that needs schemaId and scope. See
		// https://github.com/dynatrace-oss/dtctl/issues/521
		return a.dryRunSettings(doc, data)
	}

	// For other resources, return basic info
	id, _ := doc["id"].(string)
	name, _ := doc["name"].(string)
	if name == "" {
		name, _ = doc["title"].(string)
	}

	// Settings-style objects never carry an "id" field — they use "objectId"
	// (camelCase) or "objectid" (lowercase). Read those so that dry-run agrees
	// with actual apply for every resource that identifies itself that way, not
	// just for the "settings" type.
	// See https://github.com/dynatrace-oss/dtctl/issues/256 and /issues/509
	if id == "" {
		id = objectIDFromDoc(doc)
	}

	action := ActionCreated // assume create unless we can prove otherwise
	if id != "" {
		action = ActionUpdated // has ID, likely an update (best guess without API call)
	}

	return &DryRunResult{
		ApplyResultBase: ApplyResultBase{
			Action:       action,
			ResourceType: string(resourceType),
			ID:           id,
			Name:         name,
		},
	}, nil
}

// objectIDFromDoc reads the settings-style object id out of a decoded payload.
// The field is spelled "objectId" by the API and "objectid" by a YAML
// round-trip, and neither is ever an "id".
func objectIDFromDoc(doc map[string]interface{}) string {
	if id, _ := doc["objectId"].(string); id != "" {
		return id
	}
	id, _ := doc["objectid"].(string)
	return id
}

// monitoringConfigDryRun builds the dry-run result for a cloud monitoring
// config once its apply path's create-vs-update resolution has run: a resolved
// objectID means the apply issues a PUT.
func monitoringConfigDryRun(resourceType, objectID, description, scope string, warnings []string) *DryRunResult {
	action := ActionCreated
	if objectID != "" {
		action = ActionUpdated
	}
	return &DryRunResult{
		ApplyResultBase: ApplyResultBase{
			Action:       action,
			ResourceType: resourceType,
			ID:           objectID,
			Name:         description,
			Warnings:     warnings,
		},
		Scope: scope,
	}
}

// capitalize capitalizes the first letter of a string
func capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// injectID sets the "id" field in a JSON object, overwriting any existing value.
func injectID(data []byte, id string) ([]byte, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	doc["id"] = id
	return json.Marshal(doc)
}
