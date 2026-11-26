// Package client re-exports the authenticated Dynatrace HTTP client from
// github.com/Dynatrace/dtctl/sdk/session, where the implementation moved: client construction
// from a context — token resolution, OAuth refresh, the 401 retry — is shared
// session behavior, and the User-Agent is parameterized there so each
// consumer ships its own identity. This package pins dtctl's identity
// (dtctl/<version>) and keeps the CLI-only OTel trace propagation; typed API
// errors (errors.go) and pagination helpers (pagination.go) also remain here.
package client

import (
	"context"

	"github.com/go-resty/resty/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/version"
	sdkauth "github.com/Dynatrace/dtctl/sdk/auth"
	"github.com/Dynatrace/dtctl/sdk/session"
)

type (
	// Client is the authenticated HTTP client for a Dynatrace environment.
	Client = session.Client
	// UserInfo is the platform metadata user (from /platform/metadata/v1/user).
	UserInfo = session.UserInfo
)

// dtctlIdentity is the User-Agent this binary sends (plugins send their own
// product through the same parameterized sdk client).
func dtctlIdentity() session.ClientOption {
	return session.WithUserAgentProduct("dtctl", version.Version)
}

// NewFromConfig creates a client for the config's current context with
// OAuth-aware token resolution and re-resolution on 401.
func NewFromConfig(cfg *config.Config) (*Client, error) {
	return session.NewClientFromConfig(cfg, dtctlIdentity())
}

// New creates a new client with base URL and token.
func New(baseURL, token string) (*Client, error) {
	return session.NewClient(baseURL, token, dtctlIdentity())
}

// NewForTesting creates a client with retries disabled, suitable for unit tests
// that use httptest servers. This avoids the 3×1s retry wait on 500/429 responses.
func NewForTesting(baseURL, token string) (*Client, error) {
	c, err := New(baseURL, token)
	if err != nil {
		return nil, err
	}
	c.HTTP().SetRetryCount(0)
	return c, nil
}

// NewFromConfigWithOAuth creates a new client from config with OAuth support.
//
// Deprecated: Use NewFromConfig instead, which now supports OAuth tokens automatically.
func NewFromConfigWithOAuth(cfg *config.Config) (*Client, error) {
	return NewFromConfig(cfg)
}

// GetTokenWithOAuthSupport retrieves a token from config with OAuth token refresh support,
// using the current context's environment to detect the OAuth configuration.
func GetTokenWithOAuthSupport(cfg *config.Config, tokenRef string) (string, error) {
	return session.GetTokenWithOAuthSupport(cfg, tokenRef)
}

// GetTokenForContext retrieves a token from config with OAuth token refresh support,
// detecting the OAuth configuration from the supplied environment URL.
func GetTokenForContext(cfg *config.Config, environmentURL, tokenRef string) (string, error) {
	return session.GetTokenForContext(cfg, environmentURL, tokenRef)
}

// RefreshedTokenForContext re-resolves the context's bearer token after a
// request was rejected with HTTP 401 (see session.RefreshedTokenForContext).
func RefreshedTokenForContext(cfg *config.Config, environmentURL, tokenRef, rejected string) (string, error) {
	return session.RefreshedTokenForContext(cfg, environmentURL, tokenRef, rejected)
}

// IsPlatformToken reports whether token is a Dynatrace platform token.
func IsPlatformToken(token string) bool {
	return sdkauth.IsPlatformToken(token)
}

// ExtractUserIDFromToken extracts the user ID (sub claim) from a JWT token.
func ExtractUserIDFromToken(token string) (string, error) {
	return sdkauth.ExtractJWTSubject(token)
}

// InjectTraceContext configures the client to inject W3C trace context headers
// (traceparent / tracestate) on every outgoing HTTP request. It lives here —
// not in the sdk — so the sdk stays OTel-free; tracing is a dtctl CLI concern.
//
// The provided ctx is captured once at registration time; all subsequent HTTP requests
// share the same span context (the root CLI span). This means individual API calls do
// NOT get their own child spans — they all propagate the same trace-id and parent span-id.
// This is intentional for a short-lived CLI: one invocation = one logical operation.
//
// The propagator is resolved once from the global OTel registry at registration time
// to avoid per-request mutex overhead from otel.GetTextMapPropagator().
func InjectTraceContext(c *Client, ctx context.Context) {
	prop := otel.GetTextMapPropagator()
	c.HTTP().OnBeforeRequest(func(_ *resty.Client, req *resty.Request) error {
		prop.Inject(ctx, propagation.HeaderCarrier(req.Header))
		return nil
	})
}
