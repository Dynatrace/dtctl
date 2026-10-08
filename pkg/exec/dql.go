package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/aidetect"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/version"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// Re-export SDK types so existing callers continue to compile.
type (
	FilterSegmentRef      = sdkquery.FilterSegmentRef
	FilterSegmentVariable = sdkquery.FilterSegmentVariable
	DQLQueryRequest       = sdkquery.ExecuteRequest
	DQLQueryResponse      = sdkquery.Response
	DQLResult             = sdkquery.Result
	DQLMetadata           = sdkquery.Metadata
	GrailMetadata         = sdkquery.GrailMetadata
	Contributions         = sdkquery.Contributions
	BucketContribution    = sdkquery.BucketContribution
	QueryNotification     = sdkquery.Notification
	ColumnTypes           = sdkquery.ColumnTypes
	ColumnType            = sdkquery.ColumnType
	AnalysisTimeframe     = sdkquery.AnalysisTimeframe
	MetricInfo            = sdkquery.MetricInfo
	DQLVerifyRequest      = sdkquery.VerifyRequest
	DQLVerifyResponse     = sdkquery.VerifyResponse
	MetadataNotification  = sdkquery.VerifyNotification
	SyntaxPosition        = sdkquery.SyntaxPosition
	Position              = sdkquery.Position
	QueryError            = sdkquery.QueryError
	QueryStateError       = sdkquery.StateError
)

// DQLExecutor handles DQL query execution
type DQLExecutor struct {
	client         *client.Client
	sdk            *sdkquery.Handler
	tokenRefresher func() (string, error)
	// probe overrides the runner for empty-result diagnostic queries (tests).
	probe probeFunc
	// stdout and stderr receive the executor's output; nil means the process
	// streams. vfsEnv resolves query files; the zero value is the process's.
	stdout io.Writer
	stderr io.Writer
	vfsEnv vfs.Env
}

// NewDQLExecutor creates a new DQL executor
func NewDQLExecutor(c *client.Client) *DQLExecutor {
	sdk := sdkquery.NewHandler(httpclient.Wrap(c.HTTP()))
	return &DQLExecutor{client: c, sdk: sdk}
}

// WithStreams directs the executor's results to stdout and its notices,
// progress and diagnostics to stderr, for a caller that owns the invocation's
// streams. nil keeps the process stream.
func (e *DQLExecutor) WithStreams(stdout, stderr io.Writer) *DQLExecutor {
	e.stdout, e.stderr = stdout, stderr
	return e
}

// WithVFS sets the filesystem and stdin the executor reads query files from.
// The zero Env keeps the process's.
func (e *DQLExecutor) WithVFS(env vfs.Env) *DQLExecutor {
	e.vfsEnv = env
	return e
}

func (e *DQLExecutor) outW() io.Writer {
	if e.stdout == nil {
		return os.Stdout
	}
	return e.stdout
}

func (e *DQLExecutor) errW() io.Writer {
	if e.stderr == nil {
		return os.Stderr
	}
	return e.stderr
}

// WithTokenRefresher sets an optional callback that is invoked when a poll request
// receives a 401 Unauthorized response (e.g. because the OAuth token expired during
// a long-running query). The callback must return a fresh access token. The executor
// updates the underlying HTTP client with the new token and retries the poll.
func (e *DQLExecutor) WithTokenRefresher(refresher func() (string, error)) *DQLExecutor {
	e.tokenRefresher = refresher
	return e
}

// dtClientContextHeader builds the JSON value for the dt-client-context HTTP header.
// callerContext is the optional caller-supplied semantic string (empty = omit field).
func dtClientContextHeader(callerContext string) string {
	type payload struct {
		App     string `json:"app"`
		Version string `json:"version"`
		Agent   string `json:"agent,omitempty"`
		Context string `json:"context,omitempty"`
	}
	p := payload{App: "dtctl", Version: version.Version, Context: callerContext}
	if info := aidetect.Detect(); info.Detected {
		p.Agent = info.Name
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// sdkHandler returns the SDK handler with the dt-client-context header set.
func (e *DQLExecutor) sdkHandler(clientContext string) *sdkquery.Handler {
	return e.sdk.WithHeaders(map[string]string{
		"dt-client-context": dtClientContextHeader(clientContext),
	})
}

// DecodeMode controls snapshot payload decoding behavior.
type DecodeMode int

const (
	// DecodeNone disables snapshot decoding (default).
	DecodeNone DecodeMode = iota
	// DecodeSimplified decodes and simplifies variant wrappers to plain values.
	DecodeSimplified
	// DecodeFull decodes but preserves the full variant tree with type annotations.
	DecodeFull
)

// DQLExecuteOptions configures DQL query execution
type DQLExecuteOptions struct {
	// Output formatting options
	OutputFormat string
	JQFilter     string // jq filter expression applied before rendering
	AgentMode    bool   // Enable agent mode (e.g. for Dynatrace API)
	// AutoFormatByDefault reports that OutputFormat is auto only because it is
	// the agent-mode default (no -o given), so a non-JSON result carries the
	// -o json opt-out suggestion.
	AutoFormatByDefault bool
	Decode              DecodeMode // Snapshot payload decoding mode
	Width               int        // Chart width (0 = default)
	Height              int        // Chart height (0 = default)
	Fullscreen          bool       // Use terminal dimensions for chart

	// Query limit options
	MaxResultRecords       int64   // Maximum number of result records (0 = use default)
	MaxResultBytes         int64   // Maximum result size in bytes (0 = use default)
	DefaultScanLimitGbytes float64 // Scan limit in gigabytes (0 = use default)

	// Query execution options
	DefaultSamplingRatio         float64 // Sampling ratio (0 = use default, normalized to power of 10 <= 100000)
	FetchTimeoutSeconds          int32   // Time limit for fetching data in seconds (0 = use default)
	EnablePreview                bool    // Request preview results
	EnforceQueryConsumptionLimit bool    // Enforce query consumption limit
	IncludeTypes                 bool    // Include type information in results (default: true)
	IncludeContributions         bool    // Include bucket contribution information in results

	// EmitTypes surfaces the DQL per-column type block as a top-level "types"
	// key alongside "records" in structured (json/yaml) output. It is set only
	// when the user explicitly passed --include-types, so the metadata that
	// parquet/--typed request internally is not accidentally emitted. Has no
	// effect unless the API actually returned type information.
	EmitTypes bool

	// TypesRequested reports that the user explicitly asked for the type block:
	// --include-types was passed and is true. EmitTypes is set whenever the flag
	// was passed at all, --include-types=false included; the "has no effect"
	// warning keys off this field instead, so declining the block never warns
	// about not getting it. It does not affect what is printed on stdout.
	TypesRequested bool

	// Typed opts in to casting scalar columns (long, double, duration, boolean)
	// to their native JSON/YAML types using the DQL type metadata, instead of the
	// wire form where integer-valued columns arrive as strings. Off by default so
	// output stays faithful to the API; implies IncludeTypes so the mapping is
	// available. See output.ApplyTrueTypes.
	Typed bool

	// Compact omits null values and hoists the columns that hold one value in
	// every row into a `constant` map (#578). It applies to the agent envelope
	// (inline records and the spill manifest) and to plain json/yaml/toon
	// output; the command layer defaults it on in agent mode. Never applied
	// under --jq, whose program addresses the full rows.
	Compact bool

	// Series selects how the numeric arrays of timeseries records are rendered
	// (--series): full (default, unchanged), a per-series summary, or
	// extreme-preserving downsampling. See output.ApplySeriesMode.
	Series output.SeriesMode

	// Precision rounds every float in the records to this many significant
	// digits (--precision); 0 leaves values unchanged.
	Precision int

	// SeriesDefaulted and PrecisionDefaulted mark Series/Precision as the
	// agent-mode defaults rather than the caller's explicit choice. When a
	// defaulted value actually changed the result, the agent envelope carries
	// one suggestion naming the opt-out.
	SeriesDefaulted    bool
	PrecisionDefaulted bool

	// seriesAdvice is that suggestion, computed by printResults.
	seriesAdvice string

	// Timeframe options
	DefaultTimeframeStart string // Query timeframe start timestamp (ISO-8601/RFC3339)
	DefaultTimeframeEnd   string // Query timeframe end timestamp (ISO-8601/RFC3339)

	// Localization options
	Locale   string // Query locale (e.g., "en_US")
	Timezone string // Query timezone (e.g., "UTC", "Europe/Paris")

	// ShowProgress opts in to the live progress bar drawn on stderr while an
	// asynchronous query is polled. It is off by default so internal/library
	// query callers (e.g. describe sub-queries, lookup metadata fetches) stay
	// silent; only the user-facing `query` command sets it. Even when set, the
	// bar is drawn only for interactive terminals and never affects stdout, so
	// structured/piped output is unchanged regardless.
	ShowProgress bool

	// Metadata options
	MetadataFields []string // Metadata fields to include; nil/empty = disabled, ["all"] = all fields, ["minimal"] = lean agent set, specific names = filtered

	// MetadataDefaulted records that agent mode chose MetadataFields
	// (["minimal"]) because -M was not given, so an envelope that dropped
	// something names the -M=all opt-out.
	MetadataDefaulted bool

	// Verbose mirrors -v. Under --metadata=minimal it keeps the inline spill
	// decision's measurement details (threshold/measured bytes) in the envelope.
	Verbose bool

	// Segment options
	Segments []FilterSegmentRef // Filter segments to apply to the query

	// ClientContext is an optional caller-supplied semantic string included as the "context"
	// field in the dt-client-context request header (e.g. "root-cause-analysis").
	ClientContext string

	// Spill holds the fully-resolved result-spill settings. When Spill.Enabled()
	// is false (mode never) the spill path is bypassed entirely and output is
	// unchanged from today's behaviour.
	Spill SpillOptions

	// MaxFieldChars clips every string value in an agent-mode result to this
	// many runes, ending a clipped value in "…(+N chars)" (0 = no cap). Spilled
	// files always keep the full values. The command layer defaults it to
	// output.DefaultAgentMaxFieldChars in agent mode.
	MaxFieldChars int

	// MaxOutputBytes bounds the agent-mode envelope, measured on the bytes
	// actually written to stdout (0 = no budget). An inline result over it keeps
	// the leading rows that fit and marks the cut on the context.
	MaxOutputBytes int64

	// TenantID and ContextName are provenance recorded in the spill manifest and
	// used to partition the spill directory by context (D9). They are not part of
	// query execution and are only consulted on the spill path.
	TenantID    string
	ContextName string

	// Decorate amends the agent envelope context; records is nil when rows streamed to disk.
	Decorate func(ctx *output.ResponseContext, result *DQLQueryResponse, records []map[string]interface{})

	// OnRows, when set, is called once with the number of records, on every
	// path that prints them, and always before Decorate: before the rows on
	// the buffered path, after them on the streaming path. Unlike Decorate it
	// runs for every output format.
	OnRows func(n int)

	// TypedQuery is the query as the user wrote it, when the caller sends a
	// rewritten one. Empty-result diagnosis reads the fields it checks from
	// it, so it never blames a field the caller inserted.
	TypedQuery string
}

func (opts DQLExecuteOptions) reportRows(n int) {
	if opts.OnRows != nil {
		opts.OnRows(n)
	}
}

// DQLVerifyOptions configures DQL query verification
type DQLVerifyOptions struct {
	GenerateCanonicalQuery bool   // Generate a canonical (normalized) version of the query
	Timezone               string // Query timezone (e.g., "UTC", "Europe/Paris")
	Locale                 string // Query locale (e.g., "en_US")
	ClientContext          string // Optional caller-supplied semantic string for the dt-client-context header
}

// buildExecuteRequest converts CLI options to an SDK ExecuteRequest.
func buildExecuteRequest(query string, opts DQLExecuteOptions) sdkquery.ExecuteRequest {
	req := sdkquery.ExecuteRequest{
		Query: query,
	}

	if opts.MaxResultRecords > 0 {
		req.MaxResultRecords = opts.MaxResultRecords
	}
	if opts.MaxResultBytes > 0 {
		req.MaxResultBytes = opts.MaxResultBytes
	}
	if opts.DefaultScanLimitGbytes > 0 {
		req.DefaultScanLimitGbytes = opts.DefaultScanLimitGbytes
	}
	if opts.DefaultSamplingRatio > 0 {
		req.DefaultSamplingRatio = opts.DefaultSamplingRatio
	}
	if opts.FetchTimeoutSeconds > 0 {
		req.FetchTimeoutSeconds = opts.FetchTimeoutSeconds
	}
	if opts.EnablePreview {
		req.EnablePreview = true
	}
	if opts.EnforceQueryConsumptionLimit {
		req.EnforceQueryConsumptionLimit = true
	}
	if opts.IncludeTypes {
		includeTypes := true
		req.IncludeTypes = &includeTypes
	}
	if opts.IncludeContributions {
		includeContributions := true
		req.IncludeContributions = &includeContributions
	}
	if opts.DefaultTimeframeStart != "" {
		req.DefaultTimeframeStart = opts.DefaultTimeframeStart
	}
	if opts.DefaultTimeframeEnd != "" {
		req.DefaultTimeframeEnd = opts.DefaultTimeframeEnd
	}
	if opts.Locale != "" {
		req.Locale = opts.Locale
	}
	if opts.Timezone != "" {
		req.Timezone = opts.Timezone
	}
	if len(opts.Segments) > 0 {
		req.FilterSegments = opts.Segments
	}
	// Request metric-catalogue enrichment (displayName/description/unit on
	// metadata.metrics[]) only when the caller actually wants metrics metadata.
	if wantsMetricsMetadata(opts.MetadataFields) {
		req.EnrichMetricMetadata = true
	}

	return req
}

// wantsMetricsMetadata reports whether the requested metadata fields include
// metric metadata — either explicitly ("metrics") or via the "all" selector.
func wantsMetricsMetadata(fields []string) bool {
	for _, f := range fields {
		if f == "all" || f == "metrics" {
			return true
		}
	}
	return false
}

// Execute executes a DQL query
func (e *DQLExecutor) Execute(query string, outputFormat string) error {
	return e.ExecuteWithOptions(query, DQLExecuteOptions{OutputFormat: outputFormat})
}

// ExecuteWithOptions executes a DQL query with full options
func (e *DQLExecutor) ExecuteWithOptions(query string, opts DQLExecuteOptions) error {
	return e.ExecuteWithContext(context.Background(), query, opts)
}

// ExecuteWithContext executes a DQL query with a cancellable context and prints the results.
func (e *DQLExecutor) ExecuteWithContext(ctx context.Context, query string, opts DQLExecuteOptions) error {
	// When the destination takes rows one at a time, decode them straight into
	// it instead of holding the whole result (see dql_stream.go). A small result
	// still takes the buffered path below, byte for byte.
	if plan, ok := e.planStream(opts); ok {
		return e.executeStreaming(ctx, query, opts, plan)
	}

	result, err := e.ExecuteQueryWithContext(ctx, query, opts)
	if err != nil {
		return err
	}
	if result == nil {
		return nil // context was cancelled; message already printed to stderr
	}
	opts.reportRows(len(result.GetRecords()))
	return e.printResults(query, result, opts)
}

// ExecuteQuery executes a DQL query and returns the raw result
func (e *DQLExecutor) ExecuteQuery(query string) (*DQLQueryResponse, error) {
	return e.ExecuteQueryWithOptions(query, DQLExecuteOptions{})
}

// ExecuteQueryWithOptions executes a DQL query with options and returns the raw result
func (e *DQLExecutor) ExecuteQueryWithOptions(query string, opts DQLExecuteOptions) (*DQLQueryResponse, error) {
	return e.ExecuteQueryWithContext(context.Background(), query, opts)
}

// ExecuteQueryWithContext executes a DQL query with a cancellable context.
// If ctx is cancelled while the query is polling, a best-effort cancel request is sent
// to the Grail backend before returning.
func (e *DQLExecutor) ExecuteQueryWithContext(ctx context.Context, query string, opts DQLExecuteOptions) (*DQLQueryResponse, error) {
	return e.runQuery(ctx, query, opts, nil)
}

// runQuery drives execute + poll. With sink nil the response carries the rows,
// as every caller of ExecuteQueryWithContext expects; with a sink the rows are
// delivered to it as they decode and the response comes back without them (see
// dql_stream.go).
func (e *DQLExecutor) runQuery(ctx context.Context, query string, opts DQLExecuteOptions, sink *streamCollector) (*DQLQueryResponse, error) {
	req := buildExecuteRequest(query, opts)
	handler := e.sdkHandler(opts.ClientContext)

	// Build the token refresher callback for the SDK. The SDK's ExecuteAndPoll will
	// call this on 401; we refresh the token and update the underlying HTTP client.
	var onUnauthorized func() (string, error)
	if e.tokenRefresher != nil {
		onUnauthorized = func() (string, error) {
			newToken, err := e.tokenRefresher()
			if err != nil {
				return "", err
			}
			e.client.SetToken(newToken)
			return newToken, nil
		}
	}

	// Live progress on stderr. The reporter is a no-op unless stderr is an
	// interactive TTY, so piped/agent/structured output is untouched. Stop()
	// erases the line on error and cancellation; Complete() replaces it with a
	// summary on success.
	reporter := output.NewProgressReporterTo(opts.ShowProgress, opts.AgentMode, e.errW())
	defer reporter.Stop()

	// Remember the last live scan totals so the completion summary can fall back
	// to them if the terminal result metadata omits scannedBytes/Records. The
	// closure runs synchronously in this goroutine, so these need no locking.
	var lastScannedBytes, lastScannedRecords int64
	var onRecord func(map[string]interface{}) error
	if sink != nil {
		onRecord = sink.observe
		// Rows streamed to stdout arrive while the bar may still be redrawing on
		// stderr. The gate only releases rows once the query has finished, so
		// the bar is settled then, with the live scan totals: the final metadata
		// follows the rows.
		sink.beforeStdout = func() {
			reporter.Complete(output.ProgressState{Progress: 100, ScannedBytes: lastScannedBytes, ScannedRecords: lastScannedRecords})
		}
	}
	result, err := streamCall(ctx, handler, req, sdkquery.ExecuteAndPollOptions{
		OnUnauthorized: onUnauthorized,
		OnUpdate: func(u sdkquery.PollUpdate) {
			state := output.ProgressState{
				Progress:       u.Progress,
				ScannedBytes:   u.ScannedBytes,
				ScannedRecords: u.ScannedRecords,
			}
			if u.Preview != nil {
				state.PreviewRows = len(u.Preview.Records)
			}
			if u.ScannedBytes > 0 {
				lastScannedBytes = u.ScannedBytes
			}
			if u.ScannedRecords > 0 {
				lastScannedRecords = u.ScannedRecords
			}
			reporter.Update(state)
		},
	}, onRecord)
	if err != nil {
		// Clear the bar before any further stderr output (cancellation notice,
		// error hints). Stop is idempotent; the defer remains a safety net.
		drawing := reporter.Drawing()
		reporter.Stop()
		// If context was cancelled, print cancellation message. When the reporter
		// was drawing, Stop() above already cleared the line, so no leading
		// newline is needed. When it was not (--no-progress, --plain, non-TTY),
		// emit one to separate the message from a shell's "^C" echo.
		if ctx.Err() != nil {
			if !drawing {
				fmt.Fprintln(e.errW())
			}
			fmt.Fprintln(e.errW(), "Query cancelled.")
			return nil, nil
		}
		// Enhance known error types with CLI-specific hints.
		var qErr *QueryError
		if isQueryError(err, &qErr) {
			switch qErr.ErrorType {
			case "FILTER_SEGMENT_REQUIRES_VARIABLE":
				return nil, formatSegmentVariableError(qErr)
			case "PARSE_ERROR_SINGLE_QUOTES":
				return nil, formatSingleQuoteError(qErr)
			}
		}
		return nil, err
	}

	// On success, replace the bar with a completion summary carrying the final
	// scan totals. Prefer the result metadata (authoritative), but fall back to
	// the last live totals when the terminal envelope omits them, so a large scan
	// the user watched climb is never reported as "done in Xs" with no volume.
	final := output.ProgressState{Progress: 100, ScannedBytes: lastScannedBytes, ScannedRecords: lastScannedRecords}
	if m := result.GetMetadata(); m != nil {
		if m.ScannedBytes > 0 {
			final.ScannedBytes = m.ScannedBytes
		}
		if m.ScannedRecords > 0 {
			final.ScannedRecords = m.ScannedRecords
		}
	}
	reporter.Complete(final)

	return result, nil
}

// isQueryError extracts a *QueryError from the error chain.
func isQueryError(err error, target **QueryError) bool {
	return errors.As(err, target)
}

// formatSegmentVariableError produces a helpful error message when a segment
// requires variable bindings, including ready-to-use -S inline and --segments-file examples.
func formatSegmentVariableError(qErr *QueryError) error {
	args := qErr.Arguments
	segmentID := "unknown"
	variableName := "unknown"
	if len(args) >= 1 {
		segmentID = strings.Trim(args[0], "`")
	}
	if len(args) >= 3 {
		variableName = strings.TrimPrefix(args[2], "$")
	}

	return fmt.Errorf("segment %s requires variable %q\n\n"+
		"Bind the variable inline on -S using URL-query syntax:\n\n"+
		"  dtctl query \"...\" -S \"%s?%s=your-value-here\"\n\n"+
		"Or use --segments-file with a YAML file for complex cases:\n\n"+
		"  # segments.yaml\n"+
		"  - id: %s\n"+
		"    variables:\n"+
		"      - name: %s\n"+
		"        values: [\"your-value-here\"]\n\n"+
		"  dtctl query \"...\" --segments-file segments.yaml",
		segmentID, variableName, segmentID, variableName, segmentID, variableName)
}

// formatSingleQuoteError produces a helpful error when a query uses single
// quotes for a string literal. DQL only accepts double quotes; single quotes
// are almost always the result of a shell-quoting collision — wrapping the
// whole query in double quotes leaves no room for double-quoted string
// literals, so users reach for single quotes instead. The wrapped *QueryError
// is preserved in the chain so callers (agent envelope, wait fast-fail) can
// still classify it. Advice is tailored per platform.
func formatSingleQuoteError(qErr *QueryError) error {
	return fmt.Errorf("%w\n\n%s", qErr, singleQuoteHint())
}

// singleQuoteHint returns platform-specific guidance for fixing a single-quoted
// DQL string literal. On Windows it spells out the PowerShell and cmd.exe
// forms, since that is where the double-quote collision bites hardest.
func singleQuoteHint() string {
	const base = "DQL string literals must use double quotes (\"...\"), not single quotes ('...')."
	if runtime.GOOS == "windows" {
		return base + "\n\n" +
			"Your shell most likely stripped the double quotes. Quote the query so the inner \" survive:\n\n" +
			"  PowerShell:  dtctl query 'fetch logs | filter status == \"ERROR\"'\n" +
			"  cmd.exe:     dtctl query \"fetch logs | filter status == \\\"ERROR\\\"\"\n\n" +
			"Or avoid shell quoting entirely by reading the query from a file or stdin:\n\n" +
			"  dtctl query -f query.dql\n" +
			"  dtctl query -f -   # then type/pipe the query on stdin"
	}
	return base + "\n\n" +
		"Wrap the query in single quotes and use double quotes for values inside:\n\n" +
		"  dtctl query 'fetch logs | filter status == \"ERROR\"'"
}

// VerifyQuery verifies a DQL query without executing it
func (e *DQLExecutor) VerifyQuery(query string, opts DQLVerifyOptions) (*DQLVerifyResponse, error) {
	return e.VerifyQueryWithContext(context.Background(), query, opts)
}

// VerifyQueryWithContext is VerifyQuery bound to the caller's context, so an
// embedding caller that goes away stops waiting on the verification.
func (e *DQLExecutor) VerifyQueryWithContext(ctx context.Context, query string, opts DQLVerifyOptions) (*DQLVerifyResponse, error) {
	req := sdkquery.VerifyRequest{
		Query:                  query,
		GenerateCanonicalQuery: opts.GenerateCanonicalQuery,
		Timezone:               opts.Timezone,
		Locale:                 opts.Locale,
	}

	handler := e.sdkHandler(opts.ClientContext)

	// Create context with 30-second timeout (verify is fast)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return handler.Verify(ctx, req)
}

// Notification categories. A query notification is mapped to one of these so a
// single set of advice serves both the human stderr hint and the agent-envelope
// suggestions. The empty string means "no specific advice".
const (
	notifScanLimit   = "scan_limit"
	notifResultLimit = "result_limit"
	notifTimeout     = "timeout"
	notifSampling    = "sampling"
	notifConsumption = "consumption"
)

// classifyNotification maps a query notification (by type, falling back to a
// message pattern) to one of the notif* categories. The message fallback exists
// because not every deployment tags notifications with a stable type.
func classifyNotification(notificationType, message string) string {
	switch notificationType {
	case "SCAN_LIMIT_GBYTES":
		return notifScanLimit
	case "RESULT_LIMIT_RECORDS", "RESULT_LIMIT_BYTES":
		return notifResultLimit
	case "FETCH_TIMEOUT", "FETCH_EXEC_TIME_LIMIT":
		return notifTimeout
	case "SAMPLING_APPLIED":
		return notifSampling
	case "QUERY_CONSUMPTION_LIMIT":
		return notifConsumption
	}

	msg := strings.ToLower(message)
	switch {
	case strings.Contains(msg, "scan") && strings.Contains(msg, "gigabyte"):
		return notifScanLimit
	case strings.Contains(msg, "result has been limited") || strings.Contains(msg, "limited to"):
		return notifResultLimit
	case strings.Contains(msg, "internal time limit"):
		// Grail cuts a slow read short and says so only in the message on some
		// deployments; without this the result reads as complete and every row
		// below the cut becomes a fabricated absence.
		return notifTimeout
	}
	return ""
}

// Causes of an incomplete result, as reported by PartialCause. They are
// distinguished because the remedies are not interchangeable: a scan cap wants
// less data scanned or a bigger cap, a result cap wants aggregation, a timeout
// wants a narrower read. Collapsing them into one "cut short by a limit"
// message forces the reader to guess which of three fixes applies.
const (
	PartialScanLimit   = notifScanLimit
	PartialResultLimit = notifResultLimit
	PartialTimeout     = notifTimeout
	PartialConsumption = notifConsumption
)

// PartialCause reports why a query notification means the result is
// incomplete, as one of the Partial* constants, or "" if the notification does
// not truncate. Sampling is excluded — it is declared in the query text, not a
// silent truncation.
func PartialCause(n QueryNotification) string {
	switch c := classifyNotification(n.NotificationType, n.Message); c {
	case notifResultLimit, notifScanLimit, notifTimeout, notifConsumption:
		return c
	}
	return ""
}

// ResultIsPartial reports whether a query notification means the result is
// incomplete: a record/byte cap, scan limit, fetch timeout, or consumption
// stop cut it short.
func ResultIsPartial(n QueryNotification) bool {
	return PartialCause(n) != ""
}

// getHintForNotification returns a concise CLI hint (a single line, for stderr)
// for a given notification type or message. For a truncating notification it
// leads with data-reduction advice — raising the limit is the expensive last
// resort, not the first suggestion.
func getHintForNotification(notificationType, message string) string {
	switch classifyNotification(notificationType, message) {
	case notifScanLimit:
		return "Result is PARTIAL (scan stopped early). Reduce data scanned — narrow the timeframe, restrict to specific buckets (bucket:{...}; use --include-contributions to find the heavy ones), filter early with == (not contains()), or sample logs/spans (add samplingRatio:1000 to the fetch, or pass --default-sampling-ratio). Raising --default-scan-limit-gbytes <value> works but costs more."
	case notifResultLimit:
		return "Result was truncated. Aggregate in DQL (| summarize ...) to shrink it, or raise --max-result-records / --max-result-bytes <value>."
	case notifTimeout:
		return "Fetch timed out (result may be incomplete). Narrow the timeframe or add filters, or raise --fetch-timeout-seconds <value>."
	case notifSampling:
		return "Results are sampled/extrapolated (approximate). For exact values remove samplingRatio from the fetch, or pass --default-sampling-ratio 1 (higher scan cost)."
	case notifConsumption:
		return "Query hit a consumption limit. Reduce the data scanned, or pass --enforce-query-consumption-limit=false to bypass."
	}
	return ""
}

// agentAdviceForNotification returns agent-envelope suggestion lines (the "# ..."
// style used across the spill envelope) for a notification. Unlike the one-line
// human hint, it spells out the concrete alternatives an agent can act on and
// leads with the fact that the result may be incomplete.
func agentAdviceForNotification(notificationType, message string) []string {
	switch classifyNotification(notificationType, message) {
	case notifScanLimit:
		return []string{
			"# the scan limit was reached — this result is PARTIAL (the scan stopped early), not the full dataset",
			"# reduce the DATA SCANNED (aggregation alone does not help — it still scans everything). Raising the limit works but costs more and is slower:",
			"#   - narrow the timeframe, e.g. from:now()-1h instead of a wide range (usually the biggest lever)",
			"#   - restrict to the relevant bucket(s): fetch logs, bucket:{\"<bucket>\"}",
			"#   - find which bucket dominates the scan: dtctl query --include-contributions --metadata=contributions -o json '<query>' and read matchedRecordsRatio per bucket",
			"#   - filter early and prefer equality, e.g. | filter <field> == \"...\" (== scans far less than contains()/matchesPhrase())",
			"#   - for logs/spans, sample so only a fraction is scanned: add samplingRatio to the fetch, e.g. fetch logs, samplingRatio:1000 (DQL-native, portable) — or pass dtctl's --default-sampling-ratio 1000; extrapolate counts with sum(dt.system.sampling_ratio)",
			"# only if you truly need the full scan: --default-scan-limit-gbytes <value> (higher cost & duration; -1 = unlimited)",
		}
	case notifResultLimit:
		return []string{
			"# the result was truncated to the record/byte limit — rows are missing",
			"# aggregate in DQL (| summarize ...) to shrink the result, or raise the cap with --max-result-records <N> / --max-result-bytes <N>",
		}
	case notifTimeout:
		return []string{
			"# the fetch timed out — this result may be incomplete",
			"# narrow the timeframe or add filters to speed the query, or raise --fetch-timeout-seconds <N>",
		}
	case notifSampling:
		return []string{
			"# results are sampled/extrapolated (approximate, not exact); for exact values remove samplingRatio from the fetch (or pass --default-sampling-ratio 1) — higher scan cost",
		}
	case notifConsumption:
		return []string{
			"# the query hit a consumption limit and was stopped — this result may be incomplete",
			"# reduce the data scanned (narrower timeframe/filters), or pass --enforce-query-consumption-limit=false to bypass",
		}
	}
	return nil
}

// notificationAdvice converts query notifications into agent-envelope warnings
// and suggestions. Unlike PrintNotifications (which writes to stderr for a
// human), this surfaces the same information inside the JSON envelope on stdout,
// so an agent parsing the result on stdout learns the result may be PARTIAL and
// what to do next — otherwise a truncated scan looks like a complete answer.
func notificationAdvice(notifications []QueryNotification) (warnings, suggestions []string) {
	for _, n := range notifications {
		switch strings.ToUpper(n.Severity) {
		case "WARNING", "WARN", "ERROR":
		default:
			continue
		}
		if n.Message != "" {
			warnings = append(warnings, n.Message)
		}
		suggestions = append(suggestions, agentAdviceForNotification(n.NotificationType, n.Message)...)
	}
	return warnings, suggestions
}

// approximationPrefix leads every approximation line, on stderr and in the
// agent envelope's warnings alike.
const approximationPrefix = "Result is approximate: "

// approximationWarnings turns Grail's metadata.grail.approximations into
// warning lines. An approximation is a successful query that answered a
// slightly different question than the one written (e.g. a substring match
// where the query asked for a token match); without the line the caller reads
// the number as exact, and nothing else in the output hints otherwise (#415).
func approximationWarnings(result *DQLQueryResponse) []string {
	if result == nil {
		return nil
	}
	approximations := result.GetApproximations()
	if len(approximations) == 0 {
		return nil
	}
	warnings := make([]string, 0, len(approximations))
	for _, a := range approximations {
		warnings = append(warnings, approximationPrefix+a)
	}
	return warnings
}

// heavyScanWarnBytes is the scanned-data level above which the agent envelope
// warns. An agent that cannot see the bill will happily re-run an 85 GB scan
// it already paid for; the warning makes the cost visible and steers toward
// reusing the result it just received.
const heavyScanWarnBytes = 10 * 1000 * 1000 * 1000

// heavyScanAdvice emits an envelope warning + suggestion when a query scanned
// a large amount of data, so the cost is visible in-band.
func heavyScanAdvice(result *DQLQueryResponse) (warnings, suggestions []string) {
	meta := extractQueryMetadata(result)
	if meta == nil || meta.ScannedBytes < heavyScanWarnBytes {
		return nil, nil
	}
	warnings = append(warnings, fmt.Sprintf(
		"this query scanned %.1f GB — re-running it costs the same again", float64(meta.ScannedBytes)/1e9))
	suggestions = append(suggestions,
		"# heavy scan: reuse this result (dtctl inspect on a spilled file) instead of re-querying; narrow the timeframe or bucket to reduce cost")
	return warnings, suggestions
}

// timestampFilterRe spots a filter stage constraining the timestamp field —
// the idiom agents reach for when they mean "query an older window".
var timestampFilterRe = regexp.MustCompile(`(?i)\|\s*filter\b[^|]*\btimestamp\b`)

// windowAdvice explains the silent-default-window trap on empty results.
// Fires only on an empty result (no rows, or the single all-zero row a
// `summarize count()` yields) from a query with no explicit window anywhere:
//   - with a `filter timestamp` stage: the filter cannot widen the scanned
//     window, so filtering for data older than the default returns nothing —
//     silently (the t23 trap).
//   - without one: the emptiness may still be the window, not the data —
//     discovery fetches (metric.series) only show series that had datapoints
//     inside the window, and agents reported "0 OOM pods" off exactly that
//     (the t9 trap). Phrased as a possibility, since zero may be the answer.
func windowAdvice(query string, records []map[string]interface{}, opts DQLExecuteOptions) []string {
	if opts.DefaultTimeframeStart != "" || opts.DefaultTimeframeEnd != "" {
		return nil
	}
	if !isEmptyResult(records) {
		return nil
	}
	if strings.Contains(query, "from:") || strings.Contains(query, "to:") || strings.Contains(query, "timeframe:") {
		return nil
	}
	if timestampFilterRe.MatchString(query) {
		return []string{"# empty result with a `filter timestamp ...` stage: timestamp filters do NOT widen the scanned window (default: the last 2h) — set the window in the fetch instead, e.g. `fetch logs, from:now()-24h, to:now()-12h`, or pass --default-timeframe-start/--default-timeframe-end"}
	}
	advice := "# empty result from the DEFAULT query window (the last 2h) — if data may exist outside it, widen the window explicitly, e.g. `, from:now()-7d`, before concluding the count is 0"
	if strings.Contains(query, "metric.series") {
		advice = "# empty result from the DEFAULT query window (the last 2h): metric.series lists only series with datapoints INSIDE the window — widen it for discovery, e.g. `fetch metric.series, from:now()-7d`"
	}
	return []string{advice}
}

// isEmptyResult reports whether a result carries no rows, or only the single
// all-zero row a `summarize count()` yields when nothing matched.
func isEmptyResult(records []map[string]interface{}) bool {
	return len(records) == 0 || (len(records) == 1 && allAggregatesZero(records[0]))
}

// AgentDefaultPrecision is the significant-digit rounding agent mode applies
// when --precision is not given.
const AgentDefaultPrecision = 4

// defaultSeriesAdvice names the opt-out for an agent-mode default that
// actually changed the result: a summarized series, a rounded number, or both.
// An explicit --series/--precision, or a default that changed nothing, gets no
// advice.
func defaultSeriesAdvice(effect output.SeriesEffect, opts DQLExecuteOptions) string {
	summarized := effect.Summarized && opts.SeriesDefaulted
	rounded := effect.Rounded && opts.PrecisionDefaulted
	switch {
	case summarized && rounded:
		// The summary's statistics lost digits, or --series=full alone would
		// bring the points back rounded, so name both flags: together they
		// restore the raw values exactly.
		return fmt.Sprintf("# timeseries summarized and numbers rounded to %d significant digits (agent-mode default) — add --series=full --precision 0 for the raw values", opts.Precision)
	case summarized:
		return "# timeseries summarized (agent-mode default) — add --series=full for the raw datapoints"
	case rounded:
		return fmt.Sprintf("# numbers rounded to %d significant digits (agent-mode default) — add --precision 0 for full precision", opts.Precision)
	}
	return ""
}

// seriesAdvice returns the agent-default opt-out hint, if any, as a slice to
// append to an envelope's suggestions.
func seriesAdvice(opts DQLExecuteOptions) []string {
	if opts.seriesAdvice == "" {
		return nil
	}
	return []string{opts.seriesAdvice}
}

// entityFetchRe spots a query fetching a classic dt.entity.* table, and
// captures the type suffix for a concrete smartscapeNodes suggestion.
var entityFetchRe = regexp.MustCompile(`(?i)\bfetch\s+dt\.entity\.([a-z0-9_]+)`)

// lookbackAdvice rides SUCCESSFUL dt.entity.* fetches. The error-side
// redirect (dqlErrorAdvice) cannot help here: dt.entity.<type> queries
// SUCCEED and return the lookback population (entities that reported events
// in the window), which diverges from the live topology — agents reported a
// 17-host census against 12 currently monitored hosts in five consecutive
// eval batches, every time via a query that returned rc 0.
func lookbackAdvice(query string) []string {
	m := entityFetchRe.FindStringSubmatch(query)
	if m == nil {
		return nil
	}
	t := strings.ToUpper(m[1])
	return []string{fmt.Sprintf("# dt.entity.%s is an event-LOOKBACK view (entities seen in the query window), not the live topology — for a current-state census or inventory use: dtctl query 'smartscapeNodes \"%s\" | summarize count()'", m[1], t)}
}

// sampleAdvice warns that an unsorted `| limit N` returns the first records read, not a sample.
func sampleAdvice(query string, rows int) []string {
	stages := splitStages(query)
	if len(stages) < 2 || fetchStageRe.FindStringSubmatch(stages[0]) == nil {
		return nil
	}
	sorted := false
	for _, st := range stages[1:] {
		cmd, rest := splitCommand(st)
		switch strings.ToLower(cmd) {
		case "summarize", "maketimeseries", "fieldssummary", "append", "join", "lookup":
			return nil
		case "sort":
			sorted = true
		case "limit":
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil || n != rows || sorted || n == 0 {
				return nil
			}
			return []string{fmt.Sprintf("# these %d rows are the first records `limit %d` let through, not a representative sample — they cannot show how often something occurs or that it never does; aggregate before concluding, e.g. `| summarize count(), by:{<field>}`", n, n)}
		}
	}
	return nil
}

// durationAdvice names duration columns, which arrive as nanosecond integers.
func durationAdvice(query string, result *DQLQueryResponse) []string {
	if result == nil {
		return nil
	}
	seen := map[string]bool{}
	var cols []string
	for _, g := range result.GetTypes() {
		for name, ct := range g.Mappings {
			if ct.Type == "duration" && !seen[name] {
				seen[name] = true
				cols = append(cols, name)
			}
		}
	}
	if len(cols) == 0 {
		return nil
	}
	sort.Strings(cols)
	if len(cols) > 4 {
		cols = cols[:4]
	}
	advice := fmt.Sprintf("# %s: durations in NANOSECONDS (1 ms = 1,000,000) — convert before reporting", backtickList(cols))
	if topLevelIndex(strings.ToLower(query), "summarize") < 0 && topLevelIndex(strings.ToLower(query), "maketimeseries") < 0 {
		advice += fmt.Sprintf("; one record's duration is not a typical one, use e.g. `| summarize p50 = percentile(%s, 50), p90 = percentile(%s, 90)`", cols[0], cols[0])
	}
	return []string{advice}
}

// allAggregatesZero reports whether a single result row carries only zero
// numeric values (DQL long aggregates arrive as JSON strings) — the shape a
// `summarize count()` produces when nothing matched.
func allAggregatesZero(rec map[string]interface{}) bool {
	zeros := 0
	for _, v := range rec {
		switch x := v.(type) {
		case float64:
			if x != 0 {
				return false
			}
			zeros++
		case json.Number:
			if f, err := x.Float64(); err == nil && f != 0 {
				return false
			}
			zeros++
		case string:
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				if f != 0 {
					return false
				}
				zeros++
			}
		}
	}
	return zeros > 0
}

// PrintNotifications prints query notifications/warnings to stderr
func (e *DQLExecutor) PrintNotifications(notifications []QueryNotification) {
	for _, n := range notifications {
		severity := n.Severity
		if severity == "" {
			severity = "INFO"
		}
		if severity == "WARNING" || severity == "WARN" {
			output.FprintWarning(e.errW(), "%s", n.Message)
			if hint := getHintForNotification(n.NotificationType, n.Message); hint != "" {
				output.FprintHint(e.errW(), "%s", hint)
			}
		} else if severity == "ERROR" {
			output.FprintHumanError(e.errW(), "%s", n.Message)
			if hint := getHintForNotification(n.NotificationType, n.Message); hint != "" {
				output.FprintHint(e.errW(), "%s", hint)
			}
		}
	}
}

// printResults prints the query results with the given options
func (e *DQLExecutor) printResults(query string, result *DQLQueryResponse, opts DQLExecuteOptions) error {
	return e.printRecords(query, result, result.GetRecords(), opts)
}

// printRecords is printResults with the rows supplied separately, so the
// streaming path can hand over a buffer it collected itself from a response
// that carries none (see dql_stream.go).
func (e *DQLExecutor) printRecords(query string, result *DQLQueryResponse, records []map[string]interface{}, opts DQLExecuteOptions) error {
	effectiveFormat := opts.OutputFormat
	if opts.JQFilter != "" {
		effectiveFormat = output.NormalizeJQOutputFormat(effectiveFormat)
	}

	// Notifications go to stderr ahead of the rows — except where the agent
	// envelope carries them in context.warnings/suggestions: printing them
	// there too made a host that merges the streams read the advice twice,
	// once as prose it cannot parse. An agent that asked for bytes the envelope
	// cannot wrap (-o csv/yaml) still gets them on stderr, below.
	notificationsPrinted := false
	printNotifications := func() {
		if notificationsPrinted {
			return
		}
		notificationsPrinted = true
		// Approximations ride with the notifications: stderr here, the
		// envelope's context.warnings in agent mode.
		for _, w := range approximationWarnings(result) {
			output.FprintWarning(e.errW(), "%s", w)
		}
		notifications := result.GetNotifications()
		if len(notifications) == 0 {
			return
		}
		e.PrintNotifications(notifications)
		if advice := unsortedSummarizeAdvice(query, notifications); advice != "" {
			output.FprintHint(e.errW(), "%s", advice)
		}
	}
	if !opts.AgentMode {
		printNotifications()
	}

	// Apply snapshot decoding if requested
	if opts.Decode != DecodeNone && len(records) > 0 {
		simplify := opts.Decode == DecodeSimplified
		records = output.DecodeSnapshotRecords(records, simplify)

		switch effectiveFormat {
		case "", "table", "wide", "csv":
			records = output.SummarizeSnapshotForTable(records)
		}
	}

	// --typed: cast scalar columns (long/double/duration → number, boolean →
	// bool) to their native types using the DQL type metadata, so structured
	// output is math-ready instead of carrying the API's string-encoded integers.
	// Opt-in; a no-op when types are absent. Applied before the spill/agent branch
	// so the agent envelope and spilled files see the same typed records.
	colTypes := columnTypeMappings(result)
	if opts.Typed {
		output.ApplyTrueTypes(records, colTypes)
	}

	// --series / --precision: compact timeseries arrays and round numbers.
	// Opt-in; applied before the spill/agent branch so the envelope, --jq and a
	// spilled file all see the same records. Table and CSV get a summary as one
	// readable cell instead of an object they would print as "<n items>" — but
	// not in agent mode, where the "table" default is an unset format and the
	// records go into the JSON envelope.
	if opts.Series.Kind != output.SeriesFull || opts.Precision > 0 {
		tabular := effectiveFormat == "csv"
		switch effectiveFormat {
		case "", "table", "wide":
			tabular = !opts.AgentMode
		}
		var effect output.SeriesEffect
		records, effect = output.ApplySeriesModeWithEffect(records, opts.Series, opts.Precision, tabular)
		opts.seriesAdvice = defaultSeriesAdvice(effect, opts)
	}

	// Spill path (D2/D3/D19-buffered): when spilling is enabled, a large result
	// is written to disk and a compact summary is emitted in place of the rows.
	// This is strictly additive — when it decides "inline" (or spilling is
	// disabled) it falls through to the unchanged output path below. Note: a
	// small/empty result under `auto` decides inline via the threshold, while
	// `always`/`--spill-to` honours the "write the file" contract even for an
	// empty result.
	//
	// Agent mode always enters this path even under --spill=never: the
	// spill-aware emitter is what produces the structured envelope, and a
	// `never` result decides inline via a self-describing kind:"records"
	// envelope (D2/D31) rather than reverting to a human table. The path still
	// falls through (handled=false) for an explicit non-JSON encoding or a --jq
	// transform, so an agent that asked for `-o toon`/`--jq` keeps that shape.
	if opts.Spill.Enabled() || opts.AgentMode {
		handled, err := e.trySpill(query, result, records, effectiveFormat, opts)
		if handled || err != nil {
			return err
		}
	}

	// Extract metadata if requested, resolving --metadata=minimal against this
	// response so every renderer below applies the same concrete selection.
	var meta *output.QueryMetadata
	var metaAdvice []string
	if len(opts.MetadataFields) > 0 {
		meta = extractQueryMetadata(result)
		metaAdvice = metadataDefaultAdvice(query, meta, opts, false)
		opts.MetadataFields = resolveMetadataFields(query, meta, opts)
	}

	// Agent mode + --jq: emit the ordinary agent envelope with the filter output
	// in `result`, so `ok` and `context` stay machine-checkable exactly as they
	// are without --jq (#413). The query path used to rewrite the filter into a
	// bare {result, metadata} object instead, which dropped `ok` — the one field
	// a consumer could have used to tell success from failure.
	if opts.AgentMode && opts.JQFilter != "" {
		return e.printAgentJQ(query, result, records, meta, effectiveFormat, opts, metaAdvice)
	}

	// No envelope from here on: stderr is the only channel left.
	printNotifications()

	// -o auto: pick the format from the rows, so the switch below prints exactly
	// what that explicit format would. With --jq the rows are not what gets
	// printed, so the auto printer decides after the filter instead.
	if output.IsAutoFormat(effectiveFormat) && opts.JQFilter == "" {
		choice := output.ChooseAutoFormat(records)
		output.FprintAutoChoice(e.errW(), choice)
		effectiveFormat = choice.Format
	}

	// An explicit --include-types asks for the type block as a sibling of
	// "records", which only the document-shaped encodings in the default branch
	// below have room for. Say so on stderr rather than dropping it silently
	// (#435); stdout is unchanged. Checked after -o auto has resolved, so an
	// auto run that picks csv warns and one that picks yaml does not.
	if opts.TypesRequested {
		if w := includeTypesInertWarning(effectiveFormat, len(records)); w != "" {
			output.FprintWarning(e.errW(), "%s", w)
		}
	}

	printer := output.NewPrinterWithOpts(output.PrinterOptions{
		Format:     effectiveFormat,
		Writer:     e.outW(),
		Notice:     e.errW(),
		JQFilter:   opts.JQFilter,
		Width:      opts.Width,
		Height:     opts.Height,
		Fullscreen: opts.Fullscreen,
		Types:      colTypes,
	})

	switch effectiveFormat {
	case "table", "wide":
		var err error
		if effectiveFormat == "table" {
			err = printer.PrintList(records)
		} else {
			if len(records) == 0 {
				return nil
			}
			err = printer.PrintList(records)
		}
		if err != nil {
			return err
		}
		if meta != nil {
			fmt.Fprint(e.errW(), output.FormatMetadataFooter(meta, opts.MetadataFields))
		}
		return nil

	case "csv":
		if len(records) == 0 {
			return nil
		}
		if meta != nil {
			fmt.Fprint(e.errW(), output.FormatMetadataCSVComments(meta, opts.MetadataFields))
		}
		return printer.PrintList(records)

	case "jsonl":
		// An empty JSONL file (zero lines) is valid output, so skip on no records.
		if len(records) == 0 {
			return nil
		}
		return printer.PrintList(records)

	case "parquet":
		// Always emit a Parquet file, even for an empty result: a zero-byte file
		// is not valid Parquet. The printer writes a valid schema-bearing file
		// with zero rows.
		return printer.PrintList(records)

	case "chart", "sparkline", "spark", "barchart", "bar", "braille", "br":
		if meta != nil {
			output.FprintWarning(e.errW(), "--metadata is not supported with chart output formats")
		}
		if len(records) > 0 {
			return printer.Print(map[string]interface{}{"records": records})
		}
		return printer.Print(result)

	default:
		out := make(map[string]interface{})
		if c := compactionFor(records, opts); c != nil && len(records) > 0 {
			// Map keys encode sorted, so `constant` lands before `records`.
			out["records"] = compactedRows(c, records, output.NormalizeMeasureEncoding(effectiveFormat))
			if len(c.Constant) > 0 {
				out["constant"] = c.Constant
			}
		} else if len(records) > 0 {
			out["records"] = records
		} else if result.Result != nil {
			// An empty result is still a result: emit an empty array rather than
			// the nil slice's `null`, which --jq could not tell apart from a
			// filter that addressed the wrong key (#413).
			out["records"] = []map[string]interface{}{}
		}
		if meta != nil {
			out["metadata"] = output.MetadataToMap(meta, opts.MetadataFields)
		}
		// Surface the DQL per-column type block (indexRange + mappings) as a
		// sibling of "records" when the user explicitly asked for it via
		// --include-types. JSONL has no place for a document-level sibling, so
		// it is skipped there; json/yaml carry it inline.
		if opts.EmitTypes && effectiveFormat != "jsonl" {
			if types := result.GetTypes(); len(types) > 0 {
				out["types"] = types
			}
		}
		if len(out) > 0 {
			return printer.Print(out)
		}
		return printer.Print(result)
	}
}

// includeTypesInertWarning returns the warning for an explicit --include-types
// whose type block the resolved output format cannot carry, or "" when the
// format emits it. It mirrors the format switch in printResults: every format
// with its own case there prints the rows alone, and only the default branch
// (json/yaml/toon — and, under --jq, the filter input) carries "types".
//
// The chart formats depend on rows, the number of records the chart branch
// sees. With none it hands the raw API response to the chart printer, which
// falls back to JSON and so prints the response's own "types" block; there is
// nothing to warn about then. With rows, it passes {"records": ...} alone, so
// even the not-chartable JSON fallback carries no types.
func includeTypesInertWarning(format string, rows int) string {
	switch format {
	case "chart", "sparkline", "spark", "barchart", "bar", "braille", "br":
		if rows == 0 {
			return ""
		}
		return fmt.Sprintf("--include-types has no effect with %s output (types are emitted for -o json/yaml/toon)", format)
	case "table", "wide", "csv", "jsonl":
		return fmt.Sprintf("--include-types has no effect with %s output (types are emitted for -o json/yaml/toon)", format)
	case "parquet":
		return "--include-types has no effect with parquet output (the column types are already encoded in the Parquet schema)"
	}
	return ""
}

// printAgentJQ emits an agent envelope whose result is the --jq output.
//
// The filter input is the result payload — {records, metadata?, types?} — which
// is both the shape the flag was requested with (#272: --jq '.records[].timestamp')
// and the same "the filter sees the payload, not the envelope" rule every other
// command follows. `records` is always present, even when empty, so --jq '.records'
// on an empty result yields [] rather than the null that a missing key would give.
func (e *DQLExecutor) printAgentJQ(query string, result *DQLQueryResponse, records []map[string]interface{}, meta *output.QueryMetadata, effectiveFormat string, opts DQLExecuteOptions, metaAdvice []string) error {
	if records == nil {
		records = []map[string]interface{}{}
	}
	payload := map[string]interface{}{"records": records}
	if meta != nil {
		payload["metadata"] = output.MetadataToMap(meta, opts.MetadataFields)
	}
	if opts.EmitTypes {
		if types := result.GetTypes(); len(types) > 0 {
			payload["types"] = types
		}
	}

	warnings, suggestions := queryNotificationAdvice(query, result.GetNotifications())
	warnings = append(warnings, approximationWarnings(result)...)
	scanWarnings, scanSuggestions := heavyScanAdvice(result)
	warnings = append(warnings, scanWarnings...)
	suggestions = append(suggestions, scanSuggestions...)
	emptyReason, emptySuggestions := e.emptyResultAdvice(query, result, records, opts)
	suggestions = append(suggestions, emptySuggestions...)
	suggestions = append(suggestions, lookbackAdvice(query)...)
	suggestions = append(suggestions, sampleAdvice(query, len(records))...)
	suggestions = append(suggestions, durationAdvice(query, result)...)
	suggestions = append(suggestions, metaAdvice...)
	suggestions = append(suggestions, seriesAdvice(opts)...)

	total := len(records)
	ctx := &output.ResponseContext{
		Verb:        "query",
		Resource:    resourceFromQuery(query),
		Total:       &total,
		Warnings:    warnings,
		Suggestions: suggestions,
		EmptyReason: emptyReason,
	}

	if opts.Decorate != nil {
		opts.Decorate(ctx, result, records)
	}

	// A filter's output has no row structure to cut at, so the budget cannot
	// drop rows from it; say so rather than silently exceed it.
	if opts.MaxOutputBytes > 0 {
		ctx.Warnings = append(ctx.Warnings, "--max-output-bytes/--max-output-tokens are not applied to --jq output; narrow the filter to bound it")
	}

	ap := output.NewAgentPrinter(e.outW(), ctx)
	ap.SetJQFilter(opts.JQFilter)
	// Clipped after the filter ran, so the filter matches on full values.
	ap.SetMaxFieldChars(opts.MaxFieldChars)
	// Keep metadata next to `result` as the unfiltered envelope does, so a
	// filter that narrows down to the rows doesn't silently drop it.
	ap.SetMetadata(envelopeMetadata(query, result, opts))
	// -o toon asked for a token-efficient encoding of the filtered result; keep
	// it. Any other non-JSON format the envelope can't carry warns for itself.
	// With no -o the result is JSON, not an auto-chosen yaml or csv string.
	if opts.AutoFormatByDefault {
		ap.SetResultFormat("json")
	} else {
		ap.SetResultFormat(effectiveFormat)
	}
	return ap.Print(payload)
}

// columnTypeMappings flattens the DQL per-column type info from the response
// (populated when includeTypes is set) into the output-layer representation used
// by the Parquet printer to build a schema. Returns nil when no type info is
// present. When multiple type groups disagree on a column, the first wins.
func columnTypeMappings(result *DQLQueryResponse) []output.ColumnTypeMapping {
	groups := result.GetTypes()
	if len(groups) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []output.ColumnTypeMapping
	for _, g := range groups {
		for name, ct := range g.Mappings {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, output.ColumnTypeMapping{Name: name, Type: ct.Type})
		}
	}
	return out
}

// extractQueryMetadata converts DQL response metadata to the output-layer QueryMetadata type.
// Grail metadata (execution stats, query text, ...) and Metrics (timeseries metric descriptors)
// are independent siblings of the response's metadata section, so either one being present is
// enough to produce a non-nil result — gating on Grail alone would silently drop Metrics for
// responses that populate metadata.metrics without metadata.grail.
func extractQueryMetadata(result *DQLQueryResponse) *output.QueryMetadata {
	g := result.GetMetadata()
	metrics := result.GetMetrics()
	if g == nil && len(metrics) == 0 {
		return nil
	}

	meta := &output.QueryMetadata{}

	if g != nil {
		meta.ExecutionTimeMilliseconds = g.ExecutionTimeMilliseconds
		meta.ScannedRecords = g.ScannedRecords
		meta.ScannedBytes = g.ScannedBytes
		meta.ScannedDataPoints = g.ScannedDataPoints
		meta.Sampled = g.Sampled
		meta.QueryID = g.QueryID
		meta.DQLVersion = g.DQLVersion
		meta.Query = g.Query
		meta.CanonicalQuery = g.CanonicalQuery
		meta.Timezone = g.Timezone
		meta.Locale = g.Locale

		if g.AnalysisTimeframe != nil {
			meta.AnalysisTimeframe = &output.MetadataTimeframe{
				Start: g.AnalysisTimeframe.Start,
				End:   g.AnalysisTimeframe.End,
			}
		}

		if g.Contributions != nil && len(g.Contributions.Buckets) > 0 {
			contribs := &output.MetadataContribs{}
			for _, b := range g.Contributions.Buckets {
				contribs.Buckets = append(contribs.Buckets, output.MetadataBucket{
					Name:                b.Name,
					Table:               b.Table,
					ScannedBytes:        b.ScannedBytes,
					MatchedRecordsRatio: b.MatchedRecordsRatio,
				})
			}
			meta.Contributions = contribs
		}
	}

	// Approximations and notifications are carried verbatim, notifications of
	// every severity: stderr and the envelope warnings show only WARNING and
	// ERROR, so this block is where an INFO notification can be read at all.
	// Read through the getters (not g) so the block agrees with what the
	// stderr and envelope paths report.
	if approximations := result.GetApproximations(); len(approximations) > 0 {
		meta.Approximations = append([]string(nil), approximations...)
	}
	for _, n := range result.GetNotifications() {
		meta.Notifications = append(meta.Notifications, output.MetadataNotice{
			Severity:         n.Severity,
			NotificationType: n.NotificationType,
			Message:          n.Message,
			MessageFormat:    n.MessageFormat,
			Arguments:        n.Arguments,
		})
	}

	for _, m := range metrics {
		meta.Metrics = append(meta.Metrics, output.MetricInfo{
			MetricKey:   m.MetricKey,
			FieldName:   m.FieldName,
			Aggregation: m.Aggregation,
			DisplayName: m.DisplayName,
			Description: m.Description,
			Unit:        m.Unit,
		})
	}

	return meta
}

// CancelQuery sends a best-effort cancellation request for a running query.
// Errors are written to stderr but not returned — cancellation is best-effort.
func (e *DQLExecutor) CancelQuery(requestToken string) {
	if requestToken == "" {
		return
	}
	handler := e.sdkHandler("")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := handler.Cancel(ctx, requestToken); err != nil {
		fmt.Fprintf(e.errW(), "\nFailed to cancel query: %v\n", err)
		return
	}
	fmt.Fprintln(e.errW(), "\nQuery cancelled.")
}

// ExecuteFromFile executes a DQL query from a file
func (e *DQLExecutor) ExecuteFromFile(filename string, outputFormat string) error {
	data, err := e.vfsEnv.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	return e.Execute(string(data), outputFormat)
}
