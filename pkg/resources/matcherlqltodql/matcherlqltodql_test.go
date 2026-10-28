package matcherlqltodql

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func newTestHandler(t *testing.T, fn http.HandlerFunc) (*Handler, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(fn)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		srv.Close()
		t.Fatalf("client.NewForTesting: %v", err)
	}
	return NewHandler(c), srv
}

func TestTranslate_Success(t *testing.T) {
	const dql = `matchesValue(log.source, "snmptraps") and matchesValue(snmp.trap_oid, "F5-BIGIP-COMMON-MIB")`

	h, srv := newTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]string{"query": dql})
		_, _ = w.Write(body)
	})
	defer srv.Close()

	result, err := h.Translate(`log.source="snmptraps" AND snmp.trap_oid="F5-BIGIP-COMMON-MIB"`)
	if err != nil {
		t.Fatalf("Translate() error: %v", err)
	}
	if result.Query != dql {
		t.Errorf("Query = %q, want %q", result.Query, dql)
	}
}

func TestTranslate_APIError(t *testing.T) {
	h, srv := newTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"invalid LQL expression"}}`))
	})
	defer srv.Close()

	if _, err := h.Translate(`INVALID`); err == nil {
		t.Fatal("Translate() expected error for 400 response")
	}
}
