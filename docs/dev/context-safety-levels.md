# Context Safety Levels

## Overview

Context safety levels provide **client-side** protection against accidental destructive operations by binding safety constraints to connection contexts. This allows you to configure production contexts with strict safety while keeping development contexts permissive.

**Key Principle**: The safety level determines **what operations are allowed**. Confirmation behavior is **consistent** across all levels.

> **Important: Client-Side Only**
>
> Safety levels are enforced by dtctl on your local machine. They are a convenience feature to prevent accidental mistakes, **not a security boundary**. A determined user can bypass them by using the API directly.
>
> **For actual security, use proper API token scopes.** Configure your Dynatrace API tokens with the minimum required permissions. See [TOKEN_SCOPES.md](../TOKEN_SCOPES.md) for:
> - Complete scope lists for each safety level (copy-pasteable)
> - Detailed breakdown by resource type
> - Token creation instructions

## Safety Levels

From safest to most permissive:

| Level | Description | Use Case |
|-------|-------------|----------|
| `readonly` | No modifications allowed | Production monitoring, troubleshooting, read-only API tokens |
| `readwrite-mine` | Can create/update/delete own resources only | Personal development, sandbox environments |
| `readwrite-all` | Can modify all resources (no bucket deletion) | Team environments, shared staging, production administration |
| `dangerously-unrestricted` | All operations including data deletion | Development, emergency recovery, bucket management |

**Default**: If no safety level is specified, `readwrite-all` is used. This matches pre-safety-level behavior and avoids breaking existing workflows.

## Configuration

### Context Structure

```yaml
contexts:
- name: production
  context:
    environment: https://abc123.apps.dynatrace.com
    token-ref: prod-token
    safety-level: readwrite-all
    description: "Production environment - handle with care"

- name: dev-sandbox
  context:
    environment: https://dev789.live.dynatrace.com
    token-ref: dev-token
    safety-level: dangerously-unrestricted
    description: "Personal dev environment - anything goes"
```

### Per-Invocation Safety Level

An embedded caller can supply the safety level per invocation via
`cmd.Session.SafetyLevel` instead of a config-file context; an empty value
selects the `readwrite-all` default. The session materializes as a single
synthetic context named `session`, so the block messages below read
`Context 'session' does not allow ...`. See
[SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md).

## Operation Permission Matrix

| Safety Level | Read | Create | Update Own | Update Shared | Delete Own | Delete Shared | Delete Bucket |
|-------------|------|--------|------------|---------------|------------|---------------|---------------|
| `readonly` | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| `readwrite-mine` | ✅ | ✅ | ✅ | ❌ | ✅ | ❌ | ❌ |
| `readwrite-all` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| `dangerously-unrestricted` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

**Note**: "Own" vs "Shared" distinction requires ownership detection (see Implementation Notes).

## Confirmation Behavior

**Consistent across all safety levels** - once an operation is permitted by the safety level:

### Standard Operations (create, update, delete resources)

```bash
# Interactive prompt with details
dtctl delete dashboard my-dashboard

# Output:
# Resource Type: dashboard
# Name: My Dashboard
# ID: abc-123-def
# Are you sure? [y/N]:

# Skip confirmation with flag
dtctl delete dashboard my-dashboard -y
```

### Data Destruction Operations (buckets, purge)

```bash
# Requires typing the resource name exactly
dtctl delete bucket logs-bucket

# Output:
# ⚠️  WARNING: This operation is IRREVERSIBLE and will delete all data
# Type the bucket name 'logs-bucket' to confirm: _

# Or use confirmation flag
dtctl delete bucket logs-bucket --confirm=logs-bucket
```

### Dry-Run Support

All destructive operations support `--dry-run`:

```bash
dtctl delete dashboard my-dashboard --dry-run
# Output: Would delete dashboard 'My Dashboard' (abc-123-def)

dtctl delete bucket logs-bucket --dry-run
# Output: Would permanently delete bucket 'logs-bucket' and all its data
```

## Usage Examples

### Example 1: Production Read-Only Access

```bash
# Setup read-only production access
dtctl config set-context prod-viewer \
  --environment https://prod.dynatrace.com \
  --token-ref readonly-token \
  --safety-level readonly

dtctl config use-context prod-viewer

# Or use the ctx shortcut:
# dtctl ctx prod-viewer

# Allowed
dtctl get dashboards
dtctl query "fetch logs | limit 100"
dtctl describe workflow deploy-pipeline

# Blocked
dtctl delete dashboard old-dash
# Error: Context 'prod-viewer' (readonly) does not allow delete operations
```

### Example 2: Team Environment

```bash
# Setup team environment with full resource access
dtctl config set-context prod-team \
  --environment https://prod.dynatrace.com \
  --token-ref team-token \
  --safety-level readwrite-all

dtctl config use-context prod-team

# Allowed (with confirmation)
dtctl delete dashboard old-dashboard
dtctl delete workflow deprecated-workflow
dtctl edit settings app-config

# Blocked - requires dangerously-unrestricted
dtctl delete bucket temp-bucket
# Error: Context 'prod-team' (readwrite-all) does not allow bucket deletion
# Bucket operations require 'dangerously-unrestricted' safety level
```

### Example 3: Personal Development

```bash
# Setup personal dev context (default safety level)
dtctl config set-context my-dev \
  --environment https://dev.dynatrace.com \
  --token-ref dev-token \
  --safety-level readwrite-mine

dtctl config use-context my-dev

# Can modify own resources
dtctl edit dashboard my-dashboard
dtctl delete notebook my-experiment

# Blocked from modifying others' resources
dtctl delete dashboard team-dashboard
# Error: Context 'my-dev' (readwrite-mine) does not allow modifying resources owned by others
```

### Example 4: Unrestricted Development

```bash
# Setup unrestricted dev access (use with caution!)
dtctl config set-context dev-full \
  --environment https://dev.dynatrace.com \
  --token-ref dev-token \
  --safety-level dangerously-unrestricted

dtctl config use-context dev-full

# Everything allowed (with appropriate confirmations)
dtctl delete bucket test-bucket --confirm=test-bucket
dtctl delete dashboard any-dashboard -y
```

## Context Management Commands

```bash
# Set or update safety level
dtctl config set-context <name> --safety-level <level>

# List contexts with safety info
dtctl config get-contexts
dtctl config get-contexts -o wide  # Shows safety level details

# View detailed context info
dtctl config describe-context <name>
```

### Logging In

`dtctl auth login` is the other writer of a context's level, and the browser
flow requests the OAuth scopes of the level (`pkg/auth`, `GetScopesForSafetyLevel`).
The rules live in `loginSafetyLevels` in `cmd/auth.go`:

- `--safety-level` given: the flag is saved. An empty value is a usage error.
- Not given, context exists: the stored level is kept, and a context that
  stores none is left without one. A stored value that is not a level is
  refused before a token is requested.
- Not given, new context: the default is saved.

The scopes follow the level dtctl *enforces*, which in an auto-discovered
project config is the declared level clamped to the global binding of the token
the login writes (`Config.EffectiveSafetyLevelFor`). The declared level is what
gets saved; the file is not rewritten to the clamped one.

A login replaces the stored token, which is keyed by OAuth tier and token name.
Before the browser opens, the login names every other context on the same tier
and token name, in the loaded config and in the global one, whose level requests
a scope the login does not. The client credentials grant does not request the
level's scopes, so there the login only says that the shared token is replaced.

## Implementation Notes

### Ownership Detection

For `readwrite-mine` level (own vs shared resources):

1. **Attempt 1**: Call `/platform/metadata/v1/user` to get current user ID
2. **Attempt 2**: Extract user ID from JWT token (no API call)
3. **Compare**: Check if resource owner matches current user ID
4. **Fallback**: If ownership cannot be determined, assume shared (safer)

### Safety Check Flow

```
Operation Requested
        ↓
[Check Safety Level]
        ↓ (allowed)
[Check Ownership if needed]
        ↓ (permitted)
[Confirmation Prompt]
        ↓ (confirmed)
[Execute Operation]
```

### Error Messages

Clear, actionable error messages:

```
Operation not allowed:
   Context: production (readwrite-all)
   Reason: Bucket deletion requires 'dangerously-unrestricted' safety level

Suggestions:
  • Switch to a dangerously-unrestricted context
  • Contact your administrator
```

### Audit Logging (Future)

Context safety provides a foundation for audit logging:

```json
{
  "timestamp": "2026-01-15T10:30:00Z",
  "context": "production",
  "safety_level": "readwrite-all",
  "operation": "delete",
  "resource_type": "dashboard",
  "resource_id": "abc-123",
  "user": "user@company.com",
  "bypassed_safety": false,
  "confirmation_method": "interactive"
}
```

## Migration Path

### Existing Configurations

Existing contexts without a safety level will default to `readwrite-all`:

```yaml
# Existing config (no changes needed)
contexts:
- name: production
  context:
    environment: https://prod.dynatrace.com
    token-ref: prod-token
    # safety-level defaults to: readwrite-all
```

### Gradual Adoption

1. **Phase 1**: Add safety levels to critical contexts (production)
2. **Phase 2**: Review and adjust safety levels based on usage
3. **Phase 3**: Enable audit logging (future)

### Backward Compatibility

- All existing commands continue to work without changes
- Safety checks are additive (don't break existing workflows)
- Flags (`-y`, `--force`) continue to work as before
- No breaking changes to config format

## Design Principles

1. **Client-side protection** - No server changes required
2. **Explicit over implicit** - Safety levels must be consciously set
3. **Fail safe** - Default to more restrictive when uncertain
4. **Consistent UX** - Same confirmation patterns across all operations
5. **Clear feedback** - Always explain why something was blocked
6. **Escape hatches** - Provide override mechanisms for exceptional cases
7. **Auditability** - Design enables future audit logging

## Related Documents

- [TOKEN_SCOPES.md](../TOKEN_SCOPES.md) - **Required API token scopes for each safety level**
- [QUICK_START.md](../QUICK_START.md) - Configuration examples and usage
- `config-example.yaml` - Example configuration with safety levels
- `pkg/config/config.go` - Config structure definition
- `pkg/safety/checker.go` - Safety validation logic
- `cmd/config.go` - Context management commands
