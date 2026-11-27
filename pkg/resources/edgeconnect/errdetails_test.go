package edgeconnect

import (
	"errors"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// errWithDetails renders err the way the CLI does: APIError keeps the response
// body out of Error(), so a test that asserts on the body reads Details.
func errWithDetails(err error) string {
	text := err.Error()
	var apiErr *httpclient.APIError
	if errors.As(err, &apiErr) && apiErr.Details != "" {
		return text + " - " + apiErr.Details
	}
	return text
}
