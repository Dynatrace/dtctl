package reposcope

import (
	"fmt"
	"io/fs"
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
	tests := []struct {
		name   string
		dir    string
		unit   string
		tokens []string
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
			// A .git file points outside the repository: no remote.
			name:   "worktree-gitfile",
			dir:    ".",
			unit:   ".",
			tokens: []string{"module orders go.mod:1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"@"+tt.dir, func(t *testing.T) {
			s, err := extract(fixture(t, tt.name), tt.dir)
			require.NoError(t, err)
			assert.Equal(t, tt.unit, s.unit)
			assert.Equal(t, tt.tokens, tokenLines(s.tokens))
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

func TestVariants(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"checkout", []string{"checkout"}},
		{"paymentService", []string{"payment-service", "payment_service", "payment.service", "paymentservice"}},
		{"PaymentService", []string{"payment-service", "payment_service", "payment.service", "paymentservice"}},
		{"payment_service", []string{"payment-service", "payment_service", "payment.service", "paymentservice"}},
		{"HTTPServer", []string{"http-server", "http_server", "http.server", "httpserver"}},
		{"checkoutV2", []string{"checkout-v2", "checkout_v2", "checkout.v2", "checkoutv2"}},
		{"registry.example.invalid/acme/checkout:1.4.2", []string{"checkout"}},
		{"registry.example.invalid/acme/checkout@sha256:00ff", []string{"checkout"}},
		{"example.invalid/acme/orders", []string{"orders"}},
		{"api", nil},
		{"Service", nil},
		{"k8s", nil},
		{"ab", nil},
		{"  ", nil},
		{"", nil},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			assert.Equal(t, tt.want, variants(tt.raw))
		})
	}
}
