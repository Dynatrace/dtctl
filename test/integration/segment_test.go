//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/segment"
)

// TestSegment_CRUD tests the full create/get/update/delete lifecycle for
// filter segments, including DQL ↔ AST filter round-tripping.
func TestSegment_CRUD(t *testing.T) {
	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	h := segment.NewHandler(env.Client)

	// --- Create ---------------------------------------------------------
	fixture := SegmentFixture(env.TestPrefix)
	created, err := h.Create(fixture)
	if err != nil {
		t.Fatalf("Create() failed: %v", err)
	}
	env.Cleanup.Track("segment", created.UID, created.Name)

	if created.UID == "" {
		t.Fatal("expected non-empty UID after create")
	}
	t.Logf("Created segment: %s (UID: %s)", created.Name, created.UID)

	// --- Get ------------------------------------------------------------
	got, err := h.Get(created.UID)
	if err != nil {
		t.Fatalf("Get(%s) failed: %v", created.UID, err)
	}

	if got.Name != created.Name {
		t.Errorf("Get() name = %q, want %q", got.Name, created.Name)
	}

	// Verify AST→DQL conversion: the API stores JSON AST, but Get should
	// return human-readable DQL after convertIncludesForDisplay.
	if len(got.Includes) == 0 {
		t.Fatal("expected at least one include after Get")
	}
	for i, inc := range got.Includes {
		if inc.Filter == "" {
			t.Errorf("include[%d] filter is empty", i)
		}
		// The filter should be plain DQL (not start with '{')
		if len(inc.Filter) > 0 && inc.Filter[0] == '{' {
			t.Errorf("include[%d] filter is still AST after Get (expected DQL): %s", i, inc.Filter)
		}
		t.Logf("include[%d]: dataObject=%s filter=%q", i, inc.DataObject, inc.Filter)
	}

	// --- List -----------------------------------------------------------
	list, err := h.List()
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}

	found := false
	for _, seg := range list.FilterSegments {
		if seg.UID == created.UID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created segment %s not found in List() results", created.UID)
	}

	// --- Update ---------------------------------------------------------
	modifiedFixture := SegmentFixtureModified(env.TestPrefix)
	err = h.Update(created.UID, got.Version, modifiedFixture)
	if err != nil {
		t.Fatalf("Update(%s) failed: %v", created.UID, err)
	}

	// Verify update took effect and DQL round-trip still works
	updated, err := h.Get(created.UID)
	if err != nil {
		t.Fatalf("Get(%s) after update failed: %v", created.UID, err)
	}
	if len(updated.Includes) < 2 {
		t.Errorf("expected at least 2 includes after update, got %d", len(updated.Includes))
	}
	for i, inc := range updated.Includes {
		if len(inc.Filter) > 0 && inc.Filter[0] == '{' {
			t.Errorf("include[%d] filter is still AST after update+get (expected DQL): %s", i, inc.Filter)
		}
	}

	// --- GetRaw (edit workflow) ------------------------------------------
	raw, err := h.GetRaw(created.UID)
	if err != nil {
		t.Fatalf("GetRaw(%s) failed: %v", created.UID, err)
	}
	var rawSeg segment.FilterSegment
	if err := json.Unmarshal(raw, &rawSeg); err != nil {
		t.Fatalf("GetRaw returned invalid JSON: %v", err)
	}
	// GetRaw should also have DQL filters (not AST)
	for i, inc := range rawSeg.Includes {
		if len(inc.Filter) > 0 && inc.Filter[0] == '{' {
			t.Errorf("GetRaw include[%d] filter is still AST (expected DQL): %s", i, inc.Filter)
		}
	}

	// --- Delete ---------------------------------------------------------
	err = h.Delete(created.UID)
	if err != nil {
		t.Fatalf("Delete(%s) failed: %v", created.UID, err)
	}
	env.Cleanup.Untrack("segment", created.UID)

	// Verify deletion
	_, err = h.Get(created.UID)
	if err == nil {
		t.Error("expected error after deleting segment, got nil")
	}
	if !segment.IsNotFound(err) {
		t.Errorf("expected IsNotFound error, got: %v", err)
	}
}

// TestSegment_MultiInclude tests segments with complex filter expressions
// (OR-combined, multiple includes) to verify DQL round-trip fidelity.
func TestSegment_MultiInclude(t *testing.T) {
	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	h := segment.NewHandler(env.Client)

	fixture := SegmentFixtureMultiInclude(env.TestPrefix)
	created, err := h.Create(fixture)
	if err != nil {
		t.Fatalf("Create() failed: %v", err)
	}
	env.Cleanup.Track("segment", created.UID, created.Name)

	got, err := h.Get(created.UID)
	if err != nil {
		t.Fatalf("Get(%s) failed: %v", created.UID, err)
	}

	if len(got.Includes) == 0 {
		t.Fatal("expected at least one include")
	}

	// The multi-include fixture uses an OR filter: loglevel = "ERROR" OR loglevel = "WARN"
	// Verify the round-trip preserves the OR structure.
	for i, inc := range got.Includes {
		if len(inc.Filter) > 0 && inc.Filter[0] == '{' {
			t.Errorf("include[%d] filter is AST (expected DQL): %s", i, inc.Filter)
		}
		t.Logf("multi-include[%d]: %q", i, inc.Filter)
	}
}

// TestSegment_ComplexFilter tests segments with complex filter expressions
// (parenthesized groups, mixed AND/OR, implicit AND, multiple operators)
// to verify DQL round-trip fidelity for non-trivial filters.
func TestSegment_ComplexFilter(t *testing.T) {
	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	h := segment.NewHandler(env.Client)

	fixture := SegmentFixtureComplexFilter(env.TestPrefix)
	created, err := h.Create(fixture)
	if err != nil {
		t.Fatalf("Create() failed: %v", err)
	}
	env.Cleanup.Track("segment", created.UID, created.Name)

	got, err := h.Get(created.UID)
	if err != nil {
		t.Fatalf("Get(%s) failed: %v", created.UID, err)
	}

	if len(got.Includes) < 2 {
		t.Fatalf("expected at least 2 includes, got %d", len(got.Includes))
	}

	// Verify all filters came back as DQL (not AST)
	for i, inc := range got.Includes {
		if len(inc.Filter) > 0 && inc.Filter[0] == '{' {
			t.Errorf("include[%d] filter is AST (expected DQL): %s", i, inc.Filter)
		}
		t.Logf("complex-include[%d]: %q", i, inc.Filter)
	}

	// Verify the parenthesized OR group survived the round-trip
	logsFilter := got.Includes[0].Filter
	if logsFilter != `(status = "ERROR" OR status = "WARN") AND dt.system.bucket = "custom-logs"` {
		t.Errorf("unexpected logs filter after round-trip: %q", logsFilter)
	}

	// Verify implicit AND with equality operators survived
	spansFilter := got.Includes[1].Filter
	if spansFilter != `span.kind = "CLIENT" http.method = "GET"` {
		t.Errorf("unexpected spans filter after round-trip: %q", spansFilter)
	}
}
