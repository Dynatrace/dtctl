package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/pkg/resources/anomalydetector"
)

func newAnomalyDetectorHandler(t *testing.T, fn http.HandlerFunc) (*anomalydetector.Handler, func()) {
	t.Helper()
	server := httptest.NewServer(fn)
	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		server.Close()
		t.Fatalf("client.NewForTesting() error = %v", err)
	}
	c.HTTP().SetRetryCount(0)
	return anomalydetector.NewHandler(c), server.Close
}

// resolveAnomalyDetector falls back from an ID lookup to a title match, and
// used to report "not found" for whatever the fallback returned. On a 403 that
// is wrong twice over: the detector may well exist, and the permission
// diagnostics the handler attached are discarded (#476).
func TestResolveAnomalyDetectorPropagatesForbidden(t *testing.T) {
	h, closeFn := newAnomalyDetectorHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"No permission to access settings object."}}`))
	})
	defer closeFn()

	_, err := resolveAnomalyDetector(h, "obj-1")
	if err == nil {
		t.Fatal("expected an error for 403")
	}

	if strings.Contains(err.Error(), "not found") {
		t.Errorf("403 reported as a missing detector: %q", err.Error())
	}

	var diagErr *diagnostic.Error
	if !errors.As(err, &diagErr) {
		t.Fatalf("error = %T, want *diagnostic.Error", err)
	}
	if got := diagErr.ExitCode(); got != client.ExitPermissionError {
		t.Errorf("ExitCode() = %d, want %d", got, client.ExitPermissionError)
	}
	if !strings.Contains(err.Error(), "No permission to access settings object.") {
		t.Errorf("error dropped the server explanation: %q", err.Error())
	}
}

// A genuine 404 must still read as a missing detector.
func TestResolveAnomalyDetectorReportsNotFound(t *testing.T) {
	h, closeFn := newAnomalyDetectorHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	})
	defer closeFn()

	_, err := resolveAnomalyDetector(h, "obj-1")
	if err == nil {
		t.Fatal("expected an error for 404")
	}
	if !strings.Contains(err.Error(), `anomaly detector "obj-1" not found`) {
		t.Errorf("unexpected message: %q", err.Error())
	}
}
