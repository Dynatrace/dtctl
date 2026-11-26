package serviceuser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const testAccountUUID = "00000000-0000-4000-8000-000000000001"

func newTestClient(t *testing.T, handler http.Handler) *httpclient.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := httpclient.New(server.URL, httpclient.WithToken("dt0c01.synthetic"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return client
}

func serviceUserBasePath() string {
	return fmt.Sprintf("/iam/v1/accounts/%s/service-users", testAccountUUID)
}

func TestListPaginatesWithoutCombiningPageParameters(t *testing.T) {
	var requests int
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(), func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		query := r.URL.Query()
		if query.Get("page-size") != "" && query.Get("page-key") != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		switch requests {
		case 1:
			if got := query.Get("page-size"); got != "100" {
				t.Errorf("first page-size = %q, want 100", got)
			}
			if got := query.Get("page-key"); got != "" {
				t.Errorf("first page-key = %q, want empty", got)
			}
			json.NewEncoder(w).Encode(ServiceUserListResponse{
				Results:     []ServiceUser{{UID: "00000000-0000-4000-8000-000000000101", Name: "automation-alpha"}},
				NextPageKey: "next-page",
				TotalCount:  2,
			})
		case 2:
			if got := query.Get("page-size"); got != "" {
				t.Errorf("second page-size = %q, want empty", got)
			}
			if got := query.Get("page-key"); got != "next-page" {
				t.Errorf("second page-key = %q, want next-page", got)
			}
			json.NewEncoder(w).Encode(ServiceUserListResponse{
				Results:    []ServiceUser{{UID: "00000000-0000-4000-8000-000000000102", Name: "automation-beta"}},
				TotalCount: 2,
			})
		default:
			t.Errorf("unexpected request %d", requests)
		}
	})

	handler := NewHandler(newTestClient(t, mux), testAccountUUID)
	users, err := handler.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if got := []string{users[0].Name, users[1].Name}; !reflect.DeepEqual(got, []string{"automation-alpha", "automation-beta"}) {
		t.Errorf("names = %v", got)
	}
}

func TestCreateSendsPayloadAndDecodesObject(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		want := map[string]any{"name": "automation-alpha", "description": "Synthetic integration identity"}
		if !reflect.DeepEqual(payload, want) {
			t.Errorf("payload = %#v, want %#v", payload, want)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ServiceUser{
			UID: "00000000-0000-4000-8000-000000000101", Email: "automation-alpha@example.invalid",
			Name: "automation-alpha", Surname: "ServiceUser", Description: "Synthetic integration identity",
			CreatedAt: "2026-03-01T12:00:00Z", GroupUUID: "00000000-0000-4000-8000-000000000201",
		})
	})

	handler := NewHandler(newTestClient(t, mux), testAccountUUID)
	user, err := handler.Create(context.Background(), ServiceUserCreate{
		Name: "automation-alpha", Description: "Synthetic integration identity",
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if user.Email != "automation-alpha@example.invalid" || user.UID == "" {
		t.Errorf("unexpected result: %#v", user)
	}
}

func TestCreateOmitsEmptyDescription(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(), func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, exists := payload["description"]; exists {
			t.Errorf("description should be omitted: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ServiceUser{Name: "automation-alpha"})
	})

	handler := NewHandler(newTestClient(t, mux), testAccountUUID)
	if _, err := handler.Create(context.Background(), ServiceUserCreate{Name: "automation-alpha"}); err != nil {
		t.Fatalf("Create() error: %v", err)
	}
}

func TestCreateToleratesOneElementArray(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ServiceUser{{UID: "00000000-0000-4000-8000-000000000101", Name: "automation-alpha"}})
	})

	handler := NewHandler(newTestClient(t, mux), testAccountUUID)
	user, err := handler.Create(context.Background(), ServiceUserCreate{Name: "automation-alpha"})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if user.Name != "automation-alpha" {
		t.Errorf("Name = %q", user.Name)
	}
}

func TestCreateRejectsMalformedArrayResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	})

	handler := NewHandler(newTestClient(t, mux), testAccountUUID)
	_, err := handler.Create(context.Background(), ServiceUserCreate{Name: "automation-alpha"})
	if err == nil {
		t.Fatal("expected malformed response error")
	}
}

func TestCreateRejectsNullResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `null`)
	})

	handler := NewHandler(newTestClient(t, mux), testAccountUUID)
	_, err := handler.Create(context.Background(), ServiceUserCreate{Name: "automation-alpha"})
	if err == nil {
		t.Fatal("expected null response error")
	}
	if !strings.Contains(err.Error(), "expected service-user object, got null") {
		t.Errorf("error = %v", err)
	}
}

func TestDeleteUsesExactMethodAndPath(t *testing.T) {
	const userUUID = "00000000-0000-4000-8000-000000000101"
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath()+"/"+userUUID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	handler := NewHandler(newTestClient(t, mux), testAccountUUID)
	if err := handler.Delete(context.Background(), userUUID); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

func TestTypedHTTPErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
		call   func(*Handler) error
	}{
		{name: "list unauthorized", status: http.StatusUnauthorized, want: httpclient.ErrUnauthorized, call: func(h *Handler) error {
			_, err := h.List(context.Background())
			return err
		}},
		{name: "create forbidden", status: http.StatusForbidden, want: httpclient.ErrForbidden, call: func(h *Handler) error {
			_, err := h.Create(context.Background(), ServiceUserCreate{Name: "automation-alpha"})
			return err
		}},
		{name: "delete not found", status: http.StatusNotFound, want: httpclient.ErrNotFound, call: func(h *Handler) error {
			return h.Delete(context.Background(), "00000000-0000-4000-8000-000000000404")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				fmt.Fprint(w, `{"error":{"message":"synthetic error"}}`)
			}))
			t.Cleanup(server.Close)
			client, err := httpclient.New(server.URL, httpclient.WithToken("dt0c01.synthetic"))
			if err != nil {
				t.Fatalf("httpclient.New: %v", err)
			}

			err = tt.call(NewHandler(client, testAccountUUID))
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want errors.Is(%v)", err, tt.want)
			}
			var apiErr *httpclient.APIError
			if !errors.As(err, &apiErr) {
				t.Errorf("error = %v, want *httpclient.APIError", err)
			}
		})
	}
}

func TestListRejectsNullResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `null`)
	})

	handler := NewHandler(newTestClient(t, mux), testAccountUUID)
	_, err := handler.List(context.Background())
	if err == nil {
		t.Fatal("expected null list response error")
	}
	if !strings.Contains(err.Error(), "expected list response object, got null") {
		t.Errorf("error = %v", err)
	}
}

func TestCreateRejectsNullArrayElement(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[null]`)
	})

	handler := NewHandler(newTestClient(t, mux), testAccountUUID)
	_, err := handler.Create(context.Background(), ServiceUserCreate{Name: "automation-alpha"})
	if err == nil {
		t.Fatal("expected null array element error")
	}
	if !strings.Contains(err.Error(), "expected service-user object, got null") {
		t.Errorf("error = %v", err)
	}
}
