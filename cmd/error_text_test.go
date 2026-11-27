package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/pkg/resources/workflow"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func TestErrorText(t *testing.T) {
	apiErr := httpclient.NewAPIError(400, "Invalid request.", "body: field x is required")

	require.Equal(t, "API error (400): Invalid request.", apiErr.Error(), "Error() must not carry the body")
	require.Equal(t, "API error (400): Invalid request. - body: field x is required", errorText(apiErr))
	require.Equal(t, "failed to get x: API error (400): Invalid request. - body: field x is required",
		errorText(fmt.Errorf("failed to get x: %w", apiErr)))

	// No details: nothing appended. A message that already shows them is not doubled.
	require.Equal(t, "API error (404): gone", errorText(httpclient.NewAPIError(404, "gone", "")))
	require.Equal(t, "boom", errorText(errors.New("boom")))
	require.Equal(t, "", errorText(nil))

	// A diagnostic.Error renders itself; its wrapped body is not appended again.
	diag := &diagnostic.Error{Operation: "get x", StatusCode: 403, Message: "access denied", Err: apiErr}
	require.Equal(t, diag.Error(), errorText(diag))
}

// The upstream body is kept out of Error() but must still reach the user, in
// human mode and in the agent envelope.
func TestAPIErrorBodyStillRendered(t *testing.T) {
	const body = `{"error":{"code":400,"message":"Constraints violated.","details":{"constraintViolations":[{"path":"retention","message":"must be at least 1 day"}]}}}`

	setup := func(t *testing.T) {
		env := newMockEnvironment(t)
		env.respond("GET", "/platform/storage/management/v1/bucket-definitions",
			mockResponse{status: http.StatusBadRequest, contentType: "application/json", body: body})
	}

	t.Run("agent envelope", func(t *testing.T) {
		setup(t)
		code, envelope := runAgentEnvelope(t, []string{"get", "buckets"})
		require.NotZero(t, code)
		detail, _ := envelope["error"].(map[string]any)
		require.NotNil(t, detail)
		message, _ := detail["message"].(string)
		require.Contains(t, message, "Constraints violated.")
		require.Contains(t, message, "retention: must be at least 1 day")
		require.Equal(t, "bad_request", detail["code"])
	})

	t.Run("human", func(t *testing.T) {
		setup(t)
		var code int
		stderr := captureAuthStderr(t, func() {
			code, _ = captureRun(t, []string{"get", "buckets"}, RunOptions{})
		})
		require.NotZero(t, code)
		require.Contains(t, stderr, "Constraints violated.")
		require.Contains(t, stderr, "retention: must be at least 1 day")
	})
}

func TestTaskLogErrorTextKeepsPerTaskDetails(t *testing.T) {
	e := &workflow.TaskLogError{ExecutionID: "ex1", Total: 2, Failed: []workflow.TaskLogFailure{
		{Task: "a", Err: httpclient.NewAPIError(500, "boom", "detail-a")},
		{Task: "b", Err: httpclient.NewAPIError(403, "denied", "detail-b")},
	}}
	got := taskLogErrorText(e)
	if !strings.Contains(got, "detail-a") || !strings.Contains(got, "detail-b") {
		t.Errorf("per-task details lost: %q", got)
	}
}
