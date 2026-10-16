package cmd

import (
	"context"
	"fmt"
	"testing"

	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
)

// TestErrorToDetail_QueryStateError covers #495: each query end state gets
// its own envelope code, so an agent can tell a retryable expiry from a
// failed query.
func TestErrorToDetail_QueryStateError(t *testing.T) {
	tests := []struct {
		state string
		code  string
	}{
		{sdkquery.StateFailed, "query_failed"},
		{sdkquery.StateCancelled, "query_cancelled"},
		{sdkquery.StateResultGone, "result_expired"},
		{"QUEUED", "unknown_query_state"},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			err := fmt.Errorf("run query: %w", &sdkquery.StateError{State: tt.state})

			detail := errorToDetail(context.Background(), err)

			if detail.Code != tt.code {
				t.Errorf("Code = %q, want %q", detail.Code, tt.code)
			}
			if detail.Message != err.Error() {
				t.Errorf("Message = %q, want %q", detail.Message, err.Error())
			}
		})
	}
}
