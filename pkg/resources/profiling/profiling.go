// Package profiling runs code-level profiling analyses and reads their results:
// the logic behind `dtctl exec profiling`, for the command and for a program
// that uses dtctl as a library.
//
// The sequence is: ResolveWindow turns a requested timeframe into a Window,
// Handler.Hotspots or Handler.Allocations runs the analysis, and the methods of
// the returned Hotspots or Allocations read it as rows (Stacks, Functions,
// Focus) or whole (Raw). Each kind of analysis has its own result type, so a
// view is only there for the kind it reads. ClassifyError says what to do about
// an API failure, and a view without rows says why in its Empty field. The HTTP calls are the SDK's
// (sdk/api/codelevelanalysis); this package adds the window rules, the folds
// and the advice. Advice and notes are worded for a dtctl user and name dtctl
// flags.
package profiling

import (
	"github.com/Dynatrace/dtctl/pkg/client"
	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
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

// EligibleEntity reports whether entityID names something an analysis can run
// on: a process (PROCESS-xxx), or by its classic ids a process group instance
// or a whole process group. A service is not eligible. An analysis on any
// other id fails before a request is sent.
func EligibleEntity(entityID string) bool { return sdkcla.EligibleEntity(entityID) }

// Handler runs analyses against one environment.
type Handler struct {
	sdk *sdkcla.Handler
}

// NewHandler returns a Handler that sends its requests through c.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdkcla.NewHandler(httpclient.Wrap(c.HTTP()))}
}
