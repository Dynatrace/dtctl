package cmd

import (
	"bytes"
	"context"
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
	"github.com/Dynatrace/dtctl/pkg/resources/credentialvault"
)

const credTestPEM = "-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIUc2VjcmV0LW1hcmtlcg==\n-----END CERTIFICATE-----\n"

type credRecorder struct {
	mu    sync.Mutex
	calls []string
	body  map[string]any
}

func newCredentialServer(t *testing.T) (*httptest.Server, *credRecorder) {
	t.Helper()
	rec := &credRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.calls = append(rec.calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost {
			_ = json.Unmarshal(raw, &rec.body)
		}
		rec.mu.Unlock()
		if r.Method == http.MethodPost && r.URL.Path == "/platform/credential-vault/v1/entries" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"CREDENTIALS_VAULT-0000000000000009"}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func runCredentialCreate(t *testing.T, srvURL string, level config.SafetyLevel, stdin string, argv ...string) (int, string, string) {
	t.Helper()
	clearAgentEnvVars(t)
	t.Cleanup(func() { restorePristineTree(context.Background()) })
	var stdout, stderr bytes.Buffer
	code := Run(argv, RunOptions{
		Session: &Session{EnvironmentURL: srvURL, Token: "t", SafetyLevel: level, MinStability: "experimental"},
		Stdin:   strings.NewReader(stdin),
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	return code, stdout.String(), stderr.String()
}

var publicCertArgs = []string{
	"create", "credential", "--type", "public-certificate", "--scope", "extension",
	"--name", "signing-ca", "--certificate-file", "-",
}

func TestCreateCredential_DryRunSendsNothingAndHidesCertificate(t *testing.T) {
	srv, rec := newCredentialServer(t)
	for _, mode := range []string{"--agent", "--no-agent"} {
		code, out, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadWriteAll, credTestPEM,
			append(append([]string{}, publicCertArgs...), "--dry-run", mode)...)
		require.Zero(t, code, errOut)
		require.Contains(t, out, "signing-ca")
		require.Contains(t, out, "PUBLIC_CERTIFICATE")
		require.Contains(t, out, srv.URL, "dry run must name the target environment")
		require.NotContains(t, out+errOut, "c2VjcmV0LW1hcmtlcg", "dry run must not echo the certificate")
	}
	require.Empty(t, rec.calls, "a dry run must not call the API")
}

func TestCreateCredential_PublicCertificateBody(t *testing.T) {
	srv, rec := newCredentialServer(t)
	code, out, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadWriteAll, credTestPEM,
		append(append([]string{}, publicCertArgs...), "--no-agent")...)
	require.Zero(t, code, errOut)
	require.Contains(t, errOut, "CREDENTIALS_VAULT-0000000000000009")
	require.NotContains(t, out+errOut, "c2VjcmV0LW1hcmtlcg")

	require.Equal(t, []string{"POST /platform/credential-vault/v1/entries"}, rec.calls)
	require.Equal(t, "PUBLIC_CERTIFICATE", rec.body["type"])
	require.Equal(t, []any{"EXTENSION"}, rec.body["scopes"])
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte(credTestPEM)), rec.body["certificate"], "the API decodes certificate as base64")
	require.Equal(t, "PEM", rec.body["certificateFormat"])
	require.Equal(t, false, rec.body["ownerAccessOnly"])
	require.NotContains(t, rec.body, "scope", "deprecated singular scope must not be sent")
}

func TestCreateCredential_ReadOnlyIsBlocked(t *testing.T) {
	srv, rec := newCredentialServer(t)
	code, _, _ := runCredentialCreate(t, srv.URL, config.SafetyLevelReadOnly, credTestPEM,
		append(append([]string{}, publicCertArgs...), "--no-agent")...)
	require.NotZero(t, code)
	require.Empty(t, rec.calls, "readonly must block before any request")
}

func TestCredentialCommands_403NamesScopeAndBothFixes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"OAuth token is missing required scope. Use one of: [credential-vault:entries:read]"}}`)
	}))
	t.Cleanup(srv.Close)

	code, out, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadWriteAll, "", "get", "credentials", "--no-agent")
	require.NotZero(t, code)
	all := out + errOut
	require.Contains(t, all, "credential-vault:entries:read")
	require.Contains(t, all, "Platform-token context")
	require.Contains(t, all, "run 'dtctl auth login' again")
}

func TestEmptyCredentialListHint(t *testing.T) {
	authScope := credentialvault.ListOptions{Scope: "EXTENSION_AUTHENTICATION"}
	require.Contains(t, emptyCredentialListHint(nil, authScope), "--scope extension")
	require.Contains(t, emptyCredentialListHint(nil, credentialvault.ListOptions{Scope: "EXTENSION_AUTHENTICATION", Type: "PUBLIC_CERTIFICATE"}), "--scope extension")
	require.Empty(t, emptyCredentialListHint([]credentialvault.Credential{{ID: "x"}}, authScope), "no hint when something matched")
	require.Empty(t, emptyCredentialListHint(nil, credentialvault.ListOptions{Scope: "EXTENSION_AUTHENTICATION", Type: "TOKEN"}), "tokens legitimately use extension-authentication")
	require.Empty(t, emptyCredentialListHint(nil, credentialvault.ListOptions{Scope: "EXTENSION"}))
}

// Under readwrite-mine a credential may be deleted only by its owner; the vault
// reports the owner as an email address.
func TestDeleteCredential_ReadWriteMineChecksOwner(t *testing.T) {
	for _, tc := range []struct {
		owner     string
		wantFlow  bool
		wantCalls []string
	}{
		{"me@example.invalid", true, []string{"GET cred", "GET user", "DELETE cred"}},
		{"ME@Example.invalid", true, []string{"GET cred", "GET user", "DELETE cred"}},
		{"someone@example.invalid", false, []string{"GET cred", "GET user"}},
	} {
		var mu sync.Mutex
		var calls []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.URL.Path == "/platform/metadata/v1/user":
				calls = append(calls, "GET user")
				_, _ = io.WriteString(w, `{"userId":"00000000-0000-0000-0000-000000000001","emailAddress":"me@example.invalid"}`)
			case strings.HasPrefix(r.URL.Path, "/platform/credential-vault/v1/entries/") && r.Method == http.MethodGet:
				calls = append(calls, "GET cred")
				_, _ = io.WriteString(w, `{"id":"CREDENTIAL_VAULT_ENTRY-1","name":"ca","type":"PUBLIC_CERTIFICATE","scopes":["EXTENSION"],"owner":"`+tc.owner+`"}`)
			case r.Method == http.MethodDelete:
				calls = append(calls, "DELETE cred")
				w.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(w, r)
			}
		}))
		code, _, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadWriteMine, "",
			"delete", "credential", "CREDENTIAL_VAULT_ENTRY-1", "--yes", "--no-agent")
		srv.Close()
		if tc.wantFlow {
			require.Zero(t, code, errOut)
		} else {
			require.NotZero(t, code, "deleting another user's credential under readwrite-mine must be refused")
		}
		require.Equal(t, tc.wantCalls, calls, tc.owner)
	}
}

// -vv prints request bodies; the certificate must be redacted from them.
func TestCreateCredential_VerboseDebugRedactsSecrets(t *testing.T) {
	srv, rec := newCredentialServer(t)
	code, out, errOut := runCredentialCreate(t, srv.URL, config.SafetyLevelReadWriteAll, credTestPEM,
		append(append([]string{}, publicCertArgs...), "--no-agent", "-vv")...)
	require.Zero(t, code, errOut)
	require.Len(t, rec.calls, 1)
	require.Contains(t, errOut, "BODY:", "-vv must still print the request body")
	require.Contains(t, errOut, "[REDACTED]")
	b64 := rec.body["certificate"].(string)
	require.NotContains(t, out+errOut, b64, "the base64 certificate must not be printed")
	require.NotContains(t, out+errOut, "c2VjcmV0LW1hcmtlcg")
}
