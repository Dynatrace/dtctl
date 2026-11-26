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
// each fail the read, naming the line. An anchor, an alias or a merge key
// fails it too: a committed data file needs none, and each is a way to make a
// line mean something other than what it shows.
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
		if k.Value == "apiVersion" && k.Kind == yaml.ScalarNode && v.Kind == yaml.ScalarNode && !isNull(v) && v.Value != apiVersion {
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
// an explicit null.
func emptyDocument(doc *yaml.Node) bool {
	for _, n := range doc.Content {
		if !isNull(n) {
			return false
		}
	}
	return true
}

// plainNodes refuses an anchor or a merge key anywhere under n. An alias
// needs an anchor before it, so refusing the anchor refuses both.
func plainNodes(n *yaml.Node) *InvalidError {
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
}

func (p place) String() string { return fmt.Sprintf("%s on line %d", p.what, p.line) }

// item is the place of the i-th item of the list at p.
func (p place) item(i int, n *yaml.Node) place {
	return place{fmt.Sprintf("%s[%d]", p.what, i), n.Line}
}

// noValue refuses a null. A key left empty is a line somebody has not
// finished, not a way to say "anything".
func noValue(p place) *InvalidError {
	return invalid(p.String()+" has no value", "write the value, or delete the line")
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
// its value's. A key is a plain string, given once.
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
	case n.ShortTag() == "!!binary":
		return "", invalid(p.String() + " must be plain text, not !!binary data")
	}
	return n.Value, nil
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
	return r.mapping(n, place{"the document", n.Line}, func(key string, k, v *yaml.Node) (err *InvalidError) {
		p := place{key, k.Line}
		switch key {
		case "apiVersion":
			f.APIVersion, err = text(v, p)
		case "environments":
			f.Environments = map[string][]Entry{}
			err = r.mapping(v, p, func(host string, k, v *yaml.Node) (err *InvalidError) {
				f.Environments[host], err = items(v, place{fmt.Sprintf("environment %q", host), k.Line}, r.entry)
				return err
			})
		default:
			f.unknown = append(f.unknown, fmt.Sprintf("%q on line %d", key, k.Line))
		}
		return err
	})
}

func (r *reader) entry(n *yaml.Node, p place) (e Entry, err *InvalidError) {
	p.what = "entry" // its line says which
	err = r.mapping(n, p, func(key string, k, v *yaml.Node) (err *InvalidError) {
		p := place{key, k.Line}
		switch key {
		case "name":
			e.Name, err = text(v, p)
		case "path":
			e.Path, err = text(v, p)
		case "workloads":
			e.Workloads, err = items(v, p, r.workload)
		case "service-names":
			e.ServiceNames, err = items(v, p, text)
		case "process-groups":
			e.ProcessGroups, err = items(v, p, text)
		case "services":
			e.Services, err = items(v, p, text)
		case "segments":
			e.Segments, err = items(v, p, r.segment)
		default:
			r.unknownIn("an entry", k)
		}
		return err
	})
	return e, err
}

func (r *reader) workload(n *yaml.Node, p place) (w Workload, err *InvalidError) {
	err = r.mapping(n, p, func(key string, k, v *yaml.Node) (err *InvalidError) {
		p := place{key, k.Line}
		switch key {
		case "cluster-uid":
			w.ClusterUID, err = text(v, p)
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
		p := place{key, k.Line}
		switch key {
		case "id":
			s.ID, err = text(v, p)
		case "variables":
			s.Variables = map[string][]string{}
			err = r.mapping(v, p, func(name string, k, v *yaml.Node) (err *InvalidError) {
				s.Variables[name], err = items(v, place{fmt.Sprintf("variable %q", name), k.Line}, text)
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
	return unknownKeys(f.unknown)
}

// unknownKeys names keys this dtctl does not know, each as decode found it.
func unknownKeys(keys []string) string {
	noun := "key"
	if len(keys) > 1 {
		noun = "keys"
	}
	return fmt.Sprintf("unknown %s %s (a typo, or written by a newer dtctl)", noun, strings.Join(keys, ", "))
}

// encode renders f the way Save writes it: the header, then the document
// with two-space indentation. Map keys come out sorted, so the same File
// always produces the same bytes.
func encode(f *File) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(fileHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("encode %s: %w", FileName, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode %s: %w", FileName, err)
	}
	return buf.Bytes(), nil
}
