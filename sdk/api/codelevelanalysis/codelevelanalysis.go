package codelevelanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const (
	basePath        = "/platform-reserved/codelevelanalysis/v0.1"
	maxPollAttempts = 40
	// maxPollTransportErrors bounds consecutive transport errors (no HTTP
	// response at all) while polling. See poll.
	maxPollTransportErrors = 5
	// maxThrottledSubmits bounds how often a throttled (429) submit is sent;
	// maxThrottleWait caps one wait between them. See submit.
	maxThrottledSubmits = 4
	maxThrottleWait     = 60 * time.Second
	// noServerID is the sentinel for "no node preference" — used when the
	// response omits the serverId header.
	noServerID = -1
)

// throttleBaseWait is the first wait after a throttled submit that carries no
// Retry-After; it doubles with every attempt. A variable so tests can shorten it.
var throttleBaseWait = 5 * time.Second

// pollInterval is the wait before each poll. A variable so tests can shorten it.
var pollInterval = 1500 * time.Millisecond

// Phase names the step of Run in which an analysis failed.
type Phase string

const (
	// PhaseValidate: the payload was refused before any request was sent.
	PhaseValidate Phase = "validate"
	// PhaseSubmit: the request that starts the analysis failed.
	PhaseSubmit Phase = "submit"
	// PhasePoll: the analysis was started, and waiting for its result failed.
	PhasePoll Phase = "poll"
)

// Error is the error Run returns. It says in which phase the analysis failed,
// because one status means different things in each: a 404 on the submit is
// an entity the environment does not know, a 404 on a poll an expired or
// unknown task. errors.As finds the *httpclient.APIError underneath when the
// API answered with a failure status, and errors.Is finds ErrNotCompleted and
// the context's error.
type Error struct {
	Phase Phase
	// Err is the failure; its text is the Error's.
	Err error
}

func (e *Error) Error() string { return e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

// ErrNotCompleted is in the chain of the error Run returns when the analysis
// was still running after the last poll.
var ErrNotCompleted = errors.New("codelevelanalysis: analysis did not complete")

// Kind names an analysis by its API name, which is also the kind a Response
// reports.
type Kind string

const (
	// KindHotspots is the method CPU hotspots analysis.
	KindHotspots Kind = "methodHotspots"
	// KindThreads is the per-thread analysis.
	KindThreads Kind = "threadAnalysis"
	// KindAllocations is the memory allocation analysis: where memory is
	// allocated, and what survived GC.
	KindAllocations Kind = "memoryAllocation"
	// KindAllocationDetails is the memory allocation analysis of one
	// allocated type and allocating method (Payload.Type, Payload.Method).
	KindAllocationDetails Kind = "memoryAllocationDetails"
)

// LeafType selects which CPU samples a hotspots or threads analysis
// attributes. The zero value counts as LeafTypeTotal.
type LeafType string

// Leaf-type filter values (hotspots/threads). The code-level analysis API
// splits every CPU sample by whether the thread was inside a traced service
// context (SERVICE) or not (background, historically called "ambient"). The two
// counters are DISJOINT partitions of one CPU budget — the clean total is
// background + service, so omitting the filter never double-counts.
//
// The selection rides inside the servicefilter string using the API's internal
// control-char encoding: "<ordinal>\x11<value>", where \x11 (DC1) separates the
// AMBIENT_LEAF_TYPE control (ordinal 42) from its value — AMBIENT(background)=0,
// SERVICE=1. Omitting servicefilter entirely selects BOTH (the clean total).
const (
	LeafTypeTotal      LeafType = "total"      // omit the filter -> background + service (clean total)
	LeafTypeService    LeafType = "service"    // only samples taken while in a traced service context
	LeafTypeBackground LeafType = "background" // only samples taken outside any service context
)

// ErrUnknownLeafType is the error for a LeafType that is none of the
// constants.
var ErrUnknownLeafType = errors.New("codelevelanalysis: unknown leaf type")

// ParseLeafType returns the LeafType named s, LeafTypeTotal for the empty
// string. A name that is none of the constants is an error that wraps
// ErrUnknownLeafType.
func ParseLeafType(s string) (LeafType, error) {
	switch LeafType(s) {
	case "", LeafTypeTotal:
		return LeafTypeTotal, nil
	case LeafTypeService, LeafTypeBackground:
		return LeafType(s), nil
	}
	return "", fmt.Errorf("%w %q: use total, service, or background", ErrUnknownLeafType, s)
}

const (
	ambientLeafTypeOrdinal = "42"   // AMBIENT_LEAF_TYPE enum ordinal
	leafTypeControlSep     = "\x11" // DC1: separates a filter ordinal from its value
)

// encodeLeafType renders a leaf-type selection into the servicefilter wire
// value. An empty selection (or "total") returns "" so the caller omits the
// param and gets the clean background+service total. The returned string is
// left un-escaped; url.Values.Encode escapes the \x11 as %11 on the wire.
func encodeLeafType(leafType LeafType) (string, error) {
	leafType, err := ParseLeafType(string(leafType))
	if err != nil {
		return "", err
	}
	switch leafType {
	case LeafTypeBackground:
		return ambientLeafTypeOrdinal + leafTypeControlSep + "0", nil
	case LeafTypeService:
		return ambientLeafTypeOrdinal + leafTypeControlSep + "1", nil
	}
	return "", nil
}

// EligibleEntity reports whether id is a process-level entity the code-level
// analysis API accepts: a PROCESS_GROUP, a PROCESS_GROUP_INSTANCE, or a PROCESS
// (the 3rd-gen rename of PROCESS_GROUP_INSTANCE). The allowlist exists to
// reject SERVICE (and other) entities, which are NOT eligible. Run refuses
// every other id; a caller checks first to refuse one with its own wording.
// Note: "PROCESS-" does not prefix-match "PROCESS_GROUP-" (the 8th byte is
// '_' vs '-'), so each type is matched distinctly.
func EligibleEntity(id string) bool {
	return strings.HasPrefix(id, "PROCESS_GROUP-") ||
		strings.HasPrefix(id, "PROCESS_GROUP_INSTANCE-") ||
		strings.HasPrefix(id, "PROCESS-")
}

// NormalizeEntityID rewrites a 3rd-gen PROCESS-<id> to its classic
// PROCESS_GROUP_INSTANCE-<id> form. The code-level analysis API only knows the
// classic id and 404s on the PROCESS- prefix (parameter value: 'PROCESS-...').
//
// PROCESS is the 3rd-gen *rename* of PROCESS_GROUP_INSTANCE: same underlying
// entity, same internal id, only the type prefix differs — so the classic id is
// the same hex suffix with the classic prefix (equivalently, the entity's
// id_classic field). No lookup is needed. Guard on "PROCESS-" specifically so
// PROCESS_GROUP- and PROCESS_GROUP_INSTANCE- (both already classic and accepted)
// pass through untouched — note "PROCESS-" does not prefix-match "PROCESS_GROUP-"
// (8th byte '-' vs '_').
func NormalizeEntityID(id string) string {
	if suffix, ok := strings.CutPrefix(id, "PROCESS-"); ok {
		return "PROCESS_GROUP_INSTANCE-" + suffix
	}
	return id
}

type Payload struct {
	// Kind is the analysis to run: one of the Kind constants.
	Kind     Kind   `json:"kind"`
	EntityID string `json:"entityId"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	// LeafType selects which CPU samples to attribute (hotspots/threads):
	// LeafTypeTotal (default), LeafTypeService, or LeafTypeBackground. It is
	// the friendly form of ServiceFilter and, when set, decides the
	// servicefilter param via the control-char encoding (LeafTypeTotal sends
	// none).
	LeafType LeafType `json:"leafType,omitempty"`
	// ServiceFilter is the raw servicefilter wire value — an escape hatch for
	// SDK callers who already hold an encoded filter. LeafType takes precedence
	// when both are set.
	ServiceFilter   string `json:"serviceFilter,omitempty"`
	ShowWaiting     bool   `json:"showWaiting,omitempty"`
	ProblemID       string `json:"problemId,omitempty"`
	SurvivorsOnly   bool   `json:"survivorsOnly,omitempty"`
	TypeFilter      string `json:"typeFilter,omitempty"`
	APIFilter       string `json:"apiFilter,omitempty"`
	MethodFQNFilter string `json:"methodFqnFilter,omitempty"`
	Type            string `json:"type,omitempty"`
	Method          string `json:"method,omitempty"`
}

type Response struct {
	Status    string      `json:"status"` // "completed" | "no-data"
	Kind      Kind        `json:"kind"`
	EntityID  string      `json:"entityId"`
	PollCount int         `json:"pollCount"`
	Result    interface{} `json:"result"`
}

type progressToken struct {
	Token string `json:"token"`
}

type Handler struct {
	client *httpclient.Client
}

func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// Run runs one analysis and waits for its result. Every error it returns is an
// *Error naming the phase that failed.
func (h *Handler) Run(ctx context.Context, p Payload) (*Response, error) {
	if err := validate(p); err != nil {
		return nil, &Error{Phase: PhaseValidate, Err: err}
	}

	resp, err := h.submit(ctx, buildSubmitPath(p))
	if err != nil {
		return nil, &Error{Phase: PhaseSubmit, Err: err}
	}

	switch sc := resp.StatusCode(); sc {
	case http.StatusOK:
		result, err := parseResult(p, 0, resp.Body())
		if err != nil {
			return nil, &Error{Phase: PhaseSubmit, Err: err}
		}
		return result, nil
	case http.StatusNoContent:
		return &Response{Status: "no-data", Kind: p.Kind, EntityID: p.EntityID}, nil
	case http.StatusAccepted:
		// async task started — extract token and poll
	default:
		// submit already turned 4xx/5xx into an error, so this is a status
		// the API is not known to answer with. It carries no token to poll.
		return nil, &Error{Phase: PhaseSubmit, Err: fmt.Errorf("codelevelanalysis: submit: unexpected status %d", sc)}
	}

	var pt progressToken
	if err := json.Unmarshal(resp.Body(), &pt); err != nil || pt.Token == "" {
		return nil, &Error{Phase: PhaseSubmit, Err: fmt.Errorf("codelevelanalysis: submit returned 202 without a progress token")}
	}
	// The submit lands on one cluster node, which owns the async task; the
	// serverId header names that node so polls can be routed back to it.
	result, err := h.poll(ctx, p, pt.Token, readServerID(resp))
	if err != nil {
		return nil, &Error{Phase: PhasePoll, Err: err}
	}
	return result, nil
}

// submit starts the analysis. An environment limits how many analyses run at
// once and turns the rest away with a 429. The shared client already retries a
// 429 a few times within seconds, which is too short for a running analysis to
// finish, so submit waits longer between attempts: the server's Retry-After when
// it sends one, otherwise an exponential backoff from throttleBaseWait, each
// wait capped at maxThrottleWait, for at most maxThrottledSubmits attempts.
// The submit is a GET, so sending it again cannot start a second analysis that
// the first one already started.
func (h *Handler) submit(ctx context.Context, path string) (*resty.Response, error) {
	for attempt := 1; ; attempt++ {
		resp, err := h.client.HTTP().R().SetContext(ctx).Get(path)
		if err != nil {
			return nil, fmt.Errorf("codelevelanalysis: submit: %w", err)
		}
		if resp.StatusCode() != http.StatusTooManyRequests || attempt >= maxThrottledSubmits {
			// CheckResponse only errors on 4xx/5xx; Run reads the other statuses.
			if err := httpclient.CheckResponse(resp); err != nil {
				return nil, fmt.Errorf("codelevelanalysis: submit: %w", err)
			}
			return resp, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(throttleWait(resp, attempt)):
		}
	}
}

// throttleWait is how long to wait after the attempt-th submit was throttled.
// Retry-After may be delay-seconds or an HTTP date (RFC 9110 section 10.2.3).
func throttleWait(resp *resty.Response, attempt int) time.Duration {
	wait := throttleBaseWait << (attempt - 1)
	if v := strings.TrimSpace(resp.Header().Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			// Capped before the multiplication, which would wrap around to a
			// negative wait for a large enough number of seconds.
			wait = time.Duration(min(secs, int(maxThrottleWait/time.Second))) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			wait = max(time.Until(t), 0)
		}
	}
	return min(wait, maxThrottleWait)
}

// readServerID extracts the responsible cluster node from a response's serverId
// header. http.Header.Get is case-insensitive, so any header casing matches.
// Returns noServerID when the header is absent or unparseable.
func readServerID(resp *resty.Response) int {
	v := strings.TrimSpace(resp.Header().Get("serverId"))
	if v == "" {
		return noServerID
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return noServerID
	}
	return n
}

func (h *Handler) poll(ctx context.Context, p Payload, token string, serverID int) (*Response, error) {
	// serverId is fixed for the whole call: the node chosen at submit owns the
	// async task, so every poll must be routed back to it.
	resultPath := fmt.Sprintf("%s/result?token=%s&serverId=%d", basePath, url.QueryEscape(token), serverID)
	transportErrors := 0
	// failed is why the latest poll did not answer for the analysis: nil when
	// it said the analysis is still running.
	var failed error

	for attempt := 1; attempt <= maxPollAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		resp, err := h.client.HTTP().R().SetContext(ctx).Get(resultPath)
		if err != nil {
			// No response at all (a dropped connection, a timeout), after the
			// shared client's own retries. The poll is a GET and the task keeps
			// running on the server, so poll again, within the poll budget and at
			// most maxPollTransportErrors times in a row.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			transportErrors++
			if transportErrors >= maxPollTransportErrors {
				return nil, fmt.Errorf("codelevelanalysis: poll failed %d times in a row (token %s): %w", transportErrors, token, err)
			}
			failed = fmt.Errorf("codelevelanalysis: poll failed on the last of %d attempts (token %s): %w", maxPollAttempts, token, err)
			continue
		}
		transportErrors = 0

		switch sc := resp.StatusCode(); sc {
		case http.StatusOK:
			return parseResult(p, attempt, resp.Body())
		case http.StatusNoContent:
			return &Response{Status: "no-data", Kind: p.Kind, EntityID: p.EntityID, PollCount: attempt}, nil
		case http.StatusAccepted:
			// Still running: poll again.
			failed = nil
		case http.StatusTooManyRequests:
			// Rate limited (the shared client already retried a few times at
			// the transport layer); a burst of 429s must not abort an
			// otherwise-healthy analysis, so keep polling the same node. 202
			// and 429 are the only statuses that are polled again.
			failed = fmt.Errorf("codelevelanalysis: poll (token %s): %w", token, httpclient.CheckResponse(resp))
		default:
			// Any other status is terminal: an expired or unknown task (404), a
			// missing scope (403), a server error. Report it as the API's own
			// error right away rather than polling it out. Only a missing
			// response (above) or 202/429 is polled again.
			if err := httpclient.CheckResponse(resp); err != nil {
				return nil, fmt.Errorf("codelevelanalysis: poll (token %s): %w", token, err)
			}
			return nil, fmt.Errorf("codelevelanalysis: poll (token %s): unexpected status %d", token, sc)
		}
	}

	if failed != nil {
		// The budget ended on a poll that got no answer about the analysis: it
		// was not seen still running, so this is that poll's failure.
		return nil, failed
	}
	return nil, fmt.Errorf("%w within %ds (token %s)", ErrNotCompleted, int(maxPollAttempts*pollInterval/time.Second), token)
}

func parseResult(p Payload, pollCount int, body []byte) (*Response, error) {
	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("codelevelanalysis: parse result: %w", err)
	}
	return &Response{Status: "completed", Kind: p.Kind, EntityID: p.EntityID, PollCount: pollCount, Result: result}, nil
}

func validate(p Payload) error {
	switch p.Kind {
	case KindHotspots, KindThreads, KindAllocations, KindAllocationDetails:
	default:
		return fmt.Errorf("codelevelanalysis: unknown kind %q: use %s, %s, %s, or %s", string(p.Kind),
			KindHotspots, KindThreads, KindAllocations, KindAllocationDetails)
	}
	if p.EntityID == "" {
		return fmt.Errorf("codelevelanalysis: entityId is required")
	}
	if !EligibleEntity(p.EntityID) {
		return fmt.Errorf("codelevelanalysis: entityId must be a PROCESS, PROCESS_GROUP, or PROCESS_GROUP_INSTANCE — services are not eligible; got %q", p.EntityID)
	}
	if _, err := encodeLeafType(p.LeafType); err != nil {
		return err
	}
	if p.To <= p.From {
		return fmt.Errorf("codelevelanalysis: from/to must be epoch-millis with to > from")
	}
	// To is after From here, so a negative difference is one that overflowed.
	if d := p.To - p.From; d > 24*60*60*1000 || d < 0 {
		return fmt.Errorf("codelevelanalysis: timeframe must not exceed 24 hours")
	}
	if p.Kind == KindAllocationDetails && (p.Type == "" || p.Method == "") {
		return fmt.Errorf("codelevelanalysis: memoryAllocationDetails requires type and method")
	}
	return nil
}

func buildSubmitPath(p Payload) string {
	params := url.Values{}
	params.Set("from", fmt.Sprintf("%d", p.From))
	params.Set("to", fmt.Sprintf("%d", p.To))
	entity := httpclient.PathSegment(NormalizeEntityID(p.EntityID))

	setIfTrue := func(key string, v bool) {
		if v {
			params.Set(key, "true")
		}
	}
	setIfSet := func(key, v string) {
		if v != "" {
			params.Set(key, v)
		}
	}

	path := "memoryallocationdetails"
	switch p.Kind {
	case KindHotspots, KindThreads:
		// LeafType wins when set, also when it selects the total (no filter);
		// otherwise fall back to a raw ServiceFilter. validate() already
		// checked the leaf type, so the error here is impossible — ignore it
		// to keep this path total.
		serviceFilter := p.ServiceFilter
		if p.LeafType != "" {
			serviceFilter, _ = encodeLeafType(p.LeafType)
		}
		setIfSet("servicefilter", serviceFilter)
		setIfTrue("showWaiting", p.ShowWaiting)
		path = "methodhotspots"
		if p.Kind == KindThreads {
			path = "threadanalysis"
		}

	case KindAllocations:
		setIfSet("problemId", p.ProblemID)
		setIfTrue("survivorsOnly", p.SurvivorsOnly)
		setIfSet("typeFilter", p.TypeFilter)
		setIfSet("apiFilter", p.APIFilter)
		setIfSet("methodFqnFilter", p.MethodFQNFilter)
		path = "memoryallocation"

	default:
		// KindAllocationDetails: validate admits no other kind.
		setIfSet("problemId", p.ProblemID)
		setIfSet("type", p.Type)
		setIfSet("method", p.Method)
		setIfTrue("survivorsOnly", p.SurvivorsOnly)
		setIfSet("typeFilter", p.TypeFilter)
		setIfSet("apiFilter", p.APIFilter)
		setIfSet("methodFqnFilter", p.MethodFQNFilter)
	}
	return fmt.Sprintf("%s/%s/%s", basePath, path, entity) + "?" + params.Encode()
}
