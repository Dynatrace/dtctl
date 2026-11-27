package cmd

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// The repo-scope commands' output for a terminal. Every other format prints
// the reposcope value itself.

// repoScopeShownCandidates is how many `set` lines an ambiguous result
// prints; -o json has them all.
const repoScopeShownCandidates = 5

// repoScopeShownIDs is how many ids of one kind a candidate's row lists; -o
// json has every id discovery collected.
const repoScopeShownIDs = 2

// printRepoScopeCurrent is `repo-scope current`: one line, the way
// `ctx current` answers, with the reason when there is one.
func printRepoScopeCurrent(w io.Writer, s *reposcope.Status) {
	if s.Entry == nil {
		// One line, also for a reason that quotes several.
		fmt.Fprintf(w, "none  (%s)\n", strings.Join(strings.Fields(repoScopeReason(s)), " "))
		return
	}
	how := repoScopeCoverage(s.Entry) + " · " + s.Environment
	if s.Reason != "" {
		how += "; " + s.Reason
	}
	if note := repoScopeClusterNote(s.Entry); note != "" {
		how += "; the cluster " + note
	}
	fmt.Fprintf(w, "%s  (%s)\n", s.Entry.Name, how)
}

// printRepoScopeDescribe is `repo-scope describe`.
func printRepoScopeDescribe(w io.Writer, s *reposcope.Status) {
	const width = 16
	kv := func(key string, values ...string) {
		if v := strings.Join(values, ", "); v != "" {
			output.FprintDescribeKV(w, key, width, "%s", v)
		}
	}
	if s.Entry == nil {
		kv("Repo scope:", "none")
		kv("Reason:", repoScopeReason(s))
		kv("Environment:", s.Environment)
		kv("File:", s.File)
		kv("Entries:", s.Others...)
		return
	}

	e := s.Entry
	kv("Name:", e.Name)
	kv("Path:", repoScopeCoverage(e))
	kv("Environment:", s.Environment)
	kv("File:", s.File)
	kv("Note:", s.Reason)
	kv("Workloads:", workloadNames(e.Workloads)...)
	kv("Cluster:", repoScopeClusterNote(e))
	kv("Service names:", e.ServiceNames...)
	kv("Process groups:", e.ProcessGroups...)
	kv("Services:", e.Services...)
	kv("Segments:", segmentRefs(e.Segments)...)
	kv("Annotations:", repoScopeAnnotations(e)...)
	// An entry's segments are sent whether or not a filter is inserted, so
	// a missing filter is "not scoped" only for an entry without them.
	output.FprintDescribeSection(w, "Filters:")
	for _, object := range reposcope.DataObjects() {
		filter, ok, reason := reposcope.Render(e, object)
		switch {
		case !e.HasBindings():
		case ok:
			fmt.Fprintf(w, "  fetch %-6s | filter (%s)\n", object, filter.Expr)
		case len(e.Segments) > 0:
			fmt.Fprintf(w, "  fetch %-6s no filter: %s; its segments are still sent\n", object, reason)
		default:
			fmt.Fprintf(w, "  fetch %-6s not scoped: %s\n", object, reason)
		}
	}
	switch {
	case !e.HasBindings():
		fmt.Fprintf(w, "  none: entry %q references only filter segments, which are sent with every query run under it\n", e.Name)
	case len(e.Segments) > 0:
		fmt.Fprintln(w, "  every query run under the entry is sent with its segments")
	}
	// One line per filter set, spelled the way --filters takes one.
	output.FprintDescribeSection(w, "Live Debugger filters:")
	if sets, ok, reason := s.LiveDebuggerFilterSets(); ok {
		for _, set := range sets {
			fmt.Fprintf(w, "  %s\n", formatFilters(set))
		}
		if e.BindsCluster() {
			fmt.Fprintln(w, "  not narrowed by cluster: the filters include the same workload in other clusters")
		}
	} else {
		fmt.Fprintf(w, "  none: %s\n", reason)
	}
	kv("Other entries:", s.Others...)
}

// repoScopeAnnotations are an entry's annotations as key=value, by key.
func repoScopeAnnotations(e *reposcope.Entry) []string {
	var out []string
	for _, key := range slices.Sorted(maps.Keys(e.Annotations)) {
		out = append(out, key+"="+e.Annotations[key])
	}
	return out
}

// repoScopeCoverage names the directory an entry covers.
func repoScopeCoverage(e *reposcope.Entry) string {
	return cmp.Or(e.Path, "whole repository")
}

// repoScopeClusterNote says what a bound cluster does not narrow, for an
// entry that binds one beside another kind of binding, and is "" otherwise.
// The bindings are alternatives and the cluster is part of a workload, so a
// service name or an id of the same entry selects its records in every
// cluster. Read as "the cluster narrows the entry", such an entry looks
// narrower than the filter it renders.
func repoScopeClusterNote(e *reposcope.Entry) string {
	if !e.BindsCluster() {
		return ""
	}
	var others []string
	for _, kind := range []struct {
		name   string
		values []string
	}{
		{"service names", e.ServiceNames}, {"process groups", e.ProcessGroups}, {"services", e.Services},
	} {
		if len(kind.values) > 0 {
			others = append(others, kind.name)
		}
	}
	if len(others) == 0 {
		return ""
	}
	last := len(others) - 1
	list := others[last]
	if last > 0 {
		list = strings.Join(others[:last], ", ") + " and " + list
	}
	return "narrows the workloads only: " + list + " select records of every cluster"
}

func workloadNames(workloads []reposcope.Workload) []string {
	names := make([]string, len(workloads))
	for i, wl := range workloads {
		names[i] = wl.String()
	}
	return names
}

// segmentRefs spells each segment an entry references the way query's
// --segment takes one.
func segmentRefs(segments []reposcope.Segment) []string {
	refs := make([]string, len(segments))
	for i, s := range segments {
		refs[i] = s.String()
	}
	return refs
}

// repoScopeRow is an entry as `repo-scope list` prints it in a table.
type repoScopeRow struct {
	Name          string `table:"NAME"`
	Path          string `table:"PATH"`
	Workloads     string `table:"WORKLOADS"`
	ServiceNames  string `table:"SERVICE NAMES"`
	ProcessGroups string `table:"PROCESS GROUPS"`
	Services      string `table:"SERVICES"`
	Segments      string `table:"SEGMENTS"`
}

func repoScopeRows(entries []reposcope.Entry) []repoScopeRow {
	rows := make([]repoScopeRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, repoScopeRow{
			Name:          e.Name,
			Path:          repoScopeCoverage(&e),
			Workloads:     strings.Join(workloadNames(e.Workloads), ", "),
			ServiceNames:  strings.Join(e.ServiceNames, ", "),
			ProcessGroups: strings.Join(e.ProcessGroups, ", "),
			Services:      strings.Join(e.Services, ", "),
			Segments:      strings.Join(segmentRefs(e.Segments), ", "),
		})
	}
	return rows
}

// printRepoScopeDiscovery is `repo-scope discover`.
func printRepoScopeDiscovery(w io.Writer, r *reposcope.DiscoveryReport, p repoScopeProposal, elapsed time.Duration) {
	ran := 0
	for _, q := range r.Queries {
		if !q.Skipped {
			ran++
		}
	}
	took := fmt.Sprintf("%d of %d queries ran in %.1fs. Nothing was written.", ran, len(r.Queries), elapsed.Seconds())

	switch {
	case len(r.Candidates) == 0:
		printRepoScopeNoMatch(w, r, p, took)
	case r.Verdict == reposcope.VerdictMatch:
		writeRepoScopeCandidates(w, r)
		var matched []string
		for _, kind := range reposcope.VerdictKinds() {
			if r.Verdicts[kind] == reposcope.VerdictMatch {
				matched = append(matched, reposcope.VerdictLabel(kind))
			}
		}
		// The one candidate can be without a kind that is settled: records
		// that name a workload and no namespace give nothing to bind.
		fmt.Fprintf(w, "Verdict: match: %s\n", cmp.Or(strings.Join(matched, ", "), "one candidate, with no binding that selects it"))
		if len(p.handLinks) > 0 {
			fmt.Fprintf(w, "%s No binding selects this candidate alone.\n", took)
			writeRepoScopeHandLinks(w, p)
			break
		}
		fmt.Fprintf(w, "%s Save it with:\n  %s\n", took, r.Candidates[0].SetCommand)
	default:
		writeRepoScopeCandidates(w, r)
		fmt.Fprintf(w, "Verdict: %s%s\n", r.Verdict, repoScopeDoubt(r))
		switch {
		case len(p.selectAll) > 0:
			fmt.Fprintln(w, took)
			for _, a := range p.selectAll {
				fmt.Fprintf(w, "%s:\n  %s\n", capitalize(a.sentence()), a.line)
			}
		case len(p.handLinks) > 0:
			why := "No binding selects any candidate alone."
			switch {
			case !slices.ContainsFunc(r.Candidates, func(c reposcope.Candidate) bool { return c.Indirect == "" }):
				why = "No candidate is named by the unit itself (see the notes)."
			case !slices.ContainsFunc(r.Candidates, func(c reposcope.Candidate) bool { return c.Indirect == "" && !c.WorkloadUnnamed }):
				why = "No candidate's workload is named by the unit itself (see the notes)."
			}
			fmt.Fprintf(w, "%s %s\n", took, why)
			writeRepoScopeHandLinks(w, p)
		case len(r.Candidates) == 1:
			fmt.Fprintf(w, "%s If it is what runs this code, save it with:\n  %s\n", took, r.Candidates[0].SetCommand)
		default:
			fmt.Fprintf(w, "%s Save the one that runs this code with its line:\n", took)
			for i, c := range r.Candidates {
				if i == repoScopeShownCandidates {
					fmt.Fprintf(w, "  (+%d more in -o json)\n", len(r.Candidates)-i)
					break
				}
				fmt.Fprintf(w, "  %d  %s\n", c.Rank, cmp.Or(c.SetCommand, "(no binding selects this candidate alone)"))
			}
		}
	}
	for _, n := range slices.Concat(p.sharedNotes, p.lineNotes, r.Notes) {
		fmt.Fprintf(w, "Note: %s\n", repoScopeText(n))
	}
}

// repoScopeDoubt says, after a verdict that is not a match, what stands in
// the way of one. With several candidates it is their number. With one it is
// not: that candidate's name matched only in part, the result is partial and
// a query that did not answer could have found another, a sample read the
// records and could have missed another, no name of the unit's own matched
// it or only its service name did, or only the entity table holds the name.
func repoScopeDoubt(r *reposcope.DiscoveryReport) string {
	switch {
	case len(r.Candidates) != 1:
		return fmt.Sprintf(" (candidates: %d)", len(r.Candidates))
	case r.Partial:
		return ": one candidate, but the result is partial (see the notes)"
	case r.SampledRecords():
		return ": one candidate, but the records were sampled, which can miss another (see the notes)"
	case r.Candidates[0].Indirect != "":
		return ": one candidate, " + r.Candidates[0].Indirect
	case r.Candidates[0].WorkloadUnnamed:
		return ": one candidate, found by its service.name alone; no name of this repository is its workload's"
	case r.Candidates[0].Tier == "exact" && r.Candidates[0].Workload == "" && r.Candidates[0].ServiceName == "":
		// Named exactly, but found by the entity-name search, which runs
		// only when the records find nothing: the entity exists, and
		// nothing shows that it runs.
		return ": one candidate, found by its entity name alone; no span or log in 24h carries a name of this repository"
	}
	return fmt.Sprintf(": one candidate, matched by a %s of its name rather than the whole of it", r.Candidates[0].Tier)
}

// writeRepoScopeCandidates writes the unit and the candidate table, each row
// followed by its evidence, indented to the second column. The cluster column
// shows the cluster's name where the records carry one, since that is what a
// reader knows it by; the uid a binding uses is in the evidence and the set
// line.
//
// The columns are laid out here rather than by a tabwriter: each row is
// followed by lines that are not part of the table, and a cell is text an
// environment sent.
func writeRepoScopeCandidates(w io.Writer, r *reposcope.DiscoveryReport) {
	rows := [][]string{{"#", "CLUSTER", "WORKLOAD", "SERVICE NAME", "PROCESS GROUP", "SERVICE", "TIER"}}
	for _, c := range r.Candidates {
		workload := ""
		switch {
		case c.Workload != "" && c.Namespace == "":
			// Not namespace/name with half of it missing: a workload is
			// bound by both.
			workload = c.Workload + " (no namespace)"
		case c.Workload != "":
			workload = reposcope.Workload{Namespace: c.Namespace, Name: c.Workload}.String()
		}
		row := []string{strconv.Itoa(c.Rank), cmp.Or(c.ClusterName, c.ClusterUID, "-"), cmp.Or(workload, "-"), cmp.Or(c.ServiceName, "-"),
			idsColumn(c.ProcessGroups, c.ProcessGroupCount, c.Sampled), idsColumn(c.Services, c.ServiceCount, c.Sampled), c.Tier}
		for i := range row {
			row[i] = repoScopeText(row[i])
		}
		rows = append(rows, row)
	}
	const gap = 2
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	line := func(row []string) string {
		var b strings.Builder
		for i, cell := range row[:len(row)-1] {
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+gap))
		}
		return b.String() + row[len(row)-1]
	}

	fmt.Fprintf(w, "Unit: %s\n\n%s\n", repoScopeText(r.Unit), line(rows[0]))
	for i, c := range r.Candidates {
		fmt.Fprintln(w, line(rows[i+1]))
		for _, e := range c.Evidence {
			fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", widths[0]+gap), repoScopeText(e))
		}
	}
	fmt.Fprintln(w)
}

// repoScopeText is s as the terminal output shows text an environment sent:
// a cluster, workload or service name is whatever the telemetry carries. A
// control character in it (a newline, a tab, an escape sequence) would break
// the table or act on the terminal beside a line meant to be pasted, so each
// is shown as U+FFFD.
func repoScopeText(s string) string {
	return reposcope.Shown(s)
}

// idsColumn is a candidate's ids of one kind in the table: the first few,
// then how many more the count says there are. The count is the source of
// truth, so the column reads the same whether the collected list was cut or
// not. A sampled candidate's ids are the ones its sample carried.
func idsColumn(ids []string, count int64, sampled bool) string {
	shown := ids[:min(len(ids), repoScopeShownIDs)]
	var parts []string
	if len(shown) > 0 {
		parts = append(parts, strings.Join(shown, ","))
	}
	if more := max(count, int64(len(ids))) - int64(len(shown)); more > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", more))
	}
	if len(parts) == 0 {
		return "-"
	}
	if sampled {
		parts = append(parts, "(sampled)")
	}
	return strings.Join(parts, " ")
}

// printRepoScopeNoMatch reports a discovery that found nothing: what it
// tried, what it sent, and what to do instead. A sampled retry the record
// query made unnecessary was never tried, so it is left out, as is the
// process-group lookup, which needs a candidate.
func printRepoScopeNoMatch(w io.Writer, r *reposcope.DiscoveryReport, p repoScopeProposal, took string) {
	fmt.Fprintf(w, "No entity matched unit %q on %s\n", r.Unit, r.Environment)
	if len(r.Queries) > 0 {
		fmt.Fprintln(w, "Tried:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, q := range r.Queries {
			if (q.SampledRetry() || q.ProcessGroupCheck()) && q.Skipped && q.Cause == "" {
				continue
			}
			first, _, _ := strings.Cut(q.DQL, "\n")
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", q.Purpose, repoScopeQueryOutcome(q), first)
		}
		_ = tw.Flush()
		// A run interrupted before its first query sent nothing.
		if len(r.Sent) == 0 {
			fmt.Fprintf(w, "No value was sent.\n%s\n", took)
		} else {
			fmt.Fprintf(w, "Values sent: %s\n%s\n", quotedValues(r.Sent), took)
		}
	}
	writeRepoScopeHandLinks(w, p)
}

// writeRepoScopeHandLinks offers what to do when no proposed line saves the
// unit: narrow the search, or link it by hand.
func writeRepoScopeHandLinks(w io.Writer, p repoScopeProposal) {
	fmt.Fprintf(w, "You can %s; or link by hand:\n", repoScopeNarrowHint)
	for _, l := range p.handLinks {
		fmt.Fprintf(w, "  %s\n", l)
	}
	if p.dirHint != "" {
		fmt.Fprintf(w, "%s.\n", capitalize(p.dirHint))
	}
}

// repoScopeQueryOutcome is one query's result in the "Tried" list.
func repoScopeQueryOutcome(q reposcope.Query) string {
	switch {
	case q.Skipped && q.Cause != "":
		return "skipped (" + reposcope.CauseWords(q.Cause) + ")"
	case q.Skipped:
		return "skipped"
	case q.Error != "":
		return "failed (" + reposcope.CauseWords(q.Cause) + ")"
	case q.Truncated:
		return strconv.Itoa(q.Rows) + " matched, capped"
	case q.Cause != "":
		return strconv.Itoa(q.Rows) + " matched (" + reposcope.CauseWords(q.Cause) + ")"
	}
	return strconv.Itoa(q.Rows) + " matched"
}

// quotedValues lists values the way the disclosure shows what is sent.
func quotedValues(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return strings.Join(quoted, ", ")
}
