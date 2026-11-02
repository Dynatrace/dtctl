# Command Alias Design

**Status:** Design Proposal
**Created:** 2026-02-16
**Author:** dtctl team

## Overview

Command aliases let users define shorthand names for frequently used dtctl commands.
An alias expands to a full dtctl command string before execution, supporting
positional parameters and shell pipelines. Aliases are stored in the user's config
file and managed through a dedicated `dtctl alias` command group.

Aliases are a well-established CLI pattern (git, gh) that reduces repetitive typing
and lets teams share standardized shortcuts.

## Goals

1. **Reduce repetition** -- One-word shortcuts for common multi-flag commands
2. **Parameterization** -- Positional placeholders (`$1`, `$2`) for reusable templates
3. **Shell expansion** -- Pipe dtctl output through jq, grep, etc. via `!` prefix
4. **Team sharing** -- Import/export aliases as YAML for team-wide consistency
5. **Safety** -- Aliases cannot shadow built-in commands

## Non-Goals

- Alias chaining (alias referencing another alias)
- Context-scoped aliases (all aliases are global)
- Interactive alias builder / wizard
- Auto-generated aliases from usage patterns

---

## User Experience

### Managing Aliases

```bash
# Set a simple alias
dtctl alias set my-wf "get workflows --context=production"

# Use it
dtctl my-wf
# => dtctl get workflows --context=production

# Set an alias with positional parameters
dtctl alias set wf-logs 'query "fetch logs | filter workflow.id == \"$1\"" --context=production'

# Use it -- $1 is replaced with the argument
dtctl wf-logs abc-123
# => dtctl query "fetch logs | filter workflow.id == \"abc-123\"" --context=production

# Set a shell alias (! prefix)
dtctl alias set wf-count '!dtctl get workflows -o json | jq length'

# Use it -- executed through sh
dtctl wf-count
# => 42

# List all aliases
dtctl alias list

# Delete an alias
dtctl alias delete my-wf

# Delete multiple aliases
dtctl alias delete my-wf wf-logs wf-count
```

### Import and Export

```bash
# Export all aliases to a file
dtctl alias export -f team-aliases.yaml

# Import aliases from a file (merges with existing)
dtctl alias import -f team-aliases.yaml

# Import with overwrite confirmation
dtctl alias import -f team-aliases.yaml
# Alias "deploy" already exists. Overwrite? [y/N]

# Force overwrite without prompting
dtctl alias import -f team-aliases.yaml --overwrite
```

### Example alias file (`team-aliases.yaml`)

```yaml
aliases:
  prod-wf: "get workflows --context=production"
  staging-dash: "get dashboards --context=staging"
  deploy: 'apply -f deployments/ --context=$1'
  wf-count: "!dtctl get workflows -o json | jq length"
```

### List output

```
$ dtctl alias list
NAME          EXPANSION
deploy        apply -f deployments/ --context=$1
prod-wf       get workflows --context=production
staging-dash  get dashboards --context=staging
wf-count      !dtctl get workflows -o json | jq length
```

### Error Cases

```bash
# Cannot shadow a built-in command
$ dtctl alias set get "describe workflows"
Error: "get" is a built-in command and cannot be used as an alias name

# Cannot shadow a built-in subcommand path
$ dtctl alias set config "get workflows"
Error: "config" is a built-in command and cannot be used as an alias name

# Unknown alias
$ dtctl nonexistent
Error: unknown command "nonexistent"

Did you mean one of these?
  get
  describe

# Missing required positional argument
$ dtctl alias set greet 'query "hello $1"'
$ dtctl greet
Error: alias "greet" requires at least 1 argument ($1), got 0
```

---

## Technical Design

### Storage

Aliases are stored in the existing config file under a top-level `aliases` key:

```yaml
apiVersion: v1
kind: Config
current-context: production
contexts: [...]
tokens: [...]
preferences:
  output: table
  editor: vim
aliases:
  prod-wf: "get workflows --context=production"
  deploy: "apply -f deployments/ --context=$1"
  wf-count: "!dtctl get workflows -o json | jq length"
```

This keeps aliases co-located with the rest of the configuration and means they
benefit from the existing local-config (`.dtctl.yaml`) vs global-config precedence.
Project-local `.dtctl.yaml` files can define project-specific aliases.

### Config Struct Change

```go
// pkg/config/config.go

type Config struct {
    APIVersion     string            `yaml:"apiVersion"`
    Kind           string            `yaml:"kind"`
    CurrentContext string            `yaml:"current-context"`
    Contexts       []NamedContext    `yaml:"contexts"`
    Tokens         []NamedToken      `yaml:"tokens"`
    Preferences    Preferences       `yaml:"preferences"`
    Aliases        map[string]string `yaml:"aliases,omitempty"`
}
```

### Alias Package

```go
// pkg/config/alias.go
package config

import (
    "fmt"
    "os"
    "regexp"
    "sort"
    "strings"

    "gopkg.in/yaml.v3"
)

// aliasNameRegex validates alias names: letters, numbers, hyphens, underscores
var aliasNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// maxPositionalParam is the highest $N we scan for when validating argument count
const maxPositionalParam = 9

// ValidateAliasName checks that an alias name is syntactically valid.
func ValidateAliasName(name string) error {
    if name == "" {
        return fmt.Errorf("alias name cannot be empty")
    }
    if !aliasNameRegex.MatchString(name) {
        return fmt.Errorf("alias name %q is invalid: use only letters, numbers, hyphens, and underscores", name)
    }
    return nil
}

// SetAlias adds or updates an alias. Returns an error if the name is invalid.
// builtinCheck is called to verify the name does not shadow a built-in command.
func (c *Config) SetAlias(name, expansion string, builtinCheck func(string) bool) error {
    if err := ValidateAliasName(name); err != nil {
        return err
    }
    if builtinCheck != nil && builtinCheck(name) {
        return fmt.Errorf("%q is a built-in command and cannot be used as an alias name", name)
    }
    if expansion == "" {
        return fmt.Errorf("alias expansion cannot be empty")
    }
    if c.Aliases == nil {
        c.Aliases = make(map[string]string)
    }
    c.Aliases[name] = expansion
    return nil
}

// DeleteAlias removes an alias by name. Returns an error if it does not exist.
func (c *Config) DeleteAlias(name string) error {
    if c.Aliases == nil {
        return fmt.Errorf("alias %q not found", name)
    }
    if _, ok := c.Aliases[name]; !ok {
        return fmt.Errorf("alias %q not found", name)
    }
    delete(c.Aliases, name)
    return nil
}

// GetAlias returns the expansion for an alias, or empty string if not found.
func (c *Config) GetAlias(name string) (string, bool) {
    if c.Aliases == nil {
        return "", false
    }
    exp, ok := c.Aliases[name]
    return exp, ok
}

// ListAliases returns all aliases sorted alphabetically by name.
func (c *Config) ListAliases() []AliasEntry {
    entries := make([]AliasEntry, 0, len(c.Aliases))
    for name, expansion := range c.Aliases {
        entries = append(entries, AliasEntry{Name: name, Expansion: expansion})
    }
    sort.Slice(entries, func(i, j int) bool {
        return entries[i].Name < entries[j].Name
    })
    return entries
}

// AliasEntry is a single alias for display purposes.
type AliasEntry struct {
    Name      string `table:"NAME"`
    Expansion string `table:"EXPANSION"`
}

// AliasFile represents the YAML structure for import/export.
type AliasFile struct {
    Aliases map[string]string `yaml:"aliases"`
}

// ExportAliases writes aliases to a file in YAML format.
func (c *Config) ExportAliases(path string) error {
    af := AliasFile{Aliases: c.Aliases}
    data, err := yaml.Marshal(af)
    if err != nil {
        return fmt.Errorf("failed to marshal aliases: %w", err)
    }
    return os.WriteFile(path, data, 0600)
}

// ImportAliases reads aliases from a YAML file and merges them into the config.
// If overwrite is false, existing aliases are not replaced and conflicts are
// returned as a list of names.
func (c *Config) ImportAliases(path string, overwrite bool, builtinCheck func(string) bool) ([]string, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("failed to read alias file: %w", err)
    }

    var af AliasFile
    if err := yaml.Unmarshal(data, &af); err != nil {
        return nil, fmt.Errorf("failed to parse alias file: %w", err)
    }

    if c.Aliases == nil {
        c.Aliases = make(map[string]string)
    }

    var conflicts []string
    for name, expansion := range af.Aliases {
        if err := ValidateAliasName(name); err != nil {
            return nil, fmt.Errorf("invalid alias in file: %w", err)
        }
        if builtinCheck != nil && builtinCheck(name) {
            return nil, fmt.Errorf("alias %q in file shadows a built-in command", name)
        }
        if _, exists := c.Aliases[name]; exists && !overwrite {
            conflicts = append(conflicts, name)
            continue
        }
        c.Aliases[name] = expansion
    }

    return conflicts, nil
}
```

### Alias Resolution

Resolution happens in `cmd/root.go` before Cobra parses args. This is the same
approach used by `gh` -- intercept `os.Args`, check if the first non-flag argument
matches an alias, expand it, and either re-invoke Cobra or exec a shell.

```go
// cmd/alias_resolve.go
package cmd

import (
    "fmt"
    "os"
    "os/exec"
    "regexp"
    "strconv"
    "strings"

    "github.com/Dynatrace/dtctl/pkg/config"
)

// resolveAlias checks if the first argument is an alias and expands it.
// Returns (expanded args, isShellAlias, error).
// If no alias matches, returns (nil, false, nil).
func resolveAlias(args []string, cfg *config.Config) ([]string, bool, error) {
    if len(args) == 0 || cfg == nil {
        return nil, false, nil
    }

    // Skip if the first arg is a flag
    if strings.HasPrefix(args[0], "-") {
        return nil, false, nil
    }

    name := args[0]
    expansion, ok := cfg.GetAlias(name)
    if !ok {
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
    return parts
}

// execShellAlias runs a shell alias via sh -c.
func execShellAlias(shellCmd string) error {
    cmd := exec.Command("sh", "-c", shellCmd)
    cmd.Stdin = os.Stdin
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}
```

`execShellAlias` is gated on `Capabilities.ShellAliases`: the CLI grants it,
embedded and service invocations do not, so a `!` alias there fails with a
capability error instead of running `sh -c`. Session-backed invocations skip
alias resolution entirely — aliases are a host-config convenience, and a
tenant's request must not expand through the host's alias table — and `alias`
is one of the top-level commands removed from the service surface
([SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md)).

### Integration into Execute()

```go
// cmd/root.go (modified)

func Execute() {
    setupErrorHandlers(rootCmd)

    // --- Alias resolution (before Cobra parses args) ---
    // Load config quietly; if it fails, skip alias resolution (the real
    // command will produce the proper error later).
    if cfg, err := config.Load(); err == nil {
        // os.Args[0] is the binary name; work with os.Args[1:]
        expanded, isShell, err := resolveAlias(os.Args[1:], cfg)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Error: %s\n", err)
            os.Exit(1)
        }

        if isShell {
            if err := execShellAlias(expanded[0]); err != nil {
                os.Exit(1)
            }
            return
        }

        if expanded != nil {
            rootCmd.SetArgs(expanded)
        }
    }
    // --- End alias resolution ---

    if err := rootCmd.Execute(); err != nil {
        // ... existing error handling ...
    }
}
```

### Builtin Check

The builtin check is passed as a function so the `config` package doesn't depend
on Cobra:

```go
// cmd/alias.go

// isBuiltinCommand returns true if name matches any registered Cobra command.
func isBuiltinCommand(name string) bool {
    for _, cmd := range rootCmd.Commands() {
        if cmd.Name() == name {
            return true
        }
        for _, alias := range cmd.Aliases {
            if alias == name {
                return true
            }
        }
    }
    return false
}
```

### Command Definitions

```go
// cmd/alias.go
package cmd

import (
    "fmt"

    "github.com/spf13/cobra"
)

var aliasCmd = &cobra.Command{
    Use:   "alias",
    Short: "Manage command aliases",
    Long: `Create, list, and delete shorthand names for dtctl commands.

Aliases expand before command parsing, so they work exactly like typing
the full command. Use positional parameters ($1, $2, ...) for reusable
templates, or prefix with ! for shell expansion.

Examples:
  # Simple alias
  dtctl alias set prod-wf "get workflows --context=production"
  dtctl prod-wf

  # Parameterized alias
  dtctl alias set wf 'get workflow $1 --context=production'
  dtctl wf my-workflow-id

  # Shell alias (pipes, jq, etc.)
  dtctl alias set wf-count '!dtctl get workflows -o json | jq length'
  dtctl wf-count`,
}

var aliasSetCmd = &cobra.Command{
    Use:   "set <name> <expansion>",
    Short: "Create or update an alias",
    Args:  cobra.ExactArgs(2),
    RunE: func(cmd *cobra.Command, args []string) error {
        name, expansion := args[0], args[1]

        cfg, err := loadConfigRaw()
        if err != nil {
            return err
        }

        if err := cfg.SetAlias(name, expansion, isBuiltinCommand); err != nil {
            return err
        }

        if err := saveConfig(cfg); err != nil {
            return err
        }

        fmt.Printf("Alias %q set to %q\n", name, expansion)
        return nil
    },
}

var aliasListCmd = &cobra.Command{
    Use:   "list",
    Short: "List all aliases",
    Aliases: []string{"ls"},
    RunE: func(cmd *cobra.Command, args []string) error {
        cfg, err := loadConfigRaw()
        if err != nil {
            return err
        }

        entries := cfg.ListAliases()
        if len(entries) == 0 {
            fmt.Println("No aliases configured.")
            fmt.Println("Use 'dtctl alias set <name> <command>' to create one.")
            return nil
        }

        printer := NewPrinter()
        return printer.PrintList(entries)
    },
}

var aliasDeleteCmd = &cobra.Command{
    Use:   "delete <name> [name...]",
    Short: "Delete one or more aliases",
    Aliases: []string{"rm"},
    Args:  cobra.MinimumNArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        cfg, err := loadConfigRaw()
        if err != nil {
            return err
        }

        for _, name := range args {
            if err := cfg.DeleteAlias(name); err != nil {
                return err
            }
            fmt.Printf("Alias %q deleted\n", name)
        }

        return saveConfig(cfg)
    },
}

var aliasExportCmd = &cobra.Command{
    Use:   "export",
    Short: "Export aliases to a YAML file",
    RunE: func(cmd *cobra.Command, args []string) error {
        file, _ := cmd.Flags().GetString("file")
        if file == "" {
            return fmt.Errorf("--file is required")
        }

        cfg, err := loadConfigRaw()
        if err != nil {
            return err
        }

        if len(cfg.Aliases) == 0 {
            return fmt.Errorf("no aliases to export")
        }

        if err := cfg.ExportAliases(file); err != nil {
            return err
        }

        fmt.Printf("Exported %d alias(es) to %s\n", len(cfg.Aliases), file)
        return nil
    },
}

var aliasImportCmd = &cobra.Command{
    Use:   "import",
    Short: "Import aliases from a YAML file",
    RunE: func(cmd *cobra.Command, args []string) error {
        file, _ := cmd.Flags().GetString("file")
        overwrite, _ := cmd.Flags().GetBool("overwrite")

        if file == "" {
            return fmt.Errorf("--file is required")
        }

        cfg, err := loadConfigRaw()
        if err != nil {
            return err
        }

        conflicts, err := cfg.ImportAliases(file, overwrite, isBuiltinCommand)
        if err != nil {
            return err
        }

        if len(conflicts) > 0 && !overwrite {
            fmt.Printf("Skipped %d existing alias(es): %s\n",
                len(conflicts), strings.Join(conflicts, ", "))
            fmt.Println("Use --overwrite to replace existing aliases.")
        }

        if err := saveConfig(cfg); err != nil {
            return err
        }

        fmt.Println("Aliases imported successfully.")
        return nil
    },
}

func init() {
    rootCmd.AddCommand(aliasCmd)

    aliasCmd.AddCommand(aliasSetCmd)
    aliasCmd.AddCommand(aliasListCmd)
    aliasCmd.AddCommand(aliasDeleteCmd)
    aliasCmd.AddCommand(aliasExportCmd)
    aliasCmd.AddCommand(aliasImportCmd)

    aliasExportCmd.Flags().StringP("file", "f", "", "output file path")
    aliasImportCmd.Flags().StringP("file", "f", "", "input file path")
    aliasImportCmd.Flags().Bool("overwrite", false, "overwrite existing aliases")
}
```

---

## Alias Resolution Order

When dtctl receives a command, resolution follows this order:

1. **Global flags** -- If the first arg starts with `-`, skip alias lookup entirely
2. **Built-in commands** -- Cobra matches registered commands first (get, describe,
   config, alias, ctx, doctor, etc.). Built-ins always win.
3. **Aliases** -- If no built-in matches, check the alias map. Expand and re-parse.
4. **Unknown command** -- If no alias matches either, fall through to Cobra's error
   handling (which already provides "did you mean?" suggestions via `pkg/suggest`).

This means aliases can never accidentally override built-in commands, even if a
future dtctl release adds a command with the same name as an existing alias. In
that case, the built-in wins silently. Users can run `dtctl alias list` to audit.

---

## Use Cases

### 1. Environment Shortcuts

```bash
# Quick access to production resources
dtctl alias set prod-wf "get workflows --context=production"
dtctl alias set staging-dash "get dashboards --context=staging -o wide"

dtctl prod-wf          # => dtctl get workflows --context=production
dtctl staging-dash     # => dtctl get dashboards --context=staging -o wide
```

### 2. Parameterized Lookups

```bash
# Look up a workflow by name in production
dtctl alias set pw 'get workflow $1 --context=production'
dtctl pw error-handler
# => dtctl get workflow error-handler --context=production

# Deploy a specific file to a specific context
dtctl alias set deploy 'apply -f $1 --context=$2'
dtctl deploy workflows/handler.yaml production
# => dtctl apply -f workflows/handler.yaml --context=production
```

### 3. Shell Pipelines

```bash
# Count workflows
dtctl alias set wf-count '!dtctl get workflows -o json | jq length'

# Get workflow names only
dtctl alias set wf-names '!dtctl get workflows -o json | jq -r ".[].title"'

# Find workflows modified today
dtctl alias set wf-today '!dtctl get workflows -o json | jq "[.[] | select(.lastModified > \"$(date -u +%Y-%m-%dT00:00:00Z)\")]"'
```

### 4. Team Standardization

```bash
# Team lead creates standard aliases
cat > team-aliases.yaml <<EOF
aliases:
  prod-wf: "get workflows --context=production"
  staging-wf: "get workflows --context=staging"
  deploy: "apply -f $1 --context=$2"
  check: "diff -f $1 --context=production"
EOF

# Distribute to team
dtctl alias import -f team-aliases.yaml
```

### 5. CI/CD Shortcuts

```yaml
# .dtctl.yaml in project root
apiVersion: v1
kind: Config
current-context: ci
contexts:
  - name: ci
    context:
      environment: https://ci.dynatrace.com
      token-ref: ci-token
aliases:
  deploy: "apply -f ./dynatrace/ --plain"
  drift-check: "diff -f ./dynatrace/ --plain --quiet"
  validate: "apply -f ./dynatrace/ --dry-run --plain"
```

---

## Testing Strategy

### Unit Tests (`pkg/config/alias_test.go`)

```go
func TestSetAlias(t *testing.T) {
    tests := []struct {
        name      string
        aliasName string
        expansion string
        builtin   func(string) bool
        wantErr   string
    }{
        {
            name:      "simple alias",
            aliasName: "wf",
            expansion: "get workflows",
        },
        {
            name:      "alias with hyphens and underscores",
            aliasName: "prod-wf_v2",
            expansion: "get workflows --context=production",
        },
        {
            name:      "rejects empty name",
            aliasName: "",
            expansion: "get workflows",
            wantErr:   "alias name cannot be empty",
        },
        {
            name:      "rejects invalid characters",
            aliasName: "my alias!",
            expansion: "get workflows",
            wantErr:   "invalid",
        },
        {
            name:      "rejects builtin shadow",
            aliasName: "get",
            expansion: "describe workflows",
            builtin:   func(s string) bool { return s == "get" },
            wantErr:   "built-in command",
        },
        {
            name:      "rejects empty expansion",
            aliasName: "wf",
            expansion: "",
            wantErr:   "cannot be empty",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            cfg := NewConfig()
            err := cfg.SetAlias(tt.aliasName, tt.expansion, tt.builtin)
            if tt.wantErr != "" {
                require.ErrorContains(t, err, tt.wantErr)
            } else {
                require.NoError(t, err)
                got, ok := cfg.GetAlias(tt.aliasName)
                require.True(t, ok)
                require.Equal(t, tt.expansion, got)
            }
        })
    }
}

func TestDeleteAlias(t *testing.T) { /* not found, success */ }
func TestListAliases_Sorted(t *testing.T) { /* alphabetical */ }
func TestImportAliases_MergeAndConflicts(t *testing.T) { /* merge, skip, overwrite */ }
func TestImportAliases_RejectsBuiltinShadow(t *testing.T) { /* shadow in file */ }
func TestExportAliases_RoundTrip(t *testing.T) { /* export then import */ }
```

### Resolution Tests (`cmd/alias_resolve_test.go`)

```go
func TestResolveAlias(t *testing.T) {
    tests := []struct {
        name      string
        args      []string
        aliases   map[string]string
        wantArgs  []string
        wantShell bool
        wantErr   string
    }{
        {
            name:     "simple expansion",
            args:     []string{"wf"},
            aliases:  map[string]string{"wf": "get workflows"},
            wantArgs: []string{"get", "workflows"},
        },
        {
            name:     "positional params",
            args:     []string{"pw", "my-id"},
            aliases:  map[string]string{"pw": "get workflow $1"},
            wantArgs: []string{"get", "workflow", "my-id"},
        },
        {
            name:     "extra args appended",
            args:     []string{"wf", "--context=prod"},
            aliases:  map[string]string{"wf": "get workflows"},
            wantArgs: []string{"get", "workflows", "--context=prod"},
        },
        {
            name:      "shell alias",
            args:      []string{"count"},
            aliases:   map[string]string{"count": "!dtctl get wf -o json | jq length"},
            wantArgs:  []string{"dtctl get wf -o json | jq length"},
            wantShell: true,
        },
        {
            name:    "missing required arg",
            args:    []string{"pw"},
            aliases: map[string]string{"pw": "get workflow $1"},
            wantErr: "requires at least 1 argument",
        },
        {
            name:     "no match returns nil",
            args:     []string{"unknown"},
            aliases:  map[string]string{"wf": "get workflows"},
            wantArgs: nil,
        },
        {
            name:     "flag as first arg skips lookup",
            args:     []string{"--help"},
            aliases:  map[string]string{"--help": "bad"},
            wantArgs: nil,
        },
    }
    // ...
}

func TestSplitCommand_Quotes(t *testing.T) {
    tests := []struct {
        input string
        want  []string
    }{
        {`get workflows`, []string{"get", "workflows"}},
        {`query "fetch logs | limit 10"`, []string{"query", "fetch logs | limit 10"}},
        {`get workflow 'my workflow'`, []string{"get", "workflow", "my workflow"}},
    }
    // ...
}
```

### Integration Tests (`cmd/alias_test.go`)

```go
func TestAliasSetAndList(t *testing.T) { /* set, then list, verify output */ }
func TestAliasDelete(t *testing.T) { /* set, delete, verify gone */ }
func TestAliasImportExport(t *testing.T) { /* export, modify, import, verify */ }
func TestAliasExecution(t *testing.T) { /* set alias, execute it, verify args */ }
func TestAliasCannotShadowBuiltin(t *testing.T) { /* try to set "get", verify error */ }
```

---

## Error Handling

| Scenario | Error message |
|----------|--------------|
| Invalid alias name | `alias name "..." is invalid: use only letters, numbers, hyphens, and underscores` |
| Shadow built-in | `"get" is a built-in command and cannot be used as an alias name` |
| Empty expansion | `alias expansion cannot be empty` |
| Delete nonexistent | `alias "foo" not found` |
| Missing positional arg | `alias "pw" requires at least 1 argument ($1), got 0` |
| Import invalid YAML | `failed to parse alias file: ...` |
| Import shadow | `alias "get" in file shadows a built-in command` |
| Export with no aliases | `no aliases to export` |

---

## Alternatives Considered

### 1. Store aliases in a separate file

**Pros:** Clean separation, easy to share just aliases.
**Cons:** Another file to manage; config loading becomes more complex; can't use
project-local `.dtctl.yaml` for project aliases without extra logic.
**Decision:** Store in existing config. One file, one source of truth. Export
covers the sharing use case.

### 2. Alias chaining (alias referencing another alias)

**Pros:** More composable.
**Cons:** Risk of circular references, harder to debug, increases complexity
significantly for marginal value. Neither git nor gh support this.
**Decision:** Not supported. Keep it simple. Shell aliases cover complex composition.

### 3. Aliases as Cobra subcommands registered at startup

**Pros:** Full Cobra integration (help text, completion).
**Cons:** Requires loading config before Cobra init, complicates startup, aliases
would appear in `--help` output cluttering it.
**Decision:** Resolve before Cobra parses. Aliases are user shortcuts, not
first-class commands.

### 4. Support $@ for all remaining args

**Pros:** Explicit "pass everything through" syntax.
**Cons:** The current design already appends unused args, so `$@` is redundant.
Extra syntax to learn for no benefit.
**Decision:** Not needed. Unconsumed args are appended automatically.

---

## Future Enhancements

These are explicitly out of scope for the initial implementation but could be
added later if there is demand:

- **Alias descriptions** -- Optional `--description` on `alias set` for self-documenting aliases
- **Alias history** -- Track when aliases were last used for cleanup suggestions
- **Completion for alias names** -- Shell completion that includes alias names
- **Alias validation on set** -- Parse the expansion and warn if it references unknown commands

---

## Implementation Checklist

1. Add `Aliases map[string]string` to `Config` struct in `pkg/config/config.go`
2. Create `pkg/config/alias.go` with `SetAlias`, `DeleteAlias`, `GetAlias`,
   `ListAliases`, `ImportAliases`, `ExportAliases`, `ValidateAliasName`
3. Create `pkg/config/alias_test.go` with table-driven tests
4. Create `cmd/alias.go` with `alias set/list/delete/import/export` commands
5. Create `cmd/alias_resolve.go` with `resolveAlias`, `splitCommand`, `substituteParams`
6. Create `cmd/alias_resolve_test.go` with resolution and quote-parsing tests
7. Modify `cmd/root.go` `Execute()` to call `resolveAlias` before `rootCmd.Execute()`
8. Update `examples/config-example.yaml` with alias examples
9. Update `docs/dev/IMPLEMENTATION_STATUS.md`

---

## Success Metrics

- Alias CRUD works reliably (set, get, list, delete)
- Positional parameter substitution handles $1-$9 correctly
- Shell aliases execute through `sh -c` and inherit stdin/stdout/stderr
- Built-in commands can never be shadowed
- Import/export round-trips without data loss
- All tests pass with race detection enabled
- Config file remains valid YAML after alias operations

---

## References

- git aliases: https://git-scm.com/docs/git-config#Documentation/git-config.txt-aliasltnamegt
- gh alias: https://cli.github.com/manual/gh_alias
- Cobra command framework: https://github.com/spf13/cobra
