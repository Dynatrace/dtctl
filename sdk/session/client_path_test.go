package session

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// The CLI's own HTTP client is built here, not by httpclient.New, and every
// resource handler wraps this same resty client. If the request-path guard were
// installed on only one of the two constructors it would cover almost nothing.
func TestNewClientGuardsRequestPaths(t *testing.T) {
	c, err := NewClient("https://example.apps.dynatrace.com", "dt0c01.TEST")
	require.NoError(t, err)

	_, err = c.HTTP().R().Delete("/platform/slo/v1/slos/slo-1#x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid request path")
	require.Contains(t, err.Error(), "URL fragment")
}

// A request rejected before it is sent produces no response, and resty consults
// the retry conditions anyway. httpclient.IsRetryable has to survive that.
func TestNewClientDoesNotRetryARequestThatWasNeverSent(t *testing.T) {
	c, err := NewClient("https://example.apps.dynatrace.com", "dt0c01.TEST")
	require.NoError(t, err)

	require.NotPanics(t, func() {
		_, _ = c.HTTP().R().Get("/platform/slo/v1/slos/slo-1#x")
	})
	require.False(t, httpclient.IsRetryable(nil, errors.New("rejected before sending")))
}
