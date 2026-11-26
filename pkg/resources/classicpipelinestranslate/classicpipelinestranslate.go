// Package classicpipelinestranslate is the CLI resource layer for the
// OpenPipeline classic pipelines translation endpoint. It delegates HTTP calls
// to the SDK and decodes the opaque translated pipeline into a generic value so
// it can be rendered faithfully as JSON or structured YAML.
package classicpipelinestranslate

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdktranslate "github.com/Dynatrace/dtctl/sdk/api/classicpipelinestranslate"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// ValidConfigurations are the configuration scopes the endpoint accepts.
var ValidConfigurations = []string{"logs", "bizevents"}

// IsValidConfiguration reports whether scope is a configuration the endpoint
// supports.
func IsValidConfiguration(scope string) bool {
	return slices.Contains(ValidConfigurations, scope)
}

// TranslateOptions are the inputs for a translation request.
type TranslateOptions = sdktranslate.TranslateOptions

// TranslationResult is the CLI read model for a translated classic pipeline.
//
// Unlike the SDK type (where Value is a raw JSON document), Value here is the
// decoded document so that JSON and YAML printers render it structurally rather
// than as an escaped string.
type TranslationResult struct {
	Value       any  `json:"value" yaml:"value"`
	WithWarning bool `json:"withWarning" yaml:"withWarning"`
}

// Handler translates classic pipelines into OpenPipeline configuration
// pipelines.
type Handler struct {
	sdk *sdktranslate.Handler
}

// NewHandler creates a new classic pipelines translation handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdktranslate.NewHandler(httpclient.Wrap(c.HTTP()))}
}

// Translate calls the translation endpoint and decodes the opaque pipeline
// document for display.
func (h *Handler) Translate(opts TranslateOptions) (*TranslationResult, error) {
	sdkResult, err := h.sdk.Translate(context.Background(), opts)
	if err != nil {
		return nil, err
	}

	result := &TranslationResult{WithWarning: sdkResult.WithWarning}
	// Decode the opaque pipeline so YAML/JSON printers render it structurally.
	// An absent or null value is left as nil rather than treated as an error —
	// the document is forwarded verbatim.
	if len(sdkResult.Value) > 0 {
		if err := json.Unmarshal(sdkResult.Value, &result.Value); err != nil {
			return nil, fmt.Errorf("decode translated pipeline: %w", err)
		}
	}

	return result, nil
}
