package profiling

import (
	"context"
	"fmt"
)

// Analysis is the result of one analysis. Its methods read it as the views
// `dtctl exec profiling` prints; each returns every row, unlimited.
type Analysis struct {
	// Kind is the API kind that ran: Kind["hotspots"] or Kind["memory"].
	Kind string
	// Entity is the id the analysis was asked for, as the caller passed it
	// (APIEntityID gives the id the API was asked about).
	Entity string
	// Status is the API's verdict: completed, or no-data.
	Status string

	// envelope is the result in the plain shape the folds and --raw read:
	// {"status":..., "kind":..., "entityId":..., "pollCount":..., "result":{...}}.
	envelope map[string]interface{}
}

// Analyze runs one analysis and returns it ready to be read. A window comes
// from ResolveWindow; an error is best passed to ClassifyError before it is
// shown.
func (h *Handler) Analyze(ctx context.Context, p Payload) (*Analysis, error) {
	resp, err := h.Run(ctx, p)
	if err != nil {
		return nil, err
	}
	return NewAnalysis(resp), nil
}

// NewAnalysis wraps a response of Handler.Run. resp.Result must be the decoded
// JSON document as Run returns it; it is copied, not modified.
func NewAnalysis(resp *Response) *Analysis {
	return &Analysis{
		Kind:   resp.Kind,
		Entity: resp.EntityID,
		Status: resp.Status,
		envelope: compactResult(map[string]interface{}{
			"status":   resp.Status,
			"kind":     resp.Kind,
			"entityId": resp.EntityID,
			// A float64, as every other number in the decoded document is.
			"pollCount": float64(resp.PollCount),
			"result":    resp.Result,
		}).(map[string]interface{}),
	}
}

// Raw is the API's whole result under its envelope keys (status, kind,
// entityId, pollCount, result), without the timeseries data points and the
// fields that only carry chart rendering. Large: megabytes for a busy process.
func (a *Analysis) Raw() map[string]interface{} {
	return a.envelope
}

// StackView is the stack view of a hotspots analysis.
type StackView struct {
	// Rows has one row per unique call stack, by running samples, descending.
	Rows []StackRow
	// Samples is the number of running samples over all rows.
	Samples int
}

// Stacks reads a hotspots analysis as one row per unique call stack. appOnly
// keeps only application frames in each stack.
func (a *Analysis) Stacks(appOnly bool) StackView {
	v := StackView{Rows: HotspotStacks(a.envelope, appOnly)}
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

// Allocations reads an allocations analysis as one row per unique allocation
// path. appOnly keeps only application frames in each path; bySurvivors ranks
// the rows by survived bytes.
func (a *Analysis) Allocations(appOnly, bySurvivors bool) AllocationView {
	v := AllocationView{Rows: AllocationStacks(a.envelope, appOnly, bySurvivors)}
	for _, r := range v.Rows {
		v.AllocatedBytes += int64(r.AllocBytes)
		v.SurvivedBytes += int64(r.SurvivedBytes)
	}
	return v
}

// Functions reads a hotspots analysis as one row per function; see
// HotspotFunctions. It returns nil when there are no samples of that state.
func (a *Analysis) Functions(appOnly bool, state, sortBy string) *FunctionTable {
	return HotspotFunctions(a.envelope, appOnly, state, sortBy)
}

// Focus reads a hotspots analysis as one function's callers and callees; see
// HotspotFocus.
func (a *Analysis) Focus(appOnly bool, state, pattern string, limit int) (rows []FocusRow, function string, nCallers, nCallees int, err error) {
	return HotspotFocus(a.envelope, appOnly, state, pattern, limit)
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
// empty for the stack views.
func (a *Analysis) WhyEmpty(appOnly bool, state string) (EmptyReason, string) {
	if state != "" && state != "running" {
		return EmptyState, fmt.Sprintf("no %s samples for %s in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting", state, a.Entity)
	}
	// Running samples absent from a per-function view means what an empty
	// stack view means.
	if appOnly && ToCollapsed(a.Kind, a.envelope, 1, false) != "" {
		return EmptyAppOnly, fmt.Sprintf("no stacks for %s: --app-only removed every frame (the profiler classified none as application code) — re-run without --app-only", a.Entity)
	}
	return EmptyNoSamples, fmt.Sprintf("no stacks for %s: no profiling samples in the window (status=%s) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result", a.Entity, a.Status)
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
