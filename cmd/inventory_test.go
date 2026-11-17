package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	cmdtestutil "github.com/Dynatrace/dtctl/cmd/testutil"
	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/exec"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/sdk/inventory"
)

func inventoryFixture() *inventory.Inventory {
	return &inventory.Inventory{
		Context:      "example",
		GeneratedAt:  "2026-01-02T03:04:05Z",
		Capabilities: []string{"hosts", "logs", "spans"},
		Absent: []inventory.CapabilityStatus{
			{Name: "rum", Evidence: "no user.events in the data-object catalog"},
		},
		Unknown: []inventory.CapabilityStatus{
			{Name: "genai", Evidence: "probe failed: scan limit exceeded"},
		},
		EntityTypes: map[string]int64{"HOST": 12, "SERVICE": 40, "K8S_POD": 200},
		DataObjects: []string{"logs", "spans"},
		EntityViews: 2,
		QueryOnly:   []string{"metrics"},
		Buckets:     []string{"default_logs", "default_spans"},
		Segments:    []inventory.SegmentInfo{{UID: "seg-1", Name: "prod", Description: "production workloads"}},
		Notes:       []string{"catalog objects without fetch support: metrics"},
		Discovery:   &inventory.Report{Queries: 4, Seconds: 2.5},
	}
}

// TestLoadDefinitionsFile covers the CLI-side file loading around the SDK's
// byte-based ParseDefinitions: a valid file loads, and both read and parse
// failures carry the path so the user knows which --definitions file broke.
func TestLoadDefinitionsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defs.yaml")
	content := "kind: InventoryDefinitions\ncapabilities:\n  postgres:\n    entityTypes: [DB_INSTANCE_POSTGRES]\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	defs, err := loadDefinitionsFile(context.Background(), path)
	if err != nil {
		t.Fatalf("loadDefinitionsFile() error: %v", err)
	}
	if defs.Capabilities["postgres"] == nil {
		t.Errorf("postgres capability missing: %+v", defs.Capabilities)
	}

	if _, err := loadDefinitionsFile(context.Background(), filepath.Join(t.TempDir(), "absent.yaml")); err == nil || !strings.Contains(err.Error(), "absent.yaml") {
		t.Errorf("read error must carry the path, got: %v", err)
	}

	broken := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(broken, []byte("capabilities:\n  broken: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDefinitionsFile(context.Background(), broken); err == nil || !strings.Contains(err.Error(), "broken.yaml") || !strings.Contains(err.Error(), "exactly one discovery shape") {
		t.Errorf("parse error must carry path and cause, got: %v", err)
	}
}

func TestTopCensusTypes(t *testing.T) {
	census := map[string]int64{"HOST": 12, "SERVICE": 40, "K8S_POD": 40, "AWS_LAMBDA": 1}
	// Ordered by count desc, ties broken by name.
	if got, want := topCensusTypes(census, 12), "K8S_POD:40 SERVICE:40 HOST:12 AWS_LAMBDA:1"; got != want {
		t.Errorf("topCensusTypes = %q, want %q", got, want)
	}
	// Truncation reports how many types were dropped.
	if got, want := topCensusTypes(census, 2), "K8S_POD:40 SERVICE:40 (+2 more types)"; got != want {
		t.Errorf("topCensusTypes(n=2) = %q, want %q", got, want)
	}
}

func TestCapNames(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	if got := strings.Join(capNames(names, 4), ", "); got != "a, b, c, d" {
		t.Errorf("capNames(4) = %q, want all names", got)
	}
	if got := strings.Join(capNames(names, 2), ", "); got != "a, b, (+2 more — see -o json)" {
		t.Errorf("capNames(2) = %q", got)
	}
}

func TestPrintInventoryHumanCapsSegments(t *testing.T) {
	originalPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.plainMode = originalPlainMode
		output.ResetColorCache()
	}()
	gFlags.plainMode = true
	output.ResetColorCache()

	inv := inventoryFixture()
	inv.Segments = nil
	for i := 0; i < 14; i++ {
		inv.Segments = append(inv.Segments, inventory.SegmentInfo{UID: "s", Name: string(rune('a' + i))})
	}
	got := captureStdout(t, func() { printInventoryHuman(context.Background(), inv) })
	if !strings.Contains(got, "(+4 more — full list with -o json)") {
		t.Errorf("segment list must be capped in human output:\n%s", got)
	}
	if strings.Contains(got, "\n  k\n") {
		t.Errorf("segments beyond the cap must not be listed:\n%s", got)
	}
}

func TestPrintInventoryHumanGolden(t *testing.T) {
	originalPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.plainMode = originalPlainMode
		output.ResetColorCache()
	}()
	gFlags.plainMode = true
	output.ResetColorCache()

	got := captureStdout(t, func() {
		printInventoryHuman(context.Background(), inventoryFixture())
	})
	// The collapsed dt.entity.* views surface as a count suffix.
	if !strings.Contains(got, "(+2 dt.entity.* lookback views)") {
		t.Errorf("missing entity-view count in human output:\n%s", got)
	}
	cmdtestutil.AssertGolden(t, "inventory/human", cmdtestutil.StripANSI(got))
}

func TestInventoryOutputGolden(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			printer := output.NewPrinterWithOptions(format, &buf, false)
			if err := printer.Print(inventoryFixture()); err != nil {
				t.Fatalf("print failed: %v", err)
			}
			cmdtestutil.AssertGolden(t, "inventory/"+format, buf.String())
		})
	}
}

func TestInventoryOutputGoldenAgent(t *testing.T) {
	var buf bytes.Buffer
	printer := output.NewAgentPrinter(&buf, &output.ResponseContext{Verb: "inventory"})
	if err := printer.Print(inventoryFixture()); err != nil {
		t.Fatalf("print failed: %v", err)
	}
	cmdtestutil.AssertGolden(t, "inventory/agent", buf.String())
}

// grailRecorder serves one canned query:execute body and records the
// maxResultBytes each request carried, so a test can see what a discovery
// probe asked Grail for.
type grailRecorder struct {
	srv  *httptest.Server
	seen []map[string]any
}

func newGrailRecorder(t *testing.T, body string) *grailRecorder {
	t.Helper()
	g := &grailRecorder{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		g.seen = append(g.seen, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *grailRecorder) runner(t *testing.T, maxResultBytes int64) *inventoryRunner {
	t.Helper()
	c, err := client.NewForTesting(g.srv.URL, "test-token")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return &inventoryRunner{executor: exec.NewDQLExecutor(c), scanLimitGB: 25, maxResultBytes: maxResultBytes}
}

const oneRowBody = `{"state":"SUCCEEDED","result":{"records":[{"n":1}],"metadata":{"grail":{"queryId":"q"}}}}`

// A probe carries --max-result-bytes as maxResultBytes. Unset, the request
// stays as it was before the flag existed: no maxResultBytes, platform default.
func TestInventoryRunnerSendsMaxResultBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int64
		want  any // nil = key absent
	}{
		{"set", 1000000, float64(1000000)},
		{"unset", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGrailRecorder(t, oneRowBody)
			if _, err := g.runner(t, tc.bytes).RunQuery(context.Background(), "fetch logs | limit 1"); err != nil {
				t.Fatalf("RunQuery: %v", err)
			}
			if len(g.seen) == 0 {
				t.Fatal("no request reached the server")
			}
			got, present := g.seen[0]["maxResultBytes"]
			if tc.want == nil {
				if present {
					t.Errorf("maxResultBytes = %v sent with the flag unset; the request must stay as before", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("maxResultBytes = %v, want %v", got, tc.want)
			}
			// The existing caps still ride along.
			if g.seen[0]["maxResultRecords"] != float64(inventoryMaxResultRecords) || g.seen[0]["defaultScanLimitGbytes"] != float64(25) {
				t.Errorf("record or scan cap lost: %v", g.seen[0])
			}
		})
	}
}

// A probe Grail cut at the byte limit is truncated with the result-limit
// cause, so discovery reads a probe with no matching row as unknown rather
// than absent.
func TestInventoryRunnerByteCutIsTruncated(t *testing.T) {
	body := `{"state":"SUCCEEDED","result":{"records":[{"n":1}],"metadata":{"grail":{"queryId":"q",
	  "notifications":[{"severity":"WARNING","notificationType":"RESULT_LIMIT_BYTES",
	    "message":"Your result has been limited because the maximum bytes (1000) were reached."}]}}}}`
	res, err := newGrailRecorder(t, body).runner(t, 1000).RunQuery(context.Background(), "fetch logs")
	if err != nil {
		t.Fatalf("RunQuery: %v", err)
	}
	if !res.Truncated || res.TruncationCause != inventory.TruncationResultLimit {
		t.Errorf("byte cut: truncated=%v cause=%q, want true/%q", res.Truncated, res.TruncationCause, inventory.TruncationResultLimit)
	}
}

// Both discovery commands take the flag, and a negative value is refused:
// dtctl sends only a positive limit, so -1 would silently mean "no limit".
func TestInventoryMaxResultBytesFlag(t *testing.T) {
	for _, c := range []*cobra.Command{newInventoryCmd(), newInventoryArrivalsCmd()} {
		if c.Flags().Lookup("max-result-bytes") == nil {
			t.Errorf("%s has no --max-result-bytes", c.Name())
		}
	}
	c := newInventoryCmd()
	if err := c.Flags().Set("max-result-bytes", "-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := inventoryMaxResultBytes(c); err == nil {
		t.Error("--max-result-bytes -1 accepted; it must be refused, not dropped")
	}
	c = newInventoryCmd()
	if err := c.Flags().Set("max-result-bytes", "5000000"); err != nil {
		t.Fatal(err)
	}
	if got, err := inventoryMaxResultBytes(c); err != nil || got != 5000000 {
		t.Errorf("inventoryMaxResultBytes = %d, %v; want 5000000, nil", got, err)
	}
}
