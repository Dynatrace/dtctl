.PHONY: all build clean test test-unit test-integration test-all test-coverage test-update-golden install lint lint-strict fmt markdownlint markdownlint-fix security-scan check release release-snapshot test-sdk vet-sdk lint-sdk sdk-check-deps sdk-check-imports sdk-check docs-generate docs-check stability-manifest stability-compat

# --match "v*" is load-bearing: sdk/ is a second module in this repo and every
# release tag is mirrored to sdk/vX.Y.Z at the same commit. Without the filter
# `git describe` picks the sdk/ tag and local builds report a version like
# "sdk/v0.38.0-55-gd4c6dd8". Release builds are unaffected (GoReleaser sets the
# version from the release tag), so this only ever showed up in make/go install.
VERSION ?= $(shell git describe --tags --match "v*" --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Minimum test coverage percentage. Raise as coverage improves: 60 -> 70 -> 80 -> 85.
COVERAGE_THRESHOLD ?= 60

LDFLAGS = -ldflags "-X github.com/Dynatrace/dtctl/pkg/version.Version=$(VERSION) -X github.com/Dynatrace/dtctl/pkg/version.Commit=$(COMMIT) -X github.com/Dynatrace/dtctl/pkg/version.Date=$(DATE) -s -w"
INSTALL_BIN_DIR ?= $(if $(GOBIN),$(GOBIN),$(shell go env GOPATH)/bin)

MD_LINT_CLI_IMAGE := "ghcr.io/igorshubovych/markdownlint-cli:v0.31.1"

all: build

# Build the binary
build:
	@echo "Building dtctl..."
	@go build $(LDFLAGS) -o bin/dtctl .

# Build for macOS (arm64)
build-darwin-arm64:
	@echo "Building dtctl for darwin/arm64..."
	@env GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o bin/dtctl-darwin-arm64 .

# Convenience target: build for current host OS/ARCH
build-host:
	@echo "Building dtctl for host: $(shell go env GOOS)/$(shell go env GOARCH)..."
	@env GOOS=$(shell go env GOOS) GOARCH=$(shell go env GOARCH) CGO_ENABLED=0 go build $(LDFLAGS) -o bin/dtctl-host .

# Run unit tests (default test target)
test:
	@echo "Running unit tests..."
	@go test -v -race -coverprofile=coverage.out ./...

# Run only unit tests (excludes integration tests)
test-unit:
	@echo "Running unit tests..."
	@go test -v -race -coverprofile=coverage.out ./...

# Run integration tests (requires DTCTL_INTEGRATION_ENV and DTCTL_INTEGRATION_TOKEN)
# Environment variables can be set via:
#   1. .integrationtests.env file (recommended, gitignored)
#   2. Shell environment variables
#   3. Command line: DTCTL_INTEGRATION_ENV=... make test-integration
test-integration:
	@echo "Running integration tests..."
	@# Load .integrationtests.env if it exists
	@if [ -f .integrationtests.env ]; then \
		echo "Loading environment from .integrationtests.env"; \
		export $$(cat .integrationtests.env | grep -v '^#' | xargs); \
	fi; \
	if [ -z "$$DTCTL_INTEGRATION_ENV" ]; then \
		echo "Error: DTCTL_INTEGRATION_ENV not set."; \
		echo ""; \
		echo "Create .integrationtests.env with your credentials:"; \
		echo "  cp .integrationtests.env.example .integrationtests.env"; \
		echo "  # Edit .integrationtests.env with your environment URL and token"; \
		echo ""; \
		echo "Or set environment variables:"; \
		echo "  export DTCTL_INTEGRATION_ENV=https://your-env.apps.dynatrace.com"; \
		echo "  export DTCTL_INTEGRATION_TOKEN=dt0s16.XXX"; \
		exit 1; \
	fi; \
	if [ -z "$$DTCTL_INTEGRATION_TOKEN" ]; then \
		echo "Error: DTCTL_INTEGRATION_TOKEN not set."; \
		echo "See .integrationtests.env.example for setup instructions."; \
		exit 1; \
	fi; \
	go test -v -race -count=1 -tags integration ./test/integration/... ./test/e2e/...

# Regenerate golden files (run after intentional output changes)
test-update-golden:
	@echo "Updating golden files..."
	@go test ./... -update
	@echo "Golden files updated. Review changes with: git diff pkg/output/testdata/ docs/STABILITY.md"

# Regenerate the checked-in stability manifest (docs/STABILITY.md) from the live
# command tree. The same test that writes it also gates it in CI, so a contract
# change that is not regenerated fails the build. Review the diff: a line that
# disappeared from the stable surface is a broken promise, not a cleanup.
stability-manifest:
	@echo "Generating docs/STABILITY.md..."
	@go test ./test/stability/ -update
	@echo "Wrote docs/STABILITY.md. Review with: git diff docs/STABILITY.md"

# Refuse a change that withdraws stable surface. The freshness gate above only
# proves the manifest matches the tree, so a deleted stable flag passes it once
# the manifest is regenerated; this compares against another ref instead.
# It also refuses a since-version that misdates its change (new or changed but
# naming an already-shipped release, or not matching the release being cut),
# and a new flag that is stable only by inheriting its stable command's tier.
# Override the ref locally with: make stability-compat STABILITY_BASE=v0.39.0
STABILITY_BASE ?= origin/main
stability-compat:
	@python3 scripts/stability/check_compat.py --base $(STABILITY_BASE)

# Run all tests (unit + integration)
test-all: test-unit test-integration

# Run tests and enforce coverage threshold
test-coverage:
	@echo "Running tests with coverage..."
	@go test -race -coverprofile=coverage.out -covermode=atomic ./...
	@echo ""
	@echo "=== Package Coverage ==="
	@go tool cover -func=coverage.out | grep -E "^(total|.*\t)" | tail -30
	@echo ""
	@COVERAGE=$$(go tool cover -func=coverage.out | grep total | awk '{print $$3}' | sed 's/%//'); \
	COVERAGE_INT=$${COVERAGE%.*}; \
	echo "Total coverage: $${COVERAGE}% (threshold: $(COVERAGE_THRESHOLD)%)"; \
	if [ "$$COVERAGE_INT" -lt "$(COVERAGE_THRESHOLD)" ]; then \
		echo "FAIL: Coverage $${COVERAGE}% is below the $(COVERAGE_THRESHOLD)% threshold"; \
		exit 1; \
	fi; \
	echo "OK: Coverage meets threshold"

# Install locally
install:
	@echo "Installing dtctl to $(INSTALL_BIN_DIR)..."
	@mkdir -p "$(INSTALL_BIN_DIR)"
	@go build $(LDFLAGS) -o "$(INSTALL_BIN_DIR)/dtctl" .
	@if case ":$$PATH:" in *":$(INSTALL_BIN_DIR):"*) false;; *) true;; esac; then \
		echo ""; \
		echo "dtctl installed, but $(INSTALL_BIN_DIR) is not in PATH."; \
		echo "Add it with:"; \
		echo '  export PATH="$$PATH:$(INSTALL_BIN_DIR)"'; \
	fi

# Clean build artifacts
clean:
	@rm -rf bin/ dist/ coverage.out

# Run linter
lint:
	@golangci-lint run

# Run strict linter (matches CI behavior — zero tolerance)
lint-strict:
	@echo "Checking goimports formatting..."
	@goimports_output=$$(goimports -local github.com/dynatrace-oss -l .); \
	if [ -n "$$goimports_output" ]; then \
		echo "The following files are not properly formatted:"; \
		echo "$$goimports_output"; \
		echo ""; \
		echo "Run 'make fmt' to fix formatting."; \
		exit 1; \
	fi
	@echo "Checking go mod tidy..."
	@go mod tidy && git diff --exit-code go.mod go.sum > /dev/null 2>&1 || \
		(echo "go.mod or go.sum is not tidy. Run 'go mod tidy' and commit the changes." && exit 1)
	@echo "Running golangci-lint (zero tolerance)..."
	@golangci-lint run

# Run security vulnerability scan
security-scan:
	@echo "Running govulncheck..."
	@govulncheck ./...

# Run all checks (lint-strict + security)
check: lint-strict security-scan

# Format code (goimports runs path-based, so it also covers the sdk module)
fmt:
	@go fmt ./...
	@goimports -local github.com/dynatrace-oss -w .

# Markdown linting
markdownlint:
	docker run -v $(CURDIR):/workdir:ro --rm \
		--network=none --read-only --security-opt=no-new-privileges --cap-drop ALL \
		$(MD_LINT_CLI_IMAGE) "**/*.md"

markdownlint-fix:
	docker run -v $(CURDIR):/workdir --rm \
		--network=none --security-opt=no-new-privileges --cap-drop ALL --cap-add DAC_OVERRIDE \
		$(MD_LINT_CLI_IMAGE) "**/*.md" --fix

# Generate reference docs from dtctl's own command catalog.
# Builds the binary, dumps `dtctl commands --full -o json`, and feeds it to
# scripts/gen-docs/gen_all.py, which writes docs/resources/*.md,
# docs/COMMANDS.md, docs/TOKEN_SCOPES.md, and the coverage report scripts/gen-docs/INDEX.md.
# In docs/resources/*.md only the generator-managed block (the three tables:
# Supported operations, Flags, Required token scopes, wrapped in
# <!-- GENERATED:<resource>:start/end --> markers) is derived from the catalog;
# the Overview, Output, Examples, and Notes sections are hand-authored by SMEs
# and are preserved verbatim on regeneration. CI (see
# .github/workflows/docs-generate.yml) fails a PR only if that managed block is
# checked in stale. Requires python3; does not need dtctl on PATH (invokes
# ./bin/dtctl directly) and needs no auth (commands --full is a static dump).
docs-generate: build
	@echo "Generating reference docs from dtctl's command catalog..."
	@tmpdir=$$(mktemp -d) && \
	trap 'rm -rf "$$tmpdir"' EXIT && \
	./bin/dtctl commands --full -o json > "$$tmpdir/commands-full.json" && \
	python3 scripts/gen-docs/gen_all.py "$$tmpdir/commands-full.json" docs && \
	echo "Wrote docs/resources/*.md, docs/COMMANDS.md, docs/TOKEN_SCOPES.md, and scripts/gen-docs/INDEX.md"

# Validate the hand-authored half of the docs against the real CLI.
# docs-generate only guarantees the <!-- GENERATED --> blocks; the prose around
# them (overviews, examples, notes, cross-links) drifts silently. This resolves
# every `dtctl ...` in docs/ to a real command path, checks its long flags
# against that command's own --help, and resolves every relative link and
# #anchor. Needs python3 and the built binary; no auth and no network.
docs-check: build
	@python3 scripts/gen-docs/check_prose.py ./bin/dtctl docs

# Release (using goreleaser)
release:
	@goreleaser release --clean

# Release snapshot (local testing)
release-snapshot:
	@goreleaser release --snapshot --clean

# --- SDK targets ---

# Build and test the SDK module
test-sdk:
	@echo "Running SDK tests..."
	@cd sdk && go test -v -race ./...

vet-sdk:
	@cd sdk && go vet ./...

lint-sdk:
	@cd sdk && golangci-lint run ./...

# Check that the SDK does not depend on forbidden heavy dependencies
sdk-check-deps:
	@echo "Checking SDK dependencies..."
	@cd sdk && go list -m all | grep -E '(cobra|viper|logrus|opentelemetry|asciigraph|toon-go)' && \
	  echo "FORBIDDEN dependency found in sdk/" && exit 1 || echo "sdk/ deps OK"

# Check that the SDK does not import from the CLI module
sdk-check-imports:
	@echo "Checking SDK imports..."
	@cd sdk && ! grep -rE '"github.com/Dynatrace/dtctl[^/]' --include='*.go' . || \
	  (echo "FORBIDDEN: sdk/ imports from CLI module" && exit 1)
	@cd sdk && ! grep -rE '"github.com/Dynatrace/dtctl/(cmd|pkg)' --include='*.go' . || \
	  (echo "FORBIDDEN: sdk/ imports from CLI cmd/ or pkg/" && exit 1)
	@echo "sdk/ imports OK"

# Run all SDK checks
sdk-check: test-sdk vet-sdk sdk-check-deps sdk-check-imports

