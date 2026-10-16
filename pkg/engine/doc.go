// Package engine executes dtctl command lines in-process for services.
//
// It is the embedding surface described in docs/dev/SERVICE_ENGINE_DESIGN.md: a
// caller hands over a command string exactly as a user would type it locally,
// plus the tenant to run it against (environment URL + token) and an optional
// set of virtual files, and gets back the exact byte streams the CLI would
// have printed, the exit code, and the final state of the virtual files.
//
//	res, err := engine.Execute(ctx, engine.Request{
//	    Command:        `apply -f workflow.yaml --write-id --agent`,
//	    EnvironmentURL: "https://abc12345.apps.dynatrace.com",
//	    Token:          tenantToken,
//	    Files:          map[string][]byte{"workflow.yaml": src},
//	})
//	// res.Stdout is byte-identical to local CLI output;
//	// res.Files["workflow.yaml"] now carries the stamped id.
//
// Every execution is fully isolated from the host process:
//
//   - Credentials come only from the request. The host's config file,
//     contexts, keyring, and credential environment variables are scrubbed
//     for the duration of the run (cmd.Session).
//   - File arguments (-f and friends, including writebacks like `apply
//     --write-id`) resolve against the request's virtual filesystem, never
//     the host disk (pkg/vfs).
//   - No subprocesses: plugins, shell aliases, apply hooks, editors, and
//     browser opens are all disabled (cmd.Capabilities zero value).
//   - Host-only commands (config, ctx, auth, ...) are removed from the
//     surface entirely — hidden from `dtctl commands` and help, and guarded
//     with the stable agent-mode error code "unsupported_in_service".
//
// Concurrency: Execute is safe to call from multiple goroutines, but by
// default executions serialize on an internal lock (the command tree is process
// state — see cmd.Run). A service scales by running more engine processes or
// instances, or opts in on an Engine built with
// New(limits, WithConcurrentExecution(n)), which runs up to n invocations at
// once in this process, each on a command tree and state of its own; see
// docs/dev/CONCURRENT_EXECUTION.md for what that guarantees and costs. Giving
// every request a WASM instance instead costs about 55ms of CPU to start each
// one.
//
// Admission and resource limits: ExecuteWithLimits (which Execute calls with
// DefaultLimits) bounds how many requests may queue for an execution slot
// (MaxQueued, ErrTooManyQueued once exceeded), how long a request may occupy
// it (MaxDuration, applied as a context.WithTimeout threaded into the command
// tree via RunOptions.Context), and how much stdout/stderr a command may
// produce (MaxOutputBytes; excess is dropped and Result.Truncated is set). A
// zero-value Limits field falls back to DefaultLimits. The package-level
// functions share one slot; an Engine has slots and a queue of its own.
//
// Cancellation: the context gates the start of an execution (a request
// cancelled while queued never runs). Once running, a command that observes
// cmd.Context() (most of the command tree does — see cmd/run.go) is
// cancelled cooperatively when the caller's context or MaxDuration budget is
// done. A command that never checks its context, or issues a request with
// a context of its own, still runs to completion; cmd/query.go's DQL
// execution is the one tracked exception (see its own comments). Streaming
// commands (--watch, --follow, query --live) are refused outright in service
// mode via the LongRunningStreams capability, so they cannot hold the slot
// indefinitely in the first place.
package engine
