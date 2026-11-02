// Package matcherlqltodql is the CLI resource layer for the LQL-to-DQL translation endpoint.
package matcherlqltodql

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkapi "github.com/Dynatrace/dtctl/sdk/api/matcherlqltodql"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// TranslationResult is the CLI read-model for a translated LQL matcher.
type TranslationResult struct {
	Query string `json:"query" yaml:"query" table:"QUERY"`
}

// Handler translates LQL matcher expressions into DQL matcher expressions.
type Handler struct {
	sdk *sdkapi.Handler
}

// NewHandler creates a new LQL-to-DQL translation handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdkapi.NewHandler(httpclient.Wrap(c.HTTP()))}
}

// Translate calls the translation endpoint and returns the DQL result.
func (h *Handler) Translate(lql string) (*TranslationResult, error) {
	result, err := h.sdk.Translate(context.Background(), lql)
	if err != nil {
		return nil, err
	}
	return &TranslationResult{Query: result.Query}, nil
}
