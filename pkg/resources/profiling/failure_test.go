package profiling

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func TestClassifyError(t *testing.T) {
	api := func(status int) error {
		// Wrapped, the way the SDK returns it.
		return fmt.Errorf("codelevelanalysis: submit: %w", httpclient.NewAPIError(status, "the API's message", ""))
	}
	cases := []struct {
		name   string
		err    error
		ok     bool
		reason FailureReason
		advice []string // one substring per expected entry
	}{
		{name: "400", err: api(400), ok: true, reason: FailureBadRequest,
			advice: []string{"check the timeframe (in the past, at most 2h) and the entity id"}},
		{name: "403", err: api(403), ok: true, reason: FailureForbidden,
			advice: []string{"storage:profiles:read and storage:entities:read", "dtctl auth login"}},
		{name: "404", err: api(404), ok: true, reason: FailureNotFound,
			advice: []string{"knows no such process", "List eligible ids: " + FindIDsQuery}},
		{name: "429", err: api(429), ok: true, reason: FailureThrottled,
			advice: []string{"limits concurrent profiling analyses"}},
		{name: "401 is left to generic handling", err: api(401)},
		{name: "500 is left to generic handling", err: api(500)},
		{name: "not an API error", err: errors.New("codelevelanalysis: analysis did not complete within 60s")},
		{name: "nil", err: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := ClassifyError(tc.err)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				if f.Reason != "" || f.StatusCode != 0 || f.Message != "" || f.Advice != nil {
					t.Errorf("an unclassified error must yield the zero Failure, got %+v", f)
				}
				return
			}
			if f.Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", f.Reason, tc.reason)
			}
			if f.Message != "the API's message" {
				t.Errorf("Message = %q", f.Message)
			}
			if got := fmt.Sprint(f.StatusCode); got != tc.name {
				t.Errorf("StatusCode = %s, want %s", got, tc.name)
			}
			if len(f.Advice) != len(tc.advice) {
				t.Fatalf("Advice = %q, want %d entries", f.Advice, len(tc.advice))
			}
			for i, want := range tc.advice {
				if !strings.Contains(f.Advice[i], want) {
					t.Errorf("Advice[%d] = %q, want it to contain %q", i, f.Advice[i], want)
				}
			}
		})
	}
}
