package reposcope

import (
	"cmp"
	"fmt"
	"slices"
)

// LiveDebuggerFilters renders e as Live Debugger workspace filter sets, each a
// map from label to values, the shape `--filters` parses to. A workspace is
// filtered by labels its processes carry, under the same names as the record
// fields a query is filtered by:
//
//	one set per namespace: k8s.namespace.name, k8s.workload.name,
//	                       and k8s.cluster.uid where a workload binds one
//	service names:         service.name
//	process group ids:     dt.entity.process_group
//
// Within a set every label must match and any of its values may, so a
// namespace with its workloads is one set: the pair a workload binding is.
// Workloads bound to a cluster are a set of their own per cluster, carrying
// its uid as a third label, so the set selects what the binding does. The
// sets are alternatives, as the clauses Render OR-es are, and come in a
// fixed order: the workload sets first, by namespace and then cluster, then
// the service names and the process group ids. Both rules are Live
// Debugger's and verified against an environment, as are the namespace,
// workload and process group labels; service.name and k8s.cluster.uid are
// not yet. Service ids are left out: a workspace does not honour
// dt.entity.service as a label, so a set of them selects nothing.
//
// The cluster label is sent unverified because the alternative is worse: a
// set without it would place breakpoints in the same workload of a cluster
// the entry leaves out. If a workspace turns out not to carry the label, a
// set with it selects nothing, which shows.
//
// The filter segments an entry references are not rendered: a segment
// narrows a query, and a workspace has no place to take one.
//
// Values are sorted copies, never quoted: a filter set is a GraphQL variable,
// not DQL. ok is false, with a reason, when e binds nothing a label can
// carry: only service ids, or a binding kind added later.
func LiveDebuggerFilters(e *Entry) (sets []map[string][]string, ok bool, reason string) {
	type scope struct{ namespace, cluster string }
	names := map[scope][]string{}
	for _, w := range e.Workloads {
		in := scope{w.Namespace, w.ClusterUID}
		names[in] = append(names[in], w.Name)
	}
	scopes := make([]scope, 0, len(names))
	for in := range names {
		scopes = append(scopes, in)
	}
	slices.SortFunc(scopes, func(a, b scope) int {
		return cmp.Or(cmp.Compare(a.namespace, b.namespace), cmp.Compare(a.cluster, b.cluster))
	})
	for _, in := range scopes {
		set := map[string][]string{
			fieldNamespace: {in.namespace},
			fieldWorkload:  sortedCopy(names[in]),
		}
		if in.cluster != "" {
			set[fieldClusterUID] = []string{in.cluster}
		}
		sets = append(sets, set)
	}
	for _, label := range []struct {
		field  string
		values []string
	}{
		{fieldServiceName, e.ServiceNames},
		{fieldProcessGroup, e.ProcessGroups},
	} {
		if len(label.values) > 0 {
			sets = append(sets, map[string][]string{label.field: sortedCopy(label.values)})
		}
	}
	switch {
	case len(sets) == 0 && !e.HasBindings() && len(e.Segments) > 0:
		return nil, false, fmt.Sprintf("entry %q references only filter segments, which narrow queries; a Live Debugger workspace is filtered by labels, so bind --namespace/--workload or --process-group", e.Name)
	case len(sets) == 0 && len(e.Services) > 0:
		return nil, false, fmt.Sprintf("entry %q binds only service ids, which Live Debugger does not honour as a workspace label; bind --namespace/--workload or --process-group", e.Name)
	case len(sets) == 0:
		return nil, false, fmt.Sprintf("entry %q binds nothing a Live Debugger filter can carry (%s, %s with %s, or %s)",
			e.Name, fieldServiceName, fieldNamespace, fieldWorkload, fieldProcessGroup)
	}
	return sets, true, ""
}

// WorkloadFilterSet is the one namespace-and-workload set among sets, the
// rendering of LiveDebuggerFilters: the set a workspace can take from an
// entry without the caller asking for the rest. ok is false when sets holds
// no such set, because the entry binds no workload, or several, because its
// workloads run in more than one namespace or cluster.
func WorkloadFilterSet(sets []map[string][]string) (set map[string][]string, ok bool) {
	var found []map[string][]string
	for _, s := range sets {
		if _, workload := s[fieldWorkload]; workload {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		return nil, false
	}
	return found[0], true
}

// sortedCopy returns values sorted, without duplicates, leaving the entry's
// own slice as it was.
func sortedCopy(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return slices.Compact(out)
}
