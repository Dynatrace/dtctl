package reposcope

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestExtractBuildFilesWithoutNames(t *testing.T) {
	for _, tc := range []struct{ file, data string }{
		{"go.mod", "go 1.26\n"},
		{"package.json", "{"},
		{"package.json", `{ "private": true }`},
		{"pom.xml", "<project><parent><artifactId>parent-only</artifactId></parent></project>"},
		{"pyproject.toml", "[tool.unrelated]\nname = 'not-a-project'\n"},
		{"settings.gradle.kts", "include(\":app\")\n"},
		{"build.gradle", "// archivesBaseName = 'commented-out'\n"},
		{"Chart.yaml", "name: [invalid"},
		{"Chart.yaml", "apiVersion: v2\n"},
	} {
		t.Run(tc.file+tc.data, func(t *testing.T) {
			p, err := PlanDiscovery(fstest.MapFS{tc.file: {Data: []byte(tc.data)}}, ".", nil)
			require.NoError(t, err)
			require.Empty(t, p.Sent)
			require.Empty(t, p.Queries)
		})
	}
}

// A pyproject.toml names the project in [project] or [tool.poetry] and
// nowhere else: an author's name is a person's, not the project's.
func TestExtractPyprojectReadsOnlyTheProjectName(t *testing.T) {
	for name, tt := range map[string]struct {
		data string
		want []string
	}{
		"the project table": {
			"[build-system]\nrequires = [\"hatchling\"]\n\n[project]\nname = \"fraud-scoring\"\n",
			[]string{"project fraud-scoring pyproject.toml:5"}},
		"poetry's table": {
			"[tool.poetry]\nname = 'fraud-scoring'\n",
			[]string{"project fraud-scoring pyproject.toml:2"}},
		"a dotted key": {
			"project.name = \"fraud-scoring\"\n",
			[]string{"project fraud-scoring pyproject.toml"}},
		"an array of author tables after a dynamic name": {
			"[project]\ndynamic = [\"name\"]\n\n[[project.authors]]\nname = \"Alice Smith\"\n", []string{}},
		"an inline author table over several lines before the name": {
			"[project]\nauthors = [\n  { name = \"Jane Doe\" },\n]\nname = \"fraud-scoring\"\n",
			[]string{"project fraud-scoring pyproject.toml:5"}},
		"a nested table of the project": {
			"[project]\nname = \"fraud-scoring\"\n\n[project.urls]\nname = \"elsewhere\"\n",
			[]string{"project fraud-scoring pyproject.toml:2"}},
		"a name that is not a string": {"[project]\nname = 5\n", []string{}},
		"not TOML":                    {"[project\nname = \"fraud-scoring\"\n", []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := PlanDiscovery(fstest.MapFS{"pyproject.toml": {Data: []byte(tt.data)}}, ".", nil)
			require.NoError(t, err)
			require.Equal(t, tt.want, tokenLines(p.tokens))
		})
	}
}

// A name a file only refers to is read as what it refers to, or not at all:
// a YAML alias is its anchor's value, not the anchor's label; an MSBuild
// property is a placeholder like a template's; a commented-out element is
// not the project's.
func TestExtractNamesAreReadNotTheirPlaceholders(t *testing.T) {
	for name, tt := range map[string]struct {
		file, data string
		want       []string
	}{
		"a YAML alias": {"deployment.yaml",
			"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  labels:\n    app: &appname ledger-writer\n  name: *appname\n",
			[]string{"workload ledger-writer deployment.yaml:5"}},
		"an aliased mapping": {"deployment.yaml",
			"apiVersion: apps/v1\nkind: Deployment\nx-meta: &meta\n  name: ledger-writer\nmetadata: *meta\n",
			[]string{"workload ledger-writer deployment.yaml:4"}},
		"an MSBuild property": {"Fallback.csproj",
			"<Project>\n<AssemblyName>$(MSBuildProjectName)</AssemblyName>\n</Project>", []string{}},
		"a commented-out artifactId": {"pom.xml",
			"<project>\n<!-- <artifactId>old-name</artifactId> -->\n<!--\n<artifactId>older-name</artifactId>\n-->\n<artifactId>ledger-writer</artifactId>\n</project>",
			[]string{"artifact ledger-writer pom.xml:6"}},
		"a commented-out AssemblyName": {"Fallback.csproj",
			"<Project>\n<!-- <AssemblyName>OldName</AssemblyName> -->\n<AssemblyName>LedgerWriter</AssemblyName>\n</Project>",
			[]string{"assembly ledger-writer Fallback.csproj:3"}},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := PlanDiscovery(fstest.MapFS{tt.file: {Data: []byte(tt.data)}}, ".", nil)
			require.NoError(t, err)
			require.Equal(t, tt.want, tokenLines(p.tokens))
		})
	}
}

func TestExtractExplicitAssemblyName(t *testing.T) {
	p, err := PlanDiscovery(fstest.MapFS{"Fallback.csproj": {Data: []byte("<Project>\n<AssemblyName>InvoiceWorker</AssemblyName>\n</Project>")}}, ".", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"assembly invoice-worker Fallback.csproj:2"}, tokenLines(p.tokens))
	require.Contains(t, p.Sent, "InvoiceWorker")
	require.NotContains(t, p.Sent, "fallback")
}

func TestExtractMixedManifestDocuments(t *testing.T) {
	data := `kind: ConfigMap
metadata:
  name: unrelated
---
kind: Deployment
metadata:
  name: checkout
spec:
  templates:
    - spec:
        containers:
          - image: checkout:latest
            env:
              - name: OTEL_SERVICE_NAME
                valueFrom:
                  secretKeyRef: {name: hidden, key: service}
              - value: missing-name
              - name: UNRELATED
                value: ignored
              - name: OTEL_RESOURCE_ATTRIBUTES
                value: deployment.environment=prod
              - name: OTEL_RESOURCE_ATTRIBUTES
                value: deployment.environment=prod, service.name=checkout-api
---
kind: Deployment
---
kind: Deployment
metadata: [broken
`
	p, err := PlanDiscovery(fstest.MapFS{"deploy.yaml": {Data: []byte(data)}}, ".", nil)
	require.NoError(t, err)
	require.Equal(t, []string{
		"workload checkout deploy.yaml:7", "image checkout deploy.yaml:12", "service-name checkout-api deploy.yaml:23",
	}, tokenLines(p.tokens))
	require.NotContains(t, p.Sent, "unrelated")
	require.NotContains(t, p.Sent, "hidden")
}

// Keep walking readable subtrees when an unrelated directory is inaccessible,
// but propagate an unreadable root: discovery has inspected nothing then.
type deniedDirectoryFS struct {
	fstest.MapFS
	denied string
}

func (f deniedDirectoryFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == f.denied {
		return nil, fs.ErrPermission
	}
	return f.MapFS.ReadDir(name)
}

func TestPlanDiscoveryWalkErrors(t *testing.T) {
	base := fstest.MapFS{
		"locked/go.mod": {Data: []byte("module example.invalid/hidden")},
		"go.mod":        {Data: []byte("module example.invalid/checkout")},
	}
	p, err := PlanDiscovery(deniedDirectoryFS{base, "locked"}, ".", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"checkout"}, p.Sent)
	p, err = PlanDiscovery(deniedDirectoryFS{base, "."}, ".", nil)
	require.ErrorIs(t, err, fs.ErrPermission)
	require.ErrorContains(t, err, "read repository")
	require.Nil(t, p)
}

func TestExtractRemoteWithoutUsableOrigin(t *testing.T) {
	for _, config := range []string{
		"[remote \"upstream\"]\nurl = https://example.invalid/checkout.git\n",
		"[remote \"origin\"]\nurl = https://example.invalid/api.git\n",
	} {
		p, err := PlanDiscovery(fstest.MapFS{".git/config": {Data: []byte(config)}}, ".", nil)
		require.NoError(t, err)
		require.Empty(t, p.Sent)
	}
}

// RepositoryName names the repository even when discovery would hold the
// name back.
func TestRepositoryName(t *testing.T) {
	for config, want := range map[string]string{
		"[remote \"origin\"]\nurl = git@example.invalid:shop/checkout.git\n": "checkout",
		"[remote \"origin\"]\nurl = https://example.invalid/api.git\n":       "api",
	} {
		require.Equal(t, want, RepositoryName(fstest.MapFS{".git/config": {Data: []byte(config)}}), config)
	}
}

// Only the last segment of the remote's path is a name. Credentials, a query
// string, a fragment and the directories of a local path are not, and a remote
// with no path names nothing.
func TestRemoteSendsOnlyTheRepositoryName(t *testing.T) {
	for _, tt := range []struct {
		remote, name string
		sent         []string
	}{
		{"https://example.invalid/shop/checkout.git", "checkout", []string{"checkout"}},
		{"https://example.invalid/shop/checkout", "checkout", []string{"checkout"}},
		{"https://example.invalid/shop/checkout/", "checkout", []string{"checkout"}},
		{"https://example.invalid/shop/checkout?token=SECRET999", "checkout", []string{"checkout"}},
		{"https://example.invalid/shop/checkout.git#frag", "checkout", []string{"checkout"}},
		{"https://someone:SECRET999@example.invalid/shop/checkout.git", "checkout", []string{"checkout"}},
		{"https://someone:SECRET999@example.invalid", "", []string{}},
		{"https://example.invalid/", "", []string{}},
		{"https://example.invalid:port/shop/checkout.git", "", []string{}},
		{"ssh://git@example.invalid:2222/shop/checkout.git", "checkout", []string{"checkout"}},
		{"file:///srv/git/checkout.git", "checkout", []string{"checkout"}},
		{"git@example.invalid:shop/checkout.git", "checkout", []string{"checkout"}},
		{"git@example.invalid:checkout.git", "checkout", []string{"checkout"}},
		{"git@example.invalid:", "", []string{}},
		{"someone:SECRET999@example.invalid/shop/checkout.git", "checkout", []string{"checkout"}},
		{"/srv/git/checkout.git", "checkout", []string{"checkout"}},
		{"../checkout", "checkout", []string{"checkout"}},
		{`C:\Users\someone\checkout`, "", []string{}},
		{`\\fileserver\share\checkout.git`, "", []string{}},
		{"https://example.invalid/shop/Billing%20Engine.git", "Billing Engine", []string{"billing-engine", "billing.engine", "billing_engine", "billingengine"}},
	} {
		fsys := fstest.MapFS{".git/config": {Data: []byte("[remote \"origin\"]\n\turl = " + tt.remote + "\n")}}
		require.Equal(t, tt.name, RepositoryName(fsys), tt.remote)
		p, err := PlanDiscovery(fsys, ".", nil)
		require.NoError(t, err)
		require.Equal(t, tt.sent, p.Sent, tt.remote)
	}
}

func TestExtractTemplateNamesAreNotSent(t *testing.T) {
	for _, name := range []string{"${SERVICE_NAME}", "{{ .Values.name }}"} {
		p, err := PlanDiscovery(fstest.MapFS{"package.json": {Data: []byte(`{"name":"` + name + `"}`)}}, ".", nil)
		require.NoError(t, err)
		require.Empty(t, p.Sent)
	}
}
