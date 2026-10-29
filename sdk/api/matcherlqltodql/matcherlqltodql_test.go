package matcherlqltodql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func newTestClient(t *testing.T, handler http.Handler) *httpclient.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return c
}

func TestTranslate_Success(t *testing.T) {
	const dql = `matchesValue(log.source, "snmptraps") and matchesValue(snmp.trap_oid, "F5-BIGIP-COMMON-MIB")`

	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req["query"] == "" {
			t.Error("request body missing query field")
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]string{"query": dql})
		_, _ = w.Write(body)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Translate(context.Background(), `log.source="snmptraps" AND snmp.trap_oid="F5-BIGIP-COMMON-MIB"`)
	if err != nil {
		t.Fatalf("Translate() error: %v", err)
	}
	if result.Query != dql {
		t.Errorf("Query = %q, want %q", result.Query, dql)
	}
}

func TestTranslate_ForwardsLQLInRequestBody(t *testing.T) {
	const lql = `log.source="test" AND level="ERROR"`

	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got := req["query"]; got != lql {
			t.Errorf("body query = %q, want %q", got, lql)
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]string{"query": `matchesValue(log.source, "test") and matchesValue(loglevel, "ERROR")`})
		_, _ = w.Write(body)
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Translate(context.Background(), lql); err != nil {
		t.Fatalf("Translate() error: %v", err)
	}
}

func TestTranslate_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"internal error"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Translate(context.Background(), `log.source="test"`); err == nil {
		t.Fatal("Translate() expected error for 500")
	}
}

func TestTranslate_BadRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"invalid LQL expression"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Translate(context.Background(), `INVALID @@@ LQL`); err == nil {
		t.Fatal("Translate() expected error for 400")
	}
}

func TestTranslate_InvalidJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Translate(context.Background(), `log.source="test"`); err == nil {
		t.Fatal("Translate() expected parse error for malformed body")
	}
}
