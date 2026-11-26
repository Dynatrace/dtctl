package output

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Dynatrace/dtctl/pkg/reposcope"
	sdkquery "github.com/Dynatrace/dtctl/sdk/api/query"
	"github.com/Dynatrace/dtctl/sdk/inventory"
)

const (
	repoScopeHost    = "abc12345.apps.dynatrace.com"
	repoScopeStgHost = "stg98765.apps.dynatrace.com"
	repoScopeSvc     = "SERVICE-0123456789ABCDEF"
	repoScopePG      = "PROCESS_GROUP-FEDCBA9876543210"
	repoScopeCluster = "11111111-2222-3333-4444-555555555555"
)

func repoScopeEntries() []reposcope.Entry {
	return []reposcope.Entry{
		{
			Name:          "checkout",
			Path:          "services/checkout",
			Services:      []string{repoScopeSvc},
			ProcessGroups: []string{repoScopePG},
			ServiceNames:  []string{"checkout"},
			Workloads:     []reposcope.Workload{{ClusterUID: repoScopeCluster, Namespace: "payments", Name: "checkout"}},
		},
		{
			Name:      "ledger",
			Path:      "services/ledger",
			Workloads: []reposcope.Workload{{Namespace: "payments", Name: "ledger"}},
			Segments: []reposcope.Segment{
				{ID: "Ab3dEf9hIjk", Variables: map[string][]string{"environment": {"production", "staging"}}},
				{ID: "plain-1"},
			},
		},
	}
}

func repoScopeStatusLinked() reposcope.Status {
	return repoScopeStatus("services/checkout/internal")
}

// repoScopeStatus is the status resolved for dir: services/checkout is
// covered by an entry of bindings, services/ledger by one that also
// references filter segments.
func repoScopeStatus(dir string) reposcope.Status {
	f := &reposcope.File{Environments: map[string][]reposcope.Entry{
		repoScopeHost:    repoScopeEntries(),
		repoScopeStgHost: {{Name: "checkout", ServiceNames: []string{"checkout"}}},
	}}
	s, err := reposcope.Resolve(fstest.MapFS{}, f, repoScopeHost, dir, "")
	if err != nil {
		panic(err)
	}
	return *s
}

// repoScopeRunner answers discovery's queries from canned results, by the
// data object a query fetches, each object's answers in the order given: a
// record query's first, then its sampled retries'. A query with no answer
// left gets an empty result.
type repoScopeRunner map[string][]repoScopeAnswer

// repoScopeAnswer is one query's result: rows as the JSON Grail returns,
// and how the result was cut short, sampled, or failed.
type repoScopeAnswer struct {
	rows    string
	capped  bool // the scan cap stopped the query
	sampled bool
	scanned int64
	err     error
}

func (r repoScopeRunner) RunQuery(_ context.Context, dql string) (*inventory.RunResult, error) {
	object := strings.TrimSuffix(strings.Fields(strings.TrimPrefix(dql, "fetch "))[0], ",")
	var a repoScopeAnswer
	if queue := r[object]; len(queue) > 0 {
		a, r[object] = queue[0], queue[1:]
	}
	if a.err != nil {
		return nil, a.err
	}
	res := &inventory.RunResult{Sampled: a.sampled, ScannedBytes: a.scanned}
	if a.rows != "" {
		if err := json.Unmarshal([]byte("["+a.rows+"]"), &res.Records); err != nil {
			panic(err)
		}
	}
	if a.capped {
		res.Truncated, res.TruncationCause = true, inventory.TruncationScanLimit
		res.Truncations = []inventory.Truncation{{Cause: inventory.TruncationScanLimit, Typed: true}}
	}
	return res, nil
}

// repoScopeCheckoutRepo is a repository whose manifest and module both name
// the workload checkout, and which keeps a refunds service in a directory
// without a build file of its own.
var repoScopeCheckoutRepo = fstest.MapFS{
	"go.mod":                     {Data: []byte("module example.invalid/checkout\n")},
	"deploy/k8s/deployment.yaml": {Data: []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: checkout\n  namespace: payments\n")},
	"services/refunds/main.go":   {Data: []byte("package main\n")},
}

// repoScopeDiscovery is the report discovery gives for dir of
// repoScopeCheckoutRepo against an environment that answers as runner does.
// It is built by Discover, so a golden holds only a report the command can
// produce. SetCommand stays empty, as Discover leaves it: the command line
// is the CLI's to spell, and cmd's own tests pin it.
func repoScopeDiscovery(dir string, runner repoScopeRunner) reposcope.DiscoveryReport {
	p, err := reposcope.PlanDiscovery(repoScopeCheckoutRepo, dir, nil)
	if err != nil {
		panic(err)
	}
	r, err := reposcope.Discover(context.Background(), runner, p, repoScopeHost)
	if err != nil {
		panic(err)
	}
	return *r
}

// repoScopeRow is one row of a record query for the workload checkout.
func repoScopeRow(fields string) string {
	return `{"k8s.namespace.name":"payments","k8s.workload.name":"checkout","service.name":"checkout",` + fields + `}`
}

const repoScopeInCluster = `"k8s.cluster.uid":"` + repoScopeCluster + `","clusterName":"prod-eu",`

// repoScopeDiscoveryMatch is one workload, read whole in spans and logs.
func repoScopeDiscoveryMatch() reposcope.DiscoveryReport {
	return repoScopeDiscovery(".", repoScopeRunner{
		"spans": {{rows: repoScopeRow(repoScopeInCluster + `"records":"12400","serviceCount":"1","services":["` + repoScopeSvc + `"],` +
			`"processGroupCount":"1","processGroups":["` + repoScopePG + `"]`)}},
		"logs": {{rows: repoScopeRow(repoScopeInCluster + `"records":"88000","processGroupCount":"1","processGroups":[["` + repoScopePG + `"]]`)}},
	})
}

// repoScopeDiscoverySampled is an environment where a day of spans scans
// past the cap. The first sampled retry answered with the workload; the
// capped scan had also seen it in a second namespace, which the sample did
// not return. The logs were read whole.
func repoScopeDiscoverySampled() reposcope.DiscoveryReport {
	staging := strings.Replace(repoScopeRow(`"records":"40"`), "payments", "staging", 1)
	return repoScopeDiscovery(".", repoScopeRunner{
		"spans": {
			{rows: repoScopeRow(`"records":"900"`) + "," + staging, capped: true},
			{rows: repoScopeRow(`"records":"17","serviceCount":"1","services":["` + repoScopeSvc + `"]`), sampled: true, scanned: 180000000},
		},
		"logs": {{rows: repoScopeRow(`"records":"88000"`)}},
	})
}

// repoScopeDiscoveryAmbiguous is discovery for services/refunds, a directory
// of the root unit, with what makes candidates hard to tell apart: one
// workload in two clusters, one of them under a uid a scope file cannot
// hold; a workload whose logs carry no cluster; and one whose logs carry
// only its process group, found by the lookup.
func repoScopeDiscoveryAmbiguous() reposcope.DiscoveryReport {
	refunds := func(namespace, fields string) string {
		return `{"k8s.namespace.name":"` + namespace + `","k8s.workload.name":"refunds","service.name":"refunds",` + fields + `}`
	}
	const otherPG = "PROCESS_GROUP-0000000000000002"
	return repoScopeDiscovery("services/refunds", repoScopeRunner{
		"spans": {{rows: strings.Join([]string{
			refunds("payments", repoScopeInCluster+`"records":"500","processGroupCount":"1","processGroups":["`+repoScopePG+`"]`),
			refunds("payments", `"k8s.cluster.uid":"legacy-cluster","records":"300"`),
			refunds("batch", `"k8s.cluster.uid":"66666666-7777-8888-9999-000000000000","clusterName":"prod-us","records":"200"`),
			refunds("tools", `"records":"100","processGroupCount":"1","processGroups":["`+otherPG+`"]`),
		}, ",")}},
		"logs": {
			{rows: refunds("payments", repoScopeInCluster+`"records":"7000"`) + "," +
				refunds("payments", `"k8s.cluster.uid":"legacy-cluster","records":"10"`) + "," + refunds("batch", `"records":"60"`)},
			{rows: `{"dt.entity.process_group":"` + otherPG + `","records":"5012"}`},
		},
	})
}

// repoScopeDiscoveryNone is an environment in which nothing carries a name
// of the repository: every query ran and returned no row.
func repoScopeDiscoveryNone() reposcope.DiscoveryReport {
	return repoScopeDiscovery(".", repoScopeRunner{})
}

// repoScopeDiscoveryPartial is a run in which the spans query ran out of
// time, the token could not read the service entities, and the environment
// has no process group table.
func repoScopeDiscoveryPartial() reposcope.DiscoveryReport {
	return repoScopeDiscovery(".", repoScopeRunner{
		"spans":                   {{err: context.DeadlineExceeded}},
		"dt.entity.service":       {{err: &sdkquery.QueryError{ErrorType: "NOT_AUTHORIZED_FOR_TABLE", Message: "not authorized"}}},
		"dt.entity.process_group": {{err: &sdkquery.QueryError{ErrorType: "UNKNOWN_DATA_OBJECT", Message: "dt.entity.process_group"}}},
	})
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
		"status-linked":       {repoScopeStatusLinked(), all},
		"status-segments":     {repoScopeStatus("services/ledger"), all},
		"status-unlinked":     {reposcope.Status{Code: reposcope.StatusNotARepository, Reason: "not inside a git repository"}, all},
		"discovery-match":     {repoScopeDiscoveryMatch(), all},
		"discovery-none":      {repoScopeDiscoveryNone(), []string{"json"}},
		"discovery-partial":   {repoScopeDiscoveryPartial(), []string{"json"}},
		"discovery-sampled":   {repoScopeDiscoverySampled(), []string{"json"}},
		"discovery-ambiguous": {repoScopeDiscoveryAmbiguous(), []string{"json"}},
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
