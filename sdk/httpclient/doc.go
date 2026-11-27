// Package httpclient provides an HTTP client for Dynatrace APIs.
//
// It wraps [resty] with Dynatrace-specific defaults: automatic retry of
// requests that are safe to resend (see [IsRetryable]), Bearer/Api-Token auth
// scheme selection, User-Agent with AI agent detection, gzip, and redacted
// verbose logging.
//
// # Constructor
//
// Use [New] with functional [Option] values:
//
//	client, err := httpclient.New("https://abc.apps.dynatrace.com",
//	    httpclient.WithToken("dt0c01.xxx"),
//	    httpclient.WithUserAgent("myapp/1.0"),
//	)
//
// # Errors
//
// API errors are returned as [*APIError] and can be inspected with
// errors.As. Sentinel errors ([ErrUnauthorized], [ErrNotFound], etc.)
// support errors.Is for common status codes.
//
// [APIError.Error] returns the status and the short message only. The
// upstream response body (a parsed details string, or the raw body when it is
// not the platform's error envelope) is kept in [APIError.Details]; it is
// never part of the error string, so logging or returning the error does not
// pass upstream-supplied text along. Callers that want it read Details after
// errors.As.
package httpclient
