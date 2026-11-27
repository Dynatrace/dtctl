package httpclient

import (
	"errors"
	"testing"
)

func TestAPIError_Error(t *testing.T) {
	e := NewAPIError(404, "not found", "")
	if got := e.Error(); got != "API error (404): not found" {
		t.Errorf("got %q", got)
	}

	e = NewAPIError(400, "bad request", "field X is required")
	if got := e.Error(); got != "API error (400): bad request" {
		t.Errorf("got %q", got)
	}
	if e.Details != "field X is required" {
		t.Errorf("Details = %q, want it kept for callers that opt in", e.Details)
	}
}

func TestAPIError_Unwrap(t *testing.T) {
	tests := []struct {
		code     int
		sentinel error
	}{
		{400, ErrBadRequest},
		{401, ErrUnauthorized},
		{403, ErrForbidden},
		{404, ErrNotFound},
		{409, ErrConflict},
		{429, ErrRateLimited},
		{500, ErrServerError},
		{503, ErrServerError},
	}
	for _, tt := range tests {
		e := NewAPIError(tt.code, "test", "")
		if !errors.Is(e, tt.sentinel) {
			t.Errorf("NewAPIError(%d) should match sentinel %v", tt.code, tt.sentinel)
		}
	}
}

func TestAPIError_ErrorsAs(t *testing.T) {
	err := error(NewAPIError(403, "forbidden", ""))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Error("errors.As should match *APIError")
	}
	if apiErr.StatusCode != 403 {
		t.Errorf("status = %d, want 403", apiErr.StatusCode)
	}
}
