# Diff Command Design Proposal

**Status:** Design Proposal  
**Created:** 2026-02-02  
**Author:** dtctl team

## Overview

The `diff` command provides a standalone way to compare Dynatrace resources, configurations, or query results. This complements the existing `--show-diff` flag in `apply` by offering more flexible comparison scenarios.

## Goals

1. **Flexible comparison** - Compare local files, remote resources, or combinations
2. **Multiple diff formats** - Unified, side-by-side, JSON patch, semantic
3. **Resource-agnostic** - Work with any resource type
4. **CI/CD friendly** - Exit codes and machine-readable output
5. **Semantic awareness** - Understand resource-specific equivalence

## Non-Goals

- Three-way merge conflict resolution
- Interactive diff editing (use `edit` command)
- Binary file comparison
- Git integration (users can pipe to git diff)

---

## User Experience

### Basic Usage

```bash
# Compare local file with remote resource (kubectl-style: -f is desired state)
dtctl diff -f local-workflow.yaml
# Auto-detects resource type and name from file, compares with server

# Explicit resource specification
dtctl diff workflow my-workflow -f local-workflow.yaml

# Compare two local files (extension: kubectl doesn't support this)
dtctl diff -f workflow-v1.yaml -f workflow-v2.yaml

# Compare two remote resources (extension: kubectl doesn't support this)
dtctl diff workflow prod-workflow workflow staging-workflow

# Compare remote resource with stdin (kubectl-style)
cat workflow.yaml | dtctl diff -f -

# Compare query results (extension: dtctl-specific)
dtctl diff query -f query-v1.dql -f query-v2.dql
```

### Output Formats

```bash
# Unified diff (default, kubectl-style)
dtctl diff -f new.yaml

# Side-by-side diff
dtctl diff -f new.yaml --side-by-side

# JSON patch format (RFC 6902)
dtctl diff -f new.yaml --format json-patch

# Semantic diff (resource-aware)
dtctl diff -f new.yaml --semantic

# No output, just exit code (kubectl-style)
dtctl diff -f new.yaml --quiet
```

### Example Output

**Unified diff:**
```diff
--- remote: workflow/my-workflow
+++ local: new-workflow.yaml
@@ -5,7 +5,7 @@
 title: Error Handler
 description: Handles application errors
 tasks:
-  - name: notify-team
+  - name: alert-team
     action: dynatrace.slack:slack-send-message
     input:
-      channel: "#errors"
+      channel: "#alerts"
       message: "Error detected"
```

**Side-by-side:**
```
remote: workflow/my-workflow          | local: new-workflow.yaml
--------------------------------------|--------------------------------------
title: Error Handler                  | title: Error Handler
description: Handles application err  | description: Handles application err
tasks:                                | tasks:
  - name: notify-team                 |   - name: alert-team
    action: dynatrace.slack:slack-se  |     action: dynatrace.slack:slack-se
    input:                            |     input:
      channel: "#errors"              |       channel: "#alerts"
      message: "Error detected"       |       message: "Error detected"
```

**JSON Patch:**
```json
[
  {
    "op": "replace",
    "path": "/tasks/0/name",
    "value": "alert-team"
  },
  {
    "op": "replace",
    "path": "/tasks/0/input/channel",
    "value": "#alerts"
  }
]
```

**Semantic diff:**
```
Resource: workflow/my-workflow
Type: Workflow

Changes:
  tasks[0].name: "notify-team" → "alert-team"
  tasks[0].input.channel: "#errors" → "#alerts"

Summary: 2 fields changed, 0 added, 0 removed
Impact: Low (task renamed, channel updated)
```

---

## Technical Design

### Command Structure

```go
// cmd/diff.go
var diffCmd = &cobra.Command{
    Use:   "diff -f FILENAME",
    Short: "Show differences between local file and server resource (kubectl-style)",
    Long: `Compare local file (desired state) with server resource (current state).
    
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
  dtctl diff -f file1.yaml -f file2.yaml`,
    Args: cobra.RangeArgs(0, 3),
    RunE: runDiff,
}

func init() {
    diffCmd.Flags().StringSliceP("file", "f", []string{}, "Files to compare (can specify twice)")
    diffCmd.Flags().String("format", "unified", "Diff format: unified, side-by-side, json-patch, semantic")
    diffCmd.Flags().Bool("semantic", false, "Use semantic diff (resource-aware)")
    diffCmd.Flags().Bool("side-by-side", false, "Show side-by-side comparison")
    diffCmd.Flags().BoolP("quiet", "q", false, "No output, just exit code")
    diffCmd.Flags().Bool("ignore-metadata", false, "Ignore metadata fields (timestamps, versions)")
    diffCmd.Flags().Bool("ignore-order", false, "Ignore array order for comparison")
    diffCmd.Flags().Int("context", 3, "Number of context lines")
    diffCmd.Flags().Bool("color", true, "Colorize output")
}
```

### Core Diff Engine

```go
// pkg/diff/differ.go
package diff

type Differ struct {
    options DiffOptions
}

type DiffOptions struct {
    Format         DiffFormat
    IgnoreMetadata bool
    IgnoreOrder    bool
    ContextLines   int
    Colorize       bool
    Semantic       bool
}

type DiffFormat string
const (
    DiffFormatUnified     DiffFormat = "unified"
    DiffFormatSideBySide  DiffFormat = "side-by-side"
    DiffFormatJSONPatch   DiffFormat = "json-patch"
    DiffFormatSemantic    DiffFormat = "semantic"
)

type DiffResult struct {
    HasChanges bool
    Changes    []Change
    Summary    DiffSummary
    Patch      string
}

type Change struct {
    Path      string
    Operation ChangeOperation
    OldValue  interface{}
    NewValue  interface{}
    Context   []string
}

type ChangeOperation string
const (
    ChangeOpAdd     ChangeOperation = "add"
    ChangeOpRemove  ChangeOperation = "remove"
    ChangeOpReplace ChangeOperation = "replace"
)

type DiffSummary struct {
    Added    int
    Removed  int
    Modified int
    Impact   ImpactLevel
}

type ImpactLevel string
const (
    ImpactLow      ImpactLevel = "low"
    ImpactMedium   ImpactLevel = "medium"
    ImpactHigh     ImpactLevel = "high"
    ImpactCritical ImpactLevel = "critical"
)

func NewDiffer(opts DiffOptions) *Differ
func (d *Differ) Compare(left, right interface{}) (*DiffResult, error)
func (d *Differ) CompareResources(leftType, leftID, rightType, rightID string) (*DiffResult, error)
func (d *Differ) CompareFiles(leftPath, rightPath string) (*DiffResult, error)
```

### Comparison Strategies

```go
// pkg/diff/strategies.go

// Strategy interface for different comparison approaches
type ComparisonStrategy interface {
    Compare(left, right interface{}) ([]Change, error)
}

// Structural comparison (JSON/YAML structure)
type StructuralStrategy struct {
    ignoreMetadata bool
    ignoreOrder    bool
}

func (s *StructuralStrategy) Compare(left, right interface{}) ([]Change, error) {
    leftNorm := normalize(left, s.ignoreMetadata, s.ignoreOrder)
    rightNorm := normalize(right, s.ignoreMetadata, s.ignoreOrder)
    return computeDiff(leftNorm, rightNorm)
}

// Semantic comparison (resource-aware)
type SemanticStrategy struct {
    resourceType string
}

func (s *SemanticStrategy) Compare(left, right interface{}) ([]Change, error) {
    // Use resource-specific comparison logic
    switch s.resourceType {
    case "workflow":
        return compareWorkflows(left, right)
    case "dashboard":
        return compareDashboards(left, right)
    default:
        return StructuralStrategy{}.Compare(left, right)
    }
}

// Semantic comparison examples
func compareWorkflows(left, right interface{}) ([]Change, error) {
    // Ignore task order if tasks have unique IDs
    // Treat equivalent actions as same (e.g., different versions)
    // Focus on functional changes
}

func compareDashboards(left, right interface{}) ([]Change, error) {
    // Ignore tile positions if only moved
    // Compare queries semantically
    // Ignore cosmetic changes (colors, sizes)
}
```

### Normalization

```go
// pkg/diff/normalize.go

func normalize(data interface{}, ignoreMetadata, ignoreOrder bool) interface{} {
    normalized := deepCopy(data)
    
    if ignoreMetadata {
        removeMetadataFields(normalized)
    }
    
    if ignoreOrder {
        sortArrays(normalized)
    }
    
    return normalized
}

func removeMetadataFields(data interface{}) {
    // Remove common metadata fields
    fieldsToRemove := []string{
        "metadata.createdAt",
        "metadata.updatedAt",
        "metadata.version",
        "metadata.modifiedBy",
        "id", // Optional: remove IDs for content comparison
    }
    
    for _, field := range fieldsToRemove {
        removePath(data, field)
    }
}

func sortArrays(data interface{}) {
    // Sort arrays by stable key (id, name) for order-independent comparison
    visitArrays(data, func(arr []interface{}) {
        if hasStableKey(arr) {
            sortByKey(arr)
        }
    })
}
```

### Output Formatters

```go
// pkg/diff/formatters.go

type Formatter interface {
    Format(result *DiffResult) (string, error)
}

type UnifiedFormatter struct {
    contextLines int
    colorize     bool
}

func (f *UnifiedFormatter) Format(result *DiffResult) (string, error) {
    var buf bytes.Buffer
    
    buf.WriteString("--- a\n")
    buf.WriteString("+++ b\n")
    
    for _, change := range result.Changes {
        f.writeHunk(&buf, change)
    }
    
    return buf.String(), nil
}

type SideBySideFormatter struct {
    width    int
    colorize bool
}

func (f *SideBySideFormatter) Format(result *DiffResult) (string, error) {
    // Split screen formatting
    leftWidth := f.width / 2
    rightWidth := f.width / 2
    
    // Format side-by-side with alignment
}

type JSONPatchFormatter struct{}

func (f *JSONPatchFormatter) Format(result *DiffResult) (string, error) {
    // Convert changes to RFC 6902 JSON Patch format
    patch := []map[string]interface{}{}
    
    for _, change := range result.Changes {
        op := map[string]interface{}{
            "op":   string(change.Operation),
            "path": "/" + strings.ReplaceAll(change.Path, ".", "/"),
        }
        
        if change.Operation != ChangeOpRemove {
            op["value"] = change.NewValue
        }
        
        patch = append(patch, op)
    }
    
    return json.MarshalIndent(patch, "", "  ")
}

type SemanticFormatter struct {
    resourceType string
}

func (f *SemanticFormatter) Format(result *DiffResult) (string, error) {
    var buf bytes.Buffer
    
    buf.WriteString(fmt.Sprintf("Resource: %s\n\n", f.resourceType))
    buf.WriteString("Changes:\n")
    
    for _, change := range result.Changes {
        buf.WriteString(fmt.Sprintf("  %s: %v → %v\n",
            change.Path, change.OldValue, change.NewValue))
    }
    
    buf.WriteString(fmt.Sprintf("\nSummary: %d fields changed, %d added, %d removed\n",
        result.Summary.Modified, result.Summary.Added, result.Summary.Removed))
    buf.WriteString(fmt.Sprintf("Impact: %s\n", result.Summary.Impact))
    
    return buf.String(), nil
}
```

---

## Integration with Existing Commands

### Apply Command Enhancement

```go
// cmd/apply.go

// Current: --show-diff flag
// Enhanced: Use diff package for consistent output

func runApply(cmd *cobra.Command, args []string) error {
    showDiff, _ := cmd.Flags().GetBool("show-diff")
    
    if showDiff {
        differ := diff.NewDiffer(diff.DiffOptions{
            Format:   diff.DiffFormatUnified,
            Colorize: true,
        })
        
        result, err := differ.Compare(currentState, desiredState)
        if err != nil {
            return err
        }
        
        fmt.Println(result.Patch)
        
        if !confirmApply() {
            return nil
        }
    }
    
    // Proceed with apply
}
```

### Edit Command Enhancement

```go
// cmd/edit.go

// Show diff after editing before applying
func runEdit(cmd *cobra.Command, args []string) error {
    original := fetchResource(args[0])
    edited := editInEditor(original)
    
    differ := diff.NewDiffer(diff.DiffOptions{
        Format:   diff.DiffFormatUnified,
        Colorize: true,
    })
    
    result, err := differ.Compare(original, edited)
    if err != nil {
        return err
    }
    
    if !result.HasChanges {
        fmt.Println("No changes made")
        return nil
    }
    
    fmt.Println(result.Patch)
    
    if confirmApply() {
        return applyChanges(edited)
    }
    
    return nil
}
```

---

## Use Cases

### 1. Pre-deployment Validation

```bash
# Compare local changes with production (kubectl-style)
dtctl diff -f local-error-handler.yaml

# Check impact before applying with semantic awareness
dtctl diff -f updated-dashboard.yaml --semantic

# Exit code: 0 = no changes, 1 = has changes, 2 = error (kubectl-style)
if dtctl diff -f new-workflow.yaml --quiet; then
    echo "No changes detected"
else
    echo "Changes detected, review required"
fi
```

### 2. Environment Comparison

```bash
# Compare production vs staging
dtctl get workflow prod-wf -o yaml > prod.yaml
dtctl get workflow staging-wf -o yaml > staging.yaml
dtctl diff -f prod.yaml -f staging.yaml

# Or directly
dtctl diff workflow prod-wf staging-wf
```

### 3. Configuration Drift Detection

```bash
# Compare current state with baseline (kubectl-style)
dtctl diff -f baseline/workflow.yaml --ignore-metadata

# CI/CD pipeline check (kubectl-style)
if ! dtctl diff -f baseline.yaml --quiet --ignore-metadata; then
    echo "Configuration drift detected!"
    exit 1
fi
```

### 4. Query Comparison

```bash
# Compare DQL query results
dtctl diff query -f query-v1.dql -f query-v2.dql

# Compare query output
dtctl query "fetch logs | limit 10" -o yaml > before.yaml
# ... make changes ...
dtctl query "fetch logs | limit 10" -o yaml > after.yaml
dtctl diff -f before.yaml -f after.yaml
```

### 5. Bulk Comparison

```bash
# Compare all workflows in directory (kubectl-style)
for file in workflows/*.yaml; do
    if ! dtctl diff -f "$file" --quiet; then
        echo "Drift detected in $(basename "$file")"
    fi
done
```

---

## Exit Codes

```go
const (
    ExitCodeNoDiff    = 0  // No differences found
    ExitCodeHasDiff   = 1  // Differences found
    ExitCodeError     = 2  // Error occurred
)
```

Usage in scripts (kubectl-style):
```bash
dtctl diff -f new.yaml --quiet
case $? in
    0) echo "No changes" ;;
    1) echo "Has changes" ;;
    2) echo "Error occurred" ;;
esac
```

---

## Performance Considerations

### Large File Handling

```go
// Stream large files instead of loading into memory
func compareFiles(left, right string) (*DiffResult, error) {
    if isLargeFile(left) || isLargeFile(right) {
        return streamingCompare(left, right)
    }
    return standardCompare(left, right)
}
```

### Caching

```go
// Cache remote resource fetches
type DiffCache struct {
    resources map[string]interface{}
    ttl       time.Duration
}

func (c *DiffCache) GetResource(resourceType, id string) (interface{}, error) {
    key := fmt.Sprintf("%s/%s", resourceType, id)
    if cached, ok := c.resources[key]; ok {
        return cached, nil
    }
    
    resource := fetchResource(resourceType, id)
    c.resources[key] = resource
    return resource, nil
}
```

### Parallel Comparison

```go
// Compare multiple resources in parallel
func compareBulk(pairs []ResourcePair) ([]*DiffResult, error) {
    results := make([]*DiffResult, len(pairs))
    var wg sync.WaitGroup
    
    for i, pair := range pairs {
        wg.Add(1)
        go func(idx int, p ResourcePair) {
            defer wg.Done()
            results[idx], _ = compare(p.Left, p.Right)
        }(i, pair)
    }
    
    wg.Wait()
    return results, nil
}
```

---

## Testing Strategy

### Unit Tests

```go
// pkg/diff/differ_test.go
func TestDiffer_Compare(t *testing.T) {
    tests := []struct {
        name     string
        left     interface{}
        right    interface{}
        expected *DiffResult
    }{
        {
            name: "no changes",
            left: map[string]interface{}{"key": "value"},
            right: map[string]interface{}{"key": "value"},
            expected: &DiffResult{HasChanges: false},
        },
        {
            name: "field changed",
            left: map[string]interface{}{"key": "old"},
            right: map[string]interface{}{"key": "new"},
            expected: &DiffResult{
                HasChanges: true,
                Changes: []Change{
                    {Path: "key", Operation: ChangeOpReplace, OldValue: "old", NewValue: "new"},
                },
            },
        },
        // ... more test cases
    }
}
```

### Integration Tests

```go
// test/integration/diff_test.go
func TestDiff_RemoteResources(t *testing.T) {
    // Create two workflows
    wf1 := createWorkflow("test-wf-1", workflowSpec1)
    wf2 := createWorkflow("test-wf-2", workflowSpec2)
    
    // Compare them
    result := executeDiff("workflow", wf1.ID, wf2.ID)
    
    assert.True(t, result.HasChanges)
    assert.Contains(t, result.Patch, "task-name")
}
```

### E2E Tests

```bash
# test/e2e/diff_test.sh

# Test file comparison
dtctl diff -f testdata/workflow-v1.yaml -f testdata/workflow-v2.yaml > output.txt
grep -q "task-name" output.txt || exit 1

# Test exit codes
dtctl diff -f testdata/same.yaml -f testdata/same.yaml --quiet
[ $? -eq 0 ] || exit 1

dtctl diff -f testdata/diff1.yaml -f testdata/diff2.yaml --quiet
[ $? -eq 1 ] || exit 1
```

---

## Error Handling

### Resource Not Found

```go
if err := fetchResource(resourceType, id); err != nil {
    if errors.Is(err, ErrNotFound) {
        return fmt.Errorf("resource %s/%s not found", resourceType, id)
    }
    return err
}
```

### Invalid File Format

```go
func parseFile(path string) (interface{}, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("failed to read file: %w", err)
    }
    
    // Try YAML first
    var result interface{}
    if err := yaml.Unmarshal(data, &result); err == nil {
        return result, nil
    }
    
    // Try JSON
    if err := json.Unmarshal(data, &result); err == nil {
        return result, nil
    }
    
    return nil, fmt.Errorf("file is not valid YAML or JSON")
}
```

### Type Mismatch

```go
func validateComparison(left, right interface{}) error {
    leftType := detectResourceType(left)
    rightType := detectResourceType(right)
    
    if leftType != rightType {
        return fmt.Errorf("cannot compare different resource types: %s vs %s", 
            leftType, rightType)
    }
    
    return nil
}
```

---

## Documentation Requirements

### User Documentation

```markdown
# Diff Command

Compare Dynatrace resources, local files, or query results.

## Usage

dtctl diff [RESOURCE_TYPE] [NAME] [NAME2] [flags]
dtctl diff -f FILE1 -f FILE2 [flags]

## Examples

# Compare local file with remote resource
dtctl diff workflow my-workflow -f local-workflow.yaml

# Compare two remote resources
dtctl diff workflow prod-workflow staging-workflow

# Compare with semantic awareness
dtctl diff dashboard my-dashboard -f new-dashboard.yaml --semantic

# JSON patch output
dtctl diff workflow my-wf -f new-wf.yaml --format json-patch

## Flags

-f, --file strings        Files to compare (can specify twice)
    --format string       Diff format: unified, side-by-side, json-patch, semantic
    --semantic            Use semantic diff (resource-aware)
    --side-by-side        Show side-by-side comparison
-q, --quiet               No output, just exit code
    --ignore-metadata     Ignore metadata fields
    --ignore-order        Ignore array order
    --context-lines int   Number of context lines (default 3)
    --color               Colorize output (default true)

## Exit Codes

0 - No differences found
1 - Differences found
2 - Error occurred
```

---

## Alternatives Considered

### 1. Use External Diff Tools

**Pros:**
- Leverage existing tools (diff, git diff)
- No implementation needed

**Cons:**
- Not resource-aware
- Poor UX (requires temp files)
- No semantic comparison

**Decision:** Build native diff for better UX

### 2. Only Support File Comparison

**Pros:**
- Simpler implementation
- Users can fetch resources first

**Cons:**
- Extra steps for users
- No direct resource comparison

**Decision:** Support both files and resources

### 3. Integrate into Apply Only

**Pros:**
- No new command
- Simpler CLI

**Cons:**
- Limited use cases
- Can't compare arbitrary resources

**Decision:** Standalone command with apply integration

---

## Future Enhancements

### Phase 1: Basic Implementation
- Unified diff format
- File and resource comparison
- Basic normalization

### Phase 2: Enhanced Formats
- Side-by-side output
- JSON patch format
- Colorized output

### Phase 3: Semantic Awareness
- Resource-specific comparison
- Impact analysis
- Ignore cosmetic changes

### Phase 4: Advanced Features
- Bulk comparison
- Directory comparison
- Diff templates/filters

---

## Success Metrics

- Diff command works with all resource types
- Accurate change detection (no false positives)
- Performance: < 1s for typical comparisons
- Exit codes work correctly for CI/CD
- Semantic diff reduces noise by 50%+
- User adoption in deployment workflows

---

## References

- GNU diff: https://www.gnu.org/software/diffutils/
- JSON Patch (RFC 6902): https://tools.ietf.org/html/rfc6902
- kubectl diff: https://kubernetes.io/docs/reference/generated/kubectl/kubectl-commands#diff
- git diff: https://git-scm.com/docs/git-diff
