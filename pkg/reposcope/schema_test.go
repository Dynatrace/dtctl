package reposcope

import (
	"fmt"
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
	// clusterUID is a k8s.cluster.uid: the UID of a cluster's kube-system
	// namespace.
	clusterUID = "11111111-2222-3333-4444-555555555555"
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
	return &File{APIVersion: apiVersion, Environments: map[string][]Entry{
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
	for _, in := range []string{"", "\n", "\f\n", "# only a comment\n"} {
		f, err := decode([]byte(in))
		require.Nil(t, err, "%q", in)
		assert.True(t, f.Empty())
	}
}

// A key of the document itself that this dtctl does not know is read past
// and reported: it sits beside the entries and cannot change what one
// selects.
func TestDecode_UnknownDocumentKeysAreAWarning(t *testing.T) {
	f, err := decode(readFixture(t, "newer-dtctl.yaml"))
	require.Nil(t, err, "an unknown document key does not stop a read")
	assert.Equal(t, `unknown key "kind" on line 3 (a typo, or written by a newer dtctl)`, f.Warning())
	assert.Equal(t, []string{"checkout"}, f.Entries(prodHost)[0].ServiceNames)

	f, err = decode(readFixture(t, "example.yaml"))
	require.Nil(t, err)
	assert.Empty(t, f.Warning())
}

// A key inside an entry is where a constraint lives. Read past, a misspelt
// cluster-uid would leave the workload bound in every cluster, and the scope
// would be reported as applied while selecting more than the file says. Such
// a key fails the read.
func TestDecode_UnknownKeysOfAnEntryFailTheRead(t *testing.T) {
	head := "environments:\n  " + prodHost + ":\n    - name: checkout\n"
	for name, tc := range map[string]struct{ body, msg string }{
		"a misspelt cluster-uid": {
			"      workloads:\n        - cluster_uid: " + clusterUID + "\n          namespace: payments\n          name: checkout\n",
			`unknown key "cluster_uid" on line 5, in a workload (a typo, or written by a newer dtctl); a key there can narrow the entry, so the file is not applied without it`},
		"a misspelt binding": {
			"      service_names: [checkout]\n      services: [" + svcID + "]\n",
			`unknown key "service_names" on line 4, in an entry (a typo, or written by a newer dtctl); a key there can narrow the entry, so the file is not applied without it`},
		"a misspelt segment key": {
			"      service-names: [checkout]\n      segments:\n        - id: Ab3dEf9hIjk\n          variable: {env: [prod]}\n",
			`unknown key "variable" on line 7, in a segment (a typo, or written by a newer dtctl); a key there can narrow the entry, so the file is not applied without it`},
		"several": {
			"      service-names: [checkout]\n      discovered: 2026-09-30\n      workloads:\n        - namespace: payments\n          name: checkout\n          container: app\n",
			`unknown keys "discovered" on line 5, in an entry, "container" on line 9, in a workload (a typo, or written by a newer dtctl); a key there can narrow the entry, so the file is not applied without them`},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := decode([]byte(head + tc.body))
			require.NotNil(t, err)
			assert.Nil(t, f)
			assert.Equal(t, tc.msg, err.Msg)
			assert.Equal(t, []string{"correct the key, or upgrade dtctl if a newer one wrote it"}, err.Suggestions)
		})
	}
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
		{name: "workload in a cluster", doc: entry("name: checkout", "workloads: [{cluster-uid: "+clusterUID+", namespace: payments, name: checkout}]")},
		{name: "path with a space and a colon inside", doc: entry("name: checkout", "path: 'my services/check:out'", "service-names: [checkout]")},
		{name: "path in composed Unicode", doc: entry("name: checkout", "path: caf\u00e9", "service-names: [checkout]")},
		{name: "one value under two variables", doc: entry("name: checkout", "segments: [{id: Ab3dEf9hIjk, variables: {env: [prod], stage: [prod]}}]")},
		{name: "one workload name in two namespaces and two clusters", doc: entry("name: checkout", "workloads: [{namespace: payments, name: checkout}, {namespace: batch, name: checkout}, {cluster-uid: "+clusterUID+", namespace: batch2, name: checkout}]")},
		{name: "host at its longest", doc: "environments:\n  " + strings.Repeat("a.", 126) + "a:\n    - name: checkout\n      service-names: [checkout]\n"},
		{name: "a leading document marker", doc: "---\n" + entry("name: checkout", "service-names: [checkout]")},
		{name: "a trailing document marker", doc: entry("name: checkout", "service-names: [checkout]") + "---\n"},
		{name: "a trailing document holding a comment", doc: entry("name: checkout", "service-names: [checkout]") + "---\n# end\n"},
		{name: "a trailing document ended at once", doc: entry("name: checkout", "service-names: [checkout]") + "---\n...\n"},
		{name: "a trailing explicit null", doc: entry("name: checkout", "service-names: [checkout]") + "---\n~\n"},

		{name: "apiVersion v2", doc: "apiVersion: v2\n" + entry("name: checkout", "service-names: [checkout]"),
			msg: `apiVersion "v2" on line 1 is not supported (this dtctl reads "v1")`},
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

		{name: "host longer than a host name", doc: "environments:\n  " + strings.Repeat("a.", 126) + "ab:\n    - name: checkout\n      service-names: [checkout]\n",
			msg: `is not a host name`},
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
		{name: "paths differing only in a letter case folding joins",
			doc: entry("name: long", "path: ſvc", "service-names: [long]") + "    - name: plain\n      path: svc\n      service-names: [plain]\n",
			msg: `entry "plain": entry "long" already covers "ſvc", the same directory as "svc" on macOS and Windows`},
		{name: "path in decomposed Unicode", doc: entry("name: checkout", "path: \"cafe\\u0301\"", "service-names: [checkout]"),
			msg: `path "cafe\u0301" is not in Unicode NFC, the form a path is compared in (write it as "caf\u00e9")`},
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
		{name: "path through a parent that stays inside", doc: entry("name: checkout", "path: a/b/../c", "service-names: [checkout]"),
			msg: `path "a/b/../c" is not clean (write it as "a/c")`},
		{name: "path with an escape sequence", doc: entry("name: checkout", `path: "services/\e[31mred"`, "service-names: [checkout]"),
			msg: `path "services/\x1b[31mred" must not contain control or non-printing characters`},
		{name: "path of two lines", doc: entry("name: checkout", `path: "a\nb"`, "service-names: [checkout]"),
			msg: `path "a\nb" must not contain control or non-printing characters`},
		{name: "path with a NUL", doc: entry("name: checkout", `path: "a\0b"`, "service-names: [checkout]"),
			msg: `path "a\x00b" must not contain control or non-printing characters`},
		{name: "path with a line separator", doc: entry("name: checkout", `path: "a\u2028b"`, "service-names: [checkout]"),
			msg: `must not contain control or non-printing characters`},
		{name: "path on a Windows drive", doc: entry("name: checkout", "path: C:/work/svc", "service-names: [checkout]"),
			msg: `path "C:/work/svc" must be relative to the repository root`},
		{name: "path with a drive and no slash", doc: entry("name: checkout", "path: 'c:svc'", "service-names: [checkout]"),
			msg: `path "c:svc" must be relative to the repository root`},
		{name: "path starting with a space", doc: entry("name: checkout", "path: ' svc'", "service-names: [checkout]"),
			msg: `path " svc" must not start or end with a space`},
		{name: "path ending with a space", doc: entry("name: checkout", "path: 'svc '", "service-names: [checkout]"),
			msg: `path "svc " must not start or end with a space`},

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
		{name: "cluster uid in capitals", doc: entry("name: checkout", "workloads: [{cluster-uid: ABCDEF01-2222-3333-4444-555555555555, namespace: payments, name: checkout}]"),
			msg: `workloads[0] cluster-uid "ABCDEF01-2222-3333-4444-555555555555" is not a Kubernetes cluster uid (a lowercase UUID, the value of k8s.cluster.uid)`},
		{name: "cluster name for a uid", doc: entry("name: checkout", "workloads: [{cluster-uid: prod-eu, namespace: payments, name: checkout}]"),
			msg: `workloads[0] cluster-uid "prod-eu" is not a Kubernetes cluster uid`},
		{name: "workload listed twice", doc: entry("name: checkout", "workloads: [{namespace: payments, name: checkout}, {namespace: payments, name: checkout}]"),
			msg: `workloads[1] payments/checkout is listed twice`},
		{name: "segment named, not identified", doc: entry("name: checkout", "segments: [{id: Payments prod}]"),
			msg: `segments[0] id "Payments prod" is not a filter segment id (letters, digits, - and _; a name is not accepted)`},
		{name: "segment without an id", doc: entry("name: checkout", "segments: [{variables: {env: [prod]}}]"),
			msg: `segments[0] id "" is not a filter segment id`},
		{name: "segment listed twice", doc: entry("name: checkout", "segments: [{id: Ab3dEf9hIjk}, {id: Ab3dEf9hIjk}]"),
			msg: `segments[1] id "Ab3dEf9hIjk" is listed twice`},
		{name: "segment variable name", doc: entry("name: checkout", `segments: [{id: Ab3dEf9hIjk, variables: {"env name": [prod]}}]`),
			msg: `segments[0] variable "env name" is not a variable name`},
		{name: "segment variable without values", doc: entry("name: checkout", "segments: [{id: Ab3dEf9hIjk, variables: {env: []}}]"),
			msg: `segments[0] variable "env" has no values`},
		{name: "segment variable value with a separator", doc: entry("name: checkout", `segments: [{id: Ab3dEf9hIjk, variables: {env: ["prod,eu"]}}]`),
			msg: `segments[0] variable "env" value "prod,eu" must not contain , & or =, which separate values where a segment is spelt as --segment takes it`},
		{name: "segment variable value with an assignment", doc: entry("name: checkout", `segments: [{id: Ab3dEf9hIjk, variables: {env: ["a=b"]}}]`),
			msg: `segments[0] variable "env" value "a=b" must not contain , & or =, which separate values where a segment is spelt as --segment takes it`},
		{name: "segment variable control character", doc: entry("name: checkout", `segments: [{id: Ab3dEf9hIjk, variables: {env: ["a\tb"]}}]`),
			msg: `segments[0] variable "env" value "a\tb" must not contain control or non-printing characters`},
		{name: "segment variable value with a space around it", doc: entry("name: checkout", `segments: [{id: Ab3dEf9hIjk, variables: {env: [" prod "]}}]`),
			msg: `segments[0] variable "env" value " prod " must not start or end with a space`},
		{name: "segment variable value listed twice", doc: entry("name: checkout", `segments: [{id: Ab3dEf9hIjk, variables: {env: [prod, stage, prod]}}]`),
			msg: `segments[0] variable "env" value "prod" is listed twice`},
		{name: "service name listed twice", doc: entry("name: checkout", "service-names: [checkout, cart, checkout]"),
			msg: `service-names[2] "checkout" is listed twice`},
		{name: "process group listed twice", doc: entry("name: checkout", "process-groups: ["+pgID+", "+pgID+"]"),
			msg: `process-groups[1] "` + pgID + `" is listed twice`},
		{name: "service listed twice", doc: entry("name: checkout", "services: ["+svcID+", "+svcID+"]"),
			msg: `services[1] "` + svcID + `" is listed twice`},
		{name: "workload with and without a cluster", doc: entry("name: checkout", "workloads: [{cluster-uid: "+clusterUID+", namespace: payments, name: checkout}, {namespace: payments, name: checkout}]"),
			msg: `workloads[1] payments/checkout is listed with and without a cluster; without one it selects the workload in every cluster`},
		{name: "workload without and with a cluster", doc: entry("name: checkout", "workloads: [{namespace: payments, name: checkout}, {namespace: batch, name: checkout}, {cluster-uid: "+clusterUID+", namespace: payments, name: checkout}]"),
			msg: `workloads[2] payments/checkout in cluster ` + clusterUID + ` is listed with and without a cluster; without one it selects the workload in every cluster`},
		{name: "more segments than a query takes", doc: entry("name: checkout", "segments: [{id: s1}, {id: s2}, {id: s3}, {id: s4}, {id: s5}, {id: s6}, {id: s7}, {id: s8}, {id: s9}, {id: s10}, {id: s11}]"),
			msg: "lists 11 segments; a query takes at most 10"},
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

// The limits are part of the format: a file within them is read, and one
// beyond them is refused whole, not read in part.
func TestCheck_Limits(t *testing.T) {
	entries := func(n int) string {
		var b strings.Builder
		b.WriteString("environments:\n  " + prodHost + ":\n")
		for i := range n {
			fmt.Fprintf(&b, "    - name: e%d\n      path: p%d\n      service-names: [s%d]\n", i, i, i)
		}
		return b.String()
	}
	hosts := func(n int) string {
		var b strings.Builder
		b.WriteString("environments:\n")
		for i := range n {
			fmt.Fprintf(&b, "  h%d.example.invalid:\n    - name: checkout\n      service-names: [checkout]\n", i)
		}
		return b.String()
	}
	// join lists n distinct values made by item.
	join := func(n int, item func(int) string) string {
		values := make([]string, n)
		for i := range values {
			values[i] = item(i)
		}
		return strings.Join(values, ", ")
	}
	items := func(n int, item func(int) string) string { return "[" + join(n, item) + "]" }
	entry := func(lines ...string) string {
		return "environments:\n  " + prodHost + ":\n    - name: checkout\n      " + strings.Join(lines, "\n      ") + "\n"
	}
	name := func(i int) string { return fmt.Sprintf("s%d", i) }
	id := func(prefix string) func(int) string {
		return func(i int) string { return fmt.Sprintf("%s-%016X", prefix, i) }
	}
	workload := func(i int) string { return fmt.Sprintf("{namespace: payments, name: w%d}", i) }
	variable := func(i int) string { return fmt.Sprintf("v%d: [a]", i) }
	const over = maxListItems + 1
	for _, tc := range []struct {
		name string
		doc  string
		msg  string
	}{
		{"as many environments as a file takes", hosts(maxEnvironments), ""},
		{"one environment more", hosts(maxEnvironments + 1), "holds 65 environments; a file takes at most 64"},
		{"as many entries as an environment takes", entries(maxEntries), ""},
		{"one entry more", entries(maxEntries + 1), `environment "` + prodHost + `" holds 1001 entries; an environment takes at most 1000`},
		{"as many items as a list takes", entry("service-names: "+items(maxListItems, name), "services: "+items(maxListItems, id("SERVICE")),
			"process-groups: "+items(maxListItems, id("PROCESS_GROUP")), "workloads: "+items(maxListItems, workload),
			"segments: [{id: Ab3dEf9hIjk, variables: {"+join(maxListItems-1, variable)+", env: "+items(maxListItems, name)+"}}]"), ""},
		{"one service name more", entry("service-names: " + items(over, name)), `entry "checkout": service-names holds 1001 items; a list takes at most 1000`},
		{"one service more", entry("services: " + items(over, id("SERVICE"))), "services holds 1001 items; a list takes at most 1000"},
		{"one process group more", entry("process-groups: " + items(over, id("PROCESS_GROUP"))), "process-groups holds 1001 items; a list takes at most 1000"},
		{"one workload more", entry("workloads: " + items(over, workload)), "workloads holds 1001 items; a list takes at most 1000"},
		{"one variable more", entry("segments: [{id: Ab3dEf9hIjk, variables: {" + join(over, variable) + "}}]"),
			"segments[0] holds 1001 variables; a segment takes at most 1000"},
		{"one variable value more", entry("segments: [{id: Ab3dEf9hIjk, variables: {env: " + items(over, name) + "}}]"),
			`segments[0] variable "env" holds 1001 values; a variable takes at most 1000`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decode([]byte(tc.doc))
			if tc.msg == "" {
				require.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			assert.Contains(t, err.Msg, tc.msg)
		})
	}
}

// An environment listed without entries would be a host the file both
// defines and has nothing for.
func TestCheck_EnvironmentWithoutEntries(t *testing.T) {
	_, err := decode([]byte("environments:\n  " + prodHost + ": []\n"))
	require.NotNil(t, err)
	assert.Equal(t, `environment "`+prodHost+`" has no entries`, err.Msg)
	assert.Equal(t, []string{"give it an entry, or delete the line"}, err.Suggestions)
}

func TestCheck_NoBindingSuggestsWhatToAdd(t *testing.T) {
	f := &File{Environments: map[string][]Entry{prodHost: {{Name: "checkout"}}}}
	invalidErr := f.check()
	require.NotNil(t, invalidErr)
	assert.Equal(t, []string{"add at least one of workloads, service-names, process-groups, services or segments"}, invalidErr.Suggestions)
}

// A cluster is part of a workload binding, so one entry can bind the same
// namespace and workload in two clusters, and a file written before the key
// existed reads and writes back unchanged.
func TestWorkloadClusterUID(t *testing.T) {
	const other = "66666666-7777-8888-9999-000000000000"
	doc := "apiVersion: v1\nenvironments:\n  " + prodHost + ":\n    - name: checkout\n      workloads:\n" +
		"        - cluster-uid: " + clusterUID + "\n          namespace: payments\n          name: checkout\n" +
		"        - cluster-uid: " + other + "\n          namespace: payments\n          name: checkout\n" +
		"        - namespace: batch\n          name: checkout\n"
	f, invalidErr := decode([]byte(doc))
	require.Nil(t, invalidErr)
	assert.Empty(t, f.Warning())
	workloads := f.Entries(prodHost)[0].Workloads
	assert.Equal(t, []Workload{
		{ClusterUID: clusterUID, Namespace: "payments", Name: "checkout"},
		{ClusterUID: other, Namespace: "payments", Name: "checkout"},
		{Namespace: "batch", Name: "checkout"},
	}, workloads)
	assert.Equal(t, "payments/checkout in cluster "+clusterUID, workloads[0].String())
	assert.Equal(t, "batch/checkout", workloads[2].String())

	out, err := encode(f)
	require.NoError(t, err)
	assert.Equal(t, fileHeader+doc, string(out))
}

// An entry may reference existing filter segments, beside its bindings or
// alone, and they survive a rewrite of the file as written.
func TestEntrySegments(t *testing.T) {
	doc := "apiVersion: v1\nenvironments:\n  " + prodHost + ":\n    - name: checkout\n      service-names:\n        - checkout\n" +
		"      segments:\n        - id: Ab3dEf9hIjk\n          variables:\n            environment:\n              - production\n" +
		"            k8s.namespace.name:\n              - payments\n              - billing\n" +
		"        - id: plain-segment_1\n" +
		"    - name: everything\n      path: tools\n      segments:\n        - id: Ab3dEf9hIjk\n"
	f, invalidErr := decode([]byte(doc))
	require.Nil(t, invalidErr)
	assert.Empty(t, f.Warning())
	entries := f.Entries(prodHost)
	assert.Equal(t, []Segment{
		{ID: "Ab3dEf9hIjk", Variables: map[string][]string{"environment": {"production"}, "k8s.namespace.name": {"payments", "billing"}}},
		{ID: "plain-segment_1"},
	}, entries[0].Segments)
	assert.Equal(t, "Ab3dEf9hIjk?environment=production&k8s.namespace.name=payments,billing", entries[0].Segments[0].String(),
		"the spelling --segment takes, variables by name")
	assert.Equal(t, "plain-segment_1", entries[0].Segments[1].String())
	assert.Empty(t, entries[1].ServiceNames, "segments alone make an entry")

	out, err := encode(f)
	require.NoError(t, err)
	assert.Equal(t, fileHeader+doc, string(out))
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

func TestCheckInvalidUTF8Path(t *testing.T) {
	f := &File{}
	f.Upsert(prodHost, Entry{Name: "checkout", Path: "svc\xff", ServiceNames: []string{"checkout"}})
	err := f.check()
	require.NotNil(t, err)
	assert.Contains(t, err.Msg, `path "svc\xff" is not valid UTF-8`)
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
