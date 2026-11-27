# Future Features Implementation Plan

## Overview

This document outlines the implementation plan for adding new API categories to dtctl:
1. Platform Management
2. State Management for Apps
3. Grail Filter Segments
4. Grail Fieldsets
5. Grail Resource Store

## Implementation Order

We'll implement in this order (simple to complex):

1. **Platform Management** - Read-only, simple endpoints
2. **State Management** - Delete operations only, simple
3. **Grail Fieldsets** - Standard CRUD pattern
4. **Grail Filter Segments** - Standard CRUD pattern
5. **Grail Resource Store** - Standard CRUD pattern

## 1. Platform Management ✅ IMPLEMENTED

> **Status**: Implemented — see `sdk/api/platform/`, `pkg/resources/platform/`, `cmd/get_platform.go`, `cmd/describe_platform.go`

### Endpoints Implemented

- `GET /platform/management/v1/environment` - Get environment info
- `GET /platform/management/v1/environment/license` - Get license info
- `GET /platform/management/v1/environment/license/settings` - Get license feature settings (optional `key` filter)

### Commands

```bash
# Get environment information
dtctl get environment
dtctl describe environment

# Get license information
dtctl get license
dtctl describe license

# Get license feature settings
dtctl get license-settings
dtctl get license-settings AUTOMATION   # filter to one key
```

### Data Structures (actual API shapes)

```go
type EnvironmentInfo struct {
    EnvironmentID string    `json:"environmentId"`
    Type          string    `json:"type"`
    State         string    `json:"state"`
    CreateTime    time.Time `json:"createTime"`
    BlockTime     time.Time `json:"blockTime"`
}

type License struct {
    Trial                bool `json:"trial"`
    PlatformSubscription bool `json:"platformSubscription"`
}

type LicenseSetting struct {
    Key   string `json:"key"`
    Value string `json:"value"`
}
```

### Scope Required
- `app-engine:apps:run` (already requested at login; see TOKEN_SCOPES.md for the `platform-management:environments:read` narrow-scope alternative)

---

## 2. State Management for Apps

### Endpoints to Implement

- `DELETE /platform/state-management/v1/{appId}/app-states` - Delete all app states
- `DELETE /platform/state-management/v1/{appId}/user-app-states` - Delete all user app states
- `DELETE /platform/state-management/v1/{appId}/user-app-states/self` - Delete own user app states

### Commands

```bash
# Delete all app states for an app
dtctl delete app-state <app-id>

# Delete all user app states for an app (admin)
dtctl delete user-app-states <app-id>

# Delete own user app states for an app
dtctl delete user-app-states <app-id> --self
```

### Files to Create/Modify

- `pkg/resources/statemanagement/statemanagement.go` - Handler implementation
- `cmd/get.go` - No get commands (delete only)
- `cmd/delete.go` - Add delete commands

### Scopes Required
- `state-management:app-states:delete`
- `state-management:user-app-states:delete-all`
- `state-management:user-app-states:delete`

---

## 3. Grail Fieldsets

### Endpoints to Implement

- `GET /platform/storage/management/v1/fieldsets` - List fieldsets
- `GET /platform/storage/management/v1/fieldsets/{fieldsetName}` - Get fieldset
- `POST /platform/storage/management/v1/fieldsets` - Create fieldset
- `PUT /platform/storage/management/v1/fieldsets/{fieldsetName}` - Update fieldset
- `DELETE /platform/storage/management/v1/fieldsets/{fieldsetName}` - Delete fieldset

### Commands

```bash
# List all fieldsets
dtctl get fieldsets

# Get a specific fieldset
dtctl get fieldset <fieldset-name>
dtctl describe fieldset <fieldset-name>

# Create a fieldset from YAML
dtctl create fieldset -f fieldset.yaml
dtctl apply -f fieldset.yaml

# Edit a fieldset
dtctl edit fieldset <fieldset-name>

# Delete a fieldset
dtctl delete fieldset <fieldset-name>
```

### Files to Create/Modify

- `pkg/resources/grail/fieldsets.go` - Handler implementation
- `cmd/get.go` - Add getFieldsetsCmd
- `cmd/describe.go` - Add describeFieldsetCmd
- `cmd/create.go` - Add createFieldsetCmd
- `cmd/edit.go` - Add editFieldsetCmd
- `cmd/delete.go` - Add deleteFieldsetCmd
- `cmd/apply.go` - Add fieldset support

### Data Structures

```go
type Fieldset struct {
    Name        string   `json:"name" yaml:"name"`
    DisplayName string   `json:"displayName,omitempty" yaml:"displayName,omitempty"`
    Description string   `json:"description,omitempty" yaml:"description,omitempty"`
    Fields      []string `json:"fields" yaml:"fields"`
}
```

### Scopes Required
- `storage:fieldsets:read`
- `storage:fieldsets:write`
- `storage:fieldsets:delete`

---

## 4. Grail Filter Segments ✅ IMPLEMENTED

> **Status**: Implemented — see `pkg/resources/segment/`, `cmd/get_segments.go`, `cmd/describe_segments.go`, `cmd/create_segments.go`, `cmd/edit_segments.go`, and query-time `--segment`/`--segments-file` flags in `cmd/query.go`. Full design in [SEGMENTS_DESIGN.md](SEGMENTS_DESIGN.md).

### Endpoints to Implement

- `GET /platform/storage/filter-segments/v1/filter-segments` - List segments
- `GET /platform/storage/filter-segments/v1/filter-segments/{segmentName}` - Get segment
- `POST /platform/storage/filter-segments/v1/filter-segments` - Create segment
- `PUT /platform/storage/filter-segments/v1/filter-segments/{segmentName}` - Update segment
- `DELETE /platform/storage/filter-segments/v1/filter-segments/{segmentName}` - Delete segment

### Commands

```bash
# List all filter segments
dtctl get filter-segments
dtctl get segments

# Get a specific segment
dtctl get segment <segment-name>
dtctl describe segment <segment-name>

# Create a segment from YAML
dtctl create segment -f segment.yaml
dtctl apply -f segment.yaml

# Edit a segment
dtctl edit segment <segment-name>

# Delete a segment
dtctl delete segment <segment-name>
```

### Files to Create/Modify

- `pkg/resources/grail/segments.go` - Handler implementation
- `cmd/get.go` - Add getSegmentsCmd
- `cmd/describe.go` - Add describeSegmentCmd
- `cmd/create.go` - Add createSegmentCmd
- `cmd/edit.go` - Add editSegmentCmd
- `cmd/delete.go` - Add deleteSegmentCmd
- `cmd/apply.go` - Add segment support

### Data Structures

```go
type FilterSegment struct {
    Name        string `json:"name" yaml:"name"`
    DisplayName string `json:"displayName,omitempty" yaml:"displayName,omitempty"`
    Description string `json:"description,omitempty" yaml:"description,omitempty"`
    Filter      string `json:"filter" yaml:"filter"`
}
```

### Scopes Required
- `storage:filter-segments:read`
- `storage:filter-segments:write`
- `storage:filter-segments:delete`

---

## 5. Grail Resource Store

### Endpoints to Implement

- `GET /platform/storage/management/v1/resources` - List resources
- `GET /platform/storage/management/v1/resources/{resourceName}` - Get resource
- `POST /platform/storage/management/v1/resources` - Create resource
- `PUT /platform/storage/management/v1/resources/{resourceName}` - Update resource
- `DELETE /platform/storage/management/v1/resources/{resourceName}` - Delete resource

### Commands

```bash
# List all resources
dtctl get resources
dtctl get resource-store

# Get a specific resource
dtctl get resource <resource-name>
dtctl describe resource <resource-name>

# Create a resource from file
dtctl create resource -f resource.yaml
dtctl apply -f resource.yaml

# Edit a resource
dtctl edit resource <resource-name>

# Delete a resource
dtctl delete resource <resource-name>
```

### Files to Create/Modify

- `pkg/resources/grail/resourcestore.go` - Handler implementation
- `cmd/get.go` - Add getResourcesCmd
- `cmd/describe.go` - Add describeResourceCmd
- `cmd/create.go` - Add createResourceCmd
- `cmd/edit.go` - Add editResourceCmd
- `cmd/delete.go` - Add deleteResourceCmd
- `cmd/apply.go` - Add resource support

### Data Structures

```go
type Resource struct {
    Name        string                 `json:"name" yaml:"name"`
    DisplayName string                 `json:"displayName,omitempty" yaml:"displayName,omitempty"`
    Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
    Content     map[string]interface{} `json:"content" yaml:"content"`
    Version     string                 `json:"version,omitempty" yaml:"version,omitempty"`
}
```

### Scopes Required
- `storage:resources:read`
- `storage:resources:write`
- `storage:resources:delete`

---

## Testing Strategy

For each feature:

1. **Unit Tests** - Test handlers in isolation
2. **Integration Tests** - Test against real Dynatrace environment (optional, in `test/integration/`)
3. **E2E Tests** - Test CLI commands end-to-end (in `test/e2e/`)

### Test Files to Create

- `pkg/resources/platform/platform_test.go`
- `pkg/resources/statemanagement/statemanagement_test.go`
- `pkg/resources/grail/fieldsets_test.go`
- `pkg/resources/grail/segments_test.go`
- `pkg/resources/grail/resourcestore_test.go`

---

## Documentation Updates

### Files to Update

1. **README.md** - Add new resources to "What Can It Do?" table
2. **QUICK_START.md** - Add examples for new resources
3. **API_DESIGN.md** - Document new commands and flags
4. **IMPLEMENTATION_STATUS.md** - Mark features as complete

### New Documentation

Create usage examples for each resource type with common workflows.

---

## Implementation Checklist

### Phase 1: Simple Read-Only Resources (Day 1)
- [x] Platform Management implementation
- [x] Platform Management commands
- [x] Platform Management tests
- [x] Platform Management documentation

### Phase 2: Simple Delete-Only Resources (Day 1)
- [ ] State Management implementation
- [ ] State Management commands
- [ ] State Management tests
- [ ] State Management documentation

### Phase 3: Grail CRUD Resources (Day 2-3)
- [ ] Grail Fieldsets implementation
- [ ] Grail Fieldsets commands
- [ ] Grail Fieldsets tests
- [x] Grail Filter Segments implementation
- [x] Grail Filter Segments commands
- [x] Grail Filter Segments tests
- [ ] Grail Resource Store implementation
- [ ] Grail Resource Store commands
- [ ] Grail Resource Store tests
- [ ] Grail resources documentation

### Phase 4: Final Polish (Day 4)
- [ ] Update all documentation
- [ ] Integration tests
- [ ] E2E tests
- [ ] Shell completion updates
- [ ] Code review and refactoring

---

## Command Naming Conventions

Following kubectl conventions:

- **Singular for get specific**: `dtctl get environment`, `dtctl get license`
- **Plural for list**: `dtctl get fieldsets`, `dtctl get segments`
- **Short aliases**: `seg` for segments, `fs` for fieldsets
- **Consistent verbs**: get, describe, create, edit, delete, apply

---

## Code Structure Guidelines

### Handler Interface Pattern

All resource handlers should implement common patterns:

```go
type Handler struct {
    client *client.Client
}

func NewHandler(c *client.Client) *Handler {
    return &Handler{client: c}
}

func (h *Handler) List() ([]Resource, error) { ... }
func (h *Handler) Get(id string) (*Resource, error) { ... }
func (h *Handler) Create(resource *Resource) (*Resource, error) { ... }
func (h *Handler) Update(id string, resource *Resource) (*Resource, error) { ... }
func (h *Handler) Delete(id string) error { ... }
```

### Error Handling

Use the existing error patterns from `pkg/client/errors.go`

### Output Formatting

Support all existing output formats:
- Table (default)
- JSON (`-o json`)
- YAML (`-o yaml`)
- Wide (`-o wide`)

### Configuration

Support global flags:
- `--context` - Select environment
- `--output` - Output format
- `--verbose` - Verbose output
- `--dry-run` - Dry run mode
- `--chunk-size` - Pagination

---

## Profiling follow-ups

Known limits of `exec profiling`, `pkg/resources/profiling` and
`sdk/api/codelevelanalysis`, recorded so that a later change or review can
start from them. None is a planned API category; each is a defect or a gap
with a known place in the code.

- **Samples on frames without a label are reported as absent.** A result whose
  running samples sit on an unlabelled root gives `no_samples` or
  `no_state_samples` although the process was sampled (`collapsed.go`,
  `functions.go`). With `--app-only` the functions view has the same root: its
  percentages are shares of the samples on application frames, and the view
  has no second figure for every sample in the window. Cure: count the sampled
  mass before any frame is filtered, and report both.
- **A stack is one `;`-joined string.** A frame whose label contains `;`
  merges with a real path of the same text, and a consumer cannot recover the
  frames (`collapsed.go`). Cure: an additive `Frames []string` on the rows and
  a fold keyed by the frame sequence.
- **Views recompute the call tree on every call.** The stack fold copies the
  path at every node, so it is quadratic in depth, and an empty per-function
  view runs the stack fold again to find its reason (`collapsed.go`,
  `analysis.go`). Fine for one command run; to be cured (a shared path buffer,
  the reason from the first walk) before a program re-reads views on a key
  press.
- **Two retry layers for the two analysis requests.** The session client
  retries a 429 three times within seconds inside each of the SDK's own
  throttled submits and polls, and those inner retries ignore `Retry-After`
  (`sdk/session/client.go`, `codelevelanalysis.go`): up to 16 submits and 160
  polls. A terminal status whose body fails to read is also polled out and
  loses its status. Cure: one retry policy for these requests that honours
  `Retry-After` once and keeps a status it has seen.
- **No progress hook and no injectable timing.** A caller cannot observe the
  throttle waits and the polls, and the poll and throttle timings are package
  variables only the SDK's own tests can shorten. `Response.Status` and
  `FocusRow.Relation` are untyped strings.
- **An unclassified analysis error has no display-safe message.** The text of
  an error `ClassifyError` reports false for is the SDK's and can hold the
  request URL or the token of the task.
- **Any JSON object passes for a completed analysis.** A 200 whose body is
  `{}` or an object of another shape has no rows in any view and reads as a
  process without samples (`checkResult` in `analysis.go`). The keys each view
  reads are known, but what the API answers for a process that really was idle
  (an empty tree, `{}` or 204) has not been observed, so refusing an object
  without those keys could refuse a true answer. Needs one live observation
  before the rule is written.
- **Errors without a type are read by their text, in every command.** A 404
  whose body quotes a failed token refresh gets the log-in hint, and a 200
  with invalid JSON is coded `validation_error` (`classifyGenericError`,
  `isTokenRefreshError` in `cmd/root.go`). `exec profiling` types the errors
  it words itself; `cmd/root.go` knows that type by name in four places, which
  an interface the root asks for would replace.
  Three poll errors and the submit's transport error are still untyped, so
  their code follows the task token or the entity id their text quotes (a
  token that holds `config` gives `config_error`); only the timeout was given
  a fixed code. `suggest.ParseFlagError` and `suggest.ParseCommandError`
  rebuild a bare error for any text that holds `unknown flag` or `unknown
  command`, which drops the chain of every command's error. And a refused
  value is `validation_error` with exit code 1 here, where the root ties that
  code to exit code 2: kept from 0.47.0, to be decided with the interface.
- **The envelope's window span rounds to minutes.** A window under 30 seconds
  shows `"span":"0s"` beside correct bounds (`spanString` in
  `cmd/query_window.go`, shared by every command with a window).
- **The handler keeps the uncompacted response** for the life of a result,
  data points included, and the three tree walks recurse one Go frame per
  level of the tree.
- **A Node.js frame with a Windows path** keeps its `node_modules\` prefix and
  gets a `\/` in its label (`nodeLabel` in `nodes.go`). Whether the API sends
  backslashes is not known; a cure changes labels people search for.
- **Allocation frames of the agent's bridge classes survive `appOnly`.**
  `isAgentFrame` reads `className`, which an allocation node does not have;
  its name-only match covers the prefixes alone, and the comment above it says
  otherwise.
- **A redirect loop is retried by the shared client** like a failed dial
  (`sdk/httpclient/retry.go`), for every API; part of the retry policy above.
- **An empty output format beside `--plain` is read two ways.**
  `output.IsTabularFormat` treats `--output=` as a table that `--plain` turns
  into JSON, and `output.NewPrinterWithOpts` turns only `table` and `wide`
  into JSON, so `--plain --output=` prints a table in other commands.
  `exec profiling` settles the format itself (`profilingFormat`).

---

## Notes

- All new features require corresponding API tokens with appropriate scopes
- Grail resources share common patterns, implement one as template for others
- State Management is destructive (delete-only), add appropriate confirmations
- Platform Management is read-only, simplest to implement first
