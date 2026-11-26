package profiling

import (
	"errors"

	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// FailureReason names why an analysis failed, for a caller that branches on
// it. The values follow the tier of `dtctl exec profiling`
// (docs/STABILITY.md): while the command is experimental, they may change with
// it.
type FailureReason string

const (
	// FailureInvalidInput: the request was refused before it was sent: an
	// entity that is not eligible (EligibleEntity), an unknown leaf type, or
	// a window a Handler does not take (errors.Is and errors.As find
	// ErrWindowEmpty and the *WindowTooLongError).
	FailureInvalidInput FailureReason = "invalid_input"
	// FailureBadRequest: the API rejected the request parameters (400).
	FailureBadRequest FailureReason = "bad_request"
	// FailureForbidden: a scope or an IAM permission is missing (403).
	// Analyses need storage:profiles:read and storage:entities:read.
	FailureForbidden FailureReason = "forbidden"
	// FailureNotFound: the environment knows no such process (404 on the
	// request that starts the analysis). Ids are per environment, and
	// short-lived process instances disappear.
	FailureNotFound FailureReason = "not_found"
	// FailureTaskExpired: the analysis was started, but the environment no
	// longer knows its task (404 while waiting for the result). Running it
	// again starts a new one.
	FailureTaskExpired FailureReason = "task_expired"
	// FailureThrottled: the API turned the request away (429), after the
	// waits the SDK already made. On the request that starts the analysis
	// that is the environment's limit of concurrent analyses; while waiting
	// for the result it is the last answer before the SDK stopped asking.
	FailureThrottled FailureReason = "throttled"
	// FailureServer: the API failed on its side (5xx).
	FailureServer FailureReason = "server_error"
	// FailureTimeout: the analysis was still running after the last poll.
	FailureTimeout FailureReason = "timeout"
)

// Failure is an analysis failure a caller can act on.
type Failure struct {
	Reason FailureReason
	// StatusCode is the status the API answered with, and 0 for a failure
	// that is not an answer of the API (FailureInvalidInput, FailureTimeout).
	StatusCode int
	// Message is the API's own message, or the error's text where the
	// failure is not an answer of the API.
	Message string
}

// ClassifyError recognizes the failures of an analysis that a caller can act
// on, in an error that Handler.Hotspots or Handler.Allocations returned. It
// reports false for every other error: a failed connection, a cancelled
// context, a status with no meaning of its own here such as 401, and any
// error that did not come from an analysis, whose status means something
// else. Such an error is best shown as it is.
func ClassifyError(err error) (Failure, bool) {
	var runErr *sdkcla.Error
	if !errors.As(err, &runErr) {
		return Failure{}, false
	}
	phase := runErr.Phase
	if phase == sdkcla.PhaseValidate {
		return Failure{Reason: FailureInvalidInput, Message: runErr.Error()}, true
	}
	if errors.Is(err, sdkcla.ErrNotCompleted) {
		return Failure{Reason: FailureTimeout, Message: err.Error()}, true
	}

	var apiErr *httpclient.APIError
	if !errors.As(err, &apiErr) {
		return Failure{}, false
	}
	f := Failure{StatusCode: apiErr.StatusCode, Message: apiErr.Message}
	switch {
	case apiErr.StatusCode == 400:
		f.Reason = FailureBadRequest
	case apiErr.StatusCode == 403:
		f.Reason = FailureForbidden
	case apiErr.StatusCode == 404 && phase == sdkcla.PhasePoll:
		// The entity was found when the analysis was submitted: what a poll
		// does not find is the task.
		f.Reason = FailureTaskExpired
	case apiErr.StatusCode == 404:
		f.Reason = FailureNotFound
	case apiErr.StatusCode == 429:
		f.Reason = FailureThrottled
	case apiErr.StatusCode >= 500:
		f.Reason = FailureServer
	default:
		return Failure{}, false
	}
	return f, true
}
