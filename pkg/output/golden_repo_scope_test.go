package output

import (
	"bytes"
	"testing"

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
	s, err := reposcope.Resolve(f, repoScopeHost, "services/checkout/internal", "")
	if err != nil {
		panic(err)
	}
	return *s
}

// repoScopeQueries is the discovery plan for a unit named checkout, with the
// given outcome applied to each query in turn.
func repoScopeQueries(outcomes ...func(*reposcope.Query)) []reposcope.Query {
	plan := []reposcope.Query{
		{
			Purpose: "spans by workload or service name", DataObject: "spans",
			DQL: "fetch spans, from:now()-24h, scanLimitGBytes:5\n| filter in(k8s.workload.name, array(\"checkout\")) or in(service.name, array(\"checkout\"))\n| summarize records = count(), by:{k8s.namespace.name, k8s.workload.name, service.name, dt.entity.service, dt.entity.process_group}\n| sort records desc\n| limit 200",
		},
		{
			Purpose: "logs by workload or service name", DataObject: "logs",
			DQL: "fetch logs, from:now()-24h, scanLimitGBytes:5\n| filter in(k8s.workload.name, array(\"checkout\")) or in(service.name, array(\"checkout\"))\n| summarize records = count(), by:{k8s.namespace.name, k8s.workload.name, service.name, dt.entity.service, dt.entity.process_group}\n| sort records desc\n| limit 200",
		},
		{
			Purpose: "services by entity name", DataObject: "dt.entity.service",
			DQL: "fetch dt.entity.service, from:now()-24h\n| filter matchesValue(entity.name, \"*checkout*\")\n| fields id, entity.name\n| fieldsAdd nameLength = stringLength(entity.name)\n| sort nameLength asc\n| limit 200",
		},
		{
			Purpose: "process groups by entity name", DataObject: "dt.entity.process_group",
			DQL: "fetch dt.entity.process_group, from:now()-24h\n| filter matchesValue(entity.name, \"*checkout*\")\n| fields id, entity.name\n| fieldsAdd nameLength = stringLength(entity.name)\n| sort nameLength asc\n| limit 200",
		},
	}
	for i, apply := range outcomes {
		apply(&plan[i])
	}
	return plan
}

func repoScopeRows(n int) func(*reposcope.Query) { return func(q *reposcope.Query) { q.Rows = n } }
func repoScopeSkipped(q *reposcope.Query)        { q.Skipped = true }
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
			Rank:          1,
			Name:          "checkout",
			Services:      []string{repoScopeSvc},
			ProcessGroups: []string{repoScopePG},
			ServiceName:   "checkout",
			Namespace:     "payments",
			Workload:      "checkout",
			Tier:          "exact",
			Spans:         12400,
			Logs:          88000,
			Evidence: []string{
				`k8s.workload.name "checkout" from deploy/k8s/deployment.yaml:4, go.mod:1`,
				`service.name "checkout" from deploy/k8s/deployment.yaml:4, go.mod:1`,
				"12400 spans, 88000 logs in 24h",
			},
			SetCommand: "dtctl repo-scope set checkout --service SERVICE-0123456789ABCDEF --process-group PROCESS_GROUP-FEDCBA9876543210 --namespace payments --workload checkout --service-name checkout",
		}},
		Queries: repoScopeQueries(repoScopeRows(1), repoScopeRows(1), repoScopeSkipped, repoScopeSkipped),
		Sent:    []string{"checkout"},
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
			repoScopeRows(0),
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
