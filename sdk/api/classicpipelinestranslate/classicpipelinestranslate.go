// Package classicpipelinestranslate is a thin, read-only SDK client for the
// OpenPipeline "classic pipelines translation" endpoint, which converts an
// existing Classic pipeline configuration into an OpenPipeline configuration
// pipeline (Settings shape).
//
// The translated pipeline document is treated as opaque: the SDK forwards it
// verbatim as a [json.RawMessage] and does not interpret, reshape, or validate
// its contents.
package classicpipelinestranslate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// basePath is the OpenPipeline classic-pipelines translation endpoint, served
// on the standard apps domain already stored in the dtctl context.
const basePath = "/platform/openpipeline/v1/classic-pipelines/translate"

// Handler calls the classic pipelines translation endpoint.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new classic pipelines translation handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// TranslateOptions are the inputs for a single translation request.
//
// Configuration is the scope to translate (e.g. "logs" or "bizevents") and is
// required. The three booleans mirror the endpoint's query parameters; they are
// always sent explicitly so a caller can override the server-side defaults
// (notably SkipDisabledRules, which the server defaults to false).
type TranslateOptions struct {
	// Configuration is the classic pipeline scope to translate (required).
	Configuration string
	// IncludeSampleData includes processor sample data in the translation
	// (API default: true).
	IncludeSampleData bool
	// SkipDisabledRules skips disabled rules during translation
	// (API default: false).
	SkipDisabledRules bool
	// SkipBuiltinProcessingRules skips built-in processing rules during
	// translation (API default: true).
	SkipBuiltinProcessingRules bool
}

// TranslationResult is the endpoint's ClassicPipelineTranslationResult.
//
// Value is the translated OpenPipeline pipeline, an arbitrary document the SDK
// treats as opaque and forwards verbatim. WithWarning is true when at least one
// processing rule's definition script could not be translated automatically and
// the result needs a manual rewrite.
type TranslationResult struct {
	Value       json.RawMessage `json:"value"`
	WithWarning bool            `json:"withWarning"`
}

// Translate calls the translation endpoint for the requested configuration
// scope and returns the result verbatim.
func (h *Handler) Translate(ctx context.Context, opts TranslateOptions) (*TranslationResult, error) {
	if opts.Configuration == "" {
		return nil, fmt.Errorf("translate classic pipeline: configuration is required")
	}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		// All four parameters are sent explicitly (rather than relying on
		// server defaults) so that, for example, --skip-disabled-rules=true
		// can override the server's false default.
		SetQueryParam("configuration", opts.Configuration).
		SetQueryParam("includeSampleData", strconv.FormatBool(opts.IncludeSampleData)).
		SetQueryParam("skipDisabledRules", strconv.FormatBool(opts.SkipDisabledRules)).
		SetQueryParam("skipBuiltinProcessingRules", strconv.FormatBool(opts.SkipBuiltinProcessingRules)).
		Get(basePath)
	if err != nil {
		return nil, fmt.Errorf("translate classic pipeline: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("translate classic pipeline: %w", err)
	}

	var result TranslationResult
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("translate classic pipeline: parse response: %w", err)
	}

	return &result, nil
}
