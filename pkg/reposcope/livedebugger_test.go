package reposcope

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveDebuggerFilters(t *testing.T) {
	const svc2 = "SERVICE-0000000000000002"
	tests := []struct {
		name  string
		entry Entry
		want  []map[string][]string
	}{
		{
			name:  "workloads in one namespace are one set",
			entry: Entry{Name: "pair", Workloads: []Workload{{Namespace: "payments", Name: "ledger"}, {Namespace: "payments", Name: "checkout"}}},
			want: []map[string][]string{
				{"k8s.namespace.name": {"payments"}, "k8s.workload.name": {"checkout", "ledger"}},
			},
		},
		{
			name:  "workloads in two namespaces are a set each",
			entry: Entry{Name: "split", Workloads: []Workload{{Namespace: "web", Name: "checkout"}, {Namespace: "payments", Name: "checkout"}}},
			want: []map[string][]string{
				{"k8s.namespace.name": {"payments"}, "k8s.workload.name": {"checkout"}},
				{"k8s.namespace.name": {"web"}, "k8s.workload.name": {"checkout"}},
			},
		},
		{
			name: "every binding kind but service ids, in order",
			entry: Entry{
				Name:          "checkout",
				Services:      []string{svcID, svc2},
				ProcessGroups: []string{pgID},
				ServiceNames:  []string{"checkout-api", "checkout"},
				Workloads:     []Workload{{Namespace: "payments", Name: "checkout"}},
			},
			want: []map[string][]string{
				{"k8s.namespace.name": {"payments"}, "k8s.workload.name": {"checkout"}},
				{"service.name": {"checkout", "checkout-api"}},
				{"dt.entity.process_group": {pgID}},
			},
		},
		{
			name:  "service ids next to a workload leave the workload set",
			entry: Entry{Name: "checkout", Services: []string{svcID}, Workloads: []Workload{{Namespace: "payments", Name: "checkout"}}},
			want:  []map[string][]string{{"k8s.namespace.name": {"payments"}, "k8s.workload.name": {"checkout"}}},
		},
		{
			name:  "a value listed twice is sent once",
			entry: Entry{Name: "dup", ServiceNames: []string{"checkout", "checkout"}},
			want:  []map[string][]string{{"service.name": {"checkout"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sets, ok, reason := LiveDebuggerFilters(&tt.entry)
			require.True(t, ok, reason)
			assert.Empty(t, reason)
			assert.Equal(t, tt.want, sets)
		})
	}
}

// The sets are copies: sorting them must not reorder the entry, which Save
// writes back in the order the user gave, and changing them must not reach it.
func TestLiveDebuggerFilters_LeavesTheEntryAlone(t *testing.T) {
	e := Entry{Name: "checkout", ServiceNames: []string{"zeta", "alpha"}}
	sets, ok, _ := LiveDebuggerFilters(&e)
	require.True(t, ok)
	assert.Equal(t, []string{"zeta", "alpha"}, e.ServiceNames)

	sets[0]["service.name"][0] = "changed"
	assert.Equal(t, []string{"zeta", "alpha"}, e.ServiceNames)
}

// An entry that binds only service ids renders nothing, and says why. check
// refuses an entry that binds nothing, so the generic reason only guards a
// binding kind no label can carry.
func TestLiveDebuggerFilters_NothingToCarry(t *testing.T) {
	sets, ok, reason := LiveDebuggerFilters(&Entry{Name: "ids", Services: []string{svcID}})
	assert.False(t, ok)
	assert.Nil(t, sets)
	assert.Equal(t, `entry "ids" binds only service ids, which Live Debugger does not honour as a workspace label; bind --namespace/--workload or --process-group`, reason)

	sets, ok, reason = LiveDebuggerFilters(&Entry{Name: "empty"})
	assert.False(t, ok)
	assert.Nil(t, sets)
	assert.Equal(t, `entry "empty" binds nothing a Live Debugger filter can carry (service.name, k8s.namespace.name with k8s.workload.name, or dt.entity.process_group)`, reason)
}

// The default a workspace takes is the one workload set; an entry without
// one, or with one per namespace, has none.
func TestWorkloadFilterSet(t *testing.T) {
	render := func(e Entry) []map[string][]string {
		sets, ok, reason := LiveDebuggerFilters(&e)
		require.True(t, ok, reason)
		return sets
	}
	set, ok := WorkloadFilterSet(render(Entry{Name: "checkout", ServiceNames: []string{"checkout"},
		Workloads: []Workload{{Namespace: "payments", Name: "checkout"}}}))
	require.True(t, ok)
	assert.Equal(t, map[string][]string{"k8s.namespace.name": {"payments"}, "k8s.workload.name": {"checkout"}}, set)

	_, ok = WorkloadFilterSet(render(Entry{Name: "ledger", ServiceNames: []string{"ledger"}}))
	assert.False(t, ok, "no workload")
	_, ok = WorkloadFilterSet(render(Entry{Name: "split", Workloads: []Workload{{Namespace: "web", Name: "checkout"}, {Namespace: "payments", Name: "checkout"}}}))
	assert.False(t, ok, "a workload set per namespace")
}
