package profiling

import (
	"context"
	"fmt"

	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
)

// HotspotsRequest asks for a hotspots analysis: where a process spends its CPU
// time, and where its threads wait.
type HotspotsRequest struct {
	// Entity is the process to analyse; see EligibleEntity.
	Entity string
	// Window is the time to analyse, usually from ResolveWindow.
	Window Window
	// LeafType picks which CPU samples to attribute: LeafTypeTotal (also the
	// zero value), LeafTypeService or LeafTypeBackground.
	LeafType string
	// ShowWaiting includes the samples of waiting (non-running) threads.
	ShowWaiting bool
}

// AllocationsRequest asks for an allocations analysis: where a process
// allocates memory, and what survived GC. The API calls it memoryAllocation.
type AllocationsRequest struct {
	// Entity is the process to analyse; see EligibleEntity.
	Entity string
	// Window is the time to analyse, usually from ResolveWindow.
	Window Window
	// SurvivorsOnly restricts the analysis to allocations that survived a GC.
	SurvivorsOnly bool
}

// Hotspots is the result of a hotspots analysis. Its methods read it as the
// views `dtctl exec profiling hotspots` prints; each returns every row,
// unlimited.
type Hotspots struct {
	// Entity is the id the analysis was asked for, as the caller passed it
	// (APIEntityID gives the id the API was asked about).
	Entity string
	// Status is the API's verdict: completed, or no-data.
	Status string
	// PollCount is how many times the result was polled for: 0 when the API
	// answered the request with it.
	PollCount int

	// envelope is the result in the plain shape the folds and Raw read:
	// {"status":..., "kind":..., "entityId":..., "pollCount":..., "result":{...}}.
	envelope map[string]interface{}
}

// Allocations is the result of an allocations analysis. Stacks reads it as the
// view `dtctl exec profiling allocations` prints.
type Allocations struct {
	// Entity is the id the analysis was asked for, as the caller passed it
	// (APIEntityID gives the id the API was asked about).
	Entity string
	// Status is the API's verdict: completed, or no-data.
	Status string
	// PollCount is how many times the result was polled for: 0 when the API
	// answered the request with it.
	PollCount int

	envelope map[string]interface{} // as Hotspots.envelope
}

// Hotspots runs a hotspots analysis and returns it ready to be read. An error
// is best passed to ClassifyError before it is shown.
func (h *Handler) Hotspots(ctx context.Context, r HotspotsRequest) (*Hotspots, error) {
	resp, err := h.sdk.Run(ctx, sdkcla.Payload{
		Kind:        sdkcla.KindHotspots,
		EntityID:    r.Entity,
		From:        r.Window.From,
		To:          r.Window.To,
		LeafType:    r.LeafType,
		ShowWaiting: r.ShowWaiting,
	})
	if err != nil {
		return nil, err
	}
	return newHotspots(resp), nil
}

// Allocations runs an allocations analysis and returns it ready to be read. An
// error is best passed to ClassifyError before it is shown.
func (h *Handler) Allocations(ctx context.Context, r AllocationsRequest) (*Allocations, error) {
	resp, err := h.sdk.Run(ctx, sdkcla.Payload{
		Kind:          sdkcla.KindAllocations,
		EntityID:      r.Entity,
		From:          r.Window.From,
		To:            r.Window.To,
		SurvivorsOnly: r.SurvivorsOnly,
	})
	if err != nil {
		return nil, err
	}
	return newAllocations(resp), nil
}

func newHotspots(resp *sdkcla.Response) *Hotspots {
	return &Hotspots{Entity: resp.EntityID, Status: resp.Status, PollCount: resp.PollCount, envelope: newEnvelope(resp)}
}

func newAllocations(resp *sdkcla.Response) *Allocations {
	return &Allocations{Entity: resp.EntityID, Status: resp.Status, PollCount: resp.PollCount, envelope: newEnvelope(resp)}
}

// newEnvelope puts a response into the envelope shape. resp.Result is the
// decoded JSON document as Run returns it; it is copied, not modified.
func newEnvelope(resp *sdkcla.Response) map[string]interface{} {
	return compactResult(map[string]interface{}{
		"status":   resp.Status,
		"kind":     string(resp.Kind),
		"entityId": resp.EntityID,
		// A float64, as every other number in the decoded document is.
		"pollCount": float64(resp.PollCount),
		"result":    resp.Result,
	}).(map[string]interface{})
}

// Raw is the API's whole result under its envelope keys (status, kind,
// entityId, pollCount, result), without the timeseries data points and the
// fields that only carry chart rendering. Large: megabytes for a busy process.
func (h *Hotspots) Raw() map[string]interface{} {
	return h.envelope
}

// Raw is the API's whole result, as Hotspots.Raw gives it.
func (a *Allocations) Raw() map[string]interface{} {
	return a.envelope
}

// StackView is the stack view of a hotspots analysis.
type StackView struct {
	// Rows has one row per unique call stack, by running samples, descending.
	Rows []StackRow
	// Samples is the number of running samples over all rows.
	Samples int
}

// Stacks reads the analysis as one row per unique call stack. appOnly keeps
// only application frames in each stack.
func (h *Hotspots) Stacks(appOnly bool) StackView {
	v := StackView{Rows: HotspotStacks(h.envelope, appOnly)}
	for _, r := range v.Rows {
		v.Samples += r.Running
	}
	return v
}

// AllocationView is the stack view of an allocations analysis.
type AllocationView struct {
	// Rows has one row per unique allocation path, allocation site first, by
	// allocated bytes (by survived bytes when asked for), descending.
	Rows []AllocationRow
	// AllocatedBytes and SurvivedBytes are the totals over all rows.
	// SurvivedBytes is summed per GC survived and is not a size (SurvivedNote).
	AllocatedBytes int64
	SurvivedBytes  int64
}

// Stacks reads the analysis as one row per unique allocation path. appOnly
// keeps only application frames in each path; bySurvivors ranks the rows by
// survived bytes.
func (a *Allocations) Stacks(appOnly, bySurvivors bool) AllocationView {
	v := AllocationView{Rows: AllocationStacks(a.envelope, appOnly, bySurvivors)}
	for _, r := range v.Rows {
		v.AllocatedBytes += int64(r.AllocBytes)
		v.SurvivedBytes += int64(r.SurvivedBytes)
	}
	return v
}

// Functions reads the analysis as one row per function; see HotspotFunctions.
// It returns nil when there are no samples of that state.
func (h *Hotspots) Functions(appOnly bool, state, sortBy string) *FunctionTable {
	return HotspotFunctions(h.envelope, appOnly, state, sortBy)
}

// Focus reads the analysis as one function's callers and callees; see
// HotspotFocus.
func (h *Hotspots) Focus(appOnly bool, state, pattern string, limit int) (rows []FocusRow, function string, nCallers, nCallees int, err error) {
	return HotspotFocus(h.envelope, appOnly, state, pattern, limit)
}

// EmptyReason names why a view of an analysis has no rows. The values are
// stable.
type EmptyReason string

const (
	// EmptyNoSamples: the window holds no samples for the entity at all.
	EmptyNoSamples EmptyReason = "no_samples"
	// EmptyAppOnly: there are stacks, but appOnly removed every frame.
	EmptyAppOnly EmptyReason = "app_only"
	// EmptyState: a per-function view counted a thread state other than
	// running, and the window has no samples in it.
	EmptyState EmptyReason = "no_state_samples"
)

// WhyEmpty explains a view that came back without rows: the reason, and one
// line saying what to try, worded for a dtctl user. appOnly is what the view
// was read with; state is the thread state a per-function view counted, and
// empty for the stack view.
func (h *Hotspots) WhyEmpty(appOnly bool, state string) (EmptyReason, string) {
	if state != "" && state != "running" {
		return EmptyState, fmt.Sprintf("no %s samples for %s in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting", state, h.Entity)
	}
	// Running samples absent from a per-function view means what an empty
	// stack view means.
	return whyNoStacks(h.Entity, h.Status, appOnly && len(HotspotStacks(h.envelope, false)) > 0)
}

// WhyEmpty explains a stack view that came back without rows, as
// Hotspots.WhyEmpty does.
func (a *Allocations) WhyEmpty(appOnly bool) (EmptyReason, string) {
	return whyNoStacks(a.Entity, a.Status, appOnly && len(AllocationStacks(a.envelope, false, false)) > 0)
}

func whyNoStacks(entity, status string, removedByAppOnly bool) (EmptyReason, string) {
	if removedByAppOnly {
		return EmptyAppOnly, fmt.Sprintf("no stacks for %s: --app-only removed every frame (the profiler classified none as application code) — re-run without --app-only", entity)
	}
	return EmptyNoSamples, fmt.Sprintf("no stacks for %s: no profiling samples in the window (status=%s) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result", entity, status)
}

// visualOnlyKeys are fields that carry UI rendering metadata with no
// analytical value.
var visualOnlyKeys = map[string]bool{
	"color": true, // hex colour for charts
	"uuid":  true, // internal render ID
}

// emptyOnlyKeys are dropped only when they carry nothing. fileName/filePath
// are null on Java frames, but on Node.js they are the only place a frame's
// file lives (its className is empty), and the folded renderer labels frames
// from them.
var emptyOnlyKeys = map[string]bool{
	"fileName": true,
	"filePath": true,
}

// compactResult strips timeseries dataPoints and visual-only fields from an
// analysis result, so Raw carries signal without chart/render noise.
func compactResult(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, child := range val {
			if visualOnlyKeys[k] {
				continue
			}
			if emptyOnlyKeys[k] && (child == nil || child == "") {
				continue
			}
			if k == "dataPoints" {
				if arr, ok := child.([]interface{}); ok {
					out["dataPointsCount"] = len(arr)
					continue
				}
			}
			out[k] = compactResult(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = compactResult(item)
		}
		return out
	}
	return v
}
