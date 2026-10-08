package reposcope

import (
	"fmt"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/dql"
)

// Render builds the filter for dataObject from e. The clauses are OR-ed: one
// per kind of binding the object is filtered by, every literal through
// dql.Quote. Entity ids always render as in(field, array(…)): a log record
// written by several processes carries its process groups as an array, and
// in() is documented to test membership in one where == is not. A service
// name list renders as in() too, a single name as field == "…". ok is false,
// with a reason, when the object is not scoped or e binds nothing it is
// filtered by.
func Render(e *Entry, dataObject string) (f dql.Filter, ok bool, reason string) {
	var obj *scopedObject
	for i := range scopedObjects {
		if scopedObjects[i].name == dataObject {
			obj = &scopedObjects[i]
		}
	}
	if obj == nil {
		return dql.Filter{}, false,
			fmt.Sprintf("repo scopes filter fetch %s; %s is not one of them", strings.Join(DataObjects(), " and fetch "), dataObject)
	}
	var clauses []string
	compare := func(field string, values []string, render func(string, []string) string) {
		if len(values) > 0 {
			clauses = append(clauses, render(field, values))
			f.Fields = append(f.Fields, field)
		}
	}
	compare(obj.idField, obj.ids(e), membership)
	compare(fieldServiceName, e.ServiceNames, comparison)
	for _, w := range e.Workloads {
		clauses = append(clauses, fmt.Sprintf("(%s == %s and %s == %s)",
			fieldNamespace, dql.Quote(w.Namespace), fieldWorkload, dql.Quote(w.Name)))
	}
	if len(e.Workloads) > 0 {
		f.Fields = append(f.Fields, fieldNamespace, fieldWorkload)
	}
	if len(clauses) == 0 {
		return dql.Filter{}, false, fmt.Sprintf("entry %q binds nothing fetch %s is filtered by (%s, %s, or %s with %s)",
			e.Name, dataObject, obj.idField, fieldServiceName, fieldNamespace, fieldWorkload)
	}
	f.Expr = strings.Join(clauses, " or ")
	return f, true, ""
}

// Filters renders e for every scoped data object, keyed by object, for
// dql.InsertFilter. An object e binds nothing for gets an empty Filter: the
// query is refused for it, and a subquery that fetches it still counts.
func Filters(e *Entry) map[string]dql.Filter {
	out := make(map[string]dql.Filter, len(scopedObjects))
	for _, o := range scopedObjects {
		f, _, _ := Render(e, o.name)
		out[o.name] = f
	}
	return out
}

func comparison(field string, values []string) string {
	if len(values) == 1 {
		return field + " == " + dql.Quote(values[0])
	}
	return membership(field, values)
}

// membership renders values as in(field, array(…)), which matches a field
// holding one of them or an array containing one.
func membership(field string, values []string) string {
	return fmt.Sprintf("in(%s, array(%s))", field, quotedList(values))
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = dql.Quote(v)
	}
	return strings.Join(quoted, ", ")
}
