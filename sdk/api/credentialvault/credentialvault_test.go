package credentialvault

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const credPath = "/platform/credential-vault/v1/entries"

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

func TestListPaginatesAndSendsFiltersOnFirstPageOnly(t *testing.T) {
	var calls []string
	mux := http.NewServeMux()
	mux.HandleFunc(credPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		calls = append(calls, r.URL.RawQuery)

		// Simulate the API constraint: nextPageKey must not be combined with
		// any other query parameter (the page token embeds them all).
		if q.Get("nextPageKey") != "" {
			for _, p := range []string{"pageSize", "type", "name", "scope", "user"} {
				if q.Get(p) != "" {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
					return
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if q.Get("page-key") == "" {
			if q.Get("type") != TypeCertificate || q.Get("page-size") != "1" {
				t.Errorf("first page query = %q, want type and pageSize", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode(CredentialList{
				Credentials: []Credential{{ID: "CREDENTIALS_VAULT-0000000000000001", Name: "cert-1", Type: TypeCertificate}},
				NextPageKey: "page-2",
				TotalCount:  2,
			})
			return
		}
		json.NewEncoder(w).Encode(CredentialList{
			Credentials: []Credential{{ID: "CREDENTIALS_VAULT-0000000000000002", Name: "cert-2", Type: TypeCertificate}},
			TotalCount:  2,
		})
	})

	h := NewHandler(newTestClient(t, mux))
	got, err := h.List(context.Background(), ListOptions{Type: TypeCertificate, PageSize: 1})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(got.Credentials) != 2 || got.TotalCount != 2 {
		t.Fatalf("got %d credentials (total %d), want 2", len(got.Credentials), got.TotalCount)
	}
	if len(calls) != 2 || calls[1] != "page-key=page-2" {
		t.Errorf("calls = %v, want second call with page-key only", calls)
	}
}

func TestListFiltersPublicCertificatesClientSide(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(credPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("type") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":400,"message":"type: unknown value PUBLIC_CERTIFICATE"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"credentials":[{"id":"a","name":"ca","type":"PUBLIC_CERTIFICATE"},{"id":"b","name":"tok","type":"TOKEN"}],"totalCount":2}`)
	})
	got, err := NewHandler(newTestClient(t, mux)).List(context.Background(), ListOptions{Type: TypePublicCertificate})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(got.Credentials) != 1 || got.Credentials[0].ID != "a" || got.TotalCount != 1 {
		t.Fatalf("got %+v, want only the public certificate", got)
	}
}

func TestListEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(credPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"credentials":[],"totalCount":0}`)
	})
	got, err := NewHandler(newTestClient(t, mux)).List(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if got.Credentials == nil || len(got.Credentials) != 0 {
		t.Errorf("want empty non-nil slice, got %#v", got.Credentials)
	}
}

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(credPath+"/CREDENTIALS_VAULT-0000000000000001", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"CREDENTIALS_VAULT-0000000000000001","name":"ca","type":"PUBLIC_CERTIFICATE","scopes":["EXTENSION"],"ownerAccessOnly":false}`)
	})
	got, err := NewHandler(newTestClient(t, mux)).Get(context.Background(), "CREDENTIALS_VAULT-0000000000000001")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if got.Name != "ca" || len(got.Scopes) != 1 || got.Scopes[0] != ScopeExtension {
		t.Errorf("unexpected credential: %+v", got)
	}
}

func TestGetNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(credPath+"/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
	})
	if _, err := NewHandler(newTestClient(t, mux)).Get(context.Background(), "missing"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCreatePublicCertificate(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc(credPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("bad body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"CREDENTIALS_VAULT-0000000000000003"}`)
	})

	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	empty := ""
	format := CertificateFormatPEM
	id, err := NewHandler(newTestClient(t, mux)).Create(context.Background(), CredentialRequest{
		Name:              "ca",
		Type:              TypePublicCertificate,
		Scopes:            []string{ScopeExtensionAuthentication},
		Certificate:       &pem,
		CertificateFormat: &format,
		Password:          &empty,
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if id != "CREDENTIALS_VAULT-0000000000000003" {
		t.Errorf("id = %q", id)
	}
	// ownerAccessOnly is required by the API and must be sent even when false.
	if v, ok := body["ownerAccessOnly"]; !ok || v != false {
		t.Errorf("ownerAccessOnly = %v (present %v), want false", v, ok)
	}
	if v, ok := body["password"]; !ok || v != "" {
		t.Errorf("password = %v (present %v), want empty string", v, ok)
	}
	if _, ok := body["scope"]; ok {
		t.Error("deprecated `scope` must not be sent")
	}
	if body["certificate"] != pem || body["certificateFormat"] != "PEM" {
		t.Errorf("certificate fields = %v / %v", body["certificate"], body["certificateFormat"])
	}
	for _, k := range []string{"token", "user"} {
		if _, ok := body[k]; ok {
			t.Errorf("unexpected field %q in body", k)
		}
	}
}

func TestCreateMissingID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(credPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{}`)
	})
	_, err := NewHandler(newTestClient(t, mux)).Create(context.Background(), CredentialRequest{Name: "x", Type: TypeToken})
	if err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("want missing-id error, got %v", err)
	}
}

func TestUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(credPath+"/CREDENTIALS_VAULT-0000000000000001", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	token := "secret"
	err := NewHandler(newTestClient(t, mux)).Update(context.Background(), "CREDENTIALS_VAULT-0000000000000001",
		CredentialRequest{Name: "t", Type: TypeToken, Scopes: []string{ScopeExtension}, Token: &token})
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	mux := http.NewServeMux()
	mux.HandleFunc(credPath+"/CREDENTIALS_VAULT-0000000000000001", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		deleted = true
		w.WriteHeader(http.StatusNoContent)
	})
	if err := NewHandler(newTestClient(t, mux)).Delete(context.Background(), "CREDENTIALS_VAULT-0000000000000001"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if !deleted {
		t.Error("delete not called")
	}
}

func TestPathSegmentEscapesID(t *testing.T) {
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	})
	_ = NewHandler(newTestClient(t, mux)).Delete(context.Background(), "a/b#c")
	if !strings.HasSuffix(gotPath, "/entries/a%2Fb%23c") {
		t.Errorf("path = %q, want escaped id", gotPath)
	}
}
