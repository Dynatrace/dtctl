package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/dql"
	"github.com/Dynatrace/dtctl/pkg/dqlhint"
	"github.com/Dynatrace/dtctl/pkg/exec"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
	"github.com/Dynatrace/dtctl/pkg/resources/segment"
	"github.com/Dynatrace/dtctl/pkg/stability"
	sdksegment "github.com/Dynatrace/dtctl/sdk/api/segment"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
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
	repoScopeNoEntry        = "no_entry"
	repoScopeAmbiguousEntry = "ambiguous_entry"
	repoScopeInvalidFile    = "invalid_file"
	repoScopeNoBinding      = "no_binding"
	// repoScopeSegmentUnavailable is an entry one of whose filter segments
	// could not be read: the query runs as typed, without the filter either.
	repoScopeSegmentUnavailable = "segment_unavailable"
	// repoScopeUserSegment is an entry one of whose segments the command line
	// gives too. That segment went out as typed, not as committed, so the
	// scope was not applied as the file says, whatever else of it was.
	repoScopeUserSegment = "user_segment"
	// repoScopeSegmentsNotSent is an entry that references only segments,
	// under a command that sends none: nothing of the scope was applied.
	repoScopeSegmentsNotSent = "segments_not_sent"
)

// repoScopeCodes is every context.repo_scope.code; docs/AGENT_MODE.md lists
// each one but applied.
var repoScopeCodes = []string{
	string(dql.Applied), string(dql.NotFetch), string(dql.UnsupportedObject), repoScopeNoBinding,
	string(dql.UserFilter), string(dql.Subquery), string(dql.Unscannable), repoScopeNoEntry, repoScopeAmbiguousEntry,
	repoScopeInvalidFile, repoScopeSegmentUnavailable, repoScopeUserSegment, repoScopeSegmentsNotSent,
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
	// wait query and verify query have no default: what they return (an
	// exit code, a canonical text) is read by programs, where nobody reads
	// the line that says a scope was applied.
	namedRepoScopeUsage = `narrow fetch logs/spans to this entry of the repository's .dtctl-repo-scope.yaml
default: none, the query runs exactly as typed`
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
	// entry to apply (invalid_file, no_entry, ambiguous_entry). It is nil when
	// an entry resolved: what applying it does is the consumer's to say.
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
//	no name, on a command without --no-repo-scope          → off (wait query, verify query)
//	context has no environment URL                         → off (--repo-scope: the error)
//	HostWorkingDirectory not granted, a session            → off (--repo-scope: CapabilityError)
//	no .git at or above the working directory              → off (--repo-scope: InvalidError)
//	no scope file, or no entry for this host               → off (--repo-scope: NotFoundError)
//	the file fails to load                                 → warning, invalid_file (--repo-scope: the error)
//	no entry covers the directory                          → warning, no_entry
//	several entries cover it as closely                    → warning, ambiguous_entry
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
			File: reposcope.FileName, Environment: scope.host, Code: repoScopeInvalidFile, Reason: scope.unusable(err),
		}}, nil
	case status == nil:
		return nil, nil
	}
	res := &repoScopeResolution{Status: status, Warning: status.Warning}
	if status.Entry == nil {
		code := repoScopeNoEntry
		if status.Ambiguous() {
			code = repoScopeAmbiguousEntry
		}
		res.Scope = &output.RepoScope{File: status.File, Environment: status.Environment, Code: code, Reason: repoScopeReason(status)}
	}
	return res, nil
}

// warnRepoScopeFile reports keys the scope file holds that this dtctl does
// not know, for every command that reads it.
func warnRepoScopeFile(w io.Writer, warning string) {
	if warning != "" {
		output.FprintWarning(w, "%s: %s", reposcope.FileName, warning)
	}
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
	// named is the entry --repo-scope asked for, "" for the covering one.
	named string
	// optOut says the command has a --no-repo-scope this run may pass: one
	// a stability floor hides is not advice. asTyped and without word
	// the way back to the typed query by it and by named.
	optOut bool
	// filtered says the entry's filter was inserted. scope.Applied holds
	// then, and for an entry without bindings whose segments are sent.
	filtered bool
	// inserted is the run of bytes the filter added to typed, "" without one.
	inserted string
	// bound says the entry has bindings, so a filter was attempted; an entry
	// without them references segments and nothing else.
	bound bool
	// segments are the filter segments the entry references, while they are
	// still to be sent; unsent says a command that sends none dropped them.
	segments []reposcope.Segment
	unsent   string
	// overridden are the segments of the entry the user named too. Each is
	// then theirs and no longer among segments.
	overridden []string
	// userSegments are the ids of the segments the command line sent beside
	// the entry's, for a failure that names none.
	userSegments []string
	// refusedCode and refusedReason are why the filter was not inserted, and
	// unavailable why a segment could not be read; settle reports them.
	refusedCode, refusedReason, unavailable string
	// warning names keys the file holds that this dtctl does not know. The
	// envelope has one reason field, so scope.Reason carries it too;
	// notApplied is the reason without it, for stderr, where the warning has
	// a line of its own.
	warning    string
	notApplied string
	stderr     io.Writer
	// zeroRows is set by onResult and read by decorate; the executor calls
	// OnResult first.
	zeroRows bool
	// scanBounded is set when the fetch stage bounds its own scan.
	scanBounded bool
	// scanLifted is set when a scan limit of -1 in the fetch stage removes
	// the limit altogether.
	scanLifted bool
	// unboundedScan is set when the scope line said nothing bounds the scan
	// below the environment's default cap, or at all. That is advice given
	// before the run, and does not stand in for the observation below.
	unboundedScan bool
	// scanCapNoticed is set once the scan-cap line is printed, so a caller
	// that polls prints it once.
	scanCapNoticed bool
}

// applyRepoScope narrows query to the repo scope resolveRepoScope picks for
// the working directory, if any; dql.InsertFilter decides what an entry does
// to it. The caller reports the result with announce once its query limits
// are resolved, because the line says whether anything caps the scan.
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
	q.named, q.warning = name, res.Warning
	q.optOut = repoScopeAdmitted(cmd, noRepoScopeFlag)
	q.scope = res.Scope
	if q.scope == nil {
		refused := q.rewrite(res.Status)
		if name != "" && slices.Contains(repoScopeRefusals, refused) {
			return nil, &reposcope.InvalidError{
				Source:      "--" + repoScopeFlag,
				Msg:         fmt.Sprintf("entry %q cannot narrow this query (%s): %s", name, refused, q.refusedReason),
				Suggestions: []string{q.asTyped() + " to run the query as typed"},
			}
		}
		return q, nil
	}
	q.setReason(q.scope.Reason)
	return q, nil
}

// setReason records why the scope, or its filter, was not applied. The
// envelope has one reason field, so it carries the file's warning too.
func (q *repoScopedQuery) setReason(reason string) {
	q.notApplied = reason
	q.scope.Reason = reason
	if q.warning != "" {
		q.scope.Reason = strings.TrimPrefix(reason+"; "+q.warning, "; ")
	}
}

// rewrite applies the resolved entry to the query and records the outcome.
// It returns the code of a filter that could not be inserted, "" when it was
// or when the entry has none to insert.
//
// The entry's filter and its segments fail apart. A filter that cannot be
// inserted leaves the segments to be sent, so the query is still narrowed,
// but by less than the entry says: the scope reports not applied, with the
// refusal's code and the segments that went out (see settle).
func (q *repoScopedQuery) rewrite(res *reposcope.Status) (refused string) {
	e := res.Entry
	q.segments, q.bound = e.Segments, e.HasBindings()
	q.scope = &output.RepoScope{Name: e.Name, File: res.File, Environment: res.Environment, Code: string(dql.Applied)}
	if q.bound {
		rw := dql.InsertFilter(q.Query, reposcope.Filters(e))
		q.Query, q.scanBounded, q.filtered = rw.Query, rw.ScanBounded, rw.Outcome == dql.Applied
		q.inserted = rw.Inserted
		q.scanLifted = rw.ScanLimitLifted
		q.scope.DataObject, q.scope.Filter = rw.DataObject, rw.Filter
		code, reason := string(rw.Outcome), rw.Reason
		// The rewrite only knows the filters it was given. For an object
		// repo scopes do filter, the entry is what is missing, and Render
		// says which binding to add.
		if rw.Outcome == dql.UnsupportedObject && slices.Contains(reposcope.DataObjects(), rw.DataObject) {
			_, _, reason = reposcope.Render(e, rw.DataObject)
			code = repoScopeNoBinding
		}
		if !q.filtered {
			q.refusedCode, q.refusedReason = code, reason
		}
	}
	q.settle()
	return q.refusedCode
}

// settle derives what the envelope says from the two halves. The scope is
// applied when everything the entry holds went out as the entry holds it:
// its filter, and its segments when it has any. An entry whose filter was
// refused is not applied even though its segments are sent, because the rows
// are then wider than the file says; it carries the refusal's code, and
// lists the segments. Nor is an entry one of whose segments the command line
// replaced: the variables sent for it are the user's, and the file's may
// have been the narrower ones.
func (q *repoScopedQuery) settle() {
	s := q.scope
	s.Segments = nil
	for _, seg := range q.segments {
		s.Segments = append(s.Segments, output.RepoScopeSegment{ID: seg.ID, Variables: seg.Variables})
	}
	sent := q.filtered || (!q.bound && len(q.segments) > 0)
	s.Applied = sent && len(q.overridden) == 0
	switch {
	case q.unavailable != "":
		s.Code = repoScopeSegmentUnavailable
		q.setReason(q.unavailable)
	case !sent && !q.bound && len(q.overridden) > 0:
		s.Code = repoScopeUserSegment
		q.setReason("every segment it references is given on the command line, and those are sent")
	case !sent && !q.bound:
		// Only a command that sends no segments gets here (withoutSegments).
		s.Code = repoScopeSegmentsNotSent
		q.setReason("the entry references only filter segments, and verify query checks the query text only")
	case !sent && len(q.segments) > 0:
		s.Code = q.refusedCode
		q.setReason(q.refusedReason + "; the entry's filter segments were sent")
	case !sent:
		s.Code = q.refusedCode
		q.setReason(q.refusedReason)
	case len(q.overridden) > 0:
		s.Code = repoScopeUserSegment
		q.setReason(q.replacedReason())
	default:
		s.Code = string(dql.Applied)
		q.setReason("")
	}
}

// replacedReason says which segments of the entry the command line replaced,
// and what of the entry went out all the same.
func (q *repoScopedQuery) replacedReason() string {
	which := "segment " + q.overridden[0] + " is"
	if len(q.overridden) > 1 {
		which = "segments " + strings.Join(q.overridden, ", ") + " are"
	}
	var rest []string
	if q.filtered {
		rest = append(rest, "the entry's filter was inserted")
	}
	if len(q.segments) > 0 {
		rest = append(rest, "its segments "+q.segmentList()+" were sent")
	}
	return which + " given on the command line, and what is given there is sent in place of the entry's; " + strings.Join(rest, " and ")
}

// segmentList spells the entry's segments the way --segment takes them.
func (q *repoScopedQuery) segmentList() string {
	return strings.Join(segmentRefs(q.segments), ", ")
}

// checkSegments reads each filter segment the entry references, once, before
// anything is sent. The platform fails a query that names a segment it
// cannot find, so without the check one deleted segment would fail every
// query run in the repository.
//
// A segment that is not there, or that the caller may not read, drops the
// scope whole and the query runs as typed: with the filter and without the
// segment it would return rows that look scoped and are wider than the
// entry. A named entry is what the caller asked for, so there it is the
// error. Any other failed read (a server error, a rate limit, a timeout) says
// nothing about the segment and may pass on the next run, so it is an error
// either way: an unscoped run on a passing fault would hand back
// environment-wide rows for a query that is scoped a moment later.
//
// The read asks for no includes and no variables, and whether a segment's
// variables are all bound is not checked: wrapError explains that failure
// when the platform reports it. The reads run one after another. An entry
// holds at most ten, in parallel they would arrive as a burst against the
// same rate limit, and a rate-limited read is now an error.
//
// The reads end with ctx: the invocation's, which Ctrl+C cancels, and for a
// wait the one that carries its deadline. Up to ten reads, each with the
// client's own timeout and retries, would otherwise outlast both.
func (q *repoScopedQuery) checkSegments(ctx context.Context, c *client.Client) error {
	if !q.readsSegments() {
		return nil
	}
	handler := segment.NewHandler(c)
	for _, seg := range q.segments {
		err := handler.Check(ctx, seg.ID)
		if err == nil {
			continue
		}
		var reason string
		switch {
		case ctx.Err() != nil:
			return fmt.Errorf("repo scope %s: reading filter segment %s was interrupted (%w); the query was not sent", q.scope.Name, seg.ID, ctx.Err())
		case errors.Is(err, sdksegment.ErrNotFound):
			reason = fmt.Sprintf("filter segment %s does not exist in this environment, or is not shared with you", seg.ID)
		case errors.Is(err, httpclient.ErrForbidden):
			reason = fmt.Sprintf("filter segment %s could not be read (%v)", seg.ID, err)
		default:
			return fmt.Errorf("repo scope %s: filter segment %s could not be read (%w); the query was not sent: retry, or %s to run it as typed",
				q.scope.Name, seg.ID, err, q.asTyped())
		}
		if q.named != "" {
			return &reposcope.InvalidError{
				Source: "--" + repoScopeFlag,
				Msg:    fmt.Sprintf("entry %q cannot be applied: %s", q.named, reason),
				Suggestions: []string{
					fmt.Sprintf("correct the entry's segments in %s; list the environment's with: dtctl get segments", reposcope.FileName),
					q.asTyped() + " to run the query as typed",
				},
			}
		}
		q.Query, q.filtered, q.inserted, q.segments, q.unavailable = q.typed, false, "", nil, reason
		q.scope.DataObject, q.scope.Filter = "", ""
		q.settle()
		return nil
	}
	return nil
}

// typedIfScoped is the query as typed when the scope changed what was sent,
// in text or in segments, and "" otherwise. The executor's empty-result
// diagnosis takes it to mean the request is not the user's own, so an
// unscoped query must not carry it.
func (q *repoScopedQuery) typedIfScoped() string {
	if !q.changedRequest() {
		return ""
	}
	return q.typed
}

// changedRequest reports that the request is not the one the user wrote:
// the scope inserted its filter, or sends segments.
func (q *repoScopedQuery) changedRequest() bool {
	return q.scope != nil && (q.filtered || len(q.segments) > 0)
}

// repoScopeHint is the one line of a command that applies a scope only when
// one is named, run without a name where the covering entry would have
// changed the request: the entry exists, and this is how to use it. It is
// empty everywhere else, so the command says nothing a caller did not see
// before repo scopes existed: where the user turned them off, where the
// stability floor hides the flag the line names, outside a linked repository,
// for a file or an entry that offers this query nothing, and for an entry
// that, named, would refuse it.
//
// sendsSegments says whether the command sends an entry's filter segments. A
// command that does not is offered nothing by an entry that has only those.
func repoScopeHint(cmd *cobra.Command, cfg *config.Config, query string, sendsSegments bool) string {
	ctx := cmdContext(cmd)
	if name, _ := cmd.Flags().GetString(repoScopeFlag); name != "" || truthyEnv(ctx, noRepoScopeEnv) || !repoScopeAdmitted(cmd, repoScopeFlag) {
		return ""
	}
	scope, err := openRepoScope(ctx, cfg)
	if err != nil || len(scope.file.Entries(scope.host)) == 0 {
		return ""
	}
	status, err := scope.resolve("")
	if err != nil || status.Entry == nil {
		return ""
	}
	// The line names a command, so it is printed only for one that runs:
	// named, an entry whose filter cannot be inserted is an error.
	would := &repoScopedQuery{Query: query, typed: query}
	refused := would.rewrite(status)
	if !sendsSegments {
		would.withoutSegments()
	}
	if slices.Contains(repoScopeRefusals, refused) || !would.changedRequest() {
		return ""
	}
	return fmt.Sprintf("repo scope %s covers this directory and is not applied: %s applies a scope only when it is named, with --%s %s",
		status.Entry.Name, commandPathRelative(cmd, cmd.Root()), repoScopeFlag, status.Entry.Name)
}

// asTyped is what to change on this command line to run the query as typed.
// Under a name that is not "add --no-repo-scope", which the name excludes,
// and not "drop the name", which applies whichever entry covers the
// directory: the opt-out goes in the name's place. A command without the
// opt-out applies a scope only when one is named, so there the name is
// dropped.
func (q *repoScopedQuery) asTyped() string {
	switch {
	case q.named == "":
		return "pass --" + noRepoScopeFlag
	case q.optOut:
		return fmt.Sprintf("replace --%s %s with --%s", repoScopeFlag, q.named, noRepoScopeFlag)
	}
	return fmt.Sprintf("drop --%s %s", repoScopeFlag, q.named)
}

// without is asTyped for the end of a sentence: "compare with
// --no-repo-scope", "retry without --repo-scope checkout".
func (q *repoScopedQuery) without() string {
	switch {
	case q.named == "":
		return "with --" + noRepoScopeFlag
	case q.optOut:
		return fmt.Sprintf("with --%s in place of --%s %s", noRepoScopeFlag, repoScopeFlag, q.named)
	}
	return fmt.Sprintf("without --%s %s", repoScopeFlag, q.named)
}

// ignoreHint ends a line about a scope nobody named with how to turn it
// off. Someone who named the entry does not need telling.
func (q *repoScopedQuery) ignoreHint(what string) string {
	if q.named != "" {
		return ""
	}
	return fmt.Sprintf(" (use --%s to %s)", noRepoScopeFlag, what)
}

// readsSegments reports that checkSegments has segments to read.
func (q *repoScopedQuery) readsSegments() bool {
	return q.scope != nil && len(q.segments) > 0
}

// requireSegments is for a command that must not run unscoped on its own
// decision, because it keeps running after the one line that says so: live
// mode redraws the screen the warning was on. refusal says what the command
// does not do, as in "query --live does not refresh unscoped". wait query,
// which polls, needs no such check: it applies a scope only under a name,
// and there a segment that cannot be read is already an error.
func (q *repoScopedQuery) requireSegments(refusal string) error {
	if q.scope == nil || q.unavailable == "" {
		return nil
	}
	return fmt.Errorf("repo scope %s cannot be applied: %s; %s: correct the entry's segments in %s, or %s",
		q.scope.Name, q.unavailable, refusal, reposcope.FileName, q.asTyped())
}

// refuseSegmentVars fails a --segment-var aimed at a segment the entry
// references and the command line does not: the flag binds variables of a
// segment the user gave, and the entry's values are the entry's. It says how
// to give the segment other variables, which is to name it. The advice keeps
// the --segment-var as typed: the "id?name=value" spelling of --segment
// splits its values at & and , and so cannot hold every value.
//
// user is the command line's segments, resolved, and origIDs what each was
// typed as: a binding may name its segment either way, as applySegmentVars
// accepts, so a --segment given by name is the user's under its id too.
func (q *repoScopedQuery) refuseSegmentVars(cmd *cobra.Command, user []exec.FilterSegmentRef, origIDs map[string]string) error {
	if q.scope == nil || len(q.segments) == 0 {
		return nil
	}
	vars, _ := cmd.Flags().GetStringArray("segment-var")
	for _, v := range vars {
		id, _, _ := strings.Cut(strings.TrimSpace(v), ":")
		id = strings.TrimSpace(id)
		given := slices.ContainsFunc(user, func(u exec.FilterSegmentRef) bool { return u.ID == id || origIDs[u.ID] == id })
		if given || !slices.ContainsFunc(q.segments, func(s reposcope.Segment) bool { return s.ID == id }) {
			continue
		}
		return fmt.Errorf("--segment-var names segment %q, which repo scope %s references; to give it other variables, name the segment too: add --segment %q",
			id, q.scope.Name, id)
	}
	return nil
}

// yieldTo drops the entry's segments the user also names: such a segment is
// sent once, with the user's variables, since what is typed wins over what
// is committed. From then on it is the user's, so the scope neither reads it
// nor reports it. More segments than a query takes is an error: leaving some
// out would change the rows without saying so.
func (q *repoScopedQuery) yieldTo(user []exec.FilterSegmentRef) error {
	if q.scope == nil || len(q.segments) == 0 {
		return nil
	}
	kept := q.segments[:0:0]
	for _, seg := range q.segments {
		if slices.ContainsFunc(user, func(u exec.FilterSegmentRef) bool { return u.ID == seg.ID }) {
			output.FprintInfo(q.stderr, "repo scope %s: segment %s is also given on the command line; its variables there are used", q.scope.Name, seg.ID)
			q.overridden = append(q.overridden, seg.ID)
			continue
		}
		kept = append(kept, seg)
	}
	if len(user)+len(kept) > maxSegmentsPerQuery {
		return fmt.Errorf("too many segments: %d given and %d from repo scope %s, maximum is %d per query; give fewer, or %s",
			len(user), len(kept), q.scope.Name, maxSegmentsPerQuery, q.asTyped())
	}
	q.segments = kept
	q.settle()
	return nil
}

// withSegments is the segments to send: the user's own, then the entry's.
func (q *repoScopedQuery) withSegments(user []exec.FilterSegmentRef) []exec.FilterSegmentRef {
	if q.scope == nil {
		return user
	}
	out := slices.Clone(user)
	q.userSegments = nil
	for _, u := range user {
		q.userSegments = append(q.userSegments, u.ID)
	}
	for _, seg := range q.segments {
		ref := exec.FilterSegmentRef{ID: seg.ID}
		for _, name := range seg.VariableNames() {
			ref.Variables = append(ref.Variables, exec.FilterSegmentVariable{Name: name, Values: seg.Variables[name]})
		}
		out = append(out, ref)
	}
	return out
}

// withoutSegments is for a command that sends the query text and nothing
// beside it: the entry's segments are not sent, and announce says so.
func (q *repoScopedQuery) withoutSegments() {
	if q.scope == nil || len(q.segments) == 0 {
		return
	}
	q.unsent, q.segments = q.segmentList(), nil
	q.settle()
}

// announce writes the line that says what the scope did, and one for keys
// the file holds that this dtctl does not know.
//
// Anything but "off" is reported on stderr in every mode and at every
// verbosity: a scoped run must say so whatever reads stdout, and a scope that
// resolved but was not applied warns, and the query runs unscoped.
//
// limits are the run's resolved query limits, nil for a command that runs no
// query. When neither they (a positive scan limit, a sampling ratio above 1)
// nor the fetch stage bound the scan, the line says so before anything is
// sent: a scoped query reads like a cheap one, and the
// scan cap would otherwise be the first to say it was not. A negative limit
// among them lifts nothing: only a positive one is sent, so the environment's
// default cap stands as it does for none.
func (q *repoScopedQuery) announce(limits *config.QueryLimits) {
	if q.scope == nil {
		return
	}
	warnRepoScopeFile(q.stderr, q.warning)
	s := q.scope
	if q.unsent != "" {
		defer output.FprintInfo(q.stderr, "repo scope %s: its segments %s are neither read nor sent: verify query checks the query text only; query sends them", s.Name, q.unsent)
		if !q.bound {
			return
		}
	}
	with := ""
	if len(q.segments) > 0 {
		with = " with segments " + q.segmentList()
	}
	switch {
	case q.filtered:
		clause := ""
		// The fetch stage's own limit wins over the flag's default.
		if !q.scanBounded && limits != nil && limits.SamplingRatio <= 1 && (q.scanLifted || limits.ScanLimitGbytes <= 0) {
			q.unboundedScan = true
			clause = "; " + q.scanClause()
		}
		output.FprintInfo(q.stderr, "applying repo scope %s to fetch %s: %s%s%s%s",
			s.Name, s.DataObject, s.Filter, with, q.ignoreHint("ignore it"), clause)
		return
	case with != "" && !q.bound:
		output.FprintInfo(q.stderr, "applying repo scope %s: segments %s%s", s.Name, q.segmentList(), q.ignoreHint("ignore it"))
		return
	case with != "":
		output.FprintWarning(q.stderr, "repo scope %s: its filter was not inserted: %s; its segments %s still apply%s",
			s.Name, q.refusedReason, q.segmentList(), q.ignoreHint("ignore them"))
		return
	}
	if s.Code == repoScopeUserSegment {
		output.FprintInfo(q.stderr, "repo scope %s not applied: %s%s", s.Name, q.notApplied, q.ignoreHint("silence this"))
		return
	}
	label := "repo scope"
	if s.Name != "" {
		label += " " + s.Name
	}
	// A query that filters a bound field itself is not unscoped the way the
	// other refusals leave it: its own condition is what narrows it.
	runs := "the query runs unscoped"
	if s.Code == string(dql.UserFilter) {
		runs = "the query runs as typed, with its own filter and none inserted"
	}
	output.FprintWarning(q.stderr, "%s not applied: %s; %s%s",
		label, q.notApplied, runs, q.ignoreHint("silence this"))
}

// onResult is the executor's OnResult hook. A scoped query that returns
// nothing reads exactly like a quiet service, and the likelier cause is a
// binding the records do not carry, so it says how to tell the two apart. A
// scoped query the scan cap stopped says what the scope does to the scan.
func (q *repoScopedQuery) onResult(n int, result *exec.DQLQueryResponse) {
	if !q.changedRequest() {
		return
	}
	if n == 0 || q.countedNothing(n, result) {
		q.zeroRows = true
		q.noticeZeroRecords()
	}
	q.noticeScanCap(n, result)
}

func (q *repoScopedQuery) noticeZeroRecords() {
	output.FprintInfo(q.stderr, "0 records under repo scope %s; compare %s", q.scope.Name, q.without())
}

// countedNothing reports the empty result that is not zero rows: a query
// ending in an ungrouped summarize returns one row whatever it was given, and
// its count() says how many records that was. Only that column is read. Any
// other aggregate can be zero over plenty of records, and Grail's metadata
// reports the records scanned, not the ones the filter let through.
func (q *repoScopedQuery) countedNothing(n int, result *exec.DQLQueryResponse) bool {
	if n != 1 || result == nil {
		return false
	}
	column, ok := dql.UngroupedCount(q.typed)
	records := result.GetRecords()
	if !ok || len(records) != 1 {
		return false
	}
	// Grail sends a long as a string.
	switch v := records[0][column].(type) {
	case string:
		return v == "0"
	case float64:
		return v == 0
	case json.Number:
		return v == "0"
	}
	return false
}

// noticeWaitOutcome is what wait query says once its polling is over. Each
// attempt's zero rows are the condition not met yet and get no line, but the
// outcome is where a scope the records do not match shows: the wait never
// sees its records, or a wait for none is satisfied at once. The scope line
// was printed before the first attempt, which may be minutes of progress
// output ago.
func (q *repoScopedQuery) noticeWaitOutcome(met bool, records int64) {
	switch {
	case !q.changedRequest():
	case !met:
		output.FprintInfo(q.stderr, "condition not met under repo scope %s; compare %s", q.scope.Name, q.without())
	case records == 0:
		q.noticeZeroRecords()
	}
}

// noticeScanCap is the part of onResult a polling caller wants: its zero rows
// are the condition not being met yet, not something to explain on every
// attempt (noticeWaitOutcome says it once, at the end). The line is printed
// once per run, however often it is called.
//
// The inserted filter is an equality filter, one of the levers the
// executor's own scan-limit hint lists, so the line says that lever is
// already pulled and points at the hint for the others rather than
// repeating them: a scope cannot by itself keep a wide timeframe under the
// cap, and reading it as a cheaper query is the mistake the line prevents.
func (q *repoScopedQuery) noticeScanCap(_ int, result *exec.DQLQueryResponse) {
	if q.scope == nil || !q.filtered || q.scanCapNoticed || !scanCapped(result) {
		return
	}
	q.scanCapNoticed = true
	output.FprintInfo(q.stderr, "%s", q.scanCapNotice())
}

// scanClause ends the scope line of a run nothing bounds the scan of, and is
// the envelope's suggestion for it.
func (q *repoScopedQuery) scanClause() string {
	if q.scanLifted {
		return fmt.Sprintf("the repo scope filters early with %s, and no scan limit applies to this query (a scanLimitGBytes of -1): set a positive one to bound the scan",
			q.comparisons())
	}
	return fmt.Sprintf("the repo scope filters early with %s, but no scan limit is set for this query, so the environment's default cap applies: add scanLimitGBytes: or --default-scan-limit-gbytes to scan less",
		q.comparisons())
}

func (q *repoScopedQuery) scanCapNotice() string {
	return fmt.Sprintf("repo scope %s already filters early with %s, and the scan still hit its cap; scan less another way the scan-limit hint lists",
		q.scope.Name, q.comparisons())
}

// comparisons names what the inserted filter compares with: ==, in(), or
// both. Names and workloads render as ==, entity ids and lists as in(); the
// lines that say the scope already filters early say how, so they agree with
// the filter printed beside them.
func (q *repoScopedQuery) comparisons() string {
	m, _ := dql.Scan(q.scope.Filter) // literals masked: a name may hold either
	var with []string
	if strings.Contains(m.Code, "==") {
		with = append(with, "==")
	}
	if strings.Contains(m.Code, "in(") {
		with = append(with, "in()")
	}
	return strings.Join(with, " and ")
}

// scanCapped reports whether the scan limit cut result short. result is nil
// on a path that has no response to read.
func scanCapped(result *exec.DQLQueryResponse) bool {
	if result == nil {
		return false
	}
	for _, n := range result.GetNotifications() {
		if exec.PartialCause(n) == exec.PartialScanLimit {
			return true
		}
	}
	return false
}

// decorate puts the scope on the agent envelope, with the opt-out as a
// suggestion. On zero rows the comparison goes first: it is the one thing to
// try before widening the window. A scan nothing bounds, or one the cap
// stopped, adds what the scope does to it.
func (q *repoScopedQuery) decorate(c *output.ResponseContext, result *exec.DQLQueryResponse) {
	if q.scope == nil {
		return
	}
	c.RepoScope = q.scope
	switch {
	case q.zeroRows:
		c.Suggestions = append([]string{fmt.Sprintf(
			"0 records under repo scope %s: run the same query %s to tell whether the scope or the query filtered everything out",
			q.scope.Name, q.without())}, c.Suggestions...)
	case q.changedRequest():
		c.Suggestions = append(c.Suggestions, "run unscoped: "+q.asTyped())
	}
	// One scan suggestion: the cap that was hit, or else the advice.
	switch {
	case q.filtered && scanCapped(result):
		c.Suggestions = append(c.Suggestions, q.scanCapNotice())
	case q.unboundedScan:
		c.Suggestions = append(c.Suggestions, q.scanClause())
	}
}

// wrapError marks a failure of a query the scope rewrote or sent segments
// with, so its report shows what was actually sent.
//
// Only a query the platform refused is marked. Whatever else fails after the
// query came back (a --jq program, a write) has nothing to do with the scope,
// and advice to run unscoped would send its reader the wrong way.
func (q *repoScopedQuery) wrapError(err error) error {
	if err == nil || !q.changedRequest() {
		return err
	}
	var (
		refused  *exec.QueryError
		variable *exec.SegmentVariableError
		state    *exec.QueryStateError
	)
	if !errors.As(err, &refused) && !errors.As(err, &variable) && !errors.As(err, &state) {
		return err
	}
	return &ScopedQueryError{Err: err, Scope: q.scope, Sent: q.Query, inserted: q.inserted, named: q.named,
		asTyped: q.asTyped(), without: q.without(), userSegments: q.userSegments}
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
	// asTyped and without say how the command line that failed runs the
	// query as typed (repoScopedQuery.asTyped, without).
	asTyped, without string
	// userSegments are the ids of the segments the command line sent too.
	userSegments []string
	// inserted is the run of bytes the scope added to the typed text to make
	// Sent, and named the entry --repo-scope asked for; retypeRepairs turns a
	// repair of Sent into one of the command line that failed with them.
	inserted, named string
}

// repoScopeSegmentVariableCode is the agent error code of a query that
// failed over a variable of a segment the entry sent.
const repoScopeSegmentVariableCode = "repo_scope_segment_variable"

// Error is the original message: the human output already carried the
// "applying repo scope" notice before the query ran. A failure over a
// variable of a segment the entry sent is the exception, because the
// original advises flags the user never typed; segmentVariable words it.
func (e *ScopedQueryError) Error() string {
	if msg, ok := e.segmentVariable(); ok {
		return msg
	}
	return e.Err.Error()
}

// segmentVariable words the two failures a segment of the entry causes
// through its variables, naming the entry as the source and both ways out.
// One is a required variable the entry gives no value. The other is a
// variable the query cannot supply: a segment written for one kind of data
// fails a query over another, so an entry can break queries it has nothing
// to do with. ok is false for any other failure, and for a segment that is
// not the entry's.
func (e *ScopedQueryError) segmentVariable() (msg string, ok bool) {
	var required *exec.SegmentVariableError
	var queryErr *exec.QueryError
	switch {
	case len(e.Scope.Segments) == 0:
	case errors.As(e.Err, &required):
		if !slices.ContainsFunc(e.Scope.Segments, func(s output.RepoScopeSegment) bool { return s.ID == required.Segment }) {
			break
		}
		return fmt.Sprintf("segment %s requires variable %q, and repo scope %s sends it without one: give the variable a value under the entry's segments in %s, or %s to run the query as typed",
			required.Segment, required.Variable, e.Scope.Name, reposcope.FileName, e.asTyped), true
	case errors.As(e.Err, &queryErr) && queryErr.ErrorType == "MISSING_FILTER_SEGMENT_VARIABLE" && len(e.userSegments) > 0:
		// The platform names no segment here, so neither does dtctl.
		return fmt.Sprintf("one of the filter segments sent needs a variable this query cannot supply (%s): repo scope %s sent %s and the command line sent %s; remove the segment from the entry in %s or from the command line, or %s to send only the command line's",
			firstLineOf(queryErr.Error()), e.Scope.Name, e.segmentList(), strings.Join(e.userSegments, ", "), reposcope.FileName, e.asTyped), true
	case errors.As(e.Err, &queryErr) && queryErr.ErrorType == "MISSING_FILTER_SEGMENT_VARIABLE":
		return fmt.Sprintf("repo scope %s sends the filter segments %s, and one of them needs a variable this query cannot supply (%s): remove the segment from the entry in %s, or %s to run the query as typed",
			e.Scope.Name, e.segmentList(), firstLineOf(queryErr.Error()), reposcope.FileName, e.asTyped), true
	}
	return "", false
}

// segmentList spells the segments the scope sent the way --segment takes them.
func (e *ScopedQueryError) segmentList() string {
	refs := make([]string, len(e.Scope.Segments))
	for i, s := range e.Scope.Segments {
		refs[i] = reposcope.Segment{ID: s.ID, Variables: s.Variables}.String()
	}
	return strings.Join(refs, ", ")
}

func (e *ScopedQueryError) Unwrap() error { return e.Err }

// retypeRepairs replaces the runnable repairs in detail, which the query's
// own error built from the text Grail saw, with repairs of the command line
// that failed. Run as printed, such a repair would carry the inserted filter
// as if the user had typed it and, without the name, run under whichever
// entry covers the directory, with that entry's segments.
//
// Each repair has the inserted run taken out again and keeps the name. A
// repair in which the run is not found exactly once (a rule edited inside
// it) is dropped: no hint is better than one for another query. detail is
// left alone unless its suggestions are the ones the query error makes.
func (e *ScopedQueryError) retypeRepairs(detail *output.ErrorDetail) {
	var queryErr *exec.QueryError
	if !errors.As(e.Err, &queryErr) {
		return
	}
	hinted := queryHintError(queryErr)
	advice := dqlErrorAdvice(queryErr)
	if !slices.Equal(detail.Suggestions, append(dqlhint.Suggest(hinted), advice...)) {
		return
	}
	var flags []string
	if e.named != "" {
		flags = []string{"--" + repoScopeFlag, e.named}
	}
	var out []string
	for _, r := range dqlhint.Repairs(hinted) {
		query := r.Query
		if query != "" && e.inserted != "" {
			if strings.Count(query, e.inserted) != 1 {
				continue
			}
			query = strings.Replace(query, e.inserted, "", 1)
		}
		out = append(out, r.Render(query, flags...))
	}
	detail.Suggestions = append(out, advice...)
}

// Suggestions shows what was sent and how to rule the scope out. A segment's
// missing variable is one failure only the platform can report, so the
// segments the entry sent are named as well.
func (e *ScopedQueryError) Suggestions() []string {
	var out []string
	if e.Scope.Filter != "" {
		out = append(out, fmt.Sprintf("the query ran under repo scope %s, which inserted a filter after fetch %s; the text sent was: %s",
			e.Scope.Name, e.Scope.DataObject, e.Sent))
	}
	if len(e.Scope.Segments) > 0 {
		out = append(out, fmt.Sprintf("repo scope %s also sent the filter segments %s; if the failure names a segment or one of its variables, correct the entry's segments in %s",
			e.Scope.Name, e.segmentList(), reposcope.FileName))
	}
	return append(out, fmt.Sprintf("retry %s to run the query exactly as typed", e.without))
}
