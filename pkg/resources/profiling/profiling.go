package profiling

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Kind maps user-facing shorthand to API kind strings.
var Kind = sdkcla.Kind

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

type Handler struct {
	sdk *sdkcla.Handler
}

func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdkcla.NewHandler(httpclient.Wrap(c.HTTP()))}
}

func (h *Handler) Run(ctx context.Context, p Payload) (*Response, error) {
	return h.sdk.Run(ctx, p)
}
