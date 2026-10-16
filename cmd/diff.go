package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/diff"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/suggest"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

var diffCmd = newDiffCmd()

func newDiffCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "diff [RESOURCE_TYPE] [NAME1] [NAME2] [flags]",
		Short: "Show differences between resources or files",
		Long: `Compare local files (desired state) with server resources (current state).

This command follows kubectl conventions where -f specifies the desired state
and the server provides the current state. The command auto-detects resource
type and name from the file metadata.

Examples:
  # Compare local file with server (kubectl-style)
  dtctl diff -f workflow.yaml
  
  # Explicit resource specification
  dtctl diff workflow my-workflow -f local.yaml
  
  # Compare two remote resources (dtctl extension)
  dtctl diff workflow prod-wf staging-wf
  
  # Compare two local files (dtctl extension)
  dtctl diff -f file1.yaml -f file2.yaml
  
  # Show side-by-side comparison
  dtctl diff -f workflow.yaml --side-by-side
  
  # JSON patch output
  dtctl diff -f workflow.yaml -o json-patch
  
  # Semantic diff (resource-aware)
  dtctl diff -f dashboard.yaml --semantic
  
  # Quiet mode (exit code only)
  dtctl diff -f workflow.yaml --quiet

Exit Codes:
  0 - No differences found
  1 - Differences found
  2 - Error occurred`,
		Args: cobra.RangeArgs(0, 3),
		RunE: runDiff,
	}
	c.Flags().StringSliceP("file", "f", []string{}, "Files to compare (can specify twice; - reads one side from stdin)")
	c.Flags().String("format", "unified", "Diff format: unified, side-by-side, json-patch, semantic")
	c.Flags().Bool("semantic", false, "Use semantic diff (resource-aware)")
	c.Flags().Bool("side-by-side", false, "Show side-by-side comparison")
	c.Flags().BoolP("quiet", "q", false, "No output, just exit code")
	c.Flags().Bool("ignore-metadata", false, "Ignore metadata fields (timestamps, versions)")
	c.Flags().Bool("ignore-order", false, "Ignore array order for comparison")
	c.Flags().Int("context", 3, "Number of context lines")
	c.Flags().Bool("color", true, "Colorize output")
	c.Flags().StringP("output", "o", "", "Output format (overrides --format): json-patch, semantic")
	stability.MarkFlag(c, "context", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "output", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(diffCmd)
	// Renamed or removed in 1.0 because it hides a global flag
	// (contrib breaking-changes/unshadow-global-flags.md).
}

const (
	ExitCodeNoDiff  = 0
	ExitCodeHasDiff = 1
	ExitCodeError   = 2
)

func runDiff(cmd *cobra.Command, args []string) error {
	files, _ := cmd.Flags().GetStringSlice("file")
	format, _ := cmd.Flags().GetString("format")
	semantic, _ := cmd.Flags().GetBool("semantic")
	sideBySide, _ := cmd.Flags().GetBool("side-by-side")
	quiet, _ := cmd.Flags().GetBool("quiet")
	ignoreMetadata, _ := cmd.Flags().GetBool("ignore-metadata")
	ignoreOrder, _ := cmd.Flags().GetBool("ignore-order")
	contextLines, _ := cmd.Flags().GetInt("context")
	colorize, _ := cmd.Flags().GetBool("color")
	outputFlag, _ := cmd.Flags().GetString("output")

	if outputFlag != "" {
		format = outputFlag
	}

	if sideBySide {
		format = "side-by-side"
	}

	if semantic {
		format = "semantic"
	}

	diffFormat := diff.DiffFormat(format)

	opts := diff.DiffOptions{
		Format:         diffFormat,
		IgnoreMetadata: ignoreMetadata,
		IgnoreOrder:    ignoreOrder,
		ContextLines:   contextLines,
		Colorize:       colorize,
		Semantic:       semantic,
	}

	differ := diff.NewDiffer(opts).WithVFS(vfsEnv(cmdContext(cmd)))

	var result *diff.DiffResult
	var err error

	switch {
	case len(files) == 2:
		result, err = handleTwoFiles(cmdContext(cmd), differ, files[0], files[1])
	case len(files) == 1 && len(args) == 0:
		result, err = handleFileVsRemote(cmdContext(cmd), differ, files[0])
	case len(files) == 1 && len(args) >= 2:
		result, err = handleFileVsNamedResource(cmdContext(cmd), differ, files[0], args[0], args[1])
	case len(files) == 0 && len(args) == 3:
		result, err = handleTwoRemoteResources(cmdContext(cmd), differ, args[0], args[1], args[2])
	default:
		return fmt.Errorf("invalid arguments: use -f FILE1 -f FILE2, or -f FILE, or RESOURCE_TYPE NAME1 NAME2")
	}

	if err != nil {
		return err
	}

	if !quiet {
		fmt.Fprint(currentStdout(cmdContext(cmd)), result.Patch)
	}

	if result.HasChanges {
		// diff(1) semantics: exit 1 signals "differences found" without an
		// error message — the patch itself is the output.
		return &silentExitError{code: ExitCodeHasDiff, reason: "differences found"}
	}
	return nil
}

func handleTwoFiles(ctx context.Context, differ *diff.Differ, file1, file2 string) (*diff.DiffResult, error) {
	// Stdin can be read only once, so it can be at most one side.
	if file1 == "-" && file2 == "-" {
		return nil, &suggest.FlagError{Flag: "file", Message: "-f - can name only one side of a diff: stdin can be read once"}
	}
	left, err := readFileFlag(ctx, "file", file1)
	if err != nil {
		return nil, fmt.Errorf("failed to read left file: %w", err)
	}
	right, err := readFileFlag(ctx, "file", file2)
	if err != nil {
		return nil, fmt.Errorf("failed to read right file: %w", err)
	}
	return differ.CompareData(left, right, sourceName(file1), sourceName(file2))
}

func handleFileVsRemote(ctx context.Context, differ *diff.Differ, file string) (*diff.DiffResult, error) {
	_, c, err := setupClient(ctx)
	if err != nil {
		return nil, err
	}

	localData, err := parseYAMLFile(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse file: %w", err)
	}

	resourceType, resourceID, err := extractResourceInfo(localData)
	if err != nil {
		return nil, fmt.Errorf("failed to extract resource info from file: %w", err)
	}

	remoteData, err := fetchResource(c, resourceType, resourceID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch remote resource: %w", err)
	}

	return differ.Compare(remoteData, localData, fmt.Sprintf("remote: %s/%s", resourceType, resourceID), fmt.Sprintf("local: %s", sourceName(file)))
}

func handleFileVsNamedResource(ctx context.Context, differ *diff.Differ, file, resourceType, resourceID string) (*diff.DiffResult, error) {
	_, c, err := setupClient(ctx)
	if err != nil {
		return nil, err
	}

	localData, err := parseYAMLFile(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse file: %w", err)
	}

	remoteData, err := fetchResource(c, resourceType, resourceID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch remote resource: %w", err)
	}

	return differ.Compare(remoteData, localData, fmt.Sprintf("remote: %s/%s", resourceType, resourceID), fmt.Sprintf("local: %s", sourceName(file)))
}

func handleTwoRemoteResources(ctx context.Context, differ *diff.Differ, resourceType, id1, id2 string) (*diff.DiffResult, error) {
	_, c, err := setupClient(ctx)
	if err != nil {
		return nil, err
	}

	resource1, err := fetchResource(c, resourceType, id1)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch resource %s: %w", id1, err)
	}

	resource2, err := fetchResource(c, resourceType, id2)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch resource %s: %w", id2, err)
	}

	return differ.Compare(resource1, resource2, fmt.Sprintf("%s/%s", resourceType, id1), fmt.Sprintf("%s/%s", resourceType, id2))
}

func parseYAMLFile(ctx context.Context, path string) (interface{}, error) {
	data, err := readFileFlag(ctx, "file", path)
	if err != nil {
		return nil, err
	}

	jsonData, err := format.ValidateAndConvert(data)
	if err != nil {
		return nil, fmt.Errorf("invalid file format: %w", err)
	}

	var result interface{}
	if err := json.Unmarshal(jsonData, &result); err != nil {
		return nil, err
	}

	return result, nil
}

func extractResourceInfo(data interface{}) (string, string, error) {
	m, ok := data.(map[string]interface{})
	if !ok {
		return "", "", fmt.Errorf("invalid resource format")
	}

	var resourceType string
	var resourceID string

	if typeField, ok := m["type"].(string); ok {
		resourceType = typeField
	}

	if id, ok := m["id"].(string); ok {
		resourceID = id
	} else if title, ok := m["title"].(string); ok {
		resourceID = title
	}

	if resourceType == "" {
		_, hasSchedules := m["schedules"]
		_, hasTasks := m["tasks"]
		_, hasTrigger := m["trigger"]
		_, hasOwnerType := m["ownerType"]

		if hasSchedules || (hasTasks && (hasTrigger || hasOwnerType)) {
			resourceType = "workflow"
		} else if docType, ok := m["type"].(string); ok && (docType == "dashboard" || docType == "notebook") {
			resourceType = docType
		}
	}

	if resourceType == "" || resourceID == "" {
		return "", "", fmt.Errorf("could not determine resource type or ID from file")
	}

	return resourceType, resourceID, nil
}

func fetchResource(c *client.Client, resourceType, resourceID string) (interface{}, error) {
	normalizedType := normalizeResourceType(resourceType)

	switch normalizedType {
	case "workflow":
		handler := workflow.NewHandler(c)
		wf, err := handler.Get(resourceID)
		if err != nil {
			return nil, err
		}
		return wf, nil
	case "dashboard", "notebook":
		handler := document.NewHandler(c)
		doc, err := handler.Get(resourceID)
		if err != nil {
			return nil, err
		}
		return doc, nil
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

func normalizeResourceType(resourceType string) string {
	switch resourceType {
	case "wf", "workflows":
		return "workflow"
	case "db", "dash", "dashboards":
		return "dashboard"
	case "nb", "notebooks":
		return "notebook"
	default:
		return resourceType
	}
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
