package cmd

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/config"
)

const updCredID = "CREDENTIAL_VAULT_ENTRY-0000000000000001"

type updRecorder struct {
	mu    sync.Mutex
	calls []string
	body  map[string]any
}

// newUpdateCredentialServer serves one existing entry of the given type and
// owner, the current user (me@example.invalid) and PUT.
func newUpdateCredentialServer(t *testing.T, typ, owner string) (*httptest.Server, *updRecorder) {
	t.Helper()
	rec := &updRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/platform/metadata/v1/user":
			rec.calls = append(rec.calls, "GET user")
			_, _ = io.WriteString(w, `{"userId":"00000000-0000-0000-0000-000000000001","emailAddress":"me@example.invalid"}`)
		case r.URL.Path == "/platform/credential-vault/v1/entries/"+updCredID && r.Method == http.MethodGet:
			rec.calls = append(rec.calls, "GET cred")
			_, _ = io.WriteString(w, `{"id":"`+updCredID+`","name":"signing-ca","type":"`+typ+`","description":"keep me",`+
				`"scopes":["EXTENSION"],"owner":"`+owner+`","ownerAccessOnly":false,"allowContextlessRequests":true,`+
				`"allowedEntities":[{"id":"app-1","type":"APPLICATION"}]}`)
		case r.URL.Path == "/platform/credential-vault/v1/entries/"+updCredID && r.Method == http.MethodPut:
			rec.calls = append(rec.calls, "PUT cred")
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &rec.body)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestUpdateCredential_RotatesCertificateAndKeepsMetadata(t *testing.T) {
	srv, rec := newUpdateCredentialServer(t, "PUBLIC_CERTIFICATE", "me@example.invalid")
	code, out, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadWriteAll, credTestPEM,
		"update", "credential", updCredID, "--certificate-file", "-", "--no-agent")
	require.Zero(t, code, errOut)
	require.Contains(t, errOut, updCredID)
	require.NotContains(t, out+errOut, "c2VjcmV0LW1hcmtlcg", "the certificate must never be printed")

	require.Contains(t, rec.calls, "PUT cred")
	b := rec.body
	require.Equal(t, "signing-ca", b["name"], "name is kept")
	require.Equal(t, "keep me", b["description"], "description is kept")
	require.Equal(t, []any{"EXTENSION"}, b["scopes"], "scopes are kept")
	require.Equal(t, "PUBLIC_CERTIFICATE", b["type"])
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte(credTestPEM)), b["certificate"])
	require.Equal(t, "PEM", b["certificateFormat"])
	require.Equal(t, true, b["allowContextlessRequests"], "PUT replaces the entry: contextless access must be carried over")
	require.Equal(t, []any{map[string]any{"id": "app-1", "type": "APPLICATION"}}, b["allowedEntities"], "allowed entities must be carried over")
	require.NotContains(t, b, "id", "the body must not carry the ID")
}

func TestUpdateCredential_FlagsChangeMetadata(t *testing.T) {
	srv, rec := newUpdateCredentialServer(t, "TOKEN", "me@example.invalid")
	code, _, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadWriteAll, "tok-secret\n",
		"update", "credential", updCredID, "--token-file", "-", "--name", "renamed", "--scope", "extension-authentication",
		"--owner-access-only", "--no-agent")
	require.Zero(t, code, errOut)
	require.Equal(t, "renamed", rec.body["name"])
	require.Equal(t, []any{"EXTENSION_AUTHENTICATION"}, rec.body["scopes"])
	require.Equal(t, true, rec.body["ownerAccessOnly"])
	require.Equal(t, "tok-secret", rec.body["token"])
	require.NotContains(t, errOut, "tok-secret")
}

func TestUpdateCredential_RequiresSecret(t *testing.T) {
	srv, rec := newUpdateCredentialServer(t, "TOKEN", "me@example.invalid")
	code, _, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadWriteAll, "",
		"update", "credential", updCredID, "--name", "renamed", "--no-agent")
	require.NotZero(t, code)
	require.Contains(t, errOut, "must supply it again")
	require.Empty(t, rec.calls, "a missing secret fails before any request")
}

func TestUpdateCredential_DryRunSendsNoWriteAndHidesSecret(t *testing.T) {
	srv, rec := newUpdateCredentialServer(t, "TOKEN", "someone@example.invalid")
	// readonly: a preview is gated like a read.
	code, out, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadOnly, "tok-secret",
		"update", "credential", updCredID, "--token-file", "-", "--name", "renamed", "--dry-run", "--no-agent")
	require.Zero(t, code, errOut)
	require.Contains(t, out, "signing-ca → renamed")
	require.Contains(t, out, srv.URL, "the preview names the environment")
	require.NotContains(t, out+errOut, "tok-secret")
	require.NotContains(t, rec.calls, "PUT cred")
}

func TestUpdateCredential_SafetyAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name  string
		level config.SafetyLevel
		owner string
		ok    bool
	}{
		{"readonly blocked", config.SafetyLevelReadOnly, "me@example.invalid", false},
		{"readwrite-mine own", config.SafetyLevelReadWriteMine, "me@example.invalid", true},
		{"readwrite-mine other", config.SafetyLevelReadWriteMine, "someone@example.invalid", false},
		{"readwrite-all other", config.SafetyLevelReadWriteAll, "someone@example.invalid", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, rec := newUpdateCredentialServer(t, "TOKEN", tc.owner)
			code, _, errOut := runCredentialCreate(t, srv.URL, tc.level, "tok",
				"update", "credential", updCredID, "--token-file", "-", "--no-agent")
			if tc.ok {
				require.Zero(t, code, errOut)
				require.Contains(t, rec.calls, "PUT cred")
			} else {
				require.NotZero(t, code)
				require.NotContains(t, rec.calls, "PUT cred")
			}
		})
	}
}

func TestUpdateCredential_FileCannotChangeType(t *testing.T) {
	srv, rec := newUpdateCredentialServer(t, "PUBLIC_CERTIFICATE", "me@example.invalid")
	def := `{"name":"x","type":"TOKEN","scopes":["EXTENSION"],"token":"t"}`
	code, _, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadWriteAll, def,
		"update", "credential", updCredID, "-f", "-", "--no-agent")
	require.NotZero(t, code)
	require.True(t, strings.Contains(errOut, "cannot change"), errOut)
	require.NotContains(t, rec.calls, "PUT cred")
}
