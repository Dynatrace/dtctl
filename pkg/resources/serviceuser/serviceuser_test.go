package serviceuser

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	sdksu "github.com/Dynatrace/dtctl/sdk/api/serviceuser"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

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

func serviceUserBasePath(accountUUID string) string {
	return fmt.Sprintf("/iam/v1/accounts/%s/service-users", accountUUID)
}

func syntheticSDKUser() sdksu.ServiceUser {
	return sdksu.ServiceUser{
		UID: "00000000-0000-4000-8000-000000000101", Email: "automation-alpha@example.invalid",
		Name: "automation-alpha", Surname: "ServiceUser", Description: "Synthetic integration identity",
		CreatedAt: "2026-03-01T12:00:00Z", GroupUUID: "00000000-0000-4000-8000-000000000201",
	}
}

func TestFromSDKPreservesIdentityMetadata(t *testing.T) {
	user := syntheticSDKUser()
	got := fromSDK(&user)
	want := ServiceUser{
		UID: "00000000-0000-4000-8000-000000000101", Email: "automation-alpha@example.invalid",
		Name: "automation-alpha", Surname: "ServiceUser", Description: "Synthetic integration identity",
		CreatedAt: "2026-03-01T12:00:00Z", GroupUUID: "00000000-0000-4000-8000-000000000201",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fromSDK() = %#v, want %#v", got, want)
	}
	if _, exists := reflect.TypeOf(got).FieldByName("Token"); exists {
		t.Fatal("ServiceUser output model must not contain a token or secret field")
	}
	if _, exists := reflect.TypeOf(got).FieldByName("Secret"); exists {
		t.Fatal("ServiceUser output model must not contain a token or secret field")
	}
}

func TestListDelegatesAndConverts(t *testing.T) {
	const accountUUID = "00000000-0000-4000-8000-000000000001"
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(accountUUID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sdksu.ServiceUserListResponse{Results: []sdksu.ServiceUser{syntheticSDKUser()}})
	})

	handler := NewHandler(newTestClient(t, mux), accountUUID)
	users, err := handler.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(users) != 1 || users[0].Email != "automation-alpha@example.invalid" {
		t.Errorf("unexpected users: %#v", users)
	}
}

func TestCreateDelegatesAndConverts(t *testing.T) {
	const accountUUID = "00000000-0000-4000-8000-000000000001"
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(accountUUID), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(syntheticSDKUser())
	})

	handler := NewHandler(newTestClient(t, mux), accountUUID)
	user, err := handler.Create(ServiceUserCreate{Name: "automation-alpha", Description: "Synthetic integration identity"})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if user.UID != "00000000-0000-4000-8000-000000000101" || user.Description == "" {
		t.Errorf("unexpected user: %#v", user)
	}
}

func TestDeleteDelegatesByUUID(t *testing.T) {
	const accountUUID = "00000000-0000-4000-8000-000000000001"
	const userUUID = "00000000-0000-4000-8000-000000000101"
	mux := http.NewServeMux()
	mux.HandleFunc(serviceUserBasePath(accountUUID)+"/"+userUUID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	handler := NewHandler(newTestClient(t, mux), accountUUID)
	if err := handler.Delete(userUUID); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}
