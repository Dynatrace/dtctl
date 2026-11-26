package cmd

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/resources/livedebugger"
)

// workspaceFiltersChange is the filter change a create breakpoint made
// before it created the breakpoint: the two are separate requests, so the
// second can fail with the first already applied.
type workspaceFiltersChange struct {
	workspace string
	// before are the filter sets the workspace held, and after the summary
	// of the ones this run set.
	before []livedebugger.FilterSet
	after  string
	// scopeName is the entry --repo-scope named, "" for --filters.
	scopeName string
	// location is where the breakpoint was to be created.
	location string
}

// thenFailed is the error of a create that failed after the change.
func (c workspaceFiltersChange) thenFailed(err error) error {
	return &filtersChangedError{change: c, err: err}
}

// filtersChangedError is a create breakpoint that set the workspace's
// filters and then failed. The filters are not rolled back, so the error
// says what they were, what they are, and how to see and restore them.
type filtersChangedError struct {
	change workspaceFiltersChange
	err    error
}

// Error is the headline and, indented under it, what to do: a person reads
// nothing but the error.
func (e *filtersChangedError) Error() string {
	return e.Headline() + "\n  " + strings.Join(e.Suggestions(), "\n  ")
}

func (e *filtersChangedError) Unwrap() error { return e.err }

// Headline is what was changed, in which workspace, and why the create
// failed. Agent mode reports it as the message, with Suggestions beside it.
func (e *filtersChangedError) Headline() string {
	c := e.change
	after := c.after
	if c.scopeName != "" {
		after += " (repo scope " + c.scopeName + ")"
	}
	if before := workspaceFilterSummary(c.before); before != "" {
		return fmt.Sprintf("workspace %s's filters were changed from %s to %s, then creating the breakpoint at %s failed: %v", c.workspace, before, after, c.location, e.err)
	}
	return fmt.Sprintf("workspace %s's filters were set to %s (it had none), then creating the breakpoint at %s failed: %v", c.workspace, after, c.location, e.err)
}

// Suggestions are the commands that show the workspace, restore its
// previous filters where one command can, and try the create again.
func (e *filtersChangedError) Suggestions() []string {
	c := e.change
	named := ""
	if c.scopeName != "" {
		named = " --" + repoScopeFlag + " " + c.scopeName
	}
	return []string{
		fmt.Sprintf("the filters were not rolled back: every breakpoint in workspace %s is scoped by them now", c.workspace),
		"to see the workspace and its filters: dtctl get breakpoints -v" + named,
		c.restore(),
		fmt.Sprintf("to try again once the cause is fixed: dtctl create breakpoint %s%s", c.location, named),
	}
}

// restore says how to put the previous filters back, or why no one command
// does. --filters sets one set of labels in the workspace of the working
// directory, so it restores only that, and only where the change was made
// there.
func (c workspaceFiltersChange) restore() string {
	switch set, pasteable := pasteableFilterSet(c.before); {
	case workspaceFilterSummary(c.before) == "":
		return "the workspace had no filters before, and no dtctl command clears them"
	case c.scopeName != "":
		return "the previous filters are in this message; no single command restores them in a workspace --" + repoScopeFlag + " named"
	case pasteable:
		return "to restore the previous filters: dtctl update breakpoint --filters " + set
	case len(c.before) > 1:
		return "the previous filters were several sets, which --filters cannot spell: set them again from the repo scope entry that renders them, with dtctl update breakpoint --" + repoScopeFlag + " <name>"
	}
	return "the previous filters are in this message; --filters cannot spell them"
}

// pasteableFilterSet is the --filters value that sets exactly sets: there
// is one when they are a single set of labels that parses back to itself.
func pasteableFilterSet(sets []livedebugger.FilterSet) (string, bool) {
	if len(sets) != 1 || len(fieldValues(sets[0].Filters)) > 0 {
		return "", false
	}
	labels := fieldValues(sets[0].Labels)
	spelled := formatFilters(labels)
	parsed, err := parseFilters(spelled)
	if err != nil || !maps.EqualFunc(parsed, labels, slices.Equal) {
		return "", false
	}
	return spelled, true
}
