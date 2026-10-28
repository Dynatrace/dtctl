package apispec

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func widgetSpec(t *testing.T) *Spec {
	t.Helper()
	spec, err := ParseSpec(loadFixture(t, "widget-openapi.yaml"))
	require.NoError(t, err)
	return spec
}

func TestParseSpec_Metadata(t *testing.T) {
	spec := widgetSpec(t)

	require.Equal(t, "Widget Service", spec.Title)
	require.Equal(t, "Core", spec.Category)
	require.Equal(t, "Manage widgets and their sprockets.", spec.Summary)
	// x-api-gateway-url wins over the absolute `url`: it is the path the
	// platform gateway serves the API on, which is what a passthrough needs.
	require.Equal(t, "/platform/widget/v1", spec.BasePath)
	// An unquoted `version: 2.7` is a YAML float, not a string. Rendering it
	// rather than dropping it is the point of strAt's default branch.
	require.Equal(t, "2.7", spec.Version)
}

func TestParseSpec_OperationsAreProjected(t *testing.T) {
	spec := widgetSpec(t)

	ids := make([]string, 0, len(spec.Operations))
	for _, op := range spec.Operations {
		ids = append(ids, op.ID())
	}

	require.ElementsMatch(t, []string{
		"GET /",
		"GET /widgets",
		"POST /widgets",
		"POST /widgets/search",
		"GET /widgets/{id}",
		"PATCH /widgets/{id}",
		"DELETE /widgets/{id}",
		"POST /widgets/{id}:favorite",
		"POST /widgets:purge",
		"POST /sprockets:verify",
		"GET /sprockets",
	}, ids)
}

// TestParseSpec_CollectionRootIsAReachableOperation pins the empty-path case.
//
// A specification may key an operation on "" — the base path itself is the
// endpoint. Carried through verbatim it produced the operation ID "GET ", which a
// caller cannot pass back to --operation, and which Match could never resolve,
// because an incoming request for the base path normalizes to "/". The operation
// was therefore listed and unreachable at once: `exec api` on it would fall to the
// strict fallback and gate a read as a delete.
func TestParseSpec_CollectionRootIsAReachableOperation(t *testing.T) {
	spec := widgetSpec(t)

	op, ok := spec.FindByID("GET /")
	require.True(t, ok, "the collection root must be addressable by a usable ID")
	require.Equal(t, "/", op.Path)
	require.Equal(t, []string{"widget:widgets:read"}, op.Scopes)

	matched, ok := spec.Match("GET", "/platform/widget/v1")
	require.True(t, ok, "a request for the base path must resolve to the collection root")
	require.Equal(t, "GET /", matched.ID())

	// A trailing slash is the same endpoint.
	matched, ok = spec.Match("GET", "/platform/widget/v1/")
	require.True(t, ok)
	require.Equal(t, "GET /", matched.ID())
}

func TestParseSpec_ScopesAndInheritance(t *testing.T) {
	spec := widgetSpec(t)

	cases := []struct {
		id     string
		scopes []string
		access Access
		why    string
	}{
		{"GET /widgets", []string{"widget:widgets:read"}, AccessRead,
			"a declared read scope"},
		{"POST /widgets", []string{"widget:widgets:write"}, AccessWrite,
			"a declared write scope"},
		{"POST /widgets/search", []string{"widget:widgets:read"}, AccessRead,
			"a read-shaped POST: method inference would wrongly call this a mutation"},
		{"POST /widgets/{id}:favorite", []string{"widget:widgets:read"}, AccessRead,
			"a custom action that only reads"},
		{"POST /widgets:purge", []string{"widget:widgets:delete"}, AccessDelete,
			"a POST that deletes: method inference would permit it as a create, " +
				"which readwrite-mine allows while blocking deletes"},
		{"DELETE /widgets/{id}", []string{"widget:widgets:delete"}, AccessDelete,
			"a declared delete scope"},
		{"POST /sprockets:verify", nil, AccessUnknown,
			"`ssoAuth: []` declares nothing, so the operation is unclassifiable"},
		{"GET /sprockets", []string{"widget:widgets:read"}, AccessRead,
			"no operation-level security: inherits the document's declaration"},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			op, ok := spec.FindByID(tc.id)
			require.True(t, ok, "operation %s not found", tc.id)
			require.Equal(t, tc.scopes, op.Scopes, tc.why)
			require.Equal(t, tc.access, op.Access(), tc.why)
		})
	}
}

// TestParseSpec_EmptySecurityIsNotFreeAccess isolates the single most dangerous
// misreading available here: an explicitly empty scope list looks like "no scope
// needed" and is actually "the document says nothing".
func TestParseSpec_EmptySecurityIsNotFreeAccess(t *testing.T) {
	spec := widgetSpec(t)

	op, ok := spec.FindByID("POST /sprockets:verify")
	require.True(t, ok)
	require.Empty(t, op.Scopes)
	require.Equal(t, AccessUnknown, op.Access(),
		"an empty declaration must never classify as AccessRead")
}

func TestParseSpec_ParametersMergeAndDeref(t *testing.T) {
	spec := widgetSpec(t)

	t.Run("operation parameters with a $ref", func(t *testing.T) {
		op, ok := spec.FindByID("GET /widgets")
		require.True(t, ok)

		byName := map[string]Parameter{}
		for _, p := range op.Parameters {
			byName[p.Name] = p
		}

		require.Equal(t, "integer(int32)", byName["page-size"].Type)
		require.Equal(t, "query", byName["page-size"].In)

		// page-key arrives only through #/components/parameters/PageKey, so its
		// presence proves the one-hop resolution ran.
		pk, found := byName["page-key"]
		require.True(t, found, "a $ref'd parameter must be resolved, not dropped")
		require.Equal(t, "string", pk.Type)
		require.Equal(t, "Cursor from a previous page.", pk.Description)
	})

	t.Run("path-level parameters apply to every operation", func(t *testing.T) {
		// `id` is declared once on the path, not on the three operations under it.
		for _, id := range []string{"GET /widgets/{id}", "PATCH /widgets/{id}", "DELETE /widgets/{id}"} {
			op, ok := spec.FindByID(id)
			require.True(t, ok)
			require.Len(t, op.Parameters, 1, "%s should inherit the path parameter", id)
			require.Equal(t, "id", op.Parameters[0].Name)
			require.Equal(t, "path", op.Parameters[0].In)
			require.True(t, op.Parameters[0].Required)
		}
	})
}

func TestParseSpec_RequestBodySchemaIsDereferenced(t *testing.T) {
	spec := widgetSpec(t)

	op, ok := spec.FindByID("POST /widgets")
	require.True(t, ok)
	require.NotNil(t, op.RequestBody)
	require.True(t, op.RequestBody.Required)
	require.Len(t, op.RequestBody.Contents, 1)
	require.Equal(t, "application/json", op.RequestBody.Contents[0].ContentType)

	// The fixture writes `$ref: '#/components/schemas/Widget'`. Resolving it is
	// what makes the operation composable without the rest of the document — the
	// acceptance bar for `describe api --operation`.
	schema, isMap := op.RequestBody.Contents[0].Schema.(map[string]any)
	require.True(t, isMap, "request body schema must resolve to an object, got %T",
		op.RequestBody.Contents[0].Schema)
	require.Equal(t, "object", schema["type"])

	props, isMap := schema["properties"].(map[string]any)
	require.True(t, isMap)
	require.Contains(t, props, "name")
	require.Contains(t, props, "sprockets")
}

// TestParseSpec_RefResolutionStopsAfterOneHop pins the deliberate limit. The
// WidgetList schema's `widgets.items` is itself a $ref, and it stays a $ref:
// resolving transitively is what makes a projection as expensive as the raw
// document.
func TestParseSpec_RefResolutionStopsAfterOneHop(t *testing.T) {
	spec := widgetSpec(t)

	op, ok := spec.FindByID("GET /widgets")
	require.True(t, ok)
	require.NotEmpty(t, op.Responses)

	var listSchema map[string]any
	for _, r := range op.Responses {
		if r.Status != "200" {
			continue
		}
		require.Len(t, r.Contents, 1)
		listSchema, _ = r.Contents[0].Schema.(map[string]any)
	}
	require.NotNil(t, listSchema, "the top-level response $ref must be resolved")

	props, _ := listSchema["properties"].(map[string]any)
	widgets, _ := props["widgets"].(map[string]any)
	items, _ := widgets["items"].(map[string]any)
	require.Contains(t, items, "$ref",
		"a nested $ref must remain unresolved: resolution is one hop by design")
}

func TestSpec_MatchConcretePaths(t *testing.T) {
	spec := widgetSpec(t)

	cases := []struct {
		method, path string
		want         string
		why          string
	}{
		{"GET", "/platform/widget/v1/widgets", "GET /widgets",
			"an absolute path is matched after stripping the base path"},
		{"GET", "/widgets", "GET /widgets",
			"a path already relative to the base is matched as-is"},
		{"GET", "/platform/widget/v1/widgets/abc-123", "GET /widgets/{id}",
			"a placeholder matches one segment"},
		{"POST", "/platform/widget/v1/widgets/search", "POST /widgets/search",
			"a literal path beats the templated sibling it collides with"},
		{"POST", "/platform/widget/v1/widgets/abc-123:favorite", "POST /widgets/{id}:favorite",
			"a placeholder can be part of a segment, not just a whole one"},
		{"DELETE", "/platform/widget/v1/widgets/abc-123", "DELETE /widgets/{id}",
			"method selects among operations on the same template"},
		{"GET", "/platform/widget/v1/widgets/", "GET /widgets",
			"a trailing slash does not change the target"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			op, ok := spec.Match(tc.method, tc.path)
			require.True(t, ok, tc.why)
			require.Equal(t, tc.want, op.ID(), tc.why)
		})
	}

	t.Run("an unmatched path is a miss, not a guess", func(t *testing.T) {
		_, ok := spec.Match("GET", "/platform/widget/v1/gadgets")
		require.False(t, ok)
	})

	t.Run("a placeholder never spans a segment boundary", func(t *testing.T) {
		// /widgets/{id} must not swallow /widgets/a/b.
		_, ok := spec.Match("GET", "/platform/widget/v1/widgets/a/b")
		require.False(t, ok)
	})
}

// TestParseSpec_ClassicJSONDocument covers the one base path where the
// openapi.yaml convention does not hold. The classic surface serves spec3.json,
// and because JSON is a subset of YAML the same parser handles it — so a classic
// entry must never be a silent parse failure.
func TestParseSpec_ClassicJSONDocument(t *testing.T) {
	spec, err := ParseSpec(loadFixture(t, "legacy-spec3.json"))
	require.NoError(t, err)

	require.Equal(t, "Legacy Example API", spec.Title)
	require.Equal(t, "/platform/classic/example-api/v2", spec.BasePath,
		"with no x-api-gateway-url, servers[0].url is the base path")
	require.Len(t, spec.Operations, 1)
	require.Equal(t, "GET /things", spec.Operations[0].ID())
}

func TestParseSpec_Rejects(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"not a document", "just a string"},
		{"no paths section", "openapi: 3.0.3\ninfo:\n  title: X\n"},
		{"malformed yaml", "openapi: [unterminated\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSpec([]byte(tc.body))
			require.Error(t, err)
		})
	}
}

// TestParseSpec_TolerantOfMalformedSections is the fail-soft requirement at the
// document level: a section of the wrong type degrades that section only. The
// whole package rests on an undocumented convention, so a document that drifts
// must lose a field rather than the whole listing.
func TestParseSpec_TolerantOfMalformedSections(t *testing.T) {
	spec, err := ParseSpec([]byte(`
openapi: 3.0.3
info: "a string where a mapping belongs"
servers: "also wrong"
paths:
  /ok:
    get:
      summary: Still projected
      parameters: "not a list"
      responses: "not a map"
`))
	require.NoError(t, err, "a wrongly-typed section must not fail the parse")
	require.Empty(t, spec.Title)
	require.Len(t, spec.Operations, 1)
	require.Equal(t, "GET /ok", spec.Operations[0].ID())
	require.Empty(t, spec.Operations[0].Parameters)
}

func TestRegistry_EntryHelpers(t *testing.T) {
	cases := []struct {
		url      string
		basePath string
		external bool
		classic  bool
	}{
		{"/platform/widget/v1/openapi.yaml", "/platform/widget/v1", false, false},
		{"/platform/classic/example-api/v2/spec3.json", "/platform/classic/example-api/v2", false, true},
		// A cache-busting query must not leak into the derived base path — and,
		// per gcx's hard-won lesson, must never become part of a filename.
		{"/platform/widget/v1/openapi.yaml?hash=abc123", "/platform/widget/v1", false, false},
		{"https://example.invalid/api/v2/spec3.json", "", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			e := Entry{Name: "Example", URL: tc.url}
			require.Equal(t, tc.basePath, e.BasePath())
			require.Equal(t, tc.external, e.External())
			require.Equal(t, tc.classic, e.Classic())
		})
	}
}

func testRegistry() *Registry {
	return &Registry{Entries: []Entry{
		{Name: "Widget Service", URL: "/platform/widget/v1/openapi.yaml"},
		{Name: "Sprocket Service", URL: "/platform/sprocket/v1/openapi.yaml"},
		{Name: "Sprocket Admin Service", URL: "/platform/sprocket/admin/v1/openapi.yaml"},
		{Name: "Legacy Example API", URL: "/platform/classic/example-api/v2/spec3.json"},
		{Name: "External Docs", URL: "https://example.invalid/api/v2/spec3.json"},
	}}
}

func TestRegistry_FindEntry(t *testing.T) {
	reg := testRegistry()

	t.Run("exact base path", func(t *testing.T) {
		e, ok := reg.FindEntry("/platform/widget/v1")
		require.True(t, ok)
		require.Equal(t, "Widget Service", e.Name)
	})

	t.Run("exact name, case-insensitive", func(t *testing.T) {
		e, ok := reg.FindEntry("widget service")
		require.True(t, ok)
		require.Equal(t, "Widget Service", e.Name)
	})

	t.Run("unique substring", func(t *testing.T) {
		e, ok := reg.FindEntry("widget")
		require.True(t, ok)
		require.Equal(t, "Widget Service", e.Name)
	})

	t.Run("ambiguous substring is a miss", func(t *testing.T) {
		_, ok := reg.FindEntry("sprocket")
		require.False(t, ok, "two entries match; resolving one of them silently would be wrong")

		require.Len(t, reg.Candidates("sprocket"), 2,
			"the caller needs the candidates to disambiguate")
	})

	t.Run("an exact name wins over being a substring of another", func(t *testing.T) {
		e, ok := reg.FindEntry("Sprocket Service")
		require.True(t, ok)
		require.Equal(t, "Sprocket Service", e.Name)
	})

	// The no-oracle rule: a miss must fail as a miss. Nothing here may consult,
	// construct, or probe a path the registry did not return — that would let
	// dtctl confirm the existence of APIs an environment deliberately omits.
	t.Run("a miss returns nothing and synthesizes no path", func(t *testing.T) {
		for _, q := range []string{"nonexistent", "/platform/nonexistent/v1", ""} {
			_, ok := reg.FindEntry(q)
			require.False(t, ok, "query %q must miss", q)
		}
	})
}

func TestRegistry_EntryForPath(t *testing.T) {
	reg := testRegistry()

	cases := []struct {
		path string
		want string
		why  string
	}{
		{"/platform/widget/v1/widgets", "Widget Service", "the owning API by base-path prefix"},
		{"/platform/widget/v1", "Widget Service", "the base path itself"},
		{"/platform/sprocket/admin/v1/things", "Sprocket Admin Service",
			"the longest matching prefix wins over the shorter one it nests under"},
		{"/platform/sprocket/v1/things", "Sprocket Service", "the shorter prefix when it is the match"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			e, ok := reg.EntryForPath(tc.path)
			require.True(t, ok, tc.why)
			require.Equal(t, tc.want, e.Name, tc.why)
		})
	}

	t.Run("a prefix must end on a segment boundary", func(t *testing.T) {
		// /platform/widget/v1 must not claim /platform/widget/v1beta.
		_, ok := reg.EntryForPath("/platform/widget/v1beta/widgets")
		require.False(t, ok)
	})

	t.Run("an unowned path has no entry", func(t *testing.T) {
		_, ok := reg.EntryForPath("/platform/unknown/v1/things")
		require.False(t, ok)
	})
}

// newTestHandler wires a Handler to a local server. Retries are disabled so a
// deliberate error response does not cost the retry budget.
func newTestHandler(t *testing.T, h http.Handler) *Handler {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.EXAMPLE"))
	require.NoError(t, err)
	c.HTTP().SetRetryCount(0)
	return NewHandler(c)
}

func TestHandler_Registry(t *testing.T) {
	var hits int
	h := newTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, RegistryPath, r.URL.Path)
		hits++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"urls":[
			{"name":"Widget Service","url":"/platform/widget/v1/openapi.yaml"},
			{"name":"External","url":"https://example.invalid/api/v2/spec3.json"}
		]}`)
	}))

	reg, err := h.Registry()
	require.NoError(t, err)
	require.Len(t, reg.Entries, 2)
	require.Equal(t, "Widget Service", reg.Entries[0].Name)

	// The listing is one HTTP request, and a drill-down must not repeat it.
	again, err := h.Registry()
	require.NoError(t, err)
	require.Same(t, reg, again)
	require.Equal(t, 1, hits, "the registry must be fetched once per handler")
}

// TestHandler_RegistryFailsSoft covers the fail-soft contract for the index.
// Because the index is an observed convention rather than a documented one, its
// absence has to be an ordinary, well-typed outcome — never a raw 404 body.
func TestHandler_RegistryFailsSoft(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"missing", http.StatusNotFound, `{"error":{"code":404,"message":"not found"}}`, ""},
		{"unparseable", http.StatusOK, "\t\x00not yaml: [", ""},
		{"empty index", http.StatusOK, `{"urls":[]}`, "no specification entries"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))

			_, err := h.Registry()
			require.Error(t, err)

			var unavailable *RegistryUnavailableError
			require.ErrorAs(t, err, &unavailable,
				"callers switch on this type to print the Swagger UI fallback")
			require.Contains(t, unavailable.Error(), RegistryPath)
			require.NotContains(t, unavailable.Error(), "404",
				"the message must not surface a raw status or response body")
			// The cause stays reachable through Unwrap for diagnostics, but is
			// deliberately kept out of the user-facing message.
			require.NotNil(t, unavailable.Err, "the cause must remain available")
			if tc.wantErr != "" {
				require.ErrorContains(t, unavailable.Err, tc.wantErr)
			}
		})
	}
}

// TestHandler_SpecIgnoresContentType pins the rule that cost the most to learn:
// the same specification is served as at least four media types, with and
// without a charset, so parsing must never branch on Content-Type.
func TestHandler_SpecIgnoresContentType(t *testing.T) {
	body := loadFixture(t, "widget-openapi.yaml")

	contentTypes := []string{
		"application/openapi+yaml",
		"application/vnd.oai.openapi",
		"application/x-yaml",
		"application/yaml",
		"application/yaml; charset=utf-8",
		"text/plain",
		"", // no Content-Type at all
	}

	for _, ct := range contentTypes {
		name := ct
		if name == "" {
			name = "(none)"
		}
		t.Run(name, func(t *testing.T) {
			h := newTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if ct != "" {
					w.Header().Set("Content-Type", ct)
				}
				_, _ = w.Write(body)
			}))

			spec, err := h.Spec("/platform/widget/v1/openapi.yaml")
			require.NoError(t, err)
			require.Equal(t, "Widget Service", spec.Title)
		})
	}
}

func TestHandler_SpecMemoizesAndFallsBackToDocumentLocation(t *testing.T) {
	var hits int
	h := newTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		// No `servers` block: the base path must fall back to the document's own
		// location, so a passthrough can still resolve paths against it.
		fmt.Fprint(w, "openapi: 3.0.3\ninfo:\n  title: Bare\npaths:\n  /things:\n    get:\n      summary: List\n")
	}))

	spec, err := h.Spec("/platform/bare/v1/openapi.yaml")
	require.NoError(t, err)
	require.Equal(t, "/platform/bare/v1", spec.BasePath)

	again, err := h.Spec("/platform/bare/v1/openapi.yaml")
	require.NoError(t, err)
	require.Same(t, spec, again)
	require.Equal(t, 1, hits, "a drill-down must cost one fetch per API")
}

func TestHandler_SpecForEntryRefusesExternal(t *testing.T) {
	h := newTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an external entry must not be fetched")
		w.WriteHeader(http.StatusOK)
	}))

	_, err := h.SpecForEntry(Entry{Name: "External Docs", URL: "https://example.invalid/api/v2/spec3.json"})
	require.ErrorContains(t, err, "external URL")
}

func TestSpecPathForBase(t *testing.T) {
	require.Equal(t, "/platform/widget/v1/openapi.yaml", SpecPathForBase("/platform/widget/v1"))
	require.Equal(t, "/platform/widget/v1/openapi.yaml", SpecPathForBase("/platform/widget/v1/"))
}
