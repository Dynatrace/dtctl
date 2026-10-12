package cmd

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
)

// A repo scope entry binds the labels a Live Debugger workspace is filtered
// by, so the breakpoint commands can take workspace filters from it. Named
// with --repo-scope, an entry is a filter source like --filters, on create
// and update, and every filter set it renders is sent. Unnamed, create
// applies the entry covering the working directory, but only to a workspace
// with no filters yet, where nothing is re-scoped, and only its one
// namespace-and-workload set: entity ids are deprecated fields and change
// with redeploys, and a service name is unverified as a label, so those sets
// are sent only when asked for. Service ids are never sent: a workspace does
// not honour them (see reposcope.LiveDebuggerFilters).
const (
	createBreakpointRepoScopeUsage = `set the workspace filters from this entry of the repository's .dtctl-repo-scope.yaml
default: the workload filter of the entry covering the working directory, when the workspace has no filters`
	createBreakpointNoRepoScopeUsage = "leave the workspace filters to --filters, ignoring the repository's .dtctl-repo-scope.yaml (env: " + noRepoScopeEnv + "=1)"
	updateBreakpointRepoScopeUsage   = "set the workspace filters from this entry of the repository's .dtctl-repo-scope.yaml"
)

// namedBreakpointFilters is every filter set of the entry --repo-scope names.
// Every failure is an error, as on query: the caller asked for that entry's
// filters. resolveRepoScope resolves a name to its entry or fails. The
// entry's service ids are not among the sets, so a line says they are left
// out rather than let "every filter set" read as every binding.
func namedBreakpointFilters(cmd *cobra.Command, cfg *config.Config, name string) ([]map[string][]string, error) {
	res, err := resolveRepoScope(cmd, cfg, name)
	if err != nil {
		return nil, err
	}
	stderr := currentStderr(cmdContext(cmd))
	warnRepoScopeFile(stderr, res.Warning)
	sets, ok, reason := res.Status.LiveDebuggerFilterSets()
	if !ok {
		return nil, &reposcope.InvalidError{Source: "--" + repoScopeFlag, Msg: reason}
	}
	if len(res.Status.Entry.Services) > 0 {
		output.FprintInfo(stderr, "repo scope %s's service ids are not sent: Live Debugger does not honour dt.entity.service as a workspace label", name)
	}
	return sets, nil
}

// coveringFilters is what the entry covering the working directory offers a
// create that names no filters.
type coveringFilters struct {
	// name is the entry; it is empty when none applies, and skipped then
	// says why a scope file that is there supplied none. Both are empty
	// when scoping is off.
	name    string
	skipped string
	// sets are every filter set the entry renders, and set the one a
	// workspace without filters takes from it; nil when it has no single
	// namespace-and-workload set.
	sets []map[string][]string
	set  map[string][]string
}

// defaultBreakpointFilters is the entry covering the working directory,
// under resolveRepoScope's off rules.
func defaultBreakpointFilters(cmd *cobra.Command, cfg *config.Config) (coveringFilters, error) {
	res, err := resolveRepoScope(cmd, cfg, "")
	switch {
	case err != nil || res == nil:
		return coveringFilters{}, err
	case res.Scope != nil:
		return coveringFilters{skipped: res.Scope.Reason}, nil
	}
	warnRepoScopeFile(currentStderr(cmdContext(cmd)), res.Warning)
	sets, ok, reason := res.Status.LiveDebuggerFilterSets()
	if !ok {
		return coveringFilters{skipped: reason}, nil
	}
	cov := coveringFilters{name: res.Status.Entry.Name, sets: sets}
	cov.set, _ = reposcope.WorkloadFilterSet(sets)
	return cov, nil
}

// breakpointWorkspace is the workspace a create puts its breakpoint in.
type breakpointWorkspace struct {
	handler *livedebugger.Handler
	id      string
	resp    map[string]interface{} // as getOrCreateWorkspaceV2 returned it
}

// appliedDefault is what the repo scope default did to the workspace: scope
// names the entry applied and summary the filters it set, both empty when
// the workspace kept its own; cancelled is a declined confirmation.
type appliedDefault struct {
	scope, summary string
	cancelled      bool
}

// applyDefaultBreakpointFilters is create breakpoint without --filters or
// --repo-scope. A workspace that has filters keeps them, with a warning when
// the covering entry would set different ones. A workspace without filters
// gets the covering entry's namespace-and-workload set, when it renders
// exactly one; without one, the breakpoint cannot be created.
//
// A workspace with no filters cannot hold breakpoints, so the change
// normally re-scopes nothing and asks nothing. It is the one change nobody
// typed, though, so the active breakpoints are counted even in a
// non-interactive run, and if there are any the change is confirmed or,
// where nobody can be asked, refused.
func applyDefaultBreakpointFilters(cmd *cobra.Command, cfg *config.Config, ws breakpointWorkspace, location string, skipConfirm bool) (appliedDefault, error) {
	ctx := cmdContext(cmd)
	stderr := currentStderr(ctx)
	hasFilters, err := livedebugger.WorkspaceHasFilters(ws.resp)
	if err != nil {
		return appliedDefault{}, err
	}
	current, err := livedebugger.WorkspaceFilterSets(ws.resp)
	if err != nil {
		return appliedDefault{}, err
	}
	cov, err := defaultBreakpointFilters(cmd, cfg)
	if err != nil {
		return appliedDefault{}, err
	}

	switch cov.outcome(hasFilters, current) {
	case defaultKeeps:
		return appliedDefault{}, nil
	case defaultWarns:
		output.FprintWarning(stderr, "the workspace already has filters (%s); repo scope %s would set %s; pass --%s %s to apply it",
			workspaceFilterSummary(current), cov.name, filterSetsSummary(cov.sets), repoScopeFlag, cov.name)
		return appliedDefault{}, nil
	case defaultNoEntry, defaultNoSet:
		return appliedDefault{}, errors.New(cov.noFilters(location))
	}

	cancelled, err := applyWorkspaceFilters(ctx, ws.handler, ws.id, []map[string][]string{cov.set},
		workspaceFilterChange{creating: true, skipConfirm: skipConfirm, unasked: true})
	switch {
	case errors.Is(err, errFiltersDeclined):
		return appliedDefault{}, fmt.Errorf("the workspace has active breakpoints that setting its filters from repo scope %s would re-scope; pass --yes to apply it anyway, or set filters with --filters", cov.name)
	case err != nil:
		return appliedDefault{}, err
	case cancelled:
		return appliedDefault{cancelled: true}, nil
	}
	applied := appliedDefault{scope: cov.name, summary: formatFilters(cov.set)}
	output.FprintInfo(stderr, "set the workspace filters from repo scope %s: %s (--filters sets them by hand, --%s turns this default off)",
		cov.name, applied.summary, noRepoScopeFlag)
	if len(cov.sets) > 1 {
		output.FprintInfo(stderr, "repo scope %s's other filter sets (%s) are set only with --%s %s",
			cov.name, filterSetsSummary(withoutSet(cov.sets, cov.set)), repoScopeFlag, cov.name)
	}
	return applied, nil
}

// defaultOutcome is what the repo scope default does to a workspace on a
// create that names no filters.
type defaultOutcome int

const (
	defaultKeeps   defaultOutcome = iota // the workspace keeps its filters
	defaultWarns                         // it keeps them, and the entry would set others
	defaultSets                          // it has none and takes the entry's workload set
	defaultNoEntry                       // it has none, and no entry offers any: the create fails
	defaultNoSet                         // it has none, and the entry no single workload set: the create fails
)

// outcome decides the default for a workspace that has filters or not
// (hasFilters) holding current. applyDefaultBreakpointFilters acts on it and
// a dry run words it, so the two cannot disagree.
func (cov coveringFilters) outcome(hasFilters bool, current []livedebugger.FilterSet) defaultOutcome {
	switch {
	case hasFilters && cov.differsFrom(current):
		return defaultWarns
	case hasFilters:
		return defaultKeeps
	case cov.name == "":
		return defaultNoEntry
	case cov.set == nil:
		return defaultNoSet
	}
	return defaultSets
}

// noFilters is why a create at location that names no filters fails on a
// workspace without them.
func (cov coveringFilters) noFilters(location string) string {
	switch {
	case cov.skipped != "":
		return fmt.Sprintf("no workspace filters configured: %s, or pass --filters key:value", cov.skipped)
	case cov.name != "":
		return fmt.Sprintf("no workspace filters configured; repo scope %s has no single workload filter set (it renders %s): pass --%s %s for all of them, or --filters key:value",
			cov.name, filterSetsSummary(cov.sets), repoScopeFlag, cov.name)
	}
	return fmt.Sprintf("no workspace filters configured; set filters first with:\n  dtctl update breakpoint --filters key:value\nor pass --filters when creating:\n  dtctl create breakpoint %s --filters key:value", location)
}

// differsFrom reports whether a workspace holding current would be warned
// about: the entry renders sets, and current is neither all of them nor its
// workload set.
func (cov coveringFilters) differsFrom(current []livedebugger.FilterSet) bool {
	return len(cov.sets) > 0 && !workspaceHolds(current, cov.sets) &&
		(cov.set == nil || !workspaceHolds(current, []map[string][]string{cov.set}))
}

// withoutSet is sets less the one set.
func withoutSet(sets []map[string][]string, set map[string][]string) []map[string][]string {
	return slices.DeleteFunc(slices.Clone(sets), func(s map[string][]string) bool {
		return maps.EqualFunc(s, set, slices.Equal)
	})
}

// filterSetsSummary renders sets the way --filters spells one, joined with
// "; ".
func filterSetsSummary(sets []map[string][]string) string {
	parts := make([]string, len(sets))
	for i, set := range sets {
		parts[i] = formatFilters(set)
	}
	return strings.Join(parts, "; ")
}

// workspaceHolds reports whether the workspace's filters are exactly want:
// one filter set per map, in any order, each carrying that map's labels and
// nothing else.
func workspaceHolds(sets []livedebugger.FilterSet, want []map[string][]string) bool {
	if len(sets) != len(want) {
		return false
	}
	left := slices.Clone(want)
	for _, set := range sets {
		if len(fieldValues(set.Filters)) > 0 {
			return false
		}
		labels := fieldValues(set.Labels)
		i := slices.IndexFunc(left, func(w map[string][]string) bool { return maps.EqualFunc(labels, w, slices.Equal) })
		if i < 0 {
			return false
		}
		left = slices.Delete(left, i, i+1)
	}
	return true
}

// workspaceFilterSummary renders the workspace's filter sets the way
// filterSetsSummary renders the sets dtctl sends.
func workspaceFilterSummary(sets []livedebugger.FilterSet) string {
	parts := make([]string, 0, len(sets))
	for _, set := range sets {
		fields := fieldValues(set.Labels)
		maps.Copy(fields, fieldValues(set.Filters))
		if len(fields) > 0 {
			parts = append(parts, formatFilters(fields))
		}
	}
	return strings.Join(parts, "; ")
}

// fieldValues collects a filter set's fields into the map --filters parses
// to, values sorted and de-duplicated as the sets dtctl renders are, leaving
// out a field without values.
func fieldValues(fields []livedebugger.FilterField) map[string][]string {
	out := map[string][]string{}
	for _, f := range fields {
		if len(f.Values) > 0 {
			out[f.Field] = append(out[f.Field], f.Values...)
		}
	}
	for field := range out {
		slices.Sort(out[field])
		out[field] = slices.Compact(out[field])
	}
	return out
}
