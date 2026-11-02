package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/cmd"
	"github.com/Dynatrace/dtctl/pkg/engine"
)

// executeRequest is the POST /v1/execute body. Command/argv, files, and stdin
// mirror engine.Request; file contents travel as plain JSON strings (the
// payloads dtctl consumes — YAML, JSON, DQL — are text).
type executeRequest struct {
	// Command is the dtctl command line exactly as typed locally,
	// e.g. "apply -f workflow.yaml --agent". Ignored when argv is set.
	Command string `json:"command,omitempty"`
	// Argv is the pre-split argument vector; takes precedence over command.
	Argv []string `json:"argv,omitempty"`

	EnvironmentURL string `json:"environmentUrl"`
	Token          string `json:"token"`
	SafetyLevel    string `json:"safetyLevel,omitempty"`
	Profile        string `json:"profile,omitempty"`
	// Omitted means the engine default floor (`stable`), not the CLI's.
	MinStability        string   `json:"minStability,omitempty"`
	StabilityExceptions []string `json:"stabilityExceptions,omitempty"`

	Files map[string]string `json:"files,omitempty"`
	Stdin string            `json:"stdin,omitempty"`
}

// executeResponse is the POST /v1/execute response. A command that fails
// still answers 200: exitCode/stderr are the result, exactly as on a local
// shell. Non-200 statuses are reserved for requests that never ran.
type executeResponse struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// Files is the complete final state of the request's virtual filesystem.
	Files      map[string]string `json:"files,omitempty"`
	DurationMs int64             `json:"durationMs"`
	// Truncated is true when stdout or stderr was cut at the engine's
	// MaxOutputBytes; see engine.Result.Truncated.
	Truncated bool `json:"truncated,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// Handler returns the HTTP server's http.Handler:
//
//	POST /v1/execute — run one dtctl command line (body: executeRequest)
//	GET  /healthz    — liveness probe
//
// maxRequestBytes bounds the request body (virtual files travel inline).
// limits controls queue depth, duration, and output caps for each execution.
func Handler(maxRequestBytes int64, limits engine.Limits) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	mux.HandleFunc("/v1/execute", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "use POST")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)

		var req executeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge,
					fmt.Sprintf("request body exceeds %d bytes", tooLarge.Limit))
				return
			}
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}

		files := make(map[string][]byte, len(req.Files))
		for name, content := range req.Files {
			files[name] = []byte(content)
		}
		var stdin []byte
		if req.Stdin != "" {
			stdin = []byte(req.Stdin)
		}

		start := time.Now()
		res, err := engine.ExecuteWithLimits(r.Context(), engine.Request{
			Command:        req.Command,
			Argv:           req.Argv,
			EnvironmentURL: req.EnvironmentURL,
			Token:          req.Token,
			SafetyLevel:    req.SafetyLevel,
			Profile:        req.Profile,

			MinStability:        req.MinStability,
			StabilityExceptions: req.StabilityExceptions,
			Files:               files,
			Stdin:               stdin,
			// Env is intentionally not exposed over HTTP: arbitrary variables
			// reach proxies, exporters, and other process-level behavior.
			// Embedding hosts that need it use engine.Request.Env directly.
		}, limits)
		if err != nil {
			// The request never ran. Distinguish *why* so a client can react
			// correctly: a malformed request is the caller's fault (400), but
			// admission shedding or a budget/client timeout is transient and
			// worth a retry with backoff (503/504), not a request rewrite.
			switch {
			case errors.Is(err, engine.ErrTooManyQueued):
				writeError(w, http.StatusServiceUnavailable, err.Error())
			case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
				writeError(w, http.StatusGatewayTimeout, err.Error())
			default:
				writeError(w, http.StatusBadRequest, err.Error())
			}
			return
		}

		outFiles := make(map[string]string, len(res.Files))
		for name, content := range res.Files {
			outFiles[name] = string(content)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(executeResponse{
			ExitCode:   res.ExitCode,
			Stdout:     string(res.Stdout),
			Stderr:     string(res.Stderr),
			Files:      outFiles,
			DurationMs: time.Since(start).Milliseconds(),
			Truncated:  res.Truncated,
		})
	})
	return mux
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
}

// ServeOptions holds timeout and engine configuration for the HTTP server.
type ServeOptions struct {
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	Limits       engine.Limits
}

// newServer builds an *http.Server without starting it — extracted for testability.
func newServer(addr string, handler http.Handler, opts ServeOptions) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
	}
}

// newHTTPCommand builds `dtctl serve http`.
func newHTTPCommand() *cobra.Command {
	var (
		addr            string
		maxRequestBytes int64
		opts            ServeOptions
	)
	opts.Limits = engine.DefaultLimits()
	c := &cobra.Command{
		Use:   "http",
		Short: "Serve the dtctl execute API over HTTP (reference implementation)",
		Long: `Serve dtctl over HTTP: one request executes one dtctl command line for one
tenant and returns the CLI-identical output.

  POST /v1/execute
    {"command": "get workflows --agent",
     "environmentUrl": "https://abc12345.apps.dynatrace.com",
     "token": "dt0s16....",
     "safetyLevel": "readonly",
     "stabilityExceptions": ["inventory"],
     "files": {"x.yaml": "..."}}
  -> {"exitCode": 0, "stdout": "...", "stderr": "...", "files": {...}}

  GET /healthz -> {"status":"ok"}

Each request brings its own environment URL and token; the local dtctl config,
keyring, and credential environment variables are never read. The same applies
to the surface a request sees: "minStability" defaults to "stable" (stricter
than the CLI, which has a human to read an [Experimental] badge), widened per
request via "minStability" or the narrower "stabilityExceptions", and never by
DTCTL_MIN_STABILITY or DTCTL_DEVELOPMENT in this server's environment. File arguments
resolve against the request's "files", and files written back (e.g. by
apply --write-id) are returned in the response. Requests execute one at a time
per process.

This is a reference implementation: it performs no authentication of its own
(the per-request token only authenticates against Dynatrace) and binds to
localhost by default. Put your own authentication and TLS in front before
exposing it, or embed pkg/engine directly.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if cmd.RunActive() {
				// Reached through the normal pipeline (e.g. `dtctl -v serve
				// http`, which the main dispatch does not intercept): serving
				// here would hold the invocation lock for the server's
				// lifetime and deadlock every request.
				return errors.New("serve must be invoked directly as `dtctl serve http [flags]`, with no arguments before it")
			}
			if opts.WriteTimeout > 0 && opts.WriteTimeout <= opts.Limits.MaxDuration {
				// http.Server sets the write deadline when the request header
				// is read, so it covers handler execution *and* the response
				// write. A --write-timeout at or below --max-duration cuts the
				// response off right as (or before) the command's own budget
				// would end, so the caller never sees a result.
				return fmt.Errorf(
					"--write-timeout (%s) must be greater than --max-duration (%s), "+
						"or the response is cut off before a slow command can reply",
					opts.WriteTimeout, opts.Limits.MaxDuration)
			}
			return runHTTP(command.Context(), addr, maxRequestBytes, opts)
		},
	}
	c.Flags().StringVar(&addr, "addr", "127.0.0.1:7211", "listen address")
	c.Flags().Int64Var(&maxRequestBytes, "max-request-bytes", 10<<20,
		"maximum request body size in bytes (virtual files travel inline)")
	c.Flags().DurationVar(&opts.ReadTimeout, "read-timeout", 30*time.Second,
		"time allowed to read the full request including body")
	// Must exceed --max-duration's default (5m): the write deadline is set
	// when the request header is read, so it covers execution *and* the
	// response write — a command that runs the full budget still needs time
	// to write its result afterwards.
	c.Flags().DurationVar(&opts.WriteTimeout, "write-timeout", 6*time.Minute,
		"time allowed to write the response (set higher than --max-duration so slow commands can still reply)")
	c.Flags().DurationVar(&opts.IdleTimeout, "idle-timeout", 2*time.Minute,
		"maximum time to wait for the next request on a keep-alive connection")
	c.Flags().IntVar(&opts.Limits.MaxQueued, "max-queued",
		engine.DefaultLimits().MaxQueued,
		"maximum number of requests allowed to queue (waiting + running); excess returns 503")
	c.Flags().DurationVar(&opts.Limits.MaxDuration, "max-duration",
		engine.DefaultLimits().MaxDuration,
		"maximum wall-clock time allowed for a single request execution")
	return c
}

// runHTTP serves until the context is cancelled or SIGINT/SIGTERM arrives,
// then shuts down gracefully: server.Shutdown waits for in-flight handlers to
// return rather than cancelling their request context, so an execution
// already running when shutdown starts is left to finish or hit its own
// MaxDuration budget — see the pkg/engine cancellation notes.
func runHTTP(ctx context.Context, addr string, maxRequestBytes int64, opts ServeOptions) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := newServer(addr, Handler(maxRequestBytes, opts.Limits), opts)

	errc := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "dtctl serve http: listening on http://%s\n", addr)
		errc <- server.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "dtctl serve http: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
