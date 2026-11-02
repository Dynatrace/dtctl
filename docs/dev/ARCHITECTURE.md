# dtctl Architecture Proposal

## Executive Summary

This document proposes a Go-based architecture for `dtctl`, leveraging the same technology stack as kubectl. Go provides excellent CLI tooling, cross-platform compilation, and a mature ecosystem for building robust command-line applications.

## Technology Stack

### Core Language: Go 1.24+

**Rationale:**
- **kubectl compatibility**: Uses the same language and many of the same libraries as kubectl
- **Single binary distribution**: Compiles to a single, statically-linked binary with no runtime dependencies
- **Cross-platform**: Native compilation for Linux, macOS, Windows (AMD64, ARM64)
- **Performance**: Fast startup time, low memory footprint
- **Strong ecosystem**: Excellent libraries for CLI development, HTTP clients, and OpenAPI
- **Concurrency**: Built-in goroutines for parallel operations (e.g., fetching multiple resources)

### CLI Framework: Cobra + Viper

#### Cobra (Command Structure)
**Repository**: https://github.com/spf13/cobra

**Features:**
- Hierarchical command structure (verb-noun pattern)
- Automatic help generation
- Shell completion (bash, zsh, fish, powershell)
- Persistent and local flags
- Aliases and command suggestions
- **Used by**: kubectl, GitHub CLI, Hugo, Docker CLI

**Usage:**
```go
// Root command
rootCmd := &cobra.Command{
    Use:   "dtctl",
    Short: "Dynatrace platform CLI",
}

// Subcommands
getCmd := &cobra.Command{Use: "get"}
applyCmd := &cobra.Command{Use: "apply"}

// Resource-specific commands
getDocumentsCmd := &cobra.Command{
    Use:     "documents [id]",
    Aliases: []string{"document", "doc"},
    Run:     getDocumentsHandler,
}
```

#### Viper (Configuration Management)
**Repository**: https://github.com/spf13/viper

**Features:**
- Configuration file support (YAML, JSON, TOML)
- Environment variable binding
- Default values
- Live watching and re-reading of config files
- Reading from remote config systems (optional)

**Usage:**
```go
// Load config following XDG Base Directory spec
// $XDG_CONFIG_HOME/dtctl/config (default: ~/.config/dtctl/config)
viper.SetConfigName("config")
viper.AddConfigPath(config.ConfigDir())

// Bind flags to config
viper.BindPFlag("context", cmd.Flags().Lookup("context"))
```

### Watch Mode: Real-time Resource Monitoring

**Package**: `pkg/watch`

**Architecture:**
- **Watcher**: Core polling engine with configurable intervals (minimum 1s, default 2s)
- **Differ**: Change detection algorithm using map-based comparison
- **WatchPrinter**: Output formatter with kubectl-style change indicators

**Features:**
- Incremental change display (additions, modifications, deletions)
- Graceful shutdown on Ctrl+C via context cancellation
- Error handling for transient failures, rate limiting, and network issues
- Memory-efficient (only stores last state, not full history)
- Works with all `get` commands and DQL queries

**Change Indicators:**
- `+` (green) for added resources
- `~` (yellow) for modified resources
- `-` (red) for deleted resources

**Flags:**
- `--watch`: Enable watch mode
- `--interval`: Polling interval (default: 2s, min: 1s)
- `--watch-only`: Skip initial state display

### API Client Generation: OpenAPI Generator + oapi-codegen

#### oapi-codegen
**Repository**: https://github.com/deepmap/oapi-codegen

**Rationale:**
- Type-safe API clients
- Supports all HTTP methods and authentication schemes
- Generates models for request/response bodies
- Lightweight generated code

**Alternative considered**: go-swagger (more heavyweight, OpenAPI 2.0 focused)

### HTTP Client: Standard library + Resty

#### Resty
**Repository**: https://github.com/go-resty/resty

**Features:**
- Built on top of Go's standard `net/http`
- Automatic retry with exponential backoff
- Request/response middleware
- Debug logging
- Multipart form data
- Automatic marshaling/unmarshaling

**Usage:**
```go
client := resty.New().
    SetBaseURL(baseURL).
    SetAuthToken(token).
    SetRetryCount(3).
    SetRetryWaitTime(1 * time.Second).
    SetRetryMaxWaitTime(10 * time.Second).
    AddRetryCondition(func(r *resty.Response, err error) bool {
        return r.StatusCode() == 429 // Retry on rate limit
    })
```

### Output Formatting

#### Table Output: tablewriter or pterm
**Repository**:
- https://github.com/olekukonko/tablewriter
- https://github.com/pterm/pterm (more modern, colorful)

**Features:**
- ASCII table formatting
- Column alignment
- Header formatting
- Border customization
- Color support (pterm)

**Usage:**
```go
table := tablewriter.NewWriter(os.Stdout)
table.SetHeader([]string{"Name", "Type", "Owner", "Modified"})
table.AppendBulk(rows)
table.Render()
```

#### JSON/YAML: Standard library + yaml.v3

**Packages:**
- `encoding/json` (standard library)
- `gopkg.in/yaml.v3` for YAML

#### JSONPath: kubernetes/client-go JSONPath

**Repository**: Part of k8s.io/client-go

**Features:**
- kubectl-compatible JSONPath syntax
- Field selection from structured output

#### Color Control: NO_COLOR standard

**Standard**: [no-color.org](https://no-color.org/)

**Decision logic:**
```
Color enabled = NOT (NO_COLOR is set) AND NOT (--plain flag) AND (stdout is a TTY OR FORCE_COLOR=1)
```

**Implementation** (`pkg/output/styles.go`):
- `ColorEnabled()` — returns whether ANSI color output is enabled (cached with `sync.Once`)
- `Colorize(text, colorCode)` — wraps text in ANSI escape codes only when color is enabled
- `ColorCode(code)` — returns the ANSI code string or empty string when color is disabled
- `ResetColorCache()` — resets the `sync.Once` cache (for testing only)
- TTY detection uses `golang.org/x/term.IsTerminal()`

**Environment variables:**
- `NO_COLOR` (any non-empty value) — disables color output
- `FORCE_COLOR=1` — overrides TTY detection to force color on

### Context Management: Similar to kubeconfig

**Implementation:**
- YAML-based configuration file
- Store contexts, environments, tokens
- Current context pointer
- Use `gopkg.in/yaml.v3` for serialization

**Structure:**
```go
type Config struct {
    APIVersion     string              `yaml:"apiVersion"`
    Kind           string              `yaml:"kind"`
    CurrentContext string              `yaml:"current-context"`
    Contexts       []NamedContext      `yaml:"contexts"`
    Tokens         []NamedToken        `yaml:"tokens"`
    Preferences    Preferences         `yaml:"preferences"`
}

type NamedContext struct {
    Name    string  `yaml:"name"`
    Context Context `yaml:"context"`
}

type Context struct {
    Environment string `yaml:"environment"`
    TokenRef    string `yaml:"token-ref"`
}
```

**XDG Directory Structure:**

dtctl follows the XDG Base Directory Specification using the `github.com/adrg/xdg` library for cross-platform compatibility:

- **Config Directory**: Stores configuration files
  - Linux: `$XDG_CONFIG_HOME/dtctl` (default: `~/.config/dtctl`)
  - macOS: `~/Library/Application Support/dtctl`
  - Windows: `%LOCALAPPDATA%\dtctl`

- **Data Directory**: For application data (query libraries, templates)
  - Linux: `$XDG_DATA_HOME/dtctl` (default: `~/.local/share/dtctl`)
  - macOS: `~/Library/Application Support/dtctl`
  - Windows: `%LOCALAPPDATA%\dtctl`

- **Cache Directory**: For temporary cached data
  - Linux: `$XDG_CACHE_HOME/dtctl` (default: `~/.cache/dtctl`)
  - macOS: `~/Library/Caches/dtctl`
  - Windows: `%LOCALAPPDATA%\dtctl`

### Validation: validator + custom logic

**Repository**: https://github.com/go-playground/validator

**Features:**
- Struct field validation
- Custom validation functions
- OpenAPI schema validation

### Testing

#### Unit Testing: Standard library testing package
```go
import "testing"

func TestGetDocuments(t *testing.T) {
    // Test implementation
}
```

#### HTTP Mocking: httptest (standard library)
```go
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(mockResponse)
}))
defer server.Close()
```

#### Additional Tools:
- **testify**: Assertions and mocking - https://github.com/stretchr/testify
- **gomock**: Mock generation - https://github.com/golang/mock
- **Golden (snapshot) tests**: Output formatters are covered by golden-file tests in `pkg/output/golden_test.go` — uses real production structs from `pkg/resources/*` to capture exact output across all formats (table, wide, JSON, YAML, CSV, agent, watch, chart). Update with `make test-update-golden` or `go test ./pkg/output/ -run TestGolden -update`.

### Error Handling: cockroachdb/errors

**Repository**: https://github.com/cockroachdb/errors

**Features:**
- Error wrapping with context
- Stack traces
- Error formatting
- Compatible with standard `errors` package
- Better than pkg/errors (archived)

**Usage:**
```go
if err != nil {
    return errors.Wrap(err, "failed to fetch document")
}

// Exit codes
const (
    ExitSuccess           = 0
    ExitError             = 1
    ExitUsageError        = 2
    ExitAuthError         = 3
    ExitNotFoundError     = 4
    ExitPermissionError   = 5
)
```

### Logging: logrus or zap

#### logrus (simpler)
**Repository**: https://github.com/sirupsen/logrus

**Features:**
- Structured logging
- Multiple log levels
- Pluggable formatters
- Hooks for log routing

#### zap (faster, more complex)
**Repository**: https://go.uber.org/zap

**Features:**
- Extremely fast
- Structured and leveled logging
- Zero-allocation in hot paths

**Recommendation**: Start with logrus, migrate to zap if performance becomes an issue

### Build & Distribution

#### Build Tool: Goreleaser
**Repository**: https://goreleaser.com/

**Features:**
- Cross-platform builds
- Archive creation (tar.gz, zip)
- Homebrew tap generation
- Docker image builds
- GitHub/GitLab release creation
- Checksums and signing

**Configuration:**
```yaml
# .goreleaser.yml
builds:
  - id: dtctl
    binary: dtctl
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64
    env:
      - CGO_ENABLED=0
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}
      - -X main.date={{.Date}}

archives:
  - format: tar.gz
    format_overrides:
      - goos: windows
        format: zip

brews:
  - name: dtctl
    repository:
      owner: your-org
      name: homebrew-tap
    description: "Dynatrace platform CLI"
    homepage: "https://github.com/your-org/dtctl"
```

#### Package Managers:
- **Homebrew**: Auto-generated via goreleaser
- **apt/yum**: DEB/RPM packages via goreleaser
- **Scoop**: Windows package manager
- **Docker**: Container image for CI/CD usage

### Shell Completion: Cobra built-in

**Generation:**
```go
// Cobra provides built-in completion
rootCmd.CompletionCmd()
```

**Installation:**
```bash
# bash
dtctl completion bash > /etc/bash_completion.d/dtctl

# zsh
dtctl completion zsh > "${fpath[1]}/_dtctl"

# fish
dtctl completion fish > ~/.config/fish/completions/dtctl.fish
```

### Update Mechanism: self-update

**Repository**: https://github.com/rhysd/go-github-selfupdate

**Features:**
- Check for new releases
- Download and replace binary
- Verify checksums
- GitHub Releases integration

**Usage:**
```bash
dtctl version --check-update
dtctl update
```

---

## Project Structure

```
dtctl/
├── cmd/
│   ├── root.go                 # Root command
│   ├── get.go                  # Get command
│   ├── describe.go             # Describe command
│   ├── create.go               # Create command
│   ├── delete.go               # Delete command
│   ├── apply.go                # Apply command
│   ├── patch.go                # Patch command
│   ├── edit.go                 # Edit command
│   ├── logs.go                 # Logs command
│   ├── exec.go                 # Exec command
│   ├── label.go                # Label command
│   ├── wait.go                 # Wait command
│   ├── diff.go                 # Diff command
│   ├── explain.go              # Explain command
│   ├── config.go               # Config command
│   ├── auth.go                 # Auth command
│   ├── completion.go           # Shell completion
│   ├── version.go              # Version command
│   ├── run.go                  # cmd.Run: in-process entrypoint + per-invocation isolation
│   ├── capabilities.go         # Capability gate for subprocess spawns
│   ├── session.go              # Per-invocation tenant (synthetic config)
│   ├── stdio.go                # Per-invocation stream redirection
│   └── blocked.go              # Per-invocation command-surface mask
│
├── pkg/
│   ├── api/                    # Generated API clients
│   │   ├── document/           # Document API client
│   │   ├── slo/                # SLO API client
│   │   ├── automation/         # Automation API client
│   │   ├── grail/              # Grail API clients
│   │   ├── iam/                # IAM API client
│   │   └── ...                 # Other API clients
│   │
│   ├── client/                 # Core API client logic
│   │   ├── client.go           # Base client with auth, retry
│   │   ├── pagination.go       # Pagination helpers
│   │   ├── rate_limit.go       # Rate limiting
│   │   └── errors.go           # Error handling
│   │
│   ├── config/                 # Configuration management
│   │   ├── config.go           # Config structure
│   │   ├── context.go          # Context management
│   │   ├── loader.go           # Config loading
│   │   └── writer.go           # Config writing
│   │
│   ├── resources/              # Resource handlers
│   │   ├── document/           # Document resource
│   │   │   ├── get.go
│   │   │   ├── create.go
│   │   │   ├── update.go
│   │   │   ├── delete.go
│   │   │   └── share.go
│   │   ├── slo/                # SLO resource
│   │   ├── workflow/           # Workflow resource
│   │   └── ...                 # Other resources
│   │
│   ├── output/                 # Output formatting
│   │   ├── table.go            # Table formatter
│   │   ├── json.go             # JSON formatter
│   │   ├── yaml.go             # YAML formatter
│   │   ├── jsonpath.go         # JSONPath formatter
│   │   ├── styles.go           # Color control (ColorEnabled, Colorize, ColorCode)
│   │   ├── agent.go            # Agent output envelope
│   │   └── printer.go          # Printer interface
│   │
│   ├── manifest/               # Manifest parsing
│   │   ├── parser.go           # YAML/JSON manifest parser
│   │   ├── validator.go        # Manifest validation
│   │   └── applier.go          # Apply logic (create/update)
│   │
│   ├── diff/                   # Diff implementation
│   │   └── diff.go             # Resource diffing logic
│   │
│   ├── watch/                  # Watch implementation
│   │   └── watch.go            # Resource watching
│   │
│   ├── exec/                   # Exec implementations
│   │   ├── dql.go              # DQL query executor
│   │   ├── workflow.go         # Workflow executor
│   │   ├── slo.go              # SLO evaluator
│   │   ├── function.go         # Function executor
│   │   └── intent.go           # Intent URL generator
│   │
│   ├── vfs/                    # File-access seam for user-supplied paths
│   │   ├── vfs.go              # FS interface + host filesystem default
│   │   └── mapfs.go            # In-memory FS built from a request's files
│   │
│   ├── engine/                 # Embeddable service engine
│   │   ├── engine.go           # Execute(ctx, Request) — one command line per request
│   │   └── policy.go           # Commands unsupported in a service
│   │
│   ├── serve/                  # `dtctl serve <protocol>` reference servers
│   │   ├── serve.go            # Protocol parent command + standalone dispatch
│   │   └── http.go             # `serve http` — POST /v1/execute, GET /healthz
│   │
│   └── util/                   # Utilities
│       ├── editor.go           # Interactive editor
│       ├── prompt.go           # User prompts
│       ├── selector.go         # Label selector parsing
│       └── version.go          # Version comparison
│
├── scripts/                    # Build and dev scripts
│   └── build.sh                # Build script
│
├── test/                       # Integration tests
│   ├── fixtures/               # Test fixtures
│   └── e2e/                    # End-to-end tests
│
├── .goreleaser.yml             # Release configuration
├── Makefile                    # Build automation
├── go.mod                      # Go module definition
├── go.sum                      # Dependency checksums
├── main.go                     # Entry point
├── README.md
├── API_DESIGN.md
├── ARCHITECTURE.md
├── AGENTS.md
└── sdk/                        # Separate Go module (github.com/Dynatrace/dtctl/sdk)
    ├── go.mod
    ├── api/                    # Typed API wrappers (one package per API surface)
    │   ├── analyzer/           # Query analyzer
    │   ├── appengine/          # App Engine functions
    │   ├── bucket/             # Grail buckets
    │   ├── copilot/            # Davis Copilot
    │   ├── document/           # Documents + trash
    │   ├── edgeconnect/        # EdgeConnect
    │   ├── extension/          # Extensions 2.0
    │   ├── hub/                # Hub items
    │   ├── iam/                # Account IAM
    │   ├── livedebugger/       # Live debugger
    │   ├── notification/       # Notifications
    │   ├── segment/            # Data segments
    │   ├── settings/           # Settings 2.0
    │   ├── slo/                # SLOs
    │   └── workflow/           # Workflows + executions
    ├── httpclient/             # HTTP client, response helpers, pagination, typed errors
    ├── auth/                   # Token type detection
    ├── urls/                   # URL validation/normalization
    └── agentmode/              # AI agent detection
```

---

## Dependency Management

### Go Modules
Use Go modules (go.mod) for dependency management:

```go
module github.com/yourorg/dtctl

go 1.24

require (
    github.com/spf13/cobra v1.8.0
    github.com/spf13/viper v1.18.0
    github.com/go-resty/resty/v2 v2.11.0
    github.com/olekukonko/tablewriter v0.0.5
    gopkg.in/yaml.v3 v3.0.1
    github.com/cockroachdb/errors v1.11.1
    github.com/sirupsen/logrus v1.9.3
    github.com/go-playground/validator/v10 v10.16.0
    github.com/stretchr/testify v1.8.4
    k8s.io/client-go v0.29.0
)
```

---

## Build System: Makefile

```makefile
.PHONY: all build clean test generate install

VERSION ?= $(shell git describe --tags --match "v*" --always --dirty)
COMMIT ?= $(shell git rev-parse --short HEAD)
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS = -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE) -s -w"

all: build

# Build the binary
build:
	@echo "Building dtctl..."
	@go build $(LDFLAGS) -o bin/dtctl .

# Run tests
test:
	@echo "Running tests..."
	@go test -v -race -coverprofile=coverage.out ./...

# Install locally
install:
	@echo "Installing dtctl..."
	@go install $(LDFLAGS) .

# Clean build artifacts
clean:
	@rm -rf bin/ dist/ coverage.out

# Run linter
lint:
	@golangci-lint run

# Format code
fmt:
	@go fmt ./...
	@goimports -w .

# Release (using goreleaser)
release:
	@goreleaser release --clean

# Release snapshot (local testing)
release-snapshot:
	@goreleaser release --snapshot --clean
```

---

## Development Workflow

### 1. Development Setup

```bash
# Clone repository
git clone https://github.com/yourorg/dtctl.git
cd dtctl

# Install dependencies
go mod download

# Build
make build

# Run tests
make test
```

### 2. CI/CD Pipeline (GitHub Actions)

```yaml
# .github/workflows/ci.yml
name: CI

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.24'

      - name: Install dependencies
        run: go mod download

      - name: Run tests
        run: make test

      - name: Run linter
        run: make lint

      - name: Build
        run: make build

  release:
    runs-on: ubuntu-latest
    if: startsWith(github.ref, 'refs/tags/')
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.24'

      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@v5
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

---

## Core Implementation Patterns

### 1. Command Pattern

```go
// cmd/get.go
package cmd

import (
    "github.com/spf13/cobra"
    "github.com/yourorg/dtctl/pkg/resources"
)

var getCmd = &cobra.Command{
    Use:   "get [resource]",
    Short: "Display one or many resources",
}

var getDocumentsCmd = &cobra.Command{
    Use:     "documents [id]",
    Aliases: []string{"document", "doc"},
    Short:   "Get documents",
    RunE:    runGetDocuments,
}

func runGetDocuments(cmd *cobra.Command, args []string) error {
    cfg, err := config.Load()
    if err != nil {
        return err
    }

    client := client.NewFromConfig(cfg)
    handler := resources.NewDocumentHandler(client)

    opts := resources.GetOptions{
        Output:    cmd.Flags().GetString("output"),
        Selector:  cmd.Flags().GetString("selector"),
    }

    if len(args) > 0 {
        return handler.Get(args[0], opts)
    }
    return handler.List(opts)
}

func init() {
    getCmd.AddCommand(getDocumentsCmd)
    rootCmd.AddCommand(getCmd)

    // Add flags
    getDocumentsCmd.Flags().StringP("output", "o", "table", "Output format")
    getDocumentsCmd.Flags().StringP("selector", "l", "", "Label selector")
}
```

### 2. Resource Handler Pattern

> **Note**: Resource handlers in `pkg/resources/` now delegate HTTP calls to typed API wrappers in `sdk/api/`. The SDK module contains no file I/O or display logic — those remain in the CLI layer. See `sdk/README.md` for SDK design principles.

```go
// pkg/resources/document/handler.go
package document

import (
    "context"
    "github.com/yourorg/dtctl/pkg/api/document"
    "github.com/yourorg/dtctl/pkg/output"
)

type Handler struct {
    client *document.Client
}

func NewHandler(client *document.Client) *Handler {
    return &Handler{client: client}
}

func (h *Handler) Get(id string, opts GetOptions) error {
    ctx := context.Background()

    doc, err := h.client.GetDocument(ctx, id)
    if err != nil {
        return errors.Wrap(err, "failed to get document")
    }

    printer := output.NewPrinter(opts.Output)
    return printer.Print(doc)
}

func (h *Handler) List(opts ListOptions) error {
    ctx := context.Background()

    params := &document.ListDocumentsParams{
        Owner: opts.Owner,
        Type:  opts.Type,
    }

    docs, err := h.client.ListDocuments(ctx, params)
    if err != nil {
        return errors.Wrap(err, "failed to list documents")
    }

    printer := output.NewPrinter(opts.Output)
    return printer.PrintList(docs)
}
```

### 3. Output Formatting Pattern

```go
// pkg/output/printer.go
package output

import (
    "encoding/json"
    "io"
)

type Printer interface {
    Print(interface{}) error
    PrintList(interface{}) error
}

func NewPrinter(format string, writer io.Writer) Printer {
    switch format {
    case "json":
        return &JSONPrinter{writer: writer}
    case "yaml":
        return &YAMLPrinter{writer: writer}
    case "table", "wide":
        return &TablePrinter{writer: writer, wide: format == "wide"}
    default:
        return &TablePrinter{writer: writer}
    }
}

type JSONPrinter struct {
    writer io.Writer
}

func (p *JSONPrinter) Print(obj interface{}) error {
    encoder := json.NewEncoder(p.writer)
    encoder.SetIndent("", "  ")
    return encoder.Encode(obj)
}
```

### 4. Client Configuration Pattern

```go
// pkg/client/client.go
package client

import (
    "github.com/go-resty/resty/v2"
    "github.com/yourorg/dtctl/pkg/config"
)

type Client struct {
    http    *resty.Client
    baseURL string
    token   string
}

func NewFromConfig(cfg *config.Config) (*Client, error) {
    ctx := cfg.CurrentContext()
    if ctx == nil {
        return nil, errors.New("no current context")
    }

    token := cfg.GetToken(ctx.TokenRef)
    if token == "" {
        return nil, errors.New("no token found")
    }

    httpClient := resty.New().
        SetBaseURL(ctx.Environment).
        SetAuthToken(token).
        SetRetryCount(3).
        AddRetryCondition(isRetryable).
        SetLogger(log.StandardLogger())

    return &Client{
        http:    httpClient,
        baseURL: ctx.Environment,
        token:   token,
    }, nil
}

func isRetryable(r *resty.Response, err error) bool {
    if err != nil {
        return true
    }

    // Retry on rate limit or server errors
    return r.StatusCode() == 429 || r.StatusCode() >= 500
}
```

---

### 5. Invocation Model Pattern

dtctl is both a one-shot process and an in-process library called once per
service request. `cmd.Run(argv, RunOptions)` is the single entrypoint for both;
`Execute()` (the CLI's `main`) delegates to it.

```go
res, err := cmd.Run(argv, cmd.RunOptions{
    Capabilities:    cmd.Capabilities{},        // no subprocesses
    Session:         &cmd.Session{...},         // this request's tenant
    FS:              vfs.NewMapFS(files),       // this request's files
    Stdout:          &stdout,                   // this request's streams
    BlockedCommands: engine.UnsupportedCommands(),
})
```

Each invocation is bracketed by setup/restore of process-wide state:

1. **Pristine command tree** — a one-time snapshot of every command's
   as-registered state (`RunE`, `Run`, `Args`, `Hidden`, `DisableFlagParsing`)
   is restored, and all flag values reset to declared defaults, so per-run
   mutations (profile masks, scope-preflight wraps) and stale flag values cannot
   leak into the next invocation.
2. **Capabilities** applied, then restored.
3. **Session** installed behind `LoadConfig()` as a synthetic single-context
   config, with host credential/config environment variables scrubbed.
4. **Filesystem** installed for user-supplied paths (`pkg/vfs`).
5. **Streams** redirected (`os.Stdout`/`os.Stderr`/`os.Stdin` swapped), then
   drained and restored.

Because all five touch package or process state, invocations **serialize** on a
package mutex — the command tree is 277 command values wired by `init()`.
Parallelism comes from more instances or processes, not more goroutines. This is
the constraint that makes the rest safe; see
[SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md) for the full rationale and
the invariants contributors must preserve.

---

## Performance Considerations

### 1. Parallel Operations
Use goroutines for independent operations:

```go
// Fetch multiple resources in parallel
var wg sync.WaitGroup
results := make(chan *Document, len(ids))
errors := make(chan error, len(ids))

for _, id := range ids {
    wg.Add(1)
    go func(id string) {
        defer wg.Done()
        doc, err := client.GetDocument(ctx, id)
        if err != nil {
            errors <- err
            return
        }
        results <- doc
    }(id)
}

wg.Wait()
close(results)
close(errors)
```

### 2. Caching
Implement caching for schemas and templates:

```go
type Cache struct {
    schemas map[string]*Schema
    ttl     time.Duration
    mu      sync.RWMutex
}

func (c *Cache) GetSchema(id string) (*Schema, error) {
    c.mu.RLock()
    schema, ok := c.schemas[id]
    c.mu.RUnlock()

    if ok {
        return schema, nil
    }

    // Fetch and cache
    schema, err := fetchSchema(id)
    if err != nil {
        return nil, err
    }

    c.mu.Lock()
    c.schemas[id] = schema
    c.mu.Unlock()

    return schema, nil
}
```

### 3. Streaming
Stream large result sets:

```go
func (h *Handler) StreamLogs(id string, follow bool) error {
    stream, err := h.client.StreamLogs(ctx, id)
    if err != nil {
        return err
    }
    defer stream.Close()

    scanner := bufio.NewScanner(stream)
    for scanner.Scan() {
        fmt.Println(scanner.Text())
    }
    return scanner.Err()
}
```

---

## Security Considerations

### 1. Token Storage
- Store tokens in config file with restricted permissions (0600)
- Consider integration with OS keychain/credential manager
- Support for credential helpers (similar to Docker)

### 2. TLS Configuration
```go
httpClient.SetTLSClientConfig(&tls.Config{
    MinVersion: tls.VersionTLS12,
    // Add custom CA if needed
})
```

### 3. Input Validation
- Validate all user inputs
- Sanitize file paths
- Validate resource IDs and names

---

## Future Enhancements

### Phase 2 Additions:
1. **Plugin System**: ✅ shipped 2026-07-12 as the kubectl-style exec convention — `dtctl foo` execs `dtctl-foo` from PATH; Go plugins rejected (see [PLUGIN_CONVENTIONS.md](PLUGIN_CONVENTIONS.md))
2. **Embeddable engine / service mode**: ✅ shipped as `cmd.Run` + `pkg/engine` + `dtctl serve http` — the CLI surface runs in-process, multi-tenant, one invocation per request (see [SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md))
3. **Interactive Mode**: TUI using bubbletea/lipgloss
4. **Local Development Mode**: Mock server for testing
5. **Credential Providers**: Integration with HashiCorp Vault, AWS Secrets Manager
6. **Advanced Caching**: Persistent cache with TTL and invalidation
7. **Metrics**: Built-in metrics collection for usage analytics
8. **Auto-update**: Automatic version checking and updates

---

## Summary

This architecture provides:

✅ **Production-ready**: Based on proven technologies (kubectl stack)
✅ **Maintainable**: Clear project structure and patterns
✅ **Performant**: Fast startup, efficient resource usage
✅ **Cross-platform**: Single binary for all major platforms
✅ **Extensible**: Easy to add new resources and commands
✅ **Type-safe**: Generated clients from OpenAPI specs
✅ **Well-tested**: Comprehensive testing strategy
✅ **Professional**: Modern CI/CD and release automation

The proposed stack minimizes external dependencies while leveraging battle-tested libraries from the Kubernetes ecosystem and Go community.
