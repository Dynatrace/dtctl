package httpclient

import (
	"errors"
	"fmt"
)

// Sentinel errors for common HTTP status codes.
// Use errors.Is to check for these.
var (
	ErrBadRequest   = errors.New("bad request")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrRateLimited  = errors.New("rate limited")
	ErrServerError  = errors.New("server error")
)

// APIError represents an error response from a Dynatrace API.
type APIError struct {
	// StatusCode is the HTTP status code.
	StatusCode int
	// Message is a human-readable error description.
	Message string
	// Details contains additional error information from the API response body.
	// It is deliberately not part of Error(): the body is upstream-controlled
	// text that callers must opt in to logging or showing (errors.As, then
	// read Details).
	Details string
	// sentinel is the matching sentinel error, if any.
	sentinel error
}

// Error implements the error interface. It returns the status and the short
// message only; the raw response body in Details is never included, so logging
// or returning the error cannot pass upstream-supplied text along.
func (e *APIError) Error() string {
	return fmt.Sprintf("API error (%d): %s", e.StatusCode, e.Message)
}

// Unwrap returns the sentinel error for this status code, enabling errors.Is.
func (e *APIError) Unwrap() error {
	return e.sentinel
}

// NewAPIError creates a new API error with the appropriate sentinel.
func NewAPIError(statusCode int, message, details string) *APIError {
	return &APIError{
		StatusCode: statusCode,
		Message:    message,
		Details:    details,
		sentinel:   sentinelForStatus(statusCode),
	}
}

func sentinelForStatus(code int) error {
	switch code {
	case 400:
		return ErrBadRequest
	case 401:
		return ErrUnauthorized
	case 403:
		return ErrForbidden
	case 404:
		return ErrNotFound
	case 409:
		return ErrConflict
	case 429:
		return ErrRateLimited
	default:
		if code >= 500 {
			return ErrServerError
		}
		return nil
	}
}
