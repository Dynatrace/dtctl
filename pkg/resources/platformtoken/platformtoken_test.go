package platformtoken

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	sdkpt "github.com/Dynatrace/dtctl/sdk/api/platformtoken"
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

func tokenBasePath(accountUUID string) string {
	return fmt.Sprintf("/iam/v1/accounts/%s/platform-tokens", accountUUID)
}

// futureExpiry is an expiration date that is always ahead of the clock the test
// runs on. A literal date here is a time bomb: fromSDK derives EXPIRED from
// time.Now(), so a hard-coded "ACTIVE" token silently becomes an expired one the
// day the date arrives, and the suite fails on a commit that changed nothing.
func futureExpiry() string {
	return time.Now().UTC().AddDate(1, 0, 0).Format(time.RFC3339Nano)
}

func TestFromSDK(t *testing.T) {
	expiry := futureExpiry()
	sdk := sdkpt.PlatformToken{
		Name:           "ci-token",
		TokenID:        "tok-001",
		Status:         "ACTIVE",
		ExpirationDate: expiry,
		Scope:          []string{"storage:events:read", "account-idm-read"},
		Token:          "dt0s16.secret",
	}
	got := fromSDK(&sdk)
	if got.Name != "ci-token" {
		t.Errorf("Name = %q, want %q", got.Name, "ci-token")
	}
	if got.TokenID != "tok-001" {
		t.Errorf("TokenID = %q, want %q", got.TokenID, "tok-001")
	}
	if got.Status != "ACTIVE" {
		t.Errorf("Status = %q, want %q", got.Status, "ACTIVE")
	}
	if got.ExpirationDate != expiry {
		t.Errorf("ExpirationDate = %q, want %q", got.ExpirationDate, expiry)
	}
	if got.Scope != "storage:events:read account-idm-read" {
		t.Errorf("Scope = %q, want joined string", got.Scope)
	}
	if got.Token != "dt0s16.secret" {
		t.Errorf("Token = %q, want secret", got.Token)
	}
}

func TestFromSDK_ExpiredToken(t *testing.T) {
	sdk := sdkpt.PlatformToken{
		Name:           "old-token",
		TokenID:        "tok-old",
		Status:         "ACTIVE",
		ExpirationDate: "2020-01-01T00:00:00.000Z",
	}
	got := fromSDK(&sdk)
	if got.Status != "EXPIRED" {
		t.Errorf("Status = %q, want %q for past expiration", got.Status, "EXPIRED")
	}
}

func TestFromSDK_RevokedNotOverridden(t *testing.T) {
	sdk := sdkpt.PlatformToken{
		Name:           "revoked-token",
		TokenID:        "tok-rev",
		Status:         "REVOKED",
		ExpirationDate: "2020-01-01T00:00:00.000Z",
	}
	got := fromSDK(&sdk)
	if got.Status != "REVOKED" {
		t.Errorf("Status = %q, want %q (REVOKED should not be overridden)", got.Status, "REVOKED")
	}
}

func TestToken_TableTag_HidesSecret(t *testing.T) {
	// Token must have table:"-" — verify by checking struct tag
	ft, ok := reflect.TypeOf(PlatformToken{}).FieldByName("Token")
	if !ok {
		t.Fatal("PlatformToken.Token field not found")
	}
	tag := ft.Tag.Get("table")
	if tag != "-" {
		t.Errorf("PlatformToken.Token table tag = %q, want \"-\"", tag)
	}
}

func TestList(t *testing.T) {
	const uuid = "list-uuid"
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath(uuid), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sdkpt.PlatformTokenListResponse{
			Total: 2,
			Results: []sdkpt.PlatformToken{
				{Name: "alpha", TokenID: "tok-a", Status: "ACTIVE", Scope: []string{"account-idm-read"}},
				{Name: "beta", TokenID: "tok-b", Status: "REVOKED", Scope: []string{"storage:events:read"}},
			},
		})
	})

	h := NewHandler(newTestClient(t, mux), uuid)
	tokens, err := h.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(tokens))
	}
	if tokens[0].TokenID != "tok-a" {
		t.Errorf("tokens[0].TokenID = %q, want %q", tokens[0].TokenID, "tok-a")
	}
	if tokens[1].Scope != "storage:events:read" {
		t.Errorf("tokens[1].Scope = %q, want joined scope", tokens[1].Scope)
	}
}

func TestCreate(t *testing.T) {
	const uuid = "create-uuid"
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath(uuid), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(sdkpt.PlatformToken{
			Name:    "ci-token",
			TokenID: "tok-new",
			Token:   "dt0s16.thetoken",
			Status:  "ACTIVE",
			Scope:   []string{"account-idm-write"},
		})
	})

	h := NewHandler(newTestClient(t, mux), uuid)
	result, err := h.Create(PlatformTokenCreate{
		Name:           "ci-token",
		UserUUID:       "user-001",
		Scope:          []string{"account-idm-write"},
		Resource:       []string{},
		Tags:           []string{},
		ExpirationDate: futureExpiry(),
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.Token != "dt0s16.thetoken" {
		t.Errorf("Token = %q, want secret", result.Token)
	}
}

func TestRevoke(t *testing.T) {
	const uuid = "revoke-uuid"
	const tokenID = "tok-to-delete"
	mux := http.NewServeMux()
	mux.HandleFunc(tokenBasePath(uuid)+"/"+tokenID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux), uuid)
	if err := h.Revoke(tokenID); err != nil {
		t.Fatalf("Revoke() error: %v", err)
	}
}
