package cmd

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
	"github.com/Dynatrace/dtctl/pkg/resources/livedebugger"
)

// A repo scope entry binds the labels a Live Debugger workspace is filtered
// by, so the breakpoint commands can take workspace filters from it. Named
// with --repo-scope, an entry is a filter source like --filters, on create
// and update, and every filter set it renders is sent. Service ids are never
// sent: a workspace does not honour them (see reposcope.LiveDebuggerFilters).
//
// Nothing is set from an entry nobody named. A workspace's filters decide
// where every breakpoint in it can hit, and whether a workspace honours the
// labels an entry renders is not verified against an environment, so a create
// that names no filters only says that the entry covering the working
// directory exists and how to apply it (coveringEntry).
const (
	createBreakpointRepoScopeUsage    = "set the workspace filters from this entry of the repository's .dtctl-repo-scope.yaml, in the workspace named after it"
	updateBreakpointRepoScopeUsage    = "set the workspace filters from this entry of the repository's .dtctl-repo-scope.yaml; with a breakpoint identifier, only name its workspace"
	breakpointWorkspaceRepoScopeUsage = "name the workspace after this entry of the repository's .dtctl-repo-scope.yaml, wherever the command runs"
)

// namedBreakpointFilters is every filter set of the entry --repo-scope names,
// with what the caller says before it sets them (entryFilterNotes). Every
// failure is an error, as on query: the caller asked for that entry's
// filters. resolveRepoScope resolves a name to its entry or fails.
func namedBreakpointFilters(cmd *cobra.Command, cfg *config.Config, name string) (sets []map[string][]string, notes []string, err error) {
	res, err := resolveRepoScope(cmd, cfg, name)
	if err != nil {
		return nil, nil, err
	}
	sets, ok, reason := res.Status.LiveDebuggerFilterSets()
	if !ok {
		return nil, nil, &reposcope.InvalidError{Source: "--" + repoScopeFlag, Msg: reason}
	}
	return sets, entryFilterNotes(res.Status.Entry), nil
}

// entryFilterNotes says how the filters set from e differ from e, one line
// per difference. The filters are not the entry: they leave out its service
// ids, its clusters and its filter segments, so they can be wider than the
// queries the entry narrows, and they carry service.name, which nothing has
// shown a workspace to honour. Every path that sets, or previews setting,
// filters from an entry says these before it asks or writes, and an agent
// gets them in context.warnings (sayBreakpointWarnings).
func entryFilterNotes(e *reposcope.Entry) []string {
	var notes []string
	if len(e.Services) > 0 {
		notes = append(notes, fmt.Sprintf("repo scope %s's service ids are not sent: Live Debugger does not honour dt.entity.service as a workspace label", e.Name))
	}
	if e.BindsCluster() {
		notes = append(notes, fmt.Sprintf("repo scope %s binds a cluster, which a Live Debugger workspace is not narrowed by: the filters include the same workload in other clusters", e.Name))
	}
	if len(e.Segments) > 0 {
		notes = append(notes, fmt.Sprintf("repo scope %s's filter segments are not applied: a workspace is filtered by labels and takes no segment, so the filters are wider than the entry's queries", e.Name))
	}
	if len(e.ServiceNames) > 0 {
		notes = append(notes, fmt.Sprintf("repo scope %s sets service.name, which is not verified as a workspace label: a filter set a workspace does not honour selects nothing, and a breakpoint under it is created and never hits; check with dtctl describe breakpoint <id>", e.Name))
	}
	return notes
}

// coveringEntry is what the repository's scope file says to a create that
// names no filters: the entry covering the working directory and the filter
// sets --repo-scope would set from it. Nothing here is applied.
type coveringEntry struct {
	// name is the entry, and sets every filter set it renders. name is
	// empty when no entry offers filters, and skipped then says why a
	// scope file with entries for this environment offered none. Both are
	// empty when there is nothing to say.
	name    string
	sets    []map[string][]string
	skipped string
}

// coveringBreakpointEntry reads the entry covering the working directory
// from the resolution that named the workspace. It says nothing where the
// user turned repo scopes off (DTCTL_NO_REPO_SCOPE) or the stability floor
// hides --repo-scope, the flag every line it produces names.
func coveringBreakpointEntry(cmd *cobra.Command, workspace breakpointWorkspace) coveringEntry {
	ctx := cmdContext(cmd)
	status := workspace.scope
	if status == nil || truthyEnv(ctx, noRepoScopeEnv) || !repoScopeAdmitted(cmd, repoScopeFlag) {
		return coveringEntry{}
	}
	if status.Entry == nil {
		return coveringEntry{skipped: repoScopeReason(status)}
	}
	sets, ok, reason := status.LiveDebuggerFilterSets()
	if !ok {
		return coveringEntry{skipped: reason}
	}
	return coveringEntry{name: status.Entry.Name, sets: sets}
}

// noFilters is the error of a create at location that names no filters, in
// a workspace without them: a breakpoint cannot be created there, and the
// filters are the user's to choose.
func (cov coveringEntry) noFilters(workspace, location string) error {
	switch {
	case cov.name != "":
		return &invalidInputError{fmt.Errorf("workspace %s has no filters, and a breakpoint needs them: repo scope %s covers this directory, so pass --%s %s to set its filters (%s), or --filters key:value",
			workspace, cov.name, repoScopeFlag, cov.name, filterSetsSummary(cov.sets))}
	case cov.skipped != "":
		return &invalidInputError{fmt.Errorf("workspace %s has no filters, and a breakpoint needs them: %s, or pass --filters key:value", workspace, cov.skipped)}
	}
	return &invalidInputError{fmt.Errorf("no workspace filters configured; set filters first with:\n  dtctl update breakpoint --filters key:value\nor pass --filters when creating:\n  dtctl create breakpoint %s --filters key:value", location)}
}

// hint is the one line a create prints after it made a breakpoint in a
// workspace whose filters are not the covering entry's: the entry exists,
// and this command applies it. It is empty when there is no such entry or
// the workspace already holds its filters. The command changes the
// workspace's filters, so it carries selectors (breakpointSelectors).
func (cov coveringEntry) hint(workspace string, current []livedebugger.FilterSet, selectors []string) string {
	if cov.name == "" || workspaceHolds(current, cov.sets) {
		return ""
	}
	return fmt.Sprintf("workspace %s's filters (%s) are not the ones repo scope %s, which covers this directory, renders (%s); to apply them: %s",
		workspace, workspaceFilterSummary(current), cov.name, filterSetsSummary(cov.sets),
		breakpointCommandLine(selectors, "update", "breakpoint", "--"+repoScopeFlag, cov.name))
}

// preview is what a dry run of such a create says after naming the
// workspace, whose filters it has not read.
func (cov coveringEntry) preview() string {
	switch {
	case cov.name != "":
		return fmt.Sprintf(", which must have filters; repo scope %s covers this directory, and --%s %s sets its filters (%s)",
			cov.name, repoScopeFlag, cov.name, filterSetsSummary(cov.sets))
	case cov.skipped != "":
		return fmt.Sprintf(", which must have filters; %s, or pass --filters key:value", cov.skipped)
	}
	return ", which must have filters; --filters key:value sets them"
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
// nothing else. A set without a value selects by nothing and is skipped, as
// workspaceFilterSummary skips it, so the two never disagree about what the
// workspace holds.
func workspaceHolds(sets []livedebugger.FilterSet, want []map[string][]string) bool {
	left := slices.Clone(want)
	for _, set := range sets {
		labels, filters := fieldValues(set.Labels), fieldValues(set.Filters)
		if len(labels) == 0 && len(filters) == 0 {
			continue
		}
		i := slices.IndexFunc(left, func(w map[string][]string) bool { return maps.EqualFunc(labels, w, slices.Equal) })
		if len(filters) > 0 || i < 0 {
			return false
		}
		left = slices.Delete(left, i, i+1)
	}
	return len(left) == 0
}

// workspaceFilterSummary renders the workspace's filter sets the way
// filterSetsSummary renders the sets dtctl sends, for a message: the values
// are whatever the environment returned, so they are shown as repoScopeText
// shows them.
func workspaceFilterSummary(sets []livedebugger.FilterSet) string {
	parts := make([]string, 0, len(sets))
	for _, set := range sets {
		fields := fieldValues(set.Labels)
		maps.Copy(fields, fieldValues(set.Filters))
		if len(fields) > 0 {
			parts = append(parts, formatFilters(fields))
		}
	}
	return repoScopeText(strings.Join(parts, "; "))
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
