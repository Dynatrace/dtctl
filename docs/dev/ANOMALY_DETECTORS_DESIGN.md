# Anomaly Detectors Feature Design

Full-featured custom anomaly detector management for dtctl: list, describe, create, edit, delete, and apply.

---

## Background

Dynatrace anomaly detectors are Davis AI configurations that continuously monitor time series data and trigger Davis events when anomalous behavior is detected. They are the modern, unified alerting mechanism in Dynatrace, superseding the older "metric events" concept.

### Custom anomaly detectors (`builtin:davis.anomaly-detectors`)

User-created alert configurations using DQL queries or metric selectors. Each detector has:

- **Title** and optional **description**
- **Enabled** flag (active/paused)
- **Source** — what created it: "Clouds", "Services", "Dashboards", "Notebooks", "Davis Anomaly Detection", "Business Flow", "Databases", or "Rest API"
- **Analyzer** — the detection algorithm:
  - `StaticThresholdAnomalyDetectionAnalyzer` — fires when a metric crosses a fixed value
  - `AutoAdaptiveAnomalyDetectionAnalyzer` — fires when a metric deviates from a learned baseline
- **Analyzer input** — key-value pairs: `query`/`query.expression`, `threshold`, `alertCondition` (ABOVE/BELOW), `slidingWindow`, `violatingSamples`, `dealertingSamples`, `numberOfSignalFluctuations`, `alertOnMissingData`
- **Event template** — key-value pairs defining the triggered Davis event: `event.type` (PERFORMANCE_EVENT, AVAILABILITY_EVENT, CUSTOM_ALERT, RESOURCE_CONTENTION_EVENT, ERROR_EVENT, WARNING), `event.name`, `event.description`, `dt.source_entity`, etc.
- **Execution settings** — actor/service-user context for DQL execution

API: Settings v2 with schema `builtin:davis.anomaly-detectors`. Scope: `environment`. Max 1000 per tenant.

### Out of scope

The following are explicitly excluded from this feature:

**Built-in anomaly detection (`builtin:anomaly-detection.*`)** — Per-entity-type tuning of Dynatrace's built-in detection (services, hosts, k8s workloads, etc.). These are not standalone detectors but sensitivity configurations scoped to individual entities. They cannot be created or deleted, have wildly different value shapes per schema, and would produce an overwhelming list (potentially thousands of settings objects across 11 schemas). Users who need to tune built-in detection can use the generic `dtctl get settings --schema builtin:anomaly-detection.services` command.

**Internal/tangential schemas:**

- `builtin:internal.anomaly-detection.*` — internal Dynatrace schemas, not user-facing
- `builtin:anomaly.detection.alerts-category-update` — one-time migration helper
- `builtin:anomaly-detection.holiday-aware-baseline` — modifies baseline calculation globally, not a detector
- `builtin:infrastructure.disk.edge.anomaly-detectors` — narrow edge case for disk anomaly detection

### Relationship to Davis problems

Anomaly detectors are the **configuration** (input); Davis problems are the **incidents** (output). The causal chain:

```
Anomaly Detector (config) → evaluates time series → triggers Davis Event → Davis correlates/deduplicates → creates Problem
```

This feature does NOT add a separate `problem` resource. However, `describe anomaly-detector` cross-references recent problems triggered by a detector via DQL (`fetch dt.davis.problems | filter event.name == "<event name>"`), giving users immediate operational context alongside the configuration.

---

## Design

### Resource name

| Command | Use | Aliases |
|---------|-----|---------|
| get (list) | `anomaly-detectors` | `anomaly-detector`, `ad` |
| get (single) | `anomaly-detectors <id>` | (same, arg triggers single-get) |
| describe | `anomaly-detector <id-or-title>` | `ad` |
| create | `anomaly-detector` | `ad` |
| edit | `anomaly-detector <id-or-title>` | `ad` |
| delete | `anomaly-detector <id-or-title>` | `ad` |

### Handler

New package: `pkg/resources/anomalydetector/`

This wraps the Settings API directly (same pattern as `gcpconnection` and `azureconnection`), not delegating to the generic `settings` handler.

**Data structures:**

```go
// AnomalyDetector represents a custom anomaly detector (builtin:davis.anomaly-detectors).
type AnomalyDetector struct {
    ObjectID   string `json:"objectId" table:"OBJECT ID,wide"`

    // Flattened fields for table display
    Title          string `json:"title" table:"TITLE"`
    Enabled        bool   `json:"enabled" table:"ENABLED"`
    AnalyzerShort  string `json:"analyzer" table:"ANALYZER"`   // e.g. "static (>90)", "auto-adaptive"
    EventType      string `json:"eventType" table:"EVENT TYPE"`
    Source         string `json:"source,omitempty" table:"SOURCE"`
    Description    string `json:"description,omitempty" table:"DESCRIPTION,wide"`

    // Full value for JSON/YAML output and describe
    Value map[string]any `json:"value" table:"-"`
}
```

`Title` comes from `value.title`, `AnalyzerShort` is derived from the analyzer name + threshold/condition, and `EventType` is extracted from `eventTemplate.properties`.

**Handler interface:**

```go
func NewHandler(c *client.Client) *Handler
func (h *Handler) List(opts ListOptions) ([]AnomalyDetector, error)
func (h *Handler) Get(id string) (*AnomalyDetector, error)
func (h *Handler) Create(data []byte) (*AnomalyDetector, error)
func (h *Handler) Update(objectID string, data []byte) (*AnomalyDetector, error)
func (h *Handler) Delete(objectID string) error
func (h *Handler) GetRaw(objectID string) ([]byte, error)              // for edit command
func (h *Handler) RecentProblems(detector *AnomalyDetector) ([]Problem, error) // DQL cross-ref

type ListOptions struct {
    Enabled *bool  // nil = no filter, true = enabled only, false = disabled only
}
```

**Listing strategy:**

Fetch `builtin:davis.anomaly-detectors` (single schema, up to 1000 objects per tenant). Apply client-side `Enabled` filter if set. Sort by title.

### CLI Commands

| Command | File | Notes |
|---------|------|-------|
| `dtctl get anomaly-detectors` | `cmd/get_anomalydetector.go` | List all. Flag: `--enabled` (tri-state: absent=all, `--enabled`=enabled only, `--enabled=false`=disabled only) |
| `dtctl get anomaly-detector <id>` | `cmd/get_anomalydetector.go` | Get single by objectId or interactive title match |
| `dtctl describe anomaly-detector <id-or-title>` | `cmd/describe_anomalydetector.go` | Rich detail with recent problems |
| `dtctl create anomaly-detector -f ad.yaml` | `cmd/create_anomalydetector.go` | Create detector from YAML. Safety check. |
| `dtctl edit anomaly-detector <id-or-title>` | `cmd/edit_anomalydetector.go` | Open in $EDITOR. Safety check. |
| `dtctl delete anomaly-detector <id-or-title>` | `cmd/delete_anomalydetector.go` | Delete detector. Safety check. |
| `dtctl apply -f ad.yaml` | (modify `pkg/apply/`) | Idempotent create-or-update |

### Table Output

**Default columns:**

```
TITLE                                  ENABLED  ANALYZER          EVENT TYPE    SOURCE
Aurora cluster CPU high                true     static (>90)      PERFORMANCE   Clouds
VPC endpoint packet drops              true     auto-adaptive     AVAILABILITY  Clouds
Postgres availability                  true     static (<0)       AVAILABILITY  Services
EC2 instance CPU utilization           false    static (>90)      PERFORMANCE   Clouds
```

**Wide columns (add):** OBJECT ID, DESCRIPTION

**Analyzer column derivation:**

| Analyzer | Condition | Display |
|----------|-----------|---------|
| StaticThreshold | ABOVE 90 | `static (>90)` |
| StaticThreshold | BELOW 0 | `static (<0)` |
| AutoAdaptive | ABOVE | `auto-adaptive` |
| AutoAdaptive | BELOW | `auto-adaptive` |

### Describe Output

```
Title:          Aurora cluster CPU utilization
Object ID:      vu9U3hXa3q0AAAA...
Enabled:        true
Source:         Clouds
Description:    Monitors Aurora cluster CPU utilization to detect performance bottlenecks.

Analyzer:
  Type:                    Static Threshold
  Alert Condition:         ABOVE 90
  Sliding Window:          3 violating samples in 5 minutes
  De-alerting Samples:     5
  Missing Data Alert:      false

Query:
  timeseries CPUUtilizationPercentage=avg(cloud.aws.rds.CPUUtilization
    .By.DBClusterIdentifier), interval:1m,
    by:{dt.smartscape_source.id, aws.region, DBClusterIdentifier, aws.account.id}

Event Template:
  event.type:              PERFORMANCE_EVENT
  event.name:              {dims:DBClusterIdentifier} - RDS Aurora Cluster High CPU Utilization
  event.description:       Entity name: {dims:DBClusterIdentifier}, Entity id:...

Recent Problems (last 7 days):
  DISPLAY ID      STATUS   START                 DURATION
  P-2603120042    CLOSED   2026-03-28 14:22:00   12m
  P-2603118901    CLOSED   2026-03-26 03:05:00   4m
  (2 problems in the last 7 days)
```

### YAML Format (for create/apply)

Two input formats are accepted:

#### Flattened format (recommended for humans)

A flattened representation of analyzer input and event template for ergonomics. The handler converts to/from the API's `[{key, value}]` format.

```yaml
title: "High CPU on production hosts"
description: "Alert when CPU exceeds 90% on prod hosts"
enabled: true
analyzer:
  name: dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer
  input:
    query: "timeseries cpu=avg(dt.host.cpu.usage), by:{dt.entity.host}, interval:1m"
    threshold: "90"
    alertCondition: ABOVE
    violatingSamples: "3"
    slidingWindow: "5"
    dealertingSamples: "5"
    alertOnMissingData: "false"
eventTemplate:
  event.type: PERFORMANCE_EVENT
  event.name: "High CPU on {dims:dt.entity.host}"
  event.description: "CPU usage is at {severity}%, threshold {threshold}%"
```

When `source` is omitted, it defaults to `"dtctl"` for traceability.

#### Raw Settings API format

The native Settings API payload is also accepted, for interoperability with Dynatrace API docs and export/import workflows:

```yaml
schemaId: builtin:davis.anomaly-detectors
scope: environment
value:
  title: "High CPU on production hosts"
  enabled: true
  # ... full API structure with analyzerInput/eventTemplate as [{key, value}] arrays
```

Detection: if the input has a `schemaId` field equal to `builtin:davis.anomaly-detectors`, it is treated as raw format. Otherwise, the flattened format is assumed.

The `edit` command returns the flattened YAML format. On save, the handler reconstructs the API format.

#### JSON / YAML output of `get` (round-trippable through `apply`)

`dtctl get anomaly-detector <id> -o json` and `-o yaml` emit the **raw Settings envelope** so the output is directly consumable by `dtctl apply -f`:

```json
{
  "schemaId": "builtin:davis.anomaly-detectors",
  "scope": "environment",
  "objectId": "vu9U3hXa3q0AAAA",
  "schemaVersion": "1.0.42",
  "value": { "title": "...", "analyzer": {...}, "eventTemplate": {...}, ... }
}
```

This matches dashboard/notebook/SLO behavior: `get -o json | apply -f` round-trips cleanly. The `AnomalyDetector` Go struct keeps derived display fields (`Title`, `Enabled`, `AnalyzerShort`, `EventType`) for the table renderer, but those fields are tagged `json:"-"` and excluded from the wire format via custom `MarshalJSON`/`MarshalYAML` methods. Including them at the top level (alongside `value`) would produce a hybrid shape that neither matches the Settings API nor the flattened authoring format — the bug fixed by [#216](https://github.com/Dynatrace/dtctl/issues/216).

For human authoring, prefer the flattened YAML format above. The raw envelope is the export/round-trip format.

### Recent Problems Cross-Reference

The `describe` and `--agent` mode include recent problems triggered by a detector. Implementation:

```go
func (h *Handler) RecentProblems(detector *AnomalyDetector) ([]Problem, error)
```

This executes a DQL query:

```
fetch dt.davis.problems
| filter event.name == "<event.name from eventTemplate>"
| sort timestamp desc
| limit 10
| fields display_id, event.status, event.start, event.end, event.category,
         affected_entity_names, resolved_problem_duration
```

The event name is extracted from the detector's `eventTemplate.properties` where `key == "event.name"`. If the event name contains `{dims:...}` placeholders, we use `contains` matching on the static prefix instead of exact match. This is a best-effort heuristic — the prefix approach may over-match when multiple detectors share an event name prefix, but in practice detector event names are distinctive enough that this is acceptable.

### Registration Points

| File | Change |
|------|--------|
| `cmd/get.go` init() | `getCmd.AddCommand(getAnomalyDetectorsCmd)` |
| `cmd/describe.go` init() | `describeCmd.AddCommand(describeAnomalyDetectorCmd)` |
| `cmd/create.go` init() | `createCmd.AddCommand(createAnomalyDetectorCmd)` |
| `cmd/edit.go` init() | `editCmd.AddCommand(editAnomalyDetectorCmd)` |
| `cmd/delete.go` init() | `deleteCmd.AddCommand(deleteAnomalyDetectorCmd)` |
| `pkg/apply/applier.go` | Add `ResourceTypeAnomalyDetector`, detection heuristic, `applyAnomalyDetector()` |
| `pkg/resources/resolver/resolver.go` | Add title-based name resolution for interactive mode |
| `pkg/commands/listing.go` | Add `anomaly-detector` aliases: `ad`, `anomaly-detectors` |
| `pkg/output/golden_test.go` | Add test cases with real structs |

### Safety Checks

All mutating commands require safety checks per project policy:

- `create anomaly-detector` — `safety.OperationCreate`
- `edit anomaly-detector` — `safety.OperationUpdate`
- `delete anomaly-detector` — `safety.OperationDelete`

### Agent Mode (`-A`)

Agent mode output wraps results in the standard envelope with context:

```json
{
  "ok": true,
  "result": [...],
  "context": {
    "verb": "get",
    "resource": "anomaly-detector",
    "suggestions": [
      "dtctl describe anomaly-detector <title> — view full configuration and recent problems",
      "dtctl get anomaly-detectors --enabled — list only active detectors",
      "dtctl edit anomaly-detector <title> — modify detector configuration"
    ]
  }
}
```

---

## Implementation Order

### Phase 1: Core handler and read commands

1. `pkg/resources/anomalydetector/anomalydetector.go` — Handler with List, Get
2. `cmd/get_anomalydetector.go` — get/list command with `--enabled` flag
3. `cmd/describe_anomalydetector.go` — describe with analyzer detail (no problems cross-ref yet)
4. Golden tests for table output

### Phase 2: Mutations

1. `cmd/create_anomalydetector.go` — create from YAML (flattened + raw format) with safety check
2. `cmd/edit_anomalydetector.go` — edit with $EDITOR, safety check
3. `cmd/delete_anomalydetector.go` — delete with safety check
4. `pkg/apply/` integration for idempotent apply (see apply detection heuristic below)

### Phase 3: Problems cross-reference

1. `RecentProblems()` method using DQL query
2. Integrate into describe output
3. Integrate into agent mode context

### Phase 4: Polish

1. Name resolver for interactive title matching
2. Command catalog and alias registration
3. Update IMPLEMENTATION_STATUS.md (changelog is automated by release-please from conventional commits)

---

## Apply Detection Heuristic

The `detectResourceType()` function in `pkg/apply/applier.go` needs a new heuristic for anomaly detectors:

1. **Raw Settings format**: object has `schemaId == "builtin:davis.anomaly-detectors"` — detected as part of existing Settings heuristic, routed to anomaly detector handler instead of generic settings apply.
2. **Flattened format**: object has `analyzer` field (with `name` + `input` subfields) AND `eventTemplate` field — detected as anomaly detector.

For idempotent apply: if the input contains an `objectId`, attempt GET — if found, update; if 404, create. If no `objectId`, always create.

---

## Open Questions

1. **Event name matching for problems cross-ref** — Custom detector event names often contain `{dims:...}` placeholders. The current design uses prefix matching on the static portion of the event name. This is best-effort: it may over-match when multiple detectors share a prefix, or under-match if the prefix is too short. An alternative is to query problems by a broader time window and then match by the detector's object ID if the problem metadata includes it. Worth investigating during Phase 3 implementation.
