package reposcope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	prodHost = "abc12345.apps.dynatrace.com"
	stgHost  = "stg98765.apps.dynatrace.com"
	svcID    = "SERVICE-0123456789ABCDEF"
	pgID     = "PROCESS_GROUP-FEDCBA9876543210"
)

// readFixture reads a file under testdata/scope.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "scope", name))
	require.NoError(t, err)
	return data
}

// exampleFile is testdata/scope/example.yaml as a value.
func exampleFile() *File {
	return &File{Environments: map[string][]Entry{
		prodHost: {
			{
				Name:          "checkout",
				Path:          "services/checkout",
				Services:      []string{svcID},
				ProcessGroups: []string{pgID},
				ServiceNames:  []string{"checkout"},
				Workloads:     []Workload{{Namespace: "payments", Name: "checkout"}},
			},
			{Name: "ledger", Path: "services/ledger", Workloads: []Workload{{Namespace: "payments", Name: "ledger"}}},
		},
		stgHost: {
			{Name: "checkout", Path: "services/checkout", ServiceNames: []string{"checkout"}},
		},
	}}
}

func TestDecode_Example(t *testing.T) {
	f, err := decode(readFixture(t, "example.yaml"))
	require.Nil(t, err)
	assert.Equal(t, exampleFile(), f)
}

func TestDecode_EmptyIsAnEmptyFile(t *testing.T) {
	for _, in := range []string{"", "\n", "# only a comment\n"} {
		f, err := decode([]byte(in))
		require.Nil(t, err, "%q", in)
		assert.True(t, f.Empty())
	}
}

func TestDecode_UnknownKeysAreAWarning(t *testing.T) {
	f, err := decode(readFixture(t, "newer-dtctl.yaml"))
	require.Nil(t, err, "unknown keys do not stop a read")
	assert.Equal(t, `unknown keys "kind" on line 3, "discovered" on line 8, "container" on line 12 (a typo, or written by a newer dtctl)`, f.Warning())
	assert.Equal(t, []string{"checkout"}, f.Entries(prodHost)[0].ServiceNames)

	f, err = decode([]byte("environments:\n  " + prodHost + ":\n    - name: checkout\n      service_names: [checkout]\n      services: [" + svcID + "]\n"))
	require.Nil(t, err)
	assert.Equal(t, `unknown key "service_names" on line 4 (a typo, or written by a newer dtctl)`, f.Warning())

	f, err = decode(readFixture(t, "example.yaml"))
	require.Nil(t, err)
	assert.Empty(t, f.Warning())
}

func TestCheck(t *testing.T) {
	// entry renders a one-entry document for prodHost from the given entry lines.
	entry := func(lines ...string) string {
		return "environments:\n  " + prodHost + ":\n    - " + strings.Join(lines, "\n      ") + "\n"
	}
	tests := []struct {
		name string
		doc  string
		msg  string // "" means valid
	}{
		{name: "minimal", doc: entry("name: checkout", "service-names: [checkout]")},
		{name: "apiVersion v1", doc: "apiVersion: v1\n" + entry("name: checkout", "service-names: [checkout]")},
		{name: "a name at its longest", doc: entry("name: "+strings.Repeat("a", 63), "service-names: [checkout]")},
		{name: "unicode service name", doc: entry("name: checkout", "service-names: [\"Kasse – EU\"]")},
		{name: "workload name with a dot", doc: entry("name: checkout", "workloads: [{namespace: payments, name: checkout.v2}]")},
		{name: "workload name longer than a DNS label", doc: entry("name: checkout", "workloads: [{namespace: payments, name: "+strings.Repeat("a", 64)+"}]")},
		{name: "workload name at its longest", doc: entry("name: checkout", "workloads: [{namespace: payments, name: "+strings.Repeat("a", 253)+"}]")},
		{name: "a leading document marker", doc: "---\n" + entry("name: checkout", "service-names: [checkout]")},
		{name: "a trailing document marker", doc: entry("name: checkout", "service-names: [checkout]") + "---\n"},
		{name: "a trailing document holding a comment", doc: entry("name: checkout", "service-names: [checkout]") + "---\n# end\n"},
		{name: "a trailing document ended at once", doc: entry("name: checkout", "service-names: [checkout]") + "---\n...\n"},
		{name: "a trailing explicit null", doc: entry("name: checkout", "service-names: [checkout]") + "---\n~\n"},

		{name: "apiVersion v2", doc: "apiVersion: v2\n" + entry("name: checkout", "service-names: [checkout]"),
			msg: `apiVersion "v2" is not supported (this dtctl reads "v1")`},
		{name: "uppercase host", doc: "environments:\n  ABC12345.apps.dynatrace.com:\n    - name: checkout\n      service-names: [checkout]\n",
			msg: `environment "ABC12345.apps.dynatrace.com" must be written as the bare host "abc12345.apps.dynatrace.com"`},
		{name: "host with a scheme", doc: "environments:\n  https://abc12345.apps.dynatrace.com:\n    - name: checkout\n      service-names: [checkout]\n",
			msg: `must be written as the bare host "abc12345.apps.dynatrace.com"`},
		{name: "host with a port", doc: "environments:\n  \"abc12345.apps.dynatrace.com:443\":\n    - name: checkout\n      service-names: [checkout]\n",
			msg: `must be written as the bare host "abc12345.apps.dynatrace.com"`},
		{name: "host with a trailing dot", doc: "environments:\n  abc12345.apps.dynatrace.com.:\n    - name: checkout\n      service-names: [checkout]\n",
			msg: `must be written as the bare host "abc12345.apps.dynatrace.com"`},
		{name: "two spellings of one host", doc: "environments:\n  ABC12345.apps.dynatrace.com:\n    - name: a1\n      service-names: [checkout]\n  abc12345.apps.dynatrace.com:\n    - name: a2\n      service-names: [checkout]\n",
			msg: `environments "ABC12345.apps.dynatrace.com" and "abc12345.apps.dynatrace.com" name the same host`},
		{name: "not a host", doc: "environments:\n  abc_12345:\n    - name: checkout\n      service-names: [checkout]\n",
			msg: `environment "abc_12345" is not a host name`},

		{name: "name with capitals", doc: entry("name: Checkout", "service-names: [checkout]"),
			msg: `environments[abc12345.apps.dynatrace.com] entry "Checkout": name "Checkout" must be lowercase letters, digits and dashes (at most 63)`},
		{name: "missing name", doc: entry("service-names: [checkout]"),
			msg: `environments[abc12345.apps.dynatrace.com][0]: name "" must be lowercase letters`},
		{name: "name too long", doc: entry("name: "+strings.Repeat("a", 64), "service-names: [checkout]"),
			msg: "must be lowercase letters, digits and dashes (at most 63)"},
		{name: "duplicate name",
			doc: entry("name: checkout", "service-names: [a1x]") + "    - name: checkout\n      path: x\n      service-names: [b1x]\n",
			msg: `entry "checkout": the name is used twice`},
		{name: "two repository-wide entries",
			doc: entry("name: one", "service-names: [a1x]") + "    - name: two\n      service-names: [b1x]\n",
			msg: `entry "two": entry "one" already covers the whole repository`},
		{name: "duplicate path",
			doc: entry("name: one", "path: services/a", "service-names: [a1x]") + "    - name: two\n      path: services/a\n      service-names: [b1x]\n",
			msg: `entry "two": entry "one" already covers "services/a"`},
		{name: "paths differing only in case",
			doc: entry("name: upper", "path: Services/Checkout", "service-names: [upper]") + "    - name: lower\n      path: services/checkout\n      service-names: [lower]\n",
			msg: `entry "lower": entry "upper" already covers "Services/Checkout", the same directory as "services/checkout" on macOS and Windows`},
		{name: "path leaving the repository", doc: entry("name: checkout", "path: ../elsewhere", "service-names: [checkout]"),
			msg: `path "../elsewhere" leaves the repository`},
		{name: "path through a parent", doc: entry("name: checkout", "path: a/../../b", "service-names: [checkout]"),
			msg: `path "a/../../b" leaves the repository`},
		{name: "absolute path", doc: entry("name: checkout", "path: /srv/repo", "service-names: [checkout]"),
			msg: `path "/srv/repo" must be relative to the repository root`},
		{name: "backslash path", doc: entry("name: checkout", `path: 'services\checkout'`, "service-names: [checkout]"),
			msg: `must use forward slashes`},
		{name: "dot path", doc: entry("name: checkout", "path: .", "service-names: [checkout]"),
			msg: `path "." is the repository root`},
		{name: "unclean path", doc: entry("name: checkout", "path: services/./checkout/", "service-names: [checkout]"),
			msg: `path "services/./checkout/" is not clean (write it as "services/checkout")`},

		{name: "short service id", doc: entry("name: checkout", "services: [SERVICE-123]"),
			msg: `services[0] "SERVICE-123" is not a service id (SERVICE- and 16 hex digits)`},
		{name: "lowercase service id", doc: entry("name: checkout", "services: [SERVICE-0123456789abcdef]"),
			msg: `is not a service id`},
		{name: "process group id as a service", doc: entry("name: checkout", "services: ["+pgID+"]"),
			msg: `is not a service id`},
		{name: "bad process group id", doc: entry("name: checkout", "process-groups: [PG-1]"),
			msg: `process-groups[0] "PG-1" is not a process group id (PROCESS_GROUP- and 16 hex digits)`},
		{name: "service name with a quote", doc: entry("name: checkout", `service-names: ['say "hi"']`),
			msg: `service-names[0] "say \"hi\"" must not contain " or \`},
		{name: "service name with a backslash", doc: entry("name: checkout", `service-names: ['a\b']`),
			msg: `must not contain " or \`},
		{name: "service name with a newline", doc: entry("name: checkout", `service-names: ["a\nb"]`),
			msg: `must not contain control or non-printing characters`},
		{name: "empty service name", doc: entry("name: checkout", `service-names: [""]`),
			msg: `service-names[0] "" is empty`},
		{name: "service name too long", doc: entry("name: checkout", "service-names: ["+strings.Repeat("x", 256)+"]"),
			msg: `is longer than 255 characters`},
		{name: "workload namespace with capitals", doc: entry("name: checkout", "workloads: [{namespace: Payments, name: checkout}]"),
			msg: `workloads[0] namespace "Payments" is not a Kubernetes namespace name`},
		{name: "workload without a namespace", doc: entry("name: checkout", "workloads: [{name: checkout}]"),
			msg: `workloads[0] namespace "" is not a Kubernetes namespace name`},
		{name: "workload name too long", doc: entry("name: checkout", "workloads: [{namespace: payments, name: "+strings.Repeat("a", 254)+"}]"),
			msg: `is not a Kubernetes workload name`},
		{name: "workload name with an underscore", doc: entry("name: checkout", "workloads: [{namespace: payments, name: check_out}]"),
			msg: `workloads[0] name "check_out" is not a Kubernetes workload name`},
		{name: "workload name with an empty label", doc: entry("name: checkout", "workloads: [{namespace: payments, name: checkout..v2}]"),
			msg: `workloads[0] name "checkout..v2" is not a Kubernetes workload name`},
		{name: "namespace with a dot", doc: entry("name: checkout", "workloads: [{namespace: pay.ments, name: checkout}]"),
			msg: `workloads[0] namespace "pay.ments" is not a Kubernetes namespace name`},
		{name: "no binding", doc: entry("name: checkout", "path: services/checkout"),
			msg: `entry "checkout": binds nothing`},

		{name: "not YAML", doc: "environments: [unclosed\n", msg: "not valid YAML"},
		{name: "a second document",
			doc: entry("name: first", "service-names: [first]") + "---\nenvironments:\n  other.invalid:\n    - name: second\n      service-names: [second]\n",
			msg: "holds more than one YAML document"},
		{name: "a second document holding only a list", doc: entry("name: checkout", "service-names: [checkout]") + "---\n- checkout\n",
			msg: "holds more than one YAML document"},
		{name: "wrong shape", doc: entry("name: checkout", "services: SERVICE-0123456789ABCDEF"), msg: "does not match the repo scope schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := decode([]byte(tt.doc))
			if tt.msg == "" {
				require.Nil(t, err)
				require.Nil(t, f.check())
				return
			}
			require.NotNil(t, err)
			assert.Equal(t, FileName, err.Source)
			assert.Contains(t, err.Msg, tt.msg)
			assert.True(t, strings.HasPrefix(err.Error(), FileName+": "), err.Error())
		})
	}
}

func TestCheck_NoBindingSuggestsWhatToAdd(t *testing.T) {
	f := &File{Environments: map[string][]Entry{prodHost: {{Name: "checkout"}}}}
	invalidErr := f.check()
	require.NotNil(t, invalidErr)
	assert.Equal(t, []string{"add at least one of services, process-groups, service-names or workloads"}, invalidErr.Suggestions)
}

func TestUpsertRemove(t *testing.T) {
	f := &File{}
	f.Upsert(prodHost, Entry{Name: "checkout", ServiceNames: []string{"checkout"}})
	f.Upsert(prodHost, Entry{Name: "ledger", ServiceNames: []string{"ledger"}})
	f.Upsert(stgHost, Entry{Name: "checkout", ServiceNames: []string{"checkout"}})
	assert.Equal(t, []string{prodHost, stgHost}, f.Hosts())
	assert.Equal(t, []string{"checkout", "ledger"}, f.Names(prodHost))

	// Upsert replaces: no merge with what was there.
	f.Upsert(prodHost, Entry{Name: "checkout", Services: []string{svcID}})
	assert.Equal(t, []Entry{
		{Name: "checkout", Services: []string{svcID}},
		{Name: "ledger", ServiceNames: []string{"ledger"}},
	}, f.Entries(prodHost))

	assert.False(t, f.Remove(prodHost, "billing"))
	assert.False(t, f.Remove("unknown.example.invalid", "checkout"))
	assert.True(t, f.Remove(prodHost, "checkout"))
	assert.Equal(t, []string{"ledger"}, f.Names(prodHost))

	assert.True(t, f.Remove(stgHost, "checkout"))
	assert.Equal(t, []string{prodHost}, f.Hosts(), "an environment left empty goes with its last entry")
	assert.Nil(t, f.Entries(stgHost))

	assert.False(t, f.Empty())
	assert.True(t, f.Remove(prodHost, "ledger"))
	assert.True(t, f.Empty())
}

func TestCheckInvalidUTF8ServiceName(t *testing.T) {
	f := &File{}
	f.Upsert(prodHost, Entry{Name: "checkout", ServiceNames: []string{string([]byte{0xff})}})
	err := f.check()
	require.NotNil(t, err)
	assert.Contains(t, err.Msg, "is not valid UTF-8")
}

func TestCheckHostWithPathOrQuery(t *testing.T) {
	for _, suffix := range []string{"/path", "?query=value", "#fragment"} {
		f := &File{}
		f.Upsert(prodHost+suffix, Entry{Name: "checkout", ServiceNames: []string{"checkout"}})
		err := f.check()
		require.NotNil(t, err)
		assert.Contains(t, err.Msg, prodHost)
	}
}
