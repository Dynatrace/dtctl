package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// resolveAlias checks if the first argument is an alias and expands it.
// Returns (expanded args, isShellAlias, error).
// If no alias matches, returns (nil, false, nil).
func resolveAlias(ctx context.Context, args []string, cfg *config.Config) ([]string, bool, error) {
	if len(args) == 0 || cfg == nil {
		return nil, false, nil
	}

	// Skip if the first arg is a flag
	if strings.HasPrefix(args[0], "-") {
		return nil, false, nil
	}

	// Aliases from an auto-discovered local .dtctl.yaml are untrusted and never
	// honored — they can run arbitrary commands. The warning is emitted once in
	// executeArgs() via cfg.IgnoredExecKeys(); here we simply decline to expand.
	if cfg.IsLocal() {
		return nil, false, nil
	}

	name := args[0]
	expansion, ok := cfg.GetAlias(name)
	if !ok {
		return nil, false, nil
	}

	// Defense in depth: never let an alias shadow a real built-in command at
	// resolution time. The built-in guard in SetAlias/ImportAliases only runs
	// when an alias is created through dtctl; a hand-written config (e.g. a
	// planted .dtctl.yaml) can still define an alias named after a built-in
	// such as `get`, `apply`, or `version`. Refusing it here ensures the real
	// command always wins regardless of where the alias came from. We warn and
	// fall through to normal command dispatch rather than erroring out.
	if isBuiltinCommand(name) {
		fmt.Fprintf(currentStderr(ctx),
			"warning: ignoring alias %q because it shadows the built-in %q command\n",
			name, name)
		return nil, false, nil
	}

	// Shell alias: starts with !
	if strings.HasPrefix(expansion, "!") {
		shellCmd := expansion[1:]
		// Append extra args
		if len(args) > 1 {
			shellCmd += " " + strings.Join(args[1:], " ")
		}
		return []string{shellCmd}, true, nil
	}

	// Regular alias: split and substitute positional params
	parts := splitCommand(expansion)
	extraArgs := args[1:]

	// Substitute $1..$9
	maxUsed := 0
	for i, part := range parts {
		parts[i] = substituteParams(part, extraArgs, &maxUsed)
	}

	// Append unconsumed args (those beyond the highest $N used)
	if maxUsed < len(extraArgs) {
		parts = append(parts, extraArgs[maxUsed:]...)
	}

	// Validate: if $N was used, require that many args
	if maxUsed > len(extraArgs) {
		return nil, false, fmt.Errorf(
			"alias %q requires at least %d argument(s) ($1-$%d), got %d",
			name, maxUsed, maxUsed, len(extraArgs))
	}

	return parts, false, nil
}

// substituteParams replaces $1..$9 in s with values from args.
// Tracks the highest parameter index used.
func substituteParams(s string, args []string, maxUsed *int) string {
	re := regexp.MustCompile(`\$(\d)`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		idx, _ := strconv.Atoi(match[1:])
		if idx > *maxUsed {
			*maxUsed = idx
		}
		if idx >= 1 && idx <= len(args) {
			return args[idx-1]
		}
		return match // leave unreplaced if not enough args
	})
}

// splitCommand splits a command string respecting quotes.
func splitCommand(s string) []string {
	var parts []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case ch == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	// Return empty slice instead of nil
	if parts == nil {
		return []string{}
	}
	return parts
}

// execShellAlias runs a shell alias via sh -c.
func execShellAlias(ctx context.Context, shellCmd string) error {
	cmd := exec.Command("sh", "-c", shellCmd)
	cmd.Stdin = os.Stdin
	cmd.Stdout = currentStdout(ctx)
	cmd.Stderr = currentStderr(ctx)
	return cmd.Run()
}
