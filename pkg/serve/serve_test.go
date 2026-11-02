package serve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/aidetect"
	"github.com/Dynatrace/dtctl/pkg/engine"
	"github.com/Dynatrace/dtctl/sdk/session"
)

// newDynatraceMock is a minimal fake Dynatrace environment for handler tests.
func newDynatraceMock(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/platform/storage/management/v1/bucket-definitions" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"buckets":[{"bucketName":"serve_bucket","table":"logs","status":"active","retentionDays":35,"version":1,"updatable":true}]}`))
		case r.URL.Path == "/platform/storage/management/v1/bucket-definitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"bucketName":"new_bucket","table":"logs","status":"creating","retentionDays":35,"version":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func postExecute(t *testing.T, srv *httptest.Server, body string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/execute", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(resp.Body)
	require.NoError(t, err)
	return resp, buf.Bytes()
}

func TestHandler_Execute(t *testing.T) {
	dt := newDynatraceMock(t)
	srv := httptest.NewServer(Handler(10<<20, engine.DefaultLimits()))
	t.Cleanup(srv.Close)

	resp, body := postExecute(t, srv,
		`{"command":"get buckets --plain","environmentUrl":"`+dt.URL+`","token":"tenant-token"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out executeResponse
	require.NoError(t, json.Unmarshal(body, &out))
	require.Zero(t, out.ExitCode, "stderr: %s", out.Stderr)
	require.Contains(t, out.Stdout, "serve_bucket")
}

func TestHandler_FilesRoundTrip(t *testing.T) {
	dt := newDynatraceMock(t)
	srv := httptest.NewServer(Handler(10<<20, engine.DefaultLimits()))
	t.Cleanup(srv.Close)

	req, err := json.Marshal(executeRequest{
		Command:        "create bucket -f bucket.yaml --plain",
		EnvironmentURL: dt.URL,
		Token:          "tenant-token",
		Files:          map[string]string{"bucket.yaml": "bucketName: new_bucket\ntable: logs\nretentionDays: 35\n"},
	})
	require.NoError(t, err)

	resp, body := postExecute(t, srv, string(req))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out executeResponse
	require.NoError(t, json.Unmarshal(body, &out))
	require.Zero(t, out.ExitCode, "stderr: %s", out.Stderr)
	require.Contains(t, out.Files, "bucket.yaml",
		"the virtual filesystem's final state travels back to the caller")
}

// TestHandler_CommandFailureIsHTTP200: a command that fails is still a
// successful *execution* — the failure lives in exitCode/stderr, exactly as
// on a local shell. HTTP error statuses are reserved for requests that never
// ran.
func TestHandler_CommandFailureIsHTTP200(t *testing.T) {
	srv := httptest.NewServer(Handler(10<<20, engine.DefaultLimits()))
	t.Cleanup(srv.Close)

	resp, body := postExecute(t, srv,
		`{"command":"config","environmentUrl":"https://x.example.invalid","token":"t"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out executeResponse
	require.NoError(t, json.Unmarshal(body, &out))
	require.NotZero(t, out.ExitCode)
	require.Contains(t, out.Stderr, "not supported in this environment")
}

func TestHandler_RequestErrors(t *testing.T) {
	srv := httptest.NewServer(Handler(1024, engine.DefaultLimits()))
	t.Cleanup(srv.Close)

	t.Run("invalid JSON", func(t *testing.T) {
		resp, _ := postExecute(t, srv, `{not json`)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("missing token", func(t *testing.T) {
		resp, body := postExecute(t, srv,
			`{"command":"get buckets","environmentUrl":"https://x.example.invalid"}`)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(body), "Token is required")
	})

	t.Run("body too large", func(t *testing.T) {
		resp, _ := postExecute(t, srv,
			`{"command":"get buckets","stdin":"`+strings.Repeat("x", 2048)+`"}`)
		require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	})

	t.Run("method not allowed", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/v1/execute")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	})
}

// TestHandler_StabilityFloorOverTheWire: the floor and its exceptions have to
// be expressible in the protocol, or an HTTP client is stuck with the default
// and every experimental command looks like it does not exist. The exception
// form is the one to reach for — it widens the surface to what the operator
// tested, not to whatever joins the tier next release.
func TestHandler_StabilityFloorOverTheWire(t *testing.T) {
	srv := httptest.NewServer(Handler(10<<20, engine.DefaultLimits()))
	t.Cleanup(srv.Close)

	const tenant = `"environmentUrl":"https://x.example.invalid","token":"t"`

	decode := func(t *testing.T, body []byte) executeResponse {
		t.Helper()
		var out executeResponse
		require.NoError(t, json.Unmarshal(body, &out))
		return out
	}

	t.Run("default floor blocks the experimental tier", func(t *testing.T) {
		resp, body := postExecute(t, srv, `{"command":"inventory --agent",`+tenant+`}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		out := decode(t, body)
		require.NotZero(t, out.ExitCode)
		require.Contains(t, out.Stdout, `"code":"stability_blocked"`)
	})

	t.Run("exception admits exactly what it names", func(t *testing.T) {
		resp, body := postExecute(t, srv,
			`{"command":"inventory --agent","stabilityExceptions":["inventory"],`+tenant+`}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NotContains(t, decode(t, body).Stdout, `"code":"stability_blocked"`)

		resp, body = postExecute(t, srv,
			`{"command":"get breakpoints --agent","stabilityExceptions":["inventory"],`+tenant+`}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, decode(t, body).Stdout, `"code":"stability_blocked"`)
	})

	t.Run("lowering the floor grants the tier", func(t *testing.T) {
		resp, body := postExecute(t, srv,
			`{"command":"inventory --agent","minStability":"experimental",`+tenant+`}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NotContains(t, decode(t, body).Stdout, `"code":"stability_blocked"`)
	})

	// A request that never ran answers 4xx, not an exit code — a typo in the
	// floor must not read as "that command is unavailable".
	t.Run("a bad floor is a request error", func(t *testing.T) {
		resp, body := postExecute(t, srv,
			`{"command":"get buckets","minStability":"beta",`+tenant+`}`)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(body), "invalid minimum stability")
	})

	t.Run("development is not a floor value", func(t *testing.T) {
		resp, body := postExecute(t, srv,
			`{"command":"get buckets","minStability":"development",`+tenant+`}`)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(body), "not reachable from a request")
	})
}

func TestHandler_Healthz(t *testing.T) {
	srv := httptest.NewServer(Handler(1024, engine.DefaultLimits()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestNewCommand_Registered pins the command surface: `serve` is a protocol
// parent, not a server itself, so a future `serve mcp` lands beside `serve
// http` instead of competing with an incumbent default.
func TestNewCommand_Registered(t *testing.T) {
	c := NewCommand()
	require.Equal(t, "serve", c.Name())
	require.Equal(t, []string{"http"}, protocolNames(c),
		"every server is a named protocol under serve, so a future `serve mcp` "+
			"lands beside `serve http` rather than competing with a default")

	httpCmd, _, err := c.Find([]string{"http"})
	require.NoError(t, err)
	require.NotNil(t, httpCmd.Flags().Lookup("addr"))
	require.NotNil(t, httpCmd.Flags().Lookup("max-request-bytes"))
	require.NotNil(t, httpCmd.Flags().Lookup("max-queued"))
	require.NotNil(t, httpCmd.Flags().Lookup("max-duration"))
}

// TestHandler_503WhenQueueFull: a low --max-queued must shed excess requests
// with 503 rather than let the queue grow unbounded.
func TestHandler_503WhenQueueFull(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"buckets":[]}`))
	}))
	t.Cleanup(slow.Close)

	limits := engine.DefaultLimits()
	limits.MaxQueued = 1
	srv := httptest.NewServer(Handler(10<<20, limits))
	t.Cleanup(srv.Close)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		postExecute(t, srv, fmt.Sprintf(
			`{"command":"get buckets","environmentUrl":%q,"token":"t"}`, slow.URL))
	}()
	<-entered // the first request now holds the single admitted slot

	resp, _ := postExecute(t, srv, fmt.Sprintf(
		`{"command":"get buckets","environmentUrl":%q,"token":"t"}`, slow.URL))
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	close(release)
	wg.Wait()
}

// TestHTTPCommand_WriteTimeoutMustExceedMaxDuration: http.Server sets the
// write deadline when the request header is read, so it covers execution and
// the response write. --write-timeout at or below --max-duration would cut a
// slow-but-successful command's response off before the client sees it.
func TestHTTPCommand_WriteTimeoutMustExceedMaxDuration(t *testing.T) {
	c := NewCommand()
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"http", "--write-timeout=1m", "--max-duration=1m"})
	err := c.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--write-timeout")
	require.Contains(t, err.Error(), "--max-duration")
}

// TestBareServePrintsHelp: naming the protocol is mandatory, and omitting it is
// not an error — help on stdout, exit 0 (Run returns the process exit code).
func TestBareServePrintsHelp(t *testing.T) {
	c := NewCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(nil)

	require.NoError(t, c.Execute())
	require.Contains(t, out.String(), "http")
	require.Contains(t, out.String(), "Available Commands:")
}

// TestUnknownProtocolFails: cobra prints help and exits 0 for an unmatched name
// under a parent with no Run. A supervisor would read that as a started server,
// so serve validates the protocol name itself.
func TestUnknownProtocolFails(t *testing.T) {
	c := NewCommand()
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"grpc"})

	err := c.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown protocol "grpc"`)
	require.Contains(t, err.Error(), "http", "the error must list what is available")
}

// TestDevelopmentGate: server mode is opt-in. It is a development-tier
// feature, so the gate reads the one DTCTL_DEVELOPMENT list that every such
// feature shares rather than a per-feature variable.
func TestDevelopmentGate(t *testing.T) {
	for _, c := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"account", false}, {"serve-http", false},
		{"serve", true}, {"account,serve", true}, {" SERVE ", true},
		{"development.serve", true},
		// The sentinel enables every registered feature at once, which is what
		// dtctl's own test and development builds use.
		{session.DevelopmentAll, true},
	} {
		t.Setenv(session.DevelopmentEnvVar, c.value)
		require.Equalf(t, c.want, Enabled(), "%s=%q", session.DevelopmentEnvVar, c.value)
	}

	// Unset is off — the released-build default.
	t.Setenv(session.DevelopmentEnvVar, "")
	require.NoError(t, os.Unsetenv(session.DevelopmentEnvVar))
	require.False(t, Enabled())

	// The retired per-feature variable is still honored as a deprecated alias,
	// so an existing deployment does not break on upgrade.
	t.Setenv("DTCTL_EXPERIMENTAL_SERVE", "1")
	require.True(t, Enabled(), "the legacy opt-in must keep working for one release")
}

// TestDevelopmentGateHidesCommand builds the real binary and checks the gate
// end to end: the wiring lives in main (both the dispatch and the
// registration), which no unit test in this package can reach. Without the
// opt-in, `serve` must be an ordinary unknown command — not
// hidden-but-runnable, and not advertised in help or the command catalog.
func TestDevelopmentGateHidesCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the dtctl binary; skipped in -short mode")
	}
	name := "dtctl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Dir = filepath.Join("..", "..") // module root, where main lives
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	// A naive caller's environment: both the current and the legacy opt-in
	// absent entirely, not merely empty. The distinction matters — setting
	// DTCTL_DEVELOPMENT at all is what tells dtctl the caller knows the
	// mechanism, which switches on the explanation asserted further down.
	base := []string{}
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, session.DevelopmentEnvVar+"="),
			strings.HasPrefix(kv, "DTCTL_EXPERIMENTAL_SERVE="):
			continue
		}
		base = append(base, kv)
	}
	// The assertions read human output, so a test run under an AI agent must
	// not auto-enable the agent envelope. Empty means "not set" to detection.
	for _, v := range aidetect.EnvVars() {
		base = append(base, v+"=")
	}
	run := func(env []string, args ...string) (int, string) {
		c := exec.Command(exe, args...)
		c.Env = append(append([]string(nil), base...), env...)
		combined, _ := c.CombinedOutput()
		return c.ProcessState.ExitCode(), string(combined)
	}

	code, output := run(nil, "serve", "http", "--addr", "127.0.0.1:0")
	require.NotZero(t, code, "serve must not run without the opt-in")
	require.Contains(t, output, `unknown command "serve"`,
		"a caller who has never heard of the mechanism must not learn the feature exists")

	// Matched as a command entry ("  serve   Run dtctl as a server"), so the
	// assertion does not trip over unrelated prose containing "server".
	_, output = run(nil, "--help")
	require.NotRegexp(t, `(?m)^\s+serve\s`, output,
		"an off-by-default surface must not be advertised in help")

	// A caller who has set DTCTL_DEVELOPMENT — even to an off value — is
	// evidently mid-setup, and for them silence is the unhelpful answer. They
	// get the feature named and the opt-in spelled out.
	code, output = run([]string{session.DevelopmentEnvVar + "="}, "serve", "http")
	require.NotZero(t, code, "serve still must not run")
	require.Contains(t, output, "development feature")
	require.Contains(t, output, "development."+DevelopmentFeature)

	// With the opt-in the command exists again; bare `serve` is discovery.
	code, output = run([]string{session.DevelopmentEnvVar + "=" + DevelopmentFeature}, "serve")
	require.Zero(t, code, "bare serve prints help and exits 0: %s", output)
	require.Contains(t, output, "http")
}
