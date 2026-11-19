package codelevelanalysis

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
		if got := normalizeEntityID(tc.in); got != tc.want {
			t.Errorf("normalizeEntityID(%q) = %q, want %q", tc.in, got, tc.want)
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
