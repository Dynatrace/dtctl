package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/commands"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	resapi "github.com/dynatrace-oss/dtctl/pkg/resources/api"
)

// scope-check verdict statuses.
const (
	scopeStatusOK           = "ok"
	scopeStatusInsufficient = "insufficient_scope"
	scopeStatusUnknown      = "unknown"
)

// ScopeError is returned by the agent-mode auto-preflight when a mutating
// command is missing required scopes, and by the DQL scope precheck. It is
// rendered as an insufficient_scope envelope (see errorToDetail) and exits with
// ExitPermissionError.
type ScopeError struct {
	Verb     string
	Resource string
	Required []string
	Granted  []string
	Missing  []string
	// Alternatives are scope sets accepted in place of Required; the token
	// holds none of them in full.
	Alternatives [][]string
	// Reason, when set, replaces the generic message: it says which part of
	// the invocation needs the missing scopes.
	Reason string
	// Advice, when set, replaces the generic suggestions.
	Advice []string
}

func (e *ScopeError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	target := e.Verb
	if e.Resource != "" {
		target += " " + e.Resource
	}
	return fmt.Sprintf("missing %d required scope(s) for %q: %s",
		len(e.Missing), target, strings.Join(e.Missing, ", "))
}

// silentExitError carries a process exit code without producing any output.
// Used by the explicit --check-scopes path, which prints its own verdict and
// then needs to set a non-zero exit code without root.go printing a second error.
type silentExitError struct {
	code int
	// reason is recorded as the root span's status message for non-zero
	// codes; it is never printed to the user (the command already produced
	// its output — the code is part of its contract, not an error).
	reason string
}

func (e *silentExitError) Error() string { return "" }

// ScopeCheckResult is the verdict emitted by `--check-scopes`.
type ScopeCheckResult struct {
	Verb           string   `json:"verb" yaml:"verb"`
	Resource       string   `json:"resource,omitempty" yaml:"resource,omitempty"`
	Status         string   `json:"status" yaml:"status"` // ok | insufficient_scope | unknown
	RequiredScopes []string `json:"required_scopes" yaml:"required_scopes"`
	// AlternativeScopes are scope sets the endpoint accepts in place of
	// RequiredScopes; holding any one set in full is as good as holding them.
	AlternativeScopes [][]string `json:"alternative_scopes,omitempty" yaml:"alternative_scopes,omitempty"`
	// SatisfiedBy is the alternative set an ok verdict rests on, when the
	// token lacks RequiredScopes but holds one of AlternativeScopes.
	SatisfiedBy   []string `json:"satisfied_by,omitempty" yaml:"satisfied_by,omitempty"`
	GrantedScopes []string `json:"granted_scopes,omitempty" yaml:"granted_scopes,omitempty"`
	MissingScopes []string `json:"missing_scopes,omitempty" yaml:"missing_scopes,omitempty"`
	Suggestions   []string `json:"suggestions,omitempty" yaml:"suggestions,omitempty"`
}

// installScopePreflight wraps the RunE of every runnable command so that a scope
// preflight runs before the command body. It mirrors setupErrorHandlers' tree
// walk. Commands without a RunE (pure parent verbs) are skipped.
func installScopePreflight(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		installScopePreflight(sub)
	}
	orig := cmd.RunE
	if orig == nil {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		skip, err := scopePreflight(c, args)
		if err != nil {
			return err
		}
		if skip {
			return nil
		}
		return adminAccessError(c, orig(c, args))
	}
}

// scopePreflight implements the --check-scopes verdict and the agent-mode
// auto-preflight. It returns (skip=true) when the original command body should
// not run, and an error to abort with. The preflight is intentionally resilient:
// any failure to determine scopes degrades to "proceed" rather than blocking a
// command, so it can never turn a working command into a broken one.
func scopePreflight(c *cobra.Command, args []string) (skip bool, err error) {
	// Only do work when explicitly requested or when auto-preflighting in agent mode.
	if !checkScopes(cmdContext(c)) && !agentMode(cmdContext(c)) {
		return false, nil
	}

	verb, resource := verbResource(c)

	if checkScopes(cmdContext(c)) {
		required, req := scopesForInvocation(c, verb, resource)
		if req == scopeRequirementPerCall {
			// --check-scopes is explicit and terminal: the command body does not run
			// afterwards, so resolving the requirement from the environment's own
			// specification spends one round trip the caller asked for. The agent-mode
			// auto-preflight below deliberately does not do this — it runs ahead of
			// every command and must stay free.
			if scopes, ok := resolvePerCallScopes(c, args); ok {
				required, req = scopes, scopeRequirementKnown
			}
		}
		var alternatives [][]string
		if req == scopeRequirementKnown {
			alternatives = alternativesForInvocation(c, verb, resource)
		}
		result := computeScopeVerdict(cmdContext(c), verb, resource, required, alternatives, req)
		// In agent mode the verdict must use the same envelope contract as every
		// other command: an insufficient verdict reuses the ScopeError path (so it
		// renders as an `insufficient_scope` error envelope with exit 5, identical
		// to the auto-preflight), and ok/unknown verdicts are wrapped in an OK
		// envelope rather than printed as a bare object.
		if agentMode(cmdContext(c)) {
			if result.Status == scopeStatusInsufficient {
				return false, &ScopeError{
					Verb:         verb,
					Resource:     resource,
					Required:     result.RequiredScopes,
					Granted:      result.GrantedScopes,
					Missing:      result.MissingScopes,
					Alternatives: result.AlternativeScopes,
					Advice:       result.Suggestions,
				}
			}
			printScopeVerdictAgent(cmdContext(c), result)
			return true, nil
		}
		printScopeVerdict(cmdContext(c), result)
		if result.Status == scopeStatusInsufficient {
			return true, &silentExitError{code: client.ExitPermissionError, reason: "insufficient scope"}
		}
		return true, nil
	}

	// Agent-mode auto-preflight: only block mutating commands whose missing
	// scopes we can prove from an introspectable (OAuth) token. The mutating
	// check is first so non-mutating commands (the common path) never pay for
	// resolving required scopes.
	if _, mutating := commands.MutatingVerbs[verb]; !mutating {
		return false, nil
	}
	// Only a catalog-known requirement can prove a command will fail. A per-call
	// requirement would have to be resolved over the network on every single
	// invocation, which is not a price a preflight may charge — `exec api` reports
	// the platform's own 403, with the declared scope, when it happens.
	required, req := scopesForInvocation(c, verb, resource)
	if req != scopeRequirementKnown {
		return false, nil
	}
	granted, known := grantedScopesFunc(cmdContext(c))
	if !known {
		return false, nil
	}
	missing := subtractScopes(required, granted)
	if len(missing) == 0 {
		return false, nil
	}
	alternatives := alternativesForInvocation(c, verb, resource)
	if grantedAlternative(alternatives, granted) != nil {
		return false, nil
	}
	return false, &ScopeError{
		Verb:         verb,
		Resource:     resource,
		Required:     required,
		Granted:      granted,
		Missing:      missing,
		Alternatives: alternatives,
		Advice:       insufficientScopeAdvice(missing, alternatives),
	}
}

// verbResource derives the verb and resource for a resolved command. The verb is
// the ancestor whose parent is the root command; the resource is the leaf
// command name (empty when the leaf is the verb itself, e.g. `query`).
func verbResource(c *cobra.Command) (verb, resource string) {
	// Stop below whichever root c's tree has: the singleton for the CLI, a
	// per-invocation tree's own for a concurrent embedder. Comparing against
	// rootCmd walked a fresh tree up to its root, so every command resolved to
	// the verb "dtctl" and the agent-mode preflight never ran.
	node := c
	for node.Parent() != nil && node.Parent().HasParent() {
		node = node.Parent()
	}
	verb = node.Name()
	if c != node {
		resource = c.Name()
	}
	return verb, resource
}

// scopeRequirement is what the catalog can say about a command's scopes.
//
// The two-way answer this replaced could not tell "needs no scope" apart from
// "cannot say", so it reported both as ok — and an ok verdict for a command
// nothing was checked against is worse than no verdict at all.
type scopeRequirement int

const (
	// scopeRequirementNone: the command touches no platform API (ctx, commands).
	scopeRequirementNone scopeRequirement = iota
	// scopeRequirementKnown: the catalog names the scopes.
	scopeRequirementKnown
	// scopeRequirementPerCall: the scopes are a property of the invocation, not of
	// the command, so the catalog cannot hold them.
	scopeRequirementPerCall
)

// perCallScopeCommands are `<verb> <resource>` leaves whose required scopes
// depend on their arguments.
//
// `exec api` is the only one: it can reach any endpoint the environment
// publishes, and each declares its own scope. Listing a union of every scope in
// the catalog would be both wrong and useless, so the requirement is resolved per
// call — from the environment's specification — or honestly reported as unknown.
var perCallScopeCommands = map[string]bool{
	"exec api": true,
}

// flagScopeRequirements are scopes that a flag adds to its command's catalog
// requirement, keyed by `<verb> <resource>` and then by flag name.
//
// The catalog keys scopes by (verb, resource), so it cannot express "this flag
// reaches a second API". `get documents --admin-access` lists documents as their
// effective owner, which needs `document:documents:admin` on top of the read
// scope every `get documents` needs; declaring that scope on the `document`
// resource instead would demand it from every plain `get documents` too, which
// does not use it.
//
// Only list a scope here when the flag makes it a certainty. A green verdict for
// a call that then fails is the bug this table fixes, but the reverse — a scope
// demanded for an invocation that does not need it — is worse than no entry at
// all: the agent-mode auto-preflight refuses mutating commands on a missing
// required scope, so a speculative entry turns a working command into a blocked
// one.
//
// Every scope listed here must also be requested by `dtctl auth login` at some
// safety level (TestFlagScopesAreRequestedAtLogin): the preflight can only name
// a scope, and naming one that no login ever requests is how #375 happened.
var flagScopeRequirements = map[string]map[string][]string{
	// Documents, dashboards and notebooks are the same API; the flag is declared
	// on each of the three leaves, so the requirement is too.
	"get documents":  {"admin-access": {auth.DocumentAdminScope}},
	"get dashboards": {"admin-access": {auth.DocumentAdminScope}},
	"get notebooks":  {"admin-access": {auth.DocumentAdminScope}},

	// The workflow commands' --admin-access sends adminAccess=true on every
	// Automation API call, which the API refuses without
	// automation:workflows:admin (#700). `apply` declares the flag too, but its
	// scopes depend on the file, so the catalog has none to add it to.
	"get workflows":               {"admin-access": {auth.WorkflowAdminScope}},
	"describe workflow":           {"admin-access": {auth.WorkflowAdminScope}},
	"edit workflow":               {"admin-access": {auth.WorkflowAdminScope}},
	"delete workflow":             {"admin-access": {auth.WorkflowAdminScope}},
	"history workflow":            {"admin-access": {auth.WorkflowAdminScope}},
	"restore workflow":            {"admin-access": {auth.WorkflowAdminScope}},
	"get workflow-executions":     {"admin-access": {auth.WorkflowAdminScope}},
	"describe workflow-execution": {"admin-access": {auth.WorkflowAdminScope}},
	"logs workflow-execution":     {"admin-access": {auth.WorkflowAdminScope}},
	"get wfe-task-result":         {"admin-access": {auth.WorkflowAdminScope}},

	// Activating a version touches extension *definitions*;
	// --with-configurations additionally reads every monitoring configuration of
	// the extension and writes each one back against the new version's schema.
	"update extension": {"with-configurations": {
		"extensions:configurations:read", "extensions:configurations:write",
	}},
	"update extensions": {"with-configurations": {
		"extensions:configurations:read", "extensions:configurations:write",
	}},
}

// scopesForInvocation is requiredScopesFor plus the scopes that the flags set on
// this particular invocation contribute. It is what both callers want: the
// requirement of the command line in front of us, not of the command in general.
func scopesForInvocation(c *cobra.Command, verb, resource string) ([]string, scopeRequirement) {
	required, req := requiredScopesIn(c.Root(), verb, resource)
	extra := flagContributedScopes(c, verb, resource)
	if len(extra) == 0 || req != scopeRequirementKnown {
		// Augmenting anything but a known requirement would invent one: a per-call
		// command's scopes come from the endpoint it resolves, and a command the
		// catalog gives no scopes for is either local or a catalog bug — neither is
		// fixed by attributing the whole requirement to a flag. A table entry on
		// such a command is caught by TestFlagScopeRequirementsAreWellFormed.
		return required, req
	}
	return unionScopes(required, extra), scopeRequirementKnown
}

// flagContributedScopes returns the scopes contributed by the flags actually in
// effect on this invocation. A flag that is registered but off contributes
// nothing, so `--admin-access=false` is the same as omitting it.
func flagContributedScopes(c *cobra.Command, verb, resource string) []string {
	byFlag, ok := flagScopeRequirements[strings.TrimSpace(verb+" "+resource)]
	if !ok {
		return nil
	}
	var extra []string
	for name, scopes := range byFlag {
		f := c.Flags().Lookup(name)
		if f == nil {
			continue
		}
		if f.Value.Type() == "bool" {
			if on, err := c.Flags().GetBool(name); err != nil || !on {
				continue
			}
		} else if !f.Changed || f.Value.String() == "" {
			continue
		}
		extra = append(extra, scopes...)
	}
	return extra
}

// alternativesForInvocation returns the scope sets that satisfy this invocation
// in place of its catalog requirement (auth.AccessScopes.Alternatives), or nil.
//
// An alternative stands in for the resource's own scopes only. What a flag adds
// reaches a different API, so it stays required whichever alternative is held.
func alternativesForInvocation(c *cobra.Command, verb, resource string) [][]string {
	if resource == "" {
		return nil
	}
	access := auth.AccessForVerb(verb, commands.MutatingVerbs[verb])
	alternatives := auth.AlternativeScopesForResource(resource, access)
	if len(alternatives) == 0 {
		return nil
	}
	extra := flagContributedScopes(c, verb, resource)
	out := make([][]string, 0, len(alternatives))
	for _, alt := range alternatives {
		out = append(out, unionScopes(alt, extra))
	}
	return out
}

// grantedAlternative returns the first alternative whose every scope is granted,
// or nil when none is.
func grantedAlternative(alternatives [][]string, granted []string) []string {
	for _, alt := range alternatives {
		if len(alt) > 0 && len(subtractScopes(alt, granted)) == 0 {
			return alt
		}
	}
	return nil
}

// formatAlternatives renders scope sets as "a | b + c": any one set, each set
// needing all of its scopes.
func formatAlternatives(alternatives [][]string) string {
	sets := make([]string, 0, len(alternatives))
	for _, alt := range alternatives {
		sets = append(sets, strings.Join(alt, " + "))
	}
	return strings.Join(sets, " | ")
}

// insufficientScopeAdvice is what a caller can do about missing scopes: add the
// missing ones, or — where the endpoint accepts them — any one alternative set.
func insufficientScopeAdvice(missing []string, alternatives [][]string) []string {
	cfg, _ := LoadConfig()
	advice := loginScopeAdvice(cfg, missing)
	if len(alternatives) > 0 {
		advice = append(advice, "or with any one of these instead: "+formatAlternatives(alternatives))
	}
	return append(advice, "see 'dtctl commands howto' for token scope guidance")
}

// loginScopeAdvice says whether logging in again, or only a higher safety
// level, gets the missing scopes.
func loginScopeAdvice(cfg *config.Config, missing []string) []string {
	if cfg == nil || cfg.CurrentContext == "" {
		return []string{"re-create your token with: " + strings.Join(missing, ", ")}
	}
	level := cfg.GetEffectiveSafetyLevel()
	var notRequested []string
	for _, s := range missing {
		if !loginRequestsScope(level, s) {
			notRequested = append(notRequested, s)
		}
	}
	if len(notRequested) == 0 {
		return []string{fmt.Sprintf("OAuth login: 'dtctl auth login' requests %s at the %s safety level. A session from an older dtctl does not carry it — run 'dtctl auth login' again to pick it up",
			strings.Join(missing, ", "), level)}
	}
	return []string{fmt.Sprintf("OAuth login: the %s safety level of context %q does not request %s. Log in with a context at a higher safety level to get it",
		level, cfg.CurrentContext, strings.Join(notRequested, ", "))}
}

// unionScopes merges scope lists, dropping duplicates and keeping the result
// sorted so a verdict does not depend on Go's map iteration order.
func unionScopes(lists ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, l := range lists {
		for _, s := range l {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

// requiredScopesFor looks up a command's required scopes from the catalog (the
// single source of truth), and reports what kind of answer that is.
func requiredScopesFor(verb, resource string) ([]string, scopeRequirement) {
	return requiredScopesIn(rootCmd, verb, resource)
}

// requiredScopesIn is requiredScopesFor against the catalog of the tree root
// heads, so a concurrent invocation reads its own tree rather than the
// singleton.
func requiredScopesIn(root *cobra.Command, verb, resource string) ([]string, scopeRequirement) {
	if perCallScopeCommands[verb+" "+resource] {
		return nil, scopeRequirementPerCall
	}
	listing := commands.Build(root)
	v, ok := listing.Verbs[verb]
	if !ok {
		return nil, scopeRequirementNone
	}
	if resource != "" {
		if s, ok := v.RequiredScopesByResource[resource]; ok && len(s) > 0 {
			return s, scopeRequirementKnown
		}
	}
	if len(v.RequiredScopes) > 0 { // DQL verbs (query/verify/wait)
		return v.RequiredScopes, scopeRequirementKnown
	}
	return nil, scopeRequirementNone
}

// resolvePerCallScopes resolves the scopes this specific invocation needs, for a
// command whose requirement is per-call.
//
// It returns false whenever the answer is not certain — an unreadable
// specification, a path that matches no operation, or an operation that declares
// no scope. The verdict then stays "unknown", because a preflight that guesses is
// the one failure mode worse than a preflight that abstains.
func resolvePerCallScopes(c *cobra.Command, args []string) ([]string, bool) {
	// `exec api` is the only per-call command; a second one would add a case here.
	if c.Name() != "api" || c.Parent() == nil || c.Parent().Name() != "exec" || len(args) == 0 {
		return nil, false
	}

	method, _ := c.Flags().GetString("method")
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodGet
	}

	_, client, err := setupClient(cmdContext(c))
	if err != nil {
		return nil, false
	}
	_, op := resapi.NewHandler(client).ResolveForRequest(method, args[0])
	if op == nil || len(op.Scopes) == 0 {
		return nil, false
	}
	return op.Scopes, true
}

// grantedScopesFunc reads the active token's granted scopes. Overridable in tests.
var grantedScopesFunc = grantedScopes

// grantedScopes returns the scopes granted in the active context's token and
// whether they are introspectable. Opaque API/platform tokens are not
// introspectable, so (nil, false) is returned — the caller treats this as
// "unknown" rather than a false negative. The same goes for a scope list read
// back from the access token's claim, which is a subset of the grant: a scope
// absent from it is not proven missing.
func grantedScopes(ictx context.Context) (scopes []string, known bool) {
	cfg, err := loadConfig(ictx)
	if err != nil {
		return nil, false
	}
	ctx, err := cfg.CurrentContextObj()
	if err != nil {
		return nil, false
	}
	status, err := buildSessionStatusFunc(cfg.CurrentContext, ctx, ctx.TokenRef)
	if err != nil || status == nil || !status.IsOAuth || len(status.GrantedScopes) == 0 || status.grantedScopesPartial {
		return nil, false
	}
	return status.GrantedScopes, true
}

// computeScopeVerdict builds the verdict for the explicit --check-scopes path.
// alternatives are the scope sets accepted in place of required (see
// alternativesForInvocation); a token holding any one of them is sufficient.
func computeScopeVerdict(ctx context.Context, verb, resource string, required []string, alternatives [][]string, req scopeRequirement) ScopeCheckResult {
	res := ScopeCheckResult{
		Verb:           verb,
		Resource:       resource,
		RequiredScopes: required,
	}
	switch req {
	case scopeRequirementNone:
		// No platform scopes required (local command); nothing to check.
		res.Status = scopeStatusOK
		res.RequiredScopes = []string{}
		return res
	case scopeRequirementPerCall:
		// Resolution was not attempted or did not succeed. Reporting ok here would
		// assure the caller about a check that never happened — and this is exactly
		// the command where a false assurance is most expensive, since it can reach
		// any endpoint the environment publishes.
		res.Status = scopeStatusUnknown
		res.RequiredScopes = []string{}
		res.Suggestions = []string{
			"this command's required scopes depend on the endpoint it calls, so they are " +
				"not knowable in advance",
			"dtctl describe api <name> --operation '<METHOD> <path>'  -- the scope the " +
				"specification declares for that endpoint",
		}
		return res
	}

	res.AlternativeScopes = alternatives

	granted, known := grantedScopesFunc(ctx)
	if !known {
		res.Status = scopeStatusUnknown
		ensure := "token scopes are not introspectable (API/platform token); ensure it carries: " + strings.Join(required, ", ")
		if len(alternatives) > 0 {
			ensure += " (or any one of: " + formatAlternatives(alternatives) + ")"
		}
		res.Suggestions = []string{ensure}
		return res
	}

	res.GrantedScopes = granted
	missing := subtractScopes(required, granted)
	if len(missing) == 0 {
		res.Status = scopeStatusOK
		return res
	}
	if alt := grantedAlternative(alternatives, granted); alt != nil {
		res.Status = scopeStatusOK
		res.SatisfiedBy = alt
		return res
	}
	res.Status = scopeStatusInsufficient
	res.MissingScopes = missing
	res.Suggestions = insufficientScopeAdvice(missing, alternatives)
	return res
}

// printScopeVerdictAgent writes an ok/unknown verdict wrapped in the agent
// envelope, so `--check-scopes` in agent mode emits the same {ok,result,context}
// shape as every other command. Insufficient verdicts go through the ScopeError
// error-envelope path instead (see scopePreflight).
func printScopeVerdictAgent(ctx context.Context, r ScopeCheckResult) {
	resp := output.Response{
		OK:      true,
		Result:  r,
		Context: &output.ResponseContext{Verb: r.Verb, Resource: r.Resource},
	}
	enc := json.NewEncoder(currentStdout(ctx))
	enc.SetIndent("", "  ")
	_ = enc.Encode(resp)
}

// printScopeVerdict writes the verdict in the active output format (non-agent).
func printScopeVerdict(ctx context.Context, r ScopeCheckResult) {
	switch outputFormat(ctx) {
	case "json":
		enc := json.NewEncoder(currentStdout(ctx))
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
	case "yaml", "yml":
		enc := yaml.NewEncoder(currentStdout(ctx))
		enc.SetIndent(2)
		_ = enc.Encode(r)
		_ = enc.Close()
	default:
		printScopeVerdictHuman(ctx, r)
	}
}

func printScopeVerdictHuman(ctx context.Context, r ScopeCheckResult) {
	target := r.Verb
	if r.Resource != "" {
		target += " " + r.Resource
	}
	fmt.Fprintf(currentStdout(ctx), "Scope check for %q:\n", target)
	if len(r.RequiredScopes) == 0 {
		// An empty requirement means two different things, and the status is what
		// separates them: nothing is needed, or nothing could be determined. Printing
		// "no platform scopes required" for the latter would turn an abstention into
		// a claim.
		if r.Status == scopeStatusUnknown {
			fmt.Fprintln(currentStdout(ctx), "  required: unknown")
			fmt.Fprintln(currentStdout(ctx), "  status:   unknown — dtctl could not determine what this call needs")
			for _, s := range r.Suggestions {
				fmt.Fprintf(currentStdout(ctx), "    - %s\n", s)
			}
			return
		}
		fmt.Fprintln(currentStdout(ctx), "  no platform scopes required")
		return
	}
	fmt.Fprintf(currentStdout(ctx), "  required: %s\n", strings.Join(r.RequiredScopes, ", "))
	if len(r.AlternativeScopes) > 0 {
		fmt.Fprintf(currentStdout(ctx), "  or any of: %s\n", formatAlternatives(r.AlternativeScopes))
	}
	switch r.Status {
	case scopeStatusUnknown:
		fmt.Fprintln(currentStdout(ctx), "  granted:  unknown (token scopes are not introspectable)")
		fmt.Fprintln(currentStdout(ctx), "  status:   unknown — cannot verify; ensure the token carries the required scopes")
	case scopeStatusInsufficient:
		fmt.Fprintf(currentStdout(ctx), "  granted:  %s\n", strings.Join(r.GrantedScopes, ", "))
		fmt.Fprintf(currentStdout(ctx), "  missing:  %s\n", strings.Join(r.MissingScopes, ", "))
		fmt.Fprintf(currentStdout(ctx), "  status:   insufficient — missing %d scope(s)\n", len(r.MissingScopes))
		if len(r.AlternativeScopes) > 0 {
			fmt.Fprintln(currentStdout(ctx), "            and no accepted alternative is granted")
		}
	default:
		if len(r.SatisfiedBy) > 0 {
			fmt.Fprintf(currentStdout(ctx), "  status:   ok — granted accepted alternative: %s\n", strings.Join(r.SatisfiedBy, " + "))
			return
		}
		fmt.Fprintln(currentStdout(ctx), "  status:   ok — all required scopes granted")
	}
}

// subtractScopes returns the sorted elements of required not present in granted.
func subtractScopes(required, granted []string) []string {
	have := make(map[string]bool, len(granted))
	for _, g := range granted {
		have[g] = true
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	sort.Strings(missing)
	return missing
}
