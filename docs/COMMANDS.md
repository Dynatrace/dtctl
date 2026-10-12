# COMMANDS

Generated reference of every dtctl verb and the resources it operates on, with required token scopes.

## alias

Manage command aliases

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| delete | _(none declared)_ |
| export | _(none declared)_ |
| import | _(none declared)_ |
| list | _(none declared)_ |
| set | _(none declared)_ |


## apply

Apply a configuration to create or update resources

_mutating | access: write | safety: OperationCreate_

| Resource | Required scopes |
| --- | --- |
| extension-config | `extensions:configurations:write` |


## auth

Manage authentication and user identity

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| login | _(none declared)_ |
| logout | _(none declared)_ |
| refresh | _(none declared)_ |
| status | _(none declared)_ |
| whoami | _(none declared)_ |


## claim

[Experimental] Claim access to a shared resource

_mutating | access: claim | safety: OperationCreate_

| Resource | Required scopes |
| --- | --- |
| environment-share | `document:environment-shares:claim` |


## config

Manage dtctl configuration

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| current-context | _(none declared)_ |
| delete-context | _(none declared)_ |
| delete-credentials | _(none declared)_ |
| describe-context | _(none declared)_ |
| get-contexts | _(none declared)_ |
| init | _(none declared)_ |
| list-development | _(none declared)_ |
| migrate-tokens | _(none declared)_ |
| set | _(none declared)_ |
| set-context | _(none declared)_ |
| set-credentials | _(none declared)_ |
| use-context | _(none declared)_ |
| view | _(none declared)_ |


## create

Create resources from files

_mutating | access: write | safety: OperationCreate_

| Resource | Required scopes |
| --- | --- |
| anomaly-detector | `settings:objects:write` |
| breakpoint | `dev-obs:breakpoints:set` |
| bucket | `storage:buckets:write` |
| dashboard | `document:documents:write` |
| document | `document:documents:write` |
| edgeconnect | `app-engine:edge-connects:write` |
| extension | `extensions:definitions:write` |
| lookup | `storage:files:write` |
| notebook | `document:documents:write` |
| scheduling-rule | `automation:rules:write` |
| segment | `storage:filter-segments:write` |
| settings | `settings:objects:write` |
| slo | `slo:slos:write` |
| workflow | `automation:workflows:write` |

Subcommands:

| Subcommand | Description |
| --- | --- |
| aws | Create AWS resources |
| azure | Create Azure resources |
| gcp | Create GCP resources (Preview) |


## ctx

Manage contexts (shortcut for config context commands)

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| current | _(none declared)_ |
| delete | _(none declared)_ |
| describe | _(none declared)_ |
| set | _(none declared)_ |
| token | _(none declared)_ |


## delete

Delete resources

_mutating | access: delete | safety: OperationDelete_

| Resource | Required scopes |
| --- | --- |
| anomaly-detector | `settings:objects:write` |
| app | `app-engine:apps:delete` |
| breakpoint | `dev-obs:breakpoints:set` |
| bucket | `storage:buckets:write` |
| dashboard | `document:documents:delete` |
| document | `document:documents:delete` |
| edgeconnect | `app-engine:edge-connects:delete` |
| lookup | `storage:files:delete` |
| notebook | `document:documents:delete` |
| notification | `notification:notifications:write` |
| scheduling-rule | `automation:rules:write`, `automation:rules:read` |
| segment | `storage:filter-segments:delete` |
| settings | `settings:objects:write` |
| slo | `slo:slos:write` |
| trash | `document:trash.documents:delete` |
| workflow | `automation:workflows:write` |

Subcommands:

| Subcommand | Description |
| --- | --- |
| aws | Delete AWS resources |
| azure | Delete Azure resources |
| gcp | Delete GCP resources (Preview) |


## describe

Show details of a specific resource

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| analyzer | `davis:analyzers:read` |
| anomaly-detector | `settings:objects:read` |
| api | _(none declared)_ |
| app | `app-engine:apps:run` |
| breakpoint | `dev-obs:breakpoints:set` |
| bucket | `storage:buckets:read` |
| dashboard | `document:documents:read` |
| document | `document:documents:read` |
| edgeconnect | `app-engine:edge-connects:read` |
| environment | `app-engine:apps:run` |
| extension | `extensions:definitions:read` |
| extension-config | `extensions:configurations:read` |
| function | `app-engine:apps:run` |
| group | `iam:groups:read` |
| hub-extensions | `hub:catalog:read` |
| intent | `app-engine:apps:run` |
| license | `app-engine:apps:run` |
| lookup | `storage:files:read` |
| notebook | `document:documents:read` |
| scheduling-rule | `automation:rules:read` |
| segment | `storage:filter-segments:read` |
| settings | `settings:objects:read`, `app-settings:objects:read` |
| settings-schema | `settings:schemas:read` |
| slo | `slo:slos:read`, `slo:objective-templates:read` |
| trash | `document:trash.documents:read` |
| user | `iam:users:read` |
| workflow | `automation:workflows:read` |
| workflow-execution | `automation:workflows:read` |

Subcommands:

| Subcommand | Description |
| --- | --- |
| aws | Describe AWS resources |
| azure | Describe Azure resources |
| gcp | Describe GCP resources (Preview) |


## diff

Show differences between resources or files

_read-only | access: read_


## disable

Disable cloud monitoring configurations

_mutating | access: write | safety: OperationUpdate_

Subcommands:

| Subcommand | Description |
| --- | --- |
| aws | Disable AWS resources |
| azure | Disable Azure resources |
| gcp | Disable GCP resources (Preview) |


## doctor

Check configuration, connectivity, and authentication health

_read-only | access: read_


## download

Download raw resource artifacts

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| extension | `extensions:definitions:read` |


## edit

Edit a resource

_mutating | access: write | safety: OperationUpdate_

| Resource | Required scopes |
| --- | --- |
| anomaly-detector | `settings:objects:write` |
| dashboard | `document:documents:write` |
| document | `document:documents:write` |
| notebook | `document:documents:write` |
| segment | `storage:filter-segments:write` |
| setting | `settings:objects:write` |
| workflow | `automation:workflows:write` |

Subcommands:

| Subcommand | Description |
| --- | --- |
| aws | Edit AWS resources |
| azure | Edit Azure resources |
| gcp | Edit GCP resources (Preview) |


## enable

Enable cloud monitoring configurations

_mutating | access: write | safety: OperationUpdate_

Subcommands:

| Subcommand | Description |
| --- | --- |
| aws | Enable AWS resources |
| azure | Enable Azure resources |
| gcp | Enable GCP resources (Preview) |


## exec

Execute queries, workflows, or functions

_mutating | access: run | safety: OperationCreate_

| Resource | Required scopes |
| --- | --- |
| analyzer | `davis:analyzers:execute` |
| api | _(none declared)_ |
| function | `app-engine:functions:run` |
| preview-processor | `openpipeline:configurations:read` |
| slo | `slo:slos:read`, `slo:objective-templates:read` |
| workflow | `automation:workflows:run` |

Subcommands:

| Subcommand | Description |
| --- | --- |
| copilot | Chat with Davis CoPilot |


## find

Find resources based on criteria

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| intents | `app-engine:apps:run` |


## get

Display one or many resources

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| analyzers | `davis:analyzers:read` |
| anomaly-detectors | `settings:objects:read` |
| apis | _(none declared)_ |
| apps | `app-engine:apps:run` |
| breakpoints | `dev-obs:breakpoints:set` |
| buckets | `storage:buckets:read` |
| copilot-skills | `davis-copilot:conversations:execute` |
| dashboards | `document:documents:read` |
| documents | `document:documents:read` |
| edgeconnects | `app-engine:edge-connects:read` |
| environment | `app-engine:apps:run` |
| extension-configs | `extensions:configurations:read` |
| extensions | `extensions:definitions:read` |
| functions | `app-engine:apps:run` |
| groups | `iam:groups:read` |
| hub-extension-releases | `hub:catalog:read` |
| hub-extensions | `hub:catalog:read` |
| intents | `app-engine:apps:run` |
| license | `app-engine:apps:run` |
| license-settings | `app-engine:apps:run` |
| lookups | `storage:files:read` |
| notebooks | `document:documents:read` |
| notifications | `notification:notifications:read` |
| scheduling-rules | `automation:rules:read` |
| sdk-versions | `app-engine:apps:run` |
| segments | `storage:filter-segments:read` |
| settings | `settings:objects:read`, `app-settings:objects:read` |
| settings-schemas | `settings:schemas:read` |
| slo-templates | `slo:objective-templates:read` |
| slos | `slo:slos:read`, `slo:objective-templates:read` |
| snapshots | `dev-obs:breakpoints:set` |
| trash | `document:trash.documents:read` |
| users | `iam:users:read` |
| wfe-task-result | `automation:workflows:read` |
| workflow-executions | `automation:workflows:read` |
| workflows | `automation:workflows:read` |

Subcommands:

| Subcommand | Description |
| --- | --- |
| aws | Get AWS resources |
| azure | Get Azure resources |
| gcp | Get GCP resources (Preview) |


## history

Show version history of resources

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| dashboard | `document:documents:read` |
| document | `document:documents:read` |
| notebook | `document:documents:read` |
| workflow | `automation:workflows:read` |


## inspect

Inspect a spilled query-result file locally (row access, schema, stats)

_read-only | access: read_


## inventory

[Experimental] Probe the environment: which data, entity types, and capabilities exist here

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| arrivals | `storage:logs:read`, `storage:events:read`, `storage:metrics:read`, `storage:spans:read`, `storage:bizevents:read`, `storage:entities:read`, `storage:smartscape:read`, `storage:system:read`, `storage:security.events:read` |


## logs

Print logs for resources

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| workflow-execution | `automation:workflows:read` |


## open

Open resources in browser

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| intent | `app-engine:apps:run` |


## plugin

Manage dtctl plugins (executables named dtctl-* on PATH)

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| list | _(none declared)_ |


## query

Execute a DQL query

_read-only | access: read_


## repo-scope

[Experimental] Link this git repository to the entities that run its code

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| current | _(none declared)_ |
| delete | _(none declared)_ |
| describe | _(none declared)_ |
| discover | `storage:spans:read`, `storage:logs:read` |
| list | _(none declared)_ |
| set | _(none declared)_ |


## restore

Restore resources to a previous version

_mutating | access: write | safety: OperationUpdate_

| Resource | Required scopes |
| --- | --- |
| dashboard | `document:documents:write` |
| document | `document:documents:write` |
| notebook | `document:documents:write` |
| trash | `document:trash.documents:restore` |
| workflow | `automation:workflows:write` |


## share

Share documents with users or groups

_mutating | access: write | safety: OperationUpdate_

| Resource | Required scopes |
| --- | --- |
| dashboard | `document:documents:write` |
| document | `document:documents:write` |
| notebook | `document:documents:write` |


## skills

Manage AI coding assistant skill files

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| install | _(none declared)_ |
| status | _(none declared)_ |
| uninstall | _(none declared)_ |


## translate

Translate expressions between formats

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| classic-pipelines | `settings:objects:read` |
| lql-to-dql | `openpipeline:configurations:read` |


## unshare

Remove sharing from documents

_mutating | access: write | safety: OperationUpdate_

| Resource | Required scopes |
| --- | --- |
| dashboard | `document:documents:write` |
| document | `document:documents:write` |
| notebook | `document:documents:write` |


## update

Update resources

_mutating | access: write | safety: OperationUpdate_

| Resource | Required scopes |
| --- | --- |
| breakpoint | `dev-obs:breakpoints:set` |
| document | `document:documents:write` |
| extension | `extensions:definitions:write` |
| extensions | `extensions:definitions:write` |

Subcommands:

| Subcommand | Description |
| --- | --- |
| aws | Update AWS resources |
| azure | Update Azure resources |
| gcp | Update GCP resources (Preview) |


## verify

Verify resources without executing them

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| analyzer | `davis:analyzers:read` |
| openpipeline-dql-processor | `openpipeline:configurations:read` |
| openpipeline-matcher | `openpipeline:configurations:read` |
| query | _(none declared)_ |


## wait

Wait for specific conditions on resources

_read-only | access: read_

| Resource | Required scopes |
| --- | --- |
| query | _(none declared)_ |


