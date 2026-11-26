package engine_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/engine"
)

// newTLSMockEnv is newMockEnv served over TLS with httptest's self-signed
// certificate — the shape of a stand-in environment a host builds in its own
// tests. Only srv.Client().Transport trusts that certificate.
func newTLSMockEnv(t *testing.T) *mockEnv {
	t.Helper()
	m := newUnstartedMockEnv()
	m.StartTLS()
	t.Cleanup(m.Server.Close)
	return m
}

// TestWithTransport_StandInEnvironment is the success path a host could not
// test before the seam existed: its stand-in environment speaks TLS with a
// certificate only its own client trusts, and the engine built its client
// from the URL alone. With WithTransport, get, create and apply all succeed
// against it, carrying the request's own token.
func TestWithTransport_StandInEnvironment(t *testing.T) {
	env := newTLSMockEnv(t)
	eng := engine.New(engine.DefaultLimits(), engine.WithTransport(env.Client().Transport))

	for _, tc := range []struct {
		name    string
		command string
		files   map[string][]byte
		want    string
	}{
		{name: "get", command: "get buckets --plain", want: "engine_bucket"},
		{name: "create", command: "create bucket --name new_bucket --table logs --retention 35 --plain", want: "new_bucket"},
		{
			name:    "apply",
			command: "apply -f wf.yaml --plain",
			files:   map[string][]byte{"wf.yaml": []byte("title: engine-created\ntasks: {}\n")},
			want:    "wf-generated-1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := eng.Execute(context.Background(), engine.Request{
				Command:        tc.command,
				EnvironmentURL: env.URL,
				Token:          "tenant-token",
				Files:          tc.files,
			})
			require.NoError(t, err)
			require.Zero(t, res.ExitCode, "stdout: %s\nstderr: %s", res.Stdout, res.Stderr)
			// create reports on stderr, get and apply on stdout.
			require.Contains(t, string(res.Stdout)+string(res.Stderr), tc.want)
		})
	}

	env.mu.Lock()
	defer env.mu.Unlock()
	require.Equal(t, 2, env.mutations, "create and apply must each have reached the stand-in")
	for _, auth := range env.authSeen {
		require.Equal(t, "Bearer tenant-token", auth)
	}
}

// TestWithTransport_DefaultPoolDoesNotTrustStandIn pins why the seam is
// needed: without it the request goes through the shared pool, which does not
// trust the stand-in's certificate, so the run fails and nothing arrives.
// It also shows that one Engine's transport does not leak into the
// package-level Execute.
func TestWithTransport_DefaultPoolDoesNotTrustStandIn(t *testing.T) {
	env := newTLSMockEnv(t)
	_ = engine.New(engine.DefaultLimits(), engine.WithTransport(env.Client().Transport))

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "get buckets --plain",
		EnvironmentURL: env.URL,
		Token:          "tenant-token",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode, "the default pool must not trust a self-signed stand-in")
	require.Contains(t, string(res.Stdout)+string(res.Stderr), "certificate", "the run must fail on the certificate, not elsewhere; stdout: %s stderr: %s", res.Stdout, res.Stderr)

	env.mu.Lock()
	defer env.mu.Unlock()
	require.Empty(t, env.authSeen, "no request may complete over an untrusted TLS connection")
}

// recordingTransport answers every request in memory and records what it saw.
type recordingTransport struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.reqs = append(rt.reqs, req.Clone(context.Background()))
	rt.mu.Unlock()
	body := `{"buckets":[{"bucketName":"in_memory_bucket","table":"logs","status":"active","retentionDays":35,"version":1,"updatable":true}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

// TestWithTransport_OnlyTheTransportChanges: the RoundTripper sits under
// everything dtctl adds to a request, so the request it receives is the one a
// real environment would — addressed to the request's EnvironmentURL, with the
// tenant's bearer token and dtctl's User-Agent — and a concurrent engine
// routes through it the same way a serialized one does.
func TestWithTransport_OnlyTheTransportChanges(t *testing.T) {
	for name, opts := range map[string][]engine.Option{
		"serialized": nil,
		"concurrent": {engine.WithConcurrentExecution(2)},
	} {
		t.Run(name, func(t *testing.T) {
			rt := &recordingTransport{}
			eng := engine.New(engine.DefaultLimits(), append(opts, engine.WithTransport(rt))...)

			res, err := eng.Execute(context.Background(), engine.Request{
				Command:        "get buckets --plain",
				EnvironmentURL: "https://tenant.example.invalid",
				Token:          "tenant-token",
			})
			require.NoError(t, err)
			require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
			require.Contains(t, string(res.Stdout), "in_memory_bucket")

			rt.mu.Lock()
			defer rt.mu.Unlock()
			require.NotEmpty(t, rt.reqs)
			for _, req := range rt.reqs {
				require.Equal(t, "tenant.example.invalid", req.URL.Host)
				require.Equal(t, "Bearer tenant-token", req.Header.Get("Authorization"))
				require.True(t, strings.HasPrefix(req.Header.Get("User-Agent"), "dtctl/"),
					"User-Agent %q", req.Header.Get("User-Agent"))
			}
		})
	}
}
