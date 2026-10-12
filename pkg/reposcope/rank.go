package reposcope

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Candidate is one identity found for the unit: what the spans and logs of
// one workload or service name carried, merged, or one entity whose name
// matched.
type Candidate struct {
	Rank int `json:"rank" yaml:"rank"`
	// Name is the entry to save it as: the workload, else the service name,
	// else the name an entity matched, else the unit.
	Name string `json:"name" yaml:"name"`
	// Services and ProcessGroups are the ids the records carried, up to
	// discoveryIDLimit of each per row. ServiceCount and ProcessGroupCount
	// are how many there are: the largest distinct count any one row
	// reported, since the spans and logs rows of one candidate report
	// overlapping sets and a sum would count an id twice. That makes it a
	// lower bound on the union, never below the ids listed. A Capped flag
	// says a row had more ids than it listed, so the list is not all of
	// them.
	Services            []string `json:"services,omitempty" yaml:"services,omitempty"`
	ServiceCount        int64    `json:"serviceCount,omitempty" yaml:"serviceCount,omitempty"`
	ServicesCapped      bool     `json:"servicesCapped,omitempty" yaml:"servicesCapped,omitempty"`
	ProcessGroups       []string `json:"processGroups,omitempty" yaml:"processGroups,omitempty"`
	ProcessGroupCount   int64    `json:"processGroupCount,omitempty" yaml:"processGroupCount,omitempty"`
	ProcessGroupsCapped bool     `json:"processGroupsCapped,omitempty" yaml:"processGroupsCapped,omitempty"`
	ServiceName         string   `json:"serviceName,omitempty" yaml:"serviceName,omitempty"`
	// ServiceNameShared reports that another candidate carries this service
	// name under a different namespace and workload, either of which may be
	// absent: a canary beside its main workload, the same service in another
	// namespace, or one whose records name no workload at all. A
	// service-name binding would select that candidate's records too.
	ServiceNameShared bool   `json:"serviceNameShared,omitempty" yaml:"serviceNameShared,omitempty"`
	Namespace         string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Workload          string `json:"workload,omitempty" yaml:"workload,omitempty"`
	Tier              string `json:"tier" yaml:"tier"`
	// Spans and Logs count the records of a data object read whole, over the
	// discovery window. SpansSampled and LogsSampled are, for an object a
	// sampled retry read instead, the records the sample matched over that
	// retry's window. No population is reported for a sample: on a busy
	// environment the extrapolation came out hundreds of times above the
	// rate the records were written at.
	Spans        int64 `json:"spans,omitempty" yaml:"spans,omitempty"`
	Logs         int64 `json:"logs,omitempty" yaml:"logs,omitempty"`
	SpansSampled int64 `json:"spansSampled,omitempty" yaml:"spansSampled,omitempty"`
	LogsSampled  int64 `json:"logsSampled,omitempty" yaml:"logsSampled,omitempty"`
	// Sampled reports that some of the candidate's records were read by a
	// sampled retry, so a candidate quieter than the stated floor could
	// have been missed beside it.
	Sampled  bool     `json:"sampled,omitempty" yaml:"sampled,omitempty"`
	Evidence []string `json:"evidence" yaml:"evidence"`
	// MatchedDir reports that the name of the directory discovery ran for
	// is among the names that matched the candidate. It is set only when
	// the unit is the repository root and the directory is below it: that
	// name is then all that ties the candidate to the directory rather than
	// to the whole repository.
	MatchedDir bool `json:"matchedDir,omitempty" yaml:"matchedDir,omitempty"`
	// SetCommand is the command line that saves the candidate. Discover
	// leaves it empty: spelling a command line is the CLI's business.
	SetCommand string `json:"setCommand,omitempty" yaml:"setCommand,omitempty"`

	// Used while ranking; cleared once the candidates are final.
	tier tier
	// sources are the repository files whose names matched. A manifest
	// that repeats a name on several lines is still one file: ranking
	// weighs independent files, not repetition.
	sources map[string]bool
	matched string  // the token an entity matched
	rate    float64 // records, or sampled hits, per hour: see before
}

// tier is how well a candidate's name matches, best first. Records found by
// the record queries are exact by construction: in() is equality.
type tier int

const (
	tierExact tier = iota
	tierSegment
	tierSubstring
)

func (t tier) String() string {
	return [...]string{"exact", "segment", "substring"}[t]
}

// recordKey is what rows of the record queries are merged on. Every such row
// has a workload or a service name, because the query filters on them.
type recordKey struct {
	namespace, workload, serviceName string
}

type matcher struct {
	value string
	token token
}

type discovery struct {
	unit string
	// dirSource is the source of the token naming the directory discovery
	// ran for, when MatchedDir can apply; "" otherwise.
	dirSource  string
	matchers   []matcher
	namespaces []token
	records    map[recordKey]*Candidate
	entities   []*Candidate
	// samples is the sampled retry that answered for a data object, absent
	// for one read whole.
	samples map[string]*Query
}

// newDiscovery prepares the matchers: every variant of every token sent, then
// every sent value no token explains, which came from a term.
func newDiscovery(p *Plan) *discovery {
	d := &discovery{unit: p.Unit, records: map[recordKey]*Candidate{}, samples: map[string]*Query{}}
	if p.Unit == "." && p.Dir != "." {
		d.dirSource = dirSource(p.Dir)
	}
	explained := map[string]bool{}
	for _, t := range p.tokens {
		if t.kind == kindNamespace {
			d.namespaces = append(d.namespaces, t)
			continue
		}
		for _, v := range t.variants {
			d.matchers = append(d.matchers, matcher{value: v, token: t})
			explained[v] = true
		}
	}
	for _, v := range p.Sent {
		if !explained[v] {
			d.matchers = append(d.matchers, matcher{value: v, token: token{kind: kindTerm, value: v}})
		}
	}
	return d
}

// addRecord folds one aggregate row of a record query into its candidate;
// sampled is a row a sampled retry read.
func (d *discovery) addRecord(object string, rec map[string]interface{}, sampled bool) {
	key := recordKey{
		namespace:   stringField(rec, fieldNamespace),
		workload:    stringField(rec, fieldWorkload),
		serviceName: stringField(rec, fieldServiceName),
	}
	if key.workload == "" && key.serviceName == "" {
		return
	}
	c := d.records[key]
	if c == nil {
		c = &Candidate{Namespace: key.namespace, Workload: key.workload, ServiceName: key.serviceName}
		d.records[key] = c
	}
	for _, f := range []struct {
		ids              *[]string
		count            *int64
		capped           *bool
		idsKey, countKey string
		valid            *regexp.Regexp
	}{
		{&c.Services, &c.ServiceCount, &c.ServicesCapped, fieldServices, fieldServiceCount, serviceIDRe},
		{&c.ProcessGroups, &c.ProcessGroupCount, &c.ProcessGroupsCapped, fieldProcessGroups, fieldProcessGroupCount, processGroupRe},
	} {
		// For a field that is itself an array, Grail counts and collects
		// distinct arrays, so the count is compared with the arrays
		// collected, not with the ids they flatten to.
		ids, collected := idsField(rec, f.idsKey, f.valid)
		n := countField(rec, f.countKey)
		for _, id := range ids {
			*f.ids = addSorted(*f.ids, id)
		}
		*f.capped = *f.capped || n > int64(collected)
		*f.count = max(*f.count, n, int64(len(*f.ids)))
	}
	spans, logs, n := &c.Spans, &c.Logs, countField(rec, fieldRecords)
	if sampled {
		spans, logs = &c.SpansSampled, &c.LogsSampled
		c.Sampled = true
	}
	if object == objectSpans {
		*spans += n
	} else {
		*logs += n
	}
}

// addEntity turns one row of an entity-table query into a candidate carrying
// only the id of its kind, tiered by how well the entity's name matches.
func (d *discovery) addEntity(object string, rec map[string]interface{}) {
	id, name := stringField(rec, fieldEntityID), stringField(rec, fieldEntityName)
	if id == "" {
		return
	}
	t, m, ok := d.entityMatch(name)
	if !ok {
		return
	}
	c := &Candidate{tier: t, matched: m.token.value, sources: map[string]bool{m.token.file: true}}
	if object == objectServices {
		c.Services = []string{id}
	} else {
		c.ProcessGroups = []string{id}
	}
	verb := "contains"
	if t == tierExact {
		verb = "="
	}
	c.Evidence = []string{fmt.Sprintf("%s %q %s %s (%s match)", fieldEntityName, name, verb, describe(m.token), t)}
	d.entities = append(d.entities, c)
}

// entityMatch finds the best tier at which an entity name matches a sent
// value.
func (d *discovery) entityMatch(name string) (best tier, bestMatch matcher, ok bool) {
	lower := strings.ToLower(name)
	normal := ""
	if v := variants(name); len(v) > 0 {
		normal = v[0]
	}
	for _, m := range d.matchers {
		var t tier
		switch {
		case lower == m.value || normal == m.value:
			t = tierExact
		case containsSegment(lower, m.value):
			t = tierSegment
		case strings.Contains(lower, m.value):
			t = tierSubstring
		default:
			continue
		}
		if !ok || t < best {
			best, bestMatch, ok = t, m, true
		}
	}
	return best, bestMatch, ok
}

var segmentDelimiters = regexp.MustCompile(`[-_.\s]+`)

// containsSegment reports whether v is a whole run of delimited segments of
// name: "checkout" is in "checkout-service" and "eu.checkout", not in
// "checkouts".
func containsSegment(name, v string) bool {
	segs := segmentDelimiters.Split(name, -1)
	want := segmentDelimiters.Split(v, -1)
	for i := 0; i+len(want) <= len(segs); i++ {
		if strings.Join(segs[i:i+len(want)], "\x00") == strings.Join(want, "\x00") {
			return true
		}
	}
	return false
}

// explain writes a record candidate's evidence: each field the repository
// names, with the files that name it, and the record counts.
func (d *discovery) explain(c *Candidate) {
	c.sources = map[string]bool{}
	for _, field := range []struct{ name, value string }{
		{fieldWorkload, c.Workload},
		{fieldServiceName, c.ServiceName},
	} {
		var from []token
		for _, m := range d.matchers {
			if field.value != "" && m.value == field.value {
				from = append(from, m.token)
			}
		}
		c.cite(field.name, field.value, from)
	}
	var from []token
	for _, t := range d.namespaces {
		if c.Namespace != "" && sameName(t.value, c.Namespace) {
			from = append(from, t)
		}
	}
	c.cite(fieldNamespace, c.Namespace, from)
	if counts := d.counts(c); counts != "" {
		c.Evidence = append(c.Evidence, counts)
	}
}

// counts is a record candidate's evidence line for its record counts. Counts
// read whole share one window: "12400 spans, 88000 logs in 24h". A sampled
// object's hits say so, with the ratio asked for: "17 sampled span hits in
// 1h (1 in 100000 requested), 88000 logs in 24h".
func (d *discovery) counts(c *Candidate) string {
	var whole, all []string
	for _, n := range c.recordCounts() {
		if q := d.samples[n.object]; q != nil {
			all = append(all, fmt.Sprintf("%d sampled %s hits in %s (1 in %d requested)",
				n.count, strings.TrimSuffix(n.object, "s"), d.window(n.object), q.SamplingRatio))
			continue
		}
		whole = append(whole, fmt.Sprintf("%d %s", n.count, n.object))
		all = append(all, fmt.Sprintf("%d %s in %s", n.count, n.object, d.window(n.object)))
	}
	switch {
	case len(all) == 0:
		return ""
	case len(whole) == len(all):
		return strings.Join(whole, ", ") + " in " + discoveryWindow
	}
	return strings.Join(all, ", ")
}

// rate is a candidate's records per hour, each data object's over the window
// it was read in.
func (d *discovery) rate(c *Candidate) float64 {
	var rate float64
	for _, n := range c.recordCounts() {
		rate += float64(n.count) / windowHours(d.window(n.object))
	}
	return rate
}

// window is the lookback a data object's records were read over: the
// sampled retry's that answered for it, else the discovery window.
func (d *discovery) window(object string) string {
	if q := d.samples[object]; q != nil {
		return q.Window
	}
	return discoveryWindow
}

type recordCount struct {
	object string
	count  int64
}

// recordCounts is the candidate's record count, or sampled hits, per data
// object, leaving out the objects that had none. An object is read whole or
// sampled, never both, so one of each pair is zero.
func (c *Candidate) recordCounts() []recordCount {
	var out []recordCount
	for _, n := range []recordCount{{objectSpans, c.Spans + c.SpansSampled}, {objectLogs, c.Logs + c.LogsSampled}} {
		if n.count > 0 {
			out = append(out, n)
		}
	}
	return out
}

// windowHours is the length of a record query's window in hours.
func windowHours(window string) float64 {
	d, err := time.ParseDuration(window)
	if err != nil || d <= 0 {
		return 1
	}
	return d.Hours()
}

// cite adds one evidence line for a field the tokens name, listing each place
// they were read once: `service.name "checkout" from Dockerfile:4, go.mod:1`.
func (c *Candidate) cite(field, value string, from []token) {
	if len(from) == 0 {
		return
	}
	var origins []string
	seen := map[string]bool{}
	for _, t := range from {
		c.sources[t.file] = true
		if o := origin(t); !seen[o] {
			seen[o] = true
			origins = append(origins, o)
		}
	}
	c.Evidence = append(c.Evidence, fmt.Sprintf("%s %q from %s", field, value, strings.Join(origins, ", ")))
}

// origin is where a token was read: "go.mod:1", or `term "checkout"` for a
// name the user passed.
func origin(t token) string {
	if t.kind == kindTerm {
		return fmt.Sprintf("term %q", t.value)
	}
	return t.source()
}

// describe renders a token for evidence: `module "checkout" (go.mod:1)`.
func describe(t token) string {
	if t.kind == kindTerm {
		return fmt.Sprintf("term %q", t.value)
	}
	return fmt.Sprintf("%s %q (%s)", t.kind, t.value, t.source())
}

// finish ranks the candidates and writes them, with the verdicts, into r.
func (d *discovery) finish(r *DiscoveryReport) {
	all := append([]*Candidate{}, d.entities...)
	for _, c := range d.records {
		d.explain(c)
		c.rate = d.rate(c)
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool { return before(all[i], all[j]) })
	shared := sharedServiceNames(all)
	r.Candidates = make([]Candidate, len(all))
	for i, c := range all {
		c.ServiceNameShared = shared[c.ServiceName]
		c.MatchedDir = d.dirSource != "" && c.sources[d.dirSource]
		c.Rank, c.Tier = i+1, c.tier.String()
		c.Name = entryName(firstOf(c.Workload, c.ServiceName, c.matched, d.unit))
		c.tier, c.sources, c.matched, c.rate = 0, nil, "", 0
		r.Candidates[i] = *c
	}
	r.Verdict, r.Verdicts = verdicts(r.Candidates, r.Partial)
}

// sharedServiceNames is the service names more than one candidate carries.
// Record candidates are keyed by namespace, workload and service name, so
// two that carry one service name differ in the namespace or the workload.
func sharedServiceNames(cs []*Candidate) map[string]bool {
	count := map[string]int{}
	for _, c := range cs {
		if c.ServiceName != "" {
			count[c.ServiceName]++
		}
	}
	shared := map[string]bool{}
	for name, n := range count {
		shared[name] = n > 1
	}
	return shared
}

// before orders candidates: better tier, more repository files naming them,
// more records per hour, then by identity, so the order never depends on the
// order rows arrived in. Records are compared per hour because a sampled
// object may have been read over a shorter window than the other. A sampled
// object counts its hits: every candidate of one object was read by the same
// retry, so their order is the population's, while across objects hits and
// whole counts are compared as they are.
func before(c, o *Candidate) bool {
	switch {
	case c.tier != o.tier:
		return c.tier < o.tier
	case len(c.sources) != len(o.sources):
		return len(c.sources) > len(o.sources)
	case c.rate != o.rate:
		return c.rate > o.rate
	}
	return identity(c) < identity(o)
}

func identity(c *Candidate) string {
	return strings.Join([]string{c.Workload, c.ServiceName, c.Namespace,
		strings.Join(c.Services, ","), strings.Join(c.ProcessGroups, ",")}, "\x00")
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var entryNameInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// entryName turns a name found in Dynatrace or the repository into a valid
// Entry.Name.
func entryName(raw string) string {
	name := strings.Trim(entryNameInvalid.ReplaceAllString(strings.ToLower(raw), "-"), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	if name == "" {
		return "repo"
	}
	return name
}

// verdictKinds maps each verdict kind to its name for a reader and a
// candidate's values for it. A kind is decided on distinct values, so one
// candidate carrying two service ids is two services, not one. An id kind
// also has a count, the candidate's own distinct total, which may exceed the
// ids it lists.
var verdictKinds = map[string]struct {
	label  string
	values func(Candidate) []string
	count  func(Candidate) int64
}{
	"service":      {"service", func(c Candidate) []string { return c.Services }, func(c Candidate) int64 { return c.ServiceCount }},
	"processGroup": {"process group", func(c Candidate) []string { return c.ProcessGroups }, func(c Candidate) int64 { return c.ProcessGroupCount }},
	"serviceName":  {"service name", func(c Candidate) []string { return nonEmpty(c.ServiceName) }, nil},
	"workload": {"workload", func(c Candidate) []string {
		if c.Workload == "" {
			return nil
		}
		return []string{Workload{Namespace: c.Namespace, Name: c.Workload}.String()}
	}, nil},
}

func nonEmpty(v string) []string {
	if v == "" {
		return nil
	}
	return []string{v}
}

// VerdictLabel names a verdict kind in human output: "processGroup" is
// "process group".
func VerdictLabel(kind string) string {
	if k, ok := verdictKinds[kind]; ok {
		return k.label
	}
	return kind
}

// verdicts decides each kind and the whole. A partial result is never a
// match: what was not seen could be a second candidate. A kind is tallied by
// its count where it has one, so a candidate listing one of its 200 service
// ids is 200 services, not one.
func verdicts(candidates []Candidate, partial bool) (Verdict, map[string]Verdict) {
	per := make(map[string]Verdict, len(verdictKinds))
	for kind, k := range verdictKinds {
		distinct := map[string]bool{}
		var most int64
		exact := true
		for _, c := range candidates {
			values := k.values(c)
			for _, v := range values {
				distinct[v] = true
			}
			var n int64
			if k.count != nil {
				n = k.count(c)
				most = max(most, n)
			}
			if len(values) > 0 || n > 0 {
				exact = exact && c.Tier == tierExact.String()
			}
		}
		per[kind] = decide(max(len(distinct), int(most)), exact, partial)
	}
	overall := decide(len(candidates), len(candidates) == 1 && candidates[0].Tier == tierExact.String(), partial)
	return overall, per
}

func decide(distinct int, exact, partial bool) Verdict {
	switch {
	case distinct == 0:
		return VerdictNone
	case distinct == 1 && exact && !partial:
		return VerdictMatch
	}
	return VerdictAmbiguous
}

func stringField(rec map[string]interface{}, key string) string {
	s, _ := rec[key].(string)
	return s
}

// idsField reads a collected id column: an array of ids, nested once more
// when the field is itself an array (a log written by several processes
// carries its process groups as one), so it is flattened. An id the scope
// file would refuse is dropped: it could never be saved. collected is how
// many elements the column held at its top level, valid or not: the values
// collectDistinct kept, distinct ids or distinct arrays of them, which is
// what the row's countDistinct counts too, so comparing the two tells a cut
// list from a whole one.
func idsField(rec map[string]interface{}, key string, valid *regexp.Regexp) (ids []string, collected int) {
	seen := map[string]bool{}
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch v := v.(type) {
		case string:
			if !seen[v] {
				seen[v] = true
				if valid.MatchString(v) {
					ids = append(ids, v)
				}
			}
		case []interface{}:
			for _, e := range v {
				walk(e)
			}
		}
	}
	switch v := rec[key].(type) {
	case []interface{}:
		collected = len(v)
	case string:
		collected = 1
	}
	walk(rec[key])
	return ids, collected
}

// countField reads a count, which Grail sends as a number or, for a long, as
// a string.
func countField(rec map[string]interface{}, key string) int64 {
	switch n := rec[key].(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

// addSorted inserts v into the sorted set s; "" is not a value.
func addSorted(s []string, v string) []string {
	i := sort.SearchStrings(s, v)
	if v == "" || (i < len(s) && s[i] == v) {
		return s
	}
	return append(s[:i], append([]string{v}, s[i:]...)...)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
