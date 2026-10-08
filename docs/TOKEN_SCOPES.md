# Token Scopes Reference

This document lists the Dynatrace platform token scopes required for each safety level. Copy the scope list for your desired safety level when creating a token in Dynatrace.

> **Note**: Safety levels are client-side only. The token scopes you configure in Dynatrace are what actually controls access. Configure your tokens with the minimum required scopes for your use case.
>
> **Ownership checks are also client-side**: The `readwrite-mine` safety level and `--mine` flag work by comparing the resource owner ID with your user ID locally. The Dynatrace API does not enforce ownership restrictions—if your token has write access, you can modify any resource that is shared with a user. The ownership check is a convenience feature to prevent accidental modifications to shared resources.
>
> **Machine-readable source of truth**: The per-level lists and the per-resource scopes below are derived from a single canonical table (`pkg/auth/resource_scopes.go`); `GetScopesForSafetyLevel` and the command catalog both read from it, so they cannot drift. `dtctl commands -o json` exposes that table as a top-level `resource_scopes` object plus per-command `required_scopes_by_resource`, and `dtctl commands [filter] --required-scopes` prints the minimal scope union for a command set. Prefer these over hand-copying this document when provisioning tokens programmatically.

## Quick Reference

| Safety Level               | Use Case                               | Token Type        |
| -------------------------- | -------------------------------------- | ----------------- |
| `readonly`                 | Production monitoring, troubleshooting | Read-only token   |
| `readwrite-mine`           | Personal development, sandbox          | Standard token    |
| `readwrite-all`            | Team environments, administration      | Standard token    |
| `dangerously-unrestricted` | Dev environments, bucket management    | Full access token |

For creating platform tokens, see [Dynatrace Platform Tokens documentation](https://docs.dynatrace.com/docs/manage/identity-access-management/access-tokens-and-oauth-clients/platform-tokens).

## Recommended Scopes by Safety Level

### `readonly`

Read-only access for production monitoring and troubleshooting.

This level does not include the Live Debugger write scope `dev-obs:breakpoints:set`.

```
document:documents:read,
document:direct-shares:read,
document:trash.documents:read,
automation:workflows:read,
slo:slos:read,
slo:objective-templates:read,
settings:schemas:read,
settings:objects:read,
app-settings:objects:read,
extensions:definitions:read,
extensions:configurations:read,
storage:logs:read,
storage:events:read,
storage:metrics:read,
storage:spans:read,
storage:bizevents:read,
storage:entities:read,
storage:smartscape:read,
storage:system:read,
storage:security.events:read,
storage:application.snapshots:read,
storage:user.events:read,
storage:user.sessions:read,
storage:user.replays:read,
storage:buckets:read,
storage:bucket-definitions:read,
storage:fieldsets:read,
storage:fieldset-definitions:read,
storage:files:read,
storage:filter-segments:read,
iam:users:read,
iam:groups:read,
notification:notifications:read,
davis:analyzers:read,
app-engine:apps:run,
app-engine:edge-connects:read,
openpipeline:configurations:read,
```

### `readwrite-mine`

Create and manage your own resources in sandbox/development environments.

```
document:documents:read,
document:documents:write,
document:documents:delete,
document:direct-shares:read,
document:direct-shares:write,
document:direct-shares:delete,
document:trash.documents:read,
document:trash.documents:restore,
automation:workflows:read,
automation:workflows:write,
automation:workflows:run,
document:environment-shares:claim,
dev-obs:breakpoints:set,
slo:slos:read,
slo:slos:write,
slo:objective-templates:read,
settings:schemas:read,
settings:objects:read,
settings:objects:write,
app-settings:objects:read,
extensions:definitions:read,
extensions:definitions:write,
extensions:configurations:read,
extensions:configurations:write,
storage:logs:read,
storage:events:read,
storage:metrics:read,
storage:spans:read,
storage:bizevents:read,
storage:entities:read,
storage:smartscape:read,
storage:system:read,
storage:security.events:read,
storage:buckets:read,
storage:bucket-definitions:read,
storage:files:read,
storage:files:write,
storage:filter-segments:read,
storage:filter-segments:write,
iam:users:read,
iam:groups:read,
notification:notifications:read,
davis:analyzers:read,
davis:analyzers:execute,
davis-copilot:conversations:execute,
app-engine:apps:run,
app-engine:functions:run,
app-engine:edge-connects:read,
openpipeline:configurations:read,
email:emails:send
```

### `readwrite-all`

Full resource management for team environments (no data deletion; document deletes are soft — moved to trash).

```
document:documents:read,
document:documents:write,
document:documents:delete,
document:documents:admin,
document:direct-shares:read,
document:direct-shares:write,
document:direct-shares:delete,
document:environment-shares:read,
document:environment-shares:write,
document:environment-shares:claim,
document:environment-shares:delete,
document:trash.documents:read,
document:trash.documents:restore,
automation:workflows:read,
automation:workflows:write,
automation:workflows:run,
dev-obs:breakpoints:set,
slo:slos:read,
slo:slos:write,
slo:objective-templates:read,
settings:schemas:read,
settings:objects:read,
settings:objects:write,
app-settings:objects:read,
extensions:definitions:read,
extensions:definitions:write,
extensions:configurations:read,
extensions:configurations:write,
storage:logs:read,
storage:logs:write,
storage:events:read,
storage:events:write,
storage:metrics:read,
storage:metrics:write,
storage:spans:read,
storage:bizevents:read,
storage:entities:read,
storage:smartscape:read,
storage:system:read,
storage:security.events:read,
storage:application.snapshots:read,
storage:user.events:read,
storage:user.sessions:read,
storage:user.replays:read,
storage:buckets:read,
storage:buckets:write,
storage:bucket-definitions:read,
storage:fieldsets:read,
storage:fieldset-definitions:read,
storage:files:read,
storage:files:write,
storage:filter-segments:read,
storage:filter-segments:write,
iam:users:read,
iam:groups:read,
notification:notifications:read,
notification:notifications:write,
davis:analyzers:read,
davis:analyzers:execute,
davis-copilot:conversations:execute,
davis-copilot:nl2dql:execute,
davis-copilot:dql2nl:execute,
davis-copilot:document-search:execute,
app-engine:apps:install,
app-engine:apps:run,
app-engine:apps:delete,
app-engine:functions:run,
app-engine:edge-connects:read,
openpipeline:configurations:read,
app-engine:edge-connects:write,
email:emails:send
```

### `dangerously-unrestricted`

Full admin access including data deletion and bucket management.

```
document:documents:read,
document:documents:write,
document:documents:delete,
document:documents:admin,
document:direct-shares:read,
document:direct-shares:write,
document:direct-shares:delete,
document:environment-shares:read,
document:environment-shares:write,
document:environment-shares:claim,
document:environment-shares:delete,
document:trash.documents:read,
document:trash.documents:restore,
document:trash.documents:delete,
automation:workflows:read,
automation:workflows:write,
automation:workflows:run,
dev-obs:breakpoints:set,
slo:slos:read,
slo:slos:write,
slo:objective-templates:read,
settings:schemas:read,
settings:objects:read,
settings:objects:write,
settings:objects:admin,
app-settings:objects:read,
extensions:definitions:read,
extensions:definitions:write,
extensions:configurations:read,
extensions:configurations:write,
storage:logs:read,
storage:logs:write,
storage:events:read,
storage:events:write,
storage:metrics:read,
storage:metrics:write,
storage:spans:read,
storage:bizevents:read,
storage:entities:read,
storage:smartscape:read,
storage:system:read,
storage:security.events:read,
storage:application.snapshots:read,
storage:user.events:read,
storage:user.sessions:read,
storage:user.replays:read,
storage:buckets:read,
storage:buckets:write,
storage:bucket-definitions:read,
storage:bucket-definitions:write,
storage:bucket-definitions:delete,
storage:bucket-definitions:truncate,
storage:fieldsets:read,
storage:fieldset-definitions:read,
storage:fieldset-definitions:write,
storage:files:read,
storage:files:write,
storage:files:delete,
storage:filter-segments:read,
storage:filter-segments:write,
storage:filter-segments:share,
storage:filter-segments:delete,
storage:filter-segments:admin,
storage:records:delete,
iam:users:read,
iam:groups:read,
iam:policies:read,
notification:notifications:read,
notification:notifications:write,
davis:analyzers:read,
davis:analyzers:execute,
davis-copilot:conversations:execute,
davis-copilot:nl2dql:execute,
davis-copilot:dql2nl:execute,
davis-copilot:document-search:execute,
app-engine:apps:install,
app-engine:apps:run,
app-engine:apps:delete,
app-engine:functions:run,
app-engine:edge-connects:read,
openpipeline:configurations:read,
app-engine:edge-connects:write,
app-engine:edge-connects:delete,
email:emails:send
```

---

## Scopes by resource

Required scopes for every resource are generated from the CLI below (grouped by access level), and each resource page under [`resources/`](resources/) lists its own **Required token scopes** table.

### Flags that need an extra scope

A few flags reach a second API, so they need a scope the resource itself does not. `dtctl --check-scopes` accounts for them, reporting the requirement of the command line you actually typed:

| Command | Flag | Extra scope |
| --- | --- | --- |
| `dtctl get documents` / `dashboards` / `notebooks` | `--admin-access` | `document:documents:admin` |
| `dtctl update extension` / `extensions` | `--with-configurations` | `extensions:configurations:read`, `extensions:configurations:write` |

Without the extra scope the flag fails with `403 Insufficient permissions`, while the same command without it succeeds — which is why the scope is not declared on the resource.

`dtctl auth login` requests `document:documents:admin` at the `readwrite-all` and `dangerously-unrestricted` safety levels (not at `readonly` or `readwrite-mine`, which exist to keep other users' documents out of reach). The scope alone is not enough: the tenant's IAM policy must also grant the `document:documents:admin` permission — a user without it can still log in, the API just keeps refusing `--admin-access`. OAuth sessions created before dtctl requested the scope do not carry it; run `dtctl auth login` again to pick it up.

### Endpoints that accept one of several scopes

Some endpoints accept any one of several scopes. The Platform Management API behind `dtctl get environment`, `get license` and `get license-settings` (and their `describe` forms) accepts `app-engine:apps:run`, `app-engine:functions:run` or `platform-management:environments:read`. dtctl lists and requests `app-engine:apps:run`, and the table below shows the others as "or instead any one of". `dtctl --check-scopes` reports `ok` for a token that holds any one of them, and names the one it relied on in `satisfied_by`.

<!-- GENERATED:token-scopes:start -->
<!-- Do not edit by hand. Generated by scripts/gen-docs from `dtctl commands --full -o json`; run `make docs-generate`. -->

## Required scopes by resource (generated)
Generated reference of the API token scopes each resource requires, grouped by safety level.

### claim

| Resource | Scope |
| --- | --- |
| environment-share | `document:environment-shares:claim` |

### delete

| Resource | Scope |
| --- | --- |
| app | `app-engine:apps:delete` |
| breakpoint | `dev-obs:breakpoints:set` |
| dashboard | `document:documents:delete` |
| document | `document:documents:delete` |
| edgeconnect | `app-engine:edge-connects:delete` |
| lookup | `storage:files:delete` |
| notebook | `document:documents:delete` |
| scheduling-rule | `automation:rules:read` |
| scheduling-rule | `automation:rules:write` |
| segment | `storage:filter-segments:delete` |
| trash | `document:trash.documents:delete` |

### read

| Resource | Scope |
| --- | --- |
| analyzer | `davis:analyzers:read` |
| anomaly-detector | `settings:objects:read` |
| app | `app-engine:apps:run` |
| arrivals | `storage:bizevents:read` |
| arrivals | `storage:entities:read` |
| arrivals | `storage:events:read` |
| arrivals | `storage:logs:read` |
| arrivals | `storage:metrics:read` |
| arrivals | `storage:security.events:read` |
| arrivals | `storage:smartscape:read` |
| arrivals | `storage:spans:read` |
| arrivals | `storage:system:read` |
| aws | `extensions:configurations:read` |
| aws | `settings:objects:read` |
| azure | `extensions:configurations:read` |
| azure | `settings:objects:read` |
| breakpoint | `dev-obs:breakpoints:set` |
| bucket | `storage:buckets:read` |
| classic-pipelines | `settings:objects:read` |
| copilot-skill | `davis-copilot:conversations:execute` |
| dashboard | `document:documents:read` |
| discover | `storage:logs:read` |
| discover | `storage:spans:read` |
| document | `document:documents:read` |
| edgeconnect | `app-engine:edge-connects:read` |
| environment | `app-engine:apps:run` |
| environment | or instead any one of: `app-engine:functions:run`; `platform-management:environments:read` |
| extension | `extensions:definitions:read` |
| extension-config | `extensions:configurations:read` |
| function | `app-engine:apps:run` |
| gcp | `extensions:configurations:read` |
| gcp | `settings:objects:read` |
| group | `iam:groups:read` |
| hub-extension | `hub:catalog:read` |
| hub-extension-release | `hub:catalog:read` |
| intent | `app-engine:apps:run` |
| license | `app-engine:apps:run` |
| license | or instead any one of: `app-engine:functions:run`; `platform-management:environments:read` |
| license-settings | `app-engine:apps:run` |
| license-settings | or instead any one of: `app-engine:functions:run`; `platform-management:environments:read` |
| lookup | `storage:files:read` |
| lql-to-dql | `openpipeline:configurations:read` |
| notebook | `document:documents:read` |
| notification | `notification:notifications:read` |
| openpipeline-dql-processor | `openpipeline:configurations:read` |
| openpipeline-matcher | `openpipeline:configurations:read` |
| preview-processor | `openpipeline:configurations:read` |
| scheduling-rule | `automation:rules:read` |
| sdk-version | `app-engine:apps:run` |
| segment | `storage:filter-segments:read` |
| setting | `app-settings:objects:read` |
| setting | `settings:objects:read` |
| settings-schema | `settings:schemas:read` |
| slo | `slo:objective-templates:read` |
| slo | `slo:slos:read` |
| slo-template | `slo:objective-templates:read` |
| snapshot | `dev-obs:breakpoints:set` |
| trash | `document:trash.documents:read` |
| user | `iam:users:read` |
| wfe-task-result | `automation:workflows:read` |
| workflow | `automation:workflows:read` |
| workflow-execution | `automation:workflows:read` |

### run

| Resource | Scope |
| --- | --- |
| analyzer | `davis:analyzers:execute` |
| copilot | `davis-copilot:conversations:execute` |
| function | `app-engine:functions:run` |
| workflow | `automation:workflows:run` |

### write

| Resource | Scope |
| --- | --- |
| anomaly-detector | `settings:objects:write` |
| app | `app-engine:apps:install` |
| aws | `extensions:configurations:write` |
| aws | `settings:objects:write` |
| azure | `extensions:configurations:write` |
| azure | `settings:objects:write` |
| breakpoint | `dev-obs:breakpoints:set` |
| bucket | `storage:buckets:write` |
| dashboard | `document:documents:write` |
| document | `document:documents:write` |
| edgeconnect | `app-engine:edge-connects:write` |
| extension | `extensions:definitions:write` |
| extension-config | `extensions:configurations:write` |
| gcp | `extensions:configurations:write` |
| gcp | `settings:objects:write` |
| lookup | `storage:files:write` |
| notebook | `document:documents:write` |
| notification | `notification:notifications:write` |
| scheduling-rule | `automation:rules:write` |
| segment | `storage:filter-segments:write` |
| setting | `settings:objects:write` |
| slo | `slo:slos:write` |
| trash | `document:trash.documents:restore` |
| workflow | `automation:workflows:write` |
<!-- GENERATED:token-scopes:end -->
