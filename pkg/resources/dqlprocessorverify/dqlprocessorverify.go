// Package dqlprocessorverify is the CLI resource layer for the OpenPipeline DQL
// processor verify endpoint. It delegates HTTP calls to the SDK and shapes the
// response into a display-ready VerifyResult using the shared formatting logic
// from the matcherverify package.
package dqlprocessorverify

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/resources/matcherverify"
	sdkdql "github.com/Dynatrace/dtctl/sdk/api/dqlprocessorverify"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// VerifyOptions are the inputs for a single DQL processor verify request.
type VerifyOptions struct {
	Script          string
	ConfigurationID string
}

// VerifyResult is a type alias for the shared matcherverify.VerifyResult, which
// has the same table/JSON/YAML shape for both verify commands.
type VerifyResult = matcherverify.VerifyResult

// Handler verifies OpenPipeline DQL processor scripts.
type Handler struct {
	sdk *sdkdql.Handler
}

// NewHandler creates a new DQL processor verify handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdkdql.NewHandler(httpclient.Wrap(c.HTTP()))}
}

// Verify calls the verify endpoint and returns the display-ready result.
func (h *Handler) Verify(opts VerifyOptions) (*VerifyResult, error) {
	sdkResult, err := h.sdk.Verify(context.Background(), sdkdql.DQLProcessorVerifyRequest{
		Script:          opts.Script,
		ConfigurationID: opts.ConfigurationID,
	})
	if err != nil {
		return nil, err
	}
	return matcherverify.FromSDKVerifyResponse(sdkResult), nil
}
