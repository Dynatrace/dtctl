package cmd

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/dql"
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

// addRepoScopeFlags registers the entry selector and the opt-out on a command
// that sends DQL a user wrote.
func addRepoScopeFlags(c *cobra.Command) {
	c.Flags().String(repoScopeFlag, "", `narrow fetch logs/spans to this entry of the repository's .dtctl-repo-scope.yaml
default: the entry covering the working directory`)
	c.Flags().Bool(noRepoScopeFlag, false, "run the query exactly as typed, ignoring the repository's .dtctl-repo-scope.yaml (env: "+noRepoScopeEnv+"=1)")
	rejectEmptyFlag(c, repoScopeFlag)
	c.MarkFlagsMutuallyExclusive(repoScopeFlag, noRepoScopeFlag)
	stability.MarkFlag(c, repoScopeFlag, stability.Experimental, repoScopeSince)
	stability.MarkFlag(c, noRepoScopeFlag, stability.Experimental, repoScopeSince)
}

// repoScopeAdmitted reports whether the stability policy in force lets cmd
// scope a query nobody asked it to.
//
// Scoping by default changes what a stable command returns, and only its two
// flags carry the experimental mark. A floor that hides them would otherwise
// leave the behaviour running while refusing its opt-out, so the default is on
// only where both flags are usable: an exception for each admits it again. An
// explicit --repo-scope needs no such check; the floor already refused it if
// it was hidden.
func repoScopeAdmitted(cmd *cobra.Command) bool {
	policy := currentStabilityPolicy(cmdContext(cmd))
	path := commandPathRelative(cmd, cmd.Root())
	for _, name := range []string{repoScopeFlag, noRepoScopeFlag} {
		if _, ok := flagAdmitted(cmd, path, cmd.Flags().Lookup(name), policy); !ok {
			return false
		}
	}
	return true
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
	// zeroRows is set by onRows and read by decorate; the executor calls
	// OnRows first.
	zeroRows bool
}

// applyRepoScope narrows query to the repo scope in effect for the working
// directory, if any. The checks run cheapest first; the disk is touched only
// from the .git walk on:
//
//	--no-repo-scope, or DTCTL_NO_REPO_SCOPE without a name → off
//	no name, and the stability floor hides either flag     → off
//	context has no environment URL                         → off (--repo-scope: the error)
//	HostWorkingDirectory not granted, a session            → off (--repo-scope: CapabilityError)
//	no .git at or above the working directory              → off (--repo-scope: InvalidError)
//	no scope file, or no entry for this host               → off (--repo-scope: NotFoundError)
//	the file fails to load                                 → warning, invalid_file (--repo-scope: the error)
//	no entry covers the directory                          → warning, no_entry
//	an entry                                               → dql.InsertFilter decides
//
// "Off" is silence, so outside a linked repository every command behaves as
// if repo scopes did not exist. Anything else is reported on stderr in every
// mode and at every verbosity: a scoped run must say so whatever reads
// stdout. A file that cannot be used warns rather than fails, because it is
// committed and one bad edit would otherwise break every query run in the
// repository.
//
// A name asks for one entry, so a scope that resolves but cannot narrow this
// query is an error rather than an unscoped run; only the user's own filter
// on a bound field still runs, because it wins over the scope by design.
func applyRepoScope(cmd *cobra.Command, cfg *config.Config, query string) (*repoScopedQuery, error) {
	ctx := cmdContext(cmd)
	q := &repoScopedQuery{Query: query, typed: query, stderr: currentStderr(ctx)}
	name, _ := cmd.Flags().GetString(repoScopeFlag)
	if off, _ := cmd.Flags().GetBool(noRepoScopeFlag); off || (name == "" && truthyEnv(ctx, noRepoScopeEnv)) {
		return q, nil
	}
	if name == "" && !repoScopeAdmitted(cmd) {
		return q, nil
	}

	scope, err := openRepoScope(ctx, cfg)
	var status *reposcope.Status
	// Switching to a context the file does not mention must not start a
	// warning on every query.
	if err == nil && (name != "" || len(scope.file.Entries(scope.host)) > 0) {
		status, err = scope.resolve(name)
	}
	switch {
	case err != nil && name != "":
		return nil, err
	case err != nil && scope != nil:
		q.scope = &output.RepoScope{File: reposcope.FileName, Environment: scope.host, Code: repoScopeInvalidFile, Reason: err.Error()}
	case status == nil:
		return q, nil
	case status.Entry == nil:
		q.scope = &output.RepoScope{File: status.File, Environment: status.Environment, Code: repoScopeNoEntry, Reason: status.Reason}
	default:
		q.rewrite(status)
		if name != "" && slices.Contains(repoScopeRefusals, q.scope.Code) {
			return nil, &reposcope.InvalidError{
				Source:      "--" + repoScopeFlag,
				Msg:         fmt.Sprintf("entry %q cannot narrow this query (%s): %s", name, q.scope.Code, q.scope.Reason),
				Suggestions: []string{"drop --" + repoScopeFlag + " to run the query as typed"},
			}
		}
	}
	if status != nil && status.Warning != "" {
		q.warning = status.Warning
		q.scope.Reason = strings.TrimPrefix(q.scope.Reason+"; "+status.Warning, "; ")
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

// onRows is the executor's OnRows hook. A scoped query that returns nothing
// reads exactly like a quiet service, and the likelier cause is a binding the
// records do not carry, so it says how to tell the two apart.
func (q *repoScopedQuery) onRows(n int) {
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
