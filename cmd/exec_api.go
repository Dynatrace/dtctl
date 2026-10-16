package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/diagnostic"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	resapi "github.com/dynatrace-oss/dtctl/pkg/resources/api"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// execAPICmd sends a request to an arbitrary platform API endpoint.
//
// It is an escape hatch, and it is deliberately unadvertised: it exists for the
// APIs dtctl does not wrap natively, not as an alternative to the ones it does.
// Hidden keeps it out of --help, completion, and the compact command catalogs an
// agent bootstraps from; pkg/commands surfaces it under `dtctl commands --full`
// for a caller who goes looking.
//
// It is also the *governed* replacement for what callers do today. Reaching an
// uncovered API currently means `dtctl exec function --code` with ad-hoc
// JavaScript, which AppEngine hands a bearer token — the least governed path in
// the tool. This one resolves what the request actually does from the API's own
// specification and gates it through the same safety checker as every native
// mutating command.
// Hidden, but reachable, so it declares a tier like anything else. It is
// stable by decision (#544): demoting it would make it unusable under a stable
// floor, in exactly the automation that has no native alternative yet. AGENTS.md's
// "don't script against it" is advice about where integrations should end up,
// not a statement about this contract.
var execAPICmd = newExecAPICmd()

func newExecAPICmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "api <path>",
		Short:  "Send a request to a platform API endpoint (escape hatch)",
		Hidden: true,
		Long: `Send a request to a platform API endpoint that has no native dtctl command.

Prefer the native command whenever one exists: it validates input, resolves names,
formats output, and cannot be pointed at the wrong endpoint. This passthrough
exists for the APIs dtctl does not wrap — and if you find yourself scripting
against it, that API wants a native command instead.

The path is relative to the environment, so it starts with '/'. Reads need no
method; anything else requires an explicit -X, because dtctl will not infer a
mutating method — and therefore a safety operation — you did not type.

What the request is allowed to do is resolved from the API's own specification,
not from the HTTP method: a POST may need only a read scope, or may delete data.
When dtctl cannot resolve the operation, it assumes the worst and gates the
request as a delete. There is deliberately no flag to assert otherwise.

Examples:
  # A read
  dtctl exec api /platform/email/v1/emails

  # A write, with an inline body
  dtctl exec api /platform/email/v1/emails -X POST -d '{"to":["a@example.invalid"]}'

  # A body from a file, or from stdin
  dtctl exec api /platform/example/v1/things -X POST -d @body.json
  cat body.json | dtctl exec api /platform/example/v1/things -X POST -d @-

  # An explicit content type (e.g. a multipart upload)
  dtctl exec api /platform/example/v1/uploads -X POST -d @archive.zip -H 'Content-Type: application/zip'

  # Compose the request and print it without sending
  dtctl exec api /platform/example/v1/things/42 -X DELETE --dry-run

  # Find out what to call, and how
  dtctl get apis
  dtctl describe api <name> --operation 'POST /things'
`,
		Args: cobra.ExactArgs(1),
		RunE: runExecAPI,
	}
	c.Flags().StringP("method", "X", http.MethodGet, "HTTP method")
	c.Flags().StringP("data", "d", "",
		"request body: inline, @file, or @- for stdin (requires an explicit -X)")
	c.Flags().StringArrayP("header", "H", nil,
		"extra request header, 'Name: value' (repeatable)")
	stability.MarkStable(c)
	return c
}

// maxErrorBodyBytes caps how much of an error response is quoted back. The
// platform's own message is the authoritative explanation of a 4xx, so it is
// preserved — but a multi-megabyte body would bury it.
const maxErrorBodyBytes = 4096

func runExecAPI(cmd *cobra.Command, args []string) error {
	requestPath := args[0]

	method, _ := cmd.Flags().GetString("method")
	data, _ := cmd.Flags().GetString("data")
	headers, _ := cmd.Flags().GetStringArray("header")

	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodGet
	}

	// canonicalPath is the spelling classification runs on; requestPath, the
	// caller's spelling, is what goes on the wire. The two may differ only in
	// benign percent-encoding — a spelling that changes *structure* under
	// normalization is refused here, because it would let the gate classify a
	// different endpoint than the server routes.
	canonicalPath, err := validateRequestPath(requestPath)
	if err != nil {
		return err
	}
	// A body without an explicit method is refused rather than promoted to POST
	// (which is what curl and gcx do): inferring the method would infer the
	// safety operation, and this command's whole premise is that the operation is
	// derived rather than assumed.
	if data != "" && !cmd.Flags().Changed("method") {
		return fmt.Errorf("-d/--data needs an explicit method: add -X POST (or PUT, PATCH, ...)\n\n" +
			"dtctl does not infer a mutating method from the presence of a body — that would " +
			"infer the safety operation too")
	}

	body, err := readRequestBody(cmdContext(cmd), data)
	if err != nil {
		return err
	}

	headerMap, err := parseHeaderFlags(headers)
	if err != nil {
		return err
	}

	// The client is built before the gate because resolving what the request does
	// requires reading the API's specification, which is itself a read. The gate
	// then applies to the caller's request.
	cfg, c, err := setupClient(cmdContext(cmd))
	if err != nil {
		return err
	}

	handler := resapi.NewHandler(c)
	apiName, op := handler.ResolveForRequest(method, canonicalPath)
	class := resapi.Classify(method, canonicalPath, apiName, op)

	nativeBase, native := resapi.NativeCoverageForPath(canonicalPath)
	warnAboutTarget(cmdContext(cmd), canonicalPath, nativeBase, native.Command)

	if dryRun(cmdContext(cmd)) {
		return printAPIDryRun(cmd, cfg, method, requestPath, headerMap, body, class, native.Command)
	}

	if err := checkSafety(cmdContext(cmd), cfg, class.SafetyOp, safety.OwnershipUnknown); err != nil {
		return &resapi.BlockedError{
			Method:        method,
			RequestPath:   requestPath,
			Class:         class,
			NativeCommand: native.Command,
			Cause:         err,
		}
	}

	req := c.HTTP().R()
	for name, value := range headerMap {
		req.SetHeader(name, value)
	}
	if len(body) > 0 {
		if _, ok := headerMap["Content-Type"]; !ok {
			// Platform APIs are JSON; an upload declares its own type with -H.
			req.SetHeader("Content-Type", "application/json")
		}
		req.SetBody(body)
	}

	resp, err := req.Execute(method, requestPath)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, requestPath, err)
	}

	if resp.StatusCode() >= 400 {
		return apiRequestError(method, requestPath, resp.StatusCode(), resp.Body(),
			apiName, op, native.Command)
	}

	return emitAPIResponse(cmdContext(cmd), method, requestPath, resp.StatusCode(),
		resp.Header().Get("Content-Type"), resp.Body())
}

// validateRequestPath enforces that the target is a path on the configured
// environment, and returns the canonical spelling classification must run on.
// An absolute URL is refused rather than followed: the caller's credentials
// belong to the environment the context names, and a passthrough that leaves
// it would send them somewhere else. A path that changes under normalization
// (dot segments, duplicate slashes, an encoded separator) is refused too — the
// gate must classify the endpoint the server routes, not a spelling of it.
func validateRequestPath(p string) (canonical string, err error) {
	if resapi.IsAbsoluteURL(p) {
		return "", fmt.Errorf("%q is an absolute URL; pass a path on the current environment instead "+
			"(e.g. /platform/example/v1/things). dtctl sends the context's credentials, so it "+
			"will not call another host", p)
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path must start with '/' (got %q); it is relative to the environment, "+
			"e.g. /platform/example/v1/things", p)
	}
	return resapi.CanonicalRequestPath(p)
}

// readRequestBody resolves the -d value: inline text, @file, or @- for stdin —
// the curl idiom, because that is what a caller reaching for a raw HTTP call
// already knows.
//
// A user-named path goes through pkg/vfs, never os: under the service engine the
// path names a file in the *request*, and reading it from the host disk would
// read the server's filesystem on a caller's behalf.
func readRequestBody(ctx context.Context, data string) ([]byte, error) {
	if data == "" {
		return nil, nil
	}
	if !strings.HasPrefix(data, "@") {
		return []byte(data), nil
	}

	name := strings.TrimPrefix(data, "@")
	if name == "" {
		return nil, fmt.Errorf("-d @ needs a filename, or @- to read stdin")
	}
	// ReadFileOrStdin maps "-" to the process stdin, which is the stream seam an
	// embedded invocation swaps. Opening /dev/stdin as a path would slip past it.
	content, err := vfsEnv(ctx).ReadFileOrStdin(name)
	if err != nil {
		return nil, fmt.Errorf("reading request body from %s: %w", data, err)
	}
	return content, nil
}

// parseHeaderFlags parses -H 'Name: value' pairs, canonicalizing the name so a
// caller-supplied content type is recognised however it was capitalized.
//
// Auth headers are refused: the context is the identity, and dtctl attaches
// its credentials itself. A -H that replaced them would fight the client's own
// auth silently — whichever writes last wins, and nothing would say so.
func parseHeaderFlags(headers []string) (map[string]string, error) {
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		name, value, found := strings.Cut(h, ":")
		if !found {
			return nil, fmt.Errorf("invalid header %q: expected 'Name: value'", h)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("invalid header %q: the name is empty", h)
		}
		canonical := http.CanonicalHeaderKey(name)
		if canonical == "Authorization" || canonical == "Proxy-Authorization" {
			return nil, fmt.Errorf("refusing -H %s: dtctl sends the current context's credentials; "+
				"to call as a different identity, switch contexts (dtctl ctx use <name>)", canonical)
		}
		out[canonical] = strings.TrimSpace(value)
	}
	return out, nil
}

// warnAboutTarget prints the two notices that make the escape hatch
// self-deprecating: one when a native command already covers the path, and one
// when the path is off the public platform tree.
//
// Both are warnings rather than errors. The caller typed the path; dtctl's job is
// to say what it knows, not to refuse.
func warnAboutTarget(ctx context.Context, requestPath, nativeBase, nativeCommand string) {
	if nativeCommand != "" {
		output.FprintWarning(currentStderr(ctx),
			"%s is covered by '%s' — prefer the native command, which validates input, "+
				"resolves names, and formats output",
			nativeBase, nativeCommand)
	}
	if !strings.HasPrefix(requestPath, "/platform/") {
		// Deliberately phrased in terms of the public tree rather than naming any
		// particular non-public prefix: this is open source, and the warning is
		// about stability, not about what exists.
		output.FprintWarning(currentStderr(ctx),
			"%s is outside the public /platform/ API tree; APIs there carry no compatibility "+
				"guarantee and may change or disappear without notice",
			requestPath)
	}
}

// printAPIDryRun prints the composed request and the gate that would apply,
// without sending anything.
//
// It reports the safety verdict instead of enforcing it — a dry run's job is to
// show what would happen, and "this would be blocked, here is why" is more useful
// than an error that hides the composed request.
//
// In agent mode the same preview arrives as a dry-run envelope: each line's value
// under its snake_case label in result.details, plus the rendered text. The body
// is reported by size only, as it is for a human — the envelope is no more
// willing to echo a request body than the preview is.
func printAPIDryRun(cmd *cobra.Command, cfg *config.Config, method, requestPath string, headers map[string]string,
	body []byte, class resapi.Classification, nativeCommand string) error {

	const w = 14
	report := newDryRunReport(cmd).
		KV("Method:", w, "%s", method).
		KV("Path:", w, "%s", requestPath)
	if len(headers) > 0 {
		report.KV("Headers:", w, "%s", strings.Join(redactedHeaderLines(headers), ", "))
	}
	if len(body) > 0 {
		report.KV("Body:", w, "%d bytes", len(body))
	}
	report.KV("Gated as:", w, "%s", string(class.SafetyOp)).
		KV("Because:", w, "%s", class.Reason)
	if nativeCommand != "" {
		report.KV("Native:", w, "%s (preferred)", nativeCommand).
			Detail("native", "%s", nativeCommand)
	}

	verdict := "permitted in this context"
	if checker, err := NewSafetyChecker(cfg); err == nil {
		if serr := checker.CheckError(class.SafetyOp, safety.OwnershipUnknown); serr != nil {
			verdict = "BLOCKED in this context — " + safetyRefusalReason(serr)
		}
	}
	return report.KV("Safety:", w, "%s", verdict).
		Linef("").
		Linef("  Nothing was sent (--dry-run).").
		Print()
}

// safetyRefusalReason extracts the one-line reason from a safety refusal.
//
// SafetyError.Error() opens with "Operation not allowed:" and puts the reason on a
// later line, so the first line alone says nothing. The dry run has room for one
// line, and it should be the one that explains the verdict.
func safetyRefusalReason(err error) string {
	var safetyErr *safety.SafetyError
	if errors.As(err, &safetyErr) && safetyErr.Reason != "" {
		return safetyErr.Reason
	}
	return firstLineOf(err.Error())
}

// redactedHeaderLines renders headers for display with credential-bearing values
// masked. dtctl never puts the token in a -H flag itself, but a caller can, and a
// dry run's output is exactly the thing that gets pasted into a bug report.
func redactedHeaderLines(headers map[string]string) []string {
	sensitive := map[string]bool{
		"Authorization": true, "Proxy-Authorization": true, "Cookie": true,
		"X-Api-Key": true, "Api-Token": true,
	}
	out := make([]string, 0, len(headers))
	for name, value := range headers {
		if sensitive[name] {
			value = "<redacted>"
		}
		out = append(out, name+": "+value)
	}
	slices.Sort(out)
	return out
}

// emitAPIResponse renders a successful response.
//
// The output protocol is declared, not emergent: a JSON body goes through the
// normal printer — so -o json/yaml and the agent envelope behave exactly as they
// do for every other command — and anything else is passed through verbatim,
// even under --agent. A generic passthrough can return YAML, CSV, Prometheus
// text, or a binary archive, and wrapping those in an envelope would corrupt
// them; the alternative (refusing to emit them) would make the command useless
// for the uploads and exports it exists to reach.
func emitAPIResponse(ctx context.Context, method, requestPath string, status int, contentType string, body []byte) error {
	if isHTMLResponse(contentType, body) {
		// An HTML body from an API path is almost always a login redirect or an
		// error page, not a result. Say so, because a caller piping this into a
		// parser will otherwise see a confusing failure downstream.
		output.FprintWarning(currentStderr(ctx),
			"the response is HTML, not API data — %s %s may not be an API endpoint on this environment",
			method, requestPath)
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return emitEmptyAPIResponse(ctx, status)
	}

	var decoded any
	if json.Unmarshal(trimmed, &decoded) != nil {
		// Not JSON: verbatim passthrough (the declared raw protocol class).
		if _, err := currentStdout(ctx).Write(body); err != nil {
			return err
		}
		// Only when a human is watching, and never under --agent: redirected output
		// must be byte-exact, or a downloaded archive no longer matches the checksum
		// the API reports.
		if writerIsTerminal(currentStdout(ctx)) && !agentMode(ctx) && body[len(body)-1] != '\n' {
			fmt.Fprintln(currentStdout(ctx))
		}
		return nil
	}

	if agentMode(ctx) || (outputFormat(ctx) != "" && outputFormat(ctx) != "table" && outputFormat(ctx) != "wide") {
		printer := newPrinterCtx(ctx)
		if ap := enrichAgent(printer, "exec", "api"); ap != nil {
			ap.Context().Suggestions = []string{
				"dtctl describe api <name> --operation '" + method + " <path>'  -- the operation's schema",
			}
		}
		return printer.Print(decoded)
	}

	// Human default: the body as served, indented. json.Indent preserves key
	// order, so this is the platform's own response — just readable.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, trimmed, "", "  "); err != nil {
		_, err := currentStdout(ctx).Write(body)
		return err
	}
	fmt.Fprintln(currentStdout(ctx), pretty.String())
	return nil
}

// emitEmptyAPIResponse reports a body-less success (a 204, or a 200 with nothing
// in it). Printing nothing at all would leave a caller unable to tell success
// from a swallowed response.
func emitEmptyAPIResponse(ctx context.Context, status int) error {
	if agentMode(ctx) {
		printer := newPrinterCtx(ctx)
		enrichAgent(printer, "exec", "api")
		return printer.Print(map[string]any{
			"status": status,
			"body":   nil,
		})
	}
	fmt.Fprintf(currentStdout(ctx), "%d %s (no body)\n", status, http.StatusText(status))
	return nil
}

// isHTMLResponse reports whether a response is an HTML document. The content type
// is checked first, and the body sniffed as a fallback: an SSO redirect page is
// not always labelled.
func isHTMLResponse(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	head := strings.ToLower(string(body[:min(len(body), 256)]))
	head = strings.TrimSpace(head)
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// apiRequestError turns a 4xx/5xx into a structured error that preserves what the
// platform said and adds what dtctl knows.
//
// dtctl deliberately does not validate a body against the specification before
// sending: specifications are per-environment and per-version, and a client-side
// validator that rejects a request the platform would have accepted is strictly
// worse than the platform's own 400. So the specification is used here, on the
// failure path, where it costs nothing when the call succeeds — the caller that
// guessed a payload learns what the payload should have been in one round trip
// instead of iterating blind.
func apiRequestError(method, requestPath string, status int, body []byte,
	apiName string, op *resapi.Operation, nativeCommand string) error {

	message := strings.TrimSpace(string(body))
	if len(message) > maxErrorBodyBytes {
		message = message[:maxErrorBodyBytes] + fmt.Sprintf("… (%d bytes truncated)",
			len(message)-maxErrorBodyBytes)
	}
	if message == "" {
		message = http.StatusText(status)
	}

	var suggestions []string
	switch {
	case op != nil:
		suggestions = append(suggestions, fmt.Sprintf(
			"dtctl describe api %s --operation '%s'  -- its parameters, request body schema, and required scope",
			quoteCommandArg(apiName), op.ID()))
		if len(op.Scopes) > 0 {
			suggestions = append(suggestions, fmt.Sprintf(
				"the specification declares scope %s for this operation",
				strings.Join(op.Scopes, ", ")))
		}
	case apiName != "":
		suggestions = append(suggestions, fmt.Sprintf(
			"dtctl describe api %s  -- this API's operations, to check the path and method",
			quoteCommandArg(apiName)))
	default:
		suggestions = append(suggestions,
			"dtctl get apis  -- the APIs this environment publishes, to check the path")
	}
	if nativeCommand != "" {
		suggestions = append(suggestions, fmt.Sprintf(
			"%s  -- the native dtctl command for this API (preferred)", nativeCommand))
	}

	return &diagnostic.Error{
		Operation:   fmt.Sprintf("call %s %s", method, requestPath),
		StatusCode:  status,
		Message:     message,
		Suggestions: suggestions,
	}
}

func init() {
}

func init() {
}
