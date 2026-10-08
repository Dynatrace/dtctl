package reposcope

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// fileHeader opens every file Save writes. The file is committed and read by
// people who may never have heard of dtctl, so it says what it is.
const fileHeader = "# dtctl repo scope: links this repository to the Dynatrace entities that run its code.\n" +
	"# Written by 'dtctl repo-scope set'; see docs/REPO_SCOPE.md. Commit it to share it.\n"

// unknownFieldRe reads the key out of the error yaml.v3 reports for a key the
// target struct has no field for.
var unknownFieldRe = regexp.MustCompile(`^line (\d+): field (.+) not found in type `)

// decode parses and validates a scope file's bytes. Empty input is an empty,
// valid File.
//
// Unknown keys do not fail the read: a typo and a newer dtctl's binding look
// the same here, and failing would leave every query in the repository
// unscoped over one stray line. A second, strict decode finds them instead,
// for Warning and for Save to refuse on.
//
// A second YAML document with content is an error, not a warning: both
// decodes read only the first, so Save would silently drop the rest. An empty
// one, which a generator or a concatenation leaves after a trailing '---',
// carries nothing to drop.
func decode(data []byte) (*File, *InvalidError) {
	f := &File{}
	if len(bytes.TrimSpace(data)) == 0 {
		return f, nil
	}
	if err := yaml.Unmarshal(data, f); err != nil {
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			return nil, invalid("does not match the repo scope schema: "+err.Error(), "see docs/REPO_SCOPE.md for the file format")
		}
		return nil, invalid("not valid YAML: " + err.Error())
	}
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var typeErr *yaml.TypeError
	if err := strict.Decode(&File{}); errors.As(err, &typeErr) {
		for _, msg := range typeErr.Errors {
			if m := unknownFieldRe.FindStringSubmatch(msg); m != nil {
				f.unknown = append(f.unknown, fmt.Sprintf("%q on line %s", m[2], m[1]))
			}
		}
	}
	for {
		var doc yaml.Node
		err := strict.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || !emptyDocument(&doc) {
			return nil, invalid("holds more than one YAML document",
				"move every environment into the first document and delete each '---' line after it")
		}
	}
	if err := f.check(); err != nil {
		return nil, err
	}
	return f, nil
}

// emptyDocument reports a document that holds no value: nothing at all, or
// an explicit null.
func emptyDocument(doc *yaml.Node) bool {
	for _, n := range doc.Content {
		if n.Kind != yaml.ScalarNode || n.Tag != "!!null" {
			return false
		}
	}
	return true
}

// Warning names the keys the file holds that this dtctl does not know, or
// returns "". The entries apply without them.
func (f *File) Warning() string {
	if len(f.unknown) == 0 {
		return ""
	}
	noun := "key"
	if len(f.unknown) > 1 {
		noun = "keys"
	}
	return fmt.Sprintf("unknown %s %s (a typo, or written by a newer dtctl)", noun, strings.Join(f.unknown, ", "))
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
