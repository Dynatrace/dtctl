// Package dqlprocessorverify is a thin, read-only SDK client for the
// OpenPipeline DQL processor verify endpoint, which validates a DQL processor
// script against an optional configuration scope.
//
// The response types are shared with the sibling matcherverify package
// (VerifyResponse, MetadataNotification, SyntaxRange, SyntaxPosition).
package dqlprocessorverify

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/api/matcherverify"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const basePath = "/platform/openpipeline/v1/dqlProcessor/verify"

// Handler calls the DQL processor verify endpoint.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new DQL processor verify handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// DQLProcessorVerifyRequest is the request body for the DQL processor verify endpoint.
type DQLProcessorVerifyRequest struct {
	Script          string `json:"script"`
	ConfigurationID string `json:"configurationId,omitempty"`
}

// Verify sends a DQL processor verify request and returns the parsed response.
func (h *Handler) Verify(ctx context.Context, req DQLProcessorVerifyRequest) (*matcherverify.VerifyResponse, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(req).
		Post(basePath)
	if err != nil {
		return nil, fmt.Errorf("verify dql processor: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("verify dql processor: %w", err)
	}

	var result matcherverify.VerifyResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("verify dql processor: parse response: %w", err)
	}

	return &result, nil
}
