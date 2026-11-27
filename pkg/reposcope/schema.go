package reposcope

import (
	"fmt"
	"maps"
	"net"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// FileName is the scope file's name, found in the repository root.
const FileName = ".dtctl-repo-scope.yaml"

// apiVersion is the only document version this build reads and writes. An
// empty apiVersion means the same thing.
const apiVersion = "v1"

// File is the on-disk document. Environments is keyed by environment host:
// urls.Host of a context's environment URL, without the dot that ends a
// fully qualified name, which urls.Host keeps and no key of the file may
// carry. Resolve looks up the host it is given as it is, so a caller trims
// the dot. Context names are local to a machine, hosts are not, which is the
// same reasoning the config contract applies to token bindings.
type File struct {
	APIVersion   string
	Environments map[string][]Entry

	// unknown is every key the document holds that File does not model, as
	// "name" on line N. Save refuses while it is set: writing would drop them.
	unknown []string
}

// Entry is one named binding set. A query run from Path (or any directory
// under it) is scoped to these bindings; an Entry without Path covers the
// whole repository. A Path that is a Gradle deployable's module also covers
// the modules it depends on (see Resolve).
//
// The tags are the spelling of a result, camelCase in every format a command
// prints. The file spells its keys in kebab-case, and is read and written
// through its own types (see fileEntry).
//
// The bindings are listed, here and wherever they are shown, in the order
// they are preferred in: the Kubernetes workload, the service name, then the
// two entity ids, whose fields are deprecated, the process group before the
// service.
type Entry struct {
	Name          string     `json:"name" yaml:"name"`
	Path          string     `json:"path,omitempty" yaml:"path,omitempty"`
	Workloads     []Workload `json:"workloads,omitempty" yaml:"workloads,omitempty"`
	ServiceNames  []string   `json:"serviceNames,omitempty" yaml:"serviceNames,omitempty"`
	ProcessGroups []string   `json:"processGroups,omitempty" yaml:"processGroups,omitempty"`
	Services      []string   `json:"services,omitempty" yaml:"services,omitempty"`
	// Segments are existing filter segments a query under the entry sends
	// beside its text. Unlike the bindings they are not alternatives: every
	// one must hold, together with the filter the bindings render. An entry
	// may hold nothing else.
	Segments []Segment `json:"segments,omitempty" yaml:"segments,omitempty"`
	// Annotations are notes on the entry, for people and for other tools.
	// dtctl checks their form, keeps them when it rewrites the file and
	// never reads them: no annotation changes what an entry covers or
	// selects, in this dtctl or a later one. It is the one place in an
	// entry where a key this dtctl does not know may be written.
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// Segment references one filter segment of the environment by its id, with
// the values of the variables it declares. A name is never accepted: it can
// be changed, or given to a second segment, under a committed file.
type Segment struct {
	ID        string              `json:"id" yaml:"id"`
	Variables map[string][]string `json:"variables,omitempty" yaml:"variables,omitempty"`
}

// VariableNames is the names of the segment's variables, sorted.
func (s Segment) VariableNames() []string {
	names := make([]string, 0, len(s.Variables))
	for name := range s.Variables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// String renders the reference on one line, the spelling dtctl's commands
// take a segment in: "id", or "id?name=value,value&name=value" with the
// variables by name. A valid segment reads back from it unchanged:
// validation refuses a value holding one of the separators.
func (s Segment) String() string {
	var vars []string
	for _, name := range s.VariableNames() {
		vars = append(vars, name+"="+strings.Join(s.Variables[name], ","))
	}
	if len(vars) == 0 {
		return s.ID
	}
	return s.ID + "?" + strings.Join(vars, "&")
}

// Workload binds one Kubernetes workload. Namespace is required: a workload
// name alone is not unique across namespaces. ClusterUID is optional, and
// narrows the binding to one cluster: the same namespace and workload name
// can exist in several, and records without Kubernetes enrichment carry no
// cluster at all. It is the cluster's k8s.cluster.uid, never its name, which
// is a label somebody chose and can change.
type Workload struct {
	ClusterUID string `json:"clusterUid,omitempty" yaml:"clusterUid,omitempty"`
	Namespace  string `json:"namespace" yaml:"namespace"`
	Name       string `json:"name" yaml:"name"`
}

// String renders the workload as namespace/name, the way kubectl users read
// it, followed by the cluster when the binding names one.
func (w Workload) String() string {
	if w.ClusterUID != "" {
		return w.Namespace + "/" + w.Name + " in cluster " + w.ClusterUID
	}
	return w.Namespace + "/" + w.Name
}

// The literal patterns. Each value that can reach a query is held to one of
// these before it is ever quoted.
var (
	entryNameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	serviceIDRe    = regexp.MustCompile(`^SERVICE-[0-9A-F]{16}$`)
	processGroupRe = regexp.MustCompile(`^PROCESS_GROUP-[0-9A-F]{16}$`)
	dnsLabelRe     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	dnsSubdomainRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	// clusterUIDRe is a Kubernetes object UID as the API server writes one:
	// k8s.cluster.uid is the UID of the cluster's kube-system namespace.
	clusterUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// A segment id and a segment variable's name travel in the request
	// body, not in the query text. They are held to a pattern all the same:
	// a committed file names existing objects, it does not carry free text.
	segmentIDRe       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	segmentVariableRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{0,127}$`)
)

// maxSegments is how many filter segments one query takes, the platform's
// limit, and so how many an entry may reference.
const maxSegments = 10

// The sizes a file may have. They are part of the format: a reader may rely
// on them, and a file beyond one is refused whole, never read in part.
const (
	maxEnvironments = 64
	maxEntries      = 1000 // per environment
	maxListItems    = 1000 // per list of an entry, a workload or a segment
)

// maxSegmentValueRunes bounds one value of a segment variable.
const maxSegmentValueRunes = 255

// maxDNSSubdomain bounds a DNS name: an environment host, and a Kubernetes
// DNS subdomain name. A namespace is a DNS label; a workload (a Deployment,
// StatefulSet, …) is a DNS subdomain, so "checkout.v2" is a valid one.
const maxDNSSubdomain = 253

// maxKeyBytes bounds a key of the file. It is the longest any key the format
// reads can be: an environment's host, and an annotation's key.
const maxKeyBytes = max(maxDNSSubdomain, maxAnnotationKeyBytes)

// maxMessageBytes bounds what a file can make dtctl say about it: a refusal
// quotes the value it refuses, and a value can be as long as the file.
const maxMessageBytes = 2048

// maxServiceNameRunes bounds a service name; Dynatrace stores longer ones
// truncated, so a longer binding could never match.
const maxServiceNameRunes = 255

// check validates the document and every literal that could reach a query.
// The file is untrusted, since a clone brings it along, so a literal that
// fails its pattern is an error naming the host, the entry and the field,
// never something to escape and hope. Beyond the patterns above: host keys
// must already be canonical, names and paths are unique per host (so at most
// one entry omits Path), every environment has an entry, every entry binds
// something, and nothing is larger than the format allows.
//
// Paths are unique regardless of case, on every OS, and written in one
// Unicode form. Resolve folds case on macOS and Windows, where two paths
// differing only in case are one directory; a committed file has to mean the
// same thing on all of them, so it is valid everywhere or nowhere.
//
// The error's Source is FileName; Load and Save replace it with the path.
func (f *File) check() *InvalidError {
	if f.APIVersion != "" && f.APIVersion != apiVersion {
		return invalid(fmt.Sprintf("apiVersion %q is not supported (this dtctl reads %q)", f.APIVersion, apiVersion),
			"upgrade dtctl, or set apiVersion: "+apiVersion)
	}
	if n := len(f.Environments); n > maxEnvironments {
		return invalid(fmt.Sprintf("holds %d environments; a file takes at most %d", n, maxEnvironments))
	}
	for _, host := range f.Hosts() {
		if err := f.validateHost(host); err != nil {
			return err
		}
	}
	return nil
}

// validateHost checks one environment's key and entries.
func (f *File) validateHost(host string) *InvalidError {
	if canonical := canonicalHost(host); canonical != host {
		// Nothing to advise when what is left is no host either.
		if len(canonical) > maxDNSSubdomain || !dnsSubdomainRe.MatchString(canonical) {
			return invalid(fmt.Sprintf("environment %q is not a host name", host))
		}
		msg := fmt.Sprintf("environment %q must be written as the bare host %q", host, canonical)
		if _, taken := f.Environments[canonical]; taken {
			msg = fmt.Sprintf("environments %q and %q name the same host", host, canonical)
		}
		return invalid(msg, "key each environment by its lowercase host, e.g. abc12345.apps.dynatrace.com")
	}
	if len(host) > maxDNSSubdomain || !dnsSubdomainRe.MatchString(host) {
		return invalid(fmt.Sprintf("environment %q is not a host name", host))
	}
	switch n := len(f.Environments[host]); {
	case n == 0:
		return invalid(fmt.Sprintf("environment %q has no entries", host), "give it an entry, or delete the line")
	case n > maxEntries:
		return invalid(fmt.Sprintf("environment %q holds %d entries; an environment takes at most %d", host, n, maxEntries))
	}
	names := map[string]bool{}
	paths := map[string]Entry{} // by folded path
	for i, e := range f.Environments[host] {
		where := fmt.Sprintf("environments[%s][%d]", host, i)
		if e.Name != "" {
			where = fmt.Sprintf("environments[%s] entry %q", host, e.Name)
		}
		if err := e.validate(); err != nil {
			err.Msg = where + ": " + err.Msg
			return err
		}
		if names[e.Name] {
			return invalid(fmt.Sprintf("%s: the name is used twice", where),
				"give every entry under one environment its own name")
		}
		names[e.Name] = true
		if other, taken := paths[foldPath(e.Path, true)]; taken {
			covered := fmt.Sprintf("%q", other.Path)
			switch {
			case e.Path == "":
				covered = "the whole repository"
			case other.Path != e.Path:
				covered += fmt.Sprintf(", the same directory as %q on macOS and Windows", e.Path)
			}
			return invalid(fmt.Sprintf("%s: entry %q already covers %s", where, other.Name, covered),
				"give each entry its own path; at most one entry may omit it")
		}
		paths[foldPath(e.Path, true)] = e
	}
	return nil
}

// validate checks one entry's own fields.
func (e *Entry) validate() *InvalidError {
	if !entryNameRe.MatchString(e.Name) {
		return invalid(fmt.Sprintf("name %q must be lowercase letters, digits and dashes (at most 63)", e.Name))
	}
	if err := validatePath(e.Path); err != nil {
		return err
	}
	for _, list := range []struct {
		key string
		n   int
	}{
		{"workloads", len(e.Workloads)}, {"service-names", len(e.ServiceNames)},
		{"process-groups", len(e.ProcessGroups)}, {"services", len(e.Services)},
	} {
		if list.n > maxListItems {
			return tooLong(list.key, list.n, "items", "a list")
		}
	}
	for i, id := range e.Services {
		if !serviceIDRe.MatchString(id) {
			return invalid(fmt.Sprintf("services[%d] %q is not a service id (SERVICE- and 16 hex digits)", i, id))
		}
	}
	for i, id := range e.ProcessGroups {
		if !processGroupRe.MatchString(id) {
			return invalid(fmt.Sprintf("process-groups[%d] %q is not a process group id (PROCESS_GROUP- and 16 hex digits)", i, id))
		}
	}
	for i, name := range e.ServiceNames {
		if problem := serviceNameProblem(name); problem != "" {
			return invalid(fmt.Sprintf("service-names[%d] %q %s", i, name, problem))
		}
	}
	// A value given twice selects nothing more, so it is a slip of the hand
	// or of a merge, and the file is where to say so.
	for _, list := range []struct {
		key    string
		values []string
	}{{"services", e.Services}, {"process-groups", e.ProcessGroups}, {"service-names", e.ServiceNames}} {
		if i := repeated(list.values); i >= 0 {
			return invalid(fmt.Sprintf("%s[%d] %q is listed twice", list.key, i, list.values[i]))
		}
	}
	workloads := map[Workload]bool{}
	// bound is, by namespace and name, whether a workload is listed with a
	// cluster (true) or without one.
	bound := map[Workload]bool{}
	for i, w := range e.Workloads {
		// A workload's own fields first: the two checks after them name the
		// workload, and a message quotes no value that has not passed its
		// pattern.
		if w.ClusterUID != "" && !clusterUIDRe.MatchString(w.ClusterUID) {
			return invalid(fmt.Sprintf("workloads[%d] cluster-uid %q is not a Kubernetes cluster uid (a lowercase UUID, the value of %s)", i, w.ClusterUID, fieldClusterUID))
		}
		if !dnsLabelRe.MatchString(w.Namespace) {
			return invalid(fmt.Sprintf("workloads[%d] namespace %q is not a Kubernetes namespace name", i, w.Namespace))
		}
		if len(w.Name) > maxDNSSubdomain || !dnsSubdomainRe.MatchString(w.Name) {
			return invalid(fmt.Sprintf("workloads[%d] name %q is not a Kubernetes workload name", i, w.Name))
		}
		if workloads[w] {
			return invalid(fmt.Sprintf("workloads[%d] %s is listed twice", i, w))
		}
		workloads[w] = true
		// The binding without a cluster already selects the workload in every
		// cluster: one with a cluster beside it narrows nothing, while the
		// entry would say that it binds a cluster.
		anywhere := Workload{Namespace: w.Namespace, Name: w.Name}
		if clustered, listed := bound[anywhere]; listed && clustered != (w.ClusterUID != "") {
			return invalid(fmt.Sprintf("workloads[%d] %s is listed with and without a cluster; without one it selects the workload in every cluster", i, w))
		}
		bound[anywhere] = w.ClusterUID != ""
	}
	if err := e.validateSegments(); err != nil {
		return err
	}
	if err := e.validateAnnotations(); err != nil {
		return err
	}
	if !e.HasBindings() && len(e.Segments) == 0 {
		return invalid("binds nothing",
			"add at least one of workloads, service-names, process-groups, services or segments")
	}
	return nil
}

// Problem says why the entry's own fields could not be saved, or returns "".
// It is the check Save applies to each entry of a file, for a caller that
// proposes an entry and must not propose one that would be refused.
func (e Entry) Problem() string {
	if err := e.validate(); err != nil {
		return err.Msg
	}
	return ""
}

// The bounds of an entry's annotations. A key has the form of a Kubernetes
// annotation key without its upper case, and a value is one line of text.
const (
	maxAnnotations         = 64
	maxAnnotationKeyBytes  = 253
	maxAnnotationValueSize = 1024
)

var annotationKeyRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._/-]*[a-z0-9])?$`)

// validateAnnotations checks the form of the entry's annotations, which is
// all dtctl knows about them.
func (e *Entry) validateAnnotations() *InvalidError {
	if n := len(e.Annotations); n > maxAnnotations {
		return invalid(fmt.Sprintf("has %d annotations; an entry takes at most %d", n, maxAnnotations))
	}
	for _, key := range slices.Sorted(maps.Keys(e.Annotations)) {
		value := e.Annotations[key]
		switch {
		case len(key) > maxAnnotationKeyBytes || !annotationKeyRe.MatchString(key):
			return invalid(fmt.Sprintf("annotation %q is not an annotation key (lowercase letters, digits, and . _ - / inside; at most %d)", key, maxAnnotationKeyBytes))
		case len(value) > maxAnnotationValueSize:
			return invalid(fmt.Sprintf("annotation %q is longer than %d bytes", key, maxAnnotationValueSize))
		case !utf8.ValidString(value) || !printable(value):
			return invalid(fmt.Sprintf("annotation %q must not contain control or non-printing characters", key))
		}
	}
	return nil
}

// validateSegments checks the segments the entry references.
func (e *Entry) validateSegments() *InvalidError {
	if len(e.Segments) > maxSegments {
		return invalid(fmt.Sprintf("lists %d segments; a query takes at most %d", len(e.Segments), maxSegments))
	}
	seen := map[string]bool{}
	for i, s := range e.Segments {
		if !segmentIDRe.MatchString(s.ID) {
			return invalid(fmt.Sprintf("segments[%d] id %q is not a filter segment id (letters, digits, - and _; a name is not accepted)", i, s.ID),
				"reference the segment by its id, which the environment lists with its segments")
		}
		if seen[s.ID] {
			return invalid(fmt.Sprintf("segments[%d] id %q is listed twice", i, s.ID))
		}
		seen[s.ID] = true
		if n := len(s.Variables); n > maxListItems {
			return tooLong(fmt.Sprintf("segments[%d]", i), n, "variables", "a segment")
		}
		for _, name := range s.VariableNames() {
			values := s.Variables[name]
			if n := len(values); n > maxListItems {
				return tooLong(fmt.Sprintf("segments[%d] variable %q", i, name), n, "values", "a variable")
			}
			if !segmentVariableRe.MatchString(name) {
				return invalid(fmt.Sprintf("segments[%d] variable %q is not a variable name (letters, digits, _ and .)", i, name))
			}
			if len(values) == 0 {
				return invalid(fmt.Sprintf("segments[%d] variable %q has no values", i, name))
			}
			for _, v := range values {
				if problem := segmentValueProblem(v); problem != "" {
					return invalid(fmt.Sprintf("segments[%d] variable %q value %q %s", i, name, v, problem))
				}
			}
			if twice := repeated(values); twice >= 0 {
				return invalid(fmt.Sprintf("segments[%d] variable %q value %q is listed twice", i, name, values[twice]))
			}
		}
	}
	return nil
}

// repeated is the index of the first value that is already in values before
// it, or -1.
func repeated(values []string) int {
	seen := make(map[string]bool, len(values))
	for i, v := range values {
		if seen[v] {
			return i
		}
		seen[v] = true
	}
	return -1
}

// tooLong refuses a list beyond maxListItems.
func tooLong(what string, n int, things, holder string) *InvalidError {
	return invalid(fmt.Sprintf("%s holds %d %s; %s takes at most %d", what, n, things, holder, maxListItems))
}

// segmentValueProblem explains why v cannot be a segment variable's value,
// or returns "".
func segmentValueProblem(v string) string {
	n := utf8.RuneCountInString(v)
	switch {
	case !utf8.ValidString(v):
		return "is not valid UTF-8"
	case n == 0:
		return "is empty"
	case n > maxSegmentValueRunes:
		return fmt.Sprintf("is longer than %d characters", maxSegmentValueRunes)
	case strings.ContainsAny(v, ",&="):
		// String must read back as the reference it renders.
		return "must not contain , & or =, which separate the values where a segment is written as one reference (id?name=value,value)"
	case strings.TrimSpace(v) != v:
		// For the same reason: the space is dropped where that spelling is
		// read.
		return "must not start or end with a space"
	}
	if !printable(v) {
		return "must not contain control or non-printing characters"
	}
	return ""
}

// HasBindings reports whether the entry binds anything a filter is rendered
// from. An entry without bindings references segments only: it inserts no
// filter, and its segments are all it applies.
func (e *Entry) HasBindings() bool {
	return len(e.Services)+len(e.ProcessGroups)+len(e.ServiceNames)+len(e.Workloads) > 0
}

// BindsCluster reports whether a workload of the entry is bound to a
// cluster.
func (e *Entry) BindsCluster() bool {
	return slices.ContainsFunc(e.Workloads, func(w Workload) bool { return w.ClusterUID != "" })
}

// validatePath holds Path to a clean, relative, slash-separated directory
// inside the repository, in text that shows what it is: the path is printed
// in every answer about the entry, and a committed file is read in review.
func validatePath(p string) *InvalidError {
	if p == "" {
		return nil
	}
	clean := path.Clean(p)
	switch {
	case !utf8.ValidString(p):
		return invalid(fmt.Sprintf("path %q is not valid UTF-8", p))
	case !printable(p):
		return invalid(fmt.Sprintf("path %q must not contain control or non-printing characters", p))
	case strings.TrimSpace(p) != p:
		return invalid(fmt.Sprintf("path %q must not start or end with a space", p))
	case strings.Contains(p, `\`):
		return invalid(fmt.Sprintf("path %q must use forward slashes", p))
	case path.IsAbs(p) || driveRe.MatchString(p):
		return invalid(fmt.Sprintf("path %q must be relative to the repository root", p))
	case p == ".":
		return invalid(`path "." is the repository root`, "omit path for an entry that covers the whole repository")
	case clean == ".." || strings.HasPrefix(clean, "../"):
		return invalid(fmt.Sprintf("path %q leaves the repository", p))
	case clean != p:
		return invalid(fmt.Sprintf("path %q is not clean (write it as %q)", p, clean))
	case !norm.NFC.IsNormalString(p):
		// Two spellings that look the same in every editor: say which is
		// which in a form that shows it.
		return invalid(fmt.Sprintf("path %+q is not in Unicode NFC, the form a path is compared in (write it as %+q)", p, norm.NFC.String(p)))
	}
	return nil
}

// driveRe is a Windows drive at the start of a path. path.IsAbs knows only
// the POSIX form, and "C:/work" is no more inside the repository than
// "/work".
var driveRe = regexp.MustCompile(`^[A-Za-z]:`)

// printable reports whether every character of s is one a terminal shows as
// itself: no control character, escape, or invisible separator.
func printable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}

// serviceNameProblem explains why name cannot be a service-names binding, or
// returns "".
func serviceNameProblem(name string) string {
	n := utf8.RuneCountInString(name)
	switch {
	case !utf8.ValidString(name):
		return "is not valid UTF-8"
	case n == 0:
		return "is empty"
	case n > maxServiceNameRunes:
		return fmt.Sprintf("is longer than %d characters", maxServiceNameRunes)
	case strings.ContainsAny(name, `"\`):
		return `must not contain " or \`
	case strings.TrimSpace(name) != name:
		// A record's service.name is compared whole, so a padded name is
		// either a slip or a name no other spelling of the binding (a flag,
		// a Live Debugger filter) can carry.
		return "must not start or end with a space"
	}
	if !printable(name) {
		return "must not contain control or non-printing characters"
	}
	return ""
}

// canonicalHost is the form a host key must already have: lowercase, with no
// scheme, user, path, port or trailing dot.
func canonicalHost(key string) string {
	h := strings.ToLower(strings.TrimSpace(key))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndex(h, "@"); i >= 0 {
		h = h[i+1:]
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(h, ".")
}

func invalid(msg string, suggestions ...string) *InvalidError {
	return &InvalidError{Source: FileName, Msg: msg, Suggestions: suggestions}
}

// Upsert replaces the entry named e.Name under host, or appends it. It never
// merges: the entry passed in is the whole entry.
func (f *File) Upsert(host string, e Entry) {
	if f.Environments == nil {
		f.Environments = map[string][]Entry{}
	}
	entries := f.Environments[host]
	for i := range entries {
		if entries[i].Name == e.Name {
			entries[i] = e
			return
		}
	}
	f.Environments[host] = append(entries, e)
}

// Remove deletes the named entry under host, reporting whether it existed.
// An environment left with no entries is removed with it.
func (f *File) Remove(host, name string) bool {
	entries := f.Environments[host]
	for i := range entries {
		if entries[i].Name != name {
			continue
		}
		entries = append(entries[:i:i], entries[i+1:]...)
		if len(entries) == 0 {
			delete(f.Environments, host)
		} else {
			f.Environments[host] = entries
		}
		return true
	}
	return false
}

// Entries returns a copy of the entries for host, nil when the host has
// none. Upsert and Remove are how the file changes.
func (f *File) Entries(host string) []Entry {
	entries := f.Environments[host]
	if entries == nil {
		return nil
	}
	out := make([]Entry, len(entries))
	for i := range entries {
		out[i] = entries[i].clone()
	}
	return out
}

// clone is a copy of e that shares nothing with it.
func (e *Entry) clone() Entry {
	c := *e
	c.Workloads = slices.Clone(e.Workloads)
	c.ServiceNames = slices.Clone(e.ServiceNames)
	c.ProcessGroups = slices.Clone(e.ProcessGroups)
	c.Services = slices.Clone(e.Services)
	c.Segments = slices.Clone(e.Segments)
	c.Annotations = maps.Clone(e.Annotations)
	for i, seg := range c.Segments {
		if seg.Variables == nil {
			continue
		}
		c.Segments[i].Variables = make(map[string][]string, len(seg.Variables))
		for name, values := range seg.Variables {
			c.Segments[i].Variables[name] = slices.Clone(values)
		}
	}
	return c
}

// Names lists the entry names for host, in file order.
func (f *File) Names(host string) []string {
	var names []string
	for _, e := range f.Environments[host] {
		names = append(names, e.Name)
	}
	return names
}

// Hosts lists the environment hosts present, sorted.
func (f *File) Hosts() []string {
	hosts := make([]string, 0, len(f.Environments))
	for h := range f.Environments {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// Empty reports whether no environment has an entry; Save then removes the
// file instead of writing an empty one.
func (f *File) Empty() bool {
	for _, entries := range f.Environments {
		if len(entries) > 0 {
			return false
		}
	}
	return true
}
