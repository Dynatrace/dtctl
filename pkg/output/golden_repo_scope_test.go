package output

import (
	"bytes"
	"testing"
	"testing/fstest"

	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
)

const (
	repoScopeHost    = "abc12345.apps.dynatrace.com"
	repoScopeStgHost = "stg98765.apps.dynatrace.com"
	repoScopeSvc     = "SERVICE-0123456789ABCDEF"
	repoScopePG      = "PROCESS_GROUP-FEDCBA9876543210"
)

func repoScopeEntries() []reposcope.Entry {
	return []reposcope.Entry{
		{
			Name:          "checkout",
			Path:          "services/checkout",
			Services:      []string{repoScopeSvc},
			ProcessGroups: []string{repoScopePG},
			ServiceNames:  []string{"checkout"},
			Workloads:     []reposcope.Workload{{Namespace: "payments", Name: "checkout"}},
		},
		{
			Name:      "ledger",
			Path:      "services/ledger",
			Workloads: []reposcope.Workload{{Namespace: "payments", Name: "ledger"}},
		},
	}
}

func repoScopeStatusLinked() reposcope.Status {
	f := &reposcope.File{Environments: map[string][]reposcope.Entry{
		repoScopeHost:    repoScopeEntries(),
		repoScopeStgHost: {{Name: "checkout", ServiceNames: []string{"checkout"}}},
	}}
	s, err := reposcope.Resolve(fstest.MapFS{}, f, repoScopeHost, "services/checkout/internal", "")
	if err != nil {
		panic(err)
	}
	return *s
}

// repoScopeQueries is the discovery plan for a unit named checkout, with the
// given outcome applied to each query in turn: two record queries, each
// followed by its three sampled retries, then the two entity searches.
func repoScopeQueries(outcomes ...func(*reposcope.Query)) []reposcope.Query {
	p, err := reposcope.PlanDiscovery(fstest.MapFS{}, ".", []string{"checkout"})
	if err != nil {
		panic(err)
	}
	plan := p.Queries
	for i, apply := range outcomes {
		apply(&plan[i])
	}
	return plan
}

func repoScopeRows(n int) func(*reposcope.Query) { return func(q *reposcope.Query) { q.Rows = n } }
func repoScopeSkipped(q *reposcope.Query)        { q.Skipped = true }
func repoScopeCapped(q *reposcope.Query)         { q.Truncated, q.Cause = true, "truncated" }
func repoScopeFailed(cause, msg string) func(*reposcope.Query) {
	return func(q *reposcope.Query) { q.Cause, q.Error = cause, msg }
}

func repoScopeVerdicts(v reposcope.Verdict) map[string]reposcope.Verdict {
	return map[string]reposcope.Verdict{"service": v, "processGroup": v, "workload": v, "serviceName": v}
}

func repoScopeDiscoveryMatch() reposcope.DiscoveryReport {
	return reposcope.DiscoveryReport{
		Environment: repoScopeHost,
		Dir:         ".",
		Unit:        ".",
		Verdict:     reposcope.VerdictMatch,
		Verdicts:    repoScopeVerdicts(reposcope.VerdictMatch),
		Candidates: []reposcope.Candidate{{
			Rank:              1,
			Name:              "checkout",
			Services:          []string{repoScopeSvc},
			ServiceCount:      1,
			ProcessGroups:     []string{repoScopePG},
			ProcessGroupCount: 1,
			ServiceName:       "checkout",
			Namespace:         "payments",
			Workload:          "checkout",
			Tier:              "exact",
			Spans:             12400,
			Logs:              88000,
			Evidence: []string{
				`k8s.workload.name "checkout" from deploy/k8s/deployment.yaml:4, go.mod:1`,
				`service.name "checkout" from deploy/k8s/deployment.yaml:4, go.mod:1`,
				"12400 spans, 88000 logs in 24h",
			},
			SetCommand: "dtctl repo-scope set checkout --service SERVICE-0123456789ABCDEF --process-group PROCESS_GROUP-FEDCBA9876543210 --namespace payments --workload checkout --service-name checkout",
		}},
		Queries: repoScopeQueries(repoScopeRows(1), repoScopeSkipped, repoScopeSkipped, repoScopeSkipped,
			repoScopeRows(1), repoScopeSkipped, repoScopeSkipped, repoScopeSkipped, repoScopeSkipped, repoScopeSkipped),
		Sent: []string{"checkout"},
	}
}

// repoScopeDiscoverySampled is a match on an environment where a day of
// spans scans past the cap: the first sampled retry answered, the rest were
// not needed, and the logs were read whole.
func repoScopeDiscoverySampled() reposcope.DiscoveryReport {
	return reposcope.DiscoveryReport{
		Environment: repoScopeHost,
		Dir:         ".",
		Unit:        ".",
		Verdict:     reposcope.VerdictMatch,
		Verdicts:    repoScopeVerdicts(reposcope.VerdictMatch),
		Candidates: []reposcope.Candidate{{
			Rank:              1,
			Name:              "checkout",
			Services:          []string{repoScopeSvc, "SERVICE-0123456789ABCDF0"},
			ServiceCount:      212,
			ServicesCapped:    true,
			ProcessGroups:     []string{repoScopePG},
			ProcessGroupCount: 1,
			ServiceName:       "checkout",
			Namespace:         "payments",
			Workload:          "checkout",
			Tier:              "exact",
			SpansSampled:      17,
			LogsSampled:       4,
			Sampled:           true,
			Evidence: []string{
				`k8s.workload.name "checkout" from deploy/k8s/deployment.yaml:4, go.mod:1`,
				"17 sampled span hits in 1h (1 in 100000 requested), 4 sampled log hits in 1h (1 in 100000 requested)",
			},
			SetCommand: "dtctl repo-scope set checkout --namespace payments --workload checkout",
		}},
		Queries: repoScopeQueries(repoScopeCapped, repoScopeRows(1), repoScopeSkipped, repoScopeSkipped,
			repoScopeCapped, repoScopeRows(1), repoScopeSkipped, repoScopeSkipped, repoScopeSkipped, repoScopeSkipped),
		Sent: []string{"checkout"},
		Notes: []string{
			"spans scan more than the 5 GB cap over 24h, so they were sampled 1-in-100000 over 1h; a service with fewer than about 300000 spans over 1h may be missing",
			"logs scan more than the 5 GB cap over 24h, so they were sampled 1-in-100000 over 1h; a service with fewer than about 300000 logs over 1h may be missing",
		},
	}
}

func repoScopeDiscoveryNone() reposcope.DiscoveryReport {
	return reposcope.DiscoveryReport{
		Environment: repoScopeHost,
		Dir:         "services/ledger",
		Unit:        "services/ledger",
		Verdict:     reposcope.VerdictNone,
		Verdicts:    repoScopeVerdicts(reposcope.VerdictNone),
		Candidates:  []reposcope.Candidate{},
		Queries:     repoScopeQueries(),
		Sent:        []string{"checkout"},
	}
}

func repoScopeDiscoveryPartial() reposcope.DiscoveryReport {
	return reposcope.DiscoveryReport{
		Environment: repoScopeHost,
		Dir:         "services/checkout",
		Unit:        ".",
		Verdict:     reposcope.VerdictNone,
		Verdicts:    repoScopeVerdicts(reposcope.VerdictNone),
		Candidates:  []reposcope.Candidate{},
		Queries: repoScopeQueries(
			repoScopeFailed("timeout", "context deadline exceeded"),
			repoScopeSkipped, repoScopeSkipped, repoScopeSkipped,
			repoScopeRows(0),
			repoScopeSkipped, repoScopeSkipped, repoScopeSkipped,
			repoScopeFailed("auth", "query failed (NOT_AUTHORIZED_FOR_TABLE): not authorized"),
			repoScopeFailed("unknown_object", "query failed (UNKNOWN_DATA_OBJECT): dt.entity.process_group"),
		),
		Sent: []string{"checkout"},
		Notes: []string{
			"discovery ran out of time; a narrower directory or an explicit name sends less",
			"the token cannot read dt.entity.service",
			"dt.entity.process_group is not available on this environment; the record queries are what applies here",
		},
		Partial: true,
	}
}

// The repo-scope commands print their terminal output themselves (cmd's
// goldens pin it), so only the structured formats go through these printers.
// One discovery report goes through every format; the others differ only in
// their values, which JSON shows.
func TestGolden_RepoScope(t *testing.T) {
	all := []string{"json", "yaml", "toon"}
	singles := map[string]struct {
		obj     interface{}
		formats []string
	}{
		"status-linked":     {repoScopeStatusLinked(), all},
		"status-unlinked":   {reposcope.Status{Reason: "not inside a git repository"}, all},
		"discovery-match":   {repoScopeDiscoveryMatch(), all},
		"discovery-none":    {repoScopeDiscoveryNone(), []string{"json"}},
		"discovery-partial": {repoScopeDiscoveryPartial(), []string{"json"}},
		"discovery-sampled": {repoScopeDiscoverySampled(), []string{"json"}},
	}
	for name, single := range singles {
		obj := single.obj
		for _, format := range single.formats {
			t.Run(name+"-"+format, func(t *testing.T) {
				var buf bytes.Buffer
				if err := NewPrinterWithWriter(format, &buf).Print(obj); err != nil {
					t.Fatalf("Print failed: %v", err)
				}
				assertGolden(t, "repo-scope/"+name+"-"+format, buf.String())
			})
		}
	}
	for _, format := range all {
		t.Run("entries-list-"+format, func(t *testing.T) {
			var buf bytes.Buffer
			if err := NewPrinterWithWriter(format, &buf).PrintList(repoScopeEntries()); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "repo-scope/entries-list-"+format, buf.String())
		})
	}
}
