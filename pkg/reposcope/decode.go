package reposcope

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// fileHeader opens every file Save writes. The file is committed and read by
// people who may never have heard of dtctl, so it says what it is.
const fileHeader = "# dtctl repo scope: links this repository to the Dynatrace entities that run its code.\n" +
	"# Written by 'dtctl repo-scope set'; see docs/REPO_SCOPE.md. Commit it to share it.\n"

// decode parses and validates a scope file's bytes. Empty input is an empty,
// valid File.
//
// The file arrives with every clone, and a scope reported as applied must
// never be wider than the file says. So the document is parsed into nodes and
// read key by key, and nothing in it is read past: a value left empty, a key
// given twice, a key that is not a plain string and a value of another shape
// each fail the read, naming the line. Under a key that narrows an entry, and
// under a list of bindings, a value written empty ("", [] or {}) is a value
// left empty: read as the key left out, it would give the widest meaning the
// key has (see written). An anchor, an alias or a merge key fails it too: a
// committed data file needs none, and each is a way to make a line mean
// something other than what it shows.
//
// A key this dtctl does not know is a typo or a newer dtctl's key, and the
// two look the same here. Where it sits decides what it does to the read.
//
// A key of the document itself is read past, for Warning and for Save to
// refuse on. It is beside the entries and changes nothing any of them
// selects, and failing over it would leave every query in the repository
// unscoped over one stray line.
//
// A key inside an entry fails the read. That is where a constraint lives:
// read past, a misspelt cluster-uid leaves its workload bound in every
// cluster. Nothing here can tell such a key from a harmless one, so none is
// read past.
//
// The version is read before anything else, so a file of a later format is
// refused by its version and not by whatever this dtctl finds odd in it.
//
// A second YAML document with content is an error, not a warning: only the
// first is read, so Save would silently drop the rest. One empty document,
// which a generator or a concatenation leaves after a trailing '---',
// carries nothing to drop.
func decode(data []byte) (*File, *InvalidError) {
	f, err := read(data)
	if err != nil {
		err.Msg = bounded(err.Msg)
	}
	return f, err
}

// bounded is msg cut to maxMessageBytes. The file is as long as 1 MiB and a
// refusal quotes what it refuses, so without a bound one line of a clone
// decides how much every command run in it prints.
func bounded(msg string) string {
	if len(msg) <= maxMessageBytes {
		return msg
	}
	const cut = "… (cut short)"
	return strings.ToValidUTF8(msg[:maxMessageBytes-len(cut)], "") + cut
}

// read is decode without the bound on what it says.
func read(data []byte) (*File, *InvalidError) {
	f := &File{}
	if len(bytes.TrimSpace(data)) == 0 {
		return f, nil
	}
	stream := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	switch err := stream.Decode(&doc); {
	case errors.Is(err, io.EOF):
		return f, nil // comments only
	case err != nil:
		return nil, notYAML(err)
	}
	root := doc.Content[0]
	if err := otherVersion(root); err != nil {
		return nil, err
	}
	if err := onlyDocument(stream); err != nil {
		return nil, err
	}
	if err := plainNodes(root); err != nil {
		return nil, err
	}
	r := &reader{}
	if err := r.document(root, f); err != nil {
		return nil, err
	}
	if len(f.unknown) > 0 && entryCount(f) == 0 {
		// The keys would be a warning on the queries the entries scope, and
		// there are none: nothing would ever say that the file is not read.
		// An "environments" key that holds nothing is the same file.
		what := "entry"
		if _, has := mappingValue(root, "environments"); !has {
			what = `"environments" key`
		}
		return nil, invalid("holds no "+what+", so nothing in it is applied; "+unknownKeys(f.unknown),
			"write the environments under the key environments")
	}
	if len(r.narrowing) > 0 {
		them := "them"
		if len(r.narrowing) == 1 {
			them = "it"
		}
		return nil, invalid(unknownKeys(r.narrowing)+"; a key there can narrow the entry, so the file is not applied without "+them,
			"correct the key, or upgrade dtctl if a newer one wrote it")
	}
	if err := f.check(); err != nil {
		return nil, err
	}
	return f, nil
}

// notYAML reports text the YAML parser refused, on one line.
func notYAML(err error) *InvalidError {
	return invalid("not valid YAML: " + strings.Join(strings.Fields(strings.TrimPrefix(err.Error(), "yaml: ")), " "))
}

// otherVersion refuses a document that declares a version other than the one
// this dtctl reads. It looks at nothing else: what the rest of such a
// document means is the other version's to say.
func otherVersion(root *yaml.Node) *InvalidError {
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		// A tagged key or value is refused as such (see plainNodes), not
		// read as the version it spells.
		if k.Value != "apiVersion" || k.Kind != yaml.ScalarNode || v.Kind != yaml.ScalarNode || tagged(k) || tagged(v) {
			continue
		}
		if v.Value != apiVersion && strings.EqualFold(v.Value, apiVersion) {
			return invalid(fmt.Sprintf("apiVersion %q on line %d must be written %q", v.Value, k.Line, apiVersion))
		}
		if v.Value != "" && !isNull(v) && v.Value != apiVersion {
			return invalid(fmt.Sprintf("apiVersion %q on line %d is not supported (this dtctl reads %q)", v.Value, k.Line, apiVersion),
				"upgrade dtctl to read this file")
		}
	}
	return nil
}

// onlyDocument reads the rest of the stream, which may hold one more
// document as long as it is empty.
func onlyDocument(stream *yaml.Decoder) *InvalidError {
	for empty := 0; ; empty++ {
		var doc yaml.Node
		switch err := stream.Decode(&doc); {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return notYAML(err)
		case !emptyDocument(&doc):
			return invalid("holds more than one YAML document",
				"move every environment into the first document and delete each '---' line after it")
		case empty > 0:
			return invalid("holds more than one '---' after its document", "delete the '---' lines after the document")
		}
	}
}

// emptyDocument reports a document that holds no value: nothing at all, or
// a null as it is written, without an anchor or a tag that would make it
// something to keep.
func emptyDocument(doc *yaml.Node) bool {
	for _, n := range doc.Content {
		if !isNull(n) || n.Anchor != "" || tagged(n) {
			return false
		}
	}
	return true
}

// tagged reports a node written with a tag of its own (!!str, !custom).
func tagged(n *yaml.Node) bool {
	return n.Style&yaml.TaggedStyle != 0
}

// mappingValue is the value under key in the mapping n.
func mappingValue(n *yaml.Node, key string) (*yaml.Node, bool) {
	if n.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Kind == yaml.ScalarNode && n.Content[i].Value == key {
			return n.Content[i+1], true
		}
	}
	return nil, false
}

// plainNodes refuses an anchor, a merge key or an explicit tag anywhere under
// n, keys included. An alias needs an anchor before it, so refusing the anchor
// refuses both. A tag says a value is something other than the text its line
// shows (!!binary), or is ignored and so says nothing (!!int abc); every value
// of the format is read as its spelling, so no tag is needed.
func plainNodes(n *yaml.Node) *InvalidError {
	if tagged(n) {
		return invalid(fmt.Sprintf("the tag %s on line %d is not accepted", n.ShortTag(), n.Line),
			"write the value without a tag; it is read as the text the line shows")
	}
	if n.Anchor != "" {
		return invalid(fmt.Sprintf("the anchor &%s on line %d is not accepted, nor is an alias of it", n.Anchor, n.Line),
			"write each value out where it is used")
	}
	for i, child := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 && child.Value == "<<" && child.ShortTag() == "!!merge" {
			return invalid(fmt.Sprintf("the merge key << on line %d is not accepted", child.Line),
				"write each key out where it applies")
		}
		if err := plainNodes(child); err != nil {
			return err
		}
	}
	return nil
}

// isNull reports a value left empty or written as null.
func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null"
}

// place is where a value sits in the file: what the file calls it, and its
// line.
type place struct {
	what string
	line int
	// index is the item's position in the list what names, for the place
	// of an item. The text is put together only when a message needs it: a
	// list can hold as many items as the file has bytes for, and what can be
	// as long as a key.
	index   int
	indexed bool
}

func (p place) String() string {
	if p.indexed {
		return fmt.Sprintf("%s[%d] on line %d", p.what, p.index, p.line)
	}
	return fmt.Sprintf("%s on line %d", p.what, p.line)
}

// item is the place of the i-th item of the list at p.
func (p place) item(i int, n *yaml.Node) place {
	return place{what: p.what, line: n.Line, index: i, indexed: true}
}

// noValue refuses a null. A key left empty is a line somebody has not
// finished, not a way to say "anything".
func noValue(p place) *InvalidError {
	return invalid(p.String()+" has no value", "write the value, or delete the line")
}

// written refuses a value spelt empty under a key that narrows: "" for a
// path or a cluster-uid, [] for segments, {} for a segment's variables. Each
// of these keys means its widest when it is left out (the whole repository,
// every cluster, no segment, a segment as it stands), so an empty value read
// as "left out" would turn an unfinished constraint into none. Save never
// writes one: it leaves the key out.
//
// A list of bindings is held to the same rule. Bindings are alternatives, so
// one list fewer selects no more, but that stops being true of the last one:
// beside segments, an entry whose only list of bindings is written empty would
// read as an entry of segments alone, which is everything in the segment.
// Refusing the empty list wherever it stands is the rule that needs no "last".
func written(n *yaml.Node, p place) *InvalidError {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value != "" {
			return nil
		}
	case yaml.SequenceNode, yaml.MappingNode:
		if len(n.Content) > 0 {
			return nil
		}
	default:
		return nil // text, items and mapping say what is wrong with it
	}
	return noValue(p)
}

// constraint reads the text under a key that narrows.
func constraint(n *yaml.Node, p place) (string, *InvalidError) {
	if err := written(n, p); err != nil {
		return "", err
	}
	return text(n, p)
}

// wrongShape refuses a value of another kind than the format has at p.
func wrongShape(p place, want string) *InvalidError {
	return invalid(fmt.Sprintf("does not match the repo scope schema: %s must be %s", p, want),
		"see docs/REPO_SCOPE.md for the file format")
}

// reader fills a File from the nodes of a document, collecting the keys it
// does not know inside an entry.
type reader struct {
	narrowing []string
}

// mapping calls each for every key of the mapping n, with the key's node and
// its value's. A key is a plain string, given once, and no longer than a key
// of the format can be: YAML bounds a key written "key:" and not one written
// "? key", and a key is repeated in what is said about everything under it.
func (r *reader) mapping(n *yaml.Node, p place, each func(key string, k, v *yaml.Node) *InvalidError) *InvalidError {
	switch {
	case isNull(n):
		return noValue(p)
	case n.Kind != yaml.MappingNode:
		return wrongShape(p, "a mapping")
	}
	lines := make(map[string]int, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || isNull(k) {
			return invalid(fmt.Sprintf("the key on line %d must be a plain string", k.Line))
		}
		if len(k.Value) > maxKeyBytes {
			return invalid(fmt.Sprintf("the key on line %d is longer than %d bytes, which no key of the format is", k.Line, maxKeyBytes))
		}
		if first, twice := lines[k.Value]; twice {
			return invalid(fmt.Sprintf("key %q on line %d is already given on line %d", k.Value, k.Line, first))
		}
		lines[k.Value] = k.Line
		if err := each(k.Value, k, v); err != nil {
			return err
		}
	}
	return nil
}

// text reads one value as the text the file shows: a number or a boolean is
// its spelling.
func text(n *yaml.Node, p place) (string, *InvalidError) {
	switch {
	case isNull(n):
		return "", noValue(p)
	case n.Kind != yaml.ScalarNode:
		return "", wrongShape(p, "a single value")
	}
	return n.Value, nil
}

// writtenItems reads the list at p, which must hold an item (see written).
func writtenItems[T any](n *yaml.Node, p place, read func(*yaml.Node, place) (T, *InvalidError)) ([]T, *InvalidError) {
	if err := written(n, p); err != nil {
		return nil, err
	}
	return items(n, p, read)
}

// items reads the list at p, each item through read.
func items[T any](n *yaml.Node, p place, read func(*yaml.Node, place) (T, *InvalidError)) ([]T, *InvalidError) {
	switch {
	case isNull(n):
		return nil, noValue(p)
	case n.Kind != yaml.SequenceNode:
		return nil, wrongShape(p, "a list")
	}
	out := make([]T, 0, len(n.Content))
	for i, item := range n.Content {
		v, err := read(item, p.item(i, item))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *reader) document(n *yaml.Node, f *File) *InvalidError {
	if isNull(n) {
		return nil
	}
	return r.mapping(n, place{what: "the document", line: n.Line}, func(key string, k, v *yaml.Node) (err *InvalidError) {
		p := place{what: key, line: k.Line}
		switch key {
		case "apiVersion":
			// Left out, the version is v1; written, it says which.
			if f.APIVersion, err = text(v, p); err == nil && f.APIVersion == "" {
				err = noValue(p)
			}
		case "environments":
			f.Environments = map[string][]Entry{}
			err = r.mapping(v, p, func(host string, k, v *yaml.Node) (err *InvalidError) {
				f.Environments[host], err = items(v, place{what: fmt.Sprintf("environment %q", host), line: k.Line}, r.entry)
				return err
			})
		default:
			// One of the two keys in another case is a slip, and read past
			// it hides the entries or the version.
			for _, known := range []string{"apiVersion", "environments"} {
				if strings.EqualFold(key, known) {
					return invalid(fmt.Sprintf("key %q on line %d must be written %q", key, k.Line, known))
				}
			}
			f.unknown = append(f.unknown, fmt.Sprintf("%q on line %d", key, k.Line))
		}
		return err
	})
}

func (r *reader) entry(n *yaml.Node, p place) (e Entry, err *InvalidError) {
	p = place{what: "entry", line: p.line} // its line says which
	err = r.mapping(n, p, func(key string, k, v *yaml.Node) (err *InvalidError) {
		p := place{what: key, line: k.Line}
		switch key {
		case "name":
			e.Name, err = text(v, p)
		case "path":
			e.Path, err = constraint(v, p)
		case "workloads":
			e.Workloads, err = writtenItems(v, p, r.workload)
		case "service-names":
			e.ServiceNames, err = writtenItems(v, p, text)
		case "process-groups":
			e.ProcessGroups, err = writtenItems(v, p, text)
		case "services":
			e.Services, err = writtenItems(v, p, text)
		case "segments":
			e.Segments, err = writtenItems(v, p, r.segment)
		case "annotations":
			err = r.mapping(v, p, func(name string, k, v *yaml.Node) (err *InvalidError) {
				if e.Annotations == nil {
					e.Annotations = map[string]string{}
				}
				e.Annotations[name], err = text(v, place{what: fmt.Sprintf("annotation %q", name), line: k.Line})
				return err
			})
		default:
			r.unknownIn("an entry", k)
		}
		return err
	})
	return e, err
}

func (r *reader) workload(n *yaml.Node, p place) (w Workload, err *InvalidError) {
	err = r.mapping(n, p, func(key string, k, v *yaml.Node) (err *InvalidError) {
		p := place{what: key, line: k.Line}
		switch key {
		case "cluster-uid":
			w.ClusterUID, err = constraint(v, p)
		case "namespace":
			w.Namespace, err = text(v, p)
		case "name":
			w.Name, err = text(v, p)
		default:
			r.unknownIn("a workload", k)
		}
		return err
	})
	return w, err
}

func (r *reader) segment(n *yaml.Node, p place) (s Segment, err *InvalidError) {
	err = r.mapping(n, p, func(key string, k, v *yaml.Node) (err *InvalidError) {
		p := place{what: key, line: k.Line}
		switch key {
		case "id":
			s.ID, err = text(v, p)
		case "variables":
			if err = written(v, p); err != nil {
				return err
			}
			s.Variables = map[string][]string{}
			err = r.mapping(v, p, func(name string, k, v *yaml.Node) (err *InvalidError) {
				s.Variables[name], err = items(v, place{what: fmt.Sprintf("variable %q", name), line: k.Line}, text)
				return err
			})
		default:
			r.unknownIn("a segment", k)
		}
		return err
	})
	return s, err
}

// unknownIn records a key this dtctl does not know inside an entry, where a
// key can narrow what the entry selects.
func (r *reader) unknownIn(where string, k *yaml.Node) {
	r.narrowing = append(r.narrowing, fmt.Sprintf("%q on line %d, in %s", k.Value, k.Line, where))
}

// Warning names the document keys the file holds that this dtctl does not
// know, or returns "". The entries apply without them.
func (f *File) Warning() string {
	if len(f.unknown) == 0 {
		return ""
	}
	return bounded(unknownKeys(f.unknown))
}

// maxNamedKeys is how many unknown keys a message names before it counts.
const maxNamedKeys = 5

// unknownKeys names keys this dtctl does not know, each as decode found it:
// the first few, then how many more there are.
func unknownKeys(keys []string) string {
	noun := "key"
	if len(keys) > 1 {
		noun = "keys"
	}
	more := ""
	if n := len(keys) - maxNamedKeys; n > 0 {
		keys, more = keys[:maxNamedKeys], fmt.Sprintf(" and %d more", n)
	}
	return fmt.Sprintf("unknown %s %s%s (a typo, or written by a newer dtctl)", noun, strings.Join(keys, ", "), more)
}

// The file's own spelling: kebab-case keys, where Entry and Workload carry
// the camelCase a result is printed in. encode writes through these types,
// and the reader's key names are the same ones. A field added to Entry or
// Workload is added here too, or it is not written; a test compares the
// fields.
type (
	fileDoc struct {
		APIVersion   string                 `yaml:"apiVersion"`
		Environments map[string][]fileEntry `yaml:"environments"`
	}
	fileEntry struct {
		Name          string            `yaml:"name"`
		Path          string            `yaml:"path,omitempty"`
		Workloads     []fileWorkload    `yaml:"workloads,omitempty"`
		ServiceNames  []string          `yaml:"service-names,omitempty"`
		ProcessGroups []string          `yaml:"process-groups,omitempty"`
		Services      []string          `yaml:"services,omitempty"`
		Segments      []Segment         `yaml:"segments,omitempty"`
		Annotations   map[string]string `yaml:"annotations,omitempty"`
	}
	fileWorkload struct {
		ClusterUID string `yaml:"cluster-uid,omitempty"`
		Namespace  string `yaml:"namespace"`
		Name       string `yaml:"name"`
	}
)

// newFileDoc is f as Save writes it: at this dtctl's version, whether or not
// the file it was read from stated one.
func newFileDoc(f *File) fileDoc {
	doc := fileDoc{APIVersion: apiVersion, Environments: make(map[string][]fileEntry, len(f.Environments))}
	for host, entries := range f.Environments {
		out := make([]fileEntry, len(entries))
		for i, e := range entries {
			out[i] = fileEntry{
				Name: e.Name, Path: e.Path, ServiceNames: e.ServiceNames, ProcessGroups: e.ProcessGroups,
				Services: e.Services, Segments: e.Segments, Annotations: e.Annotations,
			}
			for _, w := range e.Workloads {
				out[i].Workloads = append(out[i].Workloads, fileWorkload(w))
			}
		}
		doc.Environments[host] = out
	}
	return doc
}

// encode renders f the way Save writes it: the header, then the document
// with two-space indentation. Map keys come out sorted, so the same File
// always produces the same bytes. A written file states its version, whether
// or not the one it was read from did.
func encode(f *File) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(fileHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(newFileDoc(f)); err != nil {
		return nil, fmt.Errorf("encode %s: %w", FileName, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode %s: %w", FileName, err)
	}
	return buf.Bytes(), nil
}

// entryCount is how many entries the document holds over all environments.
func entryCount(f *File) int {
	n := 0
	for _, entries := range f.Environments {
		n += len(entries)
	}
	return n
}
