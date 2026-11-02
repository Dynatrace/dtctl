// Package apispec discovers and projects the OpenAPI documents a Dynatrace
// environment publishes about itself.
//
// Two conventions carry this package, and neither is a documented contract:
//
//   - every platform API serves its specification at
//     `<api-base-path>/openapi.yaml`, and
//   - the environment publishes a machine-readable index of those documents at
//     [RegistryPath] — the same `configUrl` the environment's own Swagger UI
//     consumes.
//
// They are stable, uniform, observed conventions. Because they are not
// guaranteed, every entry point here fails soft: a missing index is a typed
// [*RegistryUnavailableError] rather than a raw 404, and one unreadable document
// never invalidates the rest of a listing.
//
// Raw specifications are far too large to hand to a caller verbatim — the
// largest observed is ~47k tokens for a single API — so the useful output is a
// projection: an operation index for choosing an operation, and one fully
// resolved operation for composing a call.
package apispec

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// RegistryPath is the environment's machine-readable index of published API
// specifications.
const RegistryPath = "/platform/metadata/v1/swagger-ui.json"

// SpecFileName is the conventional specification filename under an API base path.
const SpecFileName = "openapi.yaml"

// ClassicSpecFileName is the classic environment API's specification filename.
// The `openapi.yaml` convention does not hold for the classic surface — it is a
// 404 there — so classic entries are recognised by this name instead.
const ClassicSpecFileName = "spec3.json"

// Handler fetches and projects the specifications one environment publishes.
//
// Parsed documents are memoized for the Handler's lifetime, which makes a
// drill-down (index, then one operation) cost a single fetch per API. The cache
// is deliberately in-memory only: a spec is tenant- and version-specific, and a
// cross-invocation disk cache would be host state outliving the request.
type Handler struct {
	client *httpclient.Client

	mu       sync.Mutex
	registry *Registry
	specs    map[string]*Spec // keyed by document path
}

// NewHandler creates a specification handler bound to one environment.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c, specs: make(map[string]*Spec)}
}

// Entry is one API specification the environment's registry publishes.
type Entry struct {
	// Name is the registry's own display name, e.g. "Document Service".
	Name string `json:"name" yaml:"name"`
	// URL is the location of the specification document, usually a path relative
	// to the environment (e.g. "/platform/document/v1/openapi.yaml") but
	// occasionally absolute for documents hosted elsewhere.
	URL string `json:"url" yaml:"url"`
}

// External reports whether the entry points at an absolute URL rather than a
// path on this environment. Such entries (classic UI links) are listed but never
// fetched: they are not reachable through the platform gateway with a platform
// token.
func (e Entry) External() bool {
	return strings.Contains(e.URL, "://")
}

// Classic reports whether the entry is a classic environment-API document, which
// uses spec3.json rather than the openapi.yaml convention.
func (e Entry) Classic() bool {
	return strings.HasSuffix(e.docPath(), "/"+ClassicSpecFileName)
}

// docPath is the entry URL with any query string or fragment removed. A registry
// URL may carry a cache-busting query (`?hash=…`); it is preserved for fetching
// but must never take part in path derivation — nor, in any caller, be used to
// build a filename.
func (e Entry) docPath() string {
	path := e.URL
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	return path
}

// BasePath is the API base path the entry documents — the specification path
// with its filename removed, e.g. "/platform/document/v1". It is empty for
// external entries, whose base path is not on this environment.
func (e Entry) BasePath() string {
	if e.External() {
		return ""
	}
	path := e.docPath()
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return ""
	}
	return path[:i]
}

// Registry is the environment's index of published specifications, in the order
// the environment returned them.
type Registry struct {
	Entries []Entry
}

// registryDocument is the on-the-wire shape of [RegistryPath].
type registryDocument struct {
	URLs []Entry `json:"urls" yaml:"urls"`
}

// RegistryUnavailableError reports that the environment's API index could not be
// read. It is a distinct type because it is an expected outcome, not a bug: the
// index is an observed convention, so a caller should explain it and name the
// Swagger UI as the fallback — never surface a raw response body.
type RegistryUnavailableError struct {
	Path string
	// StatusCode is the HTTP status, or 0 when the request never completed or
	// failed after a successful response (a body that would not parse).
	StatusCode int
	Err        error
}

// Error distinguishes "not published" from "refused", because they call for
// opposite responses from the caller.
//
// Reporting a 401 as "this environment publishes no API index" is a statement
// about the environment made from evidence about the credential, and it sends
// someone to check the wrong thing entirely. Only a 404 licenses that claim.
func (e *RegistryUnavailableError) Error() string {
	switch {
	case e.StatusCode == 401 || e.StatusCode == 403:
		return fmt.Sprintf("not authorized to read the API index at %s (HTTP %d)",
			e.Path, e.StatusCode)
	case e.StatusCode >= 500:
		return fmt.Sprintf("the API index at %s is temporarily unavailable (HTTP %d)",
			e.Path, e.StatusCode)
	case e.StatusCode != 0 && e.StatusCode != 404:
		return fmt.Sprintf("the API index at %s could not be read (HTTP %d)",
			e.Path, e.StatusCode)
	default:
		return fmt.Sprintf("this environment publishes no machine-readable API index at %s", e.Path)
	}
}

func (e *RegistryUnavailableError) Unwrap() error { return e.Err }

// Registry fetches the environment's specification index. It is one HTTP
// request, and the result is memoized.
func (h *Handler) Registry() (*Registry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.registry != nil {
		return h.registry, nil
	}

	resp, err := h.client.HTTP().R().Get(RegistryPath)
	if err != nil {
		return nil, &RegistryUnavailableError{Path: RegistryPath, Err: err}
	}
	if apiErr := httpclient.CheckResponse(resp); apiErr != nil {
		return nil, &RegistryUnavailableError{
			Path:       RegistryPath,
			StatusCode: resp.StatusCode(),
			Err:        apiErr,
		}
	}

	// Parsed as YAML even though the endpoint serves JSON: YAML is a superset,
	// and the one rule that has held across every specification endpoint is
	// "parse as YAML, never branch on Content-Type" (the same artifact ships as
	// four different media types).
	var doc registryDocument
	if err := yaml.Unmarshal(resp.Body(), &doc); err != nil {
		return nil, &RegistryUnavailableError{Path: RegistryPath, Err: err}
	}

	reg := &Registry{}
	for _, e := range doc.URLs {
		if e.URL == "" {
			continue
		}
		reg.Entries = append(reg.Entries, e)
	}
	if len(reg.Entries) == 0 {
		return nil, &RegistryUnavailableError{
			Path: RegistryPath,
			Err:  fmt.Errorf("index contains no specification entries"),
		}
	}

	h.registry = reg
	return reg, nil
}

// Spec is the projection of one OpenAPI document: its identity, its base path,
// and its operations. The original bytes are retained so a caller that explicitly
// asks for the raw document does not have to fetch it twice.
type Spec struct {
	// Title, Version, Category and Summary come from `info` — Category and
	// Summary from the `x-service-category` and `x-summary` extensions, which
	// make an API's grouping machine-readable.
	Title    string
	Version  string
	Category string
	Summary  string
	// BasePath is the API's gateway-relative base path, taken from
	// `servers[0].x-api-gateway-url` where present.
	BasePath string
	// Operations are in document order.
	Operations []Operation
	// Raw is the document exactly as served.
	Raw []byte

	doc map[string]any
}

// Operation is one path+method pair, projected to what a caller needs to choose
// it (identity, summary, scope) and to compose it (parameters, bodies).
type Operation struct {
	Method      string `json:"method" yaml:"method"`
	Path        string `json:"path" yaml:"path"`
	OperationID string `json:"operation_id,omitempty" yaml:"operation_id,omitempty"`
	Summary     string `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Deprecated  bool   `json:"deprecated,omitempty" yaml:"deprecated,omitempty"`
	// Scopes are the scopes the document declares for this operation. Empty means
	// the document declares none — which is common and must not be read as "no
	// scope required": two of six APIs sampled declare no per-operation scopes at
	// all, and one declares an explicitly empty list.
	Scopes      []string     `json:"scopes,omitempty" yaml:"scopes,omitempty"`
	Parameters  []Parameter  `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	RequestBody *RequestBody `json:"request_body,omitempty" yaml:"request_body,omitempty"`
	Responses   []Response   `json:"responses,omitempty" yaml:"responses,omitempty"`
}

// ID is the operation's stable human-facing identity, "METHOD /path". It is what
// `describe api --operation` accepts and what an operation index lists.
func (o Operation) ID() string { return o.Method + " " + o.Path }

// Parameter is one path, query, header or cookie parameter.
type Parameter struct {
	Name        string `json:"name" yaml:"name"`
	In          string `json:"in" yaml:"in"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Type        string `json:"type,omitempty" yaml:"type,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Schema is the parameter's schema with a top-level $ref resolved, present
	// when it carries more than a scalar type.
	Schema any `json:"schema,omitempty" yaml:"schema,omitempty"`
}

// RequestBody is an operation's request body across its media types.
type RequestBody struct {
	Required bool        `json:"required,omitempty" yaml:"required,omitempty"`
	Contents []MediaType `json:"contents,omitempty" yaml:"contents,omitempty"`
}

// Response is one documented response status.
type Response struct {
	Status      string      `json:"status" yaml:"status"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
	Contents    []MediaType `json:"contents,omitempty" yaml:"contents,omitempty"`
}

// MediaType pairs a content type with its schema. The schema has its top-level
// $ref resolved — one hop, which is what makes it usable without the rest of the
// document, and is where resolution deliberately stops.
type MediaType struct {
	ContentType string `json:"content_type" yaml:"content_type"`
	Schema      any    `json:"schema,omitempty" yaml:"schema,omitempty"`
}

// SpecPathForBase returns the conventional specification path for an API base
// path.
func SpecPathForBase(basePath string) string {
	return strings.TrimSuffix(basePath, "/") + "/" + SpecFileName
}

// Spec fetches and parses the specification document at docPath, a path relative
// to the environment. Results are memoized per Handler.
func (h *Handler) Spec(docPath string) (*Spec, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if s, ok := h.specs[docPath]; ok {
		return s, nil
	}

	body, err := h.fetch(docPath)
	if err != nil {
		return nil, err
	}

	spec, err := ParseSpec(body)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", docPath, err)
	}
	if spec.BasePath == "" {
		// Fall back to the document's own location, which is the base path plus
		// the specification filename.
		if i := strings.LastIndex(docPath, "/"); i > 0 {
			spec.BasePath = docPath[:i]
		}
	}

	h.specs[docPath] = spec
	return spec, nil
}

// SpecUnavailableError reports that a document the index listed could not be
// fetched.
//
// It is typed, and carries the status, because "the index lists it but you
// cannot read it" is an ordinary property of an environment rather than a bug: a
// listed document may 404, and an environment may serve its specifications only
// to an interactive session, in which case an authenticated token gets a 403
// whose body talks about SSO and explains nothing. The status is what lets a
// caller say something useful instead of forwarding that.
type SpecUnavailableError struct {
	DocPath string
	// StatusCode is the HTTP status, or 0 when the request never completed.
	StatusCode int
	Err        error
}

func (e *SpecUnavailableError) Error() string {
	return fmt.Sprintf("fetching %s: %v", e.DocPath, e.Err)
}

func (e *SpecUnavailableError) Unwrap() error { return e.Err }

// fetch retrieves a document. It takes no lock, so it is callable from a method
// that already holds one.
func (h *Handler) fetch(docPath string) ([]byte, error) {
	resp, err := h.client.HTTP().R().Get(docPath)
	if err != nil {
		return nil, &SpecUnavailableError{DocPath: docPath, Err: err}
	}
	if apiErr := httpclient.CheckResponse(resp); apiErr != nil {
		return nil, &SpecUnavailableError{DocPath: docPath, StatusCode: resp.StatusCode(), Err: apiErr}
	}
	return resp.Body(), nil
}

// SpecForEntry fetches the document a registry entry points at. External entries
// are refused rather than fetched: they live on another host and are not
// reachable through the platform gateway with a platform token.
func (h *Handler) SpecForEntry(e Entry) (*Spec, error) {
	if e.External() {
		return nil, errExternalEntry(e)
	}
	return h.Spec(e.URL)
}

// RawDocument returns a specification document exactly as served, without
// projecting it. It is deliberately independent of parsing: a document dtctl
// cannot parse is precisely the case where a caller wants the bytes. A document
// already fetched for its projection is served from memory rather than re-fetched.
func (h *Handler) RawDocument(docPath string) ([]byte, error) {
	h.mu.Lock()
	cached, ok := h.specs[docPath]
	h.mu.Unlock()
	if ok {
		return cached.Raw, nil
	}
	return h.fetch(docPath)
}

// RawDocumentForEntry returns the unparsed document a registry entry points at.
func (h *Handler) RawDocumentForEntry(e Entry) ([]byte, error) {
	if e.External() {
		return nil, errExternalEntry(e)
	}
	return h.RawDocument(e.URL)
}

func errExternalEntry(e Entry) error {
	return fmt.Errorf("%q is documented at an external URL (%s), which dtctl does not fetch", e.Name, e.URL)
}

// ParseSpec projects an OpenAPI document. The input is always parsed as YAML:
// JSON is a subset, so this handles the classic `spec3.json` documents and every
// observed Content-Type variant with one code path.
func ParseSpec(body []byte) (*Spec, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("not a parseable OpenAPI document: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("not a parseable OpenAPI document: empty")
	}
	if _, hasPaths := doc["paths"]; !hasPaths {
		return nil, fmt.Errorf("not an OpenAPI document: no `paths` section")
	}

	spec := &Spec{Raw: body, doc: doc}

	info := mapAt(doc, "info")
	spec.Title = strAt(info, "title")
	spec.Version = strAt(info, "version")
	spec.Category = strAt(info, "x-service-category")
	spec.Summary = strAt(info, "x-summary")

	for _, srv := range sliceAt(doc, "servers") {
		s, ok := srv.(map[string]any)
		if !ok {
			continue
		}
		// x-api-gateway-url is the relative base path through the platform
		// gateway; `url` may be absolute or templated.
		if gw := strAt(s, "x-api-gateway-url"); gw != "" {
			spec.BasePath = strings.TrimSuffix(gw, "/")
			break
		}
		if u := strAt(s, "url"); u != "" && spec.BasePath == "" {
			spec.BasePath = strings.TrimSuffix(u, "/")
		}
	}

	// Document-level security is the fallback when an operation declares none.
	docScopes := securityScopes(doc, doc)

	spec.Operations = collectOperations(doc, docScopes)
	return spec, nil
}

// httpMethods are the OpenAPI path-item keys that denote an operation. Anything
// else under a path (`parameters`, `summary`, `servers`, extensions) is not one.
var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// collectOperations walks `paths` and projects every operation, in document
// order for paths and in fixed method order within a path so output is stable.
func collectOperations(doc map[string]any, docScopes []string) []Operation {
	paths := mapAt(doc, "paths")
	if paths == nil {
		return nil
	}

	// yaml.Unmarshal into map[string]any loses document order, so paths are
	// sorted for determinism. (Order within a spec carries no meaning; stable
	// output does.)
	pathNames := sortedKeys(paths)

	var ops []Operation
	for _, p := range pathNames {
		item := mapAt(paths, p)
		if item == nil {
			continue
		}
		// A published specification may key an operation on the empty path, meaning
		// the base path itself is the endpoint. Left as-is it produces the operation
		// ID "GET " — which cannot be passed back to --operation — and it never
		// matches a request, because Match normalizes an incoming base path to "/".
		// Normalizing here makes the collection root an ordinary operation instead of
		// one that is listed but unreachable.
		if p == "" {
			p = "/"
		}
		// Path-level parameters apply to every operation under the path.
		shared := collectParameters(doc, sliceAt(item, "parameters"))

		for _, m := range httpMethods {
			opMap := mapAt(item, m)
			if opMap == nil {
				continue
			}
			op := Operation{
				Method:      strings.ToUpper(m),
				Path:        p,
				OperationID: strAt(opMap, "operationId"),
				Summary:     strAt(opMap, "summary"),
				Description: strAt(opMap, "description"),
				Deprecated:  boolAt(opMap, "deprecated"),
			}

			if _, declared := opMap["security"]; declared {
				op.Scopes = securityScopes(opMap, doc)
			} else {
				op.Scopes = docScopes
			}

			op.Parameters = append(append([]Parameter(nil), shared...),
				collectParameters(doc, sliceAt(opMap, "parameters"))...)
			op.RequestBody = collectRequestBody(doc, mapAt(opMap, "requestBody"))
			op.Responses = collectResponses(doc, mapAt(opMap, "responses"))

			ops = append(ops, op)
		}
	}
	return ops
}

// securityScopes extracts the scopes a `security` block declares. A block may
// list several schemes; every non-empty scope list contributes.
//
// An explicitly empty list (`ssoAuth: []`) yields no scopes, which is correct
// and load-bearing: it means the document declares nothing about this
// operation's scope, not that the operation needs none.
func securityScopes(holder, doc map[string]any) []string {
	var scopes []string
	seen := map[string]bool{}
	for _, req := range sliceAt(holder, "security") {
		entry, ok := deref(doc, req).(map[string]any)
		if !ok {
			continue
		}
		for _, raw := range entry {
			for _, s := range toStrings(raw) {
				if s == "" || seen[s] {
					continue
				}
				seen[s] = true
				scopes = append(scopes, s)
			}
		}
	}
	return scopes
}

// collectParameters projects a `parameters` list, resolving each entry's $ref one
// hop.
func collectParameters(doc map[string]any, raw []any) []Parameter {
	var params []Parameter
	for _, entry := range raw {
		p, ok := deref(doc, entry).(map[string]any)
		if !ok {
			continue
		}
		schema := deref(doc, p["schema"])
		param := Parameter{
			Name:        strAt(p, "name"),
			In:          strAt(p, "in"),
			Required:    boolAt(p, "required"),
			Description: strAt(p, "description"),
			Type:        schemaType(schema),
		}
		// A bare scalar type is already carried by Type; keep the schema only
		// when it says more (enums, objects, item types), so a parameter list
		// stays compact for the common case.
		if m, ok := schema.(map[string]any); ok && len(m) > 1 {
			param.Schema = schema
		}
		if param.Name != "" {
			params = append(params, param)
		}
	}
	return params
}

// collectRequestBody projects a `requestBody`, resolving the body's $ref and each
// media type's schema $ref one hop.
func collectRequestBody(doc map[string]any, raw map[string]any) *RequestBody {
	if raw == nil {
		return nil
	}
	body, ok := deref(doc, raw).(map[string]any)
	if !ok {
		return nil
	}
	rb := &RequestBody{Required: boolAt(body, "required")}
	rb.Contents = collectContents(doc, mapAt(body, "content"))
	if len(rb.Contents) == 0 && !rb.Required {
		return nil
	}
	return rb
}

// collectResponses projects a `responses` map in sorted status order.
func collectResponses(doc map[string]any, raw map[string]any) []Response {
	if raw == nil {
		return nil
	}
	var out []Response
	for _, status := range sortedKeys(raw) {
		r, ok := deref(doc, raw[status]).(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Response{
			Status:      status,
			Description: strAt(r, "description"),
			Contents:    collectContents(doc, mapAt(r, "content")),
		})
	}
	return out
}

// collectContents projects a `content` map to media types with one-hop resolved
// schemas.
func collectContents(doc map[string]any, content map[string]any) []MediaType {
	if content == nil {
		return nil
	}
	var out []MediaType
	for _, ct := range sortedKeys(content) {
		mt, ok := content[ct].(map[string]any)
		if !ok {
			out = append(out, MediaType{ContentType: ct})
			continue
		}
		out = append(out, MediaType{
			ContentType: ct,
			Schema:      deref(doc, mt["schema"]),
		})
	}
	return out
}

// schemaType renders a schema's type compactly: "string", "array[string]",
// "object", or "" when the schema says nothing useful.
func schemaType(schema any) string {
	m, ok := schema.(map[string]any)
	if !ok {
		return ""
	}
	t := strAt(m, "type")
	if t == "array" {
		if inner := schemaType(deref(m, m["items"])); inner != "" {
			return "array[" + inner + "]"
		}
		return "array"
	}
	if t == "" {
		// A composed schema has no `type`; name the composition so the caller
		// knows to look at the schema rather than seeing a blank column.
		for _, key := range []string{"oneOf", "anyOf", "allOf"} {
			if _, has := m[key]; has {
				return key
			}
		}
	}
	if f := strAt(m, "format"); f != "" && t != "" {
		return t + "(" + f + ")"
	}
	return t
}

// deref resolves a local `$ref` one hop and returns the target. A non-ref value,
// an external ref, and an unresolvable ref are all returned unchanged — the
// caller gets the ref back rather than a nil hole.
//
// Resolution stops after one hop on purpose: it is what makes a single operation
// self-contained without pulling in the rest of the document, which is the whole
// reason a projection is affordable.
func deref(doc map[string]any, node any) any {
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}
	ref, ok := m["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/") {
		return node
	}
	target := resolvePointer(doc, ref)
	if target == nil {
		return node
	}
	return target
}

// resolvePointer walks a local JSON pointer ("#/components/schemas/Foo") through
// the document.
func resolvePointer(doc map[string]any, ref string) any {
	var current any = doc
	for _, token := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		// JSON-pointer escaping: ~1 is "/", ~0 is "~". Unescaped in that order.
		token = strings.ReplaceAll(token, "~1", "/")
		token = strings.ReplaceAll(token, "~0", "~")

		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = m[token]
		if !ok {
			return nil
		}
	}
	return current
}

// Find returns the operation with the given method and exact template path.
func (s *Spec) Find(method, templatePath string) (*Operation, bool) {
	method = strings.ToUpper(method)
	for i := range s.Operations {
		if s.Operations[i].Method == method && s.Operations[i].Path == templatePath {
			return &s.Operations[i], true
		}
	}
	return nil, false
}

// FindByID returns the operation whose [Operation.ID] matches id ("METHOD /path"),
// case-insensitively on the method. A bare path with no method matches when
// exactly one operation has that path.
func (s *Spec) FindByID(id string) (*Operation, bool) {
	id = strings.TrimSpace(id)
	if method, path, found := strings.Cut(id, " "); found {
		return s.Find(method, strings.TrimSpace(path))
	}

	var match *Operation
	for i := range s.Operations {
		if s.Operations[i].Path != id {
			continue
		}
		if match != nil {
			return nil, false // ambiguous without a method
		}
		match = &s.Operations[i]
	}
	return match, match != nil
}

// Match resolves a concrete request path to the operation that serves it. The
// request path may be absolute (including the API base path) or already relative
// to it.
//
// When several templates match, the one with the fewest placeholders wins, so a
// literal path beats a templated one (`/documents/search` over
// `/documents/{id}`).
func (s *Spec) Match(method, requestPath string) (*Operation, bool) {
	method = strings.ToUpper(method)

	relative := requestPath
	if s.BasePath != "" && strings.HasPrefix(requestPath, s.BasePath) {
		relative = strings.TrimPrefix(requestPath, s.BasePath)
	}
	if !strings.HasPrefix(relative, "/") {
		relative = "/" + relative
	}
	relative = strings.TrimSuffix(relative, "/")
	if relative == "" {
		relative = "/"
	}

	var best *Operation
	bestPlaceholders := -1
	for i := range s.Operations {
		op := &s.Operations[i]
		if op.Method != method {
			continue
		}
		if !pathTemplateMatches(op.Path, relative) {
			continue
		}
		n := strings.Count(op.Path, "{")
		if bestPlaceholders < 0 || n < bestPlaceholders {
			best, bestPlaceholders = op, n
		}
	}
	return best, best != nil
}

// placeholderPattern matches an OpenAPI path placeholder.
var placeholderPattern = regexp.MustCompile(`\{[^{}]*\}`)

// templateCache memoizes compiled path-template regexps. Spec matching happens
// per request in `exec api`, and a spec can carry 70+ templates.
var (
	templateCacheMu sync.Mutex
	templateCache   = map[string]*regexp.Regexp{}
)

// pathTemplateMatches reports whether a concrete path is an instance of an
// OpenAPI path template.
//
// Placeholders are matched within a segment rather than as whole segments,
// because Dynatrace paths put custom actions in the same segment as an
// identifier (`/documents/{id}:favorite`). A placeholder never spans a `/`.
func pathTemplateMatches(template, path string) bool {
	re, err := compileTemplate(template)
	if err != nil {
		return template == path
	}
	return re.MatchString(path)
}

func compileTemplate(template string) (*regexp.Regexp, error) {
	templateCacheMu.Lock()
	if re, ok := templateCache[template]; ok {
		templateCacheMu.Unlock()
		return re, nil
	}
	templateCacheMu.Unlock()

	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, loc := range placeholderPattern.FindAllStringIndex(template, -1) {
		b.WriteString(regexp.QuoteMeta(template[last:loc[0]]))
		// A path parameter is one or more characters within a single segment.
		b.WriteString(`[^/]+`)
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(template[last:]))
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, err
	}

	templateCacheMu.Lock()
	templateCache[template] = re
	templateCacheMu.Unlock()
	return re, nil
}

// FindEntry resolves a name or base path to a registry entry, matching only
// against what the registry actually returned.
//
// Resolution order: exact base path, then exact (case-insensitive) name, then a
// case-insensitive substring of the name or base path. A miss returns
// (nil, false) — callers must never synthesize a candidate path from a name,
// which would turn dtctl into a prober that confirms the existence of APIs an
// environment deliberately omits from its own index.
func (r *Registry) FindEntry(query string) (*Entry, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, false
	}
	lower := strings.ToLower(q)
	normalized := "/" + strings.Trim(q, "/")

	for i := range r.Entries {
		if r.Entries[i].BasePath() == normalized {
			return &r.Entries[i], true
		}
	}
	for i := range r.Entries {
		if strings.EqualFold(r.Entries[i].Name, q) {
			return &r.Entries[i], true
		}
	}

	var matches []*Entry
	for i := range r.Entries {
		e := &r.Entries[i]
		if strings.Contains(strings.ToLower(e.Name), lower) ||
			strings.Contains(strings.ToLower(e.BasePath()), lower) {
			matches = append(matches, e)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return nil, false
}

// Candidates returns every entry whose name or base path contains query,
// case-insensitively. It backs a caller's disambiguation message and, like
// [Registry.FindEntry], never looks beyond what the registry returned.
func (r *Registry) Candidates(query string) []Entry {
	lower := strings.ToLower(strings.TrimSpace(query))
	var out []Entry
	for _, e := range r.Entries {
		if lower == "" ||
			strings.Contains(strings.ToLower(e.Name), lower) ||
			strings.Contains(strings.ToLower(e.BasePath()), lower) {
			out = append(out, e)
		}
	}
	return out
}

// EntryForPath returns the registry entry whose base path is the longest prefix
// of requestPath — the API that owns a given request. The longest prefix wins so
// that a nested base path is preferred over the shorter one it sits under.
func (r *Registry) EntryForPath(requestPath string) (*Entry, bool) {
	var best *Entry
	for i := range r.Entries {
		base := r.Entries[i].BasePath()
		if base == "" || !strings.HasPrefix(requestPath, base) {
			continue
		}
		// Require a segment boundary so /platform/storage/query/v1 does not
		// claim /platform/storage/query/v1beta.
		rest := requestPath[len(base):]
		if rest != "" && !strings.HasPrefix(rest, "/") {
			continue
		}
		if best == nil || len(base) > len(best.BasePath()) {
			best = &r.Entries[i]
		}
	}
	return best, best != nil
}

// IsAbsoluteURL reports whether s is an absolute URL rather than a path on the
// environment. Callers use it to reject a passthrough target that would leave
// the configured environment.
func IsAbsoluteURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme != ""
}
