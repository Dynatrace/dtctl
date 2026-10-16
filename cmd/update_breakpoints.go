package cmd

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

const (
	breakpointDefaultOutputMessage    = "Hit on {store.rookout.frame.filename}:{store.rookout.frame.line}"
	breakpointRookoutTargetID         = "Rookout"
	breakpointRookoutTargetName       = "send_rookout"
	breakpointRookoutOnPremTargetName = "send_rookout_data_on_prem"
)

var updateBreakpointCmd = newUpdateBreakpointCmd()

func newUpdateBreakpointCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "breakpoint [<id|filename:line>]",
		Aliases: []string{"breakpoints", "bp"},
		Short:   "Update Live Debugger breakpoints and workspace filters",
		Long: `Update Live Debugger breakpoints by mutable rule ID or source location,
or update workspace filters for the current project.

Filters are workspace-scoped, so updating them with --filters also re-scopes
every existing breakpoint in the workspace. When active breakpoints would be
affected you are asked to confirm; pass --yes (-y) to skip the prompt.

Condition expressions (--condition):
  Conditions limit data collection to moments when the expression is true.
  Supported operators: ==, !=, >, >=, <, <=, in, &&, ||, (), []
  Use && for AND, || for OR, and parentheses to control precedence.
  Array and map elements can be indexed with []: arr[4]!=4, dict['a']!=4
  Use .size() to check collection length: arr.size() >= 32

  Examples:
    "value>othervalue"
    "a==1 && b!='bbb'"
    "(a<=1 || b=='bbb') && (x<y)"
    "'bbb' in a"

  Docs: https://docs.dynatrace.com/docs/shortlink/do-breakpoints#breakpoint-conditions

Examples:
  # Add or update a condition
	 dtctl update breakpoint dtctl-rule-123 --condition "value>othervalue"

  # Change the log message (use {variable} placeholders)
	 dtctl update breakpoint dtctl-rule-123 --log-message "Hit on {frame.filename}:{frame.line} value={newTodoRecord.title}"

  # Enable a breakpoint
	 dtctl update breakpoint OrderController.java:306 --enabled true

  # Disable a breakpoint
	 dtctl update breakpoint OrderController.java:306 --enabled false

  # Update workspace filters
	 dtctl update breakpoint --filters k8s.namespace.name:prod
	 dtctl update breakpoint --filters k8s.namespace.name:prod,dt.entity.host:HOST-123
`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			verbose := isDebugVerbose(cmdContext(cmd))
			filters, _ := cmd.Flags().GetString("filters")
			filtersChanged := cmd.Flags().Changed("filters")
			skipConfirm, _ := cmd.Flags().GetBool("yes")
			trailingArgs := []string{}
			if len(args) > 1 {
				trailingArgs = args[1:]
			}

			conditionChanged := cmd.Flags().Changed("condition")
			logMessageChanged := cmd.Flags().Changed("log-message")
			enabled, enabledChanged, err := getOptionalBoolFlag(cmd, "enabled", trailingArgs)
			if err != nil {
				return err
			}

			if filtersChanged {
				if err := requireFiltersValue(filters); err != nil {
					return err
				}
				if conditionChanged || enabledChanged || logMessageChanged {
					return fmt.Errorf("--filters cannot be combined with --condition, --log-message, or --enabled")
				}
				if len(args) > 0 {
					return fmt.Errorf("--filters does not accept a breakpoint identifier")
				}

				cfg, err := loadConfig(cmdContext(cmd))
				if err != nil {
					return err
				}

				ctx, err := cfg.CurrentContextObj()
				if err != nil {
					return err
				}

				if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, safety.OwnershipUnknown); err != nil {
					return err
				}

				if dryRun(cmdContext(cmd)) {
					parsed, err := parseFilters(filters)
					if err != nil {
						return err
					}
					return printBreakpointMessage(cmdContext(cmd), "update", fmt.Sprintf("Dry run: would update Live Debugger workspace filters (%s) (note: this also re-scopes existing breakpoints in the workspace)", formatFilters(parsed)))
				}

				c, err := newClientFromConfig(cmdContext(cmd), cfg)
				if err != nil {
					return err
				}

				handler, err := livedebugger.NewHandler(c, ctx.Environment)
				if err != nil {
					return err
				}

				workspaceResp, workspaceID, err := handler.GetOrCreateWorkspace(currentProjectPath())
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

				filterSets, err := buildWorkspaceFilterSets(filters)
				if err != nil {
					return err
				}

				// Changing workspace filters re-scopes every existing active
				// breakpoint in the workspace. Confirm first, unless --yes or a
				// non-interactive (--plain/agent) context. The extra read is only
				// paid when we might prompt.
				if !skipConfirm && !plainMode(cmdContext(cmd)) {
					count, err := countActiveWorkspaceBreakpoints(handler, workspaceID)
					if err != nil {
						return err
					}
					if count > 0 && !prompt.ConfirmWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), filterChangeConfirmMessage(count, false)) {
						return printBreakpointMessage(cmdContext(cmd), "update", "Cancelled")
					}
				}

				updateResp, err := handler.UpdateWorkspaceFilters(workspaceID, filterSets)
				if err != nil {
					if verbose {
						_ = printGraphQLResponse(cmdContext(cmd), "updateWorkspaceV2", updateResp)
					}
					return err
				}
				if verbose {
					if err := printGraphQLResponse(cmdContext(cmd), "updateWorkspaceV2", updateResp); err != nil {
						return err
					}
				}

				return printBreakpointMessage(cmdContext(cmd), "update", "Updated Live Debugger workspace filters")
			}

			if len(args) == 0 {
				return fmt.Errorf("accepts 1 arg(s), received 0")
			}
			if len(args) > 1 && !enabledChanged {
				return fmt.Errorf("accepts 1 arg(s), received %d", len(args))
			}
			if !conditionChanged && !enabledChanged && !logMessageChanged {
				return fmt.Errorf("at least one of --condition, --log-message, or --enabled is required")
			}

			condition, _ := cmd.Flags().GetString("condition")
			logMessage, _ := cmd.Flags().GetString("log-message")
			identifier := strings.TrimSpace(args[0])

			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return err
			}

			ctx, err := cfg.CurrentContextObj()
			if err != nil {
				return err
			}

			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, safety.OwnershipUnknown); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				changes := describeBreakpointEdits(conditionChanged, condition, logMessageChanged, logMessage, enabledChanged, enabled)
				return printBreakpointMessage(cmdContext(cmd), "update", fmt.Sprintf("Dry run: would update breakpoint %s (%s)", identifier, changes))
			}

			c, err := newClientFromConfig(cmdContext(cmd), cfg)
			if err != nil {
				return err
			}

			handler, err := livedebugger.NewHandler(c, ctx.Environment)
			if err != nil {
				return err
			}

			workspaceResp, workspaceID, err := handler.GetOrCreateWorkspace(currentProjectPath())
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

			workspaceRulesResp, err := handler.GetWorkspaceRules(workspaceID)
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

			targetRules, targetDescription, allowDirectID, err := resolveBreakpointRulesForEdit(rules, identifier)
			if err != nil {
				return err
			}

			if conditionChanged || logMessageChanged {
				if len(targetRules) == 0 {
					return fmt.Errorf("breakpoint %q not found in the current workspace", identifier)
				}
				for _, rule := range targetRules {
					ruleSettings, err := buildEditBreakpointSettings(rule, condition, conditionChanged, logMessage, logMessageChanged)
					if err != nil {
						return err
					}
					editResp, err := handler.EditBreakpoint(workspaceID, ruleSettings)
					if err != nil {
						if verbose {
							_ = printGraphQLResponse(cmdContext(cmd), "editRuleV2", editResp)
						}
						return err
					}
					if verbose {
						if err := printGraphQLResponse(cmdContext(cmd), "editRuleV2", editResp); err != nil {
							return err
						}
					}
				}
			}

			if enabledChanged {
				ruleIDs := make([]string, 0, len(targetRules))
				for _, rule := range targetRules {
					if row, ok := breakpointRowFromRule(rule); ok && row.ID != "" {
						ruleIDs = append(ruleIDs, row.ID)
					}
				}
				if len(ruleIDs) == 0 && allowDirectID {
					ruleIDs = append(ruleIDs, identifier)
				}
				if len(ruleIDs) == 0 {
					return fmt.Errorf("breakpoint %q not found in the current workspace", identifier)
				}
				enableResp, err := handler.EnableOrDisableBreakpoints(workspaceID, ruleIDs, !enabled)
				if err != nil {
					if verbose {
						_ = printGraphQLResponse(cmdContext(cmd), "enableOrDisableRules", enableResp)
					}
					return err
				}
				if verbose {
					if err := printGraphQLResponse(cmdContext(cmd), "enableOrDisableRules", enableResp); err != nil {
						return err
					}
				}
			}

			return printBreakpointMessage(cmdContext(cmd), "update", fmt.Sprintf("Updated breakpoint %s (%s)", targetDescription, describeBreakpointEdits(conditionChanged, condition, logMessageChanged, logMessage, enabledChanged, enabled)))
		},
	}
	c.Flags().String("condition", "", `Condition expression (e.g. "a==1 && b!='bbb'", "'val' in arr", "x>0 && y<=10")`)
	c.Flags().String("log-message", "", `Log message template with {variable} placeholders (e.g. "Hit on {frame.filename}:{frame.line} value={newTodoRecord.title}")`)
	c.Flags().String("enabled", "", "Enable or disable the breakpoint")
	c.Flags().String("filters", "", "workspace filters to apply (comma-separated key:value pairs)")
	c.Flags().BoolP("yes", "y", false, "skip the confirmation prompt when changing workspace filters affects existing breakpoints")
	c.Flags().Lookup("enabled").NoOptDefVal = "true"
	markLiveDebuggerExperimental(c)
	return c
}

func resolveBreakpointRulesForEdit(rules []livedebugger.BreakpointRule, identifier string) ([]livedebugger.BreakpointRule, string, bool, error) {
	if fileName, lineNumber, err := parseBreakpoint(identifier); err == nil {
		matches := findBreakpointRulesByLocation(rules, fileName, lineNumber)
		if len(matches) == 0 {
			return nil, "", false, fmt.Errorf("no breakpoints found at %s:%d", fileName, lineNumber)
		}
		return matches, fmt.Sprintf("%s:%d", fileName, lineNumber), false, nil
	}

	if rule, ok := findBreakpointRuleByID(rules, identifier); ok {
		return []livedebugger.BreakpointRule{rule}, identifier, false, nil
	}

	return nil, identifier, true, nil
}

func buildEditBreakpointSettings(rule livedebugger.BreakpointRule, condition string, conditionChanged bool, logMessage string, logMessageChanged bool) (map[string]interface{}, error) {
	row, ok := breakpointRowFromRule(rule)
	if !ok || row.ID == "" {
		return nil, fmt.Errorf("rule missing mutable rule id")
	}

	aug := rule.AugJSON
	if aug == nil {
		return nil, fmt.Errorf("rule %s missing aug_json", row.ID)
	}

	if _, hasLocation := aug["location"].(map[string]interface{}); !hasLocation {
		return nil, fmt.Errorf("rule %s missing aug_json", row.ID)
	}

	paths := extractBreakpointOperationPaths(aug)
	currentCondition := stringValue(aug["conditional"])
	if conditionChanged {
		currentCondition = condition
	}

	outputMessage := extractBreakpointOutputMessage(rule)
	if logMessageChanged {
		if logMessage == "" {
			outputMessage = unqualifyBreakpointOutputMessage(breakpointDefaultOutputMessage)
		} else {
			outputMessage = logMessage
		}
	}

	settings := map[string]interface{}{
		"mutableRuleId":               row.ID,
		"collectLocalsMethod":         stringValue(paths["store.rookout.frame"]),
		"stackTraceCollection":        stringValue(paths["store.rookout.traceback"]) == "stack.traceback()",
		"ttlHitLimit":                 intValue(aug["globalHitLimit"], 100),
		"ttlTimeLimit":                stringValue(aug["globalDisableAfterTime"]),
		"ttlTimeLimitInterval":        0,
		"collectedVariables":          extractCollectedVariables(paths),
		"targetConfiguration":         map[string]interface{}{"targetId": extractBreakpointTargetID(rule)},
		"outputMessage":               outputMessage,
		"condition":                   currentCondition,
		"rateLimit":                   stringValue(aug["rateLimit"]),
		"tracingCollection":           stringValue(paths["store.rookout.tracing"]) == "trace.dump()",
		"processMonitoringCollection": stringValue(paths["store.rookout.processMonitoring"]) == "state.dump()",
	}

	return settings, nil
}

func extractBreakpointOperationPaths(aug map[string]interface{}) map[string]interface{} {
	paths := map[string]interface{}{}
	action, ok := aug["action"].(map[string]interface{})
	if !ok {
		return paths
	}
	operations, ok := action["operations"].([]interface{})
	if !ok {
		return paths
	}
	for _, operationIfc := range operations {
		operation, ok := operationIfc.(map[string]interface{})
		if !ok {
			continue
		}
		opPaths, ok := operation["paths"].(map[string]interface{})
		if !ok {
			continue
		}
		for key, value := range opPaths {
			paths[key] = value
		}
	}
	return paths
}

func extractCollectedVariables(paths map[string]interface{}) []string {
	variables := make([]string, 0)
	for key, value := range paths {
		if !strings.HasPrefix(key, "store.rookout.variables.") {
			continue
		}
		pathValue := stringValue(value)
		if pathValue == "" {
			continue
		}
		variables = append(variables, formatCollectedVariable(pathValue))
	}
	sort.Strings(variables)
	return variables
}

func formatCollectedVariable(path string) string {
	trimmed := strings.TrimPrefix(path, "frame.")
	if className, member, ok := parseThreadLocalPath(trimmed); ok {
		return className + "." + member
	}
	return trimmed
}

func parseThreadLocalPath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, "utils.class(\"") {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, "utils.class(\"")
	className, remainder, found := strings.Cut(rest, "\")")
	if !found || className == "" {
		return "", "", false
	}
	member := strings.TrimPrefix(remainder, ".")
	if member == "" {
		return "", "", false
	}
	return className, member, true
}

// breakpointPlaceholderPattern matches {placeholder} tokens in a log message.
var breakpointPlaceholderPattern = regexp.MustCompile(`\{[^{}]*\}`)

// unqualifyBreakpointOutputMessage reverses the backend qualification of a log
// message so users see the friendly form. The backend rewrites {frame.X} to
// store.rookout.frame.X and {anything.else} to
// store.rookout.variables.anything.else; this strips those prefixes back out.
// Other namespaces (rook, agent, bp, message_info, controller) pass through
// unchanged, matching the backend.
func unqualifyBreakpointOutputMessage(message string) string {
	return breakpointPlaceholderPattern.ReplaceAllStringFunc(message, func(match string) string {
		inner := match[1 : len(match)-1]
		switch {
		case strings.HasPrefix(inner, "store.rookout.frame."):
			inner = "frame." + strings.TrimPrefix(inner, "store.rookout.frame.")
		case strings.HasPrefix(inner, "store.rookout.variables."):
			inner = strings.TrimPrefix(inner, "store.rookout.variables.")
		}
		return "{" + inner + "}"
	})
}

func extractBreakpointOutputMessage(rule livedebugger.BreakpointRule) string {
	processing := rule.Processing
	if processing == nil {
		return breakpointDefaultOutputMessage
	}
	_, ok := processing["operations"].([]interface{})
	if !ok {
		return breakpointDefaultOutputMessage
	}
	operations, ok := processing["operations"].([]interface{})
	if !ok {
		return breakpointDefaultOutputMessage
	}
	for _, operationIfc := range operations {
		operation, ok := operationIfc.(map[string]interface{})
		if !ok {
			continue
		}
		if stringValue(operation["path"]) == "temp.message.rookout.message" {
			format := stringValue(operation["format"])
			if format != "" {
				return format
			}
		}
	}
	return breakpointDefaultOutputMessage
}

func extractBreakpointTargetID(rule livedebugger.BreakpointRule) string {
	processing := rule.Processing
	if processing == nil {
		return breakpointRookoutTargetID
	}

	_, ok := processing["operations"].([]interface{})
	if !ok {
		return breakpointRookoutTargetID
	}
	operations, ok := processing["operations"].([]interface{})
	if !ok || len(operations) == 0 {
		return breakpointRookoutTargetID
	}
	lastOperation, ok := operations[len(operations)-1].(map[string]interface{})
	if !ok {
		return breakpointRookoutTargetID
	}
	name := stringValue(lastOperation["name"])
	if name == breakpointRookoutTargetName || name == breakpointRookoutOnPremTargetName {
		return breakpointRookoutTargetID
	}
	if targetID := stringValue(lastOperation["target_id"]); targetID != "" {
		return targetID
	}
	return breakpointRookoutTargetID
}

func findBreakpointRulesByLocation(rules []livedebugger.BreakpointRule, fileName string, lineNumber int) []livedebugger.BreakpointRule {
	matches := make([]livedebugger.BreakpointRule, 0)
	for _, rule := range rules {
		row, ok := breakpointRowFromRule(rule)
		if !ok {
			continue
		}
		if row.Filename == fileName && row.Line == lineNumber {
			matches = append(matches, rule)
		}
	}
	return matches
}

func findBreakpointRuleByID(rules []livedebugger.BreakpointRule, id string) (livedebugger.BreakpointRule, bool) {
	for _, rule := range rules {
		if rule.ID == id {
			return rule, true
		}
	}
	return livedebugger.BreakpointRule{}, false
}

func stringValue(value interface{}) string {
	stringVal, _ := value.(string)
	return stringVal
}

func intValue(value interface{}, defaultValue int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return defaultValue
	}
}

func describeBreakpointEdits(conditionChanged bool, condition string, logMessageChanged bool, logMessage string, enabledChanged bool, enabled bool) string {
	changes := make([]string, 0, 3)
	if conditionChanged {
		changes = append(changes, fmt.Sprintf("condition=%q", condition))
	}
	if logMessageChanged {
		displayMessage := logMessage
		if displayMessage == "" {
			displayMessage = unqualifyBreakpointOutputMessage(breakpointDefaultOutputMessage)
		}
		changes = append(changes, fmt.Sprintf("log-message=%q", displayMessage))
	}
	if enabledChanged {
		changes = append(changes, fmt.Sprintf("enabled=%t", enabled))
	}
	return strings.Join(changes, ", ")
}

func getOptionalBoolFlag(cmd *cobra.Command, flagName string, trailingArgs []string) (bool, bool, error) {
	flag := cmd.Flags().Lookup(flagName)
	if flag == nil || !flag.Changed {
		if len(trailingArgs) > 0 {
			return false, false, fmt.Errorf("accepts 1 arg(s), received %d", 1+len(trailingArgs))
		}
		return false, false, nil
	}
	if len(trailingArgs) > 1 {
		return false, true, fmt.Errorf("accepts 1 arg(s), received %d", 1+len(trailingArgs))
	}
	if len(trailingArgs) == 1 {
		parsed, err := strconv.ParseBool(strings.TrimSpace(trailingArgs[0]))
		if err != nil {
			return false, true, fmt.Errorf("invalid boolean value %q for --%s", trailingArgs[0], flagName)
		}
		return parsed, true, nil
	}

	value := strings.TrimSpace(flag.Value.String())
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, true, fmt.Errorf("invalid boolean value %q for --%s", value, flagName)
	}

	return parsed, true, nil
}

func init() {
	updateCmd.AddCommand(updateBreakpointCmd)
}
