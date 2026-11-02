package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/aidetect"
)

// Everything the mock environment serves is synthetic. It has to be: a recording
// of a real environment's API index would commit that environment's published API
// list to a public repository, and the list is a property of the environment
// rather than of dtctl.
const (
	mockRegistry = `{"urls":[
		{"name":"Widget Service","url":"/platform/widget/v1/openapi.yaml"},
		{"name":"Document Service","url":"/platform/document/v1/openapi.yaml"},
		{"name":"Sprocket Service","url":"/platform/sprocket/v1/openapi.yaml"},
		{"name":"Legacy Example API","url":"/example-tree/legacy/v1/openapi.yaml"},
		{"name":"Elsewhere API","url":"https://example.invalid/openapi.yaml"}
	]}`

	mockWidgetSpec = `openapi: 3.0.3
info:
  title: Widget
  version: 2.4.1
  x-service-category: Widgets
  x-summary: Manage widgets.
servers:
  - url: /platform/widget/v1
    x-api-gateway-url: /platform/widget/v1
paths:
  /widgets:
    get:
      operationId: listWidgets
      summary: List all widgets.
      security:
        - ssoAuth: ["widget:widgets:read"]
      parameters:
        - name: page-size
          in: query
          required: false
          schema:
            type: integer
          description: How many widgets per page.
    post:
      operationId: createWidget
      summary: Create a widget.
      security:
        - ssoAuth: ["widget:widgets:write"]
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name:
                  type: string
                  description: The widget's name.
                colour:
                  type: string
                  description: Optional colour.
      responses:
        "201":
          description: Created.
          content:
            application/json:
              schema:
                type: object
  # A POST that only reads. Real platform APIs are full of these (search,
  # validate, preview, autocomplete), and they are why the method cannot decide
  # how a request is gated: classifying POST as a write would refuse a read.
  /widgets:search:
    post:
      operationId: searchWidgets
      summary: Search widgets.
      security:
        - ssoAuth: ["widget:widgets:read"]
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                match:
                  type: string
  /widgets/{id}:
    put:
      operationId: replaceWidget
      summary: Replace a widget.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/Widget"
    delete:
      operationId: deleteWidget
      summary: Delete a widget.
      security:
        - ssoAuth: ["widget:widgets:delete"]
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
components:
  schemas:
    Widget:
      type: object
      properties:
        name:
          type: string
`
)

// mockEnvironment serves a synthetic API index and specification, and records
// every path it was asked for so a test can assert what dtctl did *not* request.
type mockEnvironment struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []string
	// calls records "METHOD /path" for every request, which is what a passthrough
	// test needs: the interesting assertion is usually that a request the gate
	// refused never reached the wire at all.
	calls []string
	// bodies records the request body per "METHOD /path", so a test can prove that
	// a body read through the vfs seam arrived intact.
	bodies map[string]string
	// specStatus overrides the status for a specification path, e.g. 403 for an
	// environment that publishes its index but not its documents.
	specStatus map[string]int
	// canned holds responses for concrete request paths, keyed "METHOD /path", so
	// `exec api` can be exercised against the same environment that publishes the
	// specification it is classified from.
	canned map[string]mockResponse
}

// mockResponse is a canned reply for one endpoint.
type mockResponse struct {
	status      int
	contentType string
	body        string
}

func newMockEnvironment(t *testing.T) *mockEnvironment {
	return newMockEnvironmentAt(t, "readonly")
}

// newMockEnvironmentAt is newMockEnvironment with an explicit safety level, for
// the tests that assert what each level permits.
func newMockEnvironmentAt(t *testing.T, safetyLevel string) *mockEnvironment {
	t.Helper()

	m := &mockEnvironment{
		specStatus: map[string]int{},
		canned:     map[string]mockResponse{},
		bodies:     map[string]string{},
	}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		body, _ := io.ReadAll(r.Body)

		m.mu.Lock()
		m.requests = append(m.requests, r.URL.Path)
		m.calls = append(m.calls, key)
		m.bodies[key] = string(body)
		status := m.specStatus[r.URL.Path]
		canned, hasCanned := m.canned[key]
		m.mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, `{"error":{"code":403,"message":"refused"}}`)
			return
		}

		if hasCanned {
			if canned.contentType != "" {
				w.Header().Set("Content-Type", canned.contentType)
			}
			w.WriteHeader(canned.status)
			_, _ = fmt.Fprint(w, canned.body)
			return
		}

		switch r.URL.Path {
		case "/platform/metadata/v1/swagger-ui.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, mockRegistry)
		case "/platform/widget/v1/openapi.yaml":
			// Deliberately a content type that is not YAML's: the same artifact
			// ships under four of them, so parsing must never branch on it.
			w.Header().Set("Content-Type", "application/vnd.oai.openapi")
			_, _ = fmt.Fprint(w, mockWidgetSpec)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
		}
	}))
	t.Cleanup(m.server.Close)

	writeIsolatedConfig(t, safetyLevelConfig(m.server.URL, safetyLevel))
	return m
}

func (m *mockEnvironment) requested(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.requests, path)
}

func (m *mockEnvironment) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// respond registers a canned reply for one endpoint.
func (m *mockEnvironment) respond(method, path string, r mockResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.canned[method+" "+path] = r
}

// called reports whether a request was actually sent. A gate that refuses must
// refuse *before* the wire, and this is how a test proves it.
func (m *mockEnvironment) called(method, path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.calls, method+" "+path)
}

// bodyOf returns the body a request carried.
func (m *mockEnvironment) bodyOf(method, path string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bodies[method+" "+path]
}

// writeIsolatedConfig installs a config file and the agent-detection scrubbing
// that every Run-level test needs.
func writeIsolatedConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	t.Setenv("DTCTL_CONFIG", path)
	t.Setenv("DTCTL_PROFILE", "")
	for _, v := range aidetect.EnvVars() {
		t.Setenv(v, "")
	}
}

// TestGetAPIsMirrorsTheIndex pins the central rule of the listing: dtctl shows
// what the environment's index shows, adding and hiding nothing. Any client-side
// filter would have to name the APIs it hides, and naming them in open source is
// itself the disclosure.
func TestGetAPIsMirrorsTheIndex(t *testing.T) {
	env := newMockEnvironment(t)

	code, out := captureRun(t, []string{"get", "apis"}, RunOptions{})
	require.Zero(t, code, out)

	for _, name := range []string{"Widget Service", "Document Service", "Sprocket Service",
		"Legacy Example API", "Elsewhere API"} {
		require.Contains(t, out, name, "every index entry must be listed")
	}
	require.Contains(t, out, "/platform/widget/v1")
	// The coverage column names the native command for an API dtctl already wraps.
	require.Regexp(t, `/platform/document/v1\s+document`, out)

	require.Equal(t, 1, env.requestCount(),
		"the default listing must cost exactly one request — no specification fetches")
}

// TestGetAPIsUncoveredIsAContributionBacklog pins both filters that make
// --uncovered a backlog rather than a listing: covered APIs drop out, and so does
// anything off the public platform tree, which satisfies neither condition of the
// curation rule it feeds.
func TestGetAPIsUncoveredIsAContributionBacklog(t *testing.T) {
	newMockEnvironment(t)

	code, out := captureRun(t, []string{"get", "apis", "--uncovered"}, RunOptions{})
	require.Zero(t, code, out)

	require.Contains(t, out, "Widget Service", "an uncovered platform API is a candidate")
	require.NotContains(t, out, "Document Service", "dtctl already wraps it")
	require.NotContains(t, out, "Legacy Example API", "not on the public platform tree")
	require.NotContains(t, out, "Elsewhere API", "documented on another host; no base path")
}

// TestGetAPIsOpsCountFailsSoftPerRow pins that one unreadable document degrades
// its own row and nothing else. An environment that publishes its index but
// refuses its documents is a real configuration, and a listing that fails
// wholesale there would be useless.
func TestGetAPIsOpsCountFailsSoftPerRow(t *testing.T) {
	env := newMockEnvironment(t)
	env.specStatus["/platform/document/v1/openapi.yaml"] = http.StatusForbidden

	code, out := captureRun(t, []string{"get", "apis", "--ops-count", "-o", "json"}, RunOptions{})
	require.Zero(t, code, out)

	require.Contains(t, out, `"operations": 5`, "the readable specification is still counted")
	require.Contains(t, out, `"spec_error"`, "the unreadable one carries its reason")
	require.Contains(t, out, "Document Service", "and keeps its row")
}

// TestDescribeAPIProjectsRatherThanDumps pins the operation index: every
// operation is present, and the raw document is not.
func TestDescribeAPIProjectsRatherThanDumps(t *testing.T) {
	newMockEnvironment(t)

	code, out := captureRun(t, []string{"describe", "api", "Widget Service"}, RunOptions{})
	require.Zero(t, code, out)

	require.Contains(t, out, "GET /widgets")
	require.Contains(t, out, "POST /widgets")
	require.Contains(t, out, "DELETE /widgets/{id}")
	require.Contains(t, out, "widget:widgets:read", "the declared scope is part of the projection")
	require.NotContains(t, out, "openapi: 3.0.3", "the projection is not the document")
}

// TestDescribeAPIOperationDetailIsEnoughToComposeACall pins the drill-down: a
// caller must be able to build the request from it without the rest of the
// document, which is what makes `exec api` usable at all.
func TestDescribeAPIOperationDetailIsEnoughToComposeACall(t *testing.T) {
	newMockEnvironment(t)

	code, out := captureRun(t,
		[]string{"describe", "api", "Widget Service", "--operation", "POST /widgets"}, RunOptions{})
	require.Zero(t, code, out)

	require.Contains(t, out, "/platform/widget/v1/widgets", "the full request path, already joined")
	require.Contains(t, out, "widget:widgets:write")
	require.Contains(t, out, "application/json")
	require.Contains(t, out, "name", "required body properties are shown")
	require.Contains(t, out, "dtctl exec api /platform/widget/v1/widgets -X POST",
		"the detail ends in a runnable invocation")
}

// TestDescribeAPIUndeclaredScopeIsNotSilence pins that an operation with no
// declared scope says so. Rendering nothing would read as "no scope required",
// and two of six sampled APIs declare no per-operation scopes at all.
func TestDescribeAPIUndeclaredScopeIsNotSilence(t *testing.T) {
	newMockEnvironment(t)

	code, out := captureRun(t,
		[]string{"describe", "api", "Widget Service", "--operation", "DELETE /widgets/{id}"}, RunOptions{})
	require.Zero(t, code, out)
	require.Contains(t, out, "widget:widgets:delete")

	// PUT declares no security at all, which is common and is not the same as
	// "no scope required".
	code, out = captureRun(t,
		[]string{"describe", "api", "Widget Service", "--operation", "PUT /widgets/{id}"}, RunOptions{})
	require.Zero(t, code, out)
	require.Contains(t, out, "not declared in the specification",
		"an absent declaration must be stated, not rendered as blank")
}

// TestDescribeAPINameMissIsNotAnExistenceOracle is the disclosure guard. A name
// dtctl cannot find in the index must fail as a miss: synthesizing a candidate
// path and probing it would turn the command into a prober that confirms whether
// an API exists on an environment whose index omits it.
func TestDescribeAPINameMissIsNotAnExistenceOracle(t *testing.T) {
	env := newMockEnvironment(t)

	code, out := captureRun(t, []string{"describe", "api", "nonexistent-service"}, RunOptions{})
	require.NotZero(t, code, "a miss must fail")
	require.NotContains(t, out, "nonexistent-service/v1",
		"no synthesized path may appear in the output")

	require.Equal(t, 1, env.requestCount(),
		"only the index may be requested; a miss must probe nothing")
	require.True(t, env.requested("/platform/metadata/v1/swagger-ui.json"))
}

// TestDescribeAPIAcceptsAnExplicitBasePath is the other half of the rule: a
// caller who already knows a base path is told nothing new by dtctl using it.
func TestDescribeAPIAcceptsAnExplicitBasePath(t *testing.T) {
	env := newMockEnvironment(t)

	code, out := captureRun(t, []string{"describe", "api", "/platform/widget/v1"}, RunOptions{})
	require.Zero(t, code, out)
	require.Contains(t, out, "GET /widgets")
	require.True(t, env.requested("/platform/widget/v1/openapi.yaml"))
}

// TestDescribeAPIRawWritesTheDocumentVerbatim pins that --raw is exactly the
// bytes the environment served, and that it goes to a file when the caller asks
// for one rather than flooding stdout.
func TestDescribeAPIRawWritesTheDocumentVerbatim(t *testing.T) {
	newMockEnvironment(t)
	dest := filepath.Join(t.TempDir(), "widget.yaml")

	code, out := captureRun(t,
		[]string{"describe", "api", "Widget Service", "--raw", "--spill-to", dest}, RunOptions{})
	require.Zero(t, code, out)

	written, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, mockWidgetSpec, string(written), "--raw must not reformat the document")
}

// TestDescribeAPIRawToStdoutIsTheHumanDefault pins that a human gets the document
// on stdout, where redirection works, rather than a path to a cache file.
func TestDescribeAPIRawToStdoutIsTheHumanDefault(t *testing.T) {
	newMockEnvironment(t)

	code, out := captureRun(t, []string{"describe", "api", "Widget Service", "--raw"}, RunOptions{})
	require.Zero(t, code, out)
	require.Contains(t, out, "openapi: 3.0.3")
	require.Contains(t, out, "x-api-gateway-url: /platform/widget/v1")
}

// TestDescribeAPIRawToStdoutIsByteExact is the stdout half of the verbatim
// contract. mockWidgetSpec ends in a newline, so neither test above could notice
// that the stdout path appended one — and the classic environment API, the one
// API that serves JSON here, commonly serves it without a trailing newline. A
// document redirected to a file is what --raw is documented for, so a gained
// byte means it no longer matches what the environment served.
func TestDescribeAPIRawToStdoutIsByteExact(t *testing.T) {
	const spec = `{"openapi":"3.0.3","info":{"title":"Widget","version":"2.4.1"},"paths":{}}`
	env := newMockEnvironment(t)
	env.respond(http.MethodGet, "/platform/widget/v1/openapi.yaml", mockResponse{
		status:      200,
		contentType: "application/json",
		body:        spec,
	})

	code, out := captureRun(t, []string{"describe", "api", "Widget Service", "--raw"}, RunOptions{})
	require.Zero(t, code, out)
	require.Equal(t, spec, out,
		"a redirected document must be byte-exact: %d bytes served, %d out", len(spec), len(out))
}

// TestAPIIndexUnavailableNamesTheFallback pins the fail-soft contract for an
// environment with no machine-readable index: a stable error code, no forwarded
// HTTP body, and the documented explorer as the way forward.
func TestAPIIndexUnavailableNamesTheFallback(t *testing.T) {
	env := newMockEnvironment(t)
	env.specStatus["/platform/metadata/v1/swagger-ui.json"] = http.StatusNotFound

	code, env2 := runAgentEnvelope(t, []string{"get", "apis"})
	require.NotZero(t, code)
	require.Equal(t, "api_index_unavailable", errorCode(t, env2))

	detail, _ := env2["error"].(map[string]any)
	require.NotNil(t, detail)
	message, _ := detail["message"].(string)
	require.Contains(t, message, "/platform/metadata/v1/swagger-ui.json")
	require.NotContains(t, message, "refused", "the upstream body is not forwarded")

	suggestions := fmt.Sprint(detail["suggestions"])
	require.Contains(t, suggestions, "/platform/swagger-ui/index.html",
		"the documented explorer is the fallback to name")
}

// TestAPIIndexRefusedIsNotAnAbsentIndex pins the distinction the 404 message must
// not swallow.
//
// "This environment publishes no machine-readable API index" is a claim about the
// environment. A 401 is evidence about the credential, and answering it with that
// claim sends someone to investigate the wrong layer — while a machine consumer
// reading api_index_unavailable would stop probing an index that is in fact there.
func TestAPIIndexRefusedIsNotAnAbsentIndex(t *testing.T) {
	env := newMockEnvironment(t)
	env.specStatus["/platform/metadata/v1/swagger-ui.json"] = http.StatusUnauthorized

	code, envelope := runAgentEnvelope(t, []string{"get", "apis"})
	require.NotZero(t, code)
	require.NotEqual(t, "api_index_unavailable", errorCode(t, envelope),
		"a refused index must not be reported as an unpublished one")

	detail, _ := envelope["error"].(map[string]any)
	require.NotNil(t, detail)
	message, _ := detail["message"].(string)
	require.Contains(t, message, "not authorized")
	require.NotContains(t, message, "publishes no",
		"the message must not make a claim about the environment from evidence about the token")

	suggestions := fmt.Sprint(detail["suggestions"])
	require.Contains(t, suggestions, "dtctl doctor",
		"the hint must point at the credential, not at the API explorer")
}

// TestSpecUnavailableExplainsAValidTokenBeingRefused pins the 403 case, which is
// the one an environment can produce with a perfectly good token: the raw body
// talks about SSO and helps nobody, so the status is translated into advice.
func TestSpecUnavailableExplainsAValidTokenBeingRefused(t *testing.T) {
	env := newMockEnvironment(t)
	env.specStatus["/platform/widget/v1/openapi.yaml"] = http.StatusForbidden

	code, envelope := runAgentEnvelope(t, []string{"describe", "api", "Widget Service"})
	require.NotZero(t, code)
	require.Equal(t, "api_spec_unavailable", errorCode(t, envelope))

	detail, _ := envelope["error"].(map[string]any)
	require.NotNil(t, detail)
	suggestions := fmt.Sprint(detail["suggestions"])
	require.Contains(t, strings.ToLower(suggestions), "interactive session")
	require.Contains(t, suggestions, "exec api",
		"the API itself is still reachable without its specification")
}
