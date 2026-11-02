---
name: pr-review
description: Perform a thorough quality review of a pull request or feature branch before merging. Use this skill whenever the user asks to review a PR, check if code is production-ready, assess quality, verify docs are updated, or asks "is this ready to merge?", "review this PR", "check quality", "is this production ready?", or similar. Also use when reviewing your own work before submitting.
---

# PR Quality Review

Perform a comprehensive quality review of a pull request or feature branch. This skill covers production readiness, code quality, UX, documentation, tests, and safety.

The review is structured as a checklist across seven dimensions. For each dimension, investigate the actual code and report specific findings -- not just "looks good" but concrete observations with file paths and line numbers.

## Getting Started

First, understand the scope of the change:

```bash
# What branch are we on, what's the base?
git branch --show-current
git log main..HEAD --oneline

# What files changed?
git diff main...HEAD --stat

# Full diff for review
git diff main...HEAD
```

If reviewing a remote PR, fetch it first:

```bash
gh pr view <number> --json title,body,files
gh pr diff <number>
```

Read the PR description and all commits to understand the intent before diving into code.

## Review Dimensions

Work through each dimension below. For each one, report a status:
- **Pass** -- meets the bar, no issues
- **Needs work** -- specific issues found (list them)
- **N/A** -- not applicable to this change

### 1. Production Readiness

Does this code behave correctly and handle failure gracefully?

- **Error handling**: Are all errors checked? Are they wrapped with context (`fmt.Errorf("context: %w", err)`)? Do they surface actionable messages to users?
- **Edge cases**: Empty inputs, nil values, missing config, network failures, API rate limits, large datasets, pagination boundaries
- **Safety checks**: All mutating commands (create, edit, apply, delete, update) must include safety checks after `LoadConfig()` and before client operations. Pattern:
  ```go
  checker, err := NewSafetyChecker(cfg)
  if err := checker.CheckError(safety.OperationXXX, safety.OwnershipUnknown); err != nil { return err }
  ```
  Verify correct operation type. Skip only in dry-run paths.
- **No stdout in library code**: `pkg/` must return data, not print. Only `cmd/` handles output.
- **No hardcoded secrets or customer data**: No real names, env IDs, tokens, or emails in code or tests. Use `@example.invalid` for emails (RFC 2606).

### 2. Code Quality

Is the code clean, idiomatic, and maintainable?

- **Go idioms**: Follows [Effective Go](https://golang.org/doc/effective_go.html) and [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- **Naming**: Descriptive names, Go conventions (camelCase unexported, PascalCase exported), `-er` suffix for interfaces
- **File size**: Files should be under 500 lines. Large files should be split.
- **Imports**: Standard library first, then third-party, then internal (`github.com/Dynatrace/dtctl`)
- **Duplication**: Look for copy-pasted code that should be extracted into helpers
- **Comments**: Exported functions/types documented. Comments explain "why" not "what".
- **Consistent patterns**: New code should follow existing patterns in the codebase. Check similar resources in `pkg/resources/` for reference implementations.

Run the linter to catch issues the eye might miss:

```bash
make lint-strict
```

### 3. User Experience

Does this feel right from the user's perspective?

- **Command naming**: Follows the `dtctl <verb> <resource>` pattern. No custom query flags -- use DQL passthrough.
- **Output formatting**: Table output is readable, columns make sense, no misalignment. JSON/YAML output is clean.
- **Error messages**: Clear, actionable, suggest next steps. In agent mode (`-A`), errors are structured JSON with machine-readable codes.
- **Interactive behavior**: Name resolution, disambiguation prompts work. `--plain` disables interactive behavior.
- **Help text**: Command has a `Short` description, `Long` description, and `Example` field. Parent verb commands have examples.
- **Aliases**: Resource has sensible aliases (e.g., `wf` for workflow, `dash` for dashboard).
- **Agent mode**: Commands support `--agent` envelope with contextual suggestions. Test with `-A -o json`.
- **Color control**: Respects `NO_COLOR`, `FORCE_COLOR`, `--plain`, and TTY detection.

Try running the actual commands to see how they feel:

```bash
# Does the help text look good?
dtctl <command> --help

# Does table output look right?
dtctl <command> --plain

# Does agent mode work?
dtctl <command> -A
```

### 4. Test Coverage

Are the changes well-tested?

- **Unit tests**: New functions have tests. Table-driven tests preferred.
- **Coverage targets**: 70% minimum overall, 80% for new packages, 90% for critical packages (`pkg/client`, `pkg/config`).
- **Edge case tests**: Not just happy paths -- test error conditions, empty inputs, boundary values.
- **Mock server guards**: Paginated mock servers must reject invalid parameter combinations (e.g., `page-size` + `page-key`). Settings API mocks must also reject `schemaIds`/`scopes` with `nextPageKey`.
- **Golden tests**: If output formatting changed or a new resource was added, golden files must be updated. Check:
  ```bash
  go test ./pkg/output/ -run TestGolden
  ```
  Golden tests use real production structs from `pkg/resources/*` -- never test-only duplicates.
- **E2E tests**: Integration scenarios in `test/e2e/` for new resources or complex workflows.

```bash
# Run full suite
go test ./...

# Check coverage
make test-coverage
```

### 5. Documentation

Is the change properly documented for users and contributors?

**Always required:**
- **Conventional commit title**: The PR title (and squash-merge commit) must be a valid conventional commit (`feat: ...`, `fix: ...`, `feat!: ...` for breaking changes) — release-please derives the version bump and GitHub Release notes from it. There is no CHANGELOG.md to edit.

**Required for new features:**
- **README.md**: Updated if the feature is user-facing and significant (new resource type, new command category)
- **docs/QUICK_START.md**: Usage examples for major new features
- **docs/dev/IMPLEMENTATION_STATUS.md**: Feature matrix rows updated
- **docs/dev/API_DESIGN.md**: Design patterns documented if introducing new conventions
- **docs/TOKEN_SCOPES.md**: New scopes documented if the feature requires additional API permissions

**Required for new resources:**
- **Resource-specific doc page** in `docs/resources/`, with a `PLAN` entry in `scripts/gen-docs/gen_all.py` so the generator owns its tables
- **Command reference**: regenerated, not hand-edited — `make docs-generate` rewrites `docs/COMMANDS.md`

**Required for new AI agent support:**
- `README.md`, `docs/QUICK_START.md`, `docs/dev/API_DESIGN.md`, `docs/dev/IMPLEMENTATION_STATUS.md` (all four)

### 6. Documentation checks

There is no documentation website: `docs/site/` is retired and holds only
redirect stubs that keep previously published URLs resolving to `docs/`. A PR
that adds a page under `docs/site/_docs/` is wrong.

**Check:**
- **Generated blocks untouched by hand**: no edits inside `<!-- GENERATED:x:start/end -->`. If the catalog changed, `make docs-generate` was run and its output committed.
- **New resource mapped**: a new catalog resource appears in `scripts/gen-docs/INDEX.md` under a page, not in the unmapped list.
- **Prose verified**: `make docs-check` passes — every documented `dtctl ...` resolves and every flag exists. CI runs this, but a reviewer should confirm new examples were actually run, not invented.
- **Links**: relative links resolve and anchors exist (`make docs-check` covers this).

### 7. PR Description

Is the PR itself well-described?

- **Title**: Clear, follows conventional commit style (`feat: ...`, `fix: ...`)
- **Summary**: Explains what changed and why (not just what files were touched)
- **Related issues**: References issues with `Closes #NNN` or `Fixes #NNN`
- **Breaking changes**: Called out explicitly if any
- **Testing section**: Describes how the change was tested
- **UX examples**: Before/after CLI output for user-facing changes

## Review Output

After completing the review, provide a summary in this format:

```
## PR Review: <title>

| Dimension | Status | Notes |
|-----------|--------|-------|
| Production readiness | Pass/Needs work | ... |
| Code quality | Pass/Needs work | ... |
| User experience | Pass/Needs work | ... |
| Test coverage | Pass/Needs work | ... |
| Documentation | Pass/Needs work | ... |
| GitHub Pages | Pass/Needs work/N/A | ... |
| PR description | Pass/Needs work | ... |

### Issues Found
1. **[Dimension]** file:line -- description of issue
2. ...

### Suggestions (non-blocking)
1. ...

### Verdict
Ready to merge / Needs revisions (list blockers)
```

Be direct and specific. Reference exact file paths and line numbers. Distinguish between blocking issues (must fix) and suggestions (nice to have). Don't pad the review with praise -- focus on what needs attention.
