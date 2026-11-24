package profiling

import (
	"strings"
	"testing"
)

// The API's childIds form a tree. A malformed result whose ids repeat or form a
// cycle must not recurse forever: a Go stack overflow is fatal and no recover
// catches it. Each walk visits a node once, so these return.

// cyclicHotspots: main -> load -> main (a cycle), and handle lists itself.
func cyclicHotspots() map[string]interface{} {
	return envelope("A",
		hnode("A", "app", "main", true, 100, 0, "B", "D"),
		hnode("B", "lib", "load", false, 60, 0, "A"),
		hnode("D", "app", "handle", true, 30, 0, "D"),
	)
}

func TestToCollapsed_CyclicTreeTerminates(t *testing.T) {
	out := ToCollapsed("methodHotspots", cyclicHotspots(), 0, false)
	if !strings.Contains(out, "app.main") {
		t.Errorf("want the reachable stacks, got %q", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Count(line, "app.main") > 1 {
			t.Errorf("a node was walked twice: %q", line)
		}
	}
}

func TestHotspotViews_CyclicTreeTerminates(t *testing.T) {
	if HotspotFunctions(cyclicHotspots(), false, "running", "self") == nil {
		t.Fatal("functions view: want rows for the reachable nodes")
	}
	if _, _, _, _, err := HotspotFocus(cyclicHotspots(), false, "running", "main", 10); err != nil {
		t.Fatalf("focus view: %v", err)
	}
}

func TestMemoryCollapsed_CyclicTreeTerminates(t *testing.T) {
	mnode := func(id float64, name string, children ...interface{}) map[string]interface{} {
		return map[string]interface{}{
			"id": id, "name": name, "childIds": children,
			"apiInfo":        map[string]interface{}{"systemApi": true},
			"allocationInfo": map[string]interface{}{"allocationSize": float64(100), "allocationCount": float64(1)},
		}
	}
	raw := map[string]interface{}{"result": map[string]interface{}{"analysisResult": map[string]interface{}{
		"stacktreeRootIds": []interface{}{float64(1)},
		"stacktreeNodes": []interface{}{
			mnode(1, "Site.alloc", float64(2), float64(3)),
			mnode(2, "Caller.run", float64(1)), // back to the root
			mnode(3, "Other.run"),
		},
	}}}
	if out := ToCollapsed("memoryAllocation", raw, 0, false); !strings.Contains(out, "Site.alloc;Other.run") {
		t.Errorf("want the acyclic path, got %q", out)
	}
}
