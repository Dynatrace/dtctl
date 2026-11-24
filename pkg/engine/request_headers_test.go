package engine_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/cmd"
	"github.com/Dynatrace/dtctl/pkg/engine"
)

// seenHeaders is what a stand-in query API saw, keyed by the DQL each request
// ran, so overlapping requests can be told apart.
type seenHeaders struct {
	mu       sync.Mutex
	requests int
	byQuery  map[string][]http.Header
}

func (s *seenHeaders) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func newQueryEnv(t *testing.T) (*httptest.Server, *seenHeaders) {
	t.Helper()
	seen := &seenHeaders{byQuery: map[string][]http.Header{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.mu.Lock()
		seen.requests++
		seen.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/platform/storage/query/v1/query:execute" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			return
		}
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen.mu.Lock()
		seen.byQuery[body.Query] = append(seen.byQuery[body.Query], r.Header.Clone())
		seen.mu.Unlock()
		_, _ = w.Write([]byte(`{"state":"SUCCEEDED","result":{"records":[{"marker":"headers-ok"}]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

// TestRequestHeaders_ReachTheQueryAPI: a request's Headers arrive on the
// platform request its query sends, next to dtctl's own headers, which are
// unchanged.
func TestRequestHeaders_ReachTheQueryAPI(t *testing.T) {
	srv, seen := newQueryEnv(t)

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        `query "fetch logs" --plain -o json`,
		EnvironmentURL: srv.URL,
		Token:          "t",
		Headers: http.Header{
			"Dt-Request-Origin": {"origin-1"},
			// Not canonical: it is sent all the same.
			"dt-caller-hint": {"hint-1"},
		},
	})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
	require.Contains(t, string(res.Stdout), "headers-ok")

	seen.mu.Lock()
	defer seen.mu.Unlock()
	require.Len(t, seen.byQuery["fetch logs"], 1)
	got := seen.byQuery["fetch logs"][0]
	require.Equal(t, "origin-1", got.Get("Dt-Request-Origin"))
	require.Equal(t, "hint-1", got.Get("Dt-Caller-Hint"))
	require.Equal(t, "Bearer t", got.Get("Authorization"))
	require.True(t, strings.HasPrefix(got.Get("User-Agent"), "dtctl/"), "User-Agent: %s", got.Get("User-Agent"))
	require.Contains(t, got.Get("Dt-Client-Context"), `"app":"dtctl"`)
}

// TestRequestHeaders_StayWithTheirRequest: overlapping invocations on one
// concurrent Engine each send their own request's headers, and a request
// without Headers sends none of another's.
func TestRequestHeaders_StayWithTheirRequest(t *testing.T) {
	srv, seen := newQueryEnv(t)
	eng := engine.New(engine.DefaultLimits(), engine.WithConcurrentExecution(3))

	origins := map[string]string{
		"fetch logs":   "origin-a",
		"fetch spans":  "origin-b",
		"fetch events": "",
	}
	var wg sync.WaitGroup
	for query, origin := range origins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := engine.Request{
				Argv:           []string{"query", query, "--plain", "-o", "json"},
				EnvironmentURL: srv.URL,
				Token:          "t",
			}
			if origin != "" {
				req.Headers = http.Header{"Dt-Request-Origin": {origin}}
			}
			res, err := eng.Execute(context.Background(), req)
			require.NoError(t, err)
			require.Zero(t, res.ExitCode, "%s: %s", query, res.Stderr)
		}()
	}
	wg.Wait()

	seen.mu.Lock()
	defer seen.mu.Unlock()
	for query, origin := range origins {
		require.Len(t, seen.byQuery[query], 1, query)
		require.Equal(t, origin, seen.byQuery[query][0].Get("Dt-Request-Origin"), query)
	}
}

// TestRequestHeaders_Refused: a header dtctl sets itself, one the session
// already decides, and one that is not valid on the wire each fail the request
// before anything is sent.
func TestRequestHeaders_Refused(t *testing.T) {
	srv, seen := newQueryEnv(t)

	for name, headers := range map[string]http.Header{
		"user agent":            {"User-Agent": {"other"}},
		"content type":          {"content-type": {"text/plain"}},
		"client context":        {"Dt-Client-Context": {"{}"}},
		"client context folded": {"dt_client_context": {"{}"}},
		"trace context":         {"Traceparent": {"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}},
		"authorization":         {"Authorization": {"Bearer other"}},
		"proxy authorization":   {"proxy-authorization": {"Basic eA=="}},
		"host":                  {"Host": {"other.example.invalid"}},
		"invalid name":          {"Dt Origin": {"x"}},
		"invalid value":         {"Dt-Origin": {"a\r\nX-Injected: b"}},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := engine.Execute(context.Background(), engine.Request{
				Command:        `query "fetch logs"`,
				EnvironmentURL: srv.URL,
				Token:          "t",
				Headers:        headers,
			})
			require.Error(t, err)
			require.Nil(t, res, "a refused header must stop the request before it runs")
		})
	}
	require.Zero(t, seen.count(), "no refused request may reach the environment")
}

// TestRequestHeaders_HostPolicy: the Engine's WithHeaderPolicy sees each
// name of a request's Headers, normalized as for `exec api -H`, and a refusal
// is a *cmd.HeaderRefusedError that fails the request before it runs.
func TestRequestHeaders_HostPolicy(t *testing.T) {
	srv, seen := newQueryEnv(t)
	var (
		mu         sync.Mutex
		policySeen []string
	)
	eng := engine.New(engine.DefaultLimits(), engine.WithHeaderPolicy(func(name string) string {
		mu.Lock()
		policySeen = append(policySeen, name)
		mu.Unlock()
		if name == "X-Forwarded-For" {
			return "the proxy in front of the environment sets it"
		}
		return ""
	}))

	res, err := eng.Execute(context.Background(), engine.Request{
		Command:        `query "fetch logs"`,
		EnvironmentURL: srv.URL,
		Token:          "t",
		Headers:        http.Header{"x_forwarded_for": {"203.0.113.7"}},
	})
	require.Nil(t, res)
	var refused *cmd.HeaderRefusedError
	require.ErrorAs(t, err, &refused)
	require.True(t, refused.Session)
	require.Equal(t, "the proxy in front of the environment sets it", refused.Reason)
	require.Zero(t, seen.count())

	res, err = eng.Execute(context.Background(), engine.Request{
		Command:        `query "fetch logs" --plain -o json`,
		EnvironmentURL: srv.URL,
		Token:          "t",
		Headers:        http.Header{"Dt-Request-Origin": {"origin-1"}},
	})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)

	// The engine checks a request before the run and the session checks it
	// again inside, so a name may be asked about more than once.
	mu.Lock()
	defer mu.Unlock()
	require.ElementsMatch(t, []string{"X-Forwarded-For", "Dt-Request-Origin"}, slices.Compact(slices.Sorted(slices.Values(policySeen))))
}

// TestRequestHeaders_ExecAPIMayNotRepeatThem: an `exec api -H` naming a header
// the request's Headers already send is refused with header_refused before
// any request is sent, whatever its spelling.
func TestRequestHeaders_ExecAPIMayNotRepeatThem(t *testing.T) {
	srv, seen := newQueryEnv(t)

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        `exec api /platform/widget/v1/widgets -H "dt_request_origin: other" --agent`,
		EnvironmentURL: srv.URL,
		Token:          "t",
		Headers:        http.Header{"Dt-Request-Origin": {"origin-1"}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, res.ExitCode, "stdout: %s\nstderr: %s", res.Stdout, res.Stderr)
	require.Contains(t, string(res.Stdout), `"code":"header_refused"`)
	require.Zero(t, seen.count())
}
