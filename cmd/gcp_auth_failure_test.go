package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func TestIsGCPAuthFailureReadsTheResponseBody(t *testing.T) {
	fromBody := fmt.Errorf("update: %w", httpclient.NewAPIError(400, "Constraints violated.", "GCP authentication failed for service account x"))
	require.True(t, isGCPAuthFailure(fromBody))
	require.False(t, isGCPAuthFailure(httpclient.NewAPIError(400, "Constraints violated.", "other")))
	require.False(t, isGCPAuthFailure(errors.New("boom")))
}
