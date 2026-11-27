package reposcope

import (
	"context"
	"encoding/json"
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

// sidecarManifest is a Deployment of the unit's own name whose second
// container, a proxy, sets a service name of its own.
const sidecarManifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout
spec:
  template:
    spec:
      containers:
        - name: app
          image: acme/checkout:1
          env:
            - name: OTEL_SERVICE_NAME
              value: checkout-api
        - name: proxy
          image: acme/mesh-proxy:2
          env:
            - name: OTEL_SERVICE_NAME
              value: mesh-proxy
`

// Names that are not the unit's own do not add up to one that is. Each of
// these found a candidate the repository does not own and called it a match.
func TestDiscover_IndirectNamesDoNotAddUp(t *testing.T) {
	checkoutModule := file("module example.invalid/acme/checkout\n")
	t.Run("a whole name that is indirect beside a part that is the unit's", func(t *testing.T) {
		// The cache's service name is spelled by a manifest the unit does
		// not claim; the unit's own name is only a segment of the workload.
		repo := fstest.MapFS{"go.mod": checkoutModule, "deploy/redis.yaml": file(deployment("redis-cache", "redis:7.2"))}
		r := discoverOne(t, repo, ".", nil, map[string]interface{}{fieldNamespace: "infra", fieldWorkload: "eu-checkout-v2", fieldServiceName: "redis-cache"})
		c := r.Candidates[0]
		assert.Equal(t, "segment", c.Tier, "the tier is that of the name that is the unit's own")
		assert.Empty(t, c.Indirect)
		assert.Equal(t, VerdictAmbiguous, r.Verdict)
		assert.Contains(t, c.Evidence, `k8s.workload.name "eu-checkout-v2" contains module "checkout" (go.mod:1) (segment match)`)
		assert.False(t, c.ServiceNameOwn)
	})
	t.Run("a whole workload name that is indirect and holds the unit's as a part", func(t *testing.T) {
		repo := fstest.MapFS{"go.mod": checkoutModule, "deploy/cache.yaml": file(deployment("checkout-cache", "redis:7.2"))}
		r := discoverOne(t, repo, ".", nil, workloadRow("infra", "checkout-cache"))
		c := r.Candidates[0]
		assert.Equal(t, "segment", c.Tier)
		assert.Empty(t, c.Indirect)
		assert.Equal(t, VerdictAmbiguous, r.Verdict)
		assert.Equal(t, []string{
			`k8s.workload.name "checkout-cache" from deploy/cache.yaml:4`,
			`k8s.workload.name "checkout-cache" contains module "checkout" (go.mod:1) (segment match)`,
			"10 spans in 24h",
		}, c.Evidence)
	})
	t.Run("a sidecar's service name in the unit's own manifest", func(t *testing.T) {
		repo := fstest.MapFS{"go.mod": checkoutModule, "deploy/checkout.yaml": file(sidecarManifest)}
		r := discoverOne(t, repo, ".", nil, map[string]interface{}{fieldNamespace: "infra", fieldWorkload: "unrelated", fieldServiceName: "mesh-proxy"})
		assert.Equal(t, VerdictAmbiguous, r.Verdict)
		assert.Equal(t, "named only by an image a manifest lists and a container of a manifest whose image is not named like the unit", r.Candidates[0].Indirect)

		// The container that runs the unit's image names the service.
		r = discoverOne(t, repo, ".", nil, map[string]interface{}{fieldServiceName: "checkout-api"})
		assert.Equal(t, VerdictMatch, r.Verdict)
		assert.Empty(t, r.Candidates[0].Indirect)
	})
	t.Run("the one container of the unit's manifest names the service whatever its image", func(t *testing.T) {
		manifest := deployment("checkout", "registry.example.invalid/base/runtime:3") + "          env:\n            - name: OTEL_SERVICE_NAME\n              value: storefront\n"
		r := discoverOne(t, fstest.MapFS{"go.mod": checkoutModule, "deploy/checkout.yaml": file(manifest)}, ".", nil,
			map[string]interface{}{fieldServiceName: "storefront"})
		assert.Equal(t, VerdictMatch, r.Verdict)
	})
	t.Run("the remote's name claims a manifest", func(t *testing.T) {
		repo := fstest.MapFS{"go.mod": checkoutModule, ".git/config": file(fmtRemote("platform")),
			"deploy/cache.yaml": file(deployment("redis-cache", "registry.example.invalid/platform:7"))}
		r := discoverOne(t, repo, ".", nil, workloadRow("infra", "redis-cache"))
		assert.Equal(t, VerdictAmbiguous, r.Verdict)
		assert.Equal(t, "named only by a manifest or chart that is not named like the unit", r.Candidates[0].Indirect)
	})
	t.Run("the remote's name and a manifest named like it, with no build file", func(t *testing.T) {
		repo := fstest.MapFS{".git/config": file(fmtRemote("checkout")), "k8s/deployment.yaml": file(deployment("checkout", "acme/checkout-image:1"))}
		r := discoverOne(t, repo, ".", nil, workloadRow("payments", "checkout"))
		assert.Equal(t, VerdictAmbiguous, r.Verdict)
		assert.Equal(t, "named only by a manifest or chart that is not named like the unit and the origin remote's name", r.Candidates[0].Indirect)
	})
	t.Run("a sidecar image named like another unit moves the manifest there", func(t *testing.T) {
		manifest := deployment("checkout-api", "acme/checkout-api:1", "acme/auth-proxy:1")
		repo := fstest.MapFS{
			"services/checkout/go.mod":              file("module example.invalid/acme/checkout\n"),
			"services/auth-proxy/go.mod":            file("module example.invalid/acme/auth-proxy\n"),
			"services/checkout/k8s/deployment.yaml": file(manifest),
		}
		p, err := PlanDiscovery(repo, "services/auth-proxy", nil)
		require.NoError(t, err)
		assert.NotContains(t, p.Sent, "checkout-api", "the manifest is the unit's it lies in")
		p, err = PlanDiscovery(repo, "services/checkout", nil)
		require.NoError(t, err)
		assert.Contains(t, p.Sent, "checkout-api")

		// Kept beside the units rather than in one, the manifest goes to
		// the unit its workload is named like, and never by an image.
		delete(repo, "services/checkout/k8s/deployment.yaml")
		repo["deploy/checkout.yaml"] = file(deployment("checkout", "acme/checkout:1", "acme/auth-proxy:1"))
		p, err = PlanDiscovery(repo, "services/auth-proxy", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"auth-proxy", "auth.proxy", "auth_proxy", "authproxy"}, p.Sent)
		r := discoverOne(t, repo, "services/checkout", nil, workloadRow("payments", "checkout"))
		assert.Equal(t, VerdictMatch, r.Verdict)
	})
	for name, metadata := range map[string]string{
		"a workload manifest without a name or an image": "  generateName: patch-\n",
		"a placeholder name that reads like the unit's":   "  name: \"${checkout}\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			patch := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n" + metadata + "spec:\n  template:\n    spec:\n      containers:\n" +
				"        - name: app\n          env:\n            - name: OTEL_SERVICE_NAME\n              value: shared-redis\n"
			repo := fstest.MapFS{"go.mod": checkoutModule, "deploy/patch.yaml": file(patch)}
			r := discoverOne(t, repo, ".", nil, map[string]interface{}{fieldNamespace: "infra", fieldServiceName: "shared-redis"})
			assert.Equal(t, VerdictAmbiguous, r.Verdict)
			assert.Equal(t, "named only by a manifest or chart that is not named like the unit", r.Candidates[0].Indirect)
		})
	}
	t.Run("a placeholder image claims nothing", func(t *testing.T) {
		repo := fstest.MapFS{"go.mod": checkoutModule, "deploy/cache.yaml": file(deployment("shared-redis", "registry.example.invalid/${checkout}:1"))}
		r := discoverOne(t, repo, ".", nil, workloadRow("infra", "shared-redis"))
		assert.Equal(t, VerdictAmbiguous, r.Verdict)
	})
	t.Run("a directory under vendor claims a manifest", func(t *testing.T) {
		repo := fstest.MapFS{"vendor/leftpad/pad.go": file("package leftpad\n"), "deploy/leftpad.yaml": file(deployment("leftpad", "acme/leftpad:1"))}
		p, err := PlanDiscovery(repo, "vendor/leftpad", nil)
		require.NoError(t, err)
		for _, tok := range p.tokens {
			assert.NotEmpty(t, tok.indirect, "%s %s", tok.kind, tok.value)
		}
		r := discoverOne(t, repo, "vendor/leftpad", nil, workloadRow("other-team", "leftpad"))
		assert.Equal(t, VerdictAmbiguous, r.Verdict)
		assert.NotEmpty(t, r.Candidates[0].Indirect)
	})
}

// A plan knows where each value was read, and that is not part of what it
// prints. One that went through JSON has only the values left, and every one
// of them would count as the user's own word: the cache a manifest names
// would be a match. Discover refuses it.
func TestDiscover_RefusesAPlanWithoutItsSources(t *testing.T) {
	repo := fstest.MapFS{"go.mod": file("module example.invalid/acme/checkout\n"), "deploy/redis.yaml": file(deployment("redis-cache", "redis:7.2"))}
	p, err := PlanDiscovery(repo, ".", nil)
	require.NoError(t, err)
	data, err := json.Marshal(p)
	require.NoError(t, err)
	var decoded Plan
	require.NoError(t, json.Unmarshal(data, &decoded))

	runner := &fakeRunner{answers: map[string][]answer{
		objectSpans: {{records: []map[string]interface{}{{fieldNamespace: "shared-infra", fieldWorkload: "redis-cache", fieldRecords: float64(10)}}}},
	}}
	r, err := Discover(context.Background(), runner, &decoded, prodHost)
	require.ErrorContains(t, err, "the plan does not say where its values were read")
	assert.Nil(t, r)
	assert.Empty(t, runner.sent, "nothing is sent for it")
}

// Only a name the user passed is a term. A value the plan carries that no
// file and no term explains finds its row and explains nothing.
func TestDiscover_AnUnexplainedValueIsNoTerm(t *testing.T) {
	p, err := PlanDiscovery(fstest.MapFS{"go.mod": file("module example.invalid/acme/checkout\n")}, ".", nil)
	require.NoError(t, err)
	p.Sent = append(p.Sent, "redis-cache")
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		objectSpans: {{records: []map[string]interface{}{{fieldNamespace: "shared-infra", fieldWorkload: "redis-cache", fieldRecords: float64(10)}}}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, "substring", r.Candidates[0].Tier)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
}

// Within a tier a candidate named by the unit itself stands before one only
// an indirect name found, whatever their volume: a busy proxy must not push
// the unit's own workload down a list of which a terminal shows the top.
func TestDiscover_RanksOwnNamesBeforeIndirectOnes(t *testing.T) {
	repo := fstest.MapFS{"go.mod": file("module example.invalid/acme/checkout\n"),
		"deploy/checkout.yaml": file(deployment("checkout", "acme/checkout:1", "envoyproxy/envoy:v1.30")),
		// A second file that lists the proxy, so that as many files name it
		// as name the unit's workload.
		"deploy/worker.yaml": file(deployment("ledger-worker", "acme/ledger-worker:1", "envoyproxy/envoy:v1.30"))}
	p, err := PlanDiscovery(repo, ".", nil)
	require.NoError(t, err)
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		objectSpans: {{records: []map[string]interface{}{
			{fieldNamespace: "mesh", fieldWorkload: "envoy", fieldRecords: float64(900000)},
			{fieldNamespace: "payments", fieldWorkload: "checkout", fieldRecords: float64(10)},
			{fieldNamespace: "payments", fieldWorkload: "acme-checkout-canary", fieldRecords: float64(5)},
		}}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 3)
	var order []string
	for _, c := range r.Candidates {
		order = append(order, c.Workload+" "+c.Tier)
	}
	assert.Equal(t, []string{"checkout exact", "envoy exact", "acme-checkout-canary segment"}, order)
	assert.NotEmpty(t, r.Candidates[1].Indirect)
}

// A name too short to be looked for inside a workload name is not found
// inside an entity name either: it matches whole, or only as a substring,
// which no line binds.
func TestDiscover_ShortNameInsideAnEntityName(t *testing.T) {
	d := newDiscovery(&Plan{tokens: []token{{kind: kindModule, value: "cart", variants: []string{"cart"}},
		{kind: kindModule, value: "checkout", variants: []string{"checkout"}}}})
	for name, want := range map[string]tier{
		"cart":                       tierExact,
		"other-team-cart-legacy":     tierSubstring,
		"other-team-checkout-legacy": tierSegment,
	} {
		got, matched := d.entityMatch(name)
		require.Len(t, matched, 1, name)
		assert.Equal(t, want, got, name)
	}
}
