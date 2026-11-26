package profiling

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func TestClassifyError(t *testing.T) {
	// The errors as the SDK returns them: an *Error naming the phase, around
	// the API's answer where there was one.
	api := func(phase sdkcla.Phase, status int) error {
		return &sdkcla.Error{Phase: phase, Err: fmt.Errorf("codelevelanalysis: %s: %w", phase, httpclient.NewAPIError(status, "the API's message", ""))}
	}
	// As the SDK's poll returns it: ErrNotCompleted wrapped with the details.
	notCompleted := &sdkcla.Error{Phase: sdkcla.PhasePoll, Err: fmt.Errorf("%w within 60s (token t1)", sdkcla.ErrNotCompleted)}
	refused := &sdkcla.Error{Phase: sdkcla.PhaseValidate, Err: errors.New(`codelevelanalysis: unknown leaf type "nope": use total, service, or background`)}
	cases := []struct {
		name string
		err  error
		want Failure
		ok   bool
	}{
		{name: "400", err: api(sdkcla.PhaseSubmit, 400), ok: true,
			want: Failure{Reason: FailureBadRequest, StatusCode: 400, Message: "the API's message"}},
		{name: "403", err: api(sdkcla.PhaseSubmit, 403), ok: true,
			want: Failure{Reason: FailureForbidden, StatusCode: 403, Message: "the API's message"}},
		{name: "403 on a poll", err: api(sdkcla.PhasePoll, 403), ok: true,
			want: Failure{Reason: FailureForbidden, StatusCode: 403, Message: "the API's message"}},
		// The same status, two meanings: the submit names the entity, a poll
		// names the task.
		{name: "404 on the submit", err: api(sdkcla.PhaseSubmit, 404), ok: true,
			want: Failure{Reason: FailureNotFound, StatusCode: 404, Message: "the API's message"}},
		{name: "404 on a poll", err: api(sdkcla.PhasePoll, 404), ok: true,
			want: Failure{Reason: FailureTaskExpired, StatusCode: 404, Message: "the API's message"}},
		{name: "429", err: api(sdkcla.PhaseSubmit, 429), ok: true,
			want: Failure{Reason: FailureThrottled, StatusCode: 429, Message: "the API's message"}},
		{name: "500", err: api(sdkcla.PhaseSubmit, 500), ok: true,
			want: Failure{Reason: FailureServer, StatusCode: 500, Message: "the API's message"}},
		{name: "503 on a poll", err: api(sdkcla.PhasePoll, 503), ok: true,
			want: Failure{Reason: FailureServer, StatusCode: 503, Message: "the API's message"}},
		{name: "never completed", err: notCompleted, ok: true,
			want: Failure{Reason: FailureTimeout, Message: "codelevelanalysis: analysis did not complete within 60s (token t1)"}},
		{name: "429 on a poll", err: api(sdkcla.PhasePoll, 429), ok: true,
			want: Failure{Reason: FailureThrottled, StatusCode: 429, Message: "the API's message"}},
		{name: "request refused before it was sent", err: refused, ok: true,
			want: Failure{Reason: FailureInvalidInput, Message: `codelevelanalysis: unknown leaf type "nope": use total, service, or background`}},
		{name: "wrapped again by the caller", err: fmt.Errorf("profile: %w", api(sdkcla.PhasePoll, 404)), ok: true,
			want: Failure{Reason: FailureTaskExpired, StatusCode: 404, Message: "the API's message"}},
		// An API error that did not come through an analysis is not one of
		// its failures: a 404 from another API names no process.
		{name: "an API error from elsewhere", err: httpclient.NewAPIError(404, "the API's message", "")},
		{name: "a 429 from elsewhere", err: fmt.Errorf("list: %w", httpclient.NewAPIError(429, "the API's message", ""))},
		{name: "401 is left to generic handling", err: api(sdkcla.PhaseSubmit, 401)},
		{name: "409 is left to generic handling", err: api(sdkcla.PhaseSubmit, 409)},
		{name: "a transport error", err: &sdkcla.Error{Phase: sdkcla.PhaseSubmit, Err: errors.New("codelevelanalysis: submit: connection refused")}},
		{name: "a cancelled context", err: &sdkcla.Error{Phase: sdkcla.PhasePoll, Err: context.Canceled}},
		{name: "not an analysis error", err: errors.New("boom")},
		{name: "nil", err: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ClassifyError(tc.err)
			if ok != tc.ok || got != tc.want {
				t.Errorf("ClassifyError = %+v, %v\nwant            %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The failures a caller meets most, through the real request path: what the
// SDK refuses before sending, and what the API answers on the submit and on a
// poll.
func TestClassifyError_ThroughTheHandler(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/result"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"task gone"}}`)
		case strings.Contains(r.URL.Path, "/memoryallocation/"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"no such entity"}}`)
		default:
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"token":"t1"}`)
		}
	}))
	defer srv.Close()
	c, err := client.NewForTesting(srv.URL, "dt0c01.test")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(c)
	window := Window{From: 1000, To: 2000}
	ctx := context.Background()

	_, err = h.Hotspots(ctx, HotspotsRequest{Entity: "SERVICE-0000000000000001", Window: window})
	want := Failure{Reason: FailureInvalidInput, Message: `codelevelanalysis: entityId must be a PROCESS, PROCESS_GROUP, or PROCESS_GROUP_INSTANCE — services are not eligible; got "SERVICE-0000000000000001"`}
	if got, ok := ClassifyError(err); !ok || got != want {
		t.Errorf("an ineligible entity: %+v, %v; want %+v", got, ok, want)
	}
	_, err = h.Hotspots(ctx, HotspotsRequest{Entity: testEntity, Window: window, LeafType: "nope"})
	want = Failure{Reason: FailureInvalidInput, Message: `codelevelanalysis: unknown leaf type "nope": use total, service, or background`}
	if got, ok := ClassifyError(err); !ok || got != want {
		t.Errorf("an unknown leaf type: %+v, %v; want %+v", got, ok, want)
	}
	_, err = h.Allocations(ctx, AllocationsRequest{Entity: testEntity, Window: Window{From: 2000, To: 1000}})
	want = Failure{Reason: FailureInvalidInput, Message: "profiling: the window ends at or before its start"}
	if got, ok := ClassifyError(err); !ok || got != want {
		t.Errorf("an inverted window: %+v, %v; want %+v", got, ok, want)
	}
	_, err = h.Hotspots(ctx, HotspotsRequest{Entity: testEntity, Window: Window{From: 0, To: 6 * 3600 * 1000}})
	want = Failure{Reason: FailureInvalidInput, Message: "profiling: the window is 6h0m0s long, over the limit of 2h0m0s"}
	if got, ok := ClassifyError(err); !ok || got != want {
		t.Errorf("a window over the limit: %+v, %v; want %+v", got, ok, want)
	}
	if requests != 0 {
		t.Errorf("%d requests were sent for input the SDK refuses", requests)
	}

	_, err = h.Allocations(ctx, AllocationsRequest{Entity: testEntity, Window: window})
	want = Failure{Reason: FailureNotFound, StatusCode: 404, Message: "no such entity"}
	if got, ok := ClassifyError(err); !ok || got != want {
		t.Errorf("a 404 on the submit: %+v, %v; want %+v", got, ok, want)
	}
	_, err = h.Hotspots(ctx, HotspotsRequest{Entity: testEntity, Window: window})
	want = Failure{Reason: FailureTaskExpired, StatusCode: 404, Message: "task gone"}
	if got, ok := ClassifyError(err); !ok || got != want {
		t.Errorf("a 404 on a poll: %+v, %v; want %+v", got, ok, want)
	}
}
