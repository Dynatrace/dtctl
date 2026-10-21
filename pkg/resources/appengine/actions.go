package appengine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/util/unknownfields"
	sdkae "github.com/dynatrace-oss/dtctl/sdk/api/appengine"
)

type AppAction struct {
	AppID       string `json:"appId" yaml:"appId" table:"APP_ID"`
	ActionName  string `json:"actionName" yaml:"actionName" table:"ACTION"`
	Title       string `json:"title,omitempty" yaml:"title,omitempty" table:"TITLE"`
	Description string `json:"description,omitempty" yaml:"description,omitempty" table:"DESCRIPTION,wide"`
	Stateful    bool   `json:"stateful,omitempty" yaml:"stateful,omitempty" table:"STATEFUL,wide"`
	FullName    string `json:"fullName" yaml:"fullName" table:"FULL_NAME"`

	// Extra holds the manifest attributes with no typed field; MarshalJSON
	// emits them inline.
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

// MarshalJSON emits the modelled fields, then each Extra attribute under its
// own name. A manifest attribute that collides with a modelled field is
// dropped: the typed field wins.
func (a AppAction) MarshalJSON() ([]byte, error) {
	type plain AppAction
	return unknownfields.Marshal(plain(a), a.Extra)
}

func (a AppAction) MarshalYAML() (any, error) {
	type plain AppAction
	return unknownfields.YAML(plain(a), a.Extra)
}

func fromSDKAppAction(s *sdkae.AppAction) (AppAction, error) {
	var extra map[string]json.RawMessage
	for key, value := range s.Extra {
		raw, err := json.Marshal(value)
		if err != nil {
			return AppAction{}, fmt.Errorf("encode action attribute %q: %w", key, err)
		}
		if extra == nil {
			extra = make(map[string]json.RawMessage, len(s.Extra))
		}
		extra[key] = raw
	}
	return AppAction{
		AppID:       s.AppID,
		ActionName:  s.ActionName,
		Title:       s.Title,
		Description: s.Description,
		Stateful:    s.Stateful,
		FullName:    s.FullName,
		Extra:       extra,
	}, nil
}

type ListActionsOptions = sdkae.ListActionsOptions

const (
	MaxActionQueryLength  = sdkae.MaxActionQueryLength
	MaxActionFilterLength = sdkae.MaxActionFilterLength
)

func (h *Handler) ListActions(opts ListActionsOptions) ([]AppAction, error) {
	sdkResult, err := h.sdk.ListActions(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	actions := make([]AppAction, len(sdkResult))
	for i := range sdkResult {
		if actions[i], err = fromSDKAppAction(&sdkResult[i]); err != nil {
			return nil, err
		}
	}
	return actions, nil
}

func (h *Handler) GetAction(fullName string) (*AppAction, error) {
	sdkResult, err := h.sdk.GetAction(context.Background(), fullName)
	if err != nil {
		return nil, err
	}
	a, err := fromSDKAppAction(sdkResult)
	if err != nil {
		return nil, err
	}
	return &a, nil
}
