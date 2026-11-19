package codelevelanalysis

import (
	"context"
	"encoding/json"
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
	pollInterval    = 1500 * time.Millisecond
	maxPollAttempts = 40
	maxPollErrors   = 40
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

// Kind maps user-facing shorthand to the API kind strings.
var Kind = map[string]string{
	"hotspots":       "methodHotspots",
	"threads":        "threadAnalysis",
	"memory":         "memoryAllocation",
	"memory-details": "memoryAllocationDetails",
}

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
	LeafTypeTotal      = "total"      // omit the filter -> background + service (clean total)
	LeafTypeService    = "service"    // only samples taken while in a traced service context
	LeafTypeBackground = "background" // only samples taken outside any service context
)

const (
	ambientLeafTypeOrdinal = "42"   // AMBIENT_LEAF_TYPE enum ordinal
	leafTypeControlSep     = "\x11" // DC1: separates a filter ordinal from its value
)

// encodeLeafType renders a leaf-type selection into the servicefilter wire
// value. An empty selection (or "total") returns "" so the caller omits the
// param and gets the clean background+service total. The returned string is
// left un-escaped; url.Values.Encode escapes the \x11 as %11 on the wire.
func encodeLeafType(leafType string) (string, error) {
	switch leafType {
	case "", LeafTypeTotal:
		return "", nil
	case LeafTypeBackground, "ambient":
		return ambientLeafTypeOrdinal + leafTypeControlSep + "0", nil
	case LeafTypeService:
		return ambientLeafTypeOrdinal + leafTypeControlSep + "1", nil
	default:
		return "", fmt.Errorf("codelevelanalysis: unknown leaf type %q: use total, service, or background", leafType)
	}
}

// isProcessGroupEntity reports whether id is a process-level entity the
// code-level analysis API accepts: a PROCESS_GROUP, a PROCESS_GROUP_INSTANCE,
// or a PROCESS (the 3rd-gen rename of PROCESS_GROUP_INSTANCE). The allowlist
// exists to reject SERVICE (and other) entities, which are NOT eligible.
// Note: "PROCESS-" does not prefix-match "PROCESS_GROUP-" (the 8th byte is
// '_' vs '-'), so each type is matched distinctly.
func isProcessGroupEntity(id string) bool {
	return strings.HasPrefix(id, "PROCESS_GROUP-") ||
		strings.HasPrefix(id, "PROCESS_GROUP_INSTANCE-") ||
		strings.HasPrefix(id, "PROCESS-")
}

// normalizeEntityID rewrites a 3rd-gen PROCESS-<id> to its classic
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
func normalizeEntityID(id string) string {
	if suffix, ok := strings.CutPrefix(id, "PROCESS-"); ok {
		return "PROCESS_GROUP_INSTANCE-" + suffix
	}
	return id
}

type Payload struct {
	Kind     string `json:"kind"`
	EntityID string `json:"entityId"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	// LeafType selects which CPU samples to attribute (hotspots/threads):
	// "total" (default), "service", or "background". It is the friendly form of
	// ServiceFilter and, when set to a non-total value, populates the
	// servicefilter param via the control-char encoding. See LeafType* consts.
	LeafType string `json:"leafType,omitempty"`
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
	Kind      string      `json:"kind"`
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

func (h *Handler) Run(ctx context.Context, p Payload) (*Response, error) {
	if err := validate(p); err != nil {
		return nil, err
	}

	resp, err := h.submit(ctx, buildSubmitPath(p))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() == 200 {
		return parseResult(p, 0, resp.Body())
	}

	// 202: async task started — extract token and poll
	var pt progressToken
	if err := json.Unmarshal(resp.Body(), &pt); err != nil || pt.Token == "" {
		return nil, fmt.Errorf("codelevelanalysis: submit returned 202 without a progress token")
	}
	// The submit lands on one cluster node, which owns the async task; the
	// serverId header names that node so polls can be routed back to it.
	return h.poll(ctx, p, pt.Token, readServerID(resp))
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
			// CheckResponse only errors on 4xx/5xx, so a 200 or 202 passes.
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
			wait = time.Duration(secs) * time.Second
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
	errCount := 0

	for attempt := 1; attempt <= maxPollAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		resp, err := h.client.HTTP().R().SetContext(ctx).Get(resultPath)
		if err != nil {
			errCount++
			if errCount >= maxPollErrors {
				return nil, fmt.Errorf("codelevelanalysis: poll failed %d times (token %s): %w", maxPollErrors, token, err)
			}
			continue
		}

		sc := resp.StatusCode()
		if sc == 429 {
			// Rate limited (the shared client already retried a few times at the
			// transport layer). Just keep polling — a burst of 429s must not
			// exhaust the error budget and abort an otherwise-healthy analysis.
			continue
		}
		if sc != 200 && sc != 202 && sc != 204 {
			errCount++
			if errCount >= maxPollErrors {
				return nil, fmt.Errorf("codelevelanalysis: poll returned %d %d times (token %s)", sc, maxPollErrors, token)
			}
			continue
		}

		if sc == 204 {
			return &Response{Status: "no-data", Kind: p.Kind, EntityID: p.EntityID, PollCount: attempt}, nil
		}
		if sc == 200 {
			return parseResult(p, attempt, resp.Body())
		}
		// 202: still running. serverId is fixed for the lifetime of the call —
		// the node chosen at submit owns the task — so we keep polling the same one.
	}

	return nil, fmt.Errorf("codelevelanalysis: analysis did not complete within %ds (token %s)",
		int(maxPollAttempts*pollInterval/time.Second), token)
}

func parseResult(p Payload, pollCount int, body []byte) (*Response, error) {
	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("codelevelanalysis: parse result: %w", err)
	}
	return &Response{Status: "completed", Kind: p.Kind, EntityID: p.EntityID, PollCount: pollCount, Result: result}, nil
}

func validate(p Payload) error {
	if p.EntityID == "" {
		return fmt.Errorf("codelevelanalysis: entityId is required")
	}
	if !isProcessGroupEntity(p.EntityID) {
		return fmt.Errorf("codelevelanalysis: entityId must be a PROCESS, PROCESS_GROUP, or PROCESS_GROUP_INSTANCE — services are not eligible; got %q", p.EntityID)
	}
	if _, err := encodeLeafType(p.LeafType); err != nil {
		return err
	}
	if p.From == 0 || p.To == 0 || p.To <= p.From {
		return fmt.Errorf("codelevelanalysis: from/to must be epoch-millis with to > from")
	}
	if p.To-p.From > 24*60*60*1000 {
		return fmt.Errorf("codelevelanalysis: timeframe must not exceed 24 hours")
	}
	if p.Kind == "memoryAllocationDetails" && (p.Type == "" || p.Method == "") {
		return fmt.Errorf("codelevelanalysis: memoryAllocationDetails requires type and method")
	}
	return nil
}

func buildSubmitPath(p Payload) string {
	params := url.Values{}
	params.Set("from", fmt.Sprintf("%d", p.From))
	params.Set("to", fmt.Sprintf("%d", p.To))
	entity := url.PathEscape(normalizeEntityID(p.EntityID))

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

	switch p.Kind {
	case "methodHotspots", "threadAnalysis":
		// LeafType wins when set; otherwise fall back to a raw ServiceFilter.
		// Both encoders were already validated in validate(), so the error here
		// is impossible — ignore it to keep this path total.
		serviceFilter := p.ServiceFilter
		if encoded, _ := encodeLeafType(p.LeafType); encoded != "" {
			serviceFilter = encoded
		}
		setIfSet("servicefilter", serviceFilter)
		setIfTrue("showWaiting", p.ShowWaiting)
		path := "methodhotspots"
		if p.Kind == "threadAnalysis" {
			path = "threadanalysis"
		}
		return fmt.Sprintf("%s/%s/%s?%s", basePath, path, entity, params.Encode())

	case "memoryAllocation":
		setIfSet("problemId", p.ProblemID)
		setIfTrue("survivorsOnly", p.SurvivorsOnly)
		setIfSet("typeFilter", p.TypeFilter)
		setIfSet("apiFilter", p.APIFilter)
		setIfSet("methodFqnFilter", p.MethodFQNFilter)
		return fmt.Sprintf("%s/memoryallocation/%s?%s", basePath, entity, params.Encode())

	case "memoryAllocationDetails":
		setIfSet("problemId", p.ProblemID)
		setIfSet("type", p.Type)
		setIfSet("method", p.Method)
		setIfTrue("survivorsOnly", p.SurvivorsOnly)
		setIfSet("typeFilter", p.TypeFilter)
		setIfSet("apiFilter", p.APIFilter)
		setIfSet("methodFqnFilter", p.MethodFQNFilter)
		return fmt.Sprintf("%s/memoryallocationdetails/%s?%s", basePath, entity, params.Encode())
	}

	panic(fmt.Sprintf("codelevelanalysis: unsupported kind %q", p.Kind))
}
