package cmd

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/dql"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// Repo scoping is on by default inside a linked repository, so it has an
// opt-out per command and one for a whole shell or CI job: the scope file is
// committed, and a job running queries from a checkout opts out once, not on
// every command line.
const (
	repoScopeFlag   = "repo-scope"
	noRepoScopeFlag = "no-repo-scope"
	noRepoScopeEnv  = "DTCTL_NO_REPO_SCOPE"
)

// Codes for a scope that resolved but was not applied for a reason the
// rewrite cannot know; the rewrite's own codes are dql.Outcome.
const (
	repoScopeNoEntry     = "no_entry"
	repoScopeInvalidFile = "invalid_file"
	repoScopeNoBinding   = "no_binding"
)

// repoScopeCodes is every context.repo_scope.code; docs/AGENT_MODE.md lists
// each one but applied.
var repoScopeCodes = []string{
	string(dql.Applied), string(dql.NotFetch), string(dql.UnsupportedObject), repoScopeNoBinding,
	string(dql.UserFilter), string(dql.Subquery), string(dql.Unscannable), repoScopeNoEntry, repoScopeInvalidFile,
}

// repoScopeRefusals are the codes for an entry that resolved but cannot narrow
// the query. Under an explicit --repo-scope each is an error: the caller asked
// for scoped rows, and environment-wide ones would answer a different
// question. user_filter is not among them, because the user's own filter on a
// bound field is what they asked for.
var repoScopeRefusals = []string{
	string(dql.NotFetch), string(dql.UnsupportedObject), repoScopeNoBinding, string(dql.Subquery), string(dql.Unscannable),
}

// The usage of the two flags on the commands that send DQL a user wrote.
const (
	queryRepoScopeUsage = `narrow fetch logs/spans to this entry of the repository's .dtctl-repo-scope.yaml
default: the entry covering the working directory`
	queryNoRepoScopeUsage = "run the query exactly as typed, ignoring the repository's .dtctl-repo-scope.yaml (env: " + noRepoScopeEnv + "=1)"
)

// addRepoScopeFlags registers the entry selector and, unless optOutUsage is
// empty, the opt-out. A command that applies a scope only when asked has no
// default to opt out of, so it registers --repo-scope alone.
func addRepoScopeFlags(c *cobra.Command, scopeUsage, optOutUsage string) {
	c.Flags().String(repoScopeFlag, "", scopeUsage)
	rejectEmptyFlag(c, repoScopeFlag)
	stability.MarkFlag(c, repoScopeFlag, stability.Experimental, repoScopeSince)
	if optOutUsage == "" {
		return
	}
	c.Flags().Bool(noRepoScopeFlag, false, optOutUsage)
	c.MarkFlagsMutuallyExclusive(repoScopeFlag, noRepoScopeFlag)
	stability.MarkFlag(c, noRepoScopeFlag, stability.Experimental, repoScopeSince)
}

// repoScopeAdmitted reports whether the stability policy in force lets cmd
// apply a scope nobody asked it to.
//
// Scoping by default changes what a stable command does, and only the repo
// scope flags carry the experimental mark. A floor that hides them would
// otherwise leave the behaviour running while refusing its opt-out, so the
// default is on only where every flag named is registered and usable: an
// exception for each admits it again. An explicit --repo-scope needs no such
// check; the floor already refused it if it was hidden.
func repoScopeAdmitted(cmd *cobra.Command, names ...string) bool {
	policy := currentStabilityPolicy(cmdContext(cmd))
	path := commandPathRelative(cmd, cmd.Root())
	for _, name := range names {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			return false
		}
		if _, ok := flagAdmitted(cmd, path, f, policy); !ok {
			return false
		}
	}
	return true
}

// repoScopeResolution is the repo scope a command runs under, before anything
// consumes it: the decision shared by every command that applies a scope.
type repoScopeResolution struct {
	// Status is the resolved scope; its Entry is nil when no entry covers the
	// working directory. Status is nil when the file failed to load.
	Status *reposcope.Status
	// Scope is what the envelope reports for a scope that resolved without an
	// entry to apply (invalid_file, no_entry). It is nil when an entry
	// resolved: what applying it does is the consumer's to say.
	Scope *output.RepoScope
	// Warning names keys the file holds that this dtctl does not know.
	Warning string
}

// resolveRepoScope decides which repo scope cmd runs under: the entry name
// asks for, or with no name the one covering the working directory. The
// checks run cheapest first; the disk is touched only from the .git walk on:
//
//	--no-repo-scope, or DTCTL_NO_REPO_SCOPE without a name → off
//	no name, and the stability floor hides either flag     → off
//	context has no environment URL                         → off (--repo-scope: the error)
//	HostWorkingDirectory not granted, a session            → off (--repo-scope: CapabilityError)
//	no .git at or above the working directory              → off (--repo-scope: InvalidError)
//	no scope file, or no entry for this host               → off (--repo-scope: NotFoundError)
//	the file fails to load                                 → warning, invalid_file (--repo-scope: the error)
//	no entry covers the directory                          → warning, no_entry
//	an entry                                               → the consumer applies it
//
// "Off" is a nil resolution and a nil error, and the consumer says nothing,
// so outside a linked repository every command behaves as if repo scopes did
// not exist. A file that cannot be used resolves rather than fails, because it
// is committed and one bad edit would otherwise break every command run in
// the repository. A name asks for one entry, so there what does not resolve
// is the error.
func resolveRepoScope(cmd *cobra.Command, cfg *config.Config, name string) (*repoScopeResolution, error) {
	ctx := cmdContext(cmd)
	if off, _ := cmd.Flags().GetBool(noRepoScopeFlag); off || (name == "" && truthyEnv(ctx, noRepoScopeEnv)) {
		return nil, nil
	}
	if name == "" && !repoScopeAdmitted(cmd, repoScopeFlag, noRepoScopeFlag) {
		return nil, nil
	}

	scope, err := openRepoScope(ctx, cfg)
	var status *reposcope.Status
	// Switching to a context the file does not mention must not start a
	// warning on every command.
	if err == nil && (name != "" || len(scope.file.Entries(scope.host)) > 0) {
		status, err = scope.resolve(name)
	}
	switch {
	case err != nil && name != "":
		return nil, err
	case err != nil && scope != nil:
		return &repoScopeResolution{Scope: &output.RepoScope{
			File: reposcope.FileName, Environment: scope.host, Code: repoScopeInvalidFile, Reason: err.Error(),
		}}, nil
	case status == nil:
		return nil, nil
	}
	res := &repoScopeResolution{Status: status, Warning: status.Warning}
	if status.Entry == nil {
		res.Scope = &output.RepoScope{File: status.File, Environment: status.Environment, Code: repoScopeNoEntry, Reason: status.Reason}
	}
	return res, nil
}

// repoScopedQuery is one invocation's query after repo scoping: the text to
// send, and what the notices, the envelope and a failure say about it.
type repoScopedQuery struct {
	// Query is the text to send. It is the input, byte for byte, unless a
	// scope was applied.
	Query string
	// typed is the input as the user wrote it.
	typed string
	// scope is nil when no scope resolved, and then nothing is printed and
	// the envelope gains no key.
	scope *output.RepoScope
	// warning names keys the file holds that this dtctl does not know.
	warning string
	stderr  io.Writer
	// zeroRows is set by onResult and read by decorate; the executor calls
	// OnResult first.
	zeroRows bool
}

// applyRepoScope narrows query to the repo scope resolveRepoScope picks for
// the working directory, if any; dql.InsertFilter decides what an entry does
// to it.
//
// Anything but "off" is reported on stderr in every mode and at every
// verbosity: a scoped run must say so whatever reads stdout, and a scope that
// resolved but was not applied warns, and the query runs unscoped.
//
// A name asks for one entry, so a scope that resolves but cannot narrow this
// query is an error rather than an unscoped run; only the user's own filter
// on a bound field still runs, because it wins over the scope by design.
func applyRepoScope(cmd *cobra.Command, cfg *config.Config, query string) (*repoScopedQuery, error) {
	q := &repoScopedQuery{Query: query, typed: query, stderr: currentStderr(cmdContext(cmd))}
	name, _ := cmd.Flags().GetString(repoScopeFlag)
	res, err := resolveRepoScope(cmd, cfg, name)
	if err != nil || res == nil {
		return q, err
	}
	q.scope = res.Scope
	if q.scope == nil {
		q.rewrite(res.Status)
		if name != "" && slices.Contains(repoScopeRefusals, q.scope.Code) {
			return nil, &reposcope.InvalidError{
				Source:      "--" + repoScopeFlag,
				Msg:         fmt.Sprintf("entry %q cannot narrow this query (%s): %s", name, q.scope.Code, q.scope.Reason),
				Suggestions: []string{"drop --" + repoScopeFlag + " to run the query as typed"},
			}
		}
	}
	if res.Warning != "" {
		q.warning = res.Warning
		q.scope.Reason = strings.TrimPrefix(q.scope.Reason+"; "+res.Warning, "; ")
	}
	q.announce()
	return q, nil
}

// rewrite applies the resolved entry to the query and records the outcome.
func (q *repoScopedQuery) rewrite(res *reposcope.Status) {
	rw := dql.InsertFilter(q.Query, reposcope.Filters(res.Entry))
	q.Query = rw.Query
	q.scope = &output.RepoScope{
		Name:        res.Entry.Name,
		File:        res.File,
		Environment: res.Environment,
		Applied:     rw.Outcome == dql.Applied,
		DataObject:  rw.DataObject,
		Filter:      rw.Filter,
		Code:        string(rw.Outcome),
		Reason:      rw.Reason,
	}
	// The rewrite only knows the filters it was given. For an object repo
	// scopes do filter, the entry is what is missing, and Render says which
	// binding to add.
	if rw.Outcome == dql.UnsupportedObject && slices.Contains(reposcope.DataObjects(), rw.DataObject) {
		_, _, reason := reposcope.Render(res.Entry, rw.DataObject)
		q.scope.Code, q.scope.Reason = repoScopeNoBinding, reason
	}
}

// announce writes the line that says what the scope did, and one for keys
// the file holds that this dtctl does not know.
func (q *repoScopedQuery) announce() {
	if q.warning != "" {
		output.FprintWarning(q.stderr, "%s: %s", reposcope.FileName, q.warning)
	}
	s := q.scope
	if s.Applied {
		output.FprintInfo(q.stderr, "applying repo scope %s to fetch %s: %s (use --%s to ignore it)",
			s.Name, s.DataObject, s.Filter, noRepoScopeFlag)
		return
	}
	label := "repo scope"
	if s.Name != "" {
		label += " " + s.Name
	}
	output.FprintWarning(q.stderr, "%s not applied: %s; the query runs unscoped (use --%s to silence this)",
		label, s.Reason, noRepoScopeFlag)
}

// onResult is the executor's OnResult hook. A scoped query that returns
// nothing reads exactly like a quiet service, and the likelier cause is a
// binding the records do not carry, so it says how to tell the two apart.
func (q *repoScopedQuery) onResult(n int, _ *exec.DQLQueryResponse) {
	if q.scope == nil || !q.scope.Applied || n > 0 {
		return
	}
	q.zeroRows = true
	output.FprintInfo(q.stderr, "0 records under repo scope %s; compare with --%s", q.scope.Name, noRepoScopeFlag)
}

// decorate puts the scope on the agent envelope, with the opt-out as a
// suggestion. On zero rows the comparison goes first: it is the one thing to
// try before widening the window.
func (q *repoScopedQuery) decorate(c *output.ResponseContext) {
	if q.scope == nil {
		return
	}
	c.RepoScope = q.scope
	switch {
	case q.zeroRows:
		c.Suggestions = append([]string{fmt.Sprintf(
			"0 records under repo scope %s: run the same query with --%s to tell whether the scope or the query filtered everything out",
			q.scope.Name, noRepoScopeFlag)}, c.Suggestions...)
	case q.scope.Applied:
		c.Suggestions = append(c.Suggestions, "run unscoped: add --"+noRepoScopeFlag)
	}
}

// wrapError marks a failure of a query the scope rewrote, so its report shows
// the text that was actually sent.
func (q *repoScopedQuery) wrapError(err error) error {
	if err == nil || q.scope == nil || !q.scope.Applied {
		return err
	}
	return &ScopedQueryError{Err: err, Scope: q.scope, Sent: q.Query}
}

// ScopedQueryError is a query that failed after repo scoping rewrote it. Grail
// saw Sent, not what the user typed, so a position in the error points into
// Sent, and the inserted filter may itself be the cause. Unwrap returns the
// original error: errorToDetail reports its code and position hints, then adds
// Suggestions.
type ScopedQueryError struct {
	Err   error
	Scope *output.RepoScope
	Sent  string
}

// Error is the original message. The human output already carried the
// "applying repo scope" notice before the query ran.
func (e *ScopedQueryError) Error() string { return e.Err.Error() }

func (e *ScopedQueryError) Unwrap() error { return e.Err }

// Suggestions shows what was sent and how to rule the scope out.
func (e *ScopedQueryError) Suggestions() []string {
	return []string{
		fmt.Sprintf("the query ran under repo scope %s, which inserted a filter after fetch %s; the text sent was: %s",
			e.Scope.Name, e.Scope.DataObject, e.Sent),
		fmt.Sprintf("retry with --%s to run the query exactly as typed", noRepoScopeFlag),
	}
}
