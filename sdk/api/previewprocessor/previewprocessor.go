// Package previewprocessor is a thin SDK client for the OpenPipeline preview
// processor endpoint, which executes a processor definition against sample
// records and returns the matched/modified results.
//
// The request body is a oneOf union envelope (processor definition + sample
// records) that the SDK accepts and forwards verbatim as a [json.RawMessage]
// without interpreting or validating its structure.
//
// Error codes 408 (request timeout) and 429 (too many concurrent requests) are
// caught and surfaced with clear, distinct messages.
package previewprocessor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const basePath = "/platform/openpipeline/v1/preview/processor"

// Handler calls the preview processor endpoint.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new preview processor handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// PreviewProcessorResultEntry is a single result entry from the preview endpoint.
type PreviewProcessorResultEntry struct {
	Matched bool           `json:"matched"`
	Record  map[string]any `json:"record,omitempty"`
}

// PreviewProcessorResult is the full response from the preview endpoint.
type PreviewProcessorResult struct {
	Results []PreviewProcessorResultEntry `json:"results"`
}

// Preview sends a processor preview request and returns the parsed results.
// The envelope parameter is the raw JSON request body (a oneOf union of
// processor definition + sample records) forwarded verbatim to the API.
func (h *Handler) Preview(ctx context.Context, envelope json.RawMessage) (*PreviewProcessorResult, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody([]byte(envelope)).
		Post(basePath)
	if err != nil {
		return nil, fmt.Errorf("preview processor: %w", err)
	}

	// Handle these error codes explicitly before the generic checker so we can
	// return a distinct, actionable message for each.
	switch resp.StatusCode() {
	case http.StatusNotFound:
		return nil, fmt.Errorf("preview processor: endpoint not found (the preview processor endpoint is not available in this environment)")
	case http.StatusRequestTimeout:
		return nil, fmt.Errorf("preview processor: request timed out — the sample data may be too large or the processor too complex; try reducing the number of sample records")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("preview processor: too many concurrent preview requests — wait a moment and retry")
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("preview processor: %w", err)
	}

	var result PreviewProcessorResult
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("preview processor: parse response: %w", err)
	}

	return &result, nil
}
