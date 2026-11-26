// Package matcherverify is a thin, read-only SDK client for the OpenPipeline
// matcher verify endpoint, which validates a DQL matcher expression against an
// optional configuration scope and stage context.
//
// The shared verify response types (VerifyResponse, MetadataNotification,
// SyntaxPosition, SyntaxRange) are defined here and re-used by the sibling
// dqlprocessorverify package, which covers the DQL processor verify endpoint.
package matcherverify

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const basePath = "/platform/openpipeline/v1/matcher/verify"

// Handler calls the matcher verify endpoint.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new matcher verify handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// MatcherVerifyRequest is the request body for the matcher verify endpoint.
type MatcherVerifyRequest struct {
	Query           string `json:"query"`
	ConfigurationID string `json:"configurationId,omitempty"`
	Context         string `json:"context,omitempty"`
}

// SyntaxPosition describes a single position (line/column/index) in a source expression.
type SyntaxPosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
	Index  int `json:"index"`
}

// SyntaxRange describes a source range with start and end positions.
type SyntaxRange struct {
	Start SyntaxPosition `json:"start"`
	End   SyntaxPosition `json:"end"`
}

// MetadataNotification is a single diagnostic notification returned by a verify endpoint.
type MetadataNotification struct {
	Severity       string       `json:"severity"`
	Message        string       `json:"message"`
	SyntaxPosition *SyntaxRange `json:"syntaxPosition,omitempty"`
}

// VerifyResponse is the response body returned by both the matcher verify and
// DQL processor verify endpoints.
type VerifyResponse struct {
	Valid         bool                   `json:"valid"`
	Notifications []MetadataNotification `json:"notifications,omitempty"`
}

// Verify sends a matcher verify request and returns the parsed response.
func (h *Handler) Verify(ctx context.Context, req MatcherVerifyRequest) (*VerifyResponse, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(req).
		Post(basePath)
	if err != nil {
		return nil, fmt.Errorf("verify matcher: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("verify matcher: %w", err)
	}

	var result VerifyResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("verify matcher: parse response: %w", err)
	}

	return &result, nil
}
