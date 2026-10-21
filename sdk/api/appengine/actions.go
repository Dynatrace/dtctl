package appengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// AppAction is one action of an installed app, as the App Registry's
// /actions API serves it.
type AppAction struct {
	AppID       string `json:"appId"`
	ActionName  string `json:"actionName"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Stateful    bool   `json:"stateful,omitempty"`
	FullName    string `json:"fullName"`

	// Extra holds the action's manifest attributes with no field above, such
	// as docs. Populated by GetAction only: GET /actions serves a fixed field
	// set, so ListActions never has them.
	Extra map[string]interface{} `json:"extra,omitempty"`
}

// modelledActionKeys are the action keys AppAction gives a typed field.
// They are excluded from Extra; adding a field above means adding its key
// here, or it will be reported in both places.
var modelledActionKeys = map[string]bool{
	"name":        true,
	"title":       true,
	"description": true,
	"stateful":    true,
}

// actionRecord is the wire shape of one action in GET /actions and of the
// body of GET /actions/{appId}/{actionName}: the declaring app by id, and the
// action as the app's manifest declares it.
type actionRecord struct {
	App struct {
		ID string `json:"id"`
	} `json:"app"`
	Action map[string]interface{} `json:"action"`
}

type actionListResponse struct {
	Actions []actionRecord `json:"actions"`
}

// listActionFields widens GET /actions beyond its default fields (app.id,
// action.name, action.title, action.description) with the other field
// AppAction models. The prefix is required: a bare "stateful" is a 400.
const listActionFields = "action.stateful"

// MaxActionQueryLength is the longest search string the endpoint accepts.
const MaxActionQueryLength = 256

// MaxActionFilterLength is the longest filter expression the endpoint accepts.
const MaxActionFilterLength = 256

// ListActionsOptions narrows what ListActions returns.
type ListActionsOptions struct {
	// AppID keeps only the actions of one app, by exact id.
	AppID string

	// Query is a whitespace-separated list of search terms. Each term must
	// appear in the app id, app name, action name, title or description;
	// terms are case-insensitive and each one narrows further.
	Query string

	// Filter is a platform filter expression on app.id and the action's
	// fields, nested paths included, e.g.
	// "not (action.docs.inputSchema is-empty)". It is sent as is; combined
	// with Query, an action must match both.
	Filter string
}

// ListActions lists the actions of installed apps.
//
// Query and Filter are sent upstream; AppID is applied here, so it needs no
// filter-expression quoting and cannot collide with a caller's Filter.
func (h *Handler) ListActions(ctx context.Context, opts ListActionsOptions) ([]AppAction, error) {
	if n := utf8.RuneCountInString(opts.Query); n > MaxActionQueryLength {
		return nil, fmt.Errorf("list actions: Query is %d characters, the maximum is %d", n, MaxActionQueryLength)
	}
	if n := utf8.RuneCountInString(opts.Filter); n > MaxActionFilterLength {
		return nil, fmt.Errorf("list actions: Filter is %d characters, the maximum is %d", n, MaxActionFilterLength)
	}

	req := h.client.HTTP().R().SetContext(ctx).SetQueryParam("add-fields", listActionFields)
	if opts.Query != "" {
		req.SetQueryParam("search", opts.Query)
	}
	if opts.Filter != "" {
		req.SetQueryParam("filter", opts.Filter)
	}

	resp, err := req.Get("/platform/app-engine/registry/v1/actions")
	if err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}

	var result actionListResponse
	if err := decodeAction(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse actions response: %w", err)
	}

	var actions []AppAction
	for _, record := range result.Actions {
		if opts.AppID != "" && record.App.ID != opts.AppID {
			continue
		}
		if action, ok := newAppAction(record.App.ID, record.Action, withoutExtra); ok {
			actions = append(actions, action)
		}
	}

	// The endpoint sorts by action name, then app id. Sort by app id first
	// instead, so an app's actions stay together, as `get functions` lists
	// an app's functions.
	slices.SortFunc(actions, func(a, b AppAction) int {
		if c := strings.Compare(a.AppID, b.AppID); c != 0 {
			return c
		}
		return strings.Compare(a.ActionName, b.ActionName)
	})

	return actions, nil
}

// GetAction returns a single action addressed as "app-id:action-name", the
// form workflow definitions use.
func (h *Handler) GetAction(ctx context.Context, fullName string) (*AppAction, error) {
	appID, actionName, err := ParseActionID(fullName)
	if err != nil {
		return nil, err
	}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/app-engine/registry/v1/actions/%s/%s",
			httpclient.PathSegment(appID), httpclient.PathSegment(actionName)))
	if err != nil {
		return nil, fmt.Errorf("get action: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get action %q: %w", fullName, err)
	}

	var record actionRecord
	if err := decodeAction(resp.Body(), &record); err != nil {
		return nil, fmt.Errorf("parse action response: %w", err)
	}

	action, ok := newAppAction(record.App.ID, record.Action, withExtra)
	if !ok {
		return nil, fmt.Errorf("action %q: response carries no action name", fullName)
	}
	return &action, nil
}

// decodeAction decodes a response body with UseNumber, so a number in an
// attribute AppAction does not model keeps the exact token the server sent
// rather than being rounded through float64.
func decodeAction(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	return dec.Decode(v)
}

// ParseActionID splits an action id of the form "app-id:action-name", the
// form workflow definitions use. "app-id/action-name", the form function ids
// use, is accepted too but never advertised: error messages and
// FormatActionID always spell the colon. It splits at the first colon or
// slash, whichever comes first: an app id can contain neither, an action name
// can contain both.
func ParseActionID(id string) (appID, actionName string, err error) {
	i := strings.IndexAny(id, ":/")
	if i <= 0 || i == len(id)-1 {
		return "", "", fmt.Errorf("invalid action id %q, expected 'app-id:action-name'", id)
	}
	return id[:i], id[i+1:], nil
}

// FormatActionID is the inverse of ParseActionID, in the canonical colon form.
func FormatActionID(appID, actionName string) string {
	return appID + ":" + actionName
}

// extraMode says whether an action keeps the keys AppAction does not model.
type extraMode bool

const (
	withExtra    extraMode = true
	withoutExtra extraMode = false
)

// newAppAction builds an action from a response's "action" object. It reports
// false for one without a name, which no command could address.
func newAppAction(appID string, entry map[string]interface{}, mode extraMode) (AppAction, bool) {
	name, _ := entry["name"].(string)
	if name == "" {
		return AppAction{}, false
	}
	title, _ := entry["title"].(string)
	description, _ := entry["description"].(string)
	stateful, _ := entry["stateful"].(bool)

	var extra map[string]interface{}
	if mode == withExtra {
		for key, value := range entry {
			if modelledActionKeys[key] {
				continue
			}
			if extra == nil {
				extra = make(map[string]interface{})
			}
			extra[key] = value
		}
	}

	return AppAction{
		AppID:       appID,
		ActionName:  name,
		Title:       title,
		Description: description,
		Stateful:    stateful,
		FullName:    FormatActionID(appID, name),
		Extra:       extra,
	}, true
}
