package appengine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// newActionServer serves handler and returns a Handler pointed at it.
func newActionServer(t *testing.T, handler http.HandlerFunc) *Handler {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, time.Millisecond, time.Millisecond))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return NewHandler(c)
}

// newActionBodyServer serves body for every request and records the last one.
func newActionBodyServer(t *testing.T, status int, body string, got **http.Request) *Handler {
	t.Helper()
	return newActionServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			*got = r
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func TestHandler_GetAction(t *testing.T) {
	const body = `{"app":{"id":"acme.app"},"action":{
		"name":"send-report","title":"Send report","description":"Sends it","stateful":true,
		"widget":"/actions/send-report"}}`

	t.Run("found", func(t *testing.T) {
		var r *http.Request
		h := newActionBodyServer(t, 200, body, &r)
		action, err := h.GetAction(context.Background(), "acme.app:send-report")
		if err != nil {
			t.Fatalf("GetAction: %v", err)
		}
		if r.URL.Path != "/platform/app-engine/registry/v1/actions/acme.app/send-report" {
			t.Errorf("requested %q", r.URL.Path)
		}
		if action.AppID != "acme.app" || action.ActionName != "send-report" || action.Title != "Send report" ||
			action.Description != "Sends it" || !action.Stateful {
			t.Errorf("unexpected action: %+v", action)
		}
		if action.FullName != "acme.app:send-report" {
			t.Errorf("FullName = %q", action.FullName)
		}
		if action.Extra["widget"] != "/actions/send-report" {
			t.Errorf("Extra did not retain the unmodelled key: %+v", action.Extra)
		}
		if _, repeated := action.Extra["title"]; repeated {
			t.Errorf("Extra repeats a modelled key: %+v", action.Extra)
		}
	})

	t.Run("path values are escaped", func(t *testing.T) {
		var r *http.Request
		h := newActionBodyServer(t, 200, body, &r)
		if _, err := h.GetAction(context.Background(), "acme.app:send:poll?x"); err != nil {
			t.Fatalf("GetAction: %v", err)
		}
		if got := r.URL.EscapedPath(); !strings.HasSuffix(got, "/actions/acme.app/send%3Apoll%3Fx") {
			t.Errorf("escaped path = %q", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		h := newActionBodyServer(t, 404, `{"error":{"code":404,"message":"no such action"}}`, nil)
		_, err := h.GetAction(context.Background(), "acme.app:missing")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), `get action "acme.app:missing"`) {
			t.Errorf("error = %v", err)
		}
	})

	t.Run("response without a name", func(t *testing.T) {
		h := newActionBodyServer(t, 200, `{"app":{"id":"acme.app"},"action":{"title":"x"}}`, nil)
		if _, err := h.GetAction(context.Background(), "acme.app:x"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed name is rejected before any request", func(t *testing.T) {
		var r *http.Request
		h := newActionBodyServer(t, 200, body, &r)
		_, err := h.GetAction(context.Background(), "acme.app")
		if err == nil || !strings.Contains(err.Error(), "expected 'app-id:action-name'") {
			t.Fatalf("error = %v", err)
		}
		if r != nil {
			t.Error("request sent for a malformed name")
		}
	})
}

func TestHandler_ListActions(t *testing.T) {
	// Out of order on purpose, with one nameless entry. The widget key must
	// not reach the result: a list never carries Extra.
	const body = `{"actions":[
		{"app":{"id":"other.app"},"action":{"name":"b1","widget":"/w/b1"}},
		{"app":{"id":"acme.app"},"action":{"name":"a2","title":"A2","description":"Second","stateful":true}},
		{"app":{"id":"acme.app"},"action":{"name":"a1"}},
		{"app":{"id":"acme.app"},"action":{"title":"nameless"}}
	]}`

	t.Run("sorted by full name, nameless entries skipped", func(t *testing.T) {
		actions, err := newActionBodyServer(t, 200, body, nil).ListActions(context.Background(), ListActionsOptions{})
		if err != nil {
			t.Fatalf("ListActions: %v", err)
		}
		var got []string
		for _, a := range actions {
			got = append(got, a.FullName)
		}
		want := []string{"acme.app:a1", "acme.app:a2", "other.app:b1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("modelled fields are mapped", func(t *testing.T) {
		actions, err := newActionBodyServer(t, 200, body, nil).ListActions(context.Background(), ListActionsOptions{AppID: "acme.app"})
		if err != nil {
			t.Fatalf("ListActions: %v", err)
		}
		if len(actions) != 2 {
			t.Fatalf("got %d actions, want 2: %+v", len(actions), actions)
		}
		a2 := actions[1]
		if a2.ActionName != "a2" || a2.Title != "A2" || a2.Description != "Second" || !a2.Stateful || a2.AppID != "acme.app" {
			t.Errorf("unexpected action: %+v", a2)
		}
	})

	t.Run("a list never carries Extra", func(t *testing.T) {
		actions, err := newActionBodyServer(t, 200, body, nil).ListActions(context.Background(), ListActionsOptions{})
		if err != nil {
			t.Fatalf("ListActions: %v", err)
		}
		for _, a := range actions {
			if a.Extra != nil {
				t.Errorf("%s carries Extra %+v", a.FullName, a.Extra)
			}
		}
	})

	t.Run("app id matching nothing", func(t *testing.T) {
		actions, err := newActionBodyServer(t, 200, body, nil).ListActions(context.Background(), ListActionsOptions{AppID: "nope.app"})
		if err != nil {
			t.Fatalf("ListActions: %v", err)
		}
		if len(actions) != 0 {
			t.Fatalf("got %d actions, want 0", len(actions))
		}
	})

	t.Run("requests GET /actions with stateful added", func(t *testing.T) {
		var r *http.Request
		if _, err := newActionBodyServer(t, 200, `{"actions":[]}`, &r).ListActions(context.Background(), ListActionsOptions{}); err != nil {
			t.Fatalf("ListActions: %v", err)
		}
		if r.URL.Path != "/platform/app-engine/registry/v1/actions" {
			t.Errorf("requested %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("add-fields"); got != "action.stateful" {
			t.Errorf("add-fields = %q", got)
		}
	})

	t.Run("server error is surfaced", func(t *testing.T) {
		_, err := newActionBodyServer(t, 400, `{"error":{"code":400,"message":"bad"}}`, nil).ListActions(context.Background(), ListActionsOptions{})
		if err == nil || !strings.Contains(err.Error(), "list actions") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestHandler_ListActions_QueryAndFilter(t *testing.T) {
	const filter = "not (action.docs.inputSchema is-empty)"

	t.Run("sent upstream", func(t *testing.T) {
		var r *http.Request
		opts := ListActionsOptions{Query: "jira create", Filter: filter}
		if _, err := newActionBodyServer(t, 200, `{"actions":[]}`, &r).ListActions(context.Background(), opts); err != nil {
			t.Fatalf("ListActions: %v", err)
		}
		if got := r.URL.Query().Get("search"); got != "jira create" {
			t.Errorf("search = %q", got)
		}
		if got := r.URL.Query().Get("filter"); got != filter {
			t.Errorf("filter = %q", got)
		}
	})

	t.Run("omitted when empty", func(t *testing.T) {
		var r *http.Request
		if _, err := newActionBodyServer(t, 200, `{"actions":[]}`, &r).ListActions(context.Background(), ListActionsOptions{}); err != nil {
			t.Fatalf("ListActions: %v", err)
		}
		for _, param := range []string{"search", "filter"} {
			if _, present := r.URL.Query()[param]; present {
				t.Errorf("%s sent although empty", param)
			}
		}
	})

	t.Run("app id is not sent upstream", func(t *testing.T) {
		var r *http.Request
		if _, err := newActionBodyServer(t, 200, `{"actions":[]}`, &r).ListActions(context.Background(), ListActionsOptions{AppID: "acme.app"}); err != nil {
			t.Fatalf("ListActions: %v", err)
		}
		if _, present := r.URL.Query()["filter"]; present {
			t.Error("app id leaked into the filter param")
		}
	})

	t.Run("over the length limits is refused before any request", func(t *testing.T) {
		cases := map[string]struct {
			opts ListActionsOptions
			want string
		}{
			"search": {ListActionsOptions{Query: strings.Repeat("x", MaxActionQueryLength+1)}, "Query is 257 characters, the maximum is 256"},
			"filter": {ListActionsOptions{Filter: strings.Repeat("x", MaxActionFilterLength+1)}, "Filter is 257 characters, the maximum is 256"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				var r *http.Request
				_, err := newActionBodyServer(t, 200, `{"actions":[]}`, &r).ListActions(context.Background(), tc.opts)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v", err)
				}
				if r != nil {
					t.Error("request sent despite an over-long value")
				}
			})
		}
	})
}

func TestHandler_GetAction_KeepsNumbersExact(t *testing.T) {
	// Above 2^53 and with more digits than a float64 holds: both would be
	// rounded if the attributes were decoded into float64.
	const body = `{"app":{"id":"acme.app"},"action":{"name":"n",
		"big":9007199254740993,"dec":0.1000000000000000055511151231257827}}`

	action, err := newActionBodyServer(t, 200, body, nil).GetAction(context.Background(), "acme.app:n")
	if err != nil {
		t.Fatalf("GetAction: %v", err)
	}
	got, err := json.Marshal(action.Extra)
	if err != nil {
		t.Fatalf("marshal Extra: %v", err)
	}
	if want := `{"big":9007199254740993,"dec":0.1000000000000000055511151231257827}`; string(got) != want {
		t.Errorf("Extra = %s, want %s", got, want)
	}
}

func TestHandler_ListActions_LimitsCountCharacters(t *testing.T) {
	// 256 characters but 768 bytes: within the limit the API documents in
	// characters, so it must be sent, not refused.
	var r *http.Request
	query := strings.Repeat("日", MaxActionQueryLength)
	if _, err := newActionBodyServer(t, 200, `{"actions":[]}`, &r).ListActions(context.Background(), ListActionsOptions{Query: query}); err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if r == nil || r.URL.Query().Get("search") != query {
		t.Fatal("the query was not sent")
	}
}

func TestParseActionID(t *testing.T) {
	cases := []struct {
		id, app, name, canonical, err string
	}{
		{id: "dynatrace.automations:http-function", app: "dynatrace.automations", name: "http-function"},
		// An app id contains neither a colon nor a slash, so everything after
		// the first of either is the action name, colons and slashes included.
		{id: "acme.app:send:poll", app: "acme.app", name: "send:poll"},
		{id: "acme.app:a/b", app: "acme.app", name: "a/b"},
		// The slash form is accepted and formats back to the colon form.
		{id: "dynatrace.automations/http-function", app: "dynatrace.automations", name: "http-function", canonical: "dynatrace.automations:http-function"},
		{id: "acme.app/a:b", app: "acme.app", name: "a:b", canonical: "acme.app:a:b"},
		{id: "acme.app/a/b", app: "acme.app", name: "a/b", canonical: "acme.app:a/b"},
		{id: "acme.app", err: "expected 'app-id:action-name'"},
		{id: ":name", err: "expected 'app-id:action-name'"},
		{id: "/name", err: "expected 'app-id:action-name'"},
		{id: "acme.app:", err: "expected 'app-id:action-name'"},
		{id: "acme.app/", err: "expected 'app-id:action-name'"},
		{id: "", err: "expected 'app-id:action-name'"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			app, name, err := ParseActionID(tc.id)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.err)
				}
				return
			}
			if err != nil || app != tc.app || name != tc.name {
				t.Fatalf("got (%q, %q, %v), want (%q, %q)", app, name, err, tc.app, tc.name)
			}
			want := tc.id
			if tc.canonical != "" {
				want = tc.canonical
			}
			if got := FormatActionID(app, name); got != want {
				t.Errorf("FormatActionID = %q, want %q", got, want)
			}
		})
	}
}

func TestHandler_GetAction_AcceptsSlashForm(t *testing.T) {
	var r *http.Request
	h := newActionBodyServer(t, 200, `{"app":{"id":"acme.app"},"action":{"name":"n"}}`, &r)
	action, err := h.GetAction(context.Background(), "acme.app/n")
	if err != nil {
		t.Fatalf("GetAction: %v", err)
	}
	if r == nil || r.URL.Path != "/platform/app-engine/registry/v1/actions/acme.app/n" {
		t.Fatalf("request = %v", r)
	}
	if action.FullName != "acme.app:n" {
		t.Errorf("FullName = %q, want the colon form", action.FullName)
	}
}
