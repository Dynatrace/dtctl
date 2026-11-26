package reposcope

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// legacyDecode is the reader the scope file had before decode read it node by
// node: yaml.v3 fills the struct, a second, strict pass finds unknown keys by
// the text of its errors. It is kept, here only, as the reference decode is
// compared with: every file it read, decode must read to the same value, save
// for what decode refuses on purpose.
func legacyDecode(data []byte) (*File, *InvalidError) {
	unknownFieldRe := regexp.MustCompile(`^line (\d+): field (.+) not found in type reposcope\.(\w+)$`)
	narrowingTypes := map[string]string{"fileEntry": "an entry", "fileWorkload": "a workload", "Segment": "a segment"}

	f := &File{}
	if len(bytes.TrimSpace(data)) == 0 {
		return f, nil
	}
	// It filled the structs that carry the file's spelling in their tags.
	var doc fileDoc
	fill := func() {
		f.APIVersion = doc.APIVersion
		if doc.Environments != nil {
			f.Environments = map[string][]Entry{}
		}
		for host, entries := range doc.Environments {
			var out []Entry
			if entries != nil {
				out = []Entry{}
			}
			for _, e := range entries {
				entry := Entry{Name: e.Name, Path: e.Path, ServiceNames: e.ServiceNames, ProcessGroups: e.ProcessGroups,
					Services: e.Services, Segments: e.Segments, Annotations: e.Annotations}
				if e.Workloads != nil {
					entry.Workloads = []Workload{}
				}
				for _, w := range e.Workloads {
					entry.Workloads = append(entry.Workloads, Workload(w))
				}
				out = append(out, entry)
			}
			f.Environments[host] = out
		}
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			return nil, invalid("does not match the repo scope schema: " + err.Error())
		}
		return nil, invalid("not valid YAML: " + err.Error())
	}
	fill()
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var typeErr *yaml.TypeError
	var narrowing []string
	if err := strict.Decode(&fileDoc{}); errors.As(err, &typeErr) {
		for _, msg := range typeErr.Errors {
			m := unknownFieldRe.FindStringSubmatch(msg)
			if m == nil {
				continue
			}
			key := fmt.Sprintf("%q on line %s", m[2], m[1])
			if place, ok := narrowingTypes[m[3]]; ok {
				narrowing = append(narrowing, key+", in "+place)
			} else {
				f.unknown = append(f.unknown, key)
			}
		}
	}
	if len(narrowing) > 0 {
		return nil, invalid(unknownKeys(narrowing))
	}
	for {
		var doc yaml.Node
		err := strict.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || !emptyDocument(&doc) {
			return nil, invalid("holds more than one YAML document")
		}
	}
	if err := f.check(); err != nil {
		return nil, err
	}
	return f, nil
}

// stricter are the refusals decode adds to what the legacy reader refused,
// each a value the legacy reader read past or read as something the line
// does not show.
var stricter = regexp.MustCompile(strings.Join([]string{
	` has no value$`,
	`^the anchor &`,
	`^the merge key << `,
	`^the key on line \d+ must be a plain string$`,
	`!!binary data$`,
	`^holds more than one '---' after its document$`,
	// A key the legacy reader's pattern could not see: empty, or of more
	// than one line.
	`^unknown keys? .*"(|[^"]*\\n[^"]*)" on line \d+, in `,
}, "|"))

// unseenKey is a document key the legacy reader's pattern could not see, as
// Warning names it. decode reports it; the legacy reader let Save drop it.
var unseenKey = regexp.MustCompile(`^"(|.*\\n.*)" on line \d+$`)

// compareWithLegacy holds decode to the legacy reader on one input.
func compareWithLegacy(t *testing.T, doc string) (bothRead bool) {
	t.Helper()
	old, oldErr := legacyDecode([]byte(doc))
	got, err := decode([]byte(doc))
	switch {
	case oldErr == nil && err == nil:
		var seen []string
		for _, key := range got.unknown {
			if !unseenKey.MatchString(key) {
				seen = append(seen, key)
			}
		}
		got.unknown = seen
		require.Equal(t, old, got, "read differently:\n%s", doc)
		return true
	case oldErr == nil:
		require.Regexp(t, stricter, err.Msg, "refuses a file that used to load:\n%s", doc)
	case err == nil:
		require.Failf(t, "reads a file that used to be refused", "%s\n\n%s", oldErr.Msg, doc)
	}
	return false
}

func TestDecode_ReadsWhatTheLegacyReaderRead(t *testing.T) {
	t.Run("fixtures", func(t *testing.T) {
		for _, name := range []string{"example.yaml", "newer-dtctl.yaml"} {
			assert.True(t, compareWithLegacy(t, string(readFixture(t, name))), name)
		}
	})
	// Files as people and generators write them: block and flow style, every
	// quoting, comments, numbers and booleans where text is meant, keys in
	// any order. Unedited, the two readers must agree exactly.
	t.Run("generated", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(1, 2))
		read := 0
		for range 3000 {
			doc := randomScopeDoc(rng)
			old, oldErr := legacyDecode([]byte(doc))
			got, err := decode([]byte(doc))
			require.Equal(t, oldErr == nil, err == nil, "%v\n%v\n%s", oldErr, err, doc)
			if err == nil {
				require.Equal(t, old, got, "read differently:\n%s", doc)
				read++
			}
		}
		assert.Greater(t, read, 1500, "most generated files are valid ones")
	})
	// The same files after a careless edit. decode may refuse more than the
	// legacy reader did, only for the reasons in stricter; it never reads
	// what was refused, and never reads a file to another value.
	t.Run("edited", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(3, 4))
		read := 0
		for range 6000 {
			doc := randomScopeDoc(rng)
			for range 1 + rng.IntN(2) {
				doc = edit(rng, doc)
			}
			if compareWithLegacy(t, doc) {
				read++
			}
		}
		assert.Greater(t, read, 500, "some edits leave a valid file")
	})
}

// mostly returns one of good, and now and then bad: a value the format
// refuses.
func mostly[T any](rng *rand.Rand, bad T, good ...T) T {
	if rng.IntN(60) == 0 {
		return bad
	}
	return pick(rng, good...)
}

// FuzzDecode holds decode to the legacy reader on whatever the fuzzer writes:
// it must not panic, must not read what the legacy reader refused, and must
// read every other file to the same value.
func FuzzDecode(f *testing.F) {
	rng := rand.New(rand.NewPCG(5, 6))
	for range 40 {
		f.Add(edit(rng, randomScopeDoc(rng)))
	}
	f.Add("a: &a [b]\n<<: *a\n")
	f.Add("? [a, b]\n: c\n---\n---\n")
	f.Fuzz(func(t *testing.T, doc string) {
		if len(doc) > 4096 {
			return // the legacy reader is quadratic in the keys of a mapping
		}
		compareWithLegacy(t, doc)
	})
}

// pick returns one of options.
func pick[T any](rng *rand.Rand, options ...T) T { return options[rng.IntN(len(options))] }

// scalar spells v in one of the styles YAML has for it.
func scalar(rng *rand.Rand, v string) string {
	plain := regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`).MatchString(v)
	switch style := rng.IntN(4); {
	case style == 0 && !strings.Contains(v, "'"):
		return "'" + v + "'"
	case style == 1 || !plain:
		return fmt.Sprintf("%q", v)
	}
	return v
}

// list spells values as a flow or a block list under a key indented by
// indent.
func list(rng *rand.Rand, indent string, values []string) string {
	if rng.IntN(2) == 0 {
		return " [" + strings.Join(values, ", ") + "]\n"
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, v := range values {
		b.WriteString(indent + "  - " + v + "\n")
	}
	return b.String()
}

// randomScopeDoc writes a scope file, mostly a valid one.
func randomScopeDoc(rng *rand.Rand) string {
	var b strings.Builder
	comment := func() {
		if rng.IntN(8) == 0 {
			b.WriteString("# " + pick(rng, "checkout", "key: value", "- item", "---") + "\n")
		}
	}
	// strs spells n of values, each at most once.
	strs := func(n int, values ...string) []string {
		rng.Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
		out := make([]string, 0, n)
		for _, v := range values[:min(n, len(values))] {
			out = append(out, scalar(rng, v))
		}
		return out
	}
	if rng.IntN(6) == 0 {
		b.WriteString("---\n")
	}
	if rng.IntN(3) == 0 {
		b.WriteString("apiVersion: " + mostly(rng, pick(rng, "v2", "1"), "v1", `"v1"`, "'v1'") + "\n")
	}
	if rng.IntN(10) == 0 {
		b.WriteString(pick(rng, "kind: RepoScope\n", "x-notes:\n  - a\n  - {b: c}\n", "defaults: {}\n"))
	}
	comment()
	b.WriteString("environments:\n")
	hosts := []string{prodHost, stgHost, "dev00001.apps.dynatrace.com"}
	rng.Shuffle(len(hosts), func(i, j int) { hosts[i], hosts[j] = hosts[j], hosts[i] })
	for _, host := range hosts[:1+rng.IntN(3)] {
		b.WriteString("  " + mostly(rng, "Bad_Host", host) + ":\n")
		for e := range 1 + rng.IntN(3) {
			keys := []string{"name", "bindings"}
			if e > 0 || rng.IntN(2) == 0 {
				keys = append(keys, "path")
			}
			if rng.IntN(3) == 0 {
				keys = append(keys, "segments")
			}
			if rng.IntN(60) == 0 {
				keys = append(keys, "typo")
			}
			rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
			lead := "    - "
			for _, key := range keys {
				b.WriteString(lead)
				lead = "      "
				switch key {
				case "name":
					b.WriteString("name: " + scalar(rng, fmt.Sprintf("%s%d", mostly(rng, "Bad", "checkout", "ledger", "e"), e)) + "\n")
				case "path":
					b.WriteString("path: " + scalar(rng, fmt.Sprintf("%s%d", mostly(rng, "../up", "services/checkout", "svc", "a/b/c", "über/x"), e)) + "\n")
				case "typo":
					b.WriteString(pick(rng, "service_names: [checkout]\n", "paths: [a]\n", "owner: team\n"))
				case "segments":
					b.WriteString("segments:\n")
					for s := range 1 + rng.IntN(2) {
						b.WriteString(fmt.Sprintf("        - id: %s\n", scalar(rng, fmt.Sprintf("Ab3dEf9hIjk%d", s))))
						if rng.IntN(2) == 0 {
							if rng.IntN(2) == 0 {
								b.WriteString("          variables: {env: [prod, " + scalar(rng, pick(rng, "eu", "1", "true")) + "], k8s.namespace.name: [payments]}\n")
							} else {
								b.WriteString("          variables:\n            env:" + list(rng, "            ", strs(1+rng.IntN(2), "prod", "stage", "2")))
							}
						}
						if rng.IntN(60) == 0 {
							b.WriteString("          variable: {env: [prod]}\n")
						}
					}
				case "bindings":
					kinds := []string{"workloads", "service-names", "process-groups", "services"}
					rng.Shuffle(len(kinds), func(i, j int) { kinds[i], kinds[j] = kinds[j], kinds[i] })
					for i, kind := range kinds[:1+rng.IntN(3)] {
						if i > 0 {
							b.WriteString(lead)
						}
						switch kind {
						case "service-names":
							b.WriteString("service-names:" + list(rng, "      ", strs(1+rng.IntN(3), "checkout", "Kasse EU", "123", "true", "1.5", "2026-09-30", "0x1F", "null-ish", mostly(rng, "", "~x"))))
						case "process-groups":
							b.WriteString("process-groups:" + list(rng, "      ", strs(1+rng.IntN(2), pgID, mostly(rng, "PG-1", "PROCESS_GROUP-0000000000000001"))))
						case "services":
							b.WriteString("services:" + list(rng, "      ", strs(1+rng.IntN(2), svcID, "SERVICE-0000000000000001")))
						case "workloads":
							b.WriteString("workloads:\n")
							for w := range 1 + rng.IntN(2) {
								cluster := ""
								if rng.IntN(3) == 0 {
									cluster = "cluster-uid: " + scalar(rng, mostly(rng, "prod-eu", clusterUID))
								}
								ns, name := "namespace: "+scalar(rng, pick(rng, "payments", "batch", "123")), "name: "+scalar(rng, fmt.Sprintf("w%d", w))
								extra := ""
								if rng.IntN(80) == 0 {
									extra = ", container: app"
								}
								if rng.IntN(2) == 0 {
									parts := []string{ns, name}
									if cluster != "" {
										parts = append(parts, cluster)
									}
									b.WriteString("        - {" + strings.Join(parts, ", ") + extra + "}\n")
									continue
								}
								b.WriteString("        - " + name + "\n          " + ns + "\n")
								if cluster != "" {
									b.WriteString("          " + cluster + "\n")
								}
							}
						}
					}
				}
				comment()
			}
		}
	}
	b.WriteString(mostly(rng, "---\nmore: true\n", "", "", "", "---\n", "...\n", "---\n# end\n"))
	return b.String()
}

// edit makes one careless change to a line of doc.
func edit(rng *rand.Rand, doc string) string {
	lines := strings.SplitAfter(doc, "\n")
	i := rng.IntN(len(lines))
	line := lines[i]
	key, value, keyed := strings.Cut(strings.TrimRight(line, "\n"), ":")
	switch rng.IntN(9) {
	case 0: // drop the line
		lines[i] = ""
	case 1: // give it twice
		lines[i] = line + line
	case 2: // leave the value out
		if keyed {
			lines[i] = key + ":" + pick(rng, "", " ~", " null", ` ""`, " []", " {}", " [~]", " [null, a]") + "\n"
		}
	case 3: // another shape
		if keyed {
			lines[i] = key + ":" + pick(rng, " 1", " true", " [a]", " {a: b}", " [[a]]", " !!binary YQ==", " !!str 5", " |\n"+strings.Repeat(" ", 20)+"text") + "\n"
		}
	case 4: // an odd key
		if keyed {
			lead := key[:len(key)-len(strings.TrimLeft(key, " -"))]
			lines[i] = lead + pick(rng, `""`, "~", `"a\nb"`, "1", "<<", `"<<"`, "? [a]\n"+lead, strings.ToUpper(strings.TrimLeft(key, " -"))) + ":" + value + "\n"
		}
	case 5: // an anchor, and an alias of it later
		if keyed && strings.TrimSpace(value) != "" {
			lines[i] = key + ": &a" + value + "\n"
			if j := i + 1 + rng.IntN(len(lines)-i); j < len(lines) {
				if k, _, ok := strings.Cut(lines[j], ":"); ok {
					lines[j] = k + ": *a\n"
				}
			}
		}
	case 6: // an item left empty
		if strings.Contains(line, "- ") {
			lines[i] = line[:strings.Index(line, "- ")+1] + pick(rng, "", " ~", " null") + "\n"
		}
	case 7: // indentation
		lines[i] = pick(rng, "  ", " ") + line
	case 8: // more documents
		lines[i] = line + pick(rng, "---\n", "---\n---\n", "---\n{unclosed\n")
	}
	return strings.Join(lines, "")
}
