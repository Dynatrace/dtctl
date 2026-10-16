package cmd

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/diagnostic"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/version"
)

// checkResult represents the outcome of a single doctor check
type checkResult struct {
	Name   string
	Status string // "ok", "warn", "fail"
	Detail string
}

// checkKeyringFunc is the function used to probe keyring availability.
// It defaults to config.CheckKeyring and can be overridden in tests.
var checkKeyringFunc = config.CheckKeyring

// doctorCmd runs health checks on the dtctl configuration and connectivity
var doctorCmd = newDoctorCmd()

func newDoctorCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Check configuration, connectivity, and authentication health",
		Long: `Run a series of diagnostic checks to verify that dtctl is properly configured
and can communicate with the Dynatrace environment.

Checks performed:
  1. dtctl version
  2. Configuration file exists and is valid
  3. Current context is set
  4. Environment URL pattern is valid (detects common domain mistakes)
  5. Keyring status (secure token storage)
  6. Token is retrievable (keyring or config)
  7. Environment URL is reachable (HTTP connectivity)
  8. API authentication works (user identity)`,
		Example: `  # Run all checks
  dtctl doctor

  # Run checks for a specific context
  dtctl doctor --context production`,
		RunE: func(cmd *cobra.Command, args []string) error {
			results := runDoctorChecks(cmdContext(cmd))
			printDoctorResults(cmdContext(cmd), results)

			// Return error if any check failed
			for _, r := range results {
				if r.Status == "fail" {
					return fmt.Errorf("one or more checks failed")
				}
			}
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

func runDoctorChecks(ctx context.Context) []checkResult {
	return runDoctorChecksWithClient(ctx, &http.Client{Timeout: 10 * time.Second})
}

func runDoctorChecksWithClient(ictx context.Context, httpClient *http.Client) []checkResult {
	var results []checkResult

	// 1. Version
	results = append(results, checkResult{
		Name:   "dtctl version",
		Status: "ok",
		Detail: fmt.Sprintf("%s (commit: %s, built: %s)", version.Version, version.Commit, version.Date),
	})

	// 2. Configuration file
	cfg, cfgErr := loadConfig(ictx)
	if cfgErr != nil {
		results = append(results, checkResult{
			Name:   "Configuration",
			Status: "fail",
			Detail: cfgErr.Error(),
		})
		return results // Cannot continue without config
	}

	configPath := config.FindLocalConfig()
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	results = append(results, checkResult{
		Name:   "Configuration",
		Status: "ok",
		Detail: configPath,
	})

	// 3. Current context
	if cfg.CurrentContext == "" {
		results = append(results, checkResult{
			Name:   "Current context",
			Status: "fail",
			Detail: "no current context set (use 'dtctl ctx <name>' to switch, or 'dtctl ctx set <name> --environment <url>' to create one)",
		})
		return results
	}

	ctx, ctxErr := cfg.CurrentContextObj()
	if ctxErr != nil {
		results = append(results, checkResult{
			Name:   "Current context",
			Status: "fail",
			Detail: ctxErr.Error(),
		})
		return results
	}

	// Config-level: reports the clamped level a local config actually gets.
	safetyLevel := cfg.GetEffectiveSafetyLevel().String()
	results = append(results, checkResult{
		Name:   "Current context",
		Status: "ok",
		Detail: fmt.Sprintf("%s (environment: %s, safety: %s)", cfg.CurrentContext, ctx.Environment, safetyLevel),
	})

	// 4. Environment URL validation
	if urlProblems := diagnostic.CheckEnvironmentURL(ctx.Environment); len(urlProblems) > 0 {
		for _, p := range urlProblems {
			detail := p.Message
			if p.SuggestedURL != "" {
				detail += fmt.Sprintf(". Suggested fix: dtctl ctx set %s --environment %s", cfg.CurrentContext, p.SuggestedURL)
			}
			results = append(results, checkResult{
				Name:   "Environment URL",
				Status: "warn",
				Detail: detail,
			})
		}
	} else {
		results = append(results, checkResult{
			Name:   "Environment URL",
			Status: "ok",
			Detail: "URL pattern looks correct",
		})
	}

	// 5. Keyring status
	storageIdx := len(results)
	keyringErr := checkKeyringFunc()
	switch {
	case config.IsFileTokenStorage():
		// Explicit file storage bypasses the keyring for OAuth tokens, so a
		// reachable keyring is not where they go.
		detail := fmt.Sprintf("file-based (%s=file)", config.EnvTokenStorage)
		if getenv(ictx, config.EnvTokenStorage) == "" {
			detail = fmt.Sprintf("file-based (chosen once; undo: delete %s or set %s=keyring)",
				config.FileTokenStorageConsentPath(), config.EnvTokenStorage)
		}
		if keyringErr != nil {
			detail += fmt.Sprintf("; keyring unavailable: %v", keyringErr)
		}
		results = append(results, checkResult{
			Name:   "Token storage",
			Status: "ok",
			Detail: detail,
		})
	case keyringErr != nil:
		detail := fmt.Sprintf("%s: %v", config.KeyringBackend(), keyringErr)
		if strings.Contains(keyringErr.Error(), config.ErrMsgCollectionUnlock) {
			detail += " (run 'dtctl auth login' to create the collection automatically)"
		}
		if config.IsKeyringAbsent(keyringErr) {
			detail += fmt.Sprintf(" (no keyring here: 'dtctl auth login' offers file-based token storage, or set %s=file)", config.EnvTokenStorage)
		} else {
			detail += fmt.Sprintf(" (set %s=file to use file-based token storage)", config.EnvTokenStorage)
		}
		results = append(results, checkResult{
			Name:   "Token storage",
			Status: "warn",
			Detail: detail,
		})
	default:
		results = append(results, checkResult{
			Name:   "Token storage",
			Status: "ok",
			Detail: config.KeyringBackend(),
		})
	}

	// 6. Token retrieval
	token, tokenErr := client.GetTokenWithOAuthSupport(cfg, ctx.TokenRef)
	if tokenErr != nil || token == "" {
		detail := "token not found"
		if tokenErr != nil {
			detail = tokenErr.Error()
		}
		results = append(results, checkResult{
			Name:   "Token",
			Status: "fail",
			Detail: fmt.Sprintf("cannot retrieve token %q: %s", ctx.TokenRef, detail),
		})
		return results
	}

	// OAuth session state (only applies when the stored token is OAuth). It is
	// read before the Token row is built because it knows which store the
	// token was actually found in.
	session, sessionErr := buildSessionStatusFunc(cfg.CurrentContext, ctx, ctx.TokenRef)
	oauthSession := sessionErr == nil && session.IsOAuth

	tokenSource := "config file"
	switch {
	case oauthSession && session.storage == auth.TokenStorageFile:
		tokenSource = fmt.Sprintf("file store (%s)", session.Storage)
	case oauthSession && session.storage == auth.TokenStorageKeyring:
		tokenSource = fmt.Sprintf("keyring (%s)", session.Storage)
	case config.IsKeyringAvailable():
		tokenSource = fmt.Sprintf("keyring (%s)", config.KeyringBackend())
	case config.IsFileTokenStorage():
		tokenSource = fmt.Sprintf("file store (%s)", config.OAuthStorageBackend())
	}

	// The keyring probe only tests reads. A keyring that answers reads but
	// refused the write (macOS `security` exit status 44) leaves the OAuth
	// token in the file store, so the probe's "ok" would name the wrong
	// backend (#393).
	if oauthSession && session.storage == auth.TokenStorageFile &&
		!config.IsFileTokenStorage() && results[storageIdx].Status == "ok" {
		results[storageIdx] = checkResult{
			Name:   "Token storage",
			Status: "warn",
			Detail: fmt.Sprintf("%s is reachable, but OAuth token %q is stored in %s, not the keyring (set %s=file to make file storage explicit)",
				config.KeyringBackend(), ctx.TokenRef, session.Storage, config.EnvTokenStorage),
		}
	}

	// Mask token for display
	maskedToken := token
	if len(token) > 8 {
		maskedToken = token[:4] + "..." + token[len(token)-4:]
	}
	results = append(results, checkResult{
		Name:   "Token",
		Status: "ok",
		Detail: fmt.Sprintf("retrieved from %s (%s)", tokenSource, maskedToken),
	})

	if sessionErr != nil {
		results = append(results, checkResult{
			Name:   "OAuth session",
			Status: "fail",
			Detail: fmt.Sprintf("could not read session state: %s", sessionErr.Error()),
		})
	} else if session.IsOAuth {
		results = append(results, oauthSessionCheckResult(session))
	}

	// 6. Environment connectivity
	req, reqErr := http.NewRequest(http.MethodHead, ctx.Environment, nil)
	if reqErr != nil {
		results = append(results, checkResult{
			Name:   "Connectivity",
			Status: "fail",
			Detail: fmt.Sprintf("invalid environment URL %s: %s", ctx.Environment, reqErr.Error()),
		})
	} else {
		resp, connErr := httpClient.Do(req)
		if connErr != nil {
			results = append(results, checkResult{
				Name:   "Connectivity",
				Status: "fail",
				Detail: fmt.Sprintf("cannot reach %s: %s", ctx.Environment, connErr.Error()),
			})
			// No point testing authentication if the server is unreachable
			return results
		} else {
			resp.Body.Close()
			results = append(results, checkResult{
				Name:   "Connectivity",
				Status: "ok",
				Detail: fmt.Sprintf("%s is reachable", ctx.Environment),
			})
		}
	}

	// 7. API authentication
	c, clientErr := newClientFromConfig(ictx, cfg)
	if clientErr != nil {
		results = append(results, checkResult{
			Name:   "Authentication",
			Status: "fail",
			Detail: clientErr.Error(),
		})
		return results
	}

	userInfo, authErr := c.CurrentUser()
	if authErr != nil {
		// /platform/metadata/v1/user requires the 'app-engine:apps:run' scope.
		// Platform tokens that lack it return 403, and unlike OAuth/JWT tokens
		// they can't be JWT-decoded as a fallback (they aren't JWTs). Surface
		// this as a known limitation, not a failure. For OAuth/JWT tokens we
		// fall back to decoding the user ID directly out of the token;
		// CurrentUserID would re-issue the same metadata call we already
		// failed, so go straight to the JWT path.
		if client.IsPlatformToken(token) {
			results = append(results, checkResult{
				Name:   "Authentication",
				Status: "warn",
				Detail: "platform token: user identity unavailable via metadata API (token likely lacks 'app-engine:apps:run' scope; platform tokens are not JWTs, so no fallback)",
			})
		} else if userID, jwtErr := client.ExtractUserIDFromToken(token); jwtErr == nil {
			results = append(results, checkResult{
				Name:   "Authentication",
				Status: "warn",
				Detail: fmt.Sprintf("metadata API unavailable, but token is valid (user: %s)", userID),
			})
		} else {
			results = append(results, checkResult{
				Name:   "Authentication",
				Status: "fail",
				Detail: fmt.Sprintf("API call failed: %s", authErr.Error()),
			})
		}
	} else {
		detail := fmt.Sprintf("authenticated as %s", userInfo.UserID)
		if userInfo.EmailAddress != "" {
			detail = fmt.Sprintf("authenticated as %s (%s)", userInfo.UserID, userInfo.EmailAddress)
		}
		results = append(results, checkResult{
			Name:   "Authentication",
			Status: "ok",
			Detail: detail,
		})
	}

	return results
}

func printDoctorResults(ctx context.Context, results []checkResult) {
	for _, r := range results {
		var icon string
		switch r.Status {
		case "ok":
			icon = output.DoctorOK()
		case "warn":
			icon = output.DoctorWarn()
		case "fail":
			icon = output.DoctorFail()
		}
		fmt.Fprintf(currentStdout(ctx), "%s %-16s %s\n", icon, r.Name, r.Detail)
	}
}

func oauthSessionCheckResult(session *SessionStatus) checkResult {
	if !session.RefreshTokenPresent {
		return checkResult{
			Name:   "OAuth session",
			Status: "warn",
			Detail: "no refresh token; run 'dtctl auth login' to enable automatic refresh",
		}
	}

	accessExpired := session.AccessTokenExpiresAt != nil && time.Now().After(*session.AccessTokenExpiresAt)
	if accessExpired {
		return checkResult{
			Name:   "OAuth session",
			Status: "warn",
			Detail: "access token expired; will refresh on next API call",
		}
	}

	var parts []string
	if session.AccessTokenExpiresAt != nil {
		parts = append(parts, fmt.Sprintf("access expires in %s", time.Until(*session.AccessTokenExpiresAt).Round(time.Second)))
	}
	parts = append(parts, "refresh token present")

	return checkResult{
		Name:   "OAuth session",
		Status: "ok",
		Detail: strings.Join(parts, "; "),
	}
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
