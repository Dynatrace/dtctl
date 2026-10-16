package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
)

var describeBreakpointCmd = newDescribeBreakpointCmd()

func newDescribeBreakpointCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "breakpoint <id|filename:line>",
		Aliases: []string{"breakpoints", "bp"},
		Short:   "Show status details for Live Debugger breakpoint(s)",
		Long: `Show detailed status information for a Live Debugger breakpoint.

Examples:
  # Describe a breakpoint by mutable rule ID
  dtctl describe breakpoint dtctl-rule-abc123

  # Describe all breakpoints at a source location
  dtctl describe breakpoint OrderController.java:306
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDescribeBreakpoint(cmd, args[0])
		},
	}
	markLiveDebuggerExperimental(c)
	return c
}

type breakpointStatusResult struct {
	ID                 string                  `json:"id" yaml:"id"`
	Location           string                  `json:"location,omitempty" yaml:"location,omitempty"`
	Enabled            bool                    `json:"enabled" yaml:"enabled"`
	DisableReason      string                  `json:"disableReason,omitempty" yaml:"disableReason,omitempty"`
	LogMessage         string                  `json:"logMessage,omitempty" yaml:"logMessage,omitempty"`
	Status             string                  `json:"status" yaml:"status"`
	ActiveRooks        []breakpointRookInfo    `json:"activeRooks,omitempty" yaml:"activeRooks,omitempty"`
	ActiveTips         []breakpointTip         `json:"activeTips,omitempty" yaml:"activeTips,omitempty"`
	PendingRooks       []breakpointRookInfo    `json:"pendingRooks,omitempty" yaml:"pendingRooks,omitempty"`
	PendingTips        []breakpointTip         `json:"pendingTips,omitempty" yaml:"pendingTips,omitempty"`
	Warnings           []breakpointStatusIssue `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Errors             []breakpointStatusIssue `json:"errors,omitempty" yaml:"errors,omitempty"`
	ControllerWarnings []breakpointStatusIssue `json:"controllerWarnings,omitempty" yaml:"controllerWarnings,omitempty"`
	ControllerErrors   []breakpointStatusIssue `json:"controllerErrors,omitempty" yaml:"controllerErrors,omitempty"`
}

type breakpointRookInfo struct {
	ID         string `json:"id,omitempty" yaml:"id,omitempty"`
	Hostname   string `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	Executable string `json:"executable,omitempty" yaml:"executable,omitempty"`
}

type breakpointTip struct {
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	DocsLink    string `json:"docsLink,omitempty" yaml:"docsLink,omitempty"`
}

type breakpointStatusIssue struct {
	Title       string               `json:"title" yaml:"title"`
	Description string               `json:"description,omitempty" yaml:"description,omitempty"`
	DocsLink    string               `json:"docsLink,omitempty" yaml:"docsLink,omitempty"`
	Args        interface{}          `json:"args,omitempty" yaml:"args,omitempty"`
	Rooks       []breakpointRookInfo `json:"rooks,omitempty" yaml:"rooks,omitempty"`
	Controllers []string             `json:"controllers,omitempty" yaml:"controllers,omitempty"`
}

func runDescribeBreakpoint(cmd *cobra.Command, identifier string) error {
	return runDescribeBreakpointWithDeps(cmd, identifier, defaultLiveDebuggerDeps())
}

func runDescribeBreakpointWithDeps(cmd *cobra.Command, identifier string, deps liveDebuggerDeps) error {
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
		if err := printGraphQLResponse(cmdContext(cmd), "getWorkspaceRules", workspaceRulesResp); err != nil {
			return err
		}
	}

	rules, err := extractWorkspaceRules(workspaceRulesResp)
	if err != nil {
		return err
	}

	targetRules, _, allowDirectID, err := resolveBreakpointRulesForEdit(rules, strings.TrimSpace(identifier))
	if err != nil {
		return err
	}
	if allowDirectID {
		return fmt.Errorf("no breakpoint found with identifier %q", strings.TrimSpace(identifier))
	}

	results := make([]breakpointStatusResult, 0, len(targetRules))
	for _, rule := range targetRules {
		ruleID := rule.ID
		statusResp, err := deps.getRuleStatusBreakdown(handler, ruleID)
		if err != nil {
			if verbose {
				_ = printGraphQLResponse(cmdContext(cmd), "GetRuleStatusBreakdown", statusResp)
			}
			return err
		}
		if verbose {
			if err := printGraphQLResponse(cmdContext(cmd), "GetRuleStatusBreakdown", statusResp); err != nil {
				return err
			}
		}

		result, err := buildBreakpointStatusResult(rule, statusResp)
		if err != nil {
			return err
		}
		results = append(results, result)
	}

	if !useBreakpointDescribeTextView(cmdContext(cmd)) {
		printer := newPrinterCtx(cmdContext(cmd))
		_ = enrichAgent(printer, "describe", "breakpoint")
		if len(results) == 1 {
			return printer.Print(results[0])
		}
		return printer.Print(results)
	}

	for i, result := range results {
		if i > 0 {
			_, _ = fmt.Fprintln(currentStdoutOr(cmdContext(cmd), rootCmd.OutOrStdout()))
		}
		printBreakpointStatusResult(cmdContext(cmd), result)
	}

	return nil
}

func useBreakpointDescribeTextView(ctx context.Context) bool {
	if agentMode(ctx) {
		return false
	}
	return outputFormat(ctx) == "" || outputFormat(ctx) == "table" || outputFormat(ctx) == "wide" || outputFormat(ctx) == "csv"
}

func buildBreakpointStatusResult(rule livedebugger.BreakpointRule, statusResp map[string]interface{}) (breakpointStatusResult, error) {
	result := breakpointStatusResult{
		ID:      rule.ID,
		Enabled: !rule.IsDisabled,
	}
	if row, ok := breakpointRowFromRule(rule); ok {
		result.ID = row.ID
		result.Location = fmt.Sprintf("%s:%d", row.Filename, row.Line)
		result.Enabled = row.Active
	}
	result.DisableReason = rule.DisableReason
	result.LogMessage = unqualifyBreakpointOutputMessage(extractBreakpointOutputMessage(rule))

	ruleStatuses, err := extractRuleStatuses(statusResp)
	if err != nil {
		return result, err
	}

	result.ActiveRooks, result.ActiveTips = extractRooksAndTipsByStatus(ruleStatuses, "Active")
	result.PendingRooks, result.PendingTips = extractRooksAndTipsByStatus(ruleStatuses, "Pending", "None")
	result.Warnings = summarizeRookIssues(ruleStatuses, "Warning")
	result.Errors = summarizeRookIssues(ruleStatuses, "Error")
	result.ControllerWarnings = summarizeControllerIssues(ruleStatuses, "Warning")
	result.ControllerErrors = summarizeControllerIssues(ruleStatuses, "Error")
	result.Status = deriveOverallBreakpointStatus(result)

	return result, nil
}

func deriveOverallBreakpointStatus(result breakpointStatusResult) string {
	if !result.Enabled {
		return "Disabled"
	}
	if len(result.Errors) > 0 || len(result.ControllerErrors) > 0 {
		return "Error"
	}
	if len(result.Warnings) > 0 || len(result.ControllerWarnings) > 0 {
		return "Warning"
	}
	if len(result.ActiveRooks) > 0 {
		return "Active"
	}
	return "Pending"
}

func extractRuleStatuses(statusResp map[string]interface{}) ([]map[string]interface{}, error) {
	statuses, err := livedebugger.ExtractRuleStatuses(statusResp)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(statuses))
	for _, status := range statuses {
		rookStatuses := make([]interface{}, 0, len(status.RookStatuses))
		for _, rook := range status.RookStatuses {
			rookStatuses = append(rookStatuses, rook)
		}
		agentStatuses := make([]interface{}, 0, len(status.AgentStatuses))
		for _, agent := range status.AgentStatuses {
			agentStatuses = append(agentStatuses, agent)
		}
		controllerStatuses := make([]interface{}, 0, len(status.ControllerStatuses))
		for _, controller := range status.ControllerStatuses {
			controllerStatuses = append(controllerStatuses, controller)
		}

		out = append(out, map[string]interface{}{
			"status":             status.Status,
			"rookStatuses":       rookStatuses,
			"agentStatuses":      agentStatuses,
			"controllerStatuses": controllerStatuses,
		})
	}
	return out, nil
}

func extractRooksAndTipsByStatus(ruleStatuses []map[string]interface{}, statuses ...string) ([]breakpointRookInfo, []breakpointTip) {
	statusSet := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		statusSet[status] = struct{}{}
	}

	rooks := make([]breakpointRookInfo, 0)
	tipsByKey := make(map[string]breakpointTip)
	for _, ruleStatus := range ruleStatuses {
		if _, ok := statusSet[stringValue(ruleStatus["status"])]; !ok {
			continue
		}
		for _, rookStatus := range interfaceMaps(ruleStatus["rookStatuses"]) {
			if rookInfo, ok := extractRookInfo(rookStatus["rook"]); ok {
				rooks = append(rooks, rookInfo)
			}
			for _, tip := range interfaceMaps(rookStatus["tips"]) {
				tipInfo := breakpointTip{Description: stringValue(tip["description"]), DocsLink: stringValue(tip["docsLink"])}
				key := tipInfo.Description + "|" + tipInfo.DocsLink
				if tipInfo.Description != "" {
					tipsByKey[key] = tipInfo
				}
			}
		}
	}

	sort.Slice(rooks, func(i, j int) bool {
		if rooks[i].Hostname == rooks[j].Hostname {
			return rooks[i].Executable < rooks[j].Executable
		}
		return rooks[i].Hostname < rooks[j].Hostname
	})

	tips := make([]breakpointTip, 0, len(tipsByKey))
	for _, tip := range tipsByKey {
		tips = append(tips, tip)
	}
	sort.Slice(tips, func(i, j int) bool { return tips[i].Description < tips[j].Description })

	return uniqueRooks(rooks), tips
}

func summarizeRookIssues(ruleStatuses []map[string]interface{}, wantedStatus string) []breakpointStatusIssue {
	issueMap := make(map[string]*breakpointStatusIssue)
	order := make([]string, 0)
	for _, ruleStatus := range ruleStatuses {
		if stringValue(ruleStatus["status"]) != wantedStatus {
			continue
		}
		for _, rookStatus := range interfaceMaps(ruleStatus["rookStatuses"]) {
			issue := issueFromError(rookStatus["error"])
			key := issueKey(issue.Title, issue.Args)
			if _, ok := issueMap[key]; !ok {
				copy := issue
				issueMap[key] = &copy
				order = append(order, key)
			}
			if rookInfo, ok := extractRookInfo(rookStatus["rook"]); ok {
				issueMap[key].Rooks = append(issueMap[key].Rooks, rookInfo)
			}
		}
	}

	issues := make([]breakpointStatusIssue, 0, len(order))
	for _, key := range order {
		issue := *issueMap[key]
		issue.Rooks = uniqueRooks(issue.Rooks)
		issues = append(issues, issue)
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Title < issues[j].Title })
	return issues
}

func summarizeControllerIssues(ruleStatuses []map[string]interface{}, wantedStatus string) []breakpointStatusIssue {
	issueMap := make(map[string]*breakpointStatusIssue)
	order := make([]string, 0)
	for _, ruleStatus := range ruleStatuses {
		if stringValue(ruleStatus["status"]) != wantedStatus {
			continue
		}
		controllerStatuses := interfaceMaps(ruleStatus["controllerStatuses"])
		if len(controllerStatuses) == 0 {
			controllerStatuses = interfaceMaps(ruleStatus["agentStatuses"])
		}
		for _, controllerStatus := range controllerStatuses {
			issue := issueFromError(controllerStatus["error"])
			key := issueKey(issue.Title, issue.Args)
			if _, ok := issueMap[key]; !ok {
				copy := issue
				issueMap[key] = &copy
				order = append(order, key)
			}
			if controllerID := stringValue(controllerStatus["controllerId"]); controllerID != "" {
				issueMap[key].Controllers = append(issueMap[key].Controllers, controllerID)
			}
		}
	}

	issues := make([]breakpointStatusIssue, 0, len(order))
	for _, key := range order {
		issue := *issueMap[key]
		issue.Controllers = uniqueStrings(issue.Controllers)
		issues = append(issues, issue)
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Title < issues[j].Title })
	return issues
}

func issueFromError(value interface{}) breakpointStatusIssue {
	errorObj, _ := value.(map[string]interface{})
	summaryObj, _ := errorObj["summary"].(map[string]interface{})
	issue := breakpointStatusIssue{
		Title:       stringValue(summaryObj["title"]),
		Description: stringValue(summaryObj["description"]),
		DocsLink:    stringValue(summaryObj["docsLink"]),
		Args:        summaryObj["args"],
	}
	if issue.Title == "" {
		issue.Title = stringValue(errorObj["type"])
	}
	if issue.Description == "" {
		issue.Description = stringValue(errorObj["message"])
	}
	if issue.Title == "" {
		issue.Title = "Unknown issue"
	}
	return issue
}

func issueKey(title string, args interface{}) string {
	encoded, _ := json.Marshal(args)
	return title + "|" + string(encoded)
}

func extractRookInfo(value interface{}) (breakpointRookInfo, bool) {
	rookObj, ok := value.(map[string]interface{})
	if !ok {
		return breakpointRookInfo{}, false
	}
	result := breakpointRookInfo{
		ID:         stringValue(rookObj["id"]),
		Hostname:   stringValue(rookObj["hostname"]),
		Executable: stringValue(rookObj["executable"]),
	}
	if result.ID == "" && result.Hostname == "" && result.Executable == "" {
		return breakpointRookInfo{}, false
	}
	return result, true
}

func interfaceMaps(value interface{}) []map[string]interface{} {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		mapItem, ok := item.(map[string]interface{})
		if ok {
			result = append(result, mapItem)
		}
	}
	return result
}

func uniqueRooks(rooks []breakpointRookInfo) []breakpointRookInfo {
	seen := make(map[string]struct{}, len(rooks))
	result := make([]breakpointRookInfo, 0, len(rooks))
	for _, rook := range rooks {
		key := rook.ID + "|" + rook.Hostname + "|" + rook.Executable
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, rook)
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok || value == "" {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func printBreakpointStatusResult(ctx context.Context, result breakpointStatusResult) {
	w := currentStdoutOr(ctx, rootCmd.OutOrStdout())
	const kw = 16
	output.FprintDescribeKV(w, "ID:", kw, "%s", result.ID)
	if result.Location != "" {
		output.FprintDescribeKV(w, "Location:", kw, "%s", result.Location)
	}
	output.FprintDescribeKV(w, "Enabled:", kw, "%t", result.Enabled)
	if result.DisableReason != "" {
		output.FprintDescribeKV(w, "Disable reason:", kw, "%s", result.DisableReason)
	}
	if result.LogMessage != "" {
		output.FprintDescribeKV(w, "Log message:", kw, "%s", result.LogMessage)
	}
	output.FprintDescribeKV(w, "Status:", kw, "%s", result.Status)
	_, _ = fmt.Fprintln(w)

	tw := tabwriter.NewWriter(currentStdoutOr(ctx, rootCmd.OutOrStdout()), 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Active rooks:\t%d\n", len(result.ActiveRooks))
	fmt.Fprintf(tw, "Pending rooks:\t%d\n", len(result.PendingRooks))
	fmt.Fprintf(tw, "Warnings:\t%d\n", len(result.Warnings))
	fmt.Fprintf(tw, "Errors:\t%d\n", len(result.Errors))
	fmt.Fprintf(tw, "Controller warnings:\t%d\n", len(result.ControllerWarnings))
	fmt.Fprintf(tw, "Controller errors:\t%d\n", len(result.ControllerErrors))
	_ = tw.Flush()

	printBreakpointRooksSection(ctx, "Active rooks", result.ActiveRooks)
	printBreakpointTipsSection(ctx, "Active tips", result.ActiveTips)
	printBreakpointRooksSection(ctx, "Pending rooks", result.PendingRooks)
	printBreakpointTipsSection(ctx, "Pending tips", result.PendingTips)
	printBreakpointIssuesSection(ctx, "Warnings", result.Warnings)
	printBreakpointIssuesSection(ctx, "Errors", result.Errors)
	printBreakpointIssuesSection(ctx, "Controller warnings", result.ControllerWarnings)
	printBreakpointIssuesSection(ctx, "Controller errors", result.ControllerErrors)
}

func printBreakpointRooksSection(ctx context.Context, title string, rooks []breakpointRookInfo) {
	if len(rooks) == 0 {
		return
	}
	_, _ = fmt.Fprintln(currentStdoutOr(ctx, rootCmd.OutOrStdout()))
	output.FprintDescribeSection(currentStdoutOr(ctx, rootCmd.OutOrStdout()), title+":")
	for _, rook := range rooks {
		label := strings.TrimSpace(strings.Join([]string{rook.Hostname, rook.Executable}, " / "))
		if label == "/" || label == "" {
			label = rook.ID
		}
		if rook.ID != "" && rook.ID != label {
			_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "  - %s (%s)\n", label, rook.ID)
			continue
		}
		_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "  - %s\n", label)
	}
}

func printBreakpointTipsSection(ctx context.Context, title string, tips []breakpointTip) {
	if len(tips) == 0 {
		return
	}
	_, _ = fmt.Fprintln(currentStdoutOr(ctx, rootCmd.OutOrStdout()))
	output.FprintDescribeSection(currentStdoutOr(ctx, rootCmd.OutOrStdout()), title+":")
	for _, tip := range tips {
		if tip.DocsLink != "" {
			_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "  - %s (%s)\n", tip.Description, tip.DocsLink)
			continue
		}
		_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "  - %s\n", tip.Description)
	}
}

func printBreakpointIssuesSection(ctx context.Context, title string, issues []breakpointStatusIssue) {
	if len(issues) == 0 {
		return
	}
	_, _ = fmt.Fprintln(currentStdoutOr(ctx, rootCmd.OutOrStdout()))
	output.FprintDescribeSection(currentStdoutOr(ctx, rootCmd.OutOrStdout()), title+":")
	for _, issue := range issues {
		_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "  - %s\n", issue.Title)
		if issue.Description != "" {
			_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "    Description: %s\n", issue.Description)
		}
		if issue.DocsLink != "" {
			_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "    Docs:        %s\n", issue.DocsLink)
		}
		if len(issue.Rooks) > 0 {
			_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "    Rooks:       %d\n", len(issue.Rooks))
			for _, rook := range issue.Rooks {
				label := strings.TrimSpace(strings.Join([]string{rook.Hostname, rook.Executable}, " / "))
				if label == "/" || label == "" {
					label = rook.ID
				}
				_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "      - %s\n", label)
			}
		}
		if len(issue.Controllers) > 0 {
			_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "    Controllers: %d\n", len(issue.Controllers))
			for _, controller := range issue.Controllers {
				_, _ = fmt.Fprintf(currentStdoutOr(ctx, rootCmd.OutOrStdout()), "      - %s\n", controller)
			}
		}
	}
}
