# Contributing to dtctl

Thank you for your interest in contributing to dtctl! This document provides guidelines and instructions for contributing.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Setup](#development-setup)
- [How to Contribute](#how-to-contribute)
- [Pull Request Process](#pull-request-process)
- [Coding Standards](#coding-standards)
- [Testing Requirements](#testing-requirements)
- [Commit Messages](#commit-messages)
- [Reporting Bugs](#reporting-bugs)
- [Suggesting Features](#suggesting-features)
- [Documentation Guidelines](#documentation-guidelines)
- [Getting Help](#getting-help)

## Code of Conduct

This project adheres to a Code of Conduct that all contributors are expected to follow. Please read [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) before contributing.

## Getting Started

### Prerequisites

- Go 1.24 or later
- Git
- A Dynatrace environment (for integration testing)

### Development Setup

1. **Fork the repository**.

2. **Clone the repository**:
   ```bash
   git clone https://github.com/Dynatrace/dtctl.git
   cd dtctl
   ```

3. **Install dependencies**:
   ```bash
   go mod download
   ```

4. **Build the project**:
   ```bash
   make build
   ```

5. **Run tests**:
   ```bash
   make test
   ```

6. **Install development tools**:
   ```bash
   # Install linters
   go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

   # Install vulnerability scanner
   go install golang.org/x/vuln/cmd/govulncheck@latest
   ```

## How to Contribute

### Ways to Contribute

- **Report bugs** - Help us identify and fix issues
- **Suggest features** - Share ideas for improvements
- **Write documentation** - Improve guides, examples, and API docs
- **Submit code** - Fix bugs or implement new features
- **Review pull requests** - Help review and test contributions

### Finding Issues to Work On

- Check the [issue tracker](https://github.com/Dynatrace/dtctl/issues)
- Look for issues labeled `good first issue` or `help wanted`
- Ask in discussions if you're unsure where to start

## Pull Request Process

### Before You Start

1. **Check existing issues and PRs** to avoid duplicate work
2. **Create an issue** to discuss major changes before implementing
3. **Fork the repository** and create a feature branch

### Creating a Pull Request

1. **Create a feature branch**:
   ```bash
   git checkout -b feature/your-feature-name
   ```

2. **Make your changes**:
   - Write clear, focused commits
   - Add tests for new functionality
   - Update documentation as needed
   - Follow coding standards (see below)

3. **Test your changes**:
   ```bash
   # Run all tests
   make test

   # Run linters
   make lint

   # Check test coverage
   make coverage
   ```

4. **Push to your fork**:
   ```bash
   git push origin feature/your-feature-name
   ```

5. **Create a pull request**:
   - Use a clear, descriptive title
   - Reference any related issues
   - Describe what changed and why
   - Include examples if applicable

### PR Requirements

Your pull request must:

- ✅ Pass all CI checks (tests, linting, security scans)
- ✅ Maintain or improve test coverage (minimum 70%)
- ✅ Include tests for new functionality
- ✅ Update documentation if behavior changes
- ✅ Follow the project's coding standards
- ✅ Use a [Conventional Commits](#commit-messages) PR title (it drives versioning and release notes — see [Releases](#releases))
- ✅ Be up-to-date with the main branch

### Review Process

1. Maintainers will review your PR within 5 business days
2. Address any feedback or requested changes
3. Once approved, a maintainer will merge your PR
4. Your contribution will be included in the next release

## Coding Standards

### Go Style Guide

Follow the official [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments) and [Effective Go](https://golang.org/doc/effective_go.html).

### Specific Guidelines

**File Organization**:
- Keep files under 500 lines
- One package per directory
- Group related functionality together

**Naming**:
- Use descriptive names (avoid abbreviations except for common ones)
- Follow Go conventions: `camelCase` for unexported, `PascalCase` for exported
- Interfaces: use `-er` suffix (e.g., `Handler`, `Executor`)

**Error Handling**:
- Always check errors
- Wrap errors with context using `fmt.Errorf("context: %w", err)`
- Return errors rather than logging and continuing

**Comments**:
- Add package-level comments for all packages
- Document all exported functions, types, and constants
- Use `//` for comments, not `/* */`
- Explain "why" not "what" for complex logic

**Code Organization**:
```go
package example

// Imports (standard library first, then third-party, then internal)
import (
    "context"
    "fmt"

    "github.com/spf13/cobra"

    "github.com/dynatrace/dtctl/pkg/client"
)

// Constants
const defaultTimeout = 30 * time.Second

// Variables
var ErrNotFound = errors.New("resource not found")

// Types
type Handler struct { ... }

// Functions
func NewHandler() *Handler { ... }
```

### Formatting

- Use `gofmt` to format code (enforced in CI)
- Use `goimports` to organize imports
- Maximum line length: 120 characters

## Testing Requirements

### Test Coverage

- **Minimum**: 70% overall coverage
- **New code**: 80% coverage for new packages
- **Critical packages**: 90% coverage for `pkg/client`, `pkg/config`

### Writing Tests

**Test file naming**: `*_test.go` (e.g., `client_test.go`)

**Test function naming**: `TestFunctionName_Scenario` (e.g., `TestNewClient_InvalidURL`)

**Test structure** (use table-driven tests):
```go
func TestHandler_Get(t *testing.T) {
    tests := []struct {
        name    string
        id      string
        want    *Resource
        wantErr bool
    }{
        {
            name:    "valid resource",
            id:      "123",
            want:    &Resource{ID: "123"},
            wantErr: false,
        },
        {
            name:    "not found",
            id:      "invalid",
            want:    nil,
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Test implementation
        })
    }
}
```

### Test Types

- **Unit tests**: Test individual functions in isolation
- **Integration tests**: Test component interactions (use `httptest` for API mocking)
- **E2E tests**: Test complete workflows (in `test/e2e/`)

### Running Tests

```bash
# Run all tests
make test

# Run tests with coverage
make coverage

# Run specific package tests
go test ./pkg/client/...

# Run specific test
go test -run TestClientNew ./pkg/client/

# Run tests with race detection
go test -race ./...
```

## Commit Messages

### Format

```
<type>: <subject>

<body>

<footer>
```

### Types

- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `test`: Adding or updating tests
- `refactor`: Code refactoring (no functional changes)
- `perf`: Performance improvements
- `chore`: Maintenance tasks (dependencies, tooling)
- `ci`: CI/CD changes

### Examples

```
feat: add support for OpenPipeline resources

Implement get, create, update, and delete operations for OpenPipeline
configurations. Includes CLI commands and resource handler.

Closes #123
```

```
fix: handle pagination correctly in document listing

Previously, only the first page of results was returned when listing
documents. Now correctly follows pagination tokens to fetch all results.

Fixes #456
```

### Guidelines

- Use imperative mood ("add feature" not "added feature")
- Keep subject line under 50 characters
- Capitalize subject line
- No period at the end of subject line
- Separate subject from body with blank line
- Wrap body at 72 characters
- Explain what and why, not how

> **Why this matters:** Releases are automated. Your commit/PR type determines the version bump and the release notes (see [Releases](#releases)), so the prefix is not just convention — it's load-bearing.

## Releases

Releases use [release-please](https://github.com/googleapis/release-please) — **contributors never edit a changelog or bump a version**, and there is no `CHANGELOG.md` file (the GitHub Release is the changelog). Releases are **triggered manually by maintainers**; ordinary merges to `main` do **not** publish anything.

How it works (maintainers):

- The release workflow runs only on manual dispatch (Actions → **Release** → **Run workflow**, or `gh workflow run release.yml`).
- **First dispatch**: release-please opens/updates a **release PR** that bumps the version based on the [Conventional Commits](#commit-messages) since the last release.
- Merge that release PR, then **dispatch again**: release-please creates the git tag and GitHub Release (with generated notes), and GoReleaser builds and publishes the binaries and Homebrew cask.
- **If GoReleaser fails** after the tag and release exist, never move or recreate the tag: the Go module proxy and checksum database have already recorded it. Fix the cause on `main`, then dispatch with `republish_tag` set to the tag (`gh workflow run release.yml -f republish_tag=vX.Y.Z`). That run skips release-please, builds the tag unchanged with `main`'s `.goreleaser.yaml`, and uploads into the existing release.

Version impact of commit types (pre-1.0):

| Commit type | Effect |
|-------------|--------|
| `feat:` | minor bump (e.g. 0.30.3 → 0.31.0) |
| `fix:` / `perf:` | patch bump (e.g. 0.30.3 → 0.30.4) |
| `feat!:`, `fix!:`, or a `BREAKING CHANGE:` footer | minor bump pre-1.0 (major once 1.0) |
| `docs:` / `test:` / `chore:` / `ci:` / `refactor:` | no release on their own |

Because PRs are squash-merged, the **PR title must be a valid conventional commit**. To override the computed version, add a `Release-As: X.Y.Z` footer to a commit on `main`.

## Reporting Bugs

### Before Reporting

- Check if the bug has already been reported
- Verify it's reproducible on the latest version
- Gather relevant information (version, OS, configuration)

### Bug Report Template

When reporting a bug, include:

1. **Description**: Clear description of the issue
2. **Steps to Reproduce**:
   ```
   1. Run `dtctl get workflows`
   2. Observe error message
   ```
3. **Expected Behavior**: What you expected to happen
4. **Actual Behavior**: What actually happened
5. **Environment**:
   - dtctl version (`dtctl version`)
   - OS and version
   - Go version (if building from source)
6. **Additional Context**: Logs, screenshots, or other relevant information

## Suggesting Features

### Feature Request Template

When suggesting a feature, include:

1. **Problem Statement**: What problem does this solve?
2. **Proposed Solution**: How should it work?
3. **Alternatives Considered**: Other approaches you considered
4. **Use Cases**: Real-world scenarios where this would be useful
5. **Implementation Ideas**: Technical approach (optional)

## Development Workflow

### Makefile Targets

```bash
# Build the binary
make build

# Run tests
make test

# Run linters
make lint

# Generate coverage report
make coverage

# Run security scans
make security-scan

# Build for all platforms
make build-all

# Clean build artifacts
make clean
```

### CI Pipeline

All pull requests must pass:

1. **Tests**: All tests must pass on Linux, macOS, and Windows
2. **Linting**: `golangci-lint` must pass with zero errors
3. **Security**: `govulncheck` must find no vulnerabilities
4. **Coverage**: Overall coverage must be ≥70%

## Documentation Guidelines

Documentation lives in `docs/` and is read on GitHub. It is plain CommonMark --
there is no Jekyll and no Liquid templating, so Go template examples like
`{{ .env }}` need no escaping.

### What is generated and what is not

`docs/resources/*.md`, `docs/COMMANDS.md`, and `docs/TOKEN_SCOPES.md` contain
`<!-- GENERATED:x:start/end -->` blocks derived from `dtctl commands --full`.
Never hand-edit inside those markers -- run `make docs-generate` and commit the
result. Everything outside them (overviews, examples, notes) is yours to write.

### Check your prose against the binary

```bash
make docs-check
```

This resolves every `dtctl ...` example in the docs to a real command path and
diffs its flags against that command's own `--help`, then checks relative links
and anchors. It runs on every PR that touches `docs/**`. If a doc deliberately
shows a command the CLI rejects ("these commands have been removed"), mark it
with `<!-- prose-check:ignore -->`.

### The retired documentation site

`docs/site/` was a Jekyll site published to GitHub Pages. It is retired: the
pages there are now redirect stubs that keep old URLs (including ones published
on docs.dynatrace.com) resolving to `docs/`. Do not add content to
`docs/site/` -- `make docs-check` fails on any page there that is not a stub.
To add or change documentation, edit `docs/` and, if the page is new, add a
redirect stub only if an old published URL needs to point at it.

## Getting Help

- **Questions**: Use [GitHub Discussions](https://github.com/Dynatrace/dtctl/discussions)
- **Bugs**: Open an [issue](https://github.com/Dynatrace/dtctl/issues)
- **Security**: See [SECURITY.md](SECURITY.md)

## License

By contributing to dtctl, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).

---

Thank you for contributing to dtctl! 🎉
