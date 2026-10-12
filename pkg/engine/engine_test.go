package engine_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/engine"
)

// mockEnv is a fake Dynatrace environment recording the Authorization header
// of every request and how many mutating requests arrived.
type mockEnv struct {
	*httptest.Server
	mu        sync.Mutex
	authSeen  []string
	mutations int
}

func newMockEnv(t *testing.T) *mockEnv {
	t.Helper()
	return newMockEnvWithConnState(t, nil)
}

// newMockEnvWithConnState is newMockEnv with a hook observing the server's
// connection states. It must be installed before Start: httptest wraps
// Config.ConnState there to track connections for Close.
func newMockEnvWithConnState(t *testing.T, connState func(net.Conn, http.ConnState)) *mockEnv {
	t.Helper()
	m := &mockEnv{}
	m.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.authSeen = append(m.authSeen, r.Header.Get("Authorization"))
		if r.Method != http.MethodGet {
			m.mutations++
		}
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/platform/storage/management/v1/bucket-definitions" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"buckets":[{"bucketName":"engine_bucket","table":"logs","status":"active","retentionDays":35,"version":1,"updatable":true}]}`))
		case r.URL.Path == "/platform/storage/management/v1/bucket-definitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"bucketName":"new_bucket","table":"logs","status":"creating","retentionDays":35,"version":1}`))
		case r.URL.Path == "/platform/automation/v1/workflows" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"wf-generated-1","title":"engine-created"}`))
		case r.URL.Path == "/platform/openpipeline/v1/matcher/lqlToDql" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"query":"matchesValue(log.source, \"stdin-test\")"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
		}
	}))
	m.Config.ConnState = connState
	m.Start()
	t.Cleanup(m.Server.Close)
	return m
}

func TestExecute_RequestValidation(t *testing.T) {
	for name, req := range map[string]engine.Request{
		"no command":       {EnvironmentURL: "https://x.example.invalid", Token: "t"},
		"unparsable quote": {Command: `create bucket -f "unclosed`, EnvironmentURL: "https://x.example.invalid", Token: "t"},
		"missing url":      {Command: "get buckets", Token: "t"},
		"missing token":    {Command: "get buckets", EnvironmentURL: "https://x.example.invalid"},
		"bad safety level": {Command: "get buckets", EnvironmentURL: "https://x.example.invalid", Token: "t", SafetyLevel: "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := engine.Execute(context.Background(), req)
			require.Error(t, err, "malformed requests must fail before running")
			require.Nil(t, res)
		})
	}
}

// TestExecute_CommandString is the core service scenario: a command string
// exactly as typed locally, executed against the request's tenant — while the
// host process carries hostile credentials and its own AI-agent environment,
// none of which may shape the request.
func TestExecute_CommandString(t *testing.T) {
	env := newMockEnv(t)
	t.Setenv("DTCTL_TOKEN", "host-secret")
	t.Setenv("DTCTL_CONFIG", "/nonexistent/host/config.yaml")
	// Deliberately NOT scrubbing AI-agent env vars (CLAUDECODE, AI_AGENT, ...):
	// session-backed runs skip agent auto-detection, so plain table output
	// here proves host env does not leak into request output.

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "get buckets --plain",
		EnvironmentURL: env.URL,
		Token:          "tenant-token",
	})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
	require.Contains(t, string(res.Stdout), "engine_bucket")
	require.NotContains(t, string(res.Stdout), `"ok":`,
		"host AI-agent env must not flip output into envelopes")

	env.mu.Lock()
	defer env.mu.Unlock()
	require.NotEmpty(t, env.authSeen)
	for _, auth := range env.authSeen {
		require.Contains(t, auth, "tenant-token")
		require.NotContains(t, auth, "host-secret")
	}
}

// TestExecute_ReusesUpstreamConnections is #576: each request builds a fresh
// client, and each client used to carry a private http.Transport, so a
// long-lived server paid a new TCP (+ TLS) handshake per request to a tenant
// it had just talked to. Requests now share one pool — and because the
// credential is a per-request header, not connection state, a pooled
// connection must still carry each request's own token and nothing else.
func TestExecute_ReusesUpstreamConnections(t *testing.T) {
	var connMu sync.Mutex
	conns := 0
	env := newMockEnvWithConnState(t, func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connMu.Lock()
			conns++
			connMu.Unlock()
		}
	})

	tokens := []string{"tenant-a-token", "tenant-b-token", "tenant-a-token"}
	var seenPerRun [][]string
	for _, token := range tokens {
		env.mu.Lock()
		before := len(env.authSeen)
		env.mu.Unlock()

		res, err := engine.Execute(context.Background(), engine.Request{
			Command:        "get buckets --plain",
			EnvironmentURL: env.URL,
			Token:          token,
		})
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)

		env.mu.Lock()
		seenPerRun = append(seenPerRun, append([]string(nil), env.authSeen[before:]...))
		env.mu.Unlock()
	}

	connMu.Lock()
	require.Equal(t, 1, conns, "sequential requests to one host must reuse one upstream connection")
	connMu.Unlock()

	for i, seen := range seenPerRun {
		require.NotEmpty(t, seen)
		for _, auth := range seen {
			require.Equal(t, "Bearer "+tokens[i], auth,
				"request %d must carry only its own credential over the pooled connection", i)
		}
	}
}

// TestExecute_QuotedFilename: the command string is split with shell quoting
// rules, so a filename with spaces resolves against the virtual filesystem.
func TestExecute_QuotedFilename(t *testing.T) {
	env := newMockEnv(t)

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        `create bucket -f "my bucket.yaml" --plain`,
		EnvironmentURL: env.URL,
		Token:          "tenant-token",
		Files: map[string][]byte{
			"my bucket.yaml": []byte("bucketName: new_bucket\ntable: logs\nretentionDays: 35\n"),
		},
	})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)

	env.mu.Lock()
	defer env.mu.Unlock()
	require.Equal(t, 1, env.mutations)
}

// TestExecute_WritebackReachesResultFiles: `apply --write-id` stamps the
// generated ID into the request's virtual file, and the caller receives the
// updated file in Result.Files — the full round-trip a LangChain-style
// virtual filesystem needs.
func TestExecute_WritebackReachesResultFiles(t *testing.T) {
	env := newMockEnv(t)

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "apply -f workflow.yaml --write-id --plain",
		EnvironmentURL: env.URL,
		Token:          "tenant-token",
		Files: map[string][]byte{
			"workflow.yaml": []byte("title: engine-created\ntasks: {}\n"),
		},
	})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
	require.Contains(t, string(res.Files["workflow.yaml"]), "wf-generated-1",
		"the generated workflow id must be stamped into the virtual file")
}

// TestExecute_StdinDash: a stdin-capable command (`-f -`) reads the request's
// stdin bytes, not the host process stdin.
func TestExecute_StdinDash(t *testing.T) {
	env := newMockEnv(t)

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "translate lql-to-dql -f - --plain",
		EnvironmentURL: env.URL,
		Token:          "tenant-token",
		Stdin:          []byte(`log.source="stdin-test"`),
	})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
	require.Contains(t, string(res.Stdout), "matchesValue",
		"the translation of the stdin-fed expression must be printed")
}

// TestExecute_UnsupportedCommands: every policy-blocked command must refuse to
// run. Registered commands answer with the signposted unsupported error;
// either way the exit code is non-zero and nothing executes.
func TestExecute_UnsupportedCommands(t *testing.T) {
	for name := range engine.UnsupportedCommands() {
		t.Run(name, func(t *testing.T) {
			res, err := engine.Execute(context.Background(), engine.Request{
				Argv:           []string{name},
				EnvironmentURL: "https://x.example.invalid",
				Token:          "t",
			})
			require.NoError(t, err)
			require.NotZero(t, res.ExitCode)
		})
	}

	// With --agent the block renders as a structured envelope with the
	// stable code, so machine callers can branch on it.
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "config --agent",
		EnvironmentURL: "https://x.example.invalid",
		Token:          "t",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	require.Contains(t, string(res.Stdout), `"code":"unsupported_in_service"`)
}

// TestExecute_BlockedHiddenFromCatalog: an agent bootstrapping from the
// `dtctl commands` catalog must not see commands the service cannot run.
func TestExecute_BlockedHiddenFromCatalog(t *testing.T) {
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "commands",
		EnvironmentURL: "https://x.example.invalid",
		Token:          "t",
	})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
	out := string(res.Stdout)
	require.Contains(t, out, "get", "the resource surface stays visible")
	// "skills:" (with colon) is the catalog entry for the top-level command;
	// the bare word also appears in the unrelated "copilot-skills" resource.
	for _, blocked := range []string{"doctor", "skills:", "completion", "repo-scope"} {
		require.NotContains(t, out, blocked)
	}
}

// TestExecute_ReadonlyBlocksMutation: the per-request safety level stops a
// mutating command before any API request leaves the process.
func TestExecute_ReadonlyBlocksMutation(t *testing.T) {
	env := newMockEnv(t)

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "create bucket --name b --table logs --retention 35 --plain",
		EnvironmentURL: env.URL,
		Token:          "tenant-token",
		SafetyLevel:    "readonly",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)

	env.mu.Lock()
	defer env.mu.Unlock()
	require.Zero(t, env.mutations)
}

// TestExecute_ProfileMasksSurface: a per-request profile reduces the command
// surface with the stable profile_blocked code.
func TestExecute_ProfileMasksSurface(t *testing.T) {
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "get buckets --agent",
		EnvironmentURL: "https://x.example.invalid",
		Token:          "t",
		Profile:        "query",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	require.Contains(t, string(res.Stdout), `"code":"profile_blocked"`)
}

// TestExecute_ContextCancelledBeforeStart: a request whose context is already
// done never runs.
func TestExecute_ContextCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := engine.Execute(ctx, engine.Request{
		Command:        "get buckets",
		EnvironmentURL: "https://x.example.invalid",
		Token:          "t",
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, res)
}

// TestExecute_ConcurrentRequests: Execute is goroutine-safe (requests
// serialize internally); every caller gets its own complete result.
func TestExecute_ConcurrentRequests(t *testing.T) {
	env := newMockEnv(t)

	var wg sync.WaitGroup
	results := make([]*engine.Result, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := engine.Execute(context.Background(), engine.Request{
				Command:        "get buckets --plain",
				EnvironmentURL: env.URL,
				Token:          "tenant-token",
			})
			require.NoError(t, err)
			results[i] = res
		}(i)
	}
	wg.Wait()

	for i, res := range results {
		require.Zero(t, res.ExitCode, "request %d failed: %s", i, res.Stderr)
		require.Contains(t, string(res.Stdout), "engine_bucket", "request %d", i)
	}
}

// newSlowMockEnv creates a mock server that blocks each request until the
// returned release channel is closed, and counts requests in mu/reqCount.
type slowMockEnv struct {
	*httptest.Server
	mu       sync.Mutex
	reqCount int
	release  chan struct{}
	entered  chan struct{}
}

func newSlowMockEnv(t *testing.T) *slowMockEnv {
	t.Helper()
	m := &slowMockEnv{
		release: make(chan struct{}),
		entered: make(chan struct{}, 1),
	}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.reqCount++
		m.mu.Unlock()
		select {
		case m.entered <- struct{}{}:
		default:
		}
		<-m.release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"buckets":[]}`))
	}))
	t.Cleanup(m.Server.Close)
	return m
}

// TestExecute_CancelledWhileQueuedNeverRuns: hold the slot with a slow goroutine,
// cancel a second request's context, assert it returns context.Canceled and the
// server saw only one request.
func TestExecute_CancelledWhileQueuedNeverRuns(t *testing.T) {
	m := newSlowMockEnv(t)

	lim := engine.DefaultLimits()
	lim.MaxQueued = 2

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		engine.ExecuteWithLimits(context.Background(), engine.Request{
			Command: "get buckets --plain", EnvironmentURL: m.URL, Token: "t",
		}, lim)
	}()

	// Wait until the first request holds the slot.
	<-m.entered

	// Pre-cancelled context: the queued request must not run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := engine.ExecuteWithLimits(ctx, engine.Request{
		Command: "get buckets --plain", EnvironmentURL: m.URL, Token: "t",
	}, lim)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, res)

	close(m.release)
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	require.Equal(t, 1, m.reqCount, "cancelled request must not reach the server")
}

// TestExecute_DurationBudgetCancelsExecution: MaxDuration elapses while the
// request is waiting for the slot; it must return within ~200ms.
func TestExecute_DurationBudgetCancelsExecution(t *testing.T) {
	m := newSlowMockEnv(t)

	lim := engine.DefaultLimits()
	lim.MaxQueued = 2
	// Second request uses a 100ms budget; the slot is held by the first goroutine
	// for as long as needed, so the budget fires while queued.
	shortLim := lim
	shortLim.MaxDuration = 100 * time.Millisecond

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		engine.ExecuteWithLimits(context.Background(), engine.Request{
			Command: "get buckets --plain", EnvironmentURL: m.URL, Token: "t",
		}, lim)
	}()
	<-m.entered

	start := time.Now()
	_, err := engine.ExecuteWithLimits(context.Background(), engine.Request{
		Command: "get buckets --plain", EnvironmentURL: m.URL, Token: "t",
	}, shortLim)
	elapsed := time.Since(start)

	close(m.release)
	wg.Wait()

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, elapsed, 200*time.Millisecond, "execution must abort within the budget")
}

// TestExecute_DurationBudgetCancelsInFlightQuery: when the request's budget
// elapses while a DQL query is still running on Grail, the query is cancelled
// on the backend and the request returns promptly, instead of polling on for
// the SDK's own poll deadline after the caller has gone (#502).
func TestExecute_DurationBudgetCancelsInFlightQuery(t *testing.T) {
	const token = "tok-engine-inflight"
	cancelled := make(chan string, 1)
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/platform/storage/query/v1/query:execute":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"state":"RUNNING","requestToken":"` + token + `"}`))
		case "/platform/storage/query/v1/query:poll":
			// A query that never finishes: hold each poll until the client
			// gives up (or the test ends), then report it still running.
			select {
			case <-r.Context().Done():
			case <-release:
			case <-time.After(5 * time.Second):
			}
			_, _ = w.Write([]byte(`{"state":"RUNNING","requestToken":"` + token + `"}`))
		case "/platform/storage/query/v1/query:cancel":
			select {
			case cancelled <- r.URL.Query().Get("request-token"):
			default:
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	lim := engine.DefaultLimits()
	lim.MaxDuration = 300 * time.Millisecond

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		_, _ = engine.ExecuteWithLimits(context.Background(), engine.Request{
			Command: `query "fetch logs" --plain`, EnvironmentURL: srv.URL, Token: "t",
		}, lim)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("query kept running after the request's budget elapsed")
	}
	require.Less(t, time.Since(start), 5*time.Second, "an abandoned query must not outlive its request")

	select {
	case got := <-cancelled:
		require.Equal(t, token, got, "the running query must be cancelled on the backend")
	default:
		t.Fatal("the running query was never cancelled on the backend")
	}
}

// TestExecute_LaterRequestsGetTheirOwnContext: each request's context ends
// with the request, so a command must not keep the context of the first
// request that ran it — or every later query would start out cancelled.
func TestExecute_LaterRequestsGetTheirOwnContext(t *testing.T) {
	var mu sync.Mutex
	executes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/platform/storage/query/v1/query:execute" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			return
		}
		mu.Lock()
		executes++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"state":"SUCCEEDED","result":{"records":[{"marker":"engine-ctx-ok"}]}}`))
	}))
	t.Cleanup(srv.Close)

	for i := 0; i < 2; i++ {
		res, err := engine.Execute(context.Background(), engine.Request{
			Command: `query "fetch logs" --plain -o json`, EnvironmentURL: srv.URL, Token: "t",
		})
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, "request %d: %s", i, res.Stderr)
		require.Contains(t, string(res.Stdout), "engine-ctx-ok", "request %d", i)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, executes)
}

// TestExecute_OutputIsCapped: a MaxOutputBytes well below the command's real
// output causes Result.Truncated and len(Stdout) <= cap.
func TestExecute_OutputIsCapped(t *testing.T) {
	env := newMockEnv(t)

	lim := engine.DefaultLimits()
	lim.MaxOutputBytes = 10

	res, err := engine.ExecuteWithLimits(context.Background(), engine.Request{
		Command: "get buckets --plain", EnvironmentURL: env.URL, Token: "t",
	}, lim)

	require.NoError(t, err)
	require.True(t, res.Truncated, "output exceeding cap must be flagged as truncated")
	require.LessOrEqual(t, len(res.Stdout), 10, "stdout must not exceed the cap")
}

// TestExecute_WatchIsRefused: --watch returns a capability_disabled error in
// engine mode (zero Capabilities, LongRunningStreams=false).
func TestExecute_WatchIsRefused(t *testing.T) {
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "get workflows --watch --agent",
		EnvironmentURL: "https://x.example.invalid",
		Token:          "t",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	require.True(t,
		strings.Contains(string(res.Stdout), `"capability_disabled"`),
		"watch in service mode must return capability_disabled envelope, got: %s", res.Stdout,
	)
}

// TestExecute_QueryLiveIsRefused: `query --live` is just as unbounded as
// `get --watch` (it loops until interrupted), so it must be refused by the
// same LongRunningStreams gate. Regression test for the gap where --live
// reached RunLive's infinite loop unchecked and held the single engine slot
// forever (see doc.go's "Cancellation" section).
func TestExecute_QueryLiveIsRefused(t *testing.T) {
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        `query "fetch logs" --live --agent`,
		EnvironmentURL: "https://x.example.invalid",
		Token:          "t",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	require.True(t,
		strings.Contains(string(res.Stdout), `"capability_disabled"`),
		"query --live in service mode must return capability_disabled envelope, got: %s", res.Stdout,
	)
}

// TestExecute_PartialLimitsAreNormalized: a Limits value with only some
// fields set must not silently misbehave on the fields left at Go's zero
// value (MaxQueued=0 would reject every request; MaxDuration=0 would cancel
// every request before it starts).
func TestExecute_PartialLimitsAreNormalized(t *testing.T) {
	env := newMockEnv(t)

	cases := []engine.Limits{
		{},
		{MaxDuration: time.Hour},
		{MaxOutputBytes: 1 << 20},
	}
	for _, lim := range cases {
		res, err := engine.ExecuteWithLimits(context.Background(), engine.Request{
			Command: "get buckets --plain", EnvironmentURL: env.URL, Token: "t",
		}, lim)
		require.NoError(t, err, "limits %+v", lim)
		require.Zero(t, res.ExitCode, "limits %+v", lim)
		require.NotEmpty(t, res.Stdout, "limits %+v: a zero-value field must fall back to DefaultLimits, not silently reject or empty the run", lim)
	}
}

// TestExecute_MaxFileBytesRejectsOversizedRequest: Request.Files exceeding
// MaxFileBytes must be rejected before the command runs (and before
// consuming an admission-control slot), not silently accepted.
func TestExecute_MaxFileBytesRejectsOversizedRequest(t *testing.T) {
	lim := engine.DefaultLimits()
	lim.MaxFileBytes = 10

	res, err := engine.ExecuteWithLimits(context.Background(), engine.Request{
		Command:        "get workflows --agent",
		EnvironmentURL: "https://x.example.invalid",
		Token:          "t",
		Files:          map[string][]byte{"big.yaml": make([]byte, 11)},
	}, lim)

	require.Error(t, err)
	require.Nil(t, res)
}
