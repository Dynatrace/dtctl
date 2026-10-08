package reposcope

import (
	"fmt"
	"net"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FileName is the scope file's name, found in the repository root.
const FileName = ".dtctl-repo-scope.yaml"

// apiVersion is the only document version this build reads and writes. An
// empty apiVersion means the same thing.
const apiVersion = "v1"

// File is the on-disk document. Environments is keyed by environment host
// (urls.Host of a context's environment URL): context names are local to a
// machine, hosts are not, which is the same reasoning the config contract
// applies to token bindings.
type File struct {
	APIVersion   string             `yaml:"apiVersion,omitempty"`
	Environments map[string][]Entry `yaml:"environments"`

	// unknown is every key the document holds that File does not model, as
	// "name" on line N. Save refuses while it is set: writing would drop them.
	unknown []string
}

// Entry is one named binding set. A query run from Path (or any directory
// under it) is scoped to these bindings; an Entry without Path covers the
// whole repository. The file spells its keys in kebab-case, results in
// camelCase.
type Entry struct {
	Name          string     `yaml:"name" json:"name" table:"NAME"`
	Path          string     `yaml:"path,omitempty" json:"path,omitempty" table:"PATH"`
	Services      []string   `yaml:"services,omitempty" json:"services,omitempty" table:"SERVICES"`
	ProcessGroups []string   `yaml:"process-groups,omitempty" json:"processGroups,omitempty" table:"PROCESS-GROUPS"`
	ServiceNames  []string   `yaml:"service-names,omitempty" json:"serviceNames,omitempty" table:"SERVICE-NAMES"`
	Workloads     []Workload `yaml:"workloads,omitempty" json:"workloads,omitempty" table:"WORKLOADS"`
}

// Workload binds one Kubernetes workload. Namespace is required: a workload
// name alone is not unique across namespaces.
type Workload struct {
	Namespace string `yaml:"namespace" json:"namespace"`
	Name      string `yaml:"name" json:"name"`
}

// String renders the workload as namespace/name, the way kubectl users read it.
func (w Workload) String() string {
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
)

// maxDNSSubdomain bounds a Kubernetes DNS subdomain name. A namespace is a
// DNS label; a workload (a Deployment, StatefulSet, …) is a DNS subdomain, so
// "checkout.v2" is a valid one.
const maxDNSSubdomain = 253

// maxServiceNameRunes bounds a service name; Dynatrace stores longer ones
// truncated, so a longer binding could never match.
const maxServiceNameRunes = 255

// check validates the document and every literal that could reach a query.
// The file is untrusted, since a clone brings it along, so a literal that
// fails its pattern is an error naming the host, the entry and the field,
// never something to escape and hope. Beyond the patterns above: host keys
// must already be canonical, names and paths are unique per host (so at most
// one entry omits Path), and every entry binds something.
//
// Paths are unique regardless of case, on every OS. Resolve folds case on
// macOS and Windows, where two paths differing only in case are one
// directory; a committed file has to mean the same thing on all of them, so
// it is valid everywhere or nowhere.
//
// The error's Source is FileName; Load and Save replace it with the path.
func (f *File) check() *InvalidError {
	if f.APIVersion != "" && f.APIVersion != apiVersion {
		return invalid(fmt.Sprintf("apiVersion %q is not supported (this dtctl reads %q)", f.APIVersion, apiVersion),
			"upgrade dtctl, or set apiVersion: "+apiVersion)
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
		msg := fmt.Sprintf("environment %q must be written as the bare host %q", host, canonical)
		if _, taken := f.Environments[canonical]; taken {
			msg = fmt.Sprintf("environments %q and %q name the same host", host, canonical)
		}
		return invalid(msg, "key each environment by its lowercase host, e.g. abc12345.apps.dynatrace.com")
	}
	if !dnsSubdomainRe.MatchString(host) {
		return invalid(fmt.Sprintf("environment %q is not a host name", host))
	}
	names := map[string]bool{}
	paths := map[string]Entry{} // by lowercase path
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
		if other, taken := paths[strings.ToLower(e.Path)]; taken {
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
		paths[strings.ToLower(e.Path)] = e
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
	for i, w := range e.Workloads {
		if !dnsLabelRe.MatchString(w.Namespace) {
			return invalid(fmt.Sprintf("workloads[%d] namespace %q is not a Kubernetes namespace name", i, w.Namespace))
		}
		if len(w.Name) > maxDNSSubdomain || !dnsSubdomainRe.MatchString(w.Name) {
			return invalid(fmt.Sprintf("workloads[%d] name %q is not a Kubernetes workload name", i, w.Name))
		}
	}
	if !e.hasBinding() {
		return invalid("binds nothing",
			"add at least one of services, process-groups, service-names or workloads")
	}
	return nil
}

func (e *Entry) hasBinding() bool {
	return len(e.Services)+len(e.ProcessGroups)+len(e.ServiceNames)+len(e.Workloads) > 0
}

// validatePath holds Path to a clean, relative, slash-separated directory
// inside the repository.
func validatePath(p string) *InvalidError {
	if p == "" {
		return nil
	}
	switch {
	case strings.Contains(p, `\`):
		return invalid(fmt.Sprintf("path %q must use forward slashes", p))
	case path.IsAbs(p):
		return invalid(fmt.Sprintf("path %q must be relative to the repository root", p))
	case p == ".":
		return invalid(`path "." is the repository root`, "omit path for an entry that covers the whole repository")
	case p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/.."):
		return invalid(fmt.Sprintf("path %q leaves the repository", p))
	case path.Clean(p) != p:
		return invalid(fmt.Sprintf("path %q is not clean (write it as %q)", p, path.Clean(p)))
	}
	return nil
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
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return "must not contain control or non-printing characters"
		}
	}
	return ""
}

// canonicalHost is the form a host key must already have: lowercase, with no
// scheme, path, port or trailing dot.
func canonicalHost(key string) string {
	h := strings.ToLower(strings.TrimSpace(key))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
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

// Entries returns the entries for host, nil when the host has none.
func (f *File) Entries(host string) []Entry {
	return f.Environments[host]
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
