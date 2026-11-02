package session

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Dynatrace/dtctl/sdk/agentmode"
	sdkauth "github.com/Dynatrace/dtctl/sdk/auth"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
	"github.com/Dynatrace/dtctl/sdk/urls"
)

// defaultUserAgentProduct identifies clients whose builder did not set an
// identity. Every consumer should pass WithUserAgentProduct — dtctl sends
// dtctl/<version>, plugins send their own product — so tenant-side request
// logs can tell consumers apart.
const defaultUserAgentProduct = "dtctl-sdk"

// Client is the authenticated HTTP client for a Dynatrace environment.
type Client struct {
	http    *resty.Client
	baseURL string

	// tokenMu guards token and lastRefresh: the 401 retry hook and SetToken
	// can run from concurrent request goroutines (e.g. parallel queries from a long-lived consumer).
	tokenMu     sync.Mutex
	token       string
	lastRefresh time.Time
}

// ClientOption customizes client construction.
type ClientOption func(*clientOptions)

type clientOptions struct {
	userAgent string
}

// WithUserAgent sets the full User-Agent product token (e.g. "dtctl/1.2.3").
// The AI-agent-environment suffix is appended automatically.
func WithUserAgent(ua string) ClientOption {
	return func(o *clientOptions) {
		o.userAgent = ua
	}
}

// WithUserAgentProduct sets the User-Agent from a product name and version,
// e.g. WithUserAgentProduct("dtctl-foo", "0.5.0") → "dtctl-foo/0.5.0".
func WithUserAgentProduct(product, version string) ClientOption {
	return WithUserAgent(fmt.Sprintf("%s/%s", product, version))
}

// NewClientFromConfig creates an authenticated client for the config's
// current context: OAuth-aware token resolution (keyring, file store, inline
// token), and re-resolution on 401 so long-running sessions survive OAuth
// access-token expiry.
func NewClientFromConfig(cfg *Config, opts ...ClientOption) (*Client, error) {
	ctx, err := cfg.CurrentContextObj()
	if err != nil {
		return nil, err
	}

	// Auto-discovered local configs must target a bare Dynatrace environment
	// origin: https, an allowlisted host, and no userinfo, path, query or
	// fragment. That prevents a rogue .dtctl.yaml from sending tokens to an
	// arbitrary or non-TLS destination, and it is the same rule Load applies
	// before adopting a ${VAR} environment (Config.resolveLocalEnvironments) —
	// re-checked here because the value may also come from a hand-built config.
	// Inline tokens remain unsupported regardless.
	envURL := ctx.Environment
	if cfg.IsLocal() {
		// A ${VAR} environment that Load did not resolve is still the literal
		// reference, which the origin check can only describe as a malformed
		// URL. Report the reference instead — an unset variable is the first
		// thing a developer hits with a freshly cloned .dtctl.yaml.
		if cfg.EnvironmentUnresolved(cfg.CurrentContext) {
			return nil, fmt.Errorf(
				"local config %q: context %q has environment %q, which did not resolve to a Dynatrace "+
					"environment URL — the environment must be a literal URL, or a single variable "+
					"reference holding the whole URL "+
					"(e.g. environment: ${DT_ENVIRONMENT_URL} with "+
					"DT_ENVIRONMENT_URL=https://abc12345.apps.dynatrace.com)",
				cfg.LocalConfigPath(), cfg.CurrentContext, envURL)
		}
		if err := urls.IsDynatraceEnvironmentOrigin(envURL); err != nil {
			return nil, fmt.Errorf(
				"local config %q: %w — use --config or DTCTL_CONFIG",
				cfg.LocalConfigPath(), err)
		}
	}

	// Use OAuth-aware token retrieval (supports both OAuth and API tokens)
	token, err := GetTokenWithOAuthSupport(cfg, ctx.TokenRef)
	if err != nil {
		return nil, err
	}

	c, err := NewClient(envURL, token, opts...)
	if err != nil {
		return nil, err
	}
	// OAuth access tokens are short-lived JWTs; a long-running invocation
	// (watch, workflow polling) outlives them. Re-resolve on 401 so the
	// session survives token expiry instead of surfacing "JWT token expired".
	tokenRef := ctx.TokenRef
	c.EnableTokenRefresh(func(rejected string) (string, error) {
		return RefreshedTokenForContext(cfg, envURL, tokenRef, rejected)
	})
	return c, nil
}

// NewForTesting creates a client with retries disabled, suitable for unit tests
// that use httptest servers. This avoids the 3×1s retry wait on 500/429 responses.
func NewForTesting(baseURL, token string) (*Client, error) {
	c, err := NewClient(baseURL, token)
	if err != nil {
		return nil, err
	}
	c.http.SetRetryCount(0)
	return c, nil
}

// NewClient creates a new client with base URL and token.
func NewClient(baseURL, token string, opts ...ClientOption) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if token == "" {
		return nil, fmt.Errorf("token is required")
	}

	options := clientOptions{userAgent: defaultUserAgentProduct}
	for _, opt := range opts {
		opt(&options)
	}

	// Build user agent with AI detection
	userAgent := options.userAgent
	if aiSuffix := agentmode.UserAgentSuffix(); aiSuffix != "" {
		userAgent += aiSuffix
	}

	// resty.New still builds this client's own http.Client and cookie jar;
	// only the RoundTripper is swapped for the process-wide pool, so a
	// long-lived consumer reuses upstream connections across clients (see
	// sharedTransport).
	//
	// The logger is what turns a refused transport setter into an error:
	// see transportSetterGuard.
	guard := &transportSetterGuard{}
	httpClient := httpclient.GuardRequestPaths(resty.New()).
		SetTransport(sharedTransport).
		SetLogger(guard).
		OnBeforeRequest(guard.checkRequest).
		SetBaseURL(baseURL).
		SetAuthScheme("Bearer").
		SetAuthToken(token).
		// Same retry policy as httpclient.New: only requests that are safe to
		// resend (idempotent methods, 429, 503 with Retry-After, failed dials).
		SetRetryCount(3).
		SetRetryWaitTime(1*time.Second).
		SetRetryMaxWaitTime(10*time.Second).
		// Multipart parts are one-shot readers (document create/update), so
		// rewind them before a retry or the resent part is empty.
		SetRetryResetReaders(true).
		AddRetryCondition(httpclient.IsRetryable).
		SetTimeout(6*time.Minute). // Allow for long-running Grail queries (up to 5 min)
		SetHeader("User-Agent", userAgent).
		SetHeader("Accept-Encoding", "gzip")

	return &Client{
		http:    httpClient,
		baseURL: baseURL,
		token:   token,
	}, nil
}

// HTTP returns the underlying resty client.
//
// Its RoundTripper is the process-wide connection pool shared by every Client
// (see sharedTransport), so resty's in-place transport setters — SetProxy,
// RemoveProxy, SetTLSClientConfig, SetCertificates, SetRootCertificate and
// friends — cannot apply to it. Calling one does not change the pool; instead
// every later request on this client fails with ErrSharedTransportSetting, so
// a required proxy or TLS setting is never silently dropped. To give one
// client its own TLS or proxy settings, install a private transport first and
// then call the setters:
//
//	c.HTTP().SetTransport(session.NewTransport()).SetProxy(proxyURL)
func (c *Client) HTTP() *resty.Client {
	return c.http
}

// SetToken updates the bearer token used for all subsequent HTTP requests.
// This is used to inject a freshly refreshed OAuth token without recreating the client.
func (c *Client) SetToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = token
	c.http.SetAuthToken(token)
}

// Token returns the bearer token currently used for requests.
func (c *Client) Token() string {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	return c.token
}

// refreshWindow rate-limits 401-triggered token refreshes: if a token that
// was refreshed moments ago is rejected again, the credentials are dead and
// retrying would only hammer the SSO endpoint.
const refreshWindow = 10 * time.Second

// EnableTokenRefresh registers a retry-on-401 hook: resolve is called with
// the rejected token and must return a fresh one (typically by refreshing an
// expired OAuth access token). The request is retried only when a genuinely
// new token was obtained; static tokens and failed refreshes surface the
// original 401. Safe for concurrent requests — one refresh serves them all.
//
// resolve runs while tokenMu is held, so Token/SetToken and other 401
// handling on this client block for its duration — bounded by the refresh
// lock timeout plus one token-endpoint round-trip. That serialization is
// deliberate (concurrent requests would only collect more 401s until the
// refresh lands); embedders should not call Token from a latency-sensitive
// loop while requests are in flight.
func (c *Client) EnableTokenRefresh(resolve func(rejected string) (string, error)) {
	c.http.AddRetryCondition(func(r *resty.Response, err error) bool {
		if err != nil || r == nil || r.StatusCode() != http.StatusUnauthorized {
			return false
		}
		c.tokenMu.Lock()
		defer c.tokenMu.Unlock()
		if time.Since(c.lastRefresh) < refreshWindow {
			// A concurrent request already swapped the token in — retry with
			// it; a fresh token rejected again means dead credentials — give up.
			return c.token != bearerOf(r.Request)
		}
		rejected := c.token
		token, resolveErr := resolve(rejected)
		if resolveErr != nil || token == "" || token == rejected {
			return false
		}
		c.lastRefresh = time.Now()
		c.token = token
		c.http.SetAuthToken(token)
		return true
	})
}

// bearerOf extracts the bearer token a request was sent with.
func bearerOf(req *resty.Request) string {
	if req == nil || req.RawRequest == nil {
		return ""
	}
	return strings.TrimPrefix(req.RawRequest.Header.Get("Authorization"), "Bearer ")
}

// sensitiveHeaders lists headers that should always be redacted in debug output
var sensitiveHeaders = []string{"authorization", "x-api-key", "cookie", "set-cookie"}

// isSensitiveHeader checks if a header name should be redacted
func isSensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	for _, h := range sensitiveHeaders {
		if lower == h {
			return true
		}
	}
	return false
}

// SetVerbosity sets the verbosity level for logging
// Level 0: normal (no debug output)
// Level 1: show request/response summary
// Level 2+: show full request/response details (sensitive headers always redacted)
func (c *Client) SetVerbosity(level int) {
	if level <= 0 {
		return
	}

	c.http.SetPreRequestHook(func(client *resty.Client, req *http.Request) error {
		var sb strings.Builder
		sb.WriteString("===> REQUEST <===\n")
		sb.WriteString(fmt.Sprintf("%s %s\n", req.Method, req.URL))
		if level >= 2 {
			sb.WriteString("HEADERS:\n")
			for k, v := range req.Header {
				if isSensitiveHeader(k) {
					sb.WriteString(fmt.Sprintf("    %s: [REDACTED]\n", k))
				} else {
					sb.WriteString(fmt.Sprintf("    %s: %s\n", k, strings.Join(v, ", ")))
				}
			}
			if bodyText := readRequestBodyForDebug(req); bodyText != "" {
				sb.WriteString(fmt.Sprintf("BODY:\n%s\n", bodyText))
			}
		}
		fmt.Fprint(os.Stderr, sb.String())
		return nil
	})

	c.http.OnAfterResponse(func(client *resty.Client, resp *resty.Response) error {
		var sb strings.Builder
		sb.WriteString("===> RESPONSE <===\n")
		sb.WriteString(fmt.Sprintf("STATUS: %d %s\n", resp.StatusCode(), resp.Status()))
		sb.WriteString(fmt.Sprintf("TIME: %s\n", resp.Time()))
		if level >= 2 {
			sb.WriteString("HEADERS:\n")
			for k, v := range resp.Header() {
				if isSensitiveHeader(k) {
					sb.WriteString(fmt.Sprintf("    %s: [REDACTED]\n", k))
				} else {
					sb.WriteString(fmt.Sprintf("    %s: %s\n", k, strings.Join(v, ", ")))
				}
			}
			sb.WriteString(fmt.Sprintf("BODY:\n%s\n", resp.String()))
		}
		fmt.Fprint(os.Stderr, sb.String())
		return nil
	})
}

func readRequestBodyForDebug(req *http.Request) string {
	defer func() {
		_ = recover()
	}()

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

// BaseURL returns the base URL of the Dynatrace environment
func (c *Client) BaseURL() string {
	return c.baseURL
}

// UserInfo contains information about the current user
type UserInfo struct {
	UserName     string `json:"userName"`
	UserID       string `json:"userId"`
	EmailAddress string `json:"emailAddress"`
}

// CurrentUser fetches the current user info from the metadata API.
// Requires scope: app-engine:apps:run
func (c *Client) CurrentUser() (*UserInfo, error) {
	var userInfo UserInfo
	resp, err := c.http.R().
		SetResult(&userInfo).
		Get("/platform/metadata/v1/user")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user info: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to fetch user info: %s", resp.Status())
	}
	return &userInfo, nil
}

// CurrentUserID returns the current user's ID.
// First tries the metadata API, falls back to JWT token decoding.
func (c *Client) CurrentUserID() (string, error) {
	// Try metadata API first
	userInfo, err := c.CurrentUser()
	if err == nil && userInfo.UserID != "" {
		return userInfo.UserID, nil
	}

	// Platform tokens are not JWTs, so the JWT fallback below would parse
	// garbage and fail with a confusing decode error (see issue #210). Surface
	// an actionable message instead.
	token := c.Token()
	if IsPlatformToken(token) {
		if err == nil {
			err = fmt.Errorf("metadata API returned no user ID")
		}
		return "", fmt.Errorf("cannot determine current user ID for a platform token: %w "+
			"(ensure the token has the 'app-engine:apps:run' scope so /platform/metadata/v1/user can return the user identity)", err)
	}

	// Fallback to JWT decoding
	return ExtractUserIDFromToken(token)
}

// platformTokenPrefix identifies Dynatrace platform tokens — opaque bearer
// tokens (not JWTs). The /platform/metadata/v1/user endpoint requires the
// 'app-engine:apps:run' scope; platform tokens that lack it get a 403, and
// the token itself cannot be JWT-decoded as a user-ID fallback.
// IsPlatformToken reports whether token is a Dynatrace platform token.
func IsPlatformToken(token string) bool {
	return sdkauth.IsPlatformToken(token)
}

// ExtractUserIDFromToken extracts the user ID (sub claim) from a JWT token.
func ExtractUserIDFromToken(token string) (string, error) {
	return sdkauth.ExtractJWTSubject(token)
}
