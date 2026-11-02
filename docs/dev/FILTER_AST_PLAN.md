# Filter AST Conversion — Implementation Plan

**Issue**: [#132](https://github.com/Dynatrace/dtctl/issues/132) — Segment filter field requires JSON AST, not DQL strings  
**Date**: 2026-04-09

---

## Problem

The Dynatrace Filter Segments API (`/platform/storage/filter-segments/v1/filter-segments`) requires the `filter` field in segment includes to be a **stringified JSON AST** — not a plain DQL filter string.

What dtctl currently sends (causes HTTP 400):
```json
{"filter": "k8s.cluster.name = \"alpha\""}
```

What the API actually expects:
```json
{"filter": "{\"type\":\"Group\",\"logicalOperator\":\"AND\",\"explicit\":false,\"range\":{\"from\":0,\"to\":25},\"children\":[{\"type\":\"Statement\",\"range\":{\"from\":0,\"to\":25},\"key\":{\"type\":\"Key\",\"textValue\":\"k8s.cluster.name\",\"value\":\"k8s.cluster.name\",\"range\":{\"from\":0,\"to\":16}},\"operator\":{\"type\":\"ComparisonOperator\",\"textValue\":\"=\",\"value\":\"=\",\"range\":{\"from\":17,\"to\":18}},\"value\":{\"type\":\"String\",\"textValue\":\"\\\"alpha\\\"\",\"value\":\"alpha\",\"range\":{\"from\":19,\"to\":26},\"isEscaped\":true}}]}"}
```

The API also returns filters as JSON AST in GET responses, so `describe` / `edit` currently display opaque AST blobs to users.

---

## Background: The AST Format

The JSON AST is the **FilterField syntax tree** — the same format used by Dynatrace's own React UI component (`@dynatrace/strato-components/filters`). The UI provides `convertStringToFilterFieldTree` and `convertFilterFieldTreeToString` as client-side utilities. There is **no server-side parse endpoint** for this format.

### Node Types

```
Group
├── type: "Group"
├── logicalOperator: "AND" | "OR"
├── explicit: boolean           (false = implicit root/AND, true = explicit grouping)
├── range: {from, to}           (character positions in the DQL string)
└── children: []                (Statement | Group | LogicalOperator nodes)

Statement
├── type: "Statement"
├── range: {from, to}
├── key: Key
├── operator: ComparisonOperator
└── value: String | Number | Variable | ...

Key
├── type: "Key"
├── textValue: string           (raw text, may include backtick escapes)
├── value: string               (parsed value)
└── range: {from, to}

ComparisonOperator
├── type: "ComparisonOperator"
├── textValue: string           ("=", "!=", "<", "<=", ">", ">=", "in", "not in")
├── value: string               (same as textValue)
└── range: {from, to}

String (value node)
├── type: "String"
├── textValue: string           (raw text including surrounding quotes if any)
├── value: string               (unquoted/unescaped value)
├── range: {from, to}
└── isEscaped: boolean          (true when textValue has wrapping quotes or backslash escapes)
```

### Operator Constraints

- Only `=` is valid for equality (the API rejects `==` with HTTP 400).
- Full operator set from Strato docs: `=`, `!=`, `<`, `<=`, `>`, `>=`, `in`, `not in`, `exists` (`= *`), `not-exists` (`!= *`).
- Field names with special characters must be backtick-escaped.

### Key Observations

- `range` values map to exact character positions in the *canonical* DQL string.
- `textValue` is the raw text, `value` is the parsed/unescaped form.
- Dynatrace's own docs warn that string→tree→string round-trips may not reproduce identical text (due to escape character variations).
- `maxLength` for the serialized filter string: **18,000 characters**.
- Max **20 includes** per segment.

---

## Design

### Approach: Build a Go FilterField parser + renderer

The FilterField syntax is a small, well-defined grammar (not full DQL):

```
filter     = expression
expression = term (("OR") term)*
term       = factor (("AND" | implicit-AND) factor)*
factor     = "(" expression ")" | statement
statement  = key operator value
key        = identifier | backtick-escaped-identifier
operator   = "=" | "!=" | "<" | "<=" | ">" | ">=" | "in" | "not in"
value      = quoted-string | unquoted-token | number | boolean
```

This is feasible because:
1. Dynatrace themselves parse it client-side (in their React FilterField component).
2. The grammar is documented and small — it's NOT full DQL.
3. The format is stable (it's a core UI component in the Strato Design System).
4. The `range` fields are mechanical (character positions derived from parsing).

### Conversion Points

```
User writes YAML           dtctl sends to API          API returns JSON
─────────────────          ──────────────────          ────────────────
filter: 'status = "ERROR"' → filter: "{JSON AST...}"   filter: "{JSON AST...}"
                              (DQL → AST on create/    (AST → DQL on get/
                               update/apply/edit)        describe/edit/getRaw)
```

**Inbound (user → API)**: Before sending to the API, detect if filter is plain DQL or already AST. If DQL, convert to AST. If already AST (starts with `{`), pass through.

**Outbound (API → user)**: After receiving from the API, convert AST back to human-readable DQL for display in `describe`, `edit`, and `get -o yaml/json`.

### Auto-Detection

```go
func isFilterAST(filter string) bool {
    return len(filter) > 0 && filter[0] == '{'
}
```

Plain DQL filter expressions never start with `{` (they start with a key name or parenthesis). This is a safe discriminator.

---

## Implementation Plan

### Step 1: FilterField parser and renderer (`pkg/resources/segment/filterast.go`)

New file with these public functions:

```go
// FilterToAST converts a DQL filter expression to a JSON AST string.
// If the input is already a JSON AST (starts with '{'), it is returned as-is.
func FilterToAST(dql string) (string, error)

// FilterFromAST converts a JSON AST string back to a DQL filter expression.
// If the input is not a JSON AST (doesn't start with '{'), it is returned as-is.
func FilterFromAST(ast string) (string, error)
```

Internal implementation:
- **Parser** (DQL → AST): Tokenize the filter string into tokens (keys, operators, values, parens, AND, OR). Then build the tree with correct `range` positions. Serialize to JSON.
- **Renderer** (AST → DQL): Parse JSON into Go structs. Walk the tree and emit DQL text. Handle: groups with explicit OR, parenthesized sub-groups, statement key/operator/value.

Supported grammar for initial implementation:
- Statements: `key = "value"`, `key != value`, `key < 42`, `key >= 3.14`, etc.
- Logical operators: `AND` (explicit or implicit via whitespace), `OR`
- Parenthesized groups: `(a = 1 OR b = 2) c = 3`
- Quoted string values (double-quoted, with backslash escapes)
- Unquoted values (alphanumeric, dots, hyphens, underscores)
- Backtick-escaped key names: `` `field.with.dots` ``
- Operators: `=`, `!=`, `<`, `<=`, `>`, `>=`, `in`, `not in`

Explicitly **out of scope** for v1 (error with clear message):
- `exists` / `not-exists` operators (`= *`, `!= *`)
- Wildcard patterns (`*value`, `value*`, `*value*`)
- Matches-phrase (`~`, `!~`)
- Search operator (`* ~`)
- Variable references (`$var`)
- `in (val1, val2)` list syntax

These can be added later if needed. The error message should say: `unsupported filter syntax "..."; provide the filter as a JSON AST string instead`.

### Step 2: Unit tests (`pkg/resources/segment/filterast_test.go`)

Test cases:

**DQL → AST (FilterToAST)**:
- Simple: `status = "ERROR"` → correct AST with range positions
- Unquoted value: `count = 42` → Number-like still treated as unquoted string/token
- Dotted key: `k8s.cluster.name = "alpha"` → key value preserves dots
- Backtick-escaped key: `` `my field` = "value" ``
- Multiple AND (implicit): `a = "1" b = "2"` → Group with AND, two Statement children
- Multiple AND (explicit): `a = "1" AND b = "2"` → same structure
- OR: `a = "1" OR b = "2"` → Group with OR
- Mixed AND/OR precedence: `a = "1" OR b = "2" c = "3"` → OR-group containing two sub-groups
- Parentheses: `(a = "1" OR b = "2") AND c = "3"`
- All comparison operators: `!=`, `<`, `<=`, `>`, `>=`
- `not in` operator: `status not in ("a", "b")`  *(if implemented)*
- Empty string → error
- Invalid syntax → clear error

**AST → DQL (FilterFromAST)**:
- Single statement AST → `key = "value"`
- Multi-statement AND → `key1 = "value1" key2 = "value2"`
- OR group → `key1 = "value1" OR key2 = "value2"`
- Nested groups → parenthesized output
- Already-DQL passthrough (non-`{` input returns as-is)

**Round-trip**:
- Parse DQL → AST → DQL and verify the DQL is semantically equivalent
- Parse the API spec examples and verify they render back to the expected DQL

**Auto-detection**:
- `isFilterAST("{...}")` → true
- `isFilterAST("status = \"ERROR\"")` → false
- Already-AST passthrough in both directions

### Step 3: Wire into segment handler (`pkg/resources/segment/segment.go`)

Add two helper functions:

```go
// convertIncludesForAPI converts include filters from DQL to AST (for create/update).
func convertIncludesForAPI(data []byte) ([]byte, error)

// convertIncludesForDisplay converts include filters from AST to DQL (for get/describe/edit).
func convertIncludesForDisplay(seg *FilterSegment)
```

**Outbound (API responses)**: Call `convertIncludesForDisplay` in:
- `Get()` — after unmarshaling the response
- `List()` — after unmarshaling the response (for each segment)
- `GetRaw()` — after `Get()` (already covered)

**Inbound (API requests)**: Call `convertIncludesForAPI` in:
- `Create(data)` — before sending the request body
- `Update(uid, version, data)` — before sending the request body

This keeps conversion centralized in the handler. No changes needed in `cmd/` files, `pkg/apply/`, or any other callers — they continue to work with human-readable DQL strings.

### Step 4: Update existing tests

**`pkg/resources/segment/segment_test.go`**:
- Update test fixtures to use DQL strings (human-readable) as input.
- Update mock server handlers to **validate** that the request body contains JSON AST (not plain DQL) in the `filter` field. This ensures the conversion is actually happening.
- Update response fixtures to return JSON AST (simulating real API behavior), and verify that the handler returns DQL strings to callers.

**`test/integration/fixtures.go`**:
- Fix `SegmentFixture()`: change `status == "ERROR"` to `status = "ERROR"` (fix `==` → `=`).
- Fix `SegmentFixtureModified()`: same fix.
- Fix `SegmentFixtureMultiInclude()`: change `loglevel == "ERROR" OR loglevel == "WARN"` to `loglevel = "ERROR" OR loglevel = "WARN"`.
- These fixtures are used by integration tests (behind build tag). The handler will auto-convert them to AST, so the DQL strings are correct input.

### Step 5: Update golden tests

**`pkg/output/golden_test.go`**:
- The `segmentFixtures()` function currently uses DQL strings in `Include.Filter`. These represent what the handler returns after AST→DQL conversion, so they should **stay as DQL strings**. No changes needed to the fixtures themselves.
- If the golden test output changes (e.g., because `describe` output now shows converted DQL differently), regenerate with `make test-update-golden` and review diffs.

### Step 6: Update documentation

**`docs/dev/SEGMENTS_DESIGN.md`**:
- Add a new section "Filter Format Conversion" explaining the DQL ↔ AST auto-conversion.
- Note that users always write DQL, dtctl handles conversion transparently.

**`docs/site/_docs/segments.md`** (that page now lives at
[../resources/filter-segments.md](../resources/filter-segments.md) -- `docs/site/`
is retired and holds only redirect stubs):
- No changes needed to YAML examples (they already show DQL, which is correct).
- Add a brief note under the `filter` field description: "dtctl automatically converts DQL filter expressions to the API's internal format."

### Step 7: Run tests and validate

```bash
go test ./pkg/resources/segment/ -v          # New + updated unit tests
go test ./pkg/output/ -run TestGolden        # Golden tests
make test                                     # Full suite
```

---

## Files Changed

### New Files

| File | Purpose |
|------|---------|
| `pkg/resources/segment/filterast.go` | DQL ↔ AST parser and renderer |
| `pkg/resources/segment/filterast_test.go` | Comprehensive unit tests |

### Modified Files

| File | Change |
|------|--------|
| `pkg/resources/segment/segment.go` | Add `convertIncludesForAPI`, `convertIncludesForDisplay`; call from Get/List/Create/Update |
| `pkg/resources/segment/segment_test.go` | Update mock servers to validate AST in requests and return AST in responses |
| `test/integration/fixtures.go` | Fix `==` → `=` in three segment fixtures |
| `docs/dev/SEGMENTS_DESIGN.md` | Add filter format conversion section |
| `docs/site/_docs/segments.md` (now `docs/resources/filter-segments.md`) | Add note about auto-conversion |

### NOT Changed (by design)

| File | Why |
|------|-----|
| `cmd/create_segments.go` | Handler does conversion — callers don't need changes |
| `cmd/edit_segments.go` | Same — handler converts on Get (display) and Update (send) |
| `cmd/describe_segments.go` | Same — handler converts on Get |
| `pkg/apply/apply_segment.go` | Same — handler converts on Create and Update |
| `pkg/output/golden_test.go` | Fixtures already use DQL strings (correct after conversion) |

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Parser doesn't handle some real-world filter syntax | Auto-detect: if filter is already AST (`{...}`), pass through. Users can always provide raw AST as an escape hatch. Clear error message for unsupported syntax. |
| `range` positions are wrong | Ranges are computed mechanically during parsing. Extensive test coverage with known-good examples from the API spec. |
| API returns unexpected AST node types in GET responses | `FilterFromAST` handles unknown types gracefully — falls back to returning the raw AST string with a warning, rather than erroring. |
| Round-trip fidelity (DQL → AST → DQL) | Dynatrace's own docs warn about this. We normalize on canonical forms (e.g., always quote string values). Editing flow: Get returns DQL, user edits DQL, Update converts back to AST. The AST may differ from the original but is semantically equivalent. |
| Grammar changes in future Strato versions | The core grammar (key/op/value with AND/OR) is stable. Advanced operators are out-of-scope with clear error + AST passthrough escape hatch. |

---

## Out of Scope

- Advanced FilterField features (wildcards, matches-phrase, exists, variables, search)
- Server-side validation of the AST before sending (the API validates it)
- Caching or memoization of parsed ASTs
- Changes to the query command (`--segment` flag) — that uses segment UIDs, not filter expressions
