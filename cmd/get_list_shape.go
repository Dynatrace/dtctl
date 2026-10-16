package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// --limit and --fields for every `get` list verb. They are persistent on
// getCmd so each subcommand inherits them; a subcommand that already had its
// own (server-side) --limit keeps it, because cobra lets a local flag shadow
// an inherited one of the same name. Shaping happens in the printer
// (output.ShapingPrinter), after the command fetched its data, so no
// subcommand needs to know about either flag.
//
// These are the singleton's storage. A per-invocation tree binds the same
// flags to its invocation instead (addGetListShapeFlags); read them through
// listShapeLimit and listShapeFields.
var (
	getListLimit  int
	getListFields string
)

// addGetListShapeFlags registers --limit and --fields on the get verb, bound to
// the given storage: the singleton's variables above, or a concurrent
// invocation's own. The storage is passed in because a command being built has
// no context yet to find the invocation through.
func addGetListShapeFlags(c *cobra.Command, limit *int, fields *string) {
	c.PersistentFlags().IntVar(limit, "limit", 0,
		"return at most this many items (0 = all; agent mode defaults to 50); applied after fetching, so --chunk-size still controls paging")
	c.PersistentFlags().StringVar(fields, "fields", "",
		"comma-separated fields to keep, in this order; dotted paths reach nested fields (modificationInfo.lastModifiedTime). table/csv/toon flatten them into columns")

	stability.MarkFlag(c, "limit", stability.Experimental, listShapeSince)
	stability.MarkFlag(c, "fields", stability.Experimental, listShapeSince)
}

// listShapeLimit and listShapeFields are this invocation's get-wide --limit
// and --fields.
func listShapeLimit(ctx context.Context) int {
	if inv := concurrentInvocation(ctx); inv != nil {
		return inv.getListLimit
	}
	return getListLimit
}

func listShapeFields(ctx context.Context) string {
	if inv := concurrentInvocation(ctx); inv != nil {
		return inv.getListFields
	}
	return getListFields
}

// listShapeSince is the release that introduced --limit/--fields on get.
const listShapeSince = "0.40.0"

// agentDefaultPage is how many items a get list returns in agent mode when
// --limit is not given: a full listing routinely overflows an agent's output
// budget, and the envelope says when (and how) more is available. --limit 0
// restores the full list.
const agentDefaultPage = 50

// invokedGetCmd is the get subcommand this invocation is running, set by the
// RunE wrapper installGetListPaging adds. NewPrinter has no command in hand,
// and the default page must apply to get list verbs only.
//
// Serialized invocations use this package-level var (safe under runMu).
// Concurrent invocations store it on their invocation via currentInvokedGetCmd.
var invokedGetCmd *cobra.Command

// currentInvokedGetCmd returns the get subcommand for this invocation:
// the per-invocation field when a concurrent invocation is active, else the
// package-level var (serialized path, safe under runMu).
func currentInvokedGetCmd(ctx context.Context) *cobra.Command {
	if inv := current(ctx); inv != nil && inv.concurrent {
		return inv.invokedGetCmd
	}
	return invokedGetCmd
}

// installGetListPaging wraps every runnable command under cmd so that it
// records itself for the duration of its RunE. Like the scope preflight, it is
// re-applied on each invocation over the pristine tree.
func installGetListPaging(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		installGetListPaging(sub)
	}
	orig := cmd.RunE
	if orig == nil {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if inv := current(cmdContext(c)); inv != nil && inv.concurrent {
			inv.invokedGetCmd = c
			defer func() { inv.invokedGetCmd = nil }()
		} else {
			invokedGetCmd = c
			defer func() { invokedGetCmd = nil }()
		}
		return orig(c, args)
	}
}

// agentPageLimit is the limit a get list verb with its own server-side
// --limit should request: the agent-mode default page unless --limit was
// given explicitly (including --limit 0), in which case limit as passed.
func agentPageLimit(cmd *cobra.Command, limit int64) int64 {
	if agentMode(cmdContext(cmd)) && !cmd.Flags().Changed("limit") {
		return agentDefaultPage
	}
	return limit
}

// shapeListOutput wraps p with the --limit/--fields shaping when either flag
// was given, and returns p untouched otherwise so the default output stays
// byte-identical. format is the effective output format and tabular selects
// the flattened column form (see output.ShapeOptions).
func shapeListOutput(ctx context.Context, p output.Printer, format string, tabular bool) output.Printer {
	fields := output.ParseFields(listShapeFields(ctx))
	limit := listShapeLimit(ctx)
	if agentMode(ctx) && usesDefaultGetLimit(currentInvokedGetCmd(ctx)) {
		limit = agentDefaultPage
	}
	if limit == 0 && len(fields) == 0 {
		return p
	}
	return output.NewShapingPrinter(p, output.ShapeOptions{
		Limit:   limit,
		Fields:  fields,
		Tabular: tabular,
		Format:  format,
		Notices: currentStderr(ctx),
	})
}

// usesDefaultGetLimit reports whether c is a get subcommand whose --limit is
// the get-wide flag and was not given. A subcommand with its own --limit
// pages server-side (agentPageLimit) and must not be cut again here.
func usesDefaultGetLimit(c *cobra.Command) bool {
	if c == nil {
		return false
	}
	f := c.Flags().Lookup("limit")
	verb := getVerbOf(c)
	return f != nil && verb != nil && f == verb.PersistentFlags().Lookup("limit") && !f.Changed
}

// getVerbOf returns the `get` verb c sits under, on whichever tree c belongs
// to — the singleton or a per-invocation one — or nil when c is not a get
// subcommand.
func getVerbOf(c *cobra.Command) *cobra.Command {
	for p := c; p != nil; p = p.Parent() {
		if parent := p.Parent(); parent != nil && !parent.HasParent() && p.Name() == "get" {
			return p
		}
	}
	return nil
}
