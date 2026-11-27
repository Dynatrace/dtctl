package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/prompt"
	"github.com/Dynatrace/dtctl/pkg/stability"
	"github.com/Dynatrace/dtctl/sdk/urls"
)

// authCheckKeyringFunc and authEnsureKeyringFunc are the functions used to
// probe and recover the keyring in auth login. They default to the real
// implementations and can be overridden in tests.
var (
	authCheckKeyringFunc  = config.CheckKeyring
	authEnsureKeyringFunc = config.EnsureKeyringCollection
)

// Hooks for the one-time file-storage consent prompt in auth login. They
// default to the real terminal checks on the invocation's streams and can be
// overridden in tests.
var (
	authIsInteractiveFunc = func(ctx context.Context) bool {
		in, ok := currentStdin(ctx).(*os.File)
		return !getPlainMode(ctx) && !getAgentMode(ctx) && ok && isTerminal(in) && writerIsTerminal(currentStderr(ctx))
	}
	authConfirmFunc = func(ctx context.Context, message string) bool {
		return prompt.ConfirmWith(currentStdin(ctx), currentStdout(ctx), message)
	}
)

// offerFileTokenStorage handles a keyring failure that file storage can solve.
// It returns true when file storage is in place for this login, either because
// the user already chose it or because they just agreed to it.
//
// It never switches silently: tokens in a file are a weaker store than a
// keyring, so the user decides, and only when the machine has no keyring at all
// (a locked or broken keyring stays an error) and a person is there to answer.
// The answer is remembered, so the question is asked once per machine.
func offerFileTokenStorage(ctx context.Context, keyringErr error) bool {
	stderr := currentStderr(ctx)
	if config.IsFileTokenStorage() {
		output.FprintWarning(stderr, "Keyring unavailable; using file-based token storage (%s)", config.OAuthStorageBackend())
		output.FprintWarning(stderr, "Tokens will be stored in plaintext. Ensure only you can read the file.")
		return true
	}
	if !config.IsKeyringAbsent(keyringErr) || !authIsInteractiveFunc(ctx) {
		return false
	}
	output.FprintWarning(stderr, "No system keyring found on this machine (%v)", keyringErr)
	if !authConfirmFunc(ctx, fmt.Sprintf("Store OAuth tokens in %s instead? (owner-only, not encrypted)", config.OAuthStorageBackend())) {
		return false
	}
	if err := config.PersistFileTokenStorage(); err != nil {
		output.FprintWarning(stderr, "Could not remember this choice (%v); set %s=file to repeat it", err, config.EnvTokenStorage)
		// Token storage is resolved process-wide (sdk/session reads the process
		// environment), so this is the only way to apply it to this login. It is
		// reachable only from a terminal, which an embedded invocation never has.
		_ = os.Setenv(config.EnvTokenStorage, "file")
		return true
	}
	output.FprintInfo(stderr, "Remembered. To undo: delete %s, or set %s=keyring", config.FileTokenStorageConsentPath(), config.EnvTokenStorage)
	return true
}

// tokenStorageUnavailableSuggestions lists the ways out when no token storage
// could be set up, most relevant first.
func tokenStorageUnavailableSuggestions(ctx context.Context, keyringErr error, contextName, environment string) []string {
	var s []string
	if config.IsKeyringAbsent(keyringErr) {
		s = append(s,
			fmt.Sprintf("No keyring on this machine: run `dtctl auth login` in a terminal to be asked once, or set %s=file", config.EnvTokenStorage))
	} else {
		s = append(s,
			fmt.Sprintf("Unlock or repair the keyring, or set %s=file to use file-based token storage", config.EnvTokenStorage))
	}
	s = append(s,
		fmt.Sprintf("Or skip OAuth with a platform token: dtctl config set-context %s --environment %q --token-ref my-token", contextName, environment),
		"  dtctl config set-credentials my-token --token <YOUR_PLATFORM_TOKEN>",
		"Token scopes: dtctl help token-scopes",
	)
	if isKeyringDisabled(ctx) {
		s = append(s, fmt.Sprintf("Unset %s if it was set unintentionally", config.EnvDisableKeyring))
	}
	return s
}

func isKeyringDisabled(ctx context.Context) bool { return getenv(ctx, config.EnvDisableKeyring) != "" }

// authClientCredentialsFunc performs the client credentials grant during
// auth login. It defaults to the real implementation and can be overridden in
// tests to exercise the non-interactive branch without a token endpoint.
var authClientCredentialsFunc = func(ctx context.Context, flow *auth.OAuthFlow, clientID, clientSecret, resource string, scopes []string) (*auth.TokenSet, error) {
	return flow.ClientCredentials(ctx, clientID, clientSecret, resource, scopes)
}

// authBrowserFlowFunc runs the interactive browser login during auth login. It
// defaults to the real flow, which opens the system browser and waits for the
// redirect; tests that drive `auth login` past the keyring gate override it so
// `go test` never opens a browser against the real SSO.
var authBrowserFlowFunc = func(ctx context.Context, flow *auth.OAuthFlow) (*auth.TokenSet, error) {
	return flow.Start(ctx)
}

// authNewOAuthFlowFunc builds the OAuth flow from the configuration auth login
// composed. It defaults to the real constructor; tests override it to read the
// scopes a login is about to request, which the flow itself does not expose.
var authNewOAuthFlowFunc = auth.NewOAuthFlow

// authCmd represents the auth command
var authCmd = newAuthCmd()

func newAuthCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication and user identity",
		Long:  `View authentication information and test permissions.`,
	}
	stability.MarkStable(c)
	return c
}

// WhoamiResult contains the current user information for output
type WhoamiResult struct {
	UserID       string `json:"userId" yaml:"userId"`
	UserName     string `json:"userName,omitempty" yaml:"userName,omitempty"`
	EmailAddress string `json:"emailAddress,omitempty" yaml:"emailAddress,omitempty"`
	Context      string `json:"context" yaml:"context"`
	Environment  string `json:"environment" yaml:"environment"`
}

// SessionStatus summarizes OAuth token state for display by `auth status` and `doctor`.
type SessionStatus struct {
	Context               string     `json:"context" yaml:"context"`
	Environment           string     `json:"environment" yaml:"environment"`
	IsOAuth               bool       `json:"isOAuth" yaml:"isOAuth"`
	Storage               string     `json:"storage,omitempty" yaml:"storage,omitempty"`
	AccessTokenPresent    bool       `json:"accessTokenPresent" yaml:"accessTokenPresent"`
	AccessTokenExpiresAt  *time.Time `json:"accessTokenExpiresAt,omitempty" yaml:"accessTokenExpiresAt,omitempty"`
	RefreshTokenPresent   bool       `json:"refreshTokenPresent" yaml:"refreshTokenPresent"`
	RefreshTokenExpiresAt *time.Time `json:"refreshTokenExpiresAt,omitempty" yaml:"refreshTokenExpiresAt,omitempty"`
	GrantedScopes         []string   `json:"grantedScopes,omitempty" yaml:"grantedScopes,omitempty"`

	// grantedScopesPartial is set when GrantedScopes came from the access
	// token's own scope claim, an audience-reduced subset of the grant. Such a
	// list is fine to display but cannot prove a scope is missing.
	grantedScopesPartial bool

	// storage is the store the token was actually read from (Storage is its
	// label). doctor compares it with the keyring probe, which only tests reads
	// and so cannot see a keyring that refused the write.
	storage auth.TokenStorage
}

// buildSessionStatusFunc builds a SessionStatus for a given context + token name.
// Overridable in tests.
var buildSessionStatusFunc = buildSessionStatus

func buildSessionStatus(contextName string, ctx *config.Context, tokenName string) (*SessionStatus, error) {
	status := &SessionStatus{
		Context:     contextName,
		Environment: ctx.Environment,
	}

	if tokenName == "" {
		return status, nil
	}

	oauthConfig := auth.OAuthConfigFromEnvironmentURLWithSafety(ctx.Environment, ctx.SafetyLevel)
	tokenManager, err := auth.NewTokenManager(oauthConfig)
	if err != nil {
		return nil, err
	}

	stored, storage, err := tokenManager.GetTokenInfoWithStorage(tokenName)
	if err != nil || stored == nil {
		// Not an OAuth token (e.g. platform token) or not stored yet.
		return status, nil
	}

	status.IsOAuth = true
	// Report the store the token was found in, not the keyring probe: a keyring
	// that answers reads but refused the write leaves the token in the file
	// store while the probe still succeeds (#393).
	status.storage = storage
	status.Storage = storage.Label()
	status.AccessTokenPresent = stored.AccessToken != ""
	if !stored.ExpiresAt.IsZero() {
		t := stored.ExpiresAt
		status.AccessTokenExpiresAt = &t
	}
	status.RefreshTokenPresent = stored.RefreshToken != ""

	if exp, ok := auth.DecodeRefreshTokenExpiry(stored.RefreshToken); ok {
		status.RefreshTokenExpiresAt = &exp
	}

	scopes := strings.Fields(stored.Scope)
	if len(scopes) == 0 {
		// Compact keyring storage may drop the scope string to fit size limits
		// while keeping the (larger) access token cached. Prefer the scope
		// companion entry, which preserves the full granted scope list; fall
		// back to the access token's own scope claim (an audience-reduced
		// subset) only if no companion exists.
		if cached := tokenManager.CachedScopes(tokenName); len(cached) > 0 {
			scopes = cached
		} else if stored.AccessToken != "" {
			scopes = auth.ExtractJWTScopes(stored.AccessToken)
			status.grantedScopesPartial = true
		}
	}
	if len(scopes) > 0 {
		status.GrantedScopes = scopes
	}

	return status, nil
}

// authWhoamiCmd shows current user identity
var authWhoamiCmd = newAuthWhoamiCmd()

func newAuthWhoamiCmd() *cobra.Command {
	var idOnly bool
	var refresh bool
	c := &cobra.Command{
		Use:   "whoami",
		Short: "Display the current user identity",
		Long: `Display information about the currently authenticated user.

This command shows the user ID, name, and email address associated with
the current authentication token. It also displays the active context
and environment.

The user information is retrieved from the Dynatrace metadata API.
If that fails (e.g., missing scope), it falls back to decoding the
JWT token's 'sub' claim.`,
		Example: `  # View current user info
  dtctl auth whoami

  # Get just the user ID (useful for scripting)
  dtctl auth whoami --id-only

  # Output as JSON
  dtctl auth whoami -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			ctx, err := cfg.CurrentContextObj()
			if err != nil {
				return fmt.Errorf("failed to get current context: %w", err)
			}

			c, err := newClientFromConfig(cmdContext(cmd), cfg)
			if err != nil {
				return fmt.Errorf("failed to create client: %w", err)
			}

			// If --id-only, just get the user ID
			if idOnly {
				userID, err := c.CurrentUserID()
				if err != nil {
					return fmt.Errorf("failed to get user ID: %w", err)
				}
				fmt.Fprintln(currentStdout(cmdContext(cmd)), userID)
				return nil
			}

			// Try to get full user info from metadata API
			userInfo, err := c.CurrentUser()
			if err != nil {
				// Fallback to JWT decoding for user ID only
				userID, jwtErr := client.ExtractUserIDFromToken(cfg.MustGetToken(ctx.TokenRef))
				if jwtErr != nil {
					return fmt.Errorf("failed to get user info: %w (JWT fallback also failed: %v)", err, jwtErr)
				}
				userInfo = &client.UserInfo{
					UserID: userID,
				}
			}

			result := WhoamiResult{
				UserID:       userInfo.UserID,
				UserName:     userInfo.UserName,
				EmailAddress: userInfo.EmailAddress,
				Context:      cfg.CurrentContext,
				Environment:  ctx.Environment,
			}

			printer := newPrinterCtx(cmdContext(cmd))

			// For table output, use a custom format
			if outputFormat(cmdContext(cmd)) == "table" || outputFormat(cmdContext(cmd)) == "" {
				const w = 13
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "User ID:", w, "%s", result.UserID)
				if result.UserName != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "User Name:", w, "%s", result.UserName)
				}
				if result.EmailAddress != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Email:", w, "%s", result.EmailAddress)
				}
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Context:", w, "%s", result.Context)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Environment:", w, "%s", result.Environment)
				return nil
			}

			return printer.Print(result)
		},
	}
	c.Flags().BoolVar(&idOnly, "id-only", false, "output only the user ID")
	c.Flags().BoolVar(&refresh, "refresh", false, "force refresh of cached user info")
	stability.MarkStable(c)
	return c
}

// authStatusCmd shows OAuth session health for the current context
var authStatusCmd = newAuthStatusCmd()

func newAuthStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Display OAuth session status and token health",
		Long: `Display the OAuth session state for the current context.

Shows whether an access token is stored, when it expires, whether a
refresh token is present (so the CLI can automatically refresh expired
access tokens), and the scopes granted to the session.

For platform tokens (non-OAuth), reports the auth type and skips
OAuth-specific fields.`,
		Example: `  # Show session status for the current context
  dtctl auth status

  # Output as JSON
  dtctl auth status -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if cfg.CurrentContext == "" {
				return fmt.Errorf("no current context set")
			}

			ctx, err := cfg.CurrentContextObj()
			if err != nil {
				return fmt.Errorf("failed to get current context: %w", err)
			}

			status, err := buildSessionStatusFunc(cfg.CurrentContext, ctx, ctx.TokenRef)
			if err != nil {
				return fmt.Errorf("failed to build session status: %w", err)
			}

			if outputFormat(cmdContext(cmd)) == "table" || outputFormat(cmdContext(cmd)) == "" {
				printSessionStatusTable(cmdContext(cmd), status)
				return nil
			}

			return newPrinterCtx(cmdContext(cmd)).Print(status)
		},
	}
	stability.MarkStable(c)
	return c
}

func printSessionStatusTable(ctx context.Context, status *SessionStatus) {
	const w = 17
	output.FprintDescribeKV(currentStdout(ctx), "Context:", w, "%s", status.Context)
	output.FprintDescribeKV(currentStdout(ctx), "Environment:", w, "%s", status.Environment)

	if !status.IsOAuth {
		output.FprintDescribeKV(currentStdout(ctx), "Auth type:", w, "%s", "platform token")
		return
	}

	output.FprintDescribeKV(currentStdout(ctx), "Auth type:", w, "%s", "OAuth")
	if status.Storage != "" {
		output.FprintDescribeKV(currentStdout(ctx), "Storage:", w, "%s", status.Storage)
	}

	output.FprintDescribeKV(currentStdout(ctx), "Access token:", w, "%s", accessTokenSummary(status))
	output.FprintDescribeKV(currentStdout(ctx), "Refresh token:", w, "%s", refreshTokenSummary(status))

	if !status.RefreshTokenPresent {
		fmt.Fprintln(currentStderr(ctx))
		output.FprintWarning(currentStderr(ctx), "No refresh token — run 'dtctl auth login' to enable automatic token refresh")
	}
}

// accessTokenSummary returns a human-readable one-liner for the access token state.
func accessTokenSummary(status *SessionStatus) string {
	now := time.Now()

	// No token at all and no refresh token to recover from.
	if !status.AccessTokenPresent && !status.RefreshTokenPresent {
		return "not present — run 'dtctl auth login'"
	}

	// Access token JWT is not cached locally (e.g. medium-compact keyring storage
	// dropped it to fit the OS keyring size limit). The refresh token — confirmed
	// present by the guard above — will be used to mint a new access token on the
	// next API call. Don't claim "valid" here: accessTokenPresent is false in the
	// structured output, so the two would contradict each other.
	if !status.AccessTokenPresent {
		if status.AccessTokenExpiresAt != nil {
			return fmt.Sprintf("not cached locally (previous expiry %s; will refresh on next call)",
				status.AccessTokenExpiresAt.Format(time.RFC3339))
		}
		return "not cached locally (will refresh on next call)"
	}

	// Access token is cached locally — report expiry when we know it.
	if status.AccessTokenExpiresAt != nil {
		remaining := status.AccessTokenExpiresAt.Sub(now).Round(time.Second)
		if remaining > 0 {
			return fmt.Sprintf("valid for %s (expires %s)",
				remaining, status.AccessTokenExpiresAt.Format(time.RFC3339))
		}
		if status.RefreshTokenPresent {
			return "expired"
		}
		return fmt.Sprintf("expired at %s — run 'dtctl auth login'",
			status.AccessTokenExpiresAt.Format(time.RFC3339))
	}

	// Access token is cached but we don't know when it expires.
	if status.RefreshTokenPresent {
		return "valid"
	}
	return "present"
}

// refreshTokenSummary returns a human-readable one-liner for the refresh token state.
func refreshTokenSummary(status *SessionStatus) string {
	if !status.RefreshTokenPresent {
		return "not present"
	}
	if status.RefreshTokenExpiresAt == nil {
		return "present"
	}
	remaining := time.Until(*status.RefreshTokenExpiresAt).Round(time.Second)
	if remaining > 0 {
		return fmt.Sprintf("valid for %s (expires %s)",
			remaining, status.RefreshTokenExpiresAt.Format(time.RFC3339))
	}
	return fmt.Sprintf("expired at %s — run 'dtctl auth login'",
		status.RefreshTokenExpiresAt.Format(time.RFC3339))
}

// resolveLoginContext fills in any missing contextName, environment, and tokenName
// values by looking up the named context in cfg.
//
// The key invariant: when contextName is already known (provided via --context),
// the environment and tokenName fallbacks are read from THAT named context — not
// from the currently active context. This prevents a bug where
//
//	dtctl auth login --context <other-context>
//
// would silently overwrite the other context's environment URL with the current
// context's URL.
//
// Returns an error only when contextName cannot be determined (no --context flag
// and no current context is set in cfg).
func resolveLoginContext(cfg *config.Config, contextName, environment, tokenName string) (string, string, string, error) {
	if contextName == "" {
		if cfg.CurrentContext == "" {
			return "", "", "", fmt.Errorf("no current context set")
		}
		contextName = cfg.CurrentContext
	}
	// Resolve missing environment / tokenName from the *named* context.
	if environment == "" || tokenName == "" {
		if nc, err := cfg.GetContext(contextName); err == nil {
			if environment == "" {
				environment = nc.Context.Environment
			}
			if tokenName == "" && nc.Context.TokenRef != "" {
				tokenName = nc.Context.TokenRef
			}
		}
	}
	return contextName, environment, tokenName, nil
}

// finalizeLoginConfig updates cfg after a successful OAuth login: sets the
// context, activates it, and prunes placeholder contexts whose names are in
// placeholderNames (computed from the raw config file before env-var expansion,
// to avoid permanently deleting contexts backed by unset env vars).
func finalizeLoginConfig(cfg *config.Config, contextName, environment, tokenName string, safetyLevel config.SafetyLevel, placeholderNames map[string]bool) {
	cfg.SetContextWithOptions(contextName, environment, tokenName, &config.ContextOptions{
		SafetyLevel: safetyLevel,
	})
	cfg.CurrentContext = contextName
	cfg.PruneEmptyEnvironments(contextName, placeholderNames)
}

// loginLevelSource says where the level a login asks for came from.
type loginLevelSource int

const (
	loginLevelFromFlag    loginLevelSource = iota // --safety-level was given
	loginLevelFromContext                         // the level the context stores
	loginLevelFromDefault                         // a new context, or one that stores no level
)

// loginLevels are the safety levels a login works with.
type loginLevels struct {
	// saved is written to the context. Empty leaves the context's file as it
	// is, which is the case for an existing context that stores no level.
	saved config.SafetyLevel
	// declared is the level the flag, the context or the default asks for,
	// and source says which of them.
	declared config.SafetyLevel
	source   loginLevelSource
	// requested is the level dtctl enforces for the context once the login
	// has written it, and so the level whose scopes the login requests.
	requested config.SafetyLevel
}

// loginSafetyLevels works out the levels for a login that stores its token as
// tokenName.
//
// With --safety-level the flag decides the declared level. Without it, a
// context that already exists keeps the level it stores, because logging in
// again is how an expired session is renewed and must not change what the
// context may do; flagLevel, the flag's default, applies to a context that is
// new or stores no level, and is saved only to a new one outside a project
// config.
//
// The scopes follow the level dtctl enforces, not the declared one. The two
// differ in an auto-discovered project config, whose level is clamped to what
// the global config binds to the token (Config.EffectiveSafetyLevelFor): a
// cloned .dtctl.yaml must not be able to make a login ask for more than the
// context is allowed to use. The clamp is looked up for tokenName, the token
// the login writes, whatever token the context stored before, and it applies
// to a new context and to the flag as well.
//
// A level that is not a valid value is refused rather than kept or replaced,
// in the context and in the global binding alike. Keeping it would save and
// print a level dtctl does not enforce, and replacing it would pick a level
// nobody chose. In a project config that covers every context of global, the
// global config's contexts (globalContextsFor), that binds tokenName, whatever
// level the clamp arrives at: dtctl ranks an unknown value with readwrite-all,
// so the clamp alone would return it only to a project context above that
// rank, and for a tie only when it is listed first.
func loginSafetyLevels(cfg *config.Config, global []config.NamedContext, contextName, tokenName string, flagLevel config.SafetyLevel, flagGiven bool) (loginLevels, error) {
	levels := loginLevels{saved: flagLevel, declared: flagLevel, source: loginLevelFromDefault}
	if flagGiven {
		levels.source = loginLevelFromFlag
	} else if nc, err := cfg.GetContext(contextName); err == nil {
		stored := nc.Context.SafetyLevel
		if !stored.IsValid() {
			return loginLevels{}, &diagnostic.Error{
				Operation: "auth login",
				Message: fmt.Sprintf("context %q stores the safety level %q, which is not a valid value (valid values: %s)",
					contextName, stored, validSafetyLevelList()),
				Suggestions: []string{
					fmt.Sprintf("Give the context a valid level, then log in again: dtctl ctx set %s --safety-level <level>", shellWord(contextName)),
					fmt.Sprintf("Or set it with the login: dtctl auth login --context %s --safety-level <level>", shellWord(contextName)),
				},
			}
		}
		levels.saved = stored
		if stored != "" {
			levels.declared = stored
			levels.source = loginLevelFromContext
		}
	} else if cfg.IsLocal() {
		// A new context in a project config stores no level: dtctl enforces
		// the level of the token's global binding there, and the default,
		// written to the file, would declare one nobody chose.
		levels.saved = ""
	}

	var refusal *diagnostic.Error
	for _, nc := range globalContextsBinding(global, tokenName) {
		if nc.Context.SafetyLevel.IsValid() {
			continue
		}
		if refusal == nil {
			refusal = &diagnostic.Error{
				Operation: "auth login",
				Message: fmt.Sprintf("global context %q binds token %q to the safety level %q, which is not a valid value (valid values: %s)",
					nc.Name, tokenName, nc.Context.SafetyLevel, validSafetyLevelList()),
			}
		}
		refusal.Suggestions = append(refusal.Suggestions, fmt.Sprintf("Give the global context a valid level: dtctl ctx set %s --global --safety-level <level>", shellWord(nc.Name)))
	}
	if refusal != nil {
		return loginLevels{}, refusal
	}

	// The declared level is valid, and so is every level the clamp can
	// return in its place.
	levels.requested = cfg.EffectiveSafetyLevelFor(&config.Context{TokenRef: tokenName, SafetyLevel: levels.declared})
	return levels, nil
}

// ceilingNotice explains why the login requests scopes for a lower level than
// the declared one. bound says whether the global config binds tokenName at
// all: a project config whose token it does not bind is held to the default.
func (l loginLevels) ceilingNotice(tokenName string, bound bool) string {
	var declared string
	switch l.source {
	case loginLevelFromFlag:
		declared = fmt.Sprintf("--safety-level %s was given", l.declared)
	case loginLevelFromContext:
		declared = fmt.Sprintf("The context declares %s", l.declared)
	default:
		declared = fmt.Sprintf("The default level is %s", l.declared)
	}
	ceiling := fmt.Sprintf("the global config allows token %q %s", tokenName, l.requested)
	if !bound {
		ceiling = fmt.Sprintf("the global config binds no context to token %q, which limits a project config to %s", tokenName, l.requested)
	}
	return fmt.Sprintf("%s; %s, so the login requests %s scopes.", declared, ceiling, l.requested)
}

// projectLoginScopes narrows the scopes of a browser login in a project
// config to those the global binding of tokenName requests as well, and
// returns that binding's level and what was left out. scopes are those of the
// enforced level.
//
// The levels are ranked, but their scope sets are not nested: readonly
// requests read scopes that readwrite-mine does not. Clamping the level by
// rank therefore still lets a project file that declares readonly obtain
// scopes the owner of a readwrite-mine token never asked for. A context
// clamped to the binding's own level requests exactly the binding's scopes,
// so nothing is left out there, nor outside a project config, nor for any
// pair of levels whose sets are nested.
func projectLoginScopes(cfg *config.Config, tokenName string, scopes []string) (kept, leftOut []string, bound config.SafetyLevel) {
	if !cfg.IsLocal() {
		return scopes, nil, ""
	}
	// The most permissive level, clamped: what is left is the binding's.
	bound = cfg.EffectiveSafetyLevelFor(&config.Context{TokenRef: tokenName, SafetyLevel: config.SafetyLevelDangerouslyUnrestricted})
	boundScopes := auth.GetScopesForSafetyLevel(bound)
	for _, s := range scopes {
		if slices.Contains(boundScopes, s) {
			kept = append(kept, s)
		} else {
			leftOut = append(leftOut, s)
		}
	}
	return kept, leftOut, bound
}

// shellWord returns s as one POSIX shell word, for a command that a message
// suggests: a context name may hold a space, a quote or a dollar sign, and the
// suggestion must still name that context when it is pasted. A name made only
// of characters no shell treats specially stays bare, which is the common case.
func shellWord(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./:@%+=,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// validSafetyLevelList is the list of valid levels as an error message shows it.
func validSafetyLevelList() string {
	levels := config.ValidSafetyLevels()
	names := make([]string, len(levels))
	for i, l := range levels {
		names[i] = string(l)
	}
	return strings.Join(names, ", ")
}

// globalContextsFor returns the contexts of the global config when cfg is an
// auto-discovered project config, whose own list holds the project file's
// contexts only, and nil otherwise. It is best effort, like the bindings the
// config builds from the same file: without a readable global config there is
// nothing to report.
func globalContextsFor(cfg *config.Config) []config.NamedContext {
	if !cfg.IsLocal() {
		return nil
	}
	global, err := config.LoadFrom(config.DefaultConfigPath())
	if err != nil {
		return nil
	}
	return global.Contexts
}

// globalContextsBinding returns the contexts of global, the global config's
// contexts, that bind tokenName: those the clamp of a project config's level
// is built from. The rule is the one of buildGlobalBindings in sdk/session,
// which keeps its bindings to itself: a context binds its token to the host
// of its environment, so one whose environment has no host binds nothing.
func globalContextsBinding(global []config.NamedContext, tokenName string) []config.NamedContext {
	var binding []config.NamedContext
	for _, nc := range global {
		if nc.Context.TokenRef == tokenName && urls.Host(nc.Context.Environment) != "" {
			binding = append(binding, nc)
		}
	}
	return binding
}

// tokenPeer is another context that uses the stored token a login replaces.
type tokenPeer struct {
	label string // the context's name as a message shows it
	// scopes are those a browser login to the context would request: the
	// most the context can get back by logging in after this login.
	scopes []string
	// otherEnvironment says that the context is on another environment of
	// the tier than the login's.
	otherEnvironment bool
}

// contextsSharingToken returns the contexts other than contextName whose
// stored token is the one a login to environment replaces when it stores its
// token as tokenName. Tokens are stored per OAuth tier and name, so a context
// of another tier keeps its own token under the same name, and two
// environments of one tier share one. global holds the contexts of the global
// config when cfg is a project config (globalContextsFor); they are peers
// too, and the login is never one of them.
//
// A peer's scopes are worked out the way its own login would: for a context
// of cfg from the level dtctl enforces for it, narrowed in a project config
// to what the binding of tokenName requests (projectLoginScopes); for a
// global context from its own level, which nothing clamps.
func contextsSharingToken(cfg *config.Config, global []config.NamedContext, contextName, tokenName, environment string) []tokenPeer {
	tier := auth.DetectEnvironment(environment)
	host := urls.Host(environment)
	shares := func(c *config.Context) bool {
		return c.TokenRef == tokenName && auth.DetectEnvironment(c.Environment) == tier
	}
	var peers []tokenPeer
	for i := range cfg.Contexts {
		nc := &cfg.Contexts[i]
		if nc.Name != contextName && shares(&nc.Context) {
			scopes, _, _ := projectLoginScopes(cfg, tokenName, auth.GetScopesForSafetyLevel(cfg.EffectiveSafetyLevelFor(&nc.Context)))
			peers = append(peers, tokenPeer{fmt.Sprintf("%q", nc.Name), scopes, urls.Host(nc.Context.Environment) != host})
		}
	}
	for i := range global {
		nc := &global[i]
		if shares(&nc.Context) {
			peers = append(peers, tokenPeer{fmt.Sprintf("%q (global config)", nc.Name), auth.GetScopesForSafetyLevel(nc.Context.GetEffectiveSafetyLevel()), urls.Host(nc.Context.Environment) != host})
		}
	}
	slices.SortFunc(peers, func(a, b tokenPeer) int { return strings.Compare(a.label, b.label) })
	return peers
}

// peerLabels returns the peers' names as a message shows them.
func peerLabels(peers []tokenPeer) []string {
	labels := make([]string, len(peers))
	for i, p := range peers {
		labels[i] = p.label
	}
	return labels
}

// contextsOnOtherEnvironments names the peers that are not on the login's
// environment. The login replaces their token with one requested for its own
// environment, and dtctl cannot tell whether theirs accepts it, so they are
// named whatever the scopes are.
func contextsOnOtherEnvironments(peers []tokenPeer) []string {
	var names []string
	for _, p := range peers {
		if p.otherEnvironment {
			names = append(names, p.label)
		}
	}
	return names
}

// contextsNarrowedByLogin names the peers on the login's environment whose
// own login would request a scope that is not in scopes, the set this login
// requests. Those contexts lose that scope until one of them logs in again.
// The comparison is by scope set and not by rank: readonly requests read
// scopes that readwrite-mine does not.
func contextsNarrowedByLogin(peers []tokenPeer, scopes []string) []string {
	var names []string
	for _, p := range peers {
		if p.otherEnvironment {
			continue
		}
		if slices.ContainsFunc(p.scopes, func(s string) bool { return !slices.Contains(scopes, s) }) {
			names = append(names, p.label)
		}
	}
	return names
}

// warnSharedToken tells the user, before a browser login replaces a stored
// token, which other contexts use it and what to do about it: narrowed are
// those that lose scopes (contextsNarrowedByLogin), elsewhere those on another
// environment than environment, the login's (contextsOnOtherEnvironments). In
// a project directory (local) a token name of its own is no way out by
// itself: dtctl refuses a token there that the global config does not bind.
func warnSharedToken(w io.Writer, tokenName, environment string, narrowed, elsewhere []string, local bool) {
	if len(narrowed) > 0 {
		whose, lose := "whose safety level requests", "that context loses"
		if len(narrowed) > 1 {
			whose, lose = "whose safety levels request", "those contexts lose"
		}
		output.FprintWarning(w, "Token %q is also used by %s, %s scopes this login does not. The login replaces the token, so %s them.", tokenName, joinNames(narrowed), whose, lose)
	}
	if len(elsewhere) > 0 {
		which := "which is on another environment"
		if len(elsewhere) > 1 {
			which = "which are on other environments"
		}
		output.FprintWarning(w, "Token %q is also used by %s, %s. The login replaces the token with one requested for %s.", tokenName, joinNames(elsewhere), which, environment)
	}

	const boundNameOnly = "A token name of its own (--token-name) works here only once a context in the global config binds that name to this environment."
	switch {
	case len(narrowed) > 0:
		target := "it"
		if len(narrowed) > 1 {
			target = "one of them"
		}
		if local {
			output.FprintHint(w, "Log in to %s afterwards to restore its scopes. %s", target, boundNameOnly)
			return
		}
		output.FprintHint(w, "Log in to %s afterwards to restore its scopes, or give this context its own token with --token-name.", target)
	case len(elsewhere) > 0 && local:
		output.FprintHint(w, "%s", boundNameOnly)
	case len(elsewhere) > 0:
		output.FprintHint(w, "Give this context its own token with --token-name to leave the stored one as it is.")
	}
}

// joinNames joins names the way a sentence lists them: a, b and c.
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// Environment variables that supply client credentials grant parameters. They
// are preferred over the equivalent flags because command line arguments are
// visible to other processes.
const (
	envLoginClientID     = "DTCTL_CLIENT_ID"
	envLoginClientSecret = "DTCTL_CLIENT_SECRET"
	envLoginAccountURN   = "DTCTL_ACCOUNT_URN"
)

// describeMissingClientCredentials names the half of the credential pair that
// was not supplied, so the operator does not have to guess which of the two
// sources (flag or environment variable) failed to reach the command.
func describeMissingClientCredentials(clientID, clientSecret string) string {
	switch {
	case clientID == "" && clientSecret == "":
		return "neither was supplied"
	case clientID == "":
		return "the client ID is missing"
	default:
		return "the client secret is missing"
	}
}

// authLoginCmd initiates browser-based OAuth login
var authLoginCmd = newAuthLoginCmd()

func newAuthLoginCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "login",
		Short: "Authenticate using browser-based OAuth login",
		Long: `Authenticate with Dynatrace using OAuth 2.0 browser-based login.

This command will:
1. Open your default browser to the Dynatrace login page
2. Wait for you to complete authentication
3. Store the OAuth tokens securely in your system keyring (or local file if keyring is unavailable)
4. Configure a context to use the authenticated session

After successful login, you can use dtctl commands without needing to manage API tokens manually.

If --context and --environment are omitted, the current context is used. This is useful
for re-authenticating when both the access token and refresh token have expired.

Safety level:
  A context that already exists keeps its safety level, whether that is below
  or above the default: renewing a session does not change what the context
  may do. Pass --safety-level to change it, and pass it in CI when the level
  must not depend on what the config already holds. A context that stores a
  value that is not a safety level is refused until it has one
  (dtctl ctx set <name> --safety-level <level>, or --safety-level here).

  In a directory with a .dtctl.yaml, dtctl enforces no more for a context than
  the global config grants its token. The login requests the scopes of that
  level, for a new context and with --safety-level too, and says so. It
  leaves out any scope of that level the granted one does not request
  (readonly under a readwrite-mine grant).

  A login replaces the stored token. If another context uses the same token
  and its level needs scopes this login does not request, the login names it
  before the browser opens.

Token storage:
  By default, OAuth tokens are stored in the OS keyring (macOS Keychain, Windows
  Credential Manager, or Linux Secret Service).

  Set DTCTL_TOKEN_STORAGE=file to store tokens in a local file
  (~/.local/share/dtctl/oauth-tokens/) with 0600 permissions. This causes a
  hard cut-over: all reads and writes go to the file store, bypassing the
  keyring entirely. Any token previously stored in the keyring will not be
  read — re-authenticate after switching. Useful for headless systems, WSL,
  containers, or Windows Admin sessions where keyring writes fail.

  On a machine with no keyring at all (typical for headless Linux), a login run
  in a terminal asks once whether to use the file store instead, and remembers
  the answer. dtctl never switches to files silently, and non-interactive runs
  (scripts, CI, --plain, --agent) never ask: set DTCTL_TOKEN_STORAGE=file there.
  To undo a remembered choice, delete the marker file doctor names, or set
  DTCTL_TOKEN_STORAGE=keyring.

If neither keyring nor file storage is available, use API token authentication
instead (dtctl config set-credentials).

Non-interactive login (CI/CD):
  Supplying a client ID and secret switches to the OAuth 2.0 client credentials
  grant, which needs no browser and no user. Prefer the environment variables
  over the flags, since command line arguments are visible to other processes:

    DTCTL_CLIENT_ID, DTCTL_CLIENT_SECRET, DTCTL_ACCOUNT_URN

  This grant authenticates the application itself, so there is no user identity
  and, per RFC 6749 section 4.4.3, no refresh token is issued. Run the command
  again to obtain a new access token when the current one expires.

  --timeout bounds the token request, and --safety-level still gates dtctl
  itself. The safety level does not narrow the token: without --scopes the
  token carries every scope the OAuth client was granted.`,
		Example: `  # Re-authenticate the current context (e.g. after token expiry)
  dtctl auth login

  # Login and create a new context named "my-env"
  dtctl auth login --context my-env --environment https://abc12345.apps.dynatrace.com

  # Login with a specific token name
  dtctl auth login --context my-env --environment https://abc12345.apps.dynatrace.com --token-name my-oauth-token

  # Login with custom timeout
  dtctl auth login --context my-env --environment https://abc12345.apps.dynatrace.com --timeout 5m

  # Non-interactive login for CI/CD (no browser)
  export DTCTL_CLIENT_ID=dt0s02.EXAMPLE
  export DTCTL_CLIENT_SECRET=dt0s02.EXAMPLE.SECRET
  export DTCTL_ACCOUNT_URN=urn:dtaccount:00000000-0000-0000-0000-000000000000
  export DTCTL_TOKEN_STORAGE=file
  dtctl auth login --context ci --environment https://abc12345.apps.dynatrace.com --safety-level readonly`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get flags
			contextName, _ := cmd.Flags().GetString("context")
			environment, _ := cmd.Flags().GetString("environment")
			tokenName, _ := cmd.Flags().GetString("token-name")
			timeoutStr, _ := cmd.Flags().GetString("timeout")
			safetyLevelStr, _ := cmd.Flags().GetString("safety-level")
			clientID, _ := cmd.Flags().GetString("client-id")
			clientSecret, _ := cmd.Flags().GetString("client-secret")
			accountURN, _ := cmd.Flags().GetString("account-urn")
			grantScopes, _ := cmd.Flags().GetStringSlice("scopes")

			// Environment variables are the safer way to supply these in CI: command
			// line flags are visible to every other process via the process table.
			if clientID == "" {
				clientID = os.Getenv(envLoginClientID)
			}
			if clientSecret == "" {
				clientSecret = os.Getenv(envLoginClientSecret)
			}
			if accountURN == "" {
				accountURN = os.Getenv(envLoginAccountURN)
			}

			// Any client credentials input at all selects the non-interactive grant,
			// including the parameters that are useless without the pair. Falling
			// back to the browser flow because half the configuration is missing is
			// the unexplained hang that this command exists to avoid.
			nonInteractive := clientID != "" || clientSecret != "" || accountURN != "" || len(grantScopes) > 0
			if nonInteractive && (clientID == "" || clientSecret == "") {
				return &diagnostic.Error{
					Operation: "auth login",
					Message: fmt.Sprintf("the client credentials grant requires both a client ID and a client secret (%s)",
						describeMissingClientCredentials(clientID, clientSecret)),
					Suggestions: []string{
						fmt.Sprintf("Set both %s and %s", envLoginClientID, envLoginClientSecret),
						"Or pass --client-id and --client-secret",
						fmt.Sprintf("Omit all of --client-id/--client-secret/--account-urn/--scopes (and %s/%s/%s) to use the interactive browser login",
							envLoginClientID, envLoginClientSecret, envLoginAccountURN),
					},
				}
			}

			// Resolve contextName, environment and tokenName from the config when not
			// supplied as explicit flags. A missing --token-name is reason enough:
			// an existing context keeps its token name even when --context and
			// --environment are both given.
			if contextName == "" || environment == "" || tokenName == "" {
				contextHint := "Use 'dtctl ctx' to list available context names, then pass --context <name> --environment <url>"
				cfg, err := loadConfig(cmdContext(cmd))
				if err != nil {
					if contextName == "" {
						return &diagnostic.Error{
							Operation:   "auth login",
							Message:     "--context and --environment are required (no existing config found)",
							Suggestions: []string{contextHint},
							Err:         err,
						}
					}
					// contextName provided but config unreadable — environment must be supplied explicitly.
				} else {
					var resolveErr error
					contextName, environment, tokenName, resolveErr = resolveLoginContext(cfg, contextName, environment, tokenName)
					if resolveErr != nil {
						return &diagnostic.Error{
							Operation:   "auth login",
							Message:     "--context and --environment are required when no current context is set",
							Suggestions: []string{contextHint, "Set a current context with 'dtctl ctx use-context <name>'"},
						}
					}
				}
				// If environment is still empty, the named context is new — --environment must be provided.
				if environment == "" {
					return &diagnostic.Error{
						Operation:   "auth login",
						Message:     fmt.Sprintf("--environment is required: context %q not found in config", contextName),
						Suggestions: []string{contextHint},
					}
				}
			}

			// Default token name to context name if not provided
			if tokenName == "" {
				tokenName = contextName + "-oauth"
			}

			// Parse timeout
			timeout, err := time.ParseDuration(timeoutStr)
			if err != nil {
				return fmt.Errorf("invalid timeout: %w", err)
			}

			// Parse and validate safety level
			safetyLevel := config.SafetyLevel(safetyLevelStr)
			if !safetyLevel.IsValid() {
				// Typed: the agent-mode code of a plain error would be read
				// off the text, which quotes the value.
				return &invalidInputError{err: fmt.Errorf("invalid safety level %q (valid values: %s)", safetyLevelStr, validSafetyLevelList())}
			}

			// Load config
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				// If config doesn't exist, create a new one
				cfg = config.NewConfig()
			}

			// levels.saved is what the context stores from here on;
			// levels.requested is what the login asks the identity provider for.
			globalContexts := globalContextsFor(cfg)
			levels, err := loginSafetyLevels(cfg, globalContexts, contextName, tokenName, safetyLevel, cmd.Flags().Changed("safety-level"))
			if err != nil {
				return err
			}
			scopeLevel := levels.requested

			// Ensure a token storage backend is available before starting OAuth flow.
			// Keyring is preferred; file-based storage is the fallback for headless/WSL/CI environments.
			if keyringErr := authCheckKeyringFunc(); keyringErr != nil {
				recovered := false
				// On Linux/WSL the persistent keyring collection may not exist yet.
				// Attempt to create it — this may trigger an OS password prompt.
				if strings.Contains(keyringErr.Error(), config.ErrMsgCollectionUnlock) {
					output.FprintInfo(currentStderr(cmdContext(cmd)), "No keyring collection found — creating one (you may be prompted for a password)...")
					if initErr := authEnsureKeyringFunc(cmd.Context()); initErr == nil {
						if authCheckKeyringFunc() == nil {
							output.FprintSuccess(currentStderr(cmdContext(cmd)), "Keyring collection created successfully")
							recovered = true
						}
					}
				}
				if !recovered {
					// Keyring is unavailable — use file-based storage if the user has
					// chosen it (or agrees to it now); otherwise explain the way out.
					if !offerFileTokenStorage(cmdContext(cmd), keyringErr) {
						return &diagnostic.Error{
							Operation:   "auth login",
							Message:     fmt.Sprintf("OAuth login requires a token storage backend, but the system keyring is unavailable: %v", keyringErr),
							Suggestions: tokenStorageUnavailableSuggestions(cmdContext(cmd), keyringErr, contextName, environment),
						}
					}
				}
			}

			// Warn about potentially wrong environment URLs
			if problems := diagnostic.CheckEnvironmentURL(environment); len(problems) > 0 {
				for _, p := range problems {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "%s", p.Message)
					if p.SuggestedURL != "" {
						output.FprintHint(currentStderr(cmdContext(cmd)), "Did you mean: %s", p.SuggestedURL)
					}
				}
				fmt.Fprintln(currentStderr(cmdContext(cmd)))
			}

			// Detect environment and create appropriate OAuth config with safety level
			oauthConfig := auth.OAuthConfigFromEnvironmentURLWithSafety(environment, scopeLevel)
			var scopesLeftOut []string
			var boundLevel config.SafetyLevel
			oauthConfig.Scopes, scopesLeftOut, boundLevel = projectLoginScopes(cfg, tokenName, oauthConfig.Scopes)

			// Log which environment we detected
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Detected environment: %s", oauthConfig.Environment)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Safety level: %s", oauthConfig.SafetyLevel)

			// A login replaces the stored token, which other contexts may use.
			peers := contextsSharingToken(cfg, globalContexts, contextName, tokenName, environment)

			// Create OAuth flow
			flow, err := authNewOAuthFlowFunc(oauthConfig)
			if err != nil {
				return fmt.Errorf("failed to initialize OAuth: %w", err)
			}

			// --timeout bounds the whole login attempt, whichever grant is used: an
			// unbounded token request is exactly the CI hang this command avoids.
			ctx, cancel := context.WithTimeout(cmdContext(cmd), timeout)
			defer cancel()

			var tokens *auth.TokenSet
			if nonInteractive {
				// Client credentials grant: no browser, no redirect, no user. Scopes
				// default to whatever the OAuth client was granted unless --scopes
				// narrows them.
				if len(grantScopes) == 0 {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "No --scopes given: the token carries every scope the OAuth client was granted.")
					// The browser flow requests the safety level's scope set at the
					// IdP, so there the two move together. Here they do not, and a
					// narrowing safety level would otherwise read as if it had.
					if scopeLevel != config.SafetyLevelDangerouslyUnrestricted {
						output.FprintWarning(currentStderr(cmdContext(cmd)), "The safety level %s gates dtctl itself; it does not narrow the token.", scopeLevel)
						output.FprintHint(currentStderr(cmdContext(cmd)), "Pass --scopes, or provision the OAuth client with only the scopes this pipeline needs.")
					}
				}
				// The grant's scopes do not follow the safety level, so all there
				// is to say about a shared token is that it is replaced.
				if len(peers) > 0 {
					output.FprintInfo(currentStderr(cmdContext(cmd)), "Token %q is also used by %s. This login replaces it with the OAuth client's token.", tokenName, joinNames(peerLabels(peers)))
				}
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Authenticating with the client credentials grant (no browser)...")
				tokens, err = authClientCredentialsFunc(ctx, flow, clientID, clientSecret, accountURN, grantScopes)
				if err != nil {
					return fmt.Errorf("authentication failed: %w", err)
				}
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Authentication successful!")
				// The endpoint may return fewer scopes than requested, so report what
				// the token actually carries rather than what was asked for.
				if tokens.Scope != "" {
					output.FprintInfo(currentStderr(cmdContext(cmd)), "Granted scopes: %s", tokens.Scope)
				}
			} else {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Requesting OAuth scopes for safety level %s...", oauthConfig.SafetyLevel)
				if scopeLevel != levels.declared {
					output.FprintInfo(currentStderr(cmdContext(cmd)), "%s", levels.ceilingNotice(tokenName, len(globalContextsBinding(globalContexts, tokenName)) > 0))
				}
				if len(scopesLeftOut) > 0 {
					output.FprintInfo(currentStderr(cmdContext(cmd)), "The login requests only scopes that the global binding of token %q (%s) requests too. Left out: %s.", tokenName, boundLevel, strings.Join(scopesLeftOut, ", "))
				}
				// Said before the browser opens, while the login can still be abandoned.
				warnSharedToken(currentStderr(cmdContext(cmd)), tokenName, environment,
					contextsNarrowedByLogin(peers, oauthConfig.Scopes), contextsOnOtherEnvironments(peers), cfg.IsLocal())

				output.FprintInfo(currentStderr(cmdContext(cmd)), "Starting OAuth authentication flow...")
				tokens, err = authBrowserFlowFunc(ctx, flow)
				if err != nil {
					return fmt.Errorf("authentication failed: %w", err)
				}

				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Authentication successful!")

				// Get user info. The client credentials grant authenticates the
				// application itself, so it has no user identity to report.
				userInfo, userErr := flow.GetUserInfo(tokens.AccessToken)
				if userErr != nil {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "Failed to retrieve user info: %v", userErr)
				} else {
					output.FprintInfo(currentStderr(cmdContext(cmd)), "Logged in as: %s (%s)", userInfo.Name, userInfo.Email)
				}
			}

			// Store tokens
			tokenManager, err := auth.NewTokenManager(oauthConfig)
			if err != nil {
				return fmt.Errorf("failed to create token manager: %w", err)
			}

			// Report the store the tokens actually landed in: the keyring probe only
			// tests reads, so a keyring that refused the write still looks
			// available (#393).
			storage, err := tokenManager.SaveTokenWithStorage(tokenName, tokens)
			if err != nil {
				return fmt.Errorf("failed to store tokens: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Tokens stored in %s as '%s'", storage.Label(), tokenName)

			// Identify placeholder contexts from the raw (unexpanded) config.
			// A context is a placeholder if its environment expands to the empty string
			// (either literally empty or an unset env-var reference like ${DT_ENVIRONMENT_URL}).
			placeholderNames := make(map[string]bool)
			if rawCfg, err := loadRawConfig(cmdContext(cmd)); err == nil {
				for _, nc := range rawCfg.Contexts {
					if os.ExpandEnv(nc.Context.Environment) == "" {
						placeholderNames[nc.Name] = true
					}
				}
			}

			finalizeLoginConfig(cfg, contextName, environment, tokenName, levels.saved, placeholderNames)

			// Save config (respects local .dtctl.yaml if present)
			if err := saveConfig(cmdContext(cmd), cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Context '%s' configured and activated", contextName)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "\nYou can now use dtctl commands with this context.")

			return nil
		},
	}
	c.Flags().String("context", "", "name for the context to create or update (defaults to current context)")
	c.Flags().String("environment", "", "Dynatrace environment URL (defaults to current context's environment)")
	c.Flags().String("token-name", "", "name for storing the OAuth token (defaults to existing token name or <context>-oauth)")
	c.Flags().String("timeout", "5m", "timeout for the authentication flow (bounds the browser flow and the client credentials token request)")
	c.Flags().String("safety-level", string(config.DefaultSafetyLevel), "safety level for the context (readonly, readwrite-mine, readwrite-all, dangerously-unrestricted); an existing context keeps its level unless this is given")
	c.Flags().String("client-id", "", "OAuth client ID for the non-interactive client credentials grant (env: "+envLoginClientID+")")
	c.Flags().String("client-secret", "", "OAuth client secret for the client credentials grant; prefer the environment variable (env: "+envLoginClientSecret+")")
	c.Flags().String("account-urn", "", "account URN sent as the resource indicator, e.g. urn:dtaccount:<uuid> (env: "+envLoginAccountURN+")")
	c.Flags().StringSlice("scopes", nil, "scopes to request for the client credentials grant (defaults to the client's own scopes)")
	stability.MarkStable(c)
	// Left out, it defaults from the config; an explicitly empty value (an
	// unset shell variable) must not silently take that default.
	rejectEmptyFlag(c, "environment")
	// Given, it replaces the level an existing context stores; an explicitly
	// empty value must not do that with the default.
	rejectEmptyFlag(c, "safety-level")
	return c
}

// authLogoutCmd logs out and removes OAuth tokens
var authLogoutCmd = newAuthLogoutCmd()

func newAuthLogoutCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "logout [context-name]",
		Short: "Logout and remove OAuth tokens",
		Long: `Remove stored OAuth tokens for a context.

This command will:
1. Remove OAuth tokens from the system keyring
2. Optionally remove the context configuration

If no context name is provided, the current context will be used.`,
		Example: `  # Logout from current context
  dtctl auth logout

  # Logout from specific context
  dtctl auth logout my-env

  # Logout and remove context
  dtctl auth logout my-env --remove-context`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Determine context name
			var contextName string
			if len(args) > 0 {
				contextName = args[0]
			} else {
				contextName = cfg.CurrentContext
			}

			if contextName == "" {
				return fmt.Errorf("no context specified and no current context set")
			}

			// Find context
			ctx, err := cfg.GetContext(contextName)
			if err != nil {
				return fmt.Errorf("context not found: %w", err)
			}

			// Get token name
			tokenName := ctx.Context.TokenRef
			if tokenName == "" {
				return fmt.Errorf("context has no token reference")
			}

			// Detect environment from context URL
			oauthConfig := auth.OAuthConfigFromEnvironmentURLWithSafety(ctx.Context.Environment, ctx.Context.SafetyLevel)

			// Delete OAuth token
			tokenManager, err := auth.NewTokenManager(oauthConfig)
			if err != nil {
				return fmt.Errorf("failed to create token manager: %w", err)
			}

			if err := tokenManager.DeleteToken(tokenName); err != nil {
				output.FprintWarning(currentStderr(cmdContext(cmd)), "Failed to delete token from keyring: %v", err)
			} else {
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Removed OAuth token '%s'", tokenName)
			}

			// Optionally remove context
			removeContext, _ := cmd.Flags().GetBool("remove-context")
			if removeContext {
				if err := cfg.DeleteContext(contextName); err != nil {
					return fmt.Errorf("failed to remove context: %w", err)
				}

				// The in-memory CurrentContext may carry the session-local
				// --context/DTCTL_CONTEXT override, which must never be
				// persisted (LoadConfig's contract). Re-derive the stored value
				// from the file before deciding whether to clear it.
				if raw, err := loadConfigRaw(cmdContext(cmd)); err == nil {
					cfg.CurrentContext = raw.CurrentContext
				}
				// If we deleted the current context, clear it
				if cfg.CurrentContext == contextName {
					cfg.CurrentContext = ""
				}

				if err := saveConfig(cmdContext(cmd), cfg); err != nil {
					return fmt.Errorf("failed to save config: %w", err)
				}

				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Removed context '%s'", contextName)
			}

			return nil
		},
	}
	c.Flags().Bool("remove-context", false, "also remove the context configuration")
	stability.MarkStable(c)
	return c
}

// authRefreshCmd refreshes OAuth tokens
var authRefreshCmd = newAuthRefreshCmd()

func newAuthRefreshCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "refresh [context-name]",
		Short: "Refresh OAuth tokens",
		Long: `Refresh OAuth access tokens using the refresh token.

This command manually triggers a token refresh. Normally, dtctl will
automatically refresh tokens when needed, but this command can be used
to force a refresh.`,
		Example: `  # Refresh tokens for current context
  dtctl auth refresh

  # Refresh tokens for specific context
  dtctl auth refresh my-env`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Determine context name
			var contextName string
			if len(args) > 0 {
				contextName = args[0]
			} else {
				contextName = cfg.CurrentContext
			}

			if contextName == "" {
				return fmt.Errorf("no context specified and no current context set")
			}

			// Find context
			ctx, err := cfg.GetContext(contextName)
			if err != nil {
				return fmt.Errorf("context not found: %w", err)
			}

			// Get token name
			tokenName := ctx.Context.TokenRef
			if tokenName == "" {
				return fmt.Errorf("context has no token reference")
			}

			// Detect environment from context URL
			oauthConfig := auth.OAuthConfigFromEnvironmentURLWithSafety(ctx.Context.Environment, ctx.Context.SafetyLevel)

			// Refresh token
			tokenManager, err := auth.NewTokenManager(oauthConfig)
			if err != nil {
				return fmt.Errorf("failed to create token manager: %w", err)
			}

			output.FprintInfo(currentStderr(cmdContext(cmd)), "Refreshing OAuth tokens...")
			tokens, err := tokenManager.RefreshToken(tokenName)
			if err != nil {
				// A client credentials token has no refresh token by design, so
				// "refresh" is a dead end for it — point at the way out instead of
				// reporting a missing token the operator cannot supply.
				if errors.Is(err, auth.ErrNoRefreshToken) {
					return &diagnostic.Error{
						Operation: "auth refresh",
						Message:   fmt.Sprintf("context %q has no refresh token, so its access token cannot be renewed in place", contextName),
						Suggestions: []string{
							fmt.Sprintf("Run 'dtctl auth login --context %s' to obtain a new access token", contextName),
							"A token from the client credentials grant never carries a refresh token (RFC 6749 section 4.4.3); re-running login is the renewal path",
						},
						Err: err,
					}
				}
				return fmt.Errorf("failed to refresh tokens: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Tokens refreshed")
			output.FprintInfo(currentStderr(cmdContext(cmd)), "New token expires at: %s", tokens.ExpiresAt.Format(time.RFC3339))

			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(authCmd)

	authCmd.AddCommand(authWhoamiCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authRefreshCmd)

	// Flags for whoami
	// Flags for login
	// Flags for logout
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
