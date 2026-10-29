// Package matcherverify is the CLI resource layer for the OpenPipeline matcher
// verify endpoint. It delegates HTTP calls to the SDK and shapes the response
// into a display-ready VerifyResult that carries structured notifications for
// machine formats (json/yaml/agent) and a flattened text column for tables.
package matcherverify

import (
	"context"
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkmatcher "github.com/Dynatrace/dtctl/sdk/api/matcherverify"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// VerifyOptions are the inputs for a single matcher verify request.
type VerifyOptions struct {
	Query           string
	ConfigurationID string
	Context         string
}

// SyntaxPosition is a single line/column/index position in the source expression.
type SyntaxPosition struct {
	Line   int `json:"line" yaml:"line"`
	Column int `json:"column" yaml:"column"`
	Index  int `json:"index" yaml:"index"`
}

// SyntaxRange is a start/end source range.
type SyntaxRange struct {
	Start SyntaxPosition `json:"start" yaml:"start"`
	End   SyntaxPosition `json:"end" yaml:"end"`
}

// Notification is a single diagnostic (severity, message, optional position),
// preserved as structured data so json/yaml/agent output keeps the details.
type Notification struct {
	Severity       string       `json:"severity" yaml:"severity"`
	Message        string       `json:"message" yaml:"message"`
	SyntaxPosition *SyntaxRange `json:"syntaxPosition,omitempty" yaml:"syntaxPosition,omitempty"`
}

// VerifyResult is the CLI read-model for a verify response. Notifications is the
// structured list surfaced in json/yaml/agent output; Summary is the same list
// flattened to a human string, used only for the table NOTIFICATIONS column and
// the default human-readable summary.
type VerifyResult struct {
	Valid         bool           `json:"valid" yaml:"valid" table:"VALID"`
	Notifications []Notification `json:"notifications,omitempty" yaml:"notifications,omitempty" table:"-"`
	Summary       string         `json:"-" yaml:"-" table:"NOTIFICATIONS"`
}

// Handler verifies OpenPipeline matcher expressions.
type Handler struct {
	sdk *sdkmatcher.Handler
}

// NewHandler creates a new matcher verify handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdkmatcher.NewHandler(httpclient.Wrap(c.HTTP()))}
}

// Verify calls the verify endpoint and returns the display-ready result.
func (h *Handler) Verify(opts VerifyOptions) (*VerifyResult, error) {
	sdkResult, err := h.sdk.Verify(context.Background(), sdkmatcher.MatcherVerifyRequest{
		Query:           opts.Query,
		ConfigurationID: opts.ConfigurationID,
		Context:         opts.Context,
	})
	if err != nil {
		return nil, err
	}
	return FromSDKVerifyResponse(sdkResult), nil
}

// FromSDKVerifyResponse converts an SDK VerifyResponse into a CLI VerifyResult,
// preserving structured notifications and computing the flattened Summary.
// Exported so sibling packages can reuse the conversion without depending on the Handler.
func FromSDKVerifyResponse(r *sdkmatcher.VerifyResponse) *VerifyResult {
	notifications := make([]Notification, 0, len(r.Notifications))
	for _, n := range r.Notifications {
		note := Notification{Severity: n.Severity, Message: n.Message}
		if n.SyntaxPosition != nil {
			note.SyntaxPosition = &SyntaxRange{
				Start: SyntaxPosition(n.SyntaxPosition.Start),
				End:   SyntaxPosition(n.SyntaxPosition.End),
			}
		}
		notifications = append(notifications, note)
	}
	return &VerifyResult{
		Valid:         r.Valid,
		Notifications: notifications,
		Summary:       FormatNotifications(r.Notifications),
	}
}

// FormatNotifications formats a slice of MetadataNotification entries into a
// single human-readable string with one entry per line.
// Format: "SEVERITY (line:col-line:col): message" (position omitted if absent).
// Exported so sibling packages (dqlprocessorverify) can reuse the same display logic.
func FormatNotifications(notifications []sdkmatcher.MetadataNotification) string {
	if len(notifications) == 0 {
		return ""
	}
	parts := make([]string, 0, len(notifications))
	for _, n := range notifications {
		parts = append(parts, formatNotification(n))
	}
	return strings.Join(parts, "\n")
}

// formatNotification formats a single MetadataNotification as a human-readable string.
func formatNotification(n sdkmatcher.MetadataNotification) string {
	if n.SyntaxPosition != nil {
		pos := n.SyntaxPosition
		return fmt.Sprintf("%s (%d:%d-%d:%d): %s",
			n.Severity,
			pos.Start.Line, pos.Start.Column,
			pos.End.Line, pos.End.Column,
			n.Message,
		)
	}
	return fmt.Sprintf("%s: %s", n.Severity, n.Message)
}
