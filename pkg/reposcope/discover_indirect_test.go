package reposcope

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func file(text string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(text)} }

// deployment is a one-document workload manifest: a Deployment of name with
// the images given, one container each.
func deployment(name string, images ...string) string {
	doc := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: " + name + "\nspec:\n  template:\n    spec:\n      containers:\n"
	for _, image := range images {
		doc += "        - image: " + image + "\n"
	}
	return doc
}

// discoverOne plans dir of repo and answers the spans query with one row.
func discoverOne(t *testing.T, repo fstest.MapFS, dir string, terms []string, row map[string]interface{}) *DiscoveryReport {
	t.Helper()
	p, err := PlanDiscovery(repo, dir, terms)
	require.NoError(t, err)
	row[fieldRecords] = float64(10)
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		objectSpans: {{records: []map[string]interface{}{row}}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1, "sent %v", p.Sent)
	return r
}

func workloadRow(namespace, workload string) map[string]interface{} {
	return map[string]interface{}{fieldNamespace: namespace, fieldWorkload: workload}
}

// A repository holds names that are not its own: the manifest of a
// dependency it deploys beside itself, the image of a sidecar, the name its
// hosting service files it under, and whatever sits in the directories kept
// for other projects' code and for test content. Each finds a candidate, and
// none settles one: the report is ambiguous, the candidate says why, and a
// term, which is the user's own word for it, settles it.
func TestDiscover_IndirectEvidenceIsNotAMatch(t *testing.T) {
	checkoutModule := file("module example.invalid/acme/checkout\n")
	tests := []struct {
		name  string
		repo  fstest.MapFS
		dir   string
		row   map[string]interface{}
		why   string
		terms []string // settle it
	}{
		{"a third-party manifest",
			fstest.MapFS{"go.mod": checkoutModule, "deploy/redis.yaml": file(deployment("redis-cache", "redis:7.2"))},
			".", workloadRow("shared-infra", "redis-cache"),
			"named only by a manifest or chart that is not named like the unit", []string{"redis-cache"}},
		{"a third-party manifest, by its image",
			fstest.MapFS{"go.mod": checkoutModule, "deploy/redis.yaml": file(deployment("redis-cache", "redis:7.2"))},
			".", workloadRow("shared-infra", "redis"),
			"named only by an image a manifest lists", []string{"redis"}},
		{"a sidecar image",
			fstest.MapFS{"go.mod": checkoutModule, "deploy/checkout.yaml": file(deployment("checkout", "acme/checkout:1", "envoyproxy/envoy:v1.30"))},
			".", map[string]interface{}{fieldNamespace: "mesh", fieldWorkload: "envoy", fieldServiceName: "envoy"},
			"named only by an image a manifest lists", []string{"envoy"}},
		{"the remote's name",
			fstest.MapFS{"go.mod": checkoutModule, ".git/config": file(fmtRemote("platform"))},
			".", map[string]interface{}{fieldServiceName: "platform"},
			"named only by the origin remote's name", []string{"platform"}},
		{"a vendored chart",
			fstest.MapFS{"go.mod": checkoutModule, "charts/redis/Chart.yaml": file("name: redis\n")},
			".", workloadRow("shared-infra", "redis"),
			"named only by a manifest or chart that is not named like the unit", []string{"redis"}},
		{"a directory under vendor",
			fstest.MapFS{"go.mod": checkoutModule, "vendor/leftpad/pad.go": file("package leftpad\n")},
			"vendor/leftpad", workloadRow("other-team", "leftpad"),
			"named only by what is under vendor/, which holds no name of this repository's own", []string{"leftpad"}},
		{"a module path under vendor",
			fstest.MapFS{"go.mod": checkoutModule, "vendor/example.invalid/acme/ledger/l.go": file("package ledger\n")},
			"vendor/example.invalid/acme/ledger", workloadRow("other-team", "ledger"),
			"named only by what is under vendor/, which holds no name of this repository's own", []string{"ledger"}},
		{"a directory under node_modules",
			fstest.MapFS{"package.json": file(`{"name": "checkout"}`), "node_modules/leftpad/index.js": file("")},
			"node_modules/leftpad", workloadRow("other-team", "leftpad"),
			"named only by what is under node_modules/, which holds no name of this repository's own", []string{"leftpad"}},
		{"a fixture directory",
			fstest.MapFS{"go.mod": checkoutModule, "testdata/fixtures/payments/x.txt": file("")},
			"testdata/fixtures/payments", workloadRow("other-team", "payments"),
			"named only by what is under testdata/, which holds no name of this repository's own", []string{"payments"}},
		{"a fixture's own build file and manifest",
			fstest.MapFS{"go.mod": checkoutModule,
				"testdata/fixtures/payments/go.mod":      file("module example.invalid/acme/payments\n"),
				"testdata/fixtures/payments/deploy.yaml": file(deployment("payments", "acme/payments:1"))},
			"testdata/fixtures/payments", workloadRow("other-team", "payments"),
			"named only by what is under testdata/, which holds no name of this repository's own", []string{"payments"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := discoverOne(t, tt.repo, tt.dir, nil, tt.row)
			c := r.Candidates[0]
			assert.Equal(t, "exact", c.Tier, "found, by its whole name")
			assert.Equal(t, VerdictAmbiguous, r.Verdict)
			for kind, v := range r.Verdicts {
				assert.NotEqual(t, VerdictMatch, v, kind)
			}
			assert.Equal(t, tt.why, c.Indirect)
			assert.False(t, c.MatchedDir && c.Indirect == "", "no directory is proposed on indirect evidence")

			r = discoverOne(t, tt.repo, tt.dir, tt.terms, tt.row)
			assert.Equal(t, VerdictMatch, r.Verdict, "a term is the user's own word for it")
			assert.Empty(t, r.Candidates[0].Indirect)
		})
	}
}

func fmtRemote(name string) string {
	return "[remote \"origin\"]\n\turl = https://git.example.invalid/acme/" + name + ".git\n"
}

// The controls: the same shapes with one name of the unit's own among what
// matched, or tying the manifest to the unit, are matches as before.
func TestDiscover_DirectEvidenceStillMatches(t *testing.T) {
	checkoutModule := file("module example.invalid/acme/checkout\n")
	tests := []struct {
		name string
		repo fstest.MapFS
		dir  string
		row  map[string]interface{}
	}{
		{"the unit's build file",
			fstest.MapFS{"go.mod": checkoutModule},
			".", workloadRow("payments", "checkout")},
		{"a manifest named like the build file",
			fstest.MapFS{"go.mod": checkoutModule, "deploy/checkout.yaml": file(deployment("checkout", "acme/checkout:1", "envoyproxy/envoy:v1.30"))},
			".", workloadRow("payments", "checkout")},
		{"a manifest whose image is named like the build file, under another workload name",
			fstest.MapFS{"go.mod": checkoutModule, "deploy/app.yaml": file(deployment("storefront", "acme/checkout:1"))},
			".", workloadRow("payments", "storefront")},
		{"a manifest named like the remote, in a repository without a build file",
			fstest.MapFS{".git/config": file(fmtRemote("checkout")), "k8s/deployment.yaml": file(deployment("checkout", "acme/checkout-image:1"))},
			".", workloadRow("payments", "checkout")},
		{"a manifest named like the unit's directory",
			fstest.MapFS{"services/ledger/Dockerfile": file("FROM scratch\n"), "deploy/ledger.yaml": file(deployment("ledger", "acme/ledger-image:1"))},
			"services/ledger", workloadRow("payments", "ledger")},
		{"the directory discovery ran for",
			fstest.MapFS{"go.mod": file("module example.invalid/acme/shop\n"), "services/ledger/main.go": file("package main\n")},
			"services/ledger", workloadRow("payments", "ledger")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := discoverOne(t, tt.repo, tt.dir, nil, tt.row)
			assert.Equal(t, VerdictMatch, r.Verdict)
			assert.Empty(t, r.Candidates[0].Indirect)
		})
	}
}

// The entity-name fallback reads the same names, so the same holds there:
// what it finds by an indirect name says so.
func TestDiscover_IndirectEvidenceInTheEntityFallback(t *testing.T) {
	repo := fstest.MapFS{"go.mod": file("module example.invalid/acme/checkout\n"), ".git/config": file(fmtRemote("platform"))}
	p, err := PlanDiscovery(repo, ".", nil)
	require.NoError(t, err)
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		objectServices: {{records: []map[string]interface{}{{fieldEntityID: svcID, fieldEntityName: "platform"}}}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, "named only by the origin remote's name", r.Candidates[0].Indirect)
}

// A name found inside a longer workload name is weighed the same way: an
// image's name inside a workload of another product finds it and proposes
// nothing, while the unit's own name inside its prefixed workload stays a
// candidate to confirm with its line.
func TestDiscover_IndirectEvidenceInsideAWorkloadName(t *testing.T) {
	repo := fstest.MapFS{
		"go.mod":            file("module example.invalid/acme/checkout\n"),
		"deploy/proxy.yaml": file(deployment("checkout", "acme/checkout:1", "nginx:1.25")),
	}
	r := discoverOne(t, repo, ".", nil, workloadRow("ingress", "ingress-nginx-controller"))
	assert.Equal(t, "segment", r.Candidates[0].Tier)
	assert.Equal(t, "named only by an image a manifest lists", r.Candidates[0].Indirect)

	r = discoverOne(t, repo, ".", nil, workloadRow("payments", "acme-prod-checkout"))
	assert.Equal(t, "segment", r.Candidates[0].Tier)
	assert.Empty(t, r.Candidates[0].Indirect)
}
