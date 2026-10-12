package reposcope

import (
	"fmt"
	"slices"
)

// LiveDebuggerFilters renders e as Live Debugger workspace filter sets, each a
// map from label to values, the shape `--filters` parses to. A workspace is
// filtered by labels its processes carry, under the same names as the record
// fields a query is filtered by:
//
//	one set per namespace: k8s.namespace.name, k8s.workload.name
//	service names:         service.name
//	process group ids:     dt.entity.process_group
//
// Within a set every label must match and any of its values may, so a
// namespace with its workloads is one set: the pair a workload binding is.
// The sets are alternatives, as the clauses Render OR-es are, and come in a
// fixed order: the workload sets first, by namespace, then the service names
// and the process group ids. Both rules are Live Debugger's and verified
// against an environment, as are the namespace, workload and process group
// labels; service.name is not yet. Service ids are left out: a workspace does
// not honour dt.entity.service as a label, so a set of them selects nothing.
//
// Values are sorted copies, never quoted: a filter set is a GraphQL variable,
// not DQL. ok is false, with a reason, when e binds nothing a label can
// carry: only service ids, or a binding kind added later.
func LiveDebuggerFilters(e *Entry) (sets []map[string][]string, ok bool, reason string) {
	byNamespace := map[string][]string{}
	for _, w := range e.Workloads {
		byNamespace[w.Namespace] = append(byNamespace[w.Namespace], w.Name)
	}
	namespaces := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		namespaces = append(namespaces, ns)
	}
	slices.Sort(namespaces)
	for _, ns := range namespaces {
		sets = append(sets, map[string][]string{
			fieldNamespace: {ns},
			fieldWorkload:  sortedCopy(byNamespace[ns]),
		})
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
// workloads run in more than one namespace.
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
