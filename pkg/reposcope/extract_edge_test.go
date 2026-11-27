package reposcope

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

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

// An editor on Windows may save a file with a UTF-8 byte order mark. It is
// not part of the file's first line.
func TestExtractReadsAFileWithAByteOrderMark(t *testing.T) {
	const bom = "\ufeff"
	for file, tt := range map[string]struct{ data, want string }{
		"go.mod":          {"module example.invalid/ledger-writer\n", "module ledger-writer go.mod:1"},
		"package.json":    {`{"name": "ledger-writer"}`, "package ledger-writer package.json:1"},
		"pyproject.toml":  {"[project]\nname = \"ledger-writer\"\n", "project ledger-writer pyproject.toml:2"},
		"deployment.yaml": {"kind: Deployment\nmetadata:\n  name: ledger-writer\n", "workload ledger-writer deployment.yaml:3"},
		"Chart.yaml":      {"name: ledger-writer\n", "chart ledger-writer Chart.yaml:1"},
		".git/config":     {"[remote \"origin\"]\nurl = https://example.invalid/shop/ledger-writer.git\n", "remote ledger-writer .git/config:2"},
	} {
		t.Run(file, func(t *testing.T) {
			p, err := PlanDiscovery(fstest.MapFS{file: {Data: []byte(bom + tt.data)}}, ".", nil)
			require.NoError(t, err)
			require.Equal(t, []string{tt.want}, tokenLines(p.tokens))
		})
	}
}

// A directory is often named for its place in a layout, not for what runs
// from it. Such a name is not sent, whether it is the directory discovery was
// asked about or a unit's own; a directory named like a service still is.
func TestExtractLayoutDirectoriesNameNothing(t *testing.T) {
	root := fstest.MapFS{
		"go.mod":                       {Data: []byte("module example.invalid/checkout\n")},
		"internal/handlers/handler.go": {Data: []byte("package handlers\n")},
		"deploy/values.txt":            {Data: []byte("x\n")},
		"src/main.go":                  {Data: []byte("package main\n")},
		"Services/ledger/main.go":      {Data: []byte("package main\n")},
	}
	for dir, want := range map[string][]string{
		"internal":          {"checkout"},
		"internal/handlers": {"checkout"},
		"deploy":            {"checkout"},
		"src":               {"checkout"},
		"Services":          {"checkout"},
		"Services/ledger":   {"checkout", "ledger"},
	} {
		p, err := PlanDiscovery(root, dir, nil)
		require.NoError(t, err)
		require.Equal(t, want, p.Sent, dir)
	}

	// A unit's own directory name is held to the same words.
	units := fstest.MapFS{
		"python/pyproject.toml": {Data: []byte("[project]\nname = \"fraud-scoring\"\n")},
		"src/go.mod":            {Data: []byte("module example.invalid/checkout\n")},
	}
	for dir, want := range map[string][]string{
		"python": {"project fraud-scoring python/pyproject.toml:2"},
		"src":    {"module checkout src/go.mod:1"},
	} {
		p, err := PlanDiscovery(units, dir, nil)
		require.NoError(t, err)
		require.Equal(t, want, tokenLines(p.tokens), dir)
	}
}

// A directory is named relative to the repository root. One that is not (an
// absolute path, one that leaves the root) is refused: the walks up from it
// used to never end, since the parent of "/" is "/".
func TestPlanDiscoveryRefusesADirectoryOutsideTheRepository(t *testing.T) {
	for _, dir := range []string{"/project", "/", "../elsewhere", "services/../../elsewhere"} {
		done := make(chan error, 1)
		go func() {
			_, err := PlanDiscovery(fstest.MapFS{"settings.gradle": {Data: []byte("rootProject.name = 'ledger-writer'\n")}}, dir, nil)
			done <- err
		}()
		select {
		case err := <-done:
			var invalid *InvalidError
			require.ErrorAs(t, err, &invalid, dir)
			require.Contains(t, err.Error(), "is not a directory inside the repository", dir)
		case <-time.After(5 * time.Second):
			t.Fatalf("PlanDiscovery(%q) did not return", dir)
		}
	}
	for _, dir := range []string{"", ".", "./", "services/..", "services/ledger/"} {
		_, err := PlanDiscovery(fstest.MapFS{}, dir, nil)
		require.NoError(t, err, dir)
	}
}

// The Gradle readers walk up from a directory too, and resolving an entry
// calls one of them with a directory of its own.
func TestGradleWalksEndAtAnAbsoluteRoot(t *testing.T) {
	type found struct {
		at, of *gradleBuild
		module string
	}
	done := make(chan found, 1)
	go func() {
		builds := gradleBuilds{"build": &gradleBuild{dir: "build", modules: map[string][]string{}}}
		done <- found{
			at:     gradleBuildAt(fstest.MapFS{}, "/project/module"),
			of:     builds.of("/project/module"),
			module: builds["build"].module("/project/module"),
		}
	}()
	select {
	case got := <-done:
		require.Equal(t, found{}, got)
	case <-time.After(5 * time.Second):
		t.Fatal("a walk up from an absolute directory did not end")
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

// .git/config is read the way git reads it, as far as an origin URL needs: a
// comment after a header or a value is none of either, a section name and a
// key are spelled in any case, and a value may be quoted. A header the reader
// does not understand ends the section before it, so the URL of a remote
// declared after origin is never taken for origin's.
func TestRepositoryNameReadsGitConfigSpellings(t *testing.T) {
	const ledger = "[remote \"backup\"] ; mirror\n\turl = https://example.invalid/shop/ledger.git\n"
	for _, tt := range []struct{ name, config, want string }{
		{"comments after the URL and after the next header",
			"[remote \"origin\"]\n\turl = https://example.invalid/shop/storefront.git # primary\n" + ledger, "storefront"},
		{"a comment after the URL alone",
			"[remote \"origin\"]\n\turl = https://example.invalid/shop/storefront.git ; primary\n", "storefront"},
		{"a comment right behind the URL",
			"[remote \"origin\"]\n\turl = https://example.invalid/shop/storefront.git#primary\n", "storefront"},
		{"a comment after origin's header",
			"[remote \"origin\"] # primary\n\turl = https://example.invalid/shop/storefront.git\n" + ledger, "storefront"},
		{"comment lines",
			"# remotes\n[remote \"origin\"]\n\t; url = https://example.invalid/shop/old.git\n\turl = https://example.invalid/shop/storefront.git\n", "storefront"},
		{"a section name in capitals", "[Remote \"origin\"]\n\turl = https://example.invalid/shop/storefront.git\n", "storefront"},
		{"a key in capitals", "[remote \"origin\"]\n\tURL = https://example.invalid/shop/storefront.git\n", "storefront"},
		{"no spaces around the equals sign", "[remote \"origin\"]\nurl=https://example.invalid/shop/storefront.git\n", "storefront"},
		{"a quoted URL", "[remote \"origin\"]\n\turl = \"https://example.invalid/shop/storefront.git\"\n", "storefront"},
		{"a quoted URL with a comment", "[remote \"origin\"]\n\turl = \"https://example.invalid/shop/storefront.git\" # primary\n", "storefront"},
		{"a quoted URL that holds a comment character", "[remote \"origin\"]\n\turl = \"https://example.invalid/shop/store;front.git\"\n", "store;front"},
		{"Windows line ends", "[remote \"origin\"]\r\n\turl = https://example.invalid/shop/storefront.git\r\n", "storefront"},
		{"a remote named Origin is another remote", "[remote \"Origin\"]\n\turl = https://example.invalid/shop/ledger.git\n", ""},
		{"a header that is not understood ends origin's section",
			"[remote \"origin\"]\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[remote \"backup\" extra]\n\turl = https://example.invalid/shop/ledger.git\n", ""},
		{"so does one that is never closed",
			"[remote \"origin\"]\n[remote \"backup\"\n\turl = https://example.invalid/shop/ledger.git\n", ""},
		{"a quote that is never closed is no URL",
			"[remote \"origin\"]\n\turl = \"https://example.invalid/shop/storefront.git\n", ""},
		{"nor is text after the closing quote",
			"[remote \"origin\"]\n\turl = \"https://example.invalid/shop/store\"front.git\n", ""},
		{"a URL key of another section", "[remote \"origin\"]\n[branch \"main\"]\n\turl = https://example.invalid/shop/ledger.git\n", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, RepositoryName(fstest.MapFS{".git/config": {Data: []byte(tt.config)}}))
		})
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
