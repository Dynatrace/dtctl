//go:build integration
// +build integration

package e2e

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	resapi "github.com/Dynatrace/dtctl/pkg/resources/api"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/test/integration"
)

// The assertions in this file are deliberately derived from whatever the
// environment publishes, never from a list of expected APIs.
//
// Two reasons, and both are requirements rather than preferences. First,
// disclosure: naming a non-public API in a public repository is the leak, and a
// fixture list would be exactly that. Second, correctness: the API index is a
// property of the environment, so any hard-coded expectation would encode one
// tenant's shape and fail on another — which is the bug this feature exists to
// avoid, not one to write into its tests.
//
// The tests therefore assert *invariants of the mechanism*: the index parses,
// every entry round-trips through resolution, a specification either parses or
// fails with a typed error, and classification never returns a verdict looser than
// the method alone would justify. Those hold on every tenant.

func TestAPIDiscoveryAgainstALiveEnvironment(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := resapi.NewHandler(env.Client)

	reg, err := handler.Registry()
	if err != nil {
		// An environment that does not publish the index at all is a legitimate
		// state, but not one this test can say anything about.
		t.Skipf("environment publishes no API index: %v", err)
	}
	if len(reg.Entries) == 0 {
		t.Skip("environment's API index is empty")
	}
	t.Logf("index lists %d APIs", len(reg.Entries))

	t.Run("every entry is usable", func(t *testing.T) {
		for _, e := range reg.Entries {
			if e.Name == "" {
				t.Errorf("index entry with URL %q has no name", e.URL)
				continue
			}
			if e.URL == "" {
				t.Errorf("index entry %q has no document URL", e.Name)
			}
			// An external entry is documented on another host: dtctl lists it
			// because the environment did, but it has no gateway base path.
			if !e.External() && e.BasePath() == "" {
				t.Logf("note: %q publishes no derivable base path", e.Name)
			}
		}
	})

	t.Run("names round-trip through resolution", func(t *testing.T) {
		for _, e := range reg.Entries {
			got, err := handler.Resolve(e.Name)
			if err != nil {
				t.Errorf("Resolve(%q) failed although the index lists it: %v", e.Name, err)
				continue
			}
			if got.URL != e.URL {
				t.Errorf("Resolve(%q) returned the entry for %q", e.Name, got.URL)
			}
		}
	})

	t.Run("base paths round-trip through resolution", func(t *testing.T) {
		for _, e := range reg.Entries {
			base := e.BasePath()
			if base == "" {
				continue
			}
			got, err := handler.Resolve(base)
			if err != nil {
				t.Errorf("Resolve(%q) failed for %q: %v", base, e.Name, err)
				continue
			}
			if got.BasePath() != base {
				t.Errorf("Resolve(%q) returned base path %q", base, got.BasePath())
			}
		}
	})

	// The whole point of the typed fetch error: an environment may publish its
	// index and refuse its documents (an interactive-session-only SSO policy does
	// exactly this), and that must degrade one row rather than break the feature.
	// This test passes on a tenant that serves every document and on one that
	// serves none.
	t.Run("a specification either parses or fails with a typed error", func(t *testing.T) {
		var parsed, unavailable int

		for _, e := range reg.Entries {
			if e.External() {
				continue
			}
			spec, err := handler.Spec(e)
			if err != nil {
				var unavailableErr *resapi.SpecUnavailableError
				if !errors.As(err, &unavailableErr) {
					t.Errorf("Spec(%q) failed with an untyped error, so no caller can "+
						"explain it: %v", e.Name, err)
					continue
				}
				if unavailableErr.DocPath == "" {
					t.Errorf("Spec(%q): the error does not name the document it could not read", e.Name)
				}
				unavailable++
				continue
			}
			if spec == nil {
				t.Errorf("Spec(%q) returned no error and no specification", e.Name)
				continue
			}
			parsed++

			for _, op := range spec.Operations {
				id := op.ID()
				method, path, found := strings.Cut(id, " ")
				if !found || method != strings.ToUpper(method) || !strings.HasPrefix(path, "/") {
					t.Errorf("%q: operation ID %q is not \"METHOD /path\", so it cannot be "+
						"passed back to --operation", e.Name, id)
				}
			}
		}

		t.Logf("%d specifications parsed, %d unavailable", parsed, unavailable)
		if parsed == 0 && unavailable == 0 {
			t.Skip("no non-external specifications to check")
		}
	})
}

func TestAPIClassificationAgainstALiveEnvironment(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := resapi.NewHandler(env.Client)

	reg, err := handler.Registry()
	if err != nil {
		t.Skipf("environment publishes no API index: %v", err)
	}

	// The invariant that matters: whatever a specification says, a request is never
	// gated *looser* than the HTTP method alone would justify. A DELETE cannot come
	// out as a read because a document claimed a read scope.
	t.Run("a specification can never loosen the method's floor", func(t *testing.T) {
		strictness := map[safety.Operation]int{
			safety.OperationRead:         0,
			safety.OperationCreate:       1,
			safety.OperationUpdate:       2,
			safety.OperationDelete:       3,
			safety.OperationDeleteBucket: 4,
		}
		floors := map[string]safety.Operation{
			http.MethodGet:    safety.OperationRead,
			http.MethodPost:   safety.OperationRead,
			http.MethodPut:    safety.OperationUpdate,
			http.MethodPatch:  safety.OperationUpdate,
			http.MethodDelete: safety.OperationDelete,
		}

		checked := 0
		for _, e := range reg.Entries {
			if e.External() || e.BasePath() == "" {
				continue
			}
			spec, err := handler.Spec(e)
			if err != nil {
				continue // covered by the discovery test; unavailability is not a failure
			}
			for i := range spec.Operations {
				op := &spec.Operations[i]
				method, path, found := strings.Cut(op.ID(), " ")
				if !found {
					continue
				}
				floor, known := floors[method]
				if !known {
					continue
				}
				requestPath := e.BasePath() + path
				class := resapi.Classify(method, requestPath, e.Name, op)
				if strictness[class.SafetyOp] < strictness[floor] {
					t.Errorf("%s %s classified as %s, which is looser than the %s floor (%s)",
						method, requestPath, class.SafetyOp, method, floor)
				}
				if class.Reason == "" {
					t.Errorf("%s %s produced a verdict with no explanation", method, requestPath)
				}
				checked++
			}
		}
		t.Logf("classified %d live operations", checked)
	})

	// A read is the one thing a passthrough must always be able to do, on any
	// tenant, at the strictest safety level.
	t.Run("a GET on any listed API is a read", func(t *testing.T) {
		for _, e := range reg.Entries {
			base := e.BasePath()
			if base == "" {
				continue
			}
			_, op := handler.ResolveForRequest(http.MethodGet, base)
			class := resapi.Classify(http.MethodGet, base, e.Name, op)
			if class.SafetyOp != safety.OperationRead {
				t.Errorf("GET %s classified as %s: %s", base, class.SafetyOp, class.Reason)
			}
		}
	})

	// Coverage is dtctl's own claim about itself, so it must be checkable against a
	// live index: a base path dtctl says it wraps has to actually be published, or
	// the claim is stale.
	t.Run("coverage claims match the live index", func(t *testing.T) {
		published := map[string]bool{}
		for _, e := range reg.Entries {
			if b := e.BasePath(); b != "" {
				published[b] = true
			}
		}
		for base := range resapi.CoveredBasePaths() {
			if !published[base] {
				// Not a failure: coverage spans every tenant shape, and any one tenant
				// may not run the service. It is worth surfacing, though, since a base
				// path no tenant publishes is a candidate for removal.
				t.Logf("note: coverage claims %s, which this environment does not publish", base)
			}
		}
	})
}

// TestExecAPIPassthroughReadAgainstALiveEnvironment exercises the passthrough
// end-to-end on a read, which is the only thing this test may safely do: a
// generic-write E2E would need an endpoint that is harmless on every tenant, and
// no such endpoint exists.
func TestExecAPIPassthroughReadAgainstALiveEnvironment(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := resapi.NewHandler(env.Client)

	reg, err := handler.Registry()
	if err != nil {
		t.Skipf("environment publishes no API index: %v", err)
	}

	// Find a GET the specification declares a read scope for, on an API dtctl
	// already wraps — so the endpoint is one this token is expected to reach.
	var method, requestPath string
	for _, e := range reg.Entries {
		if e.External() || resapi.NativeResourceFor(e.BasePath()) == "" {
			continue
		}
		spec, err := handler.Spec(e)
		if err != nil {
			continue
		}
		for i := range spec.Operations {
			m, path, found := strings.Cut(spec.Operations[i].ID(), " ")
			if !found || m != http.MethodGet || strings.Contains(path, "{") {
				continue // skip templated paths: no id to substitute
			}
			method, requestPath = m, e.BasePath()+path
			break
		}
		if requestPath != "" {
			break
		}
	}
	if requestPath == "" {
		t.Skip("no un-templated GET on a natively covered API to exercise")
	}

	t.Logf("calling %s %s", method, requestPath)
	resp, err := env.Client.HTTP().R().Execute(method, requestPath)
	if err != nil {
		t.Fatalf("%s %s: %v", method, requestPath, err)
	}
	if resp.StatusCode() >= 500 {
		t.Fatalf("%s %s returned %d", method, requestPath, resp.StatusCode())
	}
	if resp.StatusCode() >= 400 {
		// 401/403 is a token-scope property of the environment, not a dtctl defect.
		t.Skipf("%s %s returned %d — the token does not reach this endpoint",
			method, requestPath, resp.StatusCode())
	}

	_, op := handler.ResolveForRequest(method, requestPath)
	if op == nil {
		t.Errorf("the request succeeded but ResolveForRequest could not match it back to "+
			"an operation, so exec api would gate it as a delete: %s %s", method, requestPath)
	}
}
