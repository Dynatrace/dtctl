//go:build integration
// +build integration

package e2e

import (
	"errors"
	"net/http"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
	"github.com/dynatrace-oss/dtctl/test/integration"
)

// listActionsOrSkip lists every action, skipping the test where the App
// Registry's /actions API is not deployed: it is early adopter, and a 404
// there is the development tier working as intended, not drift.
func listActionsOrSkip(t *testing.T, handler *appengine.Handler) []appengine.AppAction {
	t.Helper()
	actions, err := handler.ListActions(appengine.ListActionsOptions{})
	var apiErr *httpclient.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		t.Skipf("/actions is not deployed on this environment: %v", err)
	}
	if err != nil {
		t.Fatalf("Failed to list actions: %v", err)
	}
	if len(actions) == 0 {
		t.Skip("No app declares an action in this environment")
	}
	return actions
}

func TestActionList(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := appengine.NewHandler(env.Client)
	actions := listActionsOrSkip(t, handler)
	first := actions[0]

	t.Run("list all actions", func(t *testing.T) {
		for _, a := range actions {
			if a.AppID == "" || a.ActionName == "" {
				t.Errorf("action without app id or name: %+v", a)
			}
			if want := a.AppID + ":" + a.ActionName; a.FullName != want {
				t.Errorf("FullName = %q, want %q", a.FullName, want)
			}
		}
		t.Logf("Found %d action(s)", len(actions))
	})

	t.Run("narrow by app", func(t *testing.T) {
		result, err := handler.ListActions(appengine.ListActionsOptions{AppID: first.AppID})
		if err != nil {
			t.Fatalf("Failed to list actions of %q: %v", first.AppID, err)
		}
		if len(result) == 0 {
			t.Fatalf("no actions for %q, which the full list has", first.AppID)
		}
		for _, a := range result {
			if a.AppID != first.AppID {
				t.Errorf("got an action of %q while narrowing to %q", a.AppID, first.AppID)
			}
		}
	})

	t.Run("search by action name", func(t *testing.T) {
		result, err := handler.ListActions(appengine.ListActionsOptions{Query: first.ActionName})
		if err != nil {
			t.Fatalf("Failed to search actions for %q: %v", first.ActionName, err)
		}
		found := false
		for _, a := range result {
			found = found || a.FullName == first.FullName
		}
		if !found {
			t.Errorf("searching %q did not return %q", first.ActionName, first.FullName)
		}
	})

	t.Run("filter by app id", func(t *testing.T) {
		result, err := handler.ListActions(appengine.ListActionsOptions{Filter: "app.id = '" + first.AppID + "'"})
		if err != nil {
			t.Fatalf("Failed to filter actions: %v", err)
		}
		if len(result) == 0 {
			t.Fatalf("filter on %q returned nothing", first.AppID)
		}
		for _, a := range result {
			if a.AppID != first.AppID {
				t.Errorf("filter on %q returned an action of %q", first.AppID, a.AppID)
			}
		}
	})

	t.Run("search with no matches", func(t *testing.T) {
		result, err := handler.ListActions(appengine.ListActionsOptions{Query: "zzz-nonexistent-action-zzz"})
		if err != nil {
			t.Fatalf("Failed to search actions: %v", err)
		}
		if result == nil || len(result) != 0 {
			t.Errorf("expected an empty, non-nil list, got %#v", result)
		}
	})
}

func TestActionGet(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := appengine.NewHandler(env.Client)
	first := listActionsOrSkip(t, handler)[0]

	t.Run("get by colon id", func(t *testing.T) {
		a, err := handler.GetAction(first.FullName)
		if err != nil {
			t.Fatalf("Failed to get action %q: %v", first.FullName, err)
		}
		if a.FullName != first.FullName || a.Title != first.Title {
			t.Errorf("got %q (%q), want %q (%q)", a.FullName, a.Title, first.FullName, first.Title)
		}
		t.Logf("Got action %s with %d extra attribute(s)", a.FullName, len(a.Extra))
	})

	t.Run("get by slash id", func(t *testing.T) {
		a, err := handler.GetAction(first.AppID + "/" + first.ActionName)
		if err != nil {
			t.Fatalf("Failed to get action by slash id: %v", err)
		}
		if a.FullName != first.FullName {
			t.Errorf("FullName = %q, want the colon form %q", a.FullName, first.FullName)
		}
	})

	t.Run("get non-existent action", func(t *testing.T) {
		_, err := handler.GetAction(first.AppID + ":zzz-nonexistent-action-zzz")
		var apiErr *httpclient.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
			t.Errorf("expected a 404, got %v", err)
		}
	})
}
