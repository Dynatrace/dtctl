// Package profiling runs code-level profiling analyses and reads their results:
// the logic behind `dtctl exec profiling`, for the command and for a program
// that uses dtctl as a library.
//
// The sequence is: ResolveWindow turns a requested timeframe into a Window,
// Handler.Hotspots or Handler.Allocations runs the analysis, and the methods of
// the returned Hotspots or Allocations read it as rows (Stacks, Functions,
// Focus) or whole (Raw). Each kind of analysis has its own result type, so a
// view is only there for the kind it reads.
//
// What went wrong is reported as a value, not as text to match: ResolveWindow
// and the views return typed errors, ClassifyError names the reason an
// analysis failed, and a view without rows says why in its Empty field. The
// package words none of this for a command line; `dtctl exec profiling` does
// that in cmd/, where the flag names are.
//
// The HTTP calls are the SDK's (sdk/api/codelevelanalysis); this package adds
// the window rules and the folds. Its API follows the tier of the command
// (docs/STABILITY.md): while that is experimental, so is this.
package profiling

import (
	"github.com/Dynatrace/dtctl/pkg/client"
	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// LeafType selects which CPU samples a hotspots analysis attributes. The zero
// value counts as LeafTypeTotal. The type and its values are the SDK's.
type LeafType = sdkcla.LeafType

const (
	// LeafTypeTotal attributes every sample.
	LeafTypeTotal = sdkcla.LeafTypeTotal
	// LeafTypeService attributes the samples taken inside a traced service
	// context.
	LeafTypeService = sdkcla.LeafTypeService
	// LeafTypeBackground attributes the samples taken outside any service
	// context.
	LeafTypeBackground = sdkcla.LeafTypeBackground
)

// ErrUnknownLeafType is the error for a LeafType that is none of the
// constants.
var ErrUnknownLeafType = sdkcla.ErrUnknownLeafType

// ParseLeafType returns the LeafType named s, LeafTypeTotal for the empty
// string. A name that is none of the constants is an error that wraps
// ErrUnknownLeafType.
func ParseLeafType(s string) (LeafType, error) { return sdkcla.ParseLeafType(s) }

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

// NewHandler returns a Handler that sends its requests through c. A program
// other than dtctl builds c with sdk/session (session.NewClientFromConfig and
// its own User-Agent), of whose Client client.Client is an alias; pkg/client
// builds dtctl's own, which identifies itself as dtctl.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdkcla.NewHandler(httpclient.Wrap(c.HTTP()))}
}
