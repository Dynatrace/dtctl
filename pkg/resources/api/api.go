// Package api is the CLI layer over the platform's own OpenAPI documents. It
// backs `dtctl get apis`, `dtctl describe api`, and the request classification
// `dtctl exec api` gates on.
//
// It delegates all HTTP and parsing to sdk/api/apispec and adds the parts that
// are dtctl's rather than the platform's: which APIs already have a native
// command, how a specification-declared scope maps onto a safety operation, and
// what the projections look like on screen.
package api

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/sdk/api/apispec"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Re-exported so callers need not import the SDK package alongside this one.
type (
	// Entry is one API the environment's registry publishes.
	Entry = apispec.Entry
	// Registry is the environment's index of published specifications.
	Registry = apispec.Registry
	// Spec is a parsed, projected OpenAPI document.
	Spec = apispec.Spec
	// Operation is one path+method pair from a specification.
	Operation = apispec.Operation
	// Parameter is one path, query, header or cookie parameter.
	Parameter = apispec.Parameter
	// RequestBody is an operation's request body across its media types.
	RequestBody = apispec.RequestBody
	// Response is one documented response status.
	Response = apispec.Response
	// MediaType pairs a content type with its schema.
	MediaType = apispec.MediaType
	// RegistryUnavailableError reports an environment with no usable API index.
	RegistryUnavailableError = apispec.RegistryUnavailableError
	// SpecUnavailableError reports a listed specification that could not be read.
	SpecUnavailableError = apispec.SpecUnavailableError
)

// SwaggerUIPath is the human-facing API explorer every environment serves. It is
// the documented entry point, and therefore the fallback to name whenever the
// machine-readable index is unavailable.
const SwaggerUIPath = "/platform/swagger-ui/index.html"

// Handler resolves and projects the specifications one environment publishes.
type Handler struct {
	spec *apispec.Handler
}

// NewHandler creates an API-specification handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{spec: apispec.NewHandler(httpclient.Wrap(c.HTTP()))}
}

// Registry returns the environment's specification index.
func (h *Handler) Registry() (*Registry, error) { return h.spec.Registry() }

// Spec returns the parsed specification for a registry entry.
func (h *Handler) Spec(e Entry) (*Spec, error) { return h.spec.SpecForEntry(e) }

// RawSpec returns the specification document exactly as the environment served
// it, for `describe api --raw`.
func (h *Handler) RawSpec(e Entry) ([]byte, error) { return h.spec.RawDocumentForEntry(e) }

// QuoteArg single-quotes a value that a shell would otherwise split, so a
// suggested command can be pasted and run. API names contain spaces, and a
// suggestion the caller has to repair is a suggestion that gets retyped wrong.
func QuoteArg(s string) string {
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// IsAbsoluteURL reports whether a target is an absolute URL rather than a path on
// the current environment. `exec api` refuses the former: the context's
// credentials belong to the environment it names.
func IsAbsoluteURL(s string) bool { return apispec.IsAbsoluteURL(s) }

// RawSpecEncoding reports the on-disk encoding of a raw specification document,
// derived from the document's own path rather than sniffed from its bytes: the
// same artifact is served under four different content types, so the filename is
// the only reliable signal.
func RawSpecEncoding(e Entry) string {
	if strings.HasSuffix(e.URL, ".json") {
		return "json"
	}
	return "yaml"
}

// APIInfo is one row of `dtctl get apis`.
//
// The DTCTL column is the coverage report: it names the native resource that
// wraps the API, or is blank. That makes the gap between what the platform
// publishes and what dtctl wraps visible and, via --uncovered, actionable.
type APIInfo struct {
	Name     string `json:"name" yaml:"name" table:"NAME"`
	BasePath string `json:"base_path" yaml:"base_path" table:"BASE PATH"`
	// Category comes from the specification, so it is known only when the
	// specification was fetched (--ops-count). It is a wide column rather than a
	// default one for exactly that reason.
	Category string `json:"category,omitempty" yaml:"category,omitempty" table:"CATEGORY,wide"`
	// Operations is the operation count, populated only when specifications were
	// fetched (--ops-count). A nil pointer means "not counted", which renders as
	// a blank cell rather than a misleading 0.
	Operations *int `json:"operations,omitempty" yaml:"operations,omitempty" table:"OPS"`
	// Dtctl names the native dtctl resource covering this API, or is empty.
	Dtctl string `json:"dtctl,omitempty" yaml:"dtctl,omitempty" table:"DTCTL"`
	// External marks an entry documented at an absolute URL on another host.
	// dtctl lists it (the environment published it) but cannot fetch it, and it
	// has no gateway base path — which is why the column is worth showing.
	External bool `json:"external,omitempty" yaml:"external,omitempty" table:"EXTERNAL,wide"`
	// SpecError records why this row could not be enriched, when a specification
	// fetch failed. One bad document degrades its own row and never fails the
	// listing.
	SpecError string `json:"spec_error,omitempty" yaml:"spec_error,omitempty" table:"-"`
}

// ListOptions shapes `dtctl get apis`.
type ListOptions struct {
	// Uncovered filters to APIs with no native dtctl command.
	Uncovered bool
	// OpsCount fetches every specification to fill in the operation count. It
	// fans out one request per API, so the default listing leaves it off and
	// costs exactly one request.
	OpsCount bool
}

// List projects the environment's registry into displayable rows.
//
// dtctl mirrors the registry: it adds nothing and filters nothing. Whatever this
// returns, the environment's own Swagger UI already shows, because both read the
// same index. Any client-side filtering would have to hard-code knowledge of
// which APIs to conceal — and since dtctl is open source, that list would itself
// be the disclosure. Deciding what to publish belongs to whoever configures the
// environment.
func (h *Handler) List(opts ListOptions) ([]APIInfo, error) {
	reg, err := h.Registry()
	if err != nil {
		return nil, err
	}

	rows := make([]APIInfo, 0, len(reg.Entries))
	for _, e := range reg.Entries {
		info := APIInfo{
			Name:     e.Name,
			BasePath: e.BasePath(),
			External: e.External(),
			Dtctl:    NativeResourceFor(e.BasePath()),
		}

		if opts.OpsCount && !e.External() {
			spec, specErr := h.Spec(e)
			if specErr != nil {
				// Fail soft: the index listed a document that could not be read.
				// The row loses its detail; the listing still renders.
				info.SpecError = specErr.Error()
			} else {
				count := len(spec.Operations)
				info.Operations = &count
				if spec.Category != "" {
					info.Category = spec.Category
				}
			}
		}

		if opts.Uncovered {
			if info.Dtctl != "" {
				continue
			}
			// An entry outside the public /platform/ tree is not a contribution
			// candidate: the backlog feeds the curation rule, which requires a
			// state-of-the-art, publicly supported API. Such entries still appear
			// in the plain listing — the environment published them — but
			// proposing native commands for them would be wrong.
			if !isPublicPlatformPath(info.BasePath) {
				continue
			}
		}

		rows = append(rows, info)
	}

	return rows, nil
}

// isPublicPlatformPath reports whether a base path is on the public platform API
// tree. Anything else (a non-platform prefix, or an external URL with no base
// path at all) is out of scope for the contribution backlog.
func isPublicPlatformPath(basePath string) bool {
	return strings.HasPrefix(basePath, "/platform/")
}

// Resolve maps a user-supplied name or base path to a registry entry.
//
// Resolution consults only what the registry returned. A miss is a miss: dtctl
// never synthesizes a candidate path from a name and retries it, which would
// turn a name lookup into an existence oracle for APIs an environment
// deliberately omits from its own index.
//
// An explicit, fully-qualified base path is accepted as given — the caller
// already knows it, so echoing it back discloses nothing.
func (h *Handler) Resolve(query string) (*Entry, error) {
	reg, err := h.Registry()
	if err != nil {
		return nil, err
	}

	if e, ok := reg.FindEntry(query); ok {
		return e, nil
	}

	// An explicit base path the registry does not list is still addressable: the
	// caller supplied it, and the conventional specification path under it either
	// resolves or 404s on its own merits.
	if strings.HasPrefix(query, "/") {
		return &Entry{Name: query, URL: apispec.SpecPathForBase(query)}, nil
	}

	candidates := reg.Candidates(query)
	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf(
			"no API named %q in this environment's API index; run 'dtctl get apis' to list them, "+
				"or pass an explicit base path (e.g. /platform/example/v1)", query)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "ambiguous API name %q — %d matches:\n", query, len(candidates))
		for _, c := range candidates {
			fmt.Fprintf(&b, "  %s (%s)\n", c.Name, c.BasePath())
		}
		b.WriteString("\nUse the exact name or the base path.")
		return nil, fmt.Errorf("%s", b.String())
	}
}

// ResolveForRequest identifies the API and the operation a concrete request path
// belongs to.
//
// Every failure is soft and reported as "not resolved": the environment may
// publish no index, the path may belong to no listed API, the document may be
// unreadable, and the specification may declare nothing for that path+method.
// All four are ordinary, and none of them may stop a request — the classifier
// gates an unresolved request strictly instead (see [Classify]).
func (h *Handler) ResolveForRequest(method, requestPath string) (apiName string, op *Operation) {
	// The classification is about the endpoint, not the query.
	if i := strings.IndexAny(requestPath, "?#"); i >= 0 {
		requestPath = requestPath[:i]
	}
	// Resolution models the router, and the router decodes before matching: a
	// path spelled with %3A must resolve to the same operation as one spelled
	// with ':'. Structure-changing spellings never reach a request — the
	// command refuses them (see CanonicalRequestPath) — and if one arrives
	// here anyway, decoding at worst makes this lookup miss, which the
	// classifier gates closed.
	if decoded, err := url.PathUnescape(requestPath); err == nil {
		requestPath = decoded
	}

	reg, err := h.Registry()
	if err != nil {
		return "", nil
	}
	entry, ok := reg.EntryForPath(requestPath)
	if !ok {
		return "", nil
	}

	spec, err := h.Spec(*entry)
	if err != nil {
		return entry.Name, nil
	}
	if matched, ok := spec.Match(method, requestPath); ok {
		return entry.Name, matched
	}
	return entry.Name, nil
}

// OperationSummary is one line of an operation index — the compact projection
// `dtctl describe api <name>` emits.
//
// A raw specification runs from 10k to 47k tokens, which is unusable for an
// agent and unreadable for a human. Projecting to this shape costs roughly 300
// tokens for a 38-operation API. It is a complete view at a coarser grain, not a
// truncation: every operation appears, and the full detail of any one of them is
// one drill-down away.
type OperationSummary struct {
	Operation string `json:"operation" yaml:"operation" table:"OPERATION"`
	Summary   string `json:"summary,omitempty" yaml:"summary,omitempty" table:"SUMMARY"`
	// Scope is the specification-declared scope, or empty when the document
	// declares none. Empty is meaningful and common: it is why `exec api` cannot
	// treat a declared scope as guaranteed.
	Scope      string `json:"scope,omitempty" yaml:"scope,omitempty" table:"SCOPE"`
	Deprecated bool   `json:"deprecated,omitempty" yaml:"deprecated,omitempty" table:"DEPRECATED,wide"`
}

// APIDescription is the operation index for one API.
type APIDescription struct {
	Name       string             `json:"name" yaml:"name"`
	Title      string             `json:"title,omitempty" yaml:"title,omitempty"`
	Version    string             `json:"version,omitempty" yaml:"version,omitempty"`
	Category   string             `json:"category,omitempty" yaml:"category,omitempty"`
	Summary    string             `json:"summary,omitempty" yaml:"summary,omitempty"`
	BasePath   string             `json:"base_path" yaml:"base_path"`
	Dtctl      string             `json:"dtctl,omitempty" yaml:"dtctl,omitempty"`
	Operations []OperationSummary `json:"operations" yaml:"operations"`
}

// Describe projects a specification to its operation index.
func (h *Handler) Describe(e Entry) (*APIDescription, error) {
	spec, err := h.Spec(e)
	if err != nil {
		return nil, err
	}

	desc := &APIDescription{
		Name:     e.Name,
		Title:    spec.Title,
		Version:  spec.Version,
		Category: spec.Category,
		Summary:  spec.Summary,
		BasePath: spec.BasePath,
		Dtctl:    NativeResourceFor(spec.BasePath),
	}
	if desc.Name == "" {
		desc.Name = spec.Title
	}

	for _, op := range spec.Operations {
		desc.Operations = append(desc.Operations, OperationSummary{
			Operation:  op.ID(),
			Summary:    op.Summary,
			Scope:      strings.Join(op.Scopes, ", "),
			Deprecated: op.Deprecated,
		})
	}
	sort.SliceStable(desc.Operations, func(i, j int) bool {
		return desc.Operations[i].Operation < desc.Operations[j].Operation
	})

	return desc, nil
}

// OperationDetail is one operation in full — everything needed to compose the
// call without the rest of the document.
//
// The bar here is deliberately higher than "a summary of the operation":
// composing a real request needs every parameter with its type and
// required-ness, the request body schema, and the response shapes. This is the
// prerequisite that makes `exec api` usable at all, which is why the operation
// index and this view are two grains of the same feature rather than
// alternatives.
type OperationDetail struct {
	API         string `json:"api" yaml:"api"`
	BasePath    string `json:"base_path" yaml:"base_path"`
	Operation   string `json:"operation" yaml:"operation"`
	OperationID string `json:"operation_id,omitempty" yaml:"operation_id,omitempty"`
	Summary     string `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Deprecated  bool   `json:"deprecated,omitempty" yaml:"deprecated,omitempty"`
	// URL is the full request path to call, base path plus operation path, so the
	// caller does not have to concatenate them (and cannot get it wrong).
	URL string `json:"url" yaml:"url"`
	// RequiredScopes is what the document declares. Empty means it declares
	// nothing — see [Classification] for what dtctl does in that case.
	RequiredScopes []string             `json:"required_scopes,omitempty" yaml:"required_scopes,omitempty"`
	Parameters     []apispec.Parameter  `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	RequestBody    *apispec.RequestBody `json:"request_body,omitempty" yaml:"request_body,omitempty"`
	Responses      []apispec.Response   `json:"responses,omitempty" yaml:"responses,omitempty"`
	// Example is a ready-to-run `dtctl exec api` invocation for this operation.
	Example string `json:"example" yaml:"example"`
}

// DescribeOperation resolves one operation of a specification in full. The
// operation is addressed as "METHOD /path" (its [Operation.ID]).
func (h *Handler) DescribeOperation(e Entry, operationID string) (*OperationDetail, error) {
	spec, err := h.Spec(e)
	if err != nil {
		return nil, err
	}

	op, ok := spec.FindByID(operationID)
	if !ok {
		return nil, unknownOperationError(spec, operationID)
	}

	name := e.Name
	if name == "" {
		name = spec.Title
	}

	detail := &OperationDetail{
		API:            name,
		BasePath:       spec.BasePath,
		Operation:      op.ID(),
		OperationID:    op.OperationID,
		Summary:        op.Summary,
		Description:    op.Description,
		Deprecated:     op.Deprecated,
		URL:            operationURL(spec.BasePath, op.Path),
		RequiredScopes: op.Scopes,
		Parameters:     op.Parameters,
		RequestBody:    op.RequestBody,
		Responses:      op.Responses,
	}
	detail.Example = ExampleInvocation(detail.URL, *op)
	return detail, nil
}

// operationURL joins a base path and an operation path into the path a caller
// would pass to `exec api`.
//
// The "/" case is the collection root — an operation the specification keys on the
// empty path — where naive concatenation yields a trailing slash that is not the
// endpoint's own spelling. The URL here is copied into a suggested command, so it
// has to be the path that actually works.
func operationURL(basePath, opPath string) string {
	if opPath == "/" {
		return basePath
	}
	return basePath + opPath
}

// unknownOperationError names the closest available operations rather than only
// reporting the miss, so a caller that guessed an identifier can correct it
// without re-listing the whole API.
func unknownOperationError(spec *Spec, operationID string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "no operation %q in %s", operationID, spec.Title)

	needle := strings.ToLower(operationID)
	if _, path, found := strings.Cut(needle, " "); found {
		needle = strings.TrimSpace(path)
	}

	var near []string
	for _, op := range spec.Operations {
		if needle != "" && strings.Contains(strings.ToLower(op.Path), needle) {
			near = append(near, op.ID())
		}
	}
	sort.Strings(near)
	if len(near) > 8 {
		near = near[:8]
	}
	if len(near) > 0 {
		b.WriteString("\n\nDid you mean:")
		for _, id := range near {
			fmt.Fprintf(&b, "\n  %s", id)
		}
	}
	b.WriteString("\n\nOperations are addressed as 'METHOD /path'; list them with 'dtctl describe api <name>'.")
	return fmt.Errorf("%s", b.String())
}

// ExampleInvocation renders a ready-to-run `dtctl exec api` command for an
// operation, with path placeholders left in place so the caller sees exactly
// what it has to substitute.
func ExampleInvocation(url string, op Operation) string {
	cmd := "dtctl exec api " + url
	if op.Method != "GET" {
		cmd += " -X " + op.Method
	}
	if op.RequestBody != nil && len(op.RequestBody.Contents) > 0 {
		cmd += " -d @body.json"
	}
	return cmd
}
