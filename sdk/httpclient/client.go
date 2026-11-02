package httpclient

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Dynatrace/dtctl/sdk/agentmode"
	"github.com/Dynatrace/dtctl/sdk/auth"
)

// Client is an HTTP client for Dynatrace APIs.
type Client struct {
	http            *resty.Client
	baseURL         string
	token           string
	logger          Logger
	retryConfigured bool
}

// Option configures a Client.
type Option func(*Client)

// WithToken sets the authentication token. The auth scheme (Bearer vs Api-Token)
// is determined automatically from the token prefix.
func WithToken(token string) Option {
	return func(c *Client) {
		c.token = token
	}
}

// WithRetry sets how often, and with what backoff, a failed request is
// retried. maxRetries is the number of attempts after the first; 0 disables
// retries entirely. waitTime and maxWaitTime bound the jittered exponential
// backoff between attempts.
//
// WithRetry changes only the budget, not which requests qualify: a request is
// retried only when [IsRetryable] says it is safe to send again, so a POST or
// PATCH that fails with a 5xx or a dropped connection is never resent, however
// high maxRetries is. To retry one specific non-idempotent request anyway, add
// a request-level condition with resty's Request.AddRetryCondition; it is
// consulted before the client's.
func WithRetry(maxRetries int, waitTime, maxWaitTime time.Duration) Option {
	return func(c *Client) {
		c.retryConfigured = true
		c.http.SetRetryCount(maxRetries)
		c.http.SetRetryWaitTime(waitTime)
		c.http.SetRetryMaxWaitTime(maxWaitTime)
	}
}

// WithTimeout sets the request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.http.SetTimeout(d)
	}
}

// WithUserAgent sets the User-Agent header prefix. AI agent detection
// suffix is appended automatically.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		fullUA := ua
		if aiSuffix := agentmode.UserAgentSuffix(); aiSuffix != "" {
			fullUA += aiSuffix
		}
		c.http.SetHeader("User-Agent", fullUA)
	}
}

// WithLogger sets the logger for debug output. A nil logger is ignored.
func WithLogger(l Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithHTTPProxy sets an HTTP proxy URL.
func WithHTTPProxy(proxyURL string) Option {
	return func(c *Client) {
		c.http.SetProxy(proxyURL)
	}
}

// WithTransport sets a custom http.RoundTripper, useful for injecting
// OpenTelemetry or other middleware.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) {
		c.http.SetTransport(rt)
	}
}

// noopRestyLogger discards all resty-internal log output.
type noopRestyLogger struct{}

func (noopRestyLogger) Errorf(string, ...interface{}) {}
func (noopRestyLogger) Warnf(string, ...interface{})  {}
func (noopRestyLogger) Debugf(string, ...interface{}) {}

// New creates a new Dynatrace HTTP client.
//
// The baseURL is required (e.g. "https://abc.apps.dynatrace.com").
// At minimum, provide WithToken to authenticate requests.
//
// Retries are on by default: up to 3 retries, with jittered exponential
// backoff from 1s up to 10s between attempts. Use [WithRetry] to change that,
// or WithRetry(0, 0, 0) for a single attempt.
//
// Only requests that are safe to send again are retried (see [IsRetryable]):
// idempotent methods (GET, HEAD, OPTIONS, TRACE, PUT, DELETE) on a 5xx or a
// transport error, and any method on a 429, on a 503 with Retry-After, or
// when the connection could not be established. A POST or PATCH that fails
// with any other 5xx or transport error is returned to the caller after one
// attempt, because the server may already have acted on it.
func New(baseURL string, opts ...Option) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	c := &Client{
		http:    GuardRequestPaths(resty.New()),
		baseURL: baseURL,
		logger:  noopLogger{},
	}

	// Apply options
	for _, opt := range opts {
		opt(c)
	}

	// Configure resty
	c.http.
		SetLogger(&noopRestyLogger{}).
		SetBaseURL(baseURL).
		SetHeader("Accept-Encoding", "gzip")

	// Set auth if token provided
	if c.token != "" {
		c.http.SetAuthScheme(auth.AuthScheme(c.token))
		c.http.SetAuthToken(c.token)
	}

	// Set defaults if not overridden by WithRetry
	if !c.retryConfigured {
		c.http.SetRetryCount(3)
		c.http.SetRetryWaitTime(1 * time.Second)
		c.http.SetRetryMaxWaitTime(10 * time.Second)
	}
	// Multipart bodies are one-shot readers, so rewind them before a retry
	c.http.SetRetryResetReaders(true)
	c.http.AddRetryCondition(func(r *resty.Response, err error) bool {
		retry := IsRetryable(r, err)
		if retry {
			if err != nil {
				c.logger.Debugf("retrying request due to error: %v", err)
			} else {
				c.logger.Debugf("retrying request due to status %d", r.StatusCode())
			}
		}
		return retry
	})

	if c.http.GetClient().Timeout == 0 {
		c.http.SetTimeout(6 * time.Minute)
	}

	c.logger.Debugf("initialized HTTP client for %s", baseURL)

	return c, nil
}

// HTTP returns the underlying resty client for advanced use cases.
// Prefer the typed methods (Do, GetJSON, etc.) when possible.
func (c *Client) HTTP() *resty.Client {
	return c.http
}

// SetToken updates the authentication token.
func (c *Client) SetToken(token string) {
	c.token = token
	c.http.SetAuthScheme(auth.AuthScheme(token))
	c.http.SetAuthToken(token)
}

// BaseURL returns the configured base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// sensitiveHeaders lists headers that should always be redacted in debug output.
var sensitiveHeaders = map[string]bool{
	"authorization": true,
	"x-api-key":     true,
	"cookie":        true,
	"set-cookie":    true,
}

// EnableVerboseLogging enables request/response debug logging.
// Level 1: summary. Level 2+: full headers and body (sensitive headers redacted).
func (c *Client) EnableVerboseLogging(level int, w io.Writer) {
	if level <= 0 || w == nil {
		return
	}

	c.http.SetPreRequestHook(func(_ *resty.Client, req *http.Request) error {
		var sb strings.Builder
		sb.WriteString("===> REQUEST <===\n")
		sb.WriteString(fmt.Sprintf("%s %s\n", req.Method, req.URL))
		if level >= 2 {
			sb.WriteString("HEADERS:\n")
			for k, v := range req.Header {
				if sensitiveHeaders[strings.ToLower(k)] {
					sb.WriteString(fmt.Sprintf("    %s: [REDACTED]\n", k))
				} else {
					sb.WriteString(fmt.Sprintf("    %s: %s\n", k, strings.Join(v, ", ")))
				}
			}
			if bodyText := readRequestBodyForDebug(req); bodyText != "" {
				sb.WriteString(fmt.Sprintf("BODY:\n%s\n", bodyText))
			}
		}
		fmt.Fprint(w, sb.String())
		return nil
	})

	c.http.OnAfterResponse(func(_ *resty.Client, resp *resty.Response) error {
		var sb strings.Builder
		sb.WriteString("===> RESPONSE <===\n")
		sb.WriteString(fmt.Sprintf("STATUS: %s\n", resp.Status()))
		sb.WriteString(fmt.Sprintf("TIME: %s\n", resp.Time()))
		if level >= 2 {
			sb.WriteString("HEADERS:\n")
			for k, v := range resp.Header() {
				if sensitiveHeaders[strings.ToLower(k)] {
					sb.WriteString(fmt.Sprintf("    %s: [REDACTED]\n", k))
				} else {
					sb.WriteString(fmt.Sprintf("    %s: %s\n", k, strings.Join(v, ", ")))
				}
			}
			sb.WriteString(fmt.Sprintf("BODY:\n%s\n", resp.String()))
		}
		fmt.Fprint(w, sb.String())
		return nil
	})
}

func readRequestBodyForDebug(req *http.Request) string {
	if req == nil {
		return ""
	}

	if req.GetBody != nil {
		clone, err := req.GetBody()
		if err == nil && clone != nil {
			defer clone.Close()
			body, readErr := io.ReadAll(clone)
			if readErr == nil && len(body) > 0 {
				return string(body)
			}
		}
	}

	if req.Body == nil {
		return ""
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return ""
	}
	req.Body = io.NopCloser(bytes.NewBuffer(body))

	if len(body) == 0 {
		return ""
	}

	return string(body)
}
