package reposcope

// Every Grail name this package puts into DQL. A renamed field is one edit
// here: rendering, discovery and its disclosure all read these.
//
// service.name and k8s.workload.name are stable fields in the Dynatrace
// semantic dictionary. Both entity id fields are deprecated there:
// dt.entity.service in favour of dt.smartscape.service, and
// dt.entity.process_group in favour of dt.process_group.id (Smartscape has no
// process group entity). They are what spans and logs carry today, as classic
// ids (SERVICE-…, PROCESS_GROUP-…) that smartscape queries do not return, so
// bindings use them until apiVersion v2 of the file migrates ids to their
// successors. A binding by workload or service name is unaffected by that
// migration.
const (
	fieldService      = "dt.entity.service"
	fieldProcessGroup = "dt.entity.process_group"
	fieldServiceName  = "service.name"
	fieldNamespace    = "k8s.namespace.name"
	fieldWorkload     = "k8s.workload.name"
	fieldEntityID     = "id"
	fieldEntityName   = "entity.name"

	// fieldNameLength is not a Grail field but one the entity queries add.
	// They sort shortest name first, so the closest matches are the ones
	// the row limit keeps, and DQL documents sort over a field, not over an
	// expression such as stringLength(entity.name).
	fieldNameLength = "nameLength"

	objectLogs          = "logs"
	objectSpans         = "spans"
	objectServices      = "dt.entity.service"
	objectProcessGroups = "dt.entity.process_group"
)

// scopedObject is a data object a repo scope filters, and the entity id its
// records carry: spans the service, logs the process group. Both carry
// service.name and the Kubernetes workload.
type scopedObject struct {
	name    string
	idField string
	ids     func(*Entry) []string
}

// scopedObjects is the contract between a scope file and a query: a data
// object is scoped only if it is listed here.
var scopedObjects = []scopedObject{
	{objectLogs, fieldProcessGroup, func(e *Entry) []string { return e.ProcessGroups }},
	{objectSpans, fieldService, func(e *Entry) []string { return e.Services }},
}

// DataObjects lists the data objects a repo scope filters.
func DataObjects() []string {
	names := make([]string, len(scopedObjects))
	for i, o := range scopedObjects {
		names[i] = o.name
	}
	return names
}

// MatchedFields are the fields discovery compares the repository's names
// against.
func MatchedFields() []string {
	return []string{fieldWorkload, fieldServiceName, fieldEntityName}
}
