package cmd

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// HeaderRefusedError is returned when the embedding caller's header policy
// (RunOptions.HeaderPolicy) refuses a header passed to `exec api -H`. Which
// headers a caller may set is the host's decision, not dtctl's: it depends on
// where the host runs and what the proxies in front of its upstream trust.
// Agent mode renders it with the stable code "header_refused" (see
// errorToDetail), so a host can map the refusal to a client error.
type HeaderRefusedError struct {
	// Header is the header name as the caller typed it, canonicalized.
	Header string
	// Reason is the policy's explanation.
	Reason string
}

// Headline is the one-line statement of the refusal, shared by the
// human-readable Error() string and the structured agent-mode ErrorDetail so
// the two never drift.
func (e *HeaderRefusedError) Headline() string {
	return fmt.Sprintf("refusing -H %s: %s", e.Header, e.Reason)
}

func (e *HeaderRefusedError) Error() string { return e.Headline() }

// normalizeHeaderName is the one spelling a header policy sees: canonicalized,
// with "_" folded to "-", so x_forwarded_for and X-Forwarded-For reach the
// policy as the same name. Many proxies treat the two alike; a policy that had
// to list both spellings would miss one. The name sent on the wire is not
// folded.
func normalizeHeaderName(name string) string {
	return http.CanonicalHeaderKey(strings.ReplaceAll(name, "_", "-"))
}

// currentHeaderPolicy resolves the invocation's header policy. nil refuses
// nothing beyond the CLI's own refusals (the CLI default).
func currentHeaderPolicy(ctx context.Context) func(name string) string {
	if inv := current(ctx); inv != nil {
		return inv.headerPolicy
	}
	return nil
}
