package reposcope

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture loads testdata/repos/<name> as a repository root. Fixtures spell
// .git as _git, because git refuses to commit a path named .git.
func fixture(t *testing.T, name string) fstest.MapFS {
	t.Helper()
	root := filepath.Join("testdata", "repos", name)
	fsys := fstest.MapFS{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if parts[0] == "_git" {
			parts[0] = ".git"
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fsys[strings.Join(parts, "/")] = &fstest.MapFile{Data: data}
		return nil
	})
	require.NoError(t, err)
	return fsys
}

// tokenLines renders tokens as "kind value source", one per token, so a
// fixture's expectations read like the repository they describe.
func tokenLines(tokens []token) []string {
	out := make([]string, len(tokens))
	for i, tk := range tokens {
		out[i] = fmt.Sprintf("%s %s %s", tk.kind, tk.value, tk.source())
	}
	return out
}

func TestExtract_Fixtures(t *testing.T) {
	dockerfile := &fstest.MapFile{Data: []byte("FROM scratch\n")}
	webproxy := []string{
		"dir webproxy webproxy/",
		"workload webproxy deploy/webproxy/deployment.yaml:4",
		"namespace edge deploy/webproxy/deployment.yaml:5",
		"image webproxy deploy/webproxy/deployment.yaml:11",
		"artifact webproxy-app webproxy/build.gradle.kts:6",
	}
	widget := []string{"chart widget-detect server/helm/Chart.yaml:2", "project platform-widget settings.gradle.kts:1"}
	tests := []struct {
		name string
		// extra are files added to the fixture.
		extra    fstest.MapFS
		dir      string
		unit     string
		tokens   []string
		dropped  []string
		sharedBy []string
	}{
		{
			name: "single-go",
			dir:  ".",
			unit: ".",
			tokens: []string{
				"entrypoint checkout Dockerfile:4",
				"workload checkout deploy/k8s/deployment.yaml:4",
				"namespace payments deploy/k8s/deployment.yaml:5",
				"image checkout deploy/k8s/deployment.yaml:12",
				"service-name checkout deploy/k8s/deployment.yaml:15",
				"module checkout go.mod:1",
				"remote checkout-service .git/config:6",
			},
		},
		{
			// No build file at or above the directory but the root's: the
			// root is the unit, and the directory's own name joins it. The
			// ledger manifest has no unit named like it and stays at the
			// root; the checkout one belongs to services/checkout.
			name: "monorepo-three",
			dir:  "services/ledger",
			unit: ".",
			tokens: []string{
				"dir ledger services/ledger/",
				"workload ledger-close deploy/ledger/cronjob.yaml:4",
				"namespace shop deploy/ledger/cronjob.yaml:5",
				"image ledger deploy/ledger/cronjob.yaml:14",
				"service-name ledger-close deploy/ledger/cronjob.yaml:17",
				"module shop go.mod:1",
				"remote shop .git/config:2",
			},
		},
		{
			name: "monorepo-three",
			dir:  "services/checkout/internal",
			unit: "services/checkout",
			tokens: []string{
				"dir checkout services/checkout/",
				"workload checkout deploy/checkout/deployment.yaml:4",
				"namespace shop deploy/checkout/deployment.yaml:5",
				"image checkout deploy/checkout/deployment.yaml:11",
				"entrypoint checkout-api services/checkout/Dockerfile:3",
			},
		},
		{
			name:   "helm-unrendered",
			dir:    ".",
			unit:   ".",
			tokens: []string{"chart billing chart/Chart.yaml:2"},
		},
		{
			name:   "camel-case",
			dir:    "dotnet",
			unit:   "dotnet",
			tokens: []string{"dir dotnet dotnet/", "assembly invoice-renderer dotnet/InvoiceRenderer.csproj"},
		},
		{
			name:   "camel-case",
			dir:    "java",
			unit:   "java",
			tokens: []string{"dir java java/", "artifact payment-service java/pom.xml:8"},
		},
		{
			name:   "camel-case",
			dir:    "python",
			unit:   "python",
			tokens: []string{"dir python python/", "project fraud-scoring python/pyproject.toml:5"},
		},
		{
			name:   "no-signals",
			dir:    ".",
			unit:   ".",
			tokens: []string{},
		},
		{
			// One deployable (server holds the chart): the build is the
			// one unit wherever discovery runs in it, and no module's
			// directory name is sent. The settings file's include( spans
			// several lines.
			name:   "gradle-one-chart",
			dir:    "server",
			unit:   ".",
			tokens: widget,
		},
		{
			// A Dockerfile in the deployable does not split the build.
			name:   "gradle-one-chart",
			extra:  fstest.MapFS{"server/Dockerfile": dockerfile},
			dir:    "logic-widget/src/main/java",
			unit:   ".",
			tokens: widget,
		},
		{
			// Two deployables, each claimed by a manifest: each is a unit
			// covering the modules it depends on.
			name:   "gradle-multi",
			dir:    "webproxy",
			unit:   "webproxy",
			tokens: webproxy,
		},
		{
			// Only webproxy depends on the api module.
			name:   "gradle-multi",
			dir:    "webproxy-api",
			unit:   "webproxy",
			tokens: webproxy,
		},
		{
			// Both depend on the core module: discovery runs for each
			// deployable, and sends nothing for the module itself.
			name:     "gradle-multi",
			dir:      "webproxy-core/src/main/kotlin",
			unit:     ".",
			tokens:   []string{},
			sharedBy: []string{"admin", "webproxy"},
		},
		{
			// The settings directory is the whole build, which no entry
			// of one deployable covers.
			name:     "gradle-multi",
			dir:      ".",
			unit:     ".",
			tokens:   []string{},
			sharedBy: []string{"admin", "webproxy"},
		},
		{
			// buildSrc is never entered: its settings file names the
			// build logic, not the build, so it is the build's directory.
			name:     "gradle-multi",
			dir:      "buildSrc",
			unit:     ".",
			tokens:   []string{},
			sharedBy: []string{"admin", "webproxy"},
		},
		{
			// No module is deployed by name or by what it holds, so the
			// build is the one unit, and app is not sent.
			name: "gradle-app",
			dir:  "app/src/main/java",
			unit: ".",
			tokens: []string{
				"workload orders-mobile deploy/deployment.yaml:4",
				"namespace orders deploy/deployment.yaml:5",
				"image orders-mobile deploy/deployment.yaml:11",
				"project orders-mobile settings.gradle.kts:1",
			},
		},
		{
			// A .git file points outside the repository: no remote.
			name:   "worktree-gitfile",
			dir:    ".",
			unit:   ".",
			tokens: []string{"module orders go.mod:1"},
		},
		{
			// test.Dockerfile, deployment-e2e.yaml, test/, testdata/, e2e/
			// and examples/ are not read; the dev overlay is. The smoke
			// Job is read, but its test names are held back. (The fixture
			// cannot hold a Dockerfile.test: .gitignore drops *.test.)
			name: "test-noise",
			dir:  ".",
			unit: ".",
			tokens: []string{
				"entrypoint platform-service Dockerfile:3",
				"workload platform-service deploy/deployment.yaml:4",
				"namespace platform deploy/deployment.yaml:5",
				"image platform-service deploy/deployment.yaml:11",
				"namespace platform deploy/deployment.yaml:17",
				"workload platform-service deploy/overlays/dev/deployment.yaml:4",
				"namespace dev deploy/overlays/dev/deployment.yaml:5",
				"module platform-service go.mod:1",
			},
			dropped: []string{"test-platform-service-smoke", "test-runner"},
		},
		{
			// examples/ is on the way to the directory discovery was
			// asked about, so it is read.
			name: "test-noise",
			dir:  "examples/checkout",
			unit: "examples/checkout",
			tokens: []string{
				"dir checkout examples/checkout/",
				"workload checkout examples/checkout/deploy.yaml:4",
				"namespace shop examples/checkout/deploy.yaml:5",
				"module checkout examples/checkout/go.mod:1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"@"+tt.dir, func(t *testing.T) {
			fsys := fixture(t, tt.name)
			maps.Copy(fsys, tt.extra)
			s, err := extract(fsys, tt.dir)
			require.NoError(t, err)
			assert.Equal(t, tt.unit, s.unit)
			assert.Equal(t, tt.tokens, tokenLines(s.tokens))
			assert.Equal(t, tt.dropped, s.dropped)
			assert.Equal(t, tt.sharedBy, s.sharedBy)
		})
	}
}

func TestExtract_TokensCarryTheirVariants(t *testing.T) {
	s, err := extract(fixture(t, "camel-case"), "java")
	require.NoError(t, err)
	require.Len(t, s.tokens, 2)
	assert.Equal(t, []string{"payment-service", "payment_service", "payment.service", "paymentservice", "paymentService"}, s.tokens[1].variants,
		"the repository's own spelling comes last")
}

func TestExtract_Limits(t *testing.T) {
	t.Run("too many files", func(t *testing.T) {
		fsys := fstest.MapFS{"go.mod": {Data: []byte("module example.invalid/after-the-cap\n")}}
		for i := 0; i <= maxFiles; i++ {
			fsys[fmt.Sprintf("conf/f%04d.yaml", i)] = &fstest.MapFile{Data: []byte("a: b\n")}
		}
		s, err := extract(fsys, ".")
		require.NoError(t, err)
		assert.Empty(t, s.tokens, "conf/ fills the cap before go.mod is reached")
	})
	t.Run("too deep", func(t *testing.T) {
		fsys := fstest.MapFS{
			"a/b/c/d/e/leaf/go.mod":        {Data: []byte("module example.invalid/shallow-enough\n")},
			"a/b/c/d/e/leaf/deeper/go.mod": {Data: []byte("module example.invalid/too-deep\n")},
		}
		s, err := extract(fsys, "a/b/c/d/e/leaf/deeper")
		require.NoError(t, err)
		assert.Equal(t, "a/b/c/d/e/leaf", s.unit)
		assert.Equal(t, []string{
			"dir leaf a/b/c/d/e/leaf/",
			"module shallow-enough a/b/c/d/e/leaf/go.mod:1",
		}, tokenLines(s.tokens))
	})
	t.Run("too large", func(t *testing.T) {
		big := "module example.invalid/huge\n" + strings.Repeat("// padding\n", maxFileBytes/10)
		s, err := extract(fstest.MapFS{"go.mod": {Data: []byte(big)}}, ".")
		require.NoError(t, err)
		assert.Empty(t, s.tokens)
	})
	t.Run("dependency trees are not entered", func(t *testing.T) {
		fsys := fstest.MapFS{}
		for dir := range skippedDirs {
			fsys[dir+"/sub/package.json"] = &fstest.MapFile{Data: []byte(`{"name": "vendored-thing"}`)}
		}
		s, err := extract(fsys, ".")
		require.NoError(t, err)
		assert.Empty(t, s.tokens)
	})
}

func TestExtract_TestContentIsNotRead(t *testing.T) {
	dirs := []string{"Tests", "src/test/resources"}
	for dir := range excludedDirs {
		dirs = append(dirs, dir, "."+dir)
	}
	for _, dir := range dirs {
		fsys := fstest.MapFS{dir + "/sub/package.json": {Data: []byte(`{"name": "stubbed-thing"}`)}}
		s, err := extract(fsys, ".")
		require.NoError(t, err)
		assert.Empty(t, s.tokens, dir)
	}
}

func TestExtract_ExcludedDirsOnThePathAreRead(t *testing.T) {
	fsys := fstest.MapFS{
		"examples/checkout/package.json":                {Data: []byte(`{"name": "checkout"}`)},
		"examples/checkout/test/package.json":           {Data: []byte(`{"name": "checkout-harness"}`)},
		"examples/checkout/deploy/deployment.yaml":      {Data: []byte("kind: Deployment\nmetadata:\n  name: checkout\n")},
		"examples/checkout/deploy/deployment-test.yaml": {Data: []byte("kind: Deployment\nmetadata:\n  name: checkout-load\n")},
		"examples/ledger/package.json":                  {Data: []byte(`{"name": "ledger"}`)},
	}
	s, err := extract(fsys, "examples/checkout/deploy")
	require.NoError(t, err)
	assert.Equal(t, "examples/checkout", s.unit)
	assert.Equal(t, []string{
		"dir checkout examples/checkout/",
		"workload checkout examples/checkout/deploy/deployment.yaml:3",
		"package checkout examples/checkout/package.json:1",
	}, tokenLines(s.tokens), "only the directories on the way are exempt, not what lies below them or test files in them")
}

func TestExtract_TestFiles(t *testing.T) {
	tests := []struct {
		name string
		test bool
	}{
		{"Dockerfile.test", true},
		{"test.Dockerfile", true},
		{"deployment-test.yaml", true},
		{"deployment_e2e.yaml", true},
		{"docker-compose.test.yml", true},
		{"checkout.spec.yaml", true},
		{"mock-deployment.yaml", true},
		{"Billing.Tests.csproj", true},
		{"Dockerfile", false},
		{"deployment.yaml", false},
		{"spec.yaml", false},
		{"latest.yaml", false},
		{"contest-deployment.yaml", false},
		{"Dockerfile.dev", false},
		{"go.mod", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.test, testFile(tt.name))
		})
	}
}

func TestExtract_Dockerfile(t *testing.T) {
	tests := []struct {
		name, dockerfile, want string
	}{
		{"exec-form binary", `ENTRYPOINT ["/usr/local/bin/order-api"]`, "order-api"},
		{"a jar wins over the interpreter", `ENTRYPOINT ["java", "-Xmx512m", "-jar", "/app/billing-core.jar"]`, "billing-core"},
		{"CMD extends ENTRYPOINT", "ENTRYPOINT [\"dotnet\"]\nCMD [\"Invoices.Web.dll\"]", "invoices-web"},
		{"shell form", "CMD node dist/report-builder.js", "report-builder"},
		{"interpreters and flags are skipped", `CMD ["python3", "-u", "scheduler.py"]`, "scheduler"},
		{"the last ENTRYPOINT counts", "ENTRYPOINT [\"/bin/first-thing\"]\nENTRYPOINT [\"/bin/second-thing\"]", "second-thing"},
		{"nothing but an interpreter", `CMD ["bash"]`, ""},
		{"a generic name is no token", `ENTRYPOINT ["/app/server"]`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := dockerEntrypoint("FROM scratch\n"+tt.dockerfile+"\n", "Dockerfile")
			assert.Equal(t, tt.want, got.value)
		})
	}
}

func TestExtract_Gradle(t *testing.T) {
	dockerfile := &fstest.MapFile{Data: []byte("FROM scratch\n")}
	tests := []struct {
		name     string
		fsys     fstest.MapFS
		dir      string
		unit     string
		tokens   []string
		dropped  []string
		sharedBy []string
	}{
		{
			// Inside a settings build a module's build file names the
			// build's one unit.
			name: "Groovy DSL",
			fsys: fstest.MapFS{
				"settings.gradle":        {Data: []byte("rootProject.name = 'billing'\n")},
				"invoicing/build.gradle": {Data: []byte("plugins { id 'java' }\n\narchivesBaseName = 'invoicing-service'\n")},
			},
			dir:    "invoicing",
			unit:   ".",
			tokens: []string{"artifact invoicing-service invoicing/build.gradle:3", "project billing settings.gradle:1"},
		},
		{
			// Groovy's include and project(), a nested project path, and a
			// module the settings file includes without a build file.
			name: "Groovy references",
			fsys: fstest.MapFS{
				"settings.gradle":               {Data: []byte("include ':services:billing', ':services:orders'\ninclude ':libs:ledger-core', ':libs:audit'\n")},
				"services/billing/build.gradle": {Data: []byte("dependencies {\n    implementation project(':libs:ledger-core')\n    // implementation project(':libs:audit')\n}\n")},
				"services/billing/Dockerfile":   {Data: []byte("FROM scratch\n")},
				"services/orders/Dockerfile":    {Data: []byte("FROM scratch\n")},
			},
			dir:    "libs/ledger-core/src",
			unit:   "services/billing",
			tokens: []string{"dir billing services/billing/"},
		},
		{
			// Groovy's named path argument is a reference like the
			// positional one.
			name: "Groovy project(path:)",
			fsys: fstest.MapFS{
				"settings.gradle":       {Data: []byte("include ':checkout', ':ledger', ':core'\n")},
				"checkout/build.gradle": {Data: []byte("dependencies {\n    implementation project(path: ':core')\n}\n")},
				"checkout/Dockerfile":   dockerfile,
				"ledger/build.gradle":   {Data: []byte("dependencies {\n    implementation project(':core')\n}\n")},
				"ledger/Dockerfile":     dockerfile,
				"core/build.gradle":     {Data: []byte("plugins { id 'java-library' }\n")},
			},
			dir:      "core",
			unit:     ".",
			tokens:   []string{},
			sharedBy: []string{"checkout", "ledger"},
		},
		{
			name: "Kotlin project(path =)",
			fsys: fstest.MapFS{
				"settings.gradle.kts":       {Data: []byte("include(\":checkout\", \":ledger\", \":core\")\n")},
				"checkout/build.gradle.kts": {Data: []byte("dependencies {\n    implementation(project(path = \":core\"))\n}\n")},
				"checkout/Dockerfile":       dockerfile,
				"ledger/build.gradle.kts":   {Data: []byte("dependencies {\n    implementation(project(\":core\"))\n}\n")},
				"ledger/Dockerfile":         dockerfile,
			},
			dir:      "core",
			unit:     ".",
			tokens:   []string{},
			sharedBy: []string{"checkout", "ledger"},
		},
		{
			// What only ledger's tests use is checkout's alone.
			name: "a test configuration's reference",
			fsys: fstest.MapFS{
				"settings.gradle.kts":       {Data: []byte("include(\":checkout\", \":ledger\", \":core\")\n")},
				"checkout/build.gradle.kts": {Data: []byte("dependencies {\n    implementation(project(\":core\"))\n}\n")},
				"checkout/Dockerfile":       dockerfile,
				"ledger/build.gradle.kts":   {Data: []byte("dependencies {\n    testImplementation(project(\":core\"))\n}\n")},
				"ledger/Dockerfile":         dockerfile,
			},
			dir:    "core",
			unit:   "checkout",
			tokens: []string{"dir checkout checkout/"},
		},
		{
			// The image a test module builds runs the tests: smoke-tests
			// is no deployable, so checkout is the build's one.
			name: "a test module with a Dockerfile",
			fsys: fstest.MapFS{
				"settings.gradle.kts":          {Data: []byte("include(\":checkout\", \":core\", \":smoke-tests\")\n")},
				"checkout/build.gradle.kts":    {Data: []byte("dependencies {\n    implementation(project(\":core\"))\n}\n")},
				"checkout/Dockerfile":          dockerfile,
				"smoke-tests/build.gradle.kts": {Data: []byte("dependencies {\n    implementation(project(\":core\"))\n}\n")},
				"smoke-tests/Dockerfile":       dockerfile,
			},
			dir:    "core",
			unit:   ".",
			tokens: []string{},
		},
		{
			// Without a settings file every build file is a build.
			name: "no settings file",
			fsys: fstest.MapFS{
				"services/billing/build.gradle": {Data: []byte("plugins { id 'java' }\n")},
				"services/orders/build.gradle":  {Data: []byte("plugins { id 'java' }\n")},
			},
			dir:    "services/orders/src",
			unit:   "services/orders",
			tokens: []string{"dir orders services/orders/"},
		},
		{
			name: "Kotlin DSL assignment",
			fsys: fstest.MapFS{
				"invoicing/build.gradle.kts": {Data: []byte("base {\n    archivesName = \"invoicing-service\"\n}\n")},
			},
			dir:    "invoicing",
			unit:   "invoicing",
			tokens: []string{"dir invoicing invoicing/", "artifact invoicing-service invoicing/build.gradle.kts:2"},
		},
		{
			// The build root is the nearest settings file, not the
			// repository root.
			name: "build root below the repository root",
			fsys: fstest.MapFS{
				"go.mod":                  {Data: []byte("module example.invalid/tooling\n")},
				"mobile/settings.gradle":  {Data: []byte("rootProject.name = 'orders-mobile'\n")},
				"mobile/app/build.gradle": {Data: []byte("plugins { id 'com.android.application' }\n")},
			},
			dir:    "mobile/app/src/main",
			unit:   "mobile",
			tokens: []string{"dir mobile mobile/", "project orders-mobile mobile/settings.gradle:1"},
		},
		{
			name: "a module's archives name is its build's",
			fsys: fstest.MapFS{
				"settings.gradle.kts":  {Data: []byte("rootProject.name = \"orders\"\n")},
				"app/build.gradle.kts": {Data: []byte("base {\n    archivesName.set(\"orders-api\")\n}\n")},
			},
			dir:    "app",
			unit:   ".",
			tokens: []string{"artifact orders-api app/build.gradle.kts:2", "project orders settings.gradle.kts:1"},
		},
		{
			name: "a one-line block",
			fsys: fstest.MapFS{
				"invoicing/build.gradle.kts": {Data: []byte("base { archivesName.set(\"invoicing-service\") }\n")},
			},
			dir:    "invoicing",
			unit:   "invoicing",
			tokens: []string{"dir invoicing invoicing/", "artifact invoicing-service invoicing/build.gradle.kts:1"},
		},
		{
			// A test module is part of its build, and its directory name is
			// not sent.
			name: "a test module",
			fsys: fstest.MapFS{
				"settings.gradle.kts":           {Data: []byte("rootProject.name = \"checkout\"\n")},
				"test-harness/build.gradle.kts": {Data: []byte("plugins { java }\n")},
			},
			dir:    "test-harness",
			unit:   ".",
			tokens: []string{"project checkout settings.gradle.kts:1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := extract(tt.fsys, tt.dir)
			require.NoError(t, err)
			assert.Equal(t, tt.unit, s.unit)
			assert.Equal(t, tt.tokens, tokenLines(s.tokens))
			assert.Equal(t, tt.dropped, s.dropped)
			assert.Equal(t, tt.sharedBy, s.sharedBy)
		})
	}
}

// noWalkFS fails the test when a directory is listed.
type noWalkFS struct {
	fstest.MapFS
	t *testing.T
}

func (f noWalkFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f.t.Errorf("listed %s", name)
	return f.MapFS.ReadDir(name)
}

func TestGradleBuildAt(t *testing.T) {
	t.Run("no settings file, no walk", func(t *testing.T) {
		fsys := noWalkFS{fstest.MapFS{"web/package.json": {Data: []byte(`{"name": "storefront"}`)}}, t}
		assert.Nil(t, gradleBuildAt(fsys, "web"))
	})
	t.Run("other sources do not count toward maxFiles", func(t *testing.T) {
		fsys := fstest.MapFS{
			"settings.gradle":        {Data: []byte("include ':invoicing'\n")},
			"invoicing/build.gradle": {Data: []byte("plugins { id 'java' }\n")},
		}
		for i := range maxFiles {
			fsys[fmt.Sprintf("conf/f%04d.yaml", i)] = &fstest.MapFile{Data: []byte("a: b\n")}
		}
		b := gradleBuildAt(fsys, "invoicing")
		require.NotNil(t, b)
		assert.Contains(t, b.modules, "invoicing")
	})
}

// A namespace is never sent, so the test-name rule has nothing to hold back:
// a test-named one stays in the evidence and is not reported as not sent.
func TestExtract_TestNamedNamespaceIsKept(t *testing.T) {
	s, err := extract(fstest.MapFS{"k8s.yaml": {Data: []byte(
		"kind: Deployment\nmetadata:\n  name: payments\n  namespace: test-payments\n")}}, ".")
	require.NoError(t, err)
	assert.Equal(t, []string{"workload payments k8s.yaml:3", "namespace test-payments k8s.yaml:4"}, tokenLines(s.tokens))
	assert.Empty(t, s.dropped)
}

func TestExtract_ManifestKinds(t *testing.T) {
	manifest := func(kind string) []byte {
		return []byte("kind: " + kind + "\nmetadata:\n  name: invoice-worker\n")
	}
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Rollout"} {
		s, err := extract(fstest.MapFS{"k8s.yaml": {Data: manifest(kind)}}, ".")
		require.NoError(t, err)
		assert.Equal(t, []string{"workload invoice-worker k8s.yaml:3"}, tokenLines(s.tokens), kind)
	}
	for _, kind := range []string{"Service", "ConfigMap", "Ingress"} {
		s, err := extract(fstest.MapFS{"k8s.yaml": {Data: manifest(kind)}}, ".")
		require.NoError(t, err)
		assert.Empty(t, s.tokens, kind)
	}
}
