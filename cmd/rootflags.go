package cmd

import "context"

// Per-invocation root flag values (docs/dev/CONCURRENT_EXECUTION.md).
//
// The CLI's singleton tree binds the root persistent flags to gFlags. A
// concurrent invocation's tree (newCommandTree) binds them to its own
// invocation.flags instead, so cobra writes each request's parsed values into
// that request's storage and two invocations parsing at once never share
// memory. The accessors below resolve to whichever storage the invocation ctx
// carries was bound to, from parse time on — PersistentPreRunE and initConfig
// included, not only the command body.
//
// contextName stays a plain global: a fresh tree binds --context to a discard
// variable, because a Session replaces context resolution wholesale.

// rootFlags is the set of root persistent flag values an invocation owns.
type rootFlags struct {
	outputFormat string
	jqFilter     string
	dryRun       bool
	chunkSize    int64
	agentMode    bool
	plainMode    bool
	noAgent      bool
	verbosity    int
	debugMode    bool
	cfgFile      string
	checkScopes  bool
}

// gFlags is where the singleton tree binds the root flags, and what anything
// without a concurrent invocation in its context reads (the plain CLI, the
// serialized engine path, tests).
var gFlags rootFlags

// curFlags resolves the root flag values ctx should read and write: its
// concurrent invocation's own, else gFlags.
func curFlags(ctx context.Context) *rootFlags {
	if inv := concurrentInvocation(ctx); inv != nil {
		return &inv.flags
	}
	return &gFlags
}

// outputFormatChanged reports whether this invocation passed --output. The
// Changed bit lives on the pflag.Flag, so ask the tree being run: a concurrent
// invocation's own, else the singleton.
func outputFormatChanged(ctx context.Context) bool {
	root := rootCmd
	if inv := concurrentInvocation(ctx); inv != nil && inv.treeRoot != nil {
		root = inv.treeRoot
	}
	f := root.PersistentFlags().Lookup("output")
	return f != nil && f.Changed
}

func outputFormat(ctx context.Context) string { return curFlags(ctx).outputFormat }
func agentMode(ctx context.Context) bool      { return curFlags(ctx).agentMode }
func plainMode(ctx context.Context) bool      { return curFlags(ctx).plainMode }
func noAgent(ctx context.Context) bool        { return curFlags(ctx).noAgent }
func verbosity(ctx context.Context) int       { return curFlags(ctx).verbosity }
func debugMode(ctx context.Context) bool      { return curFlags(ctx).debugMode }
func cfgFile(ctx context.Context) string      { return curFlags(ctx).cfgFile }
func checkScopes(ctx context.Context) bool    { return curFlags(ctx).checkScopes }

// setAgentMode and setPlainMode record the auto-detection decisions initConfig
// makes after parsing (an AI-agent environment implies --agent, which implies
// --plain).
func setAgentMode(ctx context.Context, v bool) { curFlags(ctx).agentMode = v }
func setPlainMode(ctx context.Context, v bool) { curFlags(ctx).plainMode = v }
func jqFilter(ctx context.Context) string      { return curFlags(ctx).jqFilter }
func dryRun(ctx context.Context) bool          { return curFlags(ctx).dryRun }
func chunkSize(ctx context.Context) int64      { return curFlags(ctx).chunkSize }

// setOutputFormat records a command's override of the requested format (the
// jq normalization in validateGlobalFlags, DTCTL_OUTPUT, and `exec analyzers`
// forcing json). It writes wherever the accessors read from, so an override
// stays inside this invocation.
func setOutputFormat(ctx context.Context, v string) { curFlags(ctx).outputFormat = v }
