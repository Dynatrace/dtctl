package reposcope

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/dql"
)

func TestRender(t *testing.T) {
	const pg2 = "PROCESS_GROUP-0000000000000002"
	full := Entry{
		Name:          "checkout",
		Services:      []string{svcID},
		ProcessGroups: []string{pgID},
		ServiceNames:  []string{"checkout"},
		Workloads:     []Workload{{Namespace: "payments", Name: "checkout"}},
	}
	tests := []struct {
		name   string
		entry  Entry
		object string
		expr   string
		fields []string
		reason string
	}{
		{
			name: "spans, every binding", entry: full, object: "spans",
			expr:   `in(dt.entity.service, array("SERVICE-0123456789ABCDEF")) or service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout")`,
			fields: []string{"dt.entity.service", "service.name", "k8s.namespace.name", "k8s.workload.name"},
		},
		{
			name: "logs, every binding", entry: full, object: "logs",
			expr:   `in(dt.entity.process_group, array("PROCESS_GROUP-FEDCBA9876543210")) or service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout")`,
			fields: []string{"dt.entity.process_group", "service.name", "k8s.namespace.name", "k8s.workload.name"},
		},
		{
			name: "several values render as in()", object: "logs",
			entry:  Entry{Name: "pgs", ProcessGroups: []string{pgID, pg2}},
			expr:   `in(dt.entity.process_group, array("PROCESS_GROUP-FEDCBA9876543210", "PROCESS_GROUP-0000000000000002"))`,
			fields: []string{"dt.entity.process_group"},
		},
		{
			name: "several workloads are OR-ed pairs", object: "spans",
			entry:  Entry{Name: "pair", Workloads: []Workload{{Namespace: "payments", Name: "checkout"}, {Namespace: "payments", Name: "ledger"}}},
			expr:   `(k8s.namespace.name == "payments" and k8s.workload.name == "checkout") or (k8s.namespace.name == "payments" and k8s.workload.name == "ledger")`,
			fields: []string{"k8s.namespace.name", "k8s.workload.name"},
		},
		{
			// check refuses these characters; Render still quotes them, as
			// the second line of defence.
			name: "literals are quoted", object: "spans",
			entry:  Entry{Name: "odd", ServiceNames: []string{`a"b\c`}},
			expr:   `service.name == "a\"b\\c"`,
			fields: []string{"service.name"},
		},
		{
			name: "logs without a binding they are filtered by", object: "logs",
			entry:  Entry{Name: "checkout", Services: []string{svcID}},
			reason: `entry "checkout" binds nothing fetch logs is filtered by (dt.entity.process_group, service.name, or k8s.namespace.name with k8s.workload.name)`,
		},
		{
			name: "spans without a binding they are filtered by", object: "spans",
			entry:  Entry{Name: "batch", ProcessGroups: []string{pgID}},
			reason: `entry "batch" binds nothing fetch spans is filtered by (dt.entity.service, service.name, or k8s.namespace.name with k8s.workload.name)`,
		},
		{
			name: "an object outside the table", object: "events", entry: full,
			reason: "repo scopes filter fetch logs and fetch spans; events is not one of them",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok, reason := Render(&tt.entry, tt.object)
			assert.Equal(t, tt.reason, reason)
			if tt.reason != "" {
				assert.False(t, ok)
				assert.Equal(t, dql.Filter{}, f)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, dql.Filter{Expr: tt.expr, Fields: tt.fields}, f)
		})
	}
}

// Filters feeds dql.InsertFilter directly: what Render produces is what the
// query sends.
func TestFilters_FeedInsertFilter(t *testing.T) {
	e := Entry{
		Name:          "checkout",
		ProcessGroups: []string{pgID},
		ServiceNames:  []string{"checkout"},
		Workloads:     []Workload{{Namespace: "payments", Name: "checkout"}},
	}
	got := dql.InsertFilter(`fetch logs | filter loglevel == "ERROR" | limit 20`, Filters(&e))
	assert.Equal(t, dql.Applied, got.Outcome)
	assert.Equal(t, `fetch logs | filter (in(dt.entity.process_group, array("PROCESS_GROUP-FEDCBA9876543210")) or service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout"))`+"\n"+`| filter loglevel == "ERROR" | limit 20`, got.Query)
}

func TestFilters(t *testing.T) {
	servicesOnly := Entry{Name: "checkout", Services: []string{svcID}}
	filters := Filters(&servicesOnly)
	assert.Equal(t, []string{"logs", "spans"}, DataObjects())
	require.Len(t, filters, len(DataObjects()), "every scoped object is named, filtered or not")
	assert.Empty(t, filters["logs"].Expr, "logs carry no service id")
	assert.Equal(t, `in(dt.entity.service, array("SERVICE-0123456789ABCDEF"))`, filters["spans"].Expr)
	assert.Equal(t, dql.Subquery, dql.InsertFilter("fetch spans | append [fetch logs]", filters).Outcome,
		"an unfiltered logs subquery still counts as one")
}

func TestRenderMultipleServiceNames(t *testing.T) {
	for _, object := range DataObjects() {
		filter, ok, reason := Render(&Entry{Name: "checkout", ServiceNames: []string{"checkout", "ledger"}}, object)
		require.True(t, ok, reason)
		assert.Equal(t, `in(service.name, array("checkout", "ledger"))`, filter.Expr)
		assert.Equal(t, []string{"service.name"}, filter.Fields)
	}
}
