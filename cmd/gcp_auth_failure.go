package cmd

import "strings"

// isGCPAuthFailure reports whether err is the Settings API's "GCP authentication
// failed" answer, which a freshly granted IAM policy produces until it
// propagates. The text is in the response body, not in Error().
func isGCPAuthFailure(err error) bool {
	return strings.Contains(errorText(err), "GCP authentication failed")
}
