package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

// Session is a per-invocation target + credential override for embedded
// callers. When set on RunOptions, the invocation runs against exactly this
// environment and token: the host's config file, contexts, keyring, and
// credential env vars are all out of the picture, which is what a multi-tenant
// service needs — every request brings its own tenant
// (docs/dev/SERVICE_ENGINE_DESIGN.md).
//
// A session replaces credential *resolution*, not command *policy*: blocking
// commands that make no sense service-side (config, ctx, login) is the
// engine's job, on top of this seam.
type Session struct {
	// EnvironmentURL is the Dynatrace environment to run against
	// (e.g. https://abc12345.apps.dynatrace.com). Required.
	EnvironmentURL string
	// Token authenticates every API call of this invocation. Required.
	Token string
	// SafetyLevel bounds mutating operations for this invocation
	// (readonly | readwrite-mine | readwrite-all | dangerously-unrestricted).
	// Empty selects the dtctl default (readwrite-all).
	SafetyLevel config.SafetyLevel
	// MinStability is the stability floor for this invocation: the weakest
	// contract a command or flag may offer and still be reachable
	// (stable | experimental). `development` is rejected — that tier is gated
	// at registration, a stage earlier than the floor, and a session has no
	// opt-in for it.
	//
	// Empty selects SessionDefaultMinStability — `stable`, which is *stricter*
	// than the CLI's default. The difference is deliberate: the CLI's default
	// admits experimental surface because a human or interactive agent reads
	// the `[Experimental]` badge and decides. An embedded caller reads no
	// badge, and a service request is unattended automation by construction —
	// precisely what "may change in any release" is a warning to. A host that
	// wants the wider surface asks for it in this field.
	MinStability config.StabilityLevel
	// StabilityExceptions admit individual below-floor commands and flags,
	// each written as a command path optionally suffixed with one flag
	// ("get breakpoints", "query --decode-snapshots"). Naming a command also
	// admits the flags that are below the floor only because that command is;
	// a flag with a weaker contract of its own needs its own entry.
	//
	// This is the field a host reaches for rather than lowering the floor: a
	// floor of `experimental` grants the entire experimental surface, including
	// flags added to already-allowed stable commands in a later release, which
	// is almost never what a service intended.
	StabilityExceptions []string
	// NoDeprecated makes this invocation refuse every deprecated command and
	// flag, as if the removal release had already happened. It is how a host
	// finds out it still depends on something scheduled to go while that is
	// still a test failure rather than an outage.
	//
	// Orthogonal to MinStability: a deprecated command is stable in shape and
	// passes any floor, it merely has a removal date. Off by default, because
	// a host that has not asked to be broken early should not be.
	NoDeprecated bool
}

// SessionDefaultMinStability is the floor a session-backed invocation gets
// when it names none. See Session.MinStability for why it is stricter than the
// CLI default.
const SessionDefaultMinStability = config.StabilityStable

// sessionContextName names the synthesized context. It appears in
// safety-check messages ("Context 'session' does not allow ...").
const sessionContextName = "session"

// newSessionTokenRef generates a per-invocation unpredictable token reference
// name ("session-<16 hex chars>"). A fixed name like "session" would let a
// host-local credential file entry (e.g. oauth:*:session) shadow the inline
// token even after the keyring is disabled.
func newSessionTokenRef() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failure is exceedingly unlikely; fall back to a fixed
		// suffix that is still distinct from the old constant "session".
		return "session-fallback"
	}
	return "session-" + hex.EncodeToString(buf[:])
}

func (s *Session) validate() error {
	if s.EnvironmentURL == "" {
		return fmt.Errorf("session: EnvironmentURL is required")
	}
	if s.Token == "" {
		return fmt.Errorf("session: Token is required")
	}
	if s.SafetyLevel != "" {
		valid := false
		for _, l := range config.ValidSafetyLevels() {
			if s.SafetyLevel == l {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("session: invalid safety level %q", s.SafetyLevel)
		}
	}
	// Rejected rather than silently defaulted: for a floor, falling back would
	// *widen* the surface the caller asked to restrict.
	//
	// `development` is refused alongside the outright typos, and the message
	// says so rather than leaving it out of the valid list: that tier is gated
	// at registration, one stage earlier than the floor, and a session offers
	// no opt-in for that stage (see sessionScrubbedEnvVars). Accepting it would
	// return an `experimental`-sized surface to a caller who believed it had
	// asked for more.
	if s.MinStability != "" {
		level, err := config.ParseStabilityLevel(string(s.MinStability))
		if err != nil || level == config.StabilityDevelopment {
			return fmt.Errorf("session: invalid minimum stability %q; valid levels are "+
				"experimental, stable (development surface is not reachable from a session)",
				s.MinStability)
		}
	}
	if _, err := stability.ParseExceptions(s.StabilityExceptions); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

// syntheticConfig materializes the session as a single-context in-memory
// config, so everything downstream of LoadConfig (client construction, token
// resolution, safety checks) works unchanged. The token is inline in the
// Tokens list — resolution never touches a keyring or credential store.
func (s *Session) syntheticConfig() *config.Config {
	level := s.SafetyLevel
	if level == "" {
		level = config.DefaultSafetyLevel
	}
	floor := s.MinStability
	if floor == "" {
		floor = SessionDefaultMinStability
	}
	tokenRef := newSessionTokenRef()
	cfg := &config.Config{
		APIVersion:     config.CurrentAPIVersion,
		Kind:           "Config",
		CurrentContext: sessionContextName,
		Contexts: []config.NamedContext{{
			Name: sessionContextName,
			Context: config.Context{
				Environment: s.EnvironmentURL,
				TokenRef:    tokenRef,
				SafetyLevel: level,
				// The floor is materialized on the context rather than left to
				// the resolver's default, so a session-backed run states its
				// contract explicitly — `dtctl commands` then reports it, and
				// an embedded agent can tell "does not exist" from "below the
				// floor this deployment accepts".
				MinStability:        floor,
				StabilityExceptions: s.StabilityExceptions,
				NoDeprecated:        s.NoDeprecated,
			},
		}},
		Tokens: []config.NamedToken{{
			Name:  tokenRef,
			Token: s.Token,
		}},
	}
	// The inline token must win unconditionally; no host credential store should
	// be consulted for a synthetic session config.
	cfg.SealInlineCredentials()
	return cfg
}

// runSession holds the active invocation's session override. Guarded by runMu
// like the rest of the per-invocation state; nil means normal CLI behavior
// (config file + context resolution).
var runSession *Session

// sessionScrubbedEnvVars are unset for the duration of a session-backed
// invocation, so host-level credentials and preferences cannot leak into a
// tenant's request. DTCTL_TOKEN/DT_API_TOKEN/DTCTL_ACCOUNT_TOKEN mirror
// credentialEnvVars (plugin_dispatch.go); DTCTL_CONFIG/DTCTL_CONTEXT would
// repoint config discovery, which a session fully replaces; DTCTL_PROFILE and
// DTCTL_OUTPUT would let the host's preferences shape the request's command
// surface and output format (per-request values are set explicitly via
// RunOptions.Env, which applies after this scrub).
//
// The rest are here because a request's *output bytes* must not depend on the
// host's environment: FORCE_COLOR would inject ANSI escapes into every
// response, NO_COLOR is scrubbed alongside it so colour resolves from the
// invocation alone, and DTCTL_SPILL/DTCTL_SPILL_DIR would re-enable spilling
// (or redirect it) behind the HostDiskSpill capability's back.
// DTCTL_TOKEN_STORAGE selects the credential backend (keyring vs. file); a
// request must never reach any host credential store, so this is scrubbed too.
//
// DTCTL_MIN_STABILITY and DTCTL_DEVELOPMENT (with the deprecated
// DTCTL_EXPERIMENTAL_* aliases) are scrubbed for the same reason as
// DTCTL_PROFILE: they shape which commands exist for the invocation, and that
// is the request's decision, not the host process's. Without the scrub a
// variable the host set for its own CLI use would silently widen or narrow
// every tenant's surface, and DTCTL_DEVELOPMENT could register unfinished
// commands into a request that never asked for them. The floor and its
// exceptions are per-request fields on Session instead; there is deliberately
// no request field for development features — a service does not expose
// surface that carries no contract at all.
var sessionScrubbedEnvVars = []string{
	"DTCTL_TOKEN", "DT_API_TOKEN", "DTCTL_ACCOUNT_TOKEN",
	"DTCTL_CONFIG", "DTCTL_CONTEXT", "DTCTL_PROFILE", "DTCTL_OUTPUT",
	"FORCE_COLOR", "NO_COLOR", "DTCTL_SPILL", "DTCTL_SPILL_DIR",
	"DTCTL_TOKEN_STORAGE",
	config.MinStabilityEnvVar, config.DevelopmentEnvVar,
	// Scrubbed for the same reason as the floor: whether a request sees
	// deprecated surface is the request's decision. Left unscrubbed, a host
	// that had set it for its own CLI use would break every tenant's request
	// the moment one of them used a dated command.
	config.NoDeprecatedEnvVar,
	"DTCTL_EXPERIMENTAL_ACCOUNT", "DTCTL_EXPERIMENTAL_SERVE",
}

// applyRunEnvironment installs the invocation's session and environment
// variables and returns a cleanup restoring the previous process state. For a
// serialized invocation the env mutations are process-wide, which is safe
// because it holds runMu exclusively — but a host application reading the
// same variables concurrently from other goroutines will observe them; hosts
// that care should not share the process with unrelated env consumers. A
// concurrent invocation mutates nothing process-wide (below).
//
// Order matters: session scrubbing first, then opts.Env, so an embedding
// caller can explicitly re-grant a variable the scrub removed.
func applyRunEnvironment(ctx context.Context, opts RunOptions) (cleanup func(), err error) {
	if opts.Session != nil {
		if err := opts.Session.validate(); err != nil {
			return nil, err
		}
	}

	// A concurrent invocation records its
	// environment on itself instead of calling os.Setenv, because the process
	// environment is shared and a scrub applied for one tenant would blank the
	// variable for every other in-flight request. The overlay is read back by
	// cmd.getenv/lookupEnv, and by sdk/session through the Config each
	// invocation gets (withInvocationEnv); a DTCTL_* read through os.Getenv
	// anywhere else sees the host's value instead
	// (TestConcurrentRequestDecidesItsOwnSurface).
	inv := current(ctx)
	concurrent := inv != nil && inv.concurrent
	if concurrent && inv.env == nil {
		inv.env = make(map[string]envValue, len(sessionScrubbedEnvVars)+len(opts.Env)+1)
	}

	var saved []envSnapshot
	set := func(key, value string) {
		if concurrent {
			inv.env[key] = envValue{value: value, present: true}
			return
		}
		saved = append(saved, snapshotEnv(key))
		_ = os.Setenv(key, value)
	}
	unset := func(key string) {
		if concurrent {
			inv.env[key] = envValue{}
			return
		}
		saved = append(saved, snapshotEnv(key))
		_ = os.Unsetenv(key)
	}

	if opts.Session != nil {
		for _, key := range sessionScrubbedEnvVars {
			unset(key)
		}
		// The synthetic config carries the token inline; keep resolution away
		// from the host OS keyring entirely (deterministic, and a service must
		// never read or populate host credential stores on a tenant's behalf).
		set(config.EnvDisableKeyring, "1")
	}
	for key, value := range opts.Env {
		set(key, value)
	}

	if concurrent {
		// The session already lives on the invocation (set in Run); the
		// filesystem joins it, and vfsEnv hands each consumer this invocation's
		// own rather than whatever the process has installed globally.
		inv.fs = opts.FS
		return func() {}, nil
	}

	runSession = opts.Session
	prevFS := vfs.SetActive(opts.FS)
	return func() {
		vfs.SetActive(prevFS)
		runSession = nil
		// Restore in reverse so an opts.Env entry that overrode a scrub
		// unwinds through the scrub back to the original host value.
		for i := len(saved) - 1; i >= 0; i-- {
			saved[i].restore()
		}
	}, nil
}

type envSnapshot struct {
	key     string
	value   string
	present bool
}

func snapshotEnv(key string) envSnapshot {
	value, present := os.LookupEnv(key)
	return envSnapshot{key: key, value: value, present: present}
}

func (e envSnapshot) restore() {
	if e.present {
		_ = os.Setenv(e.key, e.value)
	} else {
		_ = os.Unsetenv(e.key)
	}
}
