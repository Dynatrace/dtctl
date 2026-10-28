package platformtoken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const testAccountUUID = "test-uuid"

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

func tokenBasePath() string {
	return fmt.Sprintf("/iam/v1/accounts/%s/platform-tokens", testAccountUUID)
}

func TestList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := PlatformTokenListResponse{
			Total: 2,
			Results: []PlatformToken{
				{Name: "ci-token", TokenID: "tok-001", Status: "ACTIVE", Scope: []string{"storage:events:read"}},
				{Name: "dev-token", TokenID: "tok-002", Status: "ACTIVE", Scope: []string{"account-idm-read"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux), testAccountUUID)
	result, err := h.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("got %d tokens, want 2", len(result))
	}
	if result[0].TokenID != "tok-001" {
		t.Errorf("TokenID = %q, want %q", result[0].TokenID, "tok-001")
	}
}

func TestList_Paginated(t *testing.T) {
	call := 0
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		call++
		w.Header().Set("Content-Type", "application/json")
		pageNum, _ := strconv.Atoi(r.URL.Query().Get("pageNumber"))
		if pageNum == 0 {
			json.NewEncoder(w).Encode(PlatformTokenListResponse{
				Total:      2,
				PageSize:   1,
				PageNumber: 0,
				Results:    []PlatformToken{{Name: "token-a", TokenID: "tok-a", Status: "ACTIVE"}},
			})
		} else {
			json.NewEncoder(w).Encode(PlatformTokenListResponse{
				Total:      2,
				PageSize:   1,
				PageNumber: 1,
				Results:    []PlatformToken{{Name: "token-b", TokenID: "tok-b", Status: "ACTIVE"}},
			})
		}
	})

	h := NewHandler(newTestClient(t, mux), testAccountUUID)
	result, err := h.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("got %d tokens after pagination, want 2", len(result))
	}
	if call != 2 {
		t.Errorf("expected 2 HTTP calls, got %d", call)
	}
	if result[0].TokenID != "tok-a" {
		t.Errorf("first token = %q, want %q", result[0].TokenID, "tok-a")
	}
	if result[1].TokenID != "tok-b" {
		t.Errorf("second token = %q, want %q", result[1].TokenID, "tok-b")
	}
}

func TestCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req PlatformTokenCreate
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, `{"error":{"message":"name is required"}}`, http.StatusBadRequest)
			return
		}
		if len(req.Scope) == 0 {
			http.Error(w, `{"error":{"message":"scope is required"}}`, http.StatusBadRequest)
			return
		}
		if req.UserUUID == "" {
			http.Error(w, `{"error":{"message":"userUuid is required"}}`, http.StatusBadRequest)
			return
		}
		resp := PlatformToken{
			Name:           req.Name,
			TokenID:        "tok-new-001",
			Token:          "dt0s16.secrettoken.example",
			AccountUUID:    testAccountUUID,
			Scope:          req.Scope,
			Status:         "ACTIVE",
			ExpirationDate: req.ExpirationDate,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux), testAccountUUID)
	result, err := h.Create(context.Background(), PlatformTokenCreate{
		Name:           "ci-token",
		UserUUID:       "user-uuid-001",
		Scope:          []string{"storage:events:read"},
		Resource:       []string{},
		Tags:           []string{},
		ExpirationDate: "2026-10-01T00:00:00.000Z",
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.TokenID != "tok-new-001" {
		t.Errorf("TokenID = %q, want %q", result.TokenID, "tok-new-001")
	}
	if result.Token != "dt0s16.secrettoken.example" {
		t.Errorf("Token = %q, want secret token", result.Token)
	}
}

func TestRevoke(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath()+"/tok-001", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux), testAccountUUID)
	if err := h.Revoke(context.Background(), "tok-001"); err != nil {
		t.Fatalf("Revoke() error: %v", err)
	}
}

func TestList_Unauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"unauthorized"}}`)
	})

	h := NewHandler(newTestClient(t, mux), testAccountUUID)
	_, err := h.List(context.Background())
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !errors.Is(err, httpclient.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestList_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"forbidden"}}`)
	})

	h := NewHandler(newTestClient(t, mux), testAccountUUID)
	_, err := h.List(context.Background())
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if !errors.Is(err, httpclient.ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestRevoke_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath()+"/missing-id", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"message":"token not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux), testAccountUUID)
	err := h.Revoke(context.Background(), "missing-id")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !errors.Is(err, httpclient.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
