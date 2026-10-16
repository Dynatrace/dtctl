package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

func withStubbedSessionStatus(t *testing.T, status *SessionStatus) {
	t.Helper()
	original := buildSessionStatusFunc
	buildSessionStatusFunc = func(contextName string, ctx *config.Context, tokenName string) (*SessionStatus, error) {
		return status, nil
	}
	t.Cleanup(func() { buildSessionStatusFunc = original })
}

func TestOAuthSessionCheckResult_OK(t *testing.T) {
	exp := time.Now().Add(30 * time.Minute)
	status := &SessionStatus{
		IsOAuth:              true,
		AccessTokenPresent:   true,
		AccessTokenExpiresAt: &exp,
		RefreshTokenPresent:  true,
	}

	r := oauthSessionCheckResult(status)
	if r.Status != "ok" {
		t.Errorf("expected ok, got %q (detail: %s)", r.Status, r.Detail)
	}
	if !strings.Contains(r.Detail, "refresh token present") {
		t.Errorf("expected detail to mention refresh token, got %q", r.Detail)
	}
}

func TestOAuthSessionCheckResult_NoRefreshToken(t *testing.T) {
	exp := time.Now().Add(30 * time.Minute)
	status := &SessionStatus{
		IsOAuth:              true,
		AccessTokenPresent:   true,
		AccessTokenExpiresAt: &exp,
		RefreshTokenPresent:  false,
	}

	r := oauthSessionCheckResult(status)
	if r.Status != "warn" {
		t.Errorf("expected warn, got %q", r.Status)
	}
	if !strings.Contains(r.Detail, "no refresh token") {
		t.Errorf("expected detail to mention no refresh token, got %q", r.Detail)
	}
	if !strings.Contains(r.Detail, "dtctl auth login") {
		t.Errorf("expected detail to recommend login, got %q", r.Detail)
	}
}

func TestOAuthSessionCheckResult_AccessExpired(t *testing.T) {
	exp := time.Now().Add(-5 * time.Minute)
	status := &SessionStatus{
		IsOAuth:              true,
		AccessTokenPresent:   true,
		AccessTokenExpiresAt: &exp,
		RefreshTokenPresent:  true,
	}

	r := oauthSessionCheckResult(status)
	if r.Status != "warn" {
		t.Errorf("expected warn, got %q", r.Status)
	}
	if !strings.Contains(r.Detail, "access token expired") {
		t.Errorf("expected detail to mention expired access token, got %q", r.Detail)
	}
}

func TestDoctor_OAuthSessionRow_OK(t *testing.T) {
	exp := time.Now().Add(30 * time.Minute)
	withStubbedSessionStatus(t, &SessionStatus{
		IsOAuth:              true,
		AccessTokenPresent:   true,
		AccessTokenExpiresAt: &exp,
		RefreshTokenPresent:  true,
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/platform/metadata/v1/user":
			resp := map[string]interface{}{
				"userId":       "test-user-id",
				"emailAddress": "test@example.com",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", server.URL, "test-oauth")
	if err := cfg.SetToken("test-oauth", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := runDoctorChecks(context.Background())

	found := false
	for _, r := range results {
		if r.Name == "OAuth session" {
			found = true
			if r.Status != "ok" {
				t.Errorf("expected OAuth session ok, got %q (detail: %s)", r.Status, r.Detail)
			}
		}
	}
	if !found {
		t.Error("expected 'OAuth session' row in doctor output")
	}
}

func TestDoctor_OAuthSessionRow_SkippedForPlatformToken(t *testing.T) {
	withStubbedSessionStatus(t, &SessionStatus{IsOAuth: false})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", server.URL, "test-platform")
	if err := cfg.SetToken("test-platform", "dt0c01.ST.platform-token.secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := runDoctorChecks(context.Background())

	for _, r := range results {
		if r.Name == "OAuth session" {
			t.Errorf("did not expect 'OAuth session' row for platform token, got %q: %s", r.Status, r.Detail)
		}
	}
}

func TestDoctor_OAuthSessionRow_FailWhenSessionError(t *testing.T) {
	// Stub buildSessionStatusFunc to return an error (e.g. keyring unavailable)
	original := buildSessionStatusFunc
	buildSessionStatusFunc = func(contextName string, ctx *config.Context, tokenName string) (*SessionStatus, error) {
		return nil, errors.New("keyring unavailable")
	}
	t.Cleanup(func() { buildSessionStatusFunc = original })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/platform/metadata/v1/user":
			resp := map[string]interface{}{
				"userId":       "test-user-id",
				"emailAddress": "test@example.com",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", server.URL, "test-oauth")
	if err := cfg.SetToken("test-oauth", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := runDoctorChecks(context.Background())

	found := false
	for _, r := range results {
		if r.Name == "OAuth session" {
			found = true
			if r.Status != "fail" {
				t.Errorf("expected OAuth session fail, got %q (detail: %s)", r.Status, r.Detail)
			}
			if !strings.Contains(r.Detail, "keyring unavailable") {
				t.Errorf("expected detail to contain error message, got %q", r.Detail)
			}
		}
	}
	if !found {
		t.Error("expected 'OAuth session' fail row in doctor output when buildSessionStatus errors")
	}
}

func TestAccessTokenSummary(t *testing.T) {
	future := time.Now().Add(30 * time.Minute)
	past := time.Now().Add(-5 * time.Minute)

	cases := []struct {
		name    string
		status  *SessionStatus
		wantSub string // substring that must appear in the summary
		wantNot string // substring that must NOT appear (optional)
	}{
		{
			name:    "no tokens at all",
			status:  &SessionStatus{},
			wantSub: "not present",
		},
		{
			name: "cached access token, valid",
			status: &SessionStatus{
				AccessTokenPresent:   true,
				AccessTokenExpiresAt: &future,
				RefreshTokenPresent:  true,
			},
			wantSub: "valid for",
		},
		{
			name: "cached access token, expired with refresh",
			status: &SessionStatus{
				AccessTokenPresent:   true,
				AccessTokenExpiresAt: &past,
				RefreshTokenPresent:  true,
			},
			wantSub: "expired",
			wantNot: "valid for",
		},
		{
			// Regression: medium-compact keyring storage drops the access-token JWT
			// but keeps ExpiresAt. The summary must not claim "valid for X" because
			// accessTokenPresent is false in the structured (JSON) output — the two
			// would contradict each other.
			name: "access token not cached locally, expiry known, refresh present",
			status: &SessionStatus{
				AccessTokenPresent:   false,
				AccessTokenExpiresAt: &future,
				RefreshTokenPresent:  true,
			},
			wantSub: "not cached locally",
			wantNot: "valid for",
		},
		{
			name: "access token not cached, no expiry info, refresh present",
			status: &SessionStatus{
				AccessTokenPresent:  false,
				RefreshTokenPresent: true,
			},
			wantSub: "not cached locally",
			wantNot: "valid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := accessTokenSummary(tc.status)
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("expected summary to contain %q, got %q", tc.wantSub, got)
			}
			if tc.wantNot != "" && strings.Contains(got, tc.wantNot) {
				t.Errorf("expected summary NOT to contain %q, got %q", tc.wantNot, got)
			}
		})
	}
}

func TestPrintSessionStatusTable_DoesNotPanic(t *testing.T) {
	cases := []*SessionStatus{
		{Context: "c", Environment: "https://e", IsOAuth: false},
		func() *SessionStatus {
			exp := time.Now().Add(42 * time.Minute)
			return &SessionStatus{
				Context:              "c",
				Environment:          "https://e",
				IsOAuth:              true,
				Storage:              "macOS Keychain",
				AccessTokenPresent:   true,
				AccessTokenExpiresAt: &exp,
				RefreshTokenPresent:  true,
				GrantedScopes:        []string{"openid", "offline_access", "storage:logs:read"},
			}
		}(),
		{
			Context:             "c",
			Environment:         "https://e",
			IsOAuth:             true,
			AccessTokenPresent:  true,
			RefreshTokenPresent: false,
		},
	}

	for i, s := range cases {
		func(i int, s *SessionStatus) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("case %d panicked: %v", i, r)
				}
			}()
			printSessionStatusTable(context.Background(), s)
		}(i, s)
	}
}
