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
			name: "a workload's cluster is not a label: the set is its namespace's",
			entry: Entry{Name: "clusters", Workloads: []Workload{
				{ClusterUID: clusterUID, Namespace: "payments", Name: "ledger"},
				{Namespace: "payments", Name: "checkout"},
				{ClusterUID: clusterUID, Namespace: "payments", Name: "checkout"},
			}},
			want: []map[string][]string{
				{"k8s.namespace.name": {"payments"}, "k8s.workload.name": {"checkout", "ledger"}},
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
	assert.Equal(t, `entry "ids" binds only service ids, which Live Debugger does not honour as a workspace label; it needs a workload or a process group to set filters`, reason)

	sets, ok, reason = LiveDebuggerFilters(&Entry{Name: "segments", Segments: []Segment{{ID: "Ab3dEf9hIjk"}}})
	assert.False(t, ok)
	assert.Nil(t, sets)
	assert.Equal(t, `entry "segments" references only filter segments, which narrow queries; a Live Debugger workspace is filtered by labels, so it needs a workload or a process group to set filters`, reason)

	// Segments beside the service ids are named too: neither is a label.
	_, ok, reason = LiveDebuggerFilters(&Entry{Name: "both", Services: []string{svcID}, Segments: []Segment{{ID: "Ab3dEf9hIjk"}}})
	assert.False(t, ok)
	assert.Equal(t, `entry "both" binds service ids, which Live Debugger does not honour as a workspace label, and references filter segments, which narrow queries only; it needs a workload or a process group to set filters`, reason)

	sets, ok, reason = LiveDebuggerFilters(&Entry{Name: "empty"})
	assert.False(t, ok)
	assert.Nil(t, sets)
	assert.Equal(t, `entry "empty" binds nothing a Live Debugger filter can carry (service.name, k8s.namespace.name with k8s.workload.name, or dt.entity.process_group)`, reason)
}

func TestEntry_BindsCluster(t *testing.T) {
	assert.True(t, (&Entry{Workloads: []Workload{{Namespace: "payments", Name: "ledger"}, {ClusterUID: clusterUID, Namespace: "payments", Name: "checkout"}}}).BindsCluster())
	assert.False(t, (&Entry{ServiceNames: []string{"checkout"}, Workloads: []Workload{{Namespace: "payments", Name: "checkout"}}}).BindsCluster())
}
