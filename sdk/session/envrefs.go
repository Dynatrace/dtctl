package session

import (
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Round-trip preservation of ${VAR} references (docs/dev/CONFIG_CONTRACT.md,
// "Write rules").
//
// LoadFrom expands environment-variable references before parsing, so the
// struct holds resolved values. Any command that loads, modifies and saves the
// config would otherwise write those resolved values back: a token stored as
// ${MY_TOKEN} becomes the plaintext secret on disk, or "" when the variable is
// unset at the time of the write.
//
// loadFrom therefore records every scalar that was written with a '$' together
// with the value it resolved to, and SaveTo writes the reference back for each
// value that is unchanged since load. A value the command changed keeps its new
// value. Doing this in SaveTo rather than in each command covers every writer,
// including the ones that need resolved values for their own logic (auth
// login uses the environment URL before it saves).

// envRef is one config value written as an environment-variable reference.
type envRef struct {
	raw    string // the literal from the file, e.g. "${MY_TOKEN}"
	loaded string // what it resolved to at load
	// present reports whether the resolved value survived the marshal. An
	// omitempty field whose variable was unset resolves to "" and is dropped
	// (and with it any parent block left empty), so on save it is absent too;
	// segments lets SaveTo put it back.
	present  bool
	segments []string
}

// envRefPath joins config path segments into a map key. No config key or entry
// name contains NUL, so paths cannot collide through their joins.
func envRefPath(segments ...string) string { return strings.Join(segments, "\x00") }

// tokenValuePath is the path of an inline token value, by credential name.
func tokenValuePath(name string) string { return envRefPath("tokens", namedElement(name), "token") }

// Sequence elements are addressed with a segment no YAML key can spell, so a
// key such as "[x]" is never mistaken for one.
const elementMark = "\x01"

func namedElement(name string) string  { return elementMark + "name=" + name }
func indexedElement(i int) string      { return elementMark + strconv.Itoa(i) }
func isElementSegment(seg string) bool { return strings.HasPrefix(seg, elementMark) }

// walkScalars calls fn for every scalar value under n with its path segments.
// Sequence elements that are mappings with a "name" key (contexts, tokens) are
// addressed by that name, so membership changes do not shift other entries;
// other elements are addressed by index.
//
// Paths identify entries by their resolved identity, the one the struct and
// every marshal of it carry. Walking the file as written, resolve expands keys
// and names, so an entry named ${CTX} still pairs with its loaded self; on a
// marshaled document resolve is nil.
func walkScalars(n *yaml.Node, segs []string, resolve func(string) string, fn func(segs []string, n *yaml.Node)) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode {
				continue
			}
			walkScalars(val, append(segs[:len(segs):len(segs)], resolveIdentity(resolve, key.Value)), resolve, fn)
		}
	case yaml.SequenceNode:
		for i, el := range n.Content {
			walkScalars(el, append(segs[:len(segs):len(segs)], elementSegment(el, i, resolve)), resolve, fn)
		}
	case yaml.ScalarNode:
		fn(segs, n)
	}
}

func resolveIdentity(resolve func(string) string, s string) string {
	if resolve == nil {
		return s
	}
	return resolve(s)
}

// elementSegment addresses a sequence element: by name for a named mapping,
// by index otherwise.
func elementSegment(el *yaml.Node, i int, resolve func(string) string) string {
	if el.Kind == yaml.MappingNode {
		if name := findMapValue(el, "name"); name != nil && name.Kind == yaml.ScalarNode {
			return namedElement(resolveIdentity(resolve, name.Value))
		}
	}
	return indexedElement(i)
}

// insertAbsent puts value at segs under the mapping m, creating the blocks on
// the way that the marshal left out. It gives up when the path runs through a
// sequence element that is gone, or through anything that is not a mapping:
// the entry was removed, and restoring a reference must never bring it back.
func insertAbsent(m *yaml.Node, segs []string, value string) bool {
	if len(segs) == 0 || m.Kind != yaml.MappingNode || isElementSegment(segs[0]) {
		return false
	}
	key, next := segs[0], findMapValue(m, segs[0])
	if len(segs) == 1 {
		if next != nil {
			return false // present after all
		}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
		return true
	}
	if isElementSegment(segs[1]) {
		if next == nil || next.Kind != yaml.SequenceNode || len(segs) == 2 {
			return false
		}
		el := findElement(next, segs[1])
		if el == nil {
			return false
		}
		return insertAbsent(el, segs[2:], value)
	}
	if next == nil {
		// Only re-create blocks that cannot need a missing element below them,
		// so a failed restore never leaves an empty block behind.
		for _, seg := range segs[1:] {
			if isElementSegment(seg) {
				return false
			}
		}
		next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, next)
	}
	return insertAbsent(next, segs[1:], value)
}

func findElement(seq *yaml.Node, seg string) *yaml.Node {
	for i, el := range seq.Content {
		if elementSegment(el, i, nil) == seg {
			return el
		}
	}
	return nil
}

// collectEnvRefs returns the references in raw (the file as written) paired
// with what they resolved to in loaded. It returns nil when the file carries
// none, or when it cannot be parsed without expansion.
func collectEnvRefs(raw []byte, loaded *Config) map[string]envRef {
	if !strings.ContainsRune(string(raw), '$') {
		return nil
	}
	var rawDoc yaml.Node
	if err := yaml.Unmarshal(raw, &rawDoc); err != nil {
		return nil
	}
	rawRoot := mappingRoot(&rawDoc)
	if rawRoot == nil {
		return nil
	}

	refs := map[string]envRef{}
	seen := map[string]int{}
	walkScalars(rawRoot, nil, expandEnvPreservingShellParams, func(segs []string, n *yaml.Node) {
		path := envRefPath(segs...)
		seen[path]++
		if strings.ContainsRune(n.Value, '$') {
			refs[path] = envRef{raw: n.Value, segments: segs}
		}
	})
	// Two entries with the same name share a path. Restoring the reference
	// could put one entry's literal on the other, so neither is restored.
	for path, n := range seen {
		if n > 1 {
			delete(refs, path)
		}
	}
	if len(refs) == 0 {
		return nil
	}

	data, err := yaml.Marshal(loaded)
	if err != nil {
		return nil
	}
	var loadedDoc yaml.Node
	if err := yaml.Unmarshal(data, &loadedDoc); err != nil {
		return nil
	}
	if root := mappingRoot(&loadedDoc); root != nil {
		walkScalars(root, nil, nil, func(segs []string, n *yaml.Node) {
			path := envRefPath(segs...)
			if ref, ok := refs[path]; ok {
				ref.loaded, ref.present = n.Value, true
				refs[path] = ref
			}
		})
	}
	return refs
}

// restoreEnvRefs rewrites every value in the freshly marshaled document that
// is unchanged since load back to the reference it was loaded from. Like
// preserveUnknownFields it is best-effort: if anything fails to parse or
// marshal, fresh is returned unchanged.
func (c *Config) restoreEnvRefs(fresh []byte) []byte {
	if len(c.envRefs) == 0 {
		return fresh
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(fresh, &doc); err != nil {
		return fresh
	}
	root := mappingRoot(&doc)
	if root == nil {
		return fresh
	}

	changed := false
	walkScalars(root, nil, nil, func(segs []string, n *yaml.Node) {
		ref, ok := c.envRefs[envRefPath(segs...)]
		if !ok || !ref.present || n.Value != ref.loaded || n.Value == ref.raw {
			return
		}
		n.Value, n.Tag, n.Style = ref.raw, "!!str", 0
		changed = true
	})
	// Sorted, so re-created blocks come out in the same order on every save.
	paths := make([]string, 0, len(c.envRefs))
	for path, ref := range c.envRefs {
		if !ref.present {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		ref := c.envRefs[path]
		if insertAbsent(root, ref.segments, ref.raw) {
			changed = true
		}
	}
	if !changed {
		return fresh
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return fresh
	}
	var check yaml.Node
	if err := yaml.Unmarshal(out, &check); err != nil {
		return fresh
	}
	return out
}

// tokenFromEnvRef reports whether the named credential's inline value is an
// environment-variable reference: either loaded from one and unchanged since,
// or still the literal reference (set by SetToken on this Config, or loaded
// without expansion).
func (c *Config) tokenFromEnvRef(name string) bool {
	_, _, ok := c.envRefToken(name)
	return ok
}

// envRefToken resolves the named credential when its inline value is an
// environment-variable reference, returning the value (empty when the
// variable is unset) and the reference as written.
func (c *Config) envRefToken(name string) (value, raw string, ok bool) {
	for _, nt := range c.Tokens {
		if nt.Name != name {
			continue
		}
		if IsEnvReference(nt.Token) {
			return expandEnvPreservingShellParams(nt.Token), nt.Token, true
		}
		if ref, found := c.envRefs[tokenValuePath(name)]; found && ref.present && nt.Token == ref.loaded {
			return nt.Token, ref.raw, true
		}
		return "", "", false
	}
	return "", "", false
}

// TokensAsStored returns a copy of c whose inline token values show the
// ${VAR} reference they were loaded from instead of the resolved secret. Use
// it wherever a config is displayed; the resolved values stay in c for
// requests.
func (c *Config) TokensAsStored() *Config {
	out := *c
	out.Tokens = make([]NamedToken, len(c.Tokens))
	copy(out.Tokens, c.Tokens)
	for i, nt := range out.Tokens {
		if ref, ok := c.envRefs[tokenValuePath(nt.Name)]; ok && nt.Token == ref.loaded {
			out.Tokens[i].Token = ref.raw
		}
	}
	return &out
}

// IsEnvReference reports whether s names an environment variable for dtctl to
// resolve when it loads the config: "${VAR}" anywhere in it, or the whole value
// being "$VAR". Such a value must stay in the config file as written. Putting
// it in the keyring would store the literal, which is never expanded there.
func IsEnvReference(s string) bool {
	return strings.Contains(s, "${") || isWholeValueEnvRef(s)
}
