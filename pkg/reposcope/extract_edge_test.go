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

func TestExtractTemplateNamesAreNotSent(t *testing.T) {
	for _, name := range []string{"${SERVICE_NAME}", "{{ .Values.name }}"} {
		p, err := PlanDiscovery(fstest.MapFS{"package.json": {Data: []byte(`{"name":"` + name + `"}`)}}, ".", nil)
		require.NoError(t, err)
		require.Empty(t, p.Sent)
	}
}
