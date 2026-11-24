# Feature Management Design

Native Feature Management (FM) resources for dtctl: projects, release stages, features (with their flags), targeting, context attributes, SDK keys, and overrides.

Tracking issue: TBD. Jira epic [AI-1127](https://dt-rnd.atlassian.net/browse/AI-1127); this design doc is [AI-1680](https://dt-rnd.atlassian.net/browse/AI-1680), and each phase below links its story.

This doc covers phases 1 and 2, the read and write paths, and is for review before any implementation PR. Decisions 1 to 3 change dtctl-wide conventions and need maintainer sign-off; the rest are summarized. The later phases (change requests, declarative apply, GA) are outlined under [Later Phases](#later-phases); this doc will be updated with their design after phase 2.

---

## Background

Feature Management is the Dynatrace platform service for feature flags. A **feature** is a releasable unit with a set of **variants** (for example `baseline` and `variation-a`) and the typed **flags** those variants set; flags are part of the feature, not a separate resource. Each feature is released through ordered **release stages** (development, staging, production), and its **targeting** in each stage decides whether it is on, which rules pick a variant, and how far each rule's rollout has progressed.

FM is the first dtctl surface with a multi-level hierarchy (project, feature, per-stage targeting) and with optimistic locking on every write. Those two things drive most of the decisions below.

### Resources

| Resource | API path (under the base path) | Identified by | Notes |
|---|---|---|---|
| `project` | `/projects/{p}` | key | Container for everything below |
| `release-stage` | `/projects/{p}/release-stages/{rs}` | key | Ordered; a `guarded` stage needs a change request for targeting changes |
| `feature` | `/projects/{p}/features/{f}` | key | Variants, type, status, owner, and `flags[]` (`BOOLEAN`, `STRING`, `NUMBER` or `JSON`, one value per variant) |
| `feature-targeting` | `/projects/{p}/features/{f}/release-stage-targeting/{rs}` | feature + stage (+ rule position or id) | `enabled` and an ordered list of rules: conditions, the variant or split each serves, an optional rollout |
| `context-attribute` | `/projects/{p}/context-attributes/{k}` | key | Evaluation-context attribute rules match on |
| `sdk-key` | `/projects/{p}/release-stages/{rs}/sdk-keys/{k}` | key | Credential an SDK uses to fetch one stage's configuration |
| `override` | `/projects/{p}/my-overrides`, `/projects/{p}/overrides-profile` | caller | The caller's own variant overrides and the targeting keys they apply to |

```
project (--project or context default)
├── release-stage <stage>
│   └── sdk-key <stage> <key>
├── feature <feature>                                             (flags[] inline)
│   └── feature-targeting <feature> [<#|id>] --stage <stage>      (one per feature and stage; rules by position or id)
├── context-attribute <key>
└── override (yours)
```

### Management API

Base path: `/platform/feature-management/v0.1`, from the public OpenAPI spec.

Properties that matter for dtctl:

- **Keys are canonical.** Projects, features, release stages and context attributes (and flags within a feature) are addressed by tenant-unique, user-chosen keys. Targeting rules are the exception: they have only a service-generated `id`.
- **Optimistic locking.** Every update and delete takes an `optimistic-locking-version` query parameter and returns 409 when it is stale.
- **Lists** take `filter`, `sort` and `page-size` / `page-key`.

---

## Command Surface

Every FM resource sits under an `fm` group on each verb (`feature-management` is an alias), like the existing `aws` / `azure` / `gcp` groups (decision 1). The examples assume the context has a default project; any `fm` command takes `--project` to override it.

### project

*The top-level container for features, release stages, context attributes and SDK keys. Nearly every FM command works inside one.*

```bash
dtctl get fm projects
dtctl describe fm project checkout
dtctl create fm project checkout --name "Checkout" --description "Checkout flows"
dtctl create fm project -f project.yaml
dtctl edit fm project checkout
dtctl update fm project checkout --owner checkout-team
dtctl delete fm project checkout

dtctl config set-context prod --fm-project checkout   # default project for this context
```

### release-stage

*An environment a feature is released through, such as development, staging or production, in a set order.*

```bash
dtctl get fm release-stages
dtctl describe fm release-stage production
dtctl create fm release-stage canary --name "Canary" --type PRODUCTION
dtctl edit fm release-stage canary
dtctl update fm release-stage canary --description "5% of production traffic"
dtctl update fm release-stages --order development,staging,canary,production
dtctl delete fm release-stage canary
```

### feature

*A releasable unit of work: its variants, its flags, and its lifecycle status. A flag is a typed value with one value per variant; it is what SDKs evaluate.*

```bash
dtctl get fm features
dtctl describe fm feature new-checkout                     # variants, flags[], enabled state per stage
dtctl describe fm feature new-checkout --stage production  # plus that stage's rules and rollout
dtctl create fm feature new-checkout --name "New checkout" --type RELEASE --variants baseline,variation-a
dtctl create fm feature new-checkout -f feature.yaml       # variants and flags[] from the file
dtctl edit fm feature new-checkout                         # also how flags are added or removed
dtctl update fm feature new-checkout --status ARCHIVED
dtctl update fm feature new-checkout --owner checkout-team --maintainers alice,bob --description "One-page checkout"
dtctl delete fm feature new-checkout
```

### feature-targeting

*Per feature and release stage: whether targeting is on, and the ordered rules that pick a variant. Each rule has conditions that must all match, the variant or split it serves, and an optional rollout.*

Every stage of a feature always has a targeting, so `create`, `update` and `delete` work on one rule in it, addressed by its 0-based position or its id (decision 2).

```bash
dtctl get fm feature-targeting new-checkout                          # every stage: on/off and its rules
dtctl get fm feature-targeting new-checkout -s production            # one stage
dtctl describe fm feature-targeting new-checkout -s production       # one stage, the way the UI shows it

# Kill switch
dtctl disable fm feature new-checkout --stage production
dtctl enable fm feature new-checkout --stage production

# A list of users, then add and remove one
dtctl create fm feature-targeting new-checkout --stage production --variant variation-a \
  --where 'targetingKey = user-1,user-2' --position 0
dtctl update fm feature-targeting new-checkout 0 --stage production \
  --add-value targetingKey=user-3 --remove-value targetingKey=user-1

# Percentage rollout of all requests, bucketed per account
dtctl create fm feature-targeting new-checkout --stage production --variant variation-a \
  --bucketing-key account.id --rollout now=10 --rollout +1d=50,linear --rollout +3d=100
dtctl update fm feature-targeting new-checkout a93d07e1 --stage production --rollout now=0   # roll back

# Typed conditions, all of which must match, serving a split
dtctl create fm feature-targeting new-checkout --stage production --variant baseline=50,variation-a=50 \
  --where 'plan.tier = enterprise,pro' --where 'app.version >= 2.3.0'

dtctl update fm feature-targeting new-checkout 2 --stage production --position 0   # move to the top
dtctl delete fm feature-targeting new-checkout 2 --stage production                # remove one rule
dtctl delete fm feature-targeting new-checkout --stage staging --all               # remove every rule; asks first

# Whole-stage edits, for anything the rule flags don't cover
dtctl edit fm feature-targeting new-checkout --stage staging   # rules in $EDITOR, rule ids preserved
```

Rules print the way the UI shows them, with conditions in `--where` syntax so they can be copied into a command:

```text
$ dtctl get fm feature-targeting new-checkout
STAGE        TARGETING  #  ID        CONDITIONS                    VARIANT      ROLLOUT
staging      off        0  0c4e8a72  all requests                  baseline     -
production   on         0  6f1c2b9e  targetingKey = user-1,user-2  variation-a  -
production   on         1  a93d07e1  all requests                  variation-a  10% -> 50% (linear) -> 100%
```

### context-attribute

*A declared attribute of the evaluation context, such as `plan.tier`, that targeting rules can match on.*

```bash
dtctl get fm context-attributes
dtctl describe fm context-attribute plan-tier   # includes the features that reference it
dtctl create fm context-attribute plan-tier --name "Plan tier" --type STRING --context-key plan.tier
dtctl edit fm context-attribute plan-tier
dtctl update fm context-attribute plan-tier --description "Customer plan tier"
dtctl delete fm context-attribute plan-tier
```

### sdk-key

*The credential an SDK uses to fetch one release stage's flag configuration.*

```bash
dtctl get fm sdk-keys production                  # metadata only
dtctl create fm sdk-key production --type server  # prints the value once
dtctl delete fm sdk-key production <sdk-key>
```

### override

*Your own variant overrides, applied only to evaluations that carry your targeting keys, for testing a variant without changing targeting for anyone else.*

```bash
dtctl describe fm overrides                                   # your targeting keys plus every override
dtctl get fm overrides                                        # your overrides across the project
dtctl get fm override new-checkout                            # your override per stage for one feature
dtctl update fm overrides --add-targeting-key my-user-id
dtctl update fm override new-checkout --stage staging --variant variation-a
dtctl update fm override new-checkout --stage staging --clear
dtctl delete fm override new-checkout
dtctl delete fm overrides --all
```

---

## Design Decisions

The first three change dtctl-wide conventions and need maintainer sign-off.

### 1. An `fm` command group, with `--project` and a context default (needs sign-off)

Every FM resource is a subcommand of an `fm` group on each verb, alias `feature-management`, like the `aws` / `azure` / `gcp` groups (`cmd/get_providers.go`). Names like `project`, `release-stage`, `sdk-key` and `override` mean nothing outside FM and would otherwise claim generic nouns at the top of every verb.

Every path is project-scoped. The project is a persistent `--project` flag on the `fm` groups with a per-context default, the kubectl `--namespace` model: it is ambient, and as a positional it would give `sdk-key` three arguments. Features and stages vary per command, so they stay positional (`sdk-key <stage> <key>`, `feature-targeting <feature> [<rule>] --stage <s>`). Resolution is `--project`, then the context default, then an error that says how to set one.

The default is a nested block on the context, a config-contract change (see [CONFIG_CONTRACT.md](CONFIG_CONTRACT.md)), set with `dtctl config set-context <ctx> --fm-project <project>`:

```yaml
contexts:
  - name: prod
    context:
      environment: https://abc12345.apps.dynatrace.com
      feature-management:
        project: checkout
```

### 2. Targeting rules are written one at a time, by position or id (needs sign-off)

Targeting changes are the riskiest FM writes, so each one is an explicit command that says what it changes and in which stage, rather than a whole-stage `edit`: `create` / `update` / `delete fm feature-targeting <feature> [<rule>] --stage <s>` change one rule, and `enable` / `disable fm feature --stage <s>` are the kill switch. `edit fm feature-targeting` covers anything else.

- **One resource.** A stage's targeting always exists and the API has no create or delete for it, so the stage and its rules are one resource, with no separate rule resource or alias. `delete --all` clears every rule, asks first, and is a destructive pattern in `classify.go`.
- **Addressing.** Rules have no key, so the rule argument is its 0-based position or its id or a unique id prefix. The UI numbers rules from 1, so `get` and `describe` show the 0-based position dtctl takes. Positions shift on reorder, so scripts and agents should use ids.
- **Safety.** Every write is a read-modify-write of the whole stage that preserves every rule `id`, since a new id re-buckets users. `--stage` (`-s`) is required and never defaulted, so the stage is always visible in the command. Rollback (`--rollout now=0`) is safe unattended: a no-op exits 0, and a 409 is retried once.
- **Flags.** `--where '<context-key> <op> <values>'` adds a condition (all must match), with operators by value type taken from the context attribute; `--add-value` / `--remove-value` edit one value of a list; `--variant` serves a variant or a split; `--rollout <time>=<pct>[,linear]` accepts `now` and offsets like `+1d`, resolved to UTC client-side.
- **Output.** `get` and `describe` print rules as the UI does, with conditions in `--where` syntax, so what an agent reads is what it can write. `-o json` is the API's rules unchanged.

### 3. Resource names (needs sign-off)

- **`feature-targeting`** for the API's `release-stage-targeting`. dtctl names child resources after their parent, like `extension-config`, and targeting belongs to a feature.
- **One `override` resource** covering both your variant overrides and the targeting keys they apply to, which the API splits into two resources but are only useful together.

### Other decisions

| Decision | Choice | Rationale |
|---|---|---|
| Identifiers | Keys, with names accepted on a 404 | Keys are user-chosen and tenant-unique. Never dispatched on shape, since the key pattern also matches a UUID. |
| Optimistic locking | `edit` / `update` read, merge and send the version they read; `--version` asserts one | Every FM write takes a version. A 409 maps to a typed "changed since read; re-run" error. |
| Merge | On the raw JSON from the GET, not generated structs | Full-replace endpoints clear omitted fields, so an older dtctl would silently drop fields added later. |
| Errors | Read `error.message` from the nested error envelope | FM errors are `{"error": {"code", "message", "details"}}`. |
| `edit` vs `update` | `edit` opens `$EDITOR`; `update` sets fields from flags | Matches `edit aws` / `update aws`. Status (`--status`) and stage order (`update fm release-stages --order`) are `update` flags. |
| Descriptive feature edits | `update fm feature --name/--description/--owner/--maintainers` use the metadata endpoint | It has its own `features.metadata:write` scope. Mixing with `--status` is an error; adding or removing flags is `edit fm feature`. |
| SDK key values | Printed once by `create fm sdk-key`, in every output format | Agent onboarding needs the value. `get` shows metadata only and dtctl never stores it. |
| Overrides | Writes need read scopes only, but still go through `SetupWithSafety` | They change only the caller's own state. `update fm override` keeps the other stages, since the API's `PUT` replaces them all. |
| Guarded stages | Direct writes fail with a hint | They need a change request, which arrives in phase 3. |

---

## Stability Tiers

FM commands start in the `development` tier, like `account`: each verb gets an `fm` group attached with `addDevelopmentCommand(<verb>Cmd, <fmGroupCmd>, "feature-management")`, visible only after `dtctl config set development.feature-management on`.

| Tier | When |
|---|---|
| `development` | While phases 1 and 2 are being built |
| `experimental` | Once phase 2 is complete |
| `stable` | After the FM API reaches GA (targeted for February 2027), with the `nativeCoverage` entry |

The `sdk/api/featuremanagement` Go API stays stable across releases even while the CLI is `experimental`, since other Go code imports it.

## Scopes

Tracked in [AI-1674](https://dt-rnd.atlassian.net/browse/AI-1674).

`pkg/auth/resource_scopes.go` gets one entry per resource, keyed by the group-qualified name:

```go
"fm/project":           {Read: []string{"feature-management:projects:read"}, Write: []string{"feature-management:projects:write"}, Delete: []string{"feature-management:projects:delete"}},
"fm/feature":           {Read: []string{"feature-management:features:read"}, Write: []string{"feature-management:features:write"}, Delete: []string{"feature-management:features:delete"}},
"fm/release-stage":     {Read: []string{"feature-management:release-stages:read"}, Write: []string{"feature-management:release-stages:write"}, Delete: []string{"feature-management:release-stages:delete"}},
"fm/sdk-key":           {Read: []string{"feature-management:sdk-keys:read"}, Write: []string{"feature-management:sdk-keys:write"}, Delete: []string{"feature-management:sdk-keys:delete"}},
"fm/context-attribute": {Read: []string{"feature-management:context-attributes:read"}, Write: []string{"feature-management:context-attributes:write"}, Delete: []string{"feature-management:context-attributes:delete"}},
"fm/feature-targeting": {Read: []string{"feature-management:release-stage-targetings:read"}, Write: []string{"feature-management:release-stage-targetings:write"}, Delete: []string{"feature-management:release-stage-targetings:write"}}, // a rule delete is a targeting update
"fm/override":          {Read: overrideScopes, Write: overrideScopes, Delete: overrideScopes}, // projects:read, features:read, release-stages:read
```

`features.metadata:write` covers the descriptive `update fm feature` path. The dtctl OAuth clients already allow these scopes. [docs/TOKEN_SCOPES.md](../TOKEN_SCOPES.md) is updated in phase 1 for platform tokens.

---

## Implementation Phases

### Phase 1: Read path

Jira: [AI-1675](https://dt-rnd.atlassian.net/browse/AI-1675)

**Goal**: `get` and `describe` for every resource.

- `sdk/api/featuremanagement`, one package for the whole surface, types generated from the public OpenAPI document.
- `pkg/resources/featuremanagement` handlers, `get` / `describe` for project, release-stage, feature (with `flags[]`), feature-targeting (with its rules), context-attribute, sdk-key (metadata only) and override.
- `--project` and the context default (decision 1).
- Scopes, resolver, golden tests with synthetic data, docs.

### Phase 2: Write path

Jira: [AI-1676](https://dt-rnd.atlassian.net/browse/AI-1676)

**Goal**: every write, targeting first; promotion to `experimental`.

- `create` / `update` / `delete fm feature-targeting` with `--variant`, `--rollout`, `--where` with `=` on string keys, and `--add-value` / `--remove-value`, plus `enable` / `disable fm feature` (decision 2). The other operators and value types follow.
- `create`, `edit`, `update`, `delete` for every resource with read-modify-write and safety checks, including `update fm feature --status`, the metadata flags, field flags on `update fm project`, `release-stage` and `context-attribute`, `update fm release-stages --order`, `create fm sdk-key`, and override writes.
- `destructivePatterns` entries in `pkg/resources/api/classify.go` for SDK key deletion and `delete fm feature-targeting --all`.

### Later Phases

Planned, and designed in an update to this doc after phase 2:

- **Phase 3: change requests** ([AI-1677](https://dt-rnd.atlassian.net/browse/AI-1677)). Change-request bundles (`get` / `describe` / `create fm change-request-bundle`, `exec fm change-request-bundle --action publish`), a `--bundle <id>` option on the targeting and feature writes so they propose a change instead of applying it, which is how guarded stages are written, and the API's allowed-operations info in `describe` and `--agent` output.
- **Phase 4: declarative apply** ([AI-1677](https://dt-rnd.atlassian.net/browse/AI-1677)). `dtctl apply -f` for FM resources. Field sniffing can't tell FM payloads apart, and their own `type` field would hit the generic document fallback, so this needs an explicit type marker in `get -o yaml` output and a matching update to [APPLY_TYPE_DETECTION.md](APPLY_TYPE_DETECTION.md).
- **Phase 5: GA.** `experimental` to `stable` with the `nativeCoverage` entry, after the FM API reaches GA.

---

## Open Questions

1. Is the nested `feature-management` context block, with `--fm-project` on `config set-context`, acceptable (decision 1)?
2. Nothing in `sdk/api` uses codegen today. Any objection to generating the FM types from the public OpenAPI document, or a preferred generator?
