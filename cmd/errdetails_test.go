package cmd

import (
	"errors"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// errWithDetails renders err with the response body that APIError.Error() omits.
func errWithDetails(err error) string {
	text := err.Error()
	var apiErr *httpclient.APIError
	if errors.As(err, &apiErr) && apiErr.Details != "" {
		return text + " - " + apiErr.Details
	}
	return text
}
