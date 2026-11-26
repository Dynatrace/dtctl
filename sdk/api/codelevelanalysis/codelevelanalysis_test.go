package codelevelanalysis

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func TestEncodeLeafType(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{LeafTypeTotal, "", false},
		{LeafTypeBackground, "42\x110", false},
		{"ambient", "42\x110", false},
		{LeafTypeService, "42\x111", false},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		got, err := encodeLeafType(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("encodeLeafType(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("encodeLeafType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestLeafTypeWireEncoding pins the exact servicefilter query value the server
// expects: background -> 42%110, service -> 42%111, total -> omitted.
func TestLeafTypeWireEncoding(t *testing.T) {
	cases := []struct {
		leafType     string
		wantContains string // "" means servicefilter must be absent
	}{
		{LeafTypeTotal, ""},
		{LeafTypeBackground, "servicefilter=42%110"},
		{LeafTypeService, "servicefilter=42%111"},
	}
	for _, tc := range cases {
		p := Payload{Kind: "methodHotspots", EntityID: "PROCESS_GROUP_INSTANCE-ABC", From: 1, To: 2, LeafType: tc.leafType}
		path := buildSubmitPath(p)
		if tc.wantContains == "" {
			if strings.Contains(path, "servicefilter") {
				t.Errorf("leafType=%q: expected no servicefilter, got %q", tc.leafType, path)
			}
			continue
		}
		if !strings.Contains(path, tc.wantContains) {
			t.Errorf("leafType=%q: path %q does not contain %q", tc.leafType, path, tc.wantContains)
		}
	}
}

func TestValidateEntityType(t *testing.T) {
	base := Payload{Kind: "methodHotspots", From: 1, To: 2}
	cases := []struct {
		entity  string
		wantErr bool
	}{
		{"PROCESS_GROUP-ABC", false},
		{"PROCESS_GROUP_INSTANCE-ABC", false},
		{"PROCESS-ABC", false}, // 3rd-gen PGI rename
		{"SERVICE-ABC", true},
		{"HOST-ABC", true},
		{"", true},
	}
	for _, tc := range cases {
		p := base
		p.EntityID = tc.entity
		err := validate(p)
		if (err != nil) != tc.wantErr {
			t.Errorf("validate(entity=%q) err = %v, wantErr %v", tc.entity, err, tc.wantErr)
		}
	}
}

// TestNormalizeEntityID pins the 3rd-gen PROCESS- -> classic
// PROCESS_GROUP_INSTANCE- rewrite, and that other prefixes pass through.
func TestNormalizeEntityID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"PROCESS-A89D22C1D0B350C4", "PROCESS_GROUP_INSTANCE-A89D22C1D0B350C4"},
		{"PROCESS_GROUP_INSTANCE-ABC", "PROCESS_GROUP_INSTANCE-ABC"}, // already classic
		{"PROCESS_GROUP-ABC", "PROCESS_GROUP-ABC"},                   // group, not instance
		{"SERVICE-ABC", "SERVICE-ABC"},                               // untouched
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeEntityID(tc.in); got != tc.want {
			t.Errorf("NormalizeEntityID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSubmitPathRewritesProcessEntity confirms the outgoing request path carries
// the classic PGI id, never the 3rd-gen PROCESS- id the API 404s on.
func TestSubmitPathRewritesProcessEntity(t *testing.T) {
	p := Payload{Kind: "methodHotspots", EntityID: "PROCESS-A89D22C1D0B350C4", From: 1, To: 2}
	path := buildSubmitPath(p)
	if !strings.Contains(path, "PROCESS_GROUP_INSTANCE-A89D22C1D0B350C4") {
		t.Errorf("path %q does not carry the classic PGI id", path)
	}
	if strings.Contains(path, "/PROCESS-A89D22C1D0B350C4?") {
		t.Errorf("path %q still carries the 3rd-gen PROCESS- id", path)
	}
}

func TestValidateRejectsBadLeafType(t *testing.T) {
	p := Payload{Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2, LeafType: "nope"}
	if err := validate(p); err == nil {
		t.Fatalf("expected validate to reject unknown leaf type")
	}
}

// TestServiceFilterEscapeHatch confirms a raw ServiceFilter is used verbatim
// when LeafType is unset, and that LeafType wins when both are set.
func TestServiceFilterEscapeHatch(t *testing.T) {
	raw := Payload{Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2, ServiceFilter: "42\x110"}
	if got := buildSubmitPath(raw); !strings.Contains(got, "servicefilter=42%110") {
		t.Errorf("raw ServiceFilter not honored: %q", got)
	}
	both := Payload{Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2, ServiceFilter: "42\x110", LeafType: LeafTypeService}
	if got := buildSubmitPath(both); !strings.Contains(got, "servicefilter=42%111") {
		t.Errorf("LeafType should win over raw ServiceFilter: %q", got)
	}
}

// guard against url encoding drift.
func TestDC1EscapesToPercent11(t *testing.T) {
	v := url.Values{}
	v.Set("servicefilter", "42\x110")
	if got := v.Encode(); got != "servicefilter=42%110" {
		t.Fatalf("DC1 escape drifted: %q", got)
	}
}

// TestPollRoutesToSubmitServerID verifies the async flow pins every /result poll
// to the cluster node named by the submit's serverId header — a poll routed to a
// different node would not know the token.
func TestPollRoutesToSubmitServerID(t *testing.T) {
	var pollURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/methodhotspots/"):
			// Submit lands on node 7 and starts the async task.
			w.Header().Set("serverId", "7")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"token":"tok-1"}`))
		case strings.Contains(r.URL.Path, "/result"):
			pollURL = r.URL.String()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}

	resp, err := NewHandler(c).Run(context.Background(), Payload{
		Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("status = %q, want completed", resp.Status)
	}
	if !strings.Contains(pollURL, "serverId=7") {
		t.Errorf("poll URL %q missing serverId=7 from submit header", pollURL)
	}
	if !strings.Contains(pollURL, "token=tok-1") {
		t.Errorf("poll URL %q missing token", pollURL)
	}
}

// TestPollRetriesOn429 verifies a 429 during polling is retried rather than
// treated as a fatal error. Transport-level retry is disabled so the poll loop
// itself has to tolerate the rate-limit response.
func TestPollRetriesOn429(t *testing.T) {
	var resultCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/methodhotspots/"):
			w.Header().Set("serverId", "3")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"token":"tok-429"}`))
		case strings.Contains(r.URL.Path, "/result"):
			resultCalls++
			if resultCalls == 1 {
				w.WriteHeader(http.StatusTooManyRequests) // 429 on the first poll
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := httpclient.New(srv.URL,
		httpclient.WithToken("dt0c01.test"),
		httpclient.WithRetry(0, 0, 0), // isolate the poll loop from transport retry
	)
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}

	resp, err := NewHandler(c).Run(context.Background(), Payload{
		Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("status = %q, want completed", resp.Status)
	}
	if resultCalls != 2 {
		t.Errorf("expected the 429 poll to be retried (2 /result calls), got %d", resultCalls)
	}
}

// TestPollServerIDDefaultsWhenHeaderAbsent verifies a missing serverId header
// falls back to the -1 "no preference" sentinel rather than breaking the URL.
func TestPollServerIDDefaultsWhenHeaderAbsent(t *testing.T) {
	var pollURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/methodhotspots/"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"token":"tok-2"}`))
		case strings.Contains(r.URL.Path, "/result"):
			pollURL = r.URL.String()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}

	if _, err := NewHandler(c).Run(context.Background(), Payload{
		Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(pollURL, "serverId=-1") {
		t.Errorf("poll URL %q should default serverId to -1", pollURL)
	}
}

// TestSubmitWaitsOutThrottle verifies a submit the environment's profiling
// throttle turns away (429) is sent again after the server's Retry-After, and
// that the analysis then runs normally.
func TestSubmitWaitsOutThrottle(t *testing.T) {
	var submits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/methodhotspots/"):
			submits++
			if submits < 3 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, 0, 0))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	resp, err := NewHandler(c).Run(context.Background(), Payload{
		Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.Status != "completed" || submits != 3 {
		t.Errorf("status = %q after %d submits, want completed after 3", resp.Status, submits)
	}
}

// TestSubmitGivesUpOnPersistentThrottle verifies the submit stops after
// maxThrottledSubmits attempts and surfaces the 429 as an APIError.
func TestSubmitGivesUpOnPersistentThrottle(t *testing.T) {
	defer func(d time.Duration) { throttleBaseWait = d }(throttleBaseWait)
	throttleBaseWait = time.Millisecond

	var submits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submits++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, 0, 0))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	_, err = NewHandler(c).Run(context.Background(), Payload{
		Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2,
	})
	var apiErr *httpclient.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want an APIError with status 429", err)
	}
	if submits != maxThrottledSubmits {
		t.Errorf("submits = %d, want %d", submits, maxThrottledSubmits)
	}
}

// TestPollFailsFastOnTerminalStatus: only 202 and 429 are polled again. A
// terminal status (an expired task, a missing scope, a server error) is
// returned on the first poll as the API's own error, not polled out.
func TestPollFailsFastOnTerminalStatus(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var resultCalls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/methodhotspots/"):
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte(`{"token":"tok-terminal"}`))
				case strings.Contains(r.URL.Path, "/result"):
					resultCalls++
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(status) + `,"message":"task gone"}}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, 0, 0))
			if err != nil {
				t.Fatalf("httpclient.New: %v", err)
			}
			_, err = NewHandler(c).Run(context.Background(), Payload{
				Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2,
			})
			if err == nil {
				t.Fatal("Run: want an error")
			}
			if resultCalls != 1 {
				t.Errorf("polled %d times, want 1: a terminal status must not be retried", resultCalls)
			}
			var apiErr *httpclient.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status {
				t.Errorf("err = %v, want an APIError with status %d", err, status)
			}
			if !strings.Contains(err.Error(), "task gone") {
				t.Errorf("err = %v, want the API's message", err)
			}
		})
	}
}

// dropConn closes the connection without a response: a transport error.
func dropConn(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("response writer cannot hijack")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatalf("hijack: %v", err)
	}
	_ = conn.Close()
}

// TestPollRidesOutTransportErrors: a poll that gets no response at all is
// polled again (the GET is safe, the task keeps running); persistent failure
// gives up after maxPollTransportErrors in a row.
func TestPollRidesOutTransportErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		drops   int32 // requests answered by a dropped connection before a 200
		wantErr bool
	}{
		{"one drop recovers", 1, false},
		{"persistent drops give up", 1000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/methodhotspots/"):
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte(`{"token":"tok-drop"}`))
				case strings.Contains(r.URL.Path, "/result"):
					if hits.Add(1) <= tc.drops {
						dropConn(t, w)
						return
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"ok":true}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, 0, 0))
			if err != nil {
				t.Fatalf("httpclient.New: %v", err)
			}
			resp, err := NewHandler(c).Run(context.Background(), Payload{
				Kind: "methodHotspots", EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2,
			})
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && resp.Status != "completed" {
				t.Errorf("status = %q, want completed", resp.Status)
			}
			// Go's transport may resend a GET once on a dropped reused
			// connection, so the server can see up to two hits per poll.
			if n := hits.Load(); tc.wantErr && (n < maxPollTransportErrors || n > 2*maxPollTransportErrors) {
				t.Errorf("server saw %d requests, want %d polls' worth: give up after %d in a row", n, maxPollTransportErrors, maxPollTransportErrors)
			}
		})
	}
}

// A kind the API does not have is refused before any request is sent. It used
// to pass validation and panic while the request path was built.
func TestRunRejectsUnknownKind(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	for _, kind := range []Kind{"", "hotspots", "methodhotspots"} {
		_, err := NewHandler(c).Run(context.Background(), Payload{Kind: kind, EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2})
		want := `codelevelanalysis: unknown kind "` + string(kind) + `": use methodHotspots, threadAnalysis, memoryAllocation, or memoryAllocationDetails`
		if err == nil || err.Error() != want {
			t.Errorf("Run(kind=%q) err = %v, want %q", kind, err, want)
		}
	}
	if requests != 0 {
		t.Errorf("sent %d requests, want none", requests)
	}
}

// Every kind constant names its endpoint.
func TestKindsBuildTheirPaths(t *testing.T) {
	cases := map[Kind]string{
		KindHotspots:          "/methodhotspots/PROCESS_GROUP-ABC?",
		KindThreads:           "/threadanalysis/PROCESS_GROUP-ABC?",
		KindAllocations:       "/memoryallocation/PROCESS_GROUP-ABC?",
		KindAllocationDetails: "/memoryallocationdetails/PROCESS_GROUP-ABC?",
	}
	for kind, want := range cases {
		p := Payload{Kind: kind, EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2, Type: "t", Method: "m"}
		if err := validate(p); err != nil {
			t.Errorf("validate(kind=%q): %v", kind, err)
		}
		if path := buildSubmitPath(p); !strings.Contains(path, want) {
			t.Errorf("kind %q: path %q does not contain %q", kind, path, want)
		}
	}
}

// phaseServer answers the submit with submitStatus and every poll with
// pollStatus.
func phaseServer(t *testing.T, submitStatus, pollStatus int) *Handler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := submitStatus
		if strings.Contains(r.URL.Path, "/result") {
			status = pollStatus
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		switch {
		case status == http.StatusAccepted:
			_, _ = w.Write([]byte(`{"token":"tok-phase"}`))
		case status >= 400:
			_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(status) + `,"message":"refused"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, 0, 0))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return NewHandler(c)
}

// Run says in which phase an analysis failed. The same status means different
// things in each: a 404 on the submit is an unknown entity, on a poll an
// expired or unknown task.
func TestRunErrorNamesThePhase(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })

	ok := Payload{Kind: KindHotspots, EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2}
	service := ok
	service.EntityID = "SERVICE-ABC"
	cases := []struct {
		name                     string
		payload                  Payload
		submitStatus, pollStatus int
		phase                    Phase
		apiStatus                int // 0: not an API error
		notCompleted             bool
		text                     string
	}{
		{name: "payload refused", payload: service, phase: PhaseValidate,
			text: `codelevelanalysis: entityId must be a PROCESS, PROCESS_GROUP, or PROCESS_GROUP_INSTANCE — services are not eligible; got "SERVICE-ABC"`},
		{name: "submit refused", payload: ok, submitStatus: 404, phase: PhaseSubmit, apiStatus: 404},
		{name: "poll refused", payload: ok, submitStatus: 202, pollStatus: 404, phase: PhasePoll, apiStatus: 404},
		{name: "never completes", payload: ok, submitStatus: 202, pollStatus: 202, phase: PhasePoll, notCompleted: true,
			text: "codelevelanalysis: analysis did not complete within 0s (token tok-phase)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := phaseServer(t, tc.submitStatus, tc.pollStatus).Run(context.Background(), tc.payload)
			var runErr *Error
			if !errors.As(err, &runErr) {
				t.Fatalf("err = %v (%T), want an *Error", err, err)
			}
			if runErr.Phase != tc.phase {
				t.Errorf("Phase = %q, want %q", runErr.Phase, tc.phase)
			}
			var apiErr *httpclient.APIError
			if errors.As(err, &apiErr) != (tc.apiStatus != 0) || (apiErr != nil && apiErr.StatusCode != tc.apiStatus) {
				t.Errorf("API error = %v, want status %d", apiErr, tc.apiStatus)
			}
			if got := errors.Is(err, ErrNotCompleted); got != tc.notCompleted {
				t.Errorf("errors.Is(err, ErrNotCompleted) = %v, want %v", got, tc.notCompleted)
			}
			if tc.text != "" && err.Error() != tc.text {
				t.Errorf("err = %q, want %q", err, tc.text)
			}
		})
	}
}

func TestEligibleEntity(t *testing.T) {
	cases := map[string]bool{
		"PROCESS-ABC":                true,
		"PROCESS_GROUP-ABC":          true,
		"PROCESS_GROUP_INSTANCE-ABC": true,
		"SERVICE-ABC":                false,
		"HOST-ABC":                   false,
		"PROCESS":                    false,
		"process-abc":                false,
		"":                           false,
	}
	for id, want := range cases {
		if got := EligibleEntity(id); got != want {
			t.Errorf("EligibleEntity(%q) = %v, want %v", id, got, want)
		}
	}
}

// The length check must hold for bounds so far apart that their difference
// does not fit an int64: it used to wrap around and pass.
func TestValidateTimeframeLength(t *testing.T) {
	const day = int64(24 * 60 * 60 * 1000)
	cases := []struct {
		from, to int64
		wantErr  string
	}{
		{1, 1 + day, ""},
		{1, 2 + day, "codelevelanalysis: timeframe must not exceed 24 hours"},
		{math.MinInt64, 1_700_000_000_000, "codelevelanalysis: timeframe must not exceed 24 hours"},
		{-1, math.MaxInt64, "codelevelanalysis: timeframe must not exceed 24 hours"},
		{2, 1, "codelevelanalysis: from/to must be epoch-millis with to > from"},
	}
	for _, tc := range cases {
		err := validate(Payload{Kind: KindHotspots, EntityID: "PROCESS_GROUP-ABC", From: tc.from, To: tc.to})
		if got := fmt.Sprint(err); (tc.wantErr == "" && err != nil) || (tc.wantErr != "" && got != tc.wantErr) {
			t.Errorf("validate(from=%d, to=%d) = %v, want %q", tc.from, tc.to, err, tc.wantErr)
		}
	}
}

// A Retry-After too large for a Duration must wait the cap, not wrap around to
// a negative wait that sends the next submit at once.
func TestThrottleWait(t *testing.T) {
	defer func(d time.Duration) { throttleBaseWait = d }(throttleBaseWait)
	throttleBaseWait = 5 * time.Second

	cases := []struct {
		retryAfter string
		attempt    int
		want       time.Duration
	}{
		{"", 1, 5 * time.Second},
		{"", 2, 10 * time.Second},
		{"", 9, 60 * time.Second},
		{"0", 1, 0},
		{"7", 3, 7 * time.Second},
		{"61", 1, 60 * time.Second},
		{"10000000000", 1, 60 * time.Second},
		{"9223372036854775807", 1, 60 * time.Second},
		{"-1", 1, 5 * time.Second},
		{"soon", 2, 10 * time.Second},
	}
	for _, tc := range cases {
		raw := &http.Response{Header: http.Header{}}
		if tc.retryAfter != "" {
			raw.Header.Set("Retry-After", tc.retryAfter)
		}
		if got := throttleWait(&resty.Response{RawResponse: raw}, tc.attempt); got != tc.want {
			t.Errorf("throttleWait(Retry-After %q, attempt %d) = %s, want %s", tc.retryAfter, tc.attempt, got, tc.want)
		}
	}
}

// The submit answers 200 with the result, 202 with a token and 204 when there
// is nothing to analyse. Any other status that is not a failure is not one of
// the API's answers, and must not be read as a 202.
func TestSubmitStatus(t *testing.T) {
	ok := Payload{Kind: KindHotspots, EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2}

	resp, err := phaseServer(t, http.StatusNoContent, 0).Run(context.Background(), ok)
	if err != nil {
		t.Fatalf("submit 204: %v", err)
	}
	if resp.Status != "no-data" || resp.PollCount != 0 || resp.Kind != KindHotspots || resp.EntityID != "PROCESS_GROUP-ABC" || resp.Result != nil {
		t.Errorf("submit 204 = %+v, want no-data for the payload's kind and entity, unpolled, without a result", *resp)
	}

	_, err = phaseServer(t, http.StatusNonAuthoritativeInfo, 0).Run(context.Background(), ok)
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Phase != PhaseSubmit {
		t.Fatalf("submit 203: err = %v, want an *Error of the submit phase", err)
	}
	if want := "codelevelanalysis: submit: unexpected status 203"; err.Error() != want {
		t.Errorf("submit 203: err = %q, want %q", err, want)
	}
}

// pollHandler starts an analysis and answers the n-th poll with answer(n).
func pollHandler(t *testing.T, answer func(w http.ResponseWriter, n int)) *Handler {
	t.Helper()
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })

	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/result") {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"token":"tok-poll"}`))
			return
		}
		polls++
		answer(w, polls)
	}))
	t.Cleanup(srv.Close)
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, 0, 0))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return NewHandler(c)
}

// When the poll budget ends, the error says what the last poll met. Only an
// analysis that was still running (202) did not complete; a throttled last
// poll is the API's 429 and a dropped one a transport failure.
func TestPollBudgetEndsOnTheLastAnswer(t *testing.T) {
	ok := Payload{Kind: KindHotspots, EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2}

	t.Run("throttled", func(t *testing.T) {
		_, err := pollHandler(t, func(w http.ResponseWriter, _ int) {
			w.WriteHeader(http.StatusTooManyRequests)
		}).Run(context.Background(), ok)
		var apiErr *httpclient.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("err = %v, want an APIError with status 429", err)
		}
		if errors.Is(err, ErrNotCompleted) {
			t.Errorf("err = %v matches ErrNotCompleted; the analysis was never seen running", err)
		}
		var runErr *Error
		if !errors.As(err, &runErr) || runErr.Phase != PhasePoll {
			t.Errorf("err = %v, want an *Error of the poll phase", err)
		}
	})

	t.Run("connection lost", func(t *testing.T) {
		_, err := pollHandler(t, func(w http.ResponseWriter, n int) {
			if n < maxPollAttempts {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			dropConn(t, w)
		}).Run(context.Background(), ok)
		if err == nil || errors.Is(err, ErrNotCompleted) {
			t.Fatalf("err = %v, want the transport failure of the last poll", err)
		}
		if want := "codelevelanalysis: poll failed on the last of 40 attempts (token tok-poll): "; !strings.HasPrefix(err.Error(), want) {
			t.Errorf("err = %q, want it to start with %q", err, want)
		}
	})

	t.Run("still running", func(t *testing.T) {
		_, err := pollHandler(t, func(w http.ResponseWriter, _ int) {
			w.WriteHeader(http.StatusAccepted)
		}).Run(context.Background(), ok)
		if !errors.Is(err, ErrNotCompleted) {
			t.Fatalf("err = %v, want ErrNotCompleted", err)
		}
		// ErrNotCompleted is wrapped, not imitated: Unwrap reaches it.
		var runErr *Error
		if !errors.As(err, &runErr) || errors.Unwrap(runErr.Err) != ErrNotCompleted {
			t.Errorf("errors.Unwrap(%v) is not ErrNotCompleted", err)
		}
		if want := "codelevelanalysis: analysis did not complete within 0s (token tok-poll)"; err.Error() != want {
			t.Errorf("err = %q, want %q", err, want)
		}
	})
}

// LeafType decides the servicefilter whenever it is set, also when it selects
// the total, which is sent as no filter at all.
func TestLeafTypeTotalOverridesRawServiceFilter(t *testing.T) {
	p := Payload{Kind: KindHotspots, EntityID: "PROCESS_GROUP-ABC", From: 1, To: 2, ServiceFilter: "42\x110", LeafType: LeafTypeTotal}
	if got, want := buildSubmitPath(p), basePath+"/methodhotspots/PROCESS_GROUP-ABC?from=1&to=2"; got != want {
		t.Errorf("buildSubmitPath = %q, want %q", got, want)
	}
}
