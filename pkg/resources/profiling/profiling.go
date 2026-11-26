// Package profiling runs code-level profiling analyses and reads their results:
// the logic behind `dtctl exec profiling`, for the command and for a program
// that uses dtctl as a library.
//
// The sequence is: ResolveWindow turns a requested timeframe into a Window,
// Handler.Analyze runs the analysis, and the methods of the returned Analysis
// read it as rows (Stacks, Allocations, Functions, Focus) or whole (Raw).
// ClassifyError says what to do about an API failure, and Analysis.WhyEmpty
// why a view has no rows. The HTTP calls are the SDK's
// (sdk/api/codelevelanalysis); this package adds the window rules, the folds
// and the advice. Advice and notes are worded for a dtctl user and name dtctl
// flags.
package profiling

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Kind names an analysis by its API name.
type Kind = sdkcla.Kind

// The analyses `dtctl exec profiling` runs. The allocations analysis is the
// API's memoryAllocation, which the command also takes as `memory`.
const (
	KindHotspots    = sdkcla.KindHotspots
	KindAllocations = sdkcla.KindAllocations
)

// Leaf-type selections for hotspots, re-exported from the SDK.
const (
	LeafTypeTotal      = sdkcla.LeafTypeTotal
	LeafTypeService    = sdkcla.LeafTypeService
	LeafTypeBackground = sdkcla.LeafTypeBackground
)

// APIEntityID is the id the API is asked about for entityID: PROCESS-<id>
// becomes PROCESS_GROUP_INSTANCE-<id> (the same entity), classic ids pass
// through.
func APIEntityID(entityID string) string { return sdkcla.NormalizeEntityID(entityID) }

// Payload is the analysis request; re-exported from the SDK for CLI use.
type Payload = sdkcla.Payload

// Response is the analysis result; re-exported from the SDK for CLI use.
type Response = sdkcla.Response

// Handler runs analyses against one environment.
type Handler struct {
	sdk *sdkcla.Handler
}

// NewHandler returns a Handler that sends its requests through c.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdkcla.NewHandler(httpclient.Wrap(c.HTTP()))}
}

// Run runs one analysis and returns the SDK's response as it is. Analyze does
// the same and returns it ready to be read.
func (h *Handler) Run(ctx context.Context, p Payload) (*Response, error) {
	return h.sdk.Run(ctx, p)
}
