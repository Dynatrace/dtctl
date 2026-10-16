package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeCapturingServer fronts the read-only cloud mock: GETs are proxied to it,
// and the body of every write is recorded and answered with a minimal success
// object so the command can finish.
type writeCapturingServer struct {
	*httptest.Server

	mu     sync.Mutex
	bodies []map[string]any
}

func (s *writeCapturingServer) writes() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.bodies...)
}

func newWriteCapturingServer(t *testing.T) *writeCapturingServer {
	t.Helper()

	backend := newCloudMockServer(t)
	target, err := url.Parse(backend.URL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)

	s := &writeCapturingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			proxy.ServeHTTP(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		writeJSON(w, map[string]any{"objectId": mockGCPConfigID})
	}))
	t.Cleanup(s.Close)
	return s
}

func runGCPMonitoringWrite(t *testing.T, run func() error) map[string]any {
	t.Helper()

	srv := newWriteCapturingServer(t)
	setupPlatformCmdTest(t, srv.Server, "json")
	origDryRun := dryRun(context.Background())
	t.Cleanup(func() {
		gFlags.dryRun = origDryRun
		resetCloudFlagVars()
	})
	gFlags.dryRun = false
	resetCloudFlagVars()

	var runErr error
	out := capturePlatformStdout(t, func() { runErr = run() })
	require.NoError(t, runErr, "output:\n%s", out)

	writes := srv.writes()
	require.Len(t, writes, 1)
	return writes[0]
}

func gcpLocationFiltering(t *testing.T, body map[string]any) (any, bool) {
	t.Helper()
	value, ok := body["value"].(map[string]any)
	require.True(t, ok, "payload has no value object: %v", body)
	googleCloud, ok := value["googleCloud"].(map[string]any)
	require.True(t, ok, "payload has no googleCloud object: %v", value)
	locations, present := googleCloud["locationFiltering"]
	return locations, present
}

// Regression test for #573: an explicit list of every schema location makes
// the GCP Smartscape poller build a Cloud Asset query GCP rejects ("Query has
// too many alternations"), so no resources are ever discovered. Without
// --locationFiltering the created config must not filter by location at all.
func TestCreateGCPMonitoringWithoutLocationFilteringDoesNotFilter(t *testing.T) {
	body := runGCPMonitoringWrite(t, func() error {
		_ = createGCPMonitoringConfigCmd.Flags().Set("name", "new-gcp-monitoring")
		_ = createGCPMonitoringConfigCmd.Flags().Set("credentials", mockGCPConnectionName)
		return createGCPMonitoringConfigCmd.RunE(createGCPMonitoringConfigCmd, nil)
	})

	// No filter is sent by omitting the field, like the other empty filter
	// lists; the backend reads an absent list as "every location".
	locations, present := gcpLocationFiltering(t, body)
	require.False(t, present, "create without --locationFiltering must not filter by location, got %v", locations)
}

func TestCreateGCPMonitoringWithLocationFilteringSendsThem(t *testing.T) {
	body := runGCPMonitoringWrite(t, func() error {
		_ = createGCPMonitoringConfigCmd.Flags().Set("name", "new-gcp-monitoring")
		_ = createGCPMonitoringConfigCmd.Flags().Set("credentials", mockGCPConnectionName)
		_ = createGCPMonitoringConfigCmd.Flags().Set("locationFiltering", "us-central1, europe-west1")
		return createGCPMonitoringConfigCmd.RunE(createGCPMonitoringConfigCmd, nil)
	})

	locations, present := gcpLocationFiltering(t, body)
	require.True(t, present)
	require.Equal(t, []any{"us-central1", "europe-west1"}, locations)
}

// A config created before the fix carries every schema location; update has
// to be able to take it back to "no filter".
func TestUpdateGCPMonitoringLocationFilteringAllClearsFilter(t *testing.T) {
	body := runGCPMonitoringWrite(t, func() error {
		_ = updateGCPMonitoringConfigCmd.Flags().Set("name", mockGCPConfigName)
		_ = updateGCPMonitoringConfigCmd.Flags().Set("locationFiltering", "all")
		return updateGCPMonitoringConfigCmd.RunE(updateGCPMonitoringConfigCmd, nil)
	})

	// No filter is sent by omitting the field, like the other empty filter
	// lists; the backend reads an absent list as "every location".
	locations, present := gcpLocationFiltering(t, body)
	require.False(t, present, "--locationFiltering all must clear the location filter, got %v", locations)
}

func TestUpdateGCPMonitoringLocationFilteringAllCannotBeCombined(t *testing.T) {
	setupPlatformCmdTest(t, newCloudMockServer(t).Server, "json")
	t.Cleanup(resetCloudFlagVars)
	resetCloudFlagVars()
	_ = updateGCPMonitoringConfigCmd.Flags().Set("name", mockGCPConfigName)
	_ = updateGCPMonitoringConfigCmd.Flags().Set("locationFiltering", "all,us-central1")

	err := updateGCPMonitoringConfigCmd.RunE(updateGCPMonitoringConfigCmd, nil)
	require.ErrorContains(t, err, `"all"`)
}
