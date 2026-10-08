package cmd

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
)

// The repo-scope commands' output for a terminal. Every other format prints
// the reposcope value itself.

// repoScopeShownCandidates is how many `set` lines an ambiguous result
// prints; -o json has them all.
const repoScopeShownCandidates = 5

// printRepoScopeCurrent is `repo-scope current`: one line, the way
// `ctx current` answers.
func printRepoScopeCurrent(w io.Writer, s *reposcope.Status) {
	if s.Entry == nil {
		fmt.Fprintf(w, "none  (%s)\n", s.Reason)
		return
	}
	fmt.Fprintf(w, "%s  (%s · %s)\n", s.Entry.Name, repoScopeCoverage(s.Entry), s.Environment)
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
		kv("Reason:", s.Reason)
		kv("Environment:", s.Environment)
		kv("File:", s.File)
		kv("Warning:", s.Warning)
		kv("Entries:", s.Others...)
		return
	}

	e := s.Entry
	kv("Name:", e.Name)
	kv("Path:", repoScopeCoverage(e))
	kv("Environment:", s.Environment)
	kv("File:", s.File)
	kv("Note:", s.Reason)
	kv("Warning:", s.Warning)
	kv("Services:", e.Services...)
	kv("Process groups:", e.ProcessGroups...)
	kv("Service names:", e.ServiceNames...)
	kv("Workloads:", workloadNames(e.Workloads)...)
	output.FprintDescribeSection(w, "Filters:")
	for _, object := range reposcope.DataObjects() {
		if filter, ok, reason := reposcope.Render(e, object); ok {
			fmt.Fprintf(w, "  fetch %-6s | filter (%s)\n", object, filter.Expr)
		} else {
			fmt.Fprintf(w, "  fetch %-6s not scoped: %s\n", object, reason)
		}
	}
	kv("Other entries:", s.Others...)
}

// repoScopeCoverage names the directory an entry covers.
func repoScopeCoverage(e *reposcope.Entry) string {
	return cmp.Or(e.Path, "whole repository")
}

func workloadNames(workloads []reposcope.Workload) []string {
	names := make([]string, len(workloads))
	for i, wl := range workloads {
		names[i] = wl.String()
	}
	return names
}

// repoScopeRow is an entry as `repo-scope list` prints it in a table.
type repoScopeRow struct {
	Name          string `table:"NAME"`
	Path          string `table:"PATH"`
	Services      string `table:"SERVICES"`
	ProcessGroups string `table:"PROCESS GROUPS"`
	ServiceNames  string `table:"SERVICE NAMES"`
	Workloads     string `table:"WORKLOADS"`
}

func repoScopeRows(entries []reposcope.Entry) []repoScopeRow {
	rows := make([]repoScopeRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, repoScopeRow{
			Name:          e.Name,
			Path:          repoScopeCoverage(&e),
			Services:      strings.Join(e.Services, ", "),
			ProcessGroups: strings.Join(e.ProcessGroups, ", "),
			ServiceNames:  strings.Join(e.ServiceNames, ", "),
			Workloads:     strings.Join(workloadNames(e.Workloads), ", "),
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
		for kind, v := range r.Verdicts {
			if v == reposcope.VerdictMatch {
				matched = append(matched, reposcope.VerdictLabel(kind))
			}
		}
		slices.Sort(matched)
		fmt.Fprintf(w, "Verdict: match: %s\n", strings.Join(matched, ", "))
		if len(p.handLinks) > 0 {
			fmt.Fprintf(w, "%s No binding selects this candidate alone.\n", took)
			writeRepoScopeHandLinks(w, p)
			break
		}
		fmt.Fprintf(w, "%s Save it with:\n  %s\n", took, r.Candidates[0].SetCommand)
	default:
		writeRepoScopeCandidates(w, r)
		fmt.Fprintf(w, "Verdict: %s (candidates: %d)\n", r.Verdict, len(r.Candidates))
		switch {
		case len(p.selectAll) > 0:
			fmt.Fprintln(w, took)
			for _, a := range p.selectAll {
				fmt.Fprintf(w, "%s:\n  %s\n", capitalize(a.sentence()), a.line)
			}
		case len(p.handLinks) > 0:
			fmt.Fprintf(w, "%s No binding selects any candidate alone.\n", took)
			writeRepoScopeHandLinks(w, p)
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
	for _, n := range p.sharedNotes {
		fmt.Fprintf(w, "Note: %s\n", n)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "Note: %s\n", n)
	}
}

// writeRepoScopeCandidates writes the unit and the candidate table, each row
// followed by its evidence, indented to the second column.
func writeRepoScopeCandidates(w io.Writer, r *reposcope.DiscoveryReport) {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tSERVICE\tPROCESS GROUP\tWORKLOAD\tSERVICE NAME\tTIER")
	for _, c := range r.Candidates {
		workload := ""
		if c.Workload != "" {
			workload = reposcope.Workload{Namespace: c.Namespace, Name: c.Workload}.String()
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", c.Rank, cmp.Or(strings.Join(c.Services, ","), "-"),
			cmp.Or(strings.Join(c.ProcessGroups, ","), "-"), cmp.Or(workload, "-"), cmp.Or(c.ServiceName, "-"), c.Tier)
	}
	_ = tw.Flush()
	rows := strings.SplitAfter(buf.String(), "\n")
	indent := strings.Repeat(" ", strings.Index(rows[0], "SERVICE"))

	fmt.Fprintf(w, "Unit: %s\n\n%s", r.Unit, rows[0])
	for i, c := range r.Candidates {
		fmt.Fprint(w, rows[i+1])
		for _, e := range c.Evidence {
			fmt.Fprintf(w, "%s%s\n", indent, e)
		}
	}
	fmt.Fprintln(w)
}

// printRepoScopeNoMatch reports a discovery that found nothing: what it
// tried, what it sent, and what to do instead.
func printRepoScopeNoMatch(w io.Writer, r *reposcope.DiscoveryReport, p repoScopeProposal, took string) {
	fmt.Fprintf(w, "No entity matched unit %q on %s\n", r.Unit, r.Environment)
	if len(r.Queries) > 0 {
		fmt.Fprintln(w, "Tried:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, q := range r.Queries {
			first, _, _ := strings.Cut(q.DQL, "\n")
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", q.Purpose, repoScopeQueryOutcome(q), first)
		}
		_ = tw.Flush()
		fmt.Fprintf(w, "Values sent: %s\n%s\n", quotedValues(r.Sent), took)
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
	case q.Skipped:
		return "skipped"
	case q.Error != "":
		return "failed (" + q.Cause + ")"
	case q.Truncated:
		return strconv.Itoa(q.Rows) + " matched, capped"
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
