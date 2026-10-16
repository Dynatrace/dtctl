package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/resources/extension"
)

// activationRecorder serves the endpoints resolveAndActivate touches and records
// the ordered sequence of mutating calls, so tests can assert both *how many*
// times monitoring configurations were migrated and *whether* that happened
// before or after activation.
type activationRecorder struct {
	mu sync.Mutex
	// calls records one entry per request, e.g. "GET env-config",
	// "activate <version>", "PUT config/<id>".
	calls []string
	// putBodies records the decoded body of each monitoring-configuration PUT.
	putBodies []map[string]any

	// activeVersion is returned by GET environment-configuration.
	activeVersion string
	// activeVersionStatus, when non-zero, is returned by GET
	// environment-configuration instead of a body.
	activeVersionStatus int
	// stickyStatus keeps activeVersionStatus in place after activation, so a
	// persistently failing active-version lookup stays failing. Without it the
	// mock would "heal" on activation and the post-activation lookup would
	// succeed, hiding the case where the lookup fails for the whole run.
	stickyStatus bool
}

func (r *activationRecorder) record(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *activationRecorder) snapshot() ([]string, []map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...), append([]map[string]any(nil), r.putBodies...)
}

func (r *activationRecorder) countCalls(prefix string) int {
	calls, _ := r.snapshot()
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

const recorderExtension = "com.example.recorder"

func (r *activationRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	base := "/platform/extensions/v2/extensions/" + recorderExtension

	mux := http.NewServeMux()

	mux.HandleFunc(base+"/environment-configuration", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			r.record("GET env-config")
			if r.activeVersionStatus != 0 {
				w.WriteHeader(r.activeVersionStatus)
				fmt.Fprintf(w, `{"error":{"code":%d,"message":"boom"}}`, r.activeVersionStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"version": r.activeVersion})
		case http.MethodPut:
			var body map[string]string
			_ = json.NewDecoder(req.Body).Decode(&body)
			r.record("activate " + body["version"])
			// Activation flips the active version, mirroring the real API:
			// a PUT creates the environment configuration when there is none.
			r.mu.Lock()
			r.activeVersion = body["version"]
			if !r.stickyStatus {
				r.activeVersionStatus = 0
			}
			r.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"version": body["version"]})
		default:
			r.record("UNEXPECTED " + req.Method + " env-config")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc(base+"/monitoring-configurations", func(w http.ResponseWriter, req *http.Request) {
		r.record("GET configs")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"totalCount": 1,
			"items": []map[string]any{{
				"objectId": "cfg-1",
				"scope":    "HOST-1",
				"value":    map[string]any{"version": "0.0.0", "description": "keep me"},
			}},
		})
	})

	mux.HandleFunc(base+"/monitoring-configurations/cfg-1", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.putBodies = append(r.putBodies, body)
		r.mu.Unlock()
		r.record("PUT config/cfg-1")
		_ = json.NewEncoder(w).Encode(map[string]any{"objectId": "cfg-1"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newRecorderHandler(t *testing.T, srv *httptest.Server) *extension.Handler {
	t.Helper()
	c, err := client.New(srv.URL, "dt0c01.ST.test-token-value.test-secret")
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return extension.NewHandler(c)
}

// indexOf returns the position of the first call with the given prefix, or -1.
func indexOf(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

// On an upgrade the monitoring configurations must be migrated exactly once, and
// only after the new version is active.
func TestResolveAndActivate_UpgradeMigratesConfigsAfterActivation(t *testing.T) {
	rec := &activationRecorder{activeVersion: "1.0.0"}
	h := newRecorderHandler(t, rec.server(t))

	got, err := resolveAndActivate(context.Background(), h, recorderExtension, "2.0.0", false, false, true)
	if err != nil {
		t.Fatalf("resolveAndActivate: %v", err)
	}
	if got != "2.0.0" {
		t.Errorf("activated version = %q, want 2.0.0", got)
	}

	calls, putBodies := rec.snapshot()
	if n := rec.countCalls("PUT config/"); n != 1 {
		t.Errorf("monitoring config migrated %d time(s), want exactly 1; calls: %v", n, calls)
	}
	if a, p := indexOf(calls, "activate"), indexOf(calls, "PUT config/"); a == -1 || p == -1 || a > p {
		t.Errorf("on an upgrade configs must be migrated after activation; calls: %v", calls)
	}

	// The migrated config must carry the new version and keep its other fields.
	if len(putBodies) != 1 {
		t.Fatalf("expected 1 PUT body, got %d", len(putBodies))
	}
	value, ok := putBodies[0]["value"].(map[string]any)
	if !ok {
		t.Fatalf("PUT body value = %T, want map", putBodies[0]["value"])
	}
	if value["version"] != "2.0.0" {
		t.Errorf("migrated config version = %v, want 2.0.0", value["version"])
	}
	if value["description"] != "keep me" {
		t.Errorf("migration dropped unrelated config fields: %v", value)
	}
	if putBodies[0]["scope"] != "HOST-1" {
		t.Errorf("migrated config scope = %v, want HOST-1", putBodies[0]["scope"])
	}
}

// On a downgrade the configurations must be migrated before activation (the API
// rejects activating an older version while a config references a newer one) —
// and still only once. Re-reading the active version *after* activation always
// reports the just-activated version, which previously made the post-activation
// migration fire on the downgrade path too, PUTting every config twice.
func TestResolveAndActivate_DowngradeMigratesConfigsOnceBeforeActivation(t *testing.T) {
	rec := &activationRecorder{activeVersion: "2.0.0"}
	h := newRecorderHandler(t, rec.server(t))

	if _, err := resolveAndActivate(context.Background(), h, recorderExtension, "1.0.0", false, false, true); err != nil {
		t.Fatalf("resolveAndActivate: %v", err)
	}

	calls, _ := rec.snapshot()
	if n := rec.countCalls("PUT config/"); n != 1 {
		t.Errorf("monitoring config migrated %d time(s), want exactly 1; calls: %v", n, calls)
	}
	if p, a := indexOf(calls, "PUT config/"), indexOf(calls, "activate"); p == -1 || a == -1 || p > a {
		t.Errorf("on a downgrade configs must be migrated before activation; calls: %v", calls)
	}
}

// If the active version cannot be determined, --with-configurations must still
// migrate the configurations rather than silently doing nothing.
func TestResolveAndActivate_ActiveVersionLookupFailureStillMigratesConfigs(t *testing.T) {
	rec := &activationRecorder{activeVersionStatus: http.StatusForbidden, stickyStatus: true}
	h := newRecorderHandler(t, rec.server(t))

	if _, err := resolveAndActivate(context.Background(), h, recorderExtension, "2.0.0", false, false, true); err != nil {
		t.Fatalf("resolveAndActivate: %v", err)
	}

	calls, _ := rec.snapshot()
	if n := rec.countCalls("PUT config/"); n != 1 {
		t.Errorf("--with-configurations silently skipped the migration; PUTs = %d, calls: %v", n, calls)
	}
	if indexOf(calls, "activate") == -1 {
		t.Errorf("activation did not happen; calls: %v", calls)
	}
}

// A first activation has no environment configuration yet, so GET returns 404 and
// GetActiveVersion reports an empty active version. Activation must still succeed
// (the endpoint creates the configuration), and an empty current version must not
// be mistaken for a downgrade.
func TestResolveAndActivate_FirstActivationSucceeds(t *testing.T) {
	rec := &activationRecorder{activeVersionStatus: http.StatusNotFound}
	h := newRecorderHandler(t, rec.server(t))

	if _, err := resolveAndActivate(context.Background(), h, recorderExtension, "1.0.0", false, false, true); err != nil {
		t.Fatalf("first activation failed: %v", err)
	}

	calls, _ := rec.snapshot()
	if indexOf(calls, "activate 1.0.0") == -1 {
		t.Errorf("expected an activation call; calls: %v", calls)
	}
	if indexOf(calls, "UNEXPECTED") != -1 {
		t.Errorf("activation used an unexpected HTTP method; calls: %v", calls)
	}
	// No active version yet is not a downgrade: configs migrate after activation.
	if a, p := indexOf(calls, "activate"), indexOf(calls, "PUT config/"); a == -1 || p == -1 || a > p {
		t.Errorf("first activation must migrate configs after activating; calls: %v", calls)
	}
}

// Without --with-configurations nothing touches the monitoring configurations.
func TestResolveAndActivate_WithoutFlagLeavesConfigsAlone(t *testing.T) {
	rec := &activationRecorder{activeVersion: "1.0.0"}
	h := newRecorderHandler(t, rec.server(t))

	if _, err := resolveAndActivate(context.Background(), h, recorderExtension, "2.0.0", false, false, false); err != nil {
		t.Fatalf("resolveAndActivate: %v", err)
	}
	if n := rec.countCalls("PUT config/"); n != 0 {
		t.Errorf("configs were migrated without --with-configurations (%d PUTs)", n)
	}
	if n := rec.countCalls("GET configs"); n != 0 {
		t.Errorf("configs were listed without --with-configurations (%d GETs)", n)
	}
}

// resolveAndActivate must not swallow an activation failure.
func TestResolveAndActivate_ActivationErrorIsReturned(t *testing.T) {
	mux := http.NewServeMux()
	base := "/platform/extensions/v2/extensions/" + recorderExtension
	mux.HandleFunc(base+"/environment-configuration", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.0.0"})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":400,"message":"version not uploaded"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := resolveAndActivate(context.Background(), newRecorderHandler(t, srv), recorderExtension, "9.9.9", false, false, false)
	if err == nil {
		t.Fatal("expected an error when activation is rejected, got nil")
	}
	if !strings.Contains(err.Error(), "9.9.9") {
		t.Errorf("error should name the version it failed to activate: %v", err)
	}
}

// The bulk command must reject an invalid flag combination identically with and
// without --dry-run, and before it authenticates or calls the API.
func TestUpdateExtensions_FlagValidation(t *testing.T) {
	tests := []struct {
		name      string
		all       bool
		latest    bool
		hubLatest bool
		dry       bool
		wantErr   string
	}{
		{name: "--all is required", latest: true, wantErr: "--all is required"},
		{name: "--all is required even in dry-run", latest: true, dry: true, wantErr: "--all is required"},
		{name: "a version source is required", all: true, wantErr: "one of --latest or --hub-latest is required"},
		{
			name:    "a version source is required in dry-run too",
			all:     true,
			dry:     true,
			wantErr: "one of --latest or --hub-latest is required",
		},
		{
			name:      "--latest and --hub-latest conflict",
			all:       true,
			latest:    true,
			hubLatest: true,
			wantErr:   "mutually exclusive",
		},
		{
			name:      "--latest and --hub-latest conflict in dry-run too",
			all:       true,
			latest:    true,
			hubLatest: true,
			dry:       true,
			wantErr:   "mutually exclusive",
		},
	}

	origDryRun := dryRun(context.Background())
	t.Cleanup(func() {
		gFlags.dryRun = origDryRun
		_ = updateExtensionsCmd.Flags().Set("all", "false")
		_ = updateExtensionsCmd.Flags().Set("latest", "false")
		_ = updateExtensionsCmd.Flags().Set("hub-latest", "false")
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gFlags.dryRun = tt.dry
			for flag, val := range map[string]bool{"all": tt.all, "latest": tt.latest, "hub-latest": tt.hubLatest} {
				if err := updateExtensionsCmd.Flags().Set(flag, fmt.Sprintf("%t", val)); err != nil {
					t.Fatalf("set --%s: %v", flag, err)
				}
			}

			err := updateExtensionsCmd.RunE(updateExtensionsCmd, nil)
			if err == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// The single-extension command requires exactly one version source.
func TestUpdateExtension_FlagValidation(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		latest    bool
		hubLatest bool
		wantErr   string
	}{
		{name: "no version source", wantErr: "one of --version, --latest, or --hub-latest is required"},
		{name: "--version with --latest", version: "1.0.0", latest: true, wantErr: "mutually exclusive"},
		{name: "--version with --hub-latest", version: "1.0.0", hubLatest: true, wantErr: "mutually exclusive"},
		{name: "--latest with --hub-latest", latest: true, hubLatest: true, wantErr: "mutually exclusive"},
		{name: "all three", version: "1.0.0", latest: true, hubLatest: true, wantErr: "mutually exclusive"},
	}

	origDryRun := dryRun(context.Background())
	t.Cleanup(func() {
		gFlags.dryRun = origDryRun
		clearFlag(updateExtensionCmd, "version")
		_ = updateExtensionCmd.Flags().Set("latest", "false")
		_ = updateExtensionCmd.Flags().Set("hub-latest", "false")
	})
	// Dry-run so a valid combination would not reach the network; the cases
	// here must all fail validation before that matters.
	gFlags.dryRun = true

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearFlag(updateExtensionCmd, "version")
			if tt.version != "" {
				if err := updateExtensionCmd.Flags().Set("version", tt.version); err != nil {
					t.Fatalf("set --version: %v", err)
				}
			}
			for flag, val := range map[string]bool{"latest": tt.latest, "hub-latest": tt.hubLatest} {
				if err := updateExtensionCmd.Flags().Set(flag, fmt.Sprintf("%t", val)); err != nil {
					t.Fatalf("set --%s: %v", flag, err)
				}
			}

			err := runUpdateOneExtension(updateExtensionCmd, "com.example.ext")
			if err == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// Every monitoring configuration must be migrated, not just the first page.
// The SDK only follows page keys when a page size is supplied, so passing
// chunkSize 0 here would leave later pages referencing the old version.
func TestRefreshMonitoringConfigurations_MigratesAllPages(t *testing.T) {
	var (
		mu       sync.Mutex
		migrated []string
	)
	base := "/platform/extensions/v2/extensions/" + recorderExtension
	mux := http.NewServeMux()

	mux.HandleFunc(base+"/monitoring-configurations", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		// Simulate API constraint: page-size must not be combined with
		// next-page-key.
		if q.Get("page-size") != "" && q.Get("next-page-key") != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
			return
		}
		if q.Get("next-page-key") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"totalCount":  2,
				"nextPageKey": "page-2",
				"items": []map[string]any{{
					"objectId": "cfg-page1",
					"value":    map[string]any{"version": "1.0.0"},
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"totalCount": 2,
			"items": []map[string]any{{
				"objectId": "cfg-page2",
				"value":    map[string]any{"version": "1.0.0"},
			}},
		})
	})

	for _, id := range []string{"cfg-page1", "cfg-page2"} {
		mux.HandleFunc(base+"/monitoring-configurations/"+id, func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodPut {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			mu.Lock()
			migrated = append(migrated, req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:])
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"objectId": id})
		})
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	if err := refreshMonitoringConfigurations(newRecorderHandler(t, srv), recorderExtension, "2.0.0"); err != nil {
		t.Fatalf("refreshMonitoringConfigurations: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(migrated) != 2 {
		t.Fatalf("migrated %v, want both cfg-page1 and cfg-page2", migrated)
	}
}
