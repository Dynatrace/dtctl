package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/aidetect"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/plugin"
	"github.com/dynatrace-oss/dtctl/pkg/version"
)

// tryPluginDispatch gives an unknown top-level command one shot at plugin
// dispatch (docs/dev/PLUGIN_CONVENTIONS.md): `dtctl foo bar` execs
// the longest dash-joined dtctl-<name> match on PATH (dtctl-foo-bar, then
// dtctl-foo). Built-in commands always win — this only runs from the
// unknown-command error path, and the first word is re-checked against the
// built-in names so a plugin can never capture a built-in's subcommand
// typos (e.g. `dtctl get nosuch` must not exec a dtctl-get binary).
//
// Returns (exitCode, true) when a plugin handled the invocation. On Unix a
// successful dispatch never returns (process replacement); the code path
// returning here means Windows, or an exec failure.
func tryPluginDispatch(ctx context.Context, args []string) (int, bool) {
	// Capability gate: on Unix, dispatch REPLACES this process (syscall.Exec).
	// Embedded callers must never reach it; falling through yields the normal
	// unknown-command error with suggestions instead.
	if !currentCaps(ctx).PluginDispatch {
		return 0, false
	}
	words, rest := splitPluginArgs(args)
	if len(words) == 0 || isBuiltinCommandName(words[0]) {
		return 0, false
	}
	inv, ok := plugin.Resolve(words, rest, exec.LookPath)
	if !ok {
		return 0, false
	}
	// Only the flags dtctl consumed feed the env contract; everything from
	// the command words on belongs to the plugin and must not be reflected
	// (a plugin's own --config must not change DTCTL_CONFIG).
	leading := args[:len(args)-len(words)-len(rest)]
	code, err := execForward(ctx, inv.Path, inv.Args, pluginEnv(leading))
	if err != nil {
		err = fmt.Errorf("plugin %s: %w", inv.Path, err)
		if pluginAgentMode(leading) {
			// Mirror the root error path: machine consumers read the
			// structured envelope from stdout.
			_ = output.PrintError(currentStdout(ctx), errorToDetail(ctx, err))
		} else {
			fmt.Fprintf(currentStderr(ctx), "Error: %v\n", err)
		}
		return 1, true
	}
	return code, true
}

// splitPluginArgs separates dtctl's own leading flags from the command words
// that name a plugin, and everything after them. Leading flags are consumed
// by dtctl (they are reflected into the plugin env contract, not passed
// through); the contiguous run of non-flag tokens that follows are the
// command words; the remainder is passed to the plugin verbatim.
//
//	dtctl --context prod foo bar --baz  →  words [foo bar], rest [--baz]
func splitPluginArgs(args []string) (words, rest []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			break
		}
		i++
		if strings.HasPrefix(a, "--") {
			flagName := a
			if eq := strings.Index(a, "="); eq >= 0 {
				flagName = a[:eq]
			}
			if flagsTakingValues[flagName] && !strings.Contains(a, "=") &&
				i < len(args) && !strings.HasPrefix(args[i], "-") {
				i++ // skip the flag's value token
			}
		} else if shortFlagsTakingValues[a] &&
			i < len(args) && !strings.HasPrefix(args[i], "-") {
			i++ // skip the short flag's value token
		}
	}
	for i < len(args) && !strings.HasPrefix(args[i], "-") {
		words = append(words, args[i])
		i++
	}
	return words, args[i:]
}

// isBuiltinCommandName reports whether name is a built-in dtctl command or
// one of its aliases. Plugins can never shadow these.
func isBuiltinCommandName(name string) bool {
	for _, c := range rootCmd.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return true
		}
	}
	return false
}

// builtinCommandNames returns the set of built-in command names and aliases,
// for plugin-discovery shadow warnings.
func builtinCommandNames() map[string]bool {
	names := make(map[string]bool)
	for _, c := range rootCmd.Commands() {
		names[c.Name()] = true
		for _, a := range c.Aliases {
			names[a] = true
		}
	}
	return names
}

// credentialEnvVars are the environment variables dtctl itself names as token
// carriers (docs/AGENT_MODE.md, `dtctl config init`'s
// ${DT_API_TOKEN} scaffold, and `dtctl account login`/`account status` for the
// account token). They are stripped from a plugin's environment: plugins
// resolve credentials themselves through the sdk (the same config file and
// keyring service), never through inherited env. Tokens placed in user-chosen
// variables referenced via ${...} config expansion cannot be recognized and
// are inherited like the rest of the environment.
var credentialEnvVars = []string{"DTCTL_TOKEN", "DT_API_TOKEN", "DTCTL_ACCOUNT_TOKEN"}

// pluginEnv builds the environment for a plugin exec from dtctl's own leading
// flags: the inherited environment minus credentialEnvVars, plus the
// dtctl→plugin contract variables.
//
// Because dispatch happens on cobra's unknown-command error path, dtctl's
// persistent flags were never parsed — the contract values are derived from
// the raw leading args (and auto-detection), mirroring initConfig.
func pluginEnv(leading []string) []string {
	env := os.Environ()
	for _, key := range credentialEnvVars {
		env = dropEnv(env, key)
	}
	if ctx := rawFlagValue(leading, "--context"); ctx != "" {
		env = overrideEnv(env, "DTCTL_CONTEXT", ctx)
	}
	env = overrideEnv(env, "DTCTL_CONFIG", effectiveConfigPath(rawFlagValue(leading, "--config")))
	agent := pluginAgentMode(leading)
	if agent {
		env = overrideEnv(env, "DTCTL_AGENT", "1")
	}
	if agent || hasRawFlag(leading, "--plain") {
		env = overrideEnv(env, "DTCTL_PLAIN", "1")
	}
	env = overrideEnv(env, "DTCTL_CALLER_VERSION", version.Version)
	return env
}

// pluginAgentMode mirrors initConfig's agent-mode resolution for the
// unparsed leading flags of a plugin invocation.
func pluginAgentMode(leading []string) bool {
	return hasRawFlag(leading, "--agent") || hasShortFlagLetter(leading, 'A') ||
		(aidetect.Detect().Detected && !hasRawFlag(leading, "--no-agent"))
}

// effectiveConfigPath resolves the config file path in effect for the plugin
// contract: the explicit --config value, else a discovered local .dtctl.yaml,
// else the default global path.
func effectiveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if local := config.FindLocalConfig(); local != "" {
		return local
	}
	return config.DefaultConfigPath()
}

// overrideEnv returns env with key set to value, replacing any existing
// entries (duplicate keys in an exec environment are resolved
// inconsistently across platforms, so never append blindly).
func overrideEnv(env []string, key, value string) []string {
	return append(dropEnv(env, key), key+"="+value)
}

// dropEnv returns env without any entries for key.
func dropEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// rawFlagValue extracts a long flag's value from unparsed args, supporting
// both "--flag value" and "--flag=value".
func rawFlagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return a[len(flag)+1:]
		}
	}
	return ""
}

// hasShortFlagLetter reports whether the unparsed args carry the given
// letter in any short-flag cluster (e.g. 'A' in "-A" or "-Av").
func hasShortFlagLetter(args []string, letter rune) bool {
	for _, a := range args {
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			continue
		}
		if strings.ContainsRune(a[1:], letter) {
			return true
		}
	}
	return false
}

// hasRawFlag reports whether the unparsed args carry the given long flag,
// either as "--flag" or "--flag=value".
func hasRawFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}
