//go:build integration
// +build integration

package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/resources/platformtoken"
	sdkauth "github.com/Dynatrace/dtctl/sdk/auth"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// setupAccountTokenTest resolves account credentials in priority order:
//  1. DTCTL_ACCOUNT_TOKEN + DTCTL_ACCOUNT_UUID env vars (CI / explicit override)
//  2. Local dtctl config + keyring (after `dtctl account login`)
func setupAccountTokenTest(t *testing.T) (*httpclient.Client, string, string) {
	t.Helper()

	token := os.Getenv("DTCTL_ACCOUNT_TOKEN")
	accountUUID := os.Getenv("DTCTL_ACCOUNT_UUID")

	if token == "" || accountUUID == "" {
		// Fall back to the local config + keyring so developers who have run
		// `dtctl account login` don't need to set any env vars.
		cfg, err := config.Load()
		if err != nil {
			t.Skip("Skipping account token test: DTCTL_ACCOUNT_TOKEN/UUID not set and config not loadable:", err)
		}
		ctx, err := cfg.CurrentContextObj()
		if err != nil {
			t.Skip("Skipping account token test: DTCTL_ACCOUNT_TOKEN/UUID not set and no current context:", err)
		}

		if accountUUID == "" {
			accountUUID = ctx.AccountUUID
		}
		if accountUUID == "" {
			t.Skip("Skipping account token test: set DTCTL_ACCOUNT_UUID or account-uuid in your dtctl context")
		}

		if token == "" {
			env := auth.DetectEnvironment(ctx.Environment)
			oauthCfg := auth.AccountOAuthConfig(env, ctx.SafetyLevel, accountUUID)
			tm, err := auth.NewTokenManager(oauthCfg)
			if err != nil {
				t.Skip("Skipping account token test: could not open keyring:", err)
			}
			token, err = tm.GetToken("account-" + accountUUID)
			if err != nil || token == "" {
				t.Skip("Skipping account token test: no account token in keyring — run 'dtctl account login'")
			}
		}
	}

	userUUID, err := sdkauth.ExtractJWTSubject(token)
	if err != nil {
		t.Fatalf("could not extract user UUID from account token: %v", err)
	}

	baseURL := os.Getenv("DTCTL_ACCOUNT_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.dynatrace.com"
	}

	c, err := httpclient.New(baseURL, httpclient.WithToken(token))
	if err != nil {
		t.Fatalf("failed to create account client: %v", err)
	}

	return c, accountUUID, userUUID
}

// createTestToken creates a token and registers cleanup. Returns the created token.
func createTestToken(t *testing.T, handler *platformtoken.Handler, name, userUUID, accountUUID string, scopes []string) *platformtoken.PlatformToken {
	t.Helper()
	expiration := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	tok, err := handler.Create(platformtoken.PlatformTokenCreate{
		Name:           name,
		UserUUID:       userUUID,
		Scope:          scopes,
		Resource:       []string{"urn:dtaccount:" + accountUUID},
		ExpirationDate: expiration,
	})
	if err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
	if tok.TokenID == "" {
		t.Fatalf("Create(%q): empty TokenID", name)
	}
	if tok.Token == "" {
		t.Fatalf("Create(%q): missing token secret in response", name)
	}
	t.Logf("created token %q (ID: %s)", name, tok.TokenID)
	t.Cleanup(func() {
		_ = handler.Revoke(tok.TokenID)
	})
	return tok
}

func findInList(tokens []platformtoken.PlatformToken, tokenID string) *platformtoken.PlatformToken {
	for i := range tokens {
		if tokens[i].TokenID == tokenID {
			return &tokens[i]
		}
	}
	return nil
}

// TestAccountTokenList_ReadOnly verifies that listing tokens works without write access.
func TestAccountTokenList_ReadOnly(t *testing.T) {
	c, accountUUID, _ := setupAccountTokenTest(t)
	handler := platformtoken.NewHandler(c, accountUUID)

	tokens, err := handler.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	t.Logf("listed %d platform tokens", len(tokens))
}

// TestAccountTokenLifecycle covers the full create → list → revoke → verify cycle
// for tokens with single and multiple scopes.
func TestAccountTokenLifecycle(t *testing.T) {
	c, accountUUID, userUUID := setupAccountTokenTest(t)
	handler := platformtoken.NewHandler(c, accountUUID)

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Create three tokens:
	//   token1 — single scope
	//   token2, token3 — multiple scopes
	token1 := createTestToken(t, handler, "dtctl-e2e-tok1-"+ts, userUUID, accountUUID, []string{"storage:buckets:read"})
	token2 := createTestToken(t, handler, "dtctl-e2e-tok2-"+ts, userUUID, accountUUID, []string{"storage:buckets:read", "storage:logs:read"})
	token3 := createTestToken(t, handler, "dtctl-e2e-tok3-"+ts, userUUID, accountUUID, []string{"storage:buckets:read", "storage:logs:read"})

	t.Run("verify_in_list", func(t *testing.T) {
		tokens, err := handler.List()
		if err != nil {
			t.Fatalf("List() error: %v", err)
		}
		for _, tok := range []*platformtoken.PlatformToken{token1, token2, token3} {
			entry := findInList(tokens, tok.TokenID)
			if entry == nil {
				t.Errorf("token %q (ID: %s) not found in list after creation", tok.Name, tok.TokenID)
				continue
			}
			if entry.Status == "REVOKED" {
				t.Errorf("token %q shows REVOKED immediately after creation", tok.TokenID)
			}
		}
	})

	t.Run("revoke", func(t *testing.T) {
		for _, tok := range []*platformtoken.PlatformToken{token1, token2, token3} {
			if err := handler.Revoke(tok.TokenID); err != nil {
				t.Errorf("Revoke(%q): %v", tok.TokenID, err)
			} else {
				t.Logf("revoked %s", tok.TokenID)
			}
		}
	})

	t.Run("verify_after_revoke", func(t *testing.T) {
		tokens, err := handler.List()
		if err != nil {
			t.Fatalf("List() error: %v", err)
		}
		for _, tok := range []*platformtoken.PlatformToken{token1, token2, token3} {
			entry := findInList(tokens, tok.TokenID)
			if entry == nil {
				continue // absent from list is fine
			}
			if entry.Status != "REVOKED" {
				t.Errorf("token %q still has status %q after revoke", tok.TokenID, entry.Status)
			}
		}
	})
}

// TestAccountTokenDoubleRevoke verifies that revoking an already-revoked token returns an error.
func TestAccountTokenDoubleRevoke(t *testing.T) {
	c, accountUUID, userUUID := setupAccountTokenTest(t)
	handler := platformtoken.NewHandler(c, accountUUID)

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	tok := createTestToken(t, handler, "dtctl-e2e-dbl-revoke-"+ts, userUUID, accountUUID, []string{"storage:buckets:read"})

	if err := handler.Revoke(tok.TokenID); err != nil {
		t.Fatalf("first Revoke() error: %v", err)
	}

	if err := handler.Revoke(tok.TokenID); err == nil {
		t.Error("second Revoke() should have returned an error for an already-revoked token")
	} else {
		t.Logf("second Revoke() correctly returned error: %v", err)
	}
}
