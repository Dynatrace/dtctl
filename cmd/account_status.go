package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

var accountStatusCmd = newAccountStatusCmd()

func newAccountStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show account authentication status",
		Long: `Show the status of the account-plane authentication session.

Displays the account UUID in use, whether a token is stored in the keyring
from 'dtctl account login', when it expires, and whether DTCTL_ACCOUNT_TOKEN
is overriding the stored token.`,
		Example: `  dtctl account status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return err
			}
			ctx, err := cfg.CurrentContextObj()
			if err != nil {
				return err
			}

			const w = 20

			// Resolve UUID from env/config only (no discovery — no token yet).
			accountUUID := ""
			uuidSource := ""
			if v := getenv(cmdContext(cmd), "DTCTL_ACCOUNT_UUID"); v != "" {
				accountUUID = v
				uuidSource = "DTCTL_ACCOUNT_UUID env var"
			} else if ctx.AccountUUID != "" {
				accountUUID = ctx.AccountUUID
				uuidSource = "context config"
			}

			if accountUUID != "" {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Account UUID:", w, "%s", accountUUID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "UUID source:", w, "%s", uuidSource)
			} else {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Account UUID:", w, "%s", "(not set — run 'dtctl account login --account-uuid <uuid>')")
			}

			fmt.Fprintln(currentStderr(cmdContext(cmd)))

			// Env var override warning.
			if v := getenv(cmdContext(cmd), "DTCTL_ACCOUNT_TOKEN"); v != "" {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Token source:", w, "%s", "DTCTL_ACCOUNT_TOKEN env var (overrides keyring)")
				output.FprintWarning(currentStderr(cmdContext(cmd)), "DTCTL_ACCOUNT_TOKEN is set — it overrides any stored account token.")
				output.FprintWarning(currentStderr(cmdContext(cmd)), "Unset it to use the token stored by 'dtctl account login'.")
				fmt.Fprintln(currentStderr(cmdContext(cmd)))
			}

			// Keyring token status.
			if accountUUID == "" {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Keyring token:", w, "%s", "(unknown — account UUID not set)")
				return nil
			}

			env := auth.DetectEnvironment(ctx.Environment)
			oauthCfg := auth.AccountOAuthConfig(env, ctx.SafetyLevel, accountUUID)
			tm, err := auth.NewTokenManager(oauthCfg)
			if err != nil {
				return fmt.Errorf("token manager: %w", err)
			}

			stored, err := tm.GetTokenInfo(accountTokenKeyName(accountUUID))
			if err != nil || stored == nil {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Keyring token:", w, "%s", "not found — run 'dtctl account login'")
				return nil
			}

			output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Keyring token:", w, "%s", "present")
			output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Storage:", w, "%s", "keyring")

			if stored.AccessToken != "" {
				if stored.ExpiresAt.IsZero() {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Access token:", w, "%s", "present (expiry unknown)")
				} else {
					remaining := time.Until(stored.ExpiresAt).Round(time.Second)
					if remaining > 0 {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Access token:", w, "valid for %s (expires %s)", remaining, stored.ExpiresAt.Format(time.RFC3339))
					} else {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Access token:", w, "expired at %s", stored.ExpiresAt.Format(time.RFC3339))
					}
				}
			} else {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Access token:", w, "%s", "not cached locally (will refresh on next call)")
			}

			if stored.RefreshToken != "" {
				if exp, ok := auth.DecodeRefreshTokenExpiry(stored.RefreshToken); ok {
					remaining := time.Until(exp).Round(time.Second)
					if remaining > 0 {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Refresh token:", w, "valid for %s (expires %s)", remaining, exp.Format(time.RFC3339))
					} else {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Refresh token:", w, "expired — run 'dtctl account login' again")
					}
				} else {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Refresh token:", w, "%s", "present")
				}
			} else {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Refresh token:", w, "%s", "not present")
			}

			if stored.Scope != "" {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Scopes:", w, "%s", strings.Join(strings.Fields(stored.Scope), ", "))
			}

			return nil
		},
	}
	return c
}

func init() {
	accountCmd.AddCommand(accountStatusCmd)
}
