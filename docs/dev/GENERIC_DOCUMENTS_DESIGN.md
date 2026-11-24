# Generic Documents Support

**Status:** Implemented
**Created:** 2026-03-09
**Shipped:** 2026-03-10
**Author:** dtctl team

## Problem Statement

dtctl currently exposes dashboards and notebooks as first-class CLI resources (`dtctl get dashboards`, `dtctl get notebooks`), both backed by the same Dynatrace Documents API (`/platform/document/v1/documents`). The CLI explicitly filters by `type=='dashboard'` or `type=='notebook'` when listing.

This works well for these two known types, but the Documents API is type-agnostic -- it stores any document with an arbitrary `type` string. Dynatrace apps can (and do) store their own document types beyond dashboard/notebook. Examples include:

- **Infrastructure views** (Dynatrace-defined app documents)
- **Custom app data** (third-party or customer-built Dynatrace Apps storing configuration/state as documents)
- **Launchpad** configurations
- **Any future document type** Dynatrace or app developers introduce

Users currently have no way to list, inspect, export, or manage these documents through dtctl. They're invisible.

## Goals

1. **Visibility** -- Let users list *all* documents regardless of type, or filter by any type
2. **Interoperability** -- Enable get/describe/delete/create/apply/edit/share/history for any document type, not just dashboard/notebook
3. **Backward compatibility** -- `dtctl get dashboards` and `dtctl get notebooks` continue to work unchanged
4. **Discoverability** -- Help users discover what document types exist in their environment
5. **AI/automation friendly** -- Generic documents work with `--agent` mode for programmatic access

## Non-Goals

- Replacing or deprecating `dtctl get dashboards` / `dtctl get notebooks` (these remain as convenient aliases with type-specific UX)
- Type-specific content validation for unknown document types (we can't know the schema)
- Type-specific output formatting (e.g., tile counts) for unknown types -- only dashboard/notebook get that treatment
- Managing non-document resources (workflows, SLOs, etc.) through this command

## Design

### Command Structure

```bash
# List ALL documents (no type filter)
dtctl get documents
dtctl get doc

# Filter by type
dtctl get documents --type dashboard
dtctl get documents --type notebook
dtctl get documents --type launchpad
dtctl get documents --type my-custom-app:config

# Other filters (same as dashboards/notebooks)
dtctl get documents --name "production"
dtctl get documents --mine

# Get specific document by ID (type-agnostic)
dtctl get document <id>

# Describe any document
dtctl describe document <id>

# Delete any document
dtctl delete document <id>

# Create with explicit type
dtctl create document -f payload.json --type my-custom-type
dtctl create document -f payload.yaml --type launchpad

# Apply (create-or-update) -- type detected from payload or flag
dtctl apply -f document.yaml

# Edit any document
dtctl edit document <id>

# Share any document (uses same sharing API)
dtctl share document <id> --user <user-sso-id>
dtctl unshare document <id> --user <user-sso-id>

# Open any document to the whole environment: a claimable link, or public
# (isPrivate=false). The two are independent; each unshare mode undoes one,
# and `--environment link,public` asks for both. A document has at most one
# link per access level, so a read and a read-write link coexist.
dtctl share document <id> --environment link [--access read|read-write]
dtctl share document <id> --environment public
dtctl unshare document <id> --environment link [--access read|read-write]
dtctl unshare document <id> --environment public

# History for any document
dtctl history document <id>
dtctl restore document <id> --version 3

# Diff any document
dtctl diff document <id> --version 2..5
```

### Relationship to Existing Commands

```
dtctl get dashboards     -->  equivalent to: dtctl get documents --type dashboard
dtctl get notebooks      -->  equivalent to: dtctl get documents --type notebook
dtctl get documents      -->  lists ALL document types (superset)
```

The existing `dashboards` and `notebooks` commands remain as **aliases with enhanced UX**:
- Type-specific tile/section counting in output
- Type-specific URL generation (known app URLs)
- Type-specific validation during create/apply
- Familiar names for the most common use case

The new `documents` command is the **generic escape hatch** for everything else.

### Output

**Table format (`dtctl get documents`):**
```
ID                                    NAME                     TYPE         OWNER                     PRIVATE  CREATED
aaaaaaaa-1111-2222-3333-444444444444  Production Overview      dashboard    user-a@example.invalid    false    2026-01-15
bbbbbbbb-1111-2222-3333-444444444444  Debug Session            notebook     user-b@example.invalid    true     2026-02-01
cccccccc-1111-2222-3333-444444444444  My Launchpad             launchpad    user-a@example.invalid    false    2026-02-10
dddddddd-1111-2222-3333-444444444444  App Config               my-app:cfg   user-c@example.invalid    false    2026-03-01
```

The `TYPE` column is always shown (unlike `dtctl get dashboards` where type is implicit).

**Describe format** includes all metadata fields from the API, with no type-specific enrichment for unknown types.

### Type Discovery

```bash
# List distinct document types in the environment
dtctl get documents --types
```

Output:
```
TYPE         COUNT
dashboard    47
notebook     12
launchpad    3
my-app:cfg   8
```

This issues a single unfiltered `List` call and aggregates types client-side. Useful for discoverability and auditing.

### Implementation Plan

#### 1. New command files

| File | Purpose |
|------|---------|
| `cmd/get_documents_generic.go` | `getDocumentsCmd` -- list/get with `--type` filter |
| `cmd/describe_documents_generic.go` | `describeDocumentCmd` -- describe any document |
| `cmd/create_documents_generic.go` | `createDocumentCmd` -- create with `--type` flag |
| `cmd/delete_documents_generic.go` | `deleteDocumentCmd` -- delete any document |
| `cmd/edit_documents_generic.go` | `editDocumentCmd` -- edit any document |

Or, preferably, **extend the existing `cmd/get_documents.go`** etc. with a new subcommand alongside the existing dashboard/notebook commands.

#### 2. Handler changes (`pkg/resources/document/`)

**Minimal.** The `Handler` and `DocumentFilters` already support arbitrary `Type` strings. The only change needed is allowing `Type` to be empty (list all) or any arbitrary value -- which it already does. No handler changes required.

#### 3. Resolver changes (`pkg/resources/resolver/`)

Add `TypeDocument` to the resolver:

```go
const (
    TypeWorkflow  ResourceType = "workflow"
    TypeDashboard ResourceType = "dashboard"
    TypeNotebook  ResourceType = "notebook"
    TypeDocument  ResourceType = "document"  // generic, searches all document types
)
```

When resolving a generic document by name, search without type filter. This may return mixed types -- the disambiguation prompt should show the type to help the user pick.

#### 4. Apply changes (`pkg/apply/applier.go`)

The applier currently hard-codes `ResourceDashboard` and `ResourceNotebook`. Add a `ResourceDocument` type that:
- Detects type from the `"type"` field in the YAML/JSON payload
- Falls back to dashboard/notebook-specific logic for known types
- Uses generic logic (no tile counting, generic URL) for unknown types

```go
case ResourceDocument:
    docType := detectDocumentType(data)
    return a.applyDocument(data, docType, opts)
```

#### 5. Golden test updates

Add golden test cases for generic document output (table, wide, JSON, YAML, CSV) using a synthetic document with a non-standard type.

#### 6. Registration

Register `documents` / `document` / `doc` as a resource in `cmd/get.go`, `cmd/describe.go`, `cmd/create.go`, `cmd/delete.go`, `cmd/edit.go`.

### Apply / Create Type Detection

When applying or creating a generic document, the type must be known:

1. **Explicit `--type` flag** -- always wins
2. **`type` field in the YAML/JSON payload** -- standard Documents API field
3. **Error** -- if neither is present, fail with a clear message

```bash
# Type from flag
dtctl create document -f data.json --type my-app:config

# Type from payload (payload contains "type": "launchpad")
dtctl apply -f launchpad.yaml

# Error: no type
dtctl create document -f raw.json
# Error: document type is required. Use --type flag or include "type" in the payload.
```

### Safety Considerations

- Generic `delete document` requires the same safety checks as `delete dashboard` / `delete notebook` (already uses `safety.OperationDelete`)
- The `--type` flag on `create` prevents accidental creation of wrong document types
- Unknown document types still go through ownership checks for delete/edit

### What Changes for Existing Users

**Nothing breaks:**
- `dtctl get dashboards` -- unchanged
- `dtctl get notebooks` -- unchanged  
- `dtctl apply -f dashboard.yaml` -- unchanged (type detection still works)
- All existing aliases (`dash`, `db`, `nb`) -- unchanged

**New capabilities:**
- `dtctl get documents` -- see everything
- `dtctl get documents --type <anything>` -- access any document type
- `dtctl describe document <id>` -- works for any document, not just dashboard/notebook
- `dtctl delete document <id>` -- delete any document type

### Edge Cases

1. **Name collision across types**: `dtctl get document "My Doc"` could match a dashboard AND a notebook. The resolver disambiguation prompt shows the type column to let the user pick.

2. **Unknown type in apply**: If a YAML file has `type: "my-custom"` and the user runs `dtctl apply -f file.yaml`, the applier should handle it generically -- skip tile counting, use generic URL pattern, still do create-or-update logic.

3. **Trash**: `dtctl get trash` already lists all document types. No changes needed.

4. **Share/history/restore**: These are document-level operations (not type-specific) -- they work for any document type with no code changes to the handler. Only the command registration needs to accept `document` as a resource.

## Alternatives Considered

### 1. Do nothing -- keep only dashboard/notebook

**Pros:** Simpler, fewer commands
**Cons:** Users can't manage other document types; as Dynatrace adds more document-based features, dtctl falls behind; power users have no escape hatch

### 2. Add each new type as a first-class resource (like we did for dashboard/notebook)

**Pros:** Type-specific UX for each
**Cons:** Doesn't scale; requires a code change for every new document type Dynatrace or app developers introduce; doesn't help with custom app documents

### 3. Generic `documents` only, deprecate `dashboards`/`notebooks` (chosen: no)

**Pros:** Single unified command
**Cons:** Worse UX -- dashboard/notebook users lose convenient aliases, type-specific output, and discoverability. The existing commands are well-established.

### 4. Generic `documents` alongside existing commands (chosen: yes)

**Pros:** Best of both worlds -- convenient aliases for common types, generic access for everything else. No breaking changes. Scales to any document type without code changes.
**Cons:** Slightly more surface area in the CLI. Acceptable trade-off.

## Implementation Phases

### Phase 1: Read-only access ✅ Implemented
- `dtctl get documents [--type TYPE] [--name NAME] [--mine]`
- `dtctl get document <id>`
- `dtctl describe document <id>`
- `dtctl get documents --types` (type discovery)
- Golden tests

### Phase 2: Mutating operations ✅ Implemented
- `dtctl delete document <id>`
- `dtctl create document -f FILE --type TYPE`
- `dtctl edit document <id>`
- Safety checks (ownership-aware, same as dashboard/notebook)

### Phase 3: History & restore ✅ Implemented
- `dtctl history document <id>`
- `dtctl restore document <id> <version>`

### Phase 3b: Collaboration & diff (not yet implemented)
- `dtctl share document <id>`
- `dtctl diff document <id>`

## Open Questions

1. **Should `dtctl get documents` be the default (no type filter), or should it require `--type`?**
   Recommendation: default to listing all. The TYPE column disambiguates. This is more useful for discoverability.

2. **Should we show a hint when users run `dtctl get dashboards` that `dtctl get documents` exists?**
   Recommendation: No. Don't clutter existing UX. Mention it in `dtctl commands` and docs only.

3. **Should resolver support generic document name resolution?**
   Recommendation: Yes, but only when the user explicitly uses `document` as the resource type. Dashboard/notebook resolution stays scoped to their type.

## References

- Dynatrace Documents API: `/platform/document/v1/documents`
- Implementation: `pkg/resources/document/document.go`, `cmd/get_documents.go`, `cmd/create_documents.go`, `cmd/edit_documents.go`, `cmd/describe_documents.go`, `cmd/history.go`, `cmd/restore.go`
- Resolver: `pkg/resources/resolver/resolver.go` (`TypeDocument`)
- API design principles: `docs/dev/API_DESIGN.md` (see § 1. Documents (Generic))
