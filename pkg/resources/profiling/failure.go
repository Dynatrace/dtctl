package profiling

import (
	"errors"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// FindIDsQuery is the dtctl command that lists eligible entity ids.
const FindIDsQuery = `dtctl query 'smartscapeNodes PROCESS | fields id, dt.process_group.detected_name, dt.process_group.id'`

// FailureReason names why the API refused an analysis, for a caller that
// branches on it. The values are stable.
type FailureReason string

const (
	// FailureBadRequest: the API rejected the request parameters (400).
	FailureBadRequest FailureReason = "bad_request"
	// FailureForbidden: a scope or an IAM permission is missing (403).
	FailureForbidden FailureReason = "forbidden"
	// FailureNotFound: the environment knows no such process (404).
	FailureNotFound FailureReason = "not_found"
	// FailureThrottled: the environment is at its limit of concurrent
	// analyses (429), after the waits the SDK already made.
	FailureThrottled FailureReason = "throttled"
)

// Failure is an API failure a caller can act on.
type Failure struct {
	Reason     FailureReason
	StatusCode int
	// Message is the API's own message.
	Message string
	// Advice is what to do about it, one step per entry, worded for a dtctl
	// user.
	Advice []string
}

// ClassifyError recognizes the API failures of an analysis that a caller can
// act on, and says what to do about each. It reports false for every other
// error, which is then best shown as it is.
func ClassifyError(err error) (Failure, bool) {
	var apiErr *httpclient.APIError
	if !errors.As(err, &apiErr) {
		return Failure{}, false
	}
	f := Failure{StatusCode: apiErr.StatusCode, Message: apiErr.Message}
	switch apiErr.StatusCode {
	case 400:
		f.Reason = FailureBadRequest
		f.Advice = []string{
			"The API rejected the request parameters — check the timeframe (in the past, at most 2h) and the entity id",
		}
	case 403:
		f.Reason = FailureForbidden
		f.Advice = []string{
			"Profiling needs storage:profiles:read and storage:entities:read, both on the token and in the IAM policy",
			"OAuth (dtctl auth login): log in again so the session requests storage:profiles:read",
		}
	case 404:
		f.Reason = FailureNotFound
		f.Advice = []string{
			"The environment knows no such process (ids are per environment, and short-lived process instances disappear)",
			"List eligible ids: " + FindIDsQuery,
		}
	case 429:
		f.Reason = FailureThrottled
		f.Advice = []string{
			"The environment limits concurrent profiling analyses and is at that limit — wait and retry, and avoid running many analyses in parallel",
		}
	default:
		return Failure{}, false
	}
	return f, true
}
