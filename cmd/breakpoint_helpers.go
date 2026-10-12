package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// liveDebuggerSince is the dtctl version at which the Live Debugger surface
// entered the experimental tier. One constant rather than six literals, so a
// promotion cannot half-land.
const liveDebuggerSince = "0.39.0"

// markLiveDebuggerExperimental declares one Live Debugger command
// `experimental`, and carries the reason for the whole family.
//
// The surface is driven by the Live Debugger API, whose breakpoint and
// snapshot payload shapes are still moving, and dtctl's own decoding of those
// payloads (pkg/proto/livedebugger) is derived from them — so the CLI cannot
// promise a shape it does not control. `query --decode-snapshots` is marked as
// a flag rather than a command for the same reason: `query` itself is stable,
// only the decoding is not.
//
// Every command calls this for itself, next to where it is declared: the tier
// is an annotation on the command, not an entry in a table that has to be kept
// in step with one. Promote the family when the Live Debugger API's payload
// contract is settled.
func markLiveDebuggerExperimental(cmd *cobra.Command) {
	stability.Mark(cmd, stability.Experimental, liveDebuggerSince)
}

type liveDebuggerDeps struct {
	loadConfig             func(context.Context) (*config.Config, error)
	newClient              func(context.Context, *config.Config) (*client.Client, error)
	newHandler             func(*client.Client, string) (*livedebugger.Handler, error)
	getOrCreateWorkspace   func(*livedebugger.Handler, string) (map[string]interface{}, string, error)
	getWorkspaceRules      func(*livedebugger.Handler, string) (map[string]interface{}, error)
	getRuleStatusBreakdown func(*livedebugger.Handler, string) (map[string]interface{}, error)
}

func defaultLiveDebuggerDeps() liveDebuggerDeps {
	return liveDebuggerDeps{
		loadConfig: loadConfig,
		newClient:  newClientFromConfig,
		newHandler: livedebugger.NewHandler,
		getOrCreateWorkspace: func(handler *livedebugger.Handler, projectPath string) (map[string]interface{}, string, error) {
			return handler.GetOrCreateWorkspace(projectPath)
		},
		getWorkspaceRules: func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
			return handler.GetWorkspaceRules(workspaceID)
		},
		getRuleStatusBreakdown: func(handler *livedebugger.Handler, ruleID string) (map[string]interface{}, error) {
			return handler.GetRuleStatusBreakdown(ruleID)
		},
	}
}

type breakpointRow struct {
	ID         string `table:"ID" json:"id" yaml:"id"`
	Filename   string `table:"FILENAME" json:"filename" yaml:"filename"`
	Line       int    `table:"LINE NUMBER" json:"lineNumber" yaml:"lineNumber"`
	Active     bool   `table:"ACTIVE" json:"active" yaml:"active"`
	LogMessage string `table:"LOG MESSAGE" json:"logMessage" yaml:"logMessage"`
}

func runGetBreakpoints(cmd *cobra.Command, args []string) error {
	return runGetBreakpointsWithDeps(cmd, args, defaultLiveDebuggerDeps())
}

func runGetBreakpointsWithDeps(cmd *cobra.Command, args []string, deps liveDebuggerDeps) error {
	verbose := isDebugVerbose(cmdContext(cmd))

	cfg, err := deps.loadConfig(cmdContext(cmd))
	if err != nil {
		return err
	}

	ctx, err := cfg.CurrentContextObj()
	if err != nil {
		return err
	}

	c, err := deps.newClient(cmdContext(cmd), cfg)
	if err != nil {
		return err
	}

	handler, err := deps.newHandler(c, ctx.Environment)
	if err != nil {
		return err
	}

	workspaceResp, workspaceID, err := deps.getOrCreateWorkspace(handler, currentProjectPath())
	if err != nil {
		if verbose {
			_ = printGraphQLResponse(cmdContext(cmd), "getOrCreateWorkspaceV2", workspaceResp)
		}
		return err
	}
	if verbose {
		if err := printGraphQLResponse(cmdContext(cmd), "getOrCreateWorkspaceV2", workspaceResp); err != nil {
			return err
		}
	}

	workspaceRulesResp, err := deps.getWorkspaceRules(handler, workspaceID)
	if err != nil {
		if verbose {
			_ = printGraphQLResponse(cmdContext(cmd), "getWorkspaceRules", workspaceRulesResp)
		}
		return err
	}

	if verbose {
		return printGraphQLResponse(cmdContext(cmd), "getWorkspaceRules", workspaceRulesResp)
	}

	rows, err := extractBreakpointRows(workspaceRulesResp)
	if err != nil {
		return err
	}

	// This printer writes to rootCmd's writer rather than going through
	// NewPrinter, so it opts into the get-wide --limit/--fields itself.
	var printer output.Printer
	if agentMode(cmdContext(cmd)) {
		printer = shapeListOutput(cmdContext(cmd), output.NewAgentPrinter(currentStdoutOr(cmdContext(cmd), rootCmd.OutOrStdout()), &output.ResponseContext{}), "json", false)
	} else {
		printer = shapeListOutput(cmdContext(cmd), newPrinterOpts(cmdContext(cmd), output.PrinterOptions{Format: outputFormat(cmdContext(cmd)), Writer: currentStdoutOr(cmdContext(cmd), rootCmd.OutOrStdout()), PlainMode: plainMode(cmdContext(cmd))}),
			outputFormat(cmdContext(cmd)), output.IsTabularFormat(outputFormat(cmdContext(cmd)), plainMode(cmdContext(cmd))))
	}
	_ = enrichAgent(printer, "get", "breakpoint")
	return printer.PrintList(rows)
}

func isDebugVerbose(ctx context.Context) bool {
	return debugMode(ctx) || verbosity(ctx) > 0
}

func extractBreakpointRows(workspaceRulesResp map[string]interface{}) ([]breakpointRow, error) {
	rules, err := extractWorkspaceRules(workspaceRulesResp)
	if err != nil {
		return nil, err
	}

	rows := make([]breakpointRow, 0, len(rules))
	for _, rule := range rules {
		row, ok := breakpointRowFromRule(rule)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Filename == rows[j].Filename {
			return rows[i].Line < rows[j].Line
		}
		return rows[i].Filename < rows[j].Filename
	})

	return rows, nil
}

func extractWorkspaceRules(workspaceRulesResp map[string]interface{}) ([]livedebugger.BreakpointRule, error) {
	return livedebugger.ExtractWorkspaceRules(workspaceRulesResp)
}

func breakpointRowFromRule(rule livedebugger.BreakpointRule) (breakpointRow, bool) {
	augJSON := rule.AugJSON
	if augJSON == nil {
		return breakpointRow{}, false
	}

	location, ok := augJSON["location"].(map[string]interface{})
	if !ok {
		return breakpointRow{}, false
	}

	id := rule.ID
	filename, _ := location["filename"].(string)
	if filename == "" {
		return breakpointRow{}, false
	}

	line := 0
	switch lineno := location["lineno"].(type) {
	case int:
		line = lineno
	case int32:
		line = int(lineno)
	case int64:
		line = int(lineno)
	case float64:
		line = int(lineno)
	}

	isDisabled := rule.IsDisabled
	logMessage := unqualifyBreakpointOutputMessage(extractBreakpointOutputMessage(rule))
	return breakpointRow{ID: id, Filename: filename, Line: line, Active: !isDisabled, LogMessage: logMessage}, true
}

func currentProjectPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "no-project"
	}
	project := filepath.Base(cwd)
	if project == "" || project == "." || project == string(filepath.Separator) {
		return "no-project"
	}
	return project
}

func parseFilters(input string) (map[string][]string, error) {
	filters := map[string][]string{}
	parts := strings.Split(input, ",")
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}

		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			key, value, found = strings.Cut(trimmed, "=")
		}
		if !found {
			return nil, fmt.Errorf("invalid filter %q: expected key:value", trimmed)
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return nil, fmt.Errorf("invalid filter %q: key and value are required", trimmed)
		}

		filters[key] = append(filters[key], value)
	}

	if len(filters) == 0 {
		return nil, fmt.Errorf("no valid filters provided")
	}

	for key := range filters {
		sort.Strings(filters[key])
	}

	return filters, nil
}

// formatFilters renders parsed filters as a canonical, deterministic
// "key:value,key:value" string. Keys are sorted (values are already sorted by
// parseFilters), so dry-run previews show exactly what will be sent rather than
// the raw, possibly unnormalized, user input (e.g. "ns : prod" -> "ns:prod").
func formatFilters(parsed map[string][]string) string {
	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	pairs := make([]string, 0)
	for _, key := range keys {
		for _, value := range parsed[key] {
			pairs = append(pairs, key+":"+value)
		}
	}
	return strings.Join(pairs, ",")
}

// padBreakpointID zero-pads a numeric immutable breakpoint ID to 32 characters,
// matching the breakpoint.id format used in DQL snapshot queries.
func padBreakpointID(id string) string {
	if len(id) == 0 || len(id) >= 32 {
		return id
	}
	return strings.Repeat("0", 32-len(id)) + id
}

// requireFiltersValue validates that a --filters flag which was provided also
// carries a value. It is shared by create and update breakpoint so the
// "empty --filters" error is identical (and greppable) in both commands. An
// empty value means the flag was set but blank, e.g. --filters "".
func requireFiltersValue(filters string) error {
	if strings.TrimSpace(filters) == "" {
		return fmt.Errorf("--filters provided without a value")
	}
	return nil
}

// countActiveRules counts the enabled breakpoint rules in a GetWorkspaceRules
// GraphQL response. Disabled rules are excluded on purpose: a breakpoint that
// reached its hit limit is auto-disabled by the runtime, so it should not
// inflate the confirmation count shown before workspace filters are changed.
func countActiveRules(resp map[string]interface{}) (int, error) {
	rules, err := livedebugger.ExtractWorkspaceRules(resp)
	if err != nil {
		return 0, err
	}
	count := 0
	for i := range rules {
		if !rules[i].IsDisabled {
			count++
		}
	}
	return count, nil
}

// workspaceFilterOps are the Live Debugger calls applyWorkspaceFilters makes,
// split out so tests can stand in for the handler.
type workspaceFilterOps struct {
	getWorkspaceRules      func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error)
	updateWorkspaceFilters func(handler *livedebugger.Handler, workspaceID string, filterSets []map[string]interface{}) (map[string]interface{}, error)
}

func defaultWorkspaceFilterOps() workspaceFilterOps {
	return workspaceFilterOps{
		getWorkspaceRules: func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
			return handler.GetWorkspaceRules(workspaceID)
		},
		updateWorkspaceFilters: func(handler *livedebugger.Handler, workspaceID string, filterSets []map[string]interface{}) (map[string]interface{}, error) {
			return handler.UpdateWorkspaceFilters(workspaceID, filterSets)
		},
	}
}

// applyWorkspaceFilters replaces the workspace's filters with sets, one filter
// set per map. It is the one place create and update breakpoint change
// workspace filters.
//
// Changing them re-scopes every existing active breakpoint in the workspace,
// not just one being created, so it confirms first; creating says whether the
// same operation also creates a breakpoint, for the prompt. --yes
// (skipConfirm) skips the confirmation, and so does a non-interactive
// (--plain/agent) run unless countAlways is set: then a re-scope nobody
// confirms is declined rather than made. The extra read that counts the
// active breakpoints is only paid when the run might prompt.
//
// cancelled reports a declined confirmation; the workspace is unchanged then,
// and the caller says so.
func applyWorkspaceFilters(ctx context.Context, handler *livedebugger.Handler, workspaceID string, sets []map[string][]string, creating, skipConfirm, countAlways bool) (cancelled bool, err error) {
	return applyWorkspaceFiltersWithOps(ctx, handler, workspaceID, sets, creating, skipConfirm, countAlways, defaultWorkspaceFilterOps())
}

func applyWorkspaceFiltersWithOps(ctx context.Context, handler *livedebugger.Handler, workspaceID string, sets []map[string][]string, creating, skipConfirm, countAlways bool, ops workspaceFilterOps) (bool, error) {
	verbose := isDebugVerbose(ctx)

	if !skipConfirm && (!plainMode(ctx) || countAlways) {
		rulesResp, err := ops.getWorkspaceRules(handler, workspaceID)
		if err != nil {
			return false, err
		}
		count, err := countActiveRules(rulesResp)
		if err != nil {
			return false, err
		}
		if count > 0 && !prompt.ConfirmWith(currentStdin(ctx), currentStdout(ctx), filterChangeConfirmMessage(count, creating)) {
			return true, nil
		}
	}

	updateResp, err := ops.updateWorkspaceFilters(handler, workspaceID, livedebugger.BuildFilterSets(sets...))
	if err != nil {
		if verbose {
			_ = printGraphQLResponse(ctx, "updateWorkspaceV2", updateResp)
		}
		return false, err
	}
	if verbose {
		if err := printGraphQLResponse(ctx, "updateWorkspaceV2", updateResp); err != nil {
			return false, err
		}
	}
	return false, nil
}

// filterChangeConfirmMessage builds the prompt shown before changing workspace
// filters, which re-scopes every existing active breakpoint in the workspace.
// creating indicates the same operation also creates a new breakpoint.
func filterChangeConfirmMessage(count int, creating bool) string {
	noun := "breakpoints"
	if count == 1 {
		noun = "breakpoint"
	}
	if creating {
		return fmt.Sprintf("In addition to creating this breakpoint, this will change the workspace filters for %d active %s. Continue?", count, noun)
	}
	return fmt.Sprintf("This will change the workspace filters for %d active %s. Continue?", count, noun)
}

func parseBreakpoint(input string) (string, int, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", 0, fmt.Errorf("breakpoint cannot be empty")
	}

	fileName, lineString, found := strings.Cut(trimmed, ":")
	if !found {
		return "", 0, fmt.Errorf("invalid breakpoint %q: expected File.java:line", trimmed)
	}

	fileName = strings.TrimSpace(fileName)
	lineString = strings.TrimSpace(lineString)
	if fileName == "" || lineString == "" {
		return "", 0, fmt.Errorf("invalid breakpoint %q: file and line are required", trimmed)
	}

	lineNumber, err := strconv.Atoi(lineString)
	if err != nil || lineNumber <= 0 {
		return "", 0, fmt.Errorf("invalid breakpoint line %q: must be a positive integer", lineString)
	}

	return fileName, lineNumber, nil
}

func printGraphQLResponse(ctx context.Context, operation string, payload map[string]interface{}) error {
	if payload == nil {
		return nil
	}

	wrapper := buildGraphQLResponse(operation, payload)

	encoded, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s response: %w", operation, err)
	}

	_, _ = fmt.Fprintln(currentStdoutOr(ctx, rootCmd.OutOrStdout()), string(encoded))
	return nil
}

func buildGraphQLResponse(operation string, payload map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"operation": operation,
		"response":  payload,
	}
}
