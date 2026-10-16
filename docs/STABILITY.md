# dtctl stability manifest

<!-- GENERATED FILE — do not edit. Regenerate with: make stability-manifest -->

Every command and flag dtctl exposes, with the contract it carries. This file is
generated from the live command tree and checked in, so a change to the
contract shows up as a reviewable diff and a failing build rather than as a
silent break.

| Tier | Promise |
|---|---|
| `stable` | Invocation and output contract are additive-only. Removal or an incompatible change requires a deprecation cycle. |
| `experimental` | May change or be removed in any release. Not covered by dtctl's stability guarantees. |
| `development` | Unfinished, no guarantees — of any kind, including the backend's. Not registered unless opted in via `dtctl config set development.<feature> on`. |

A tier describes the whole path, not just dtctl's side of it. A `development`
command commonly targets an unreleased or feature-flagged API, so it may return
404, 403 or 500 on an environment where the feature is simply not deployed —
and dtctl does **not** translate that into "not available here". Read a failure
from a development command as "this does not work yet", not as a bug report.

Stable is never implied. Every command declares its tier where it is built and
the build fails on one that does not; a command that somehow reaches runtime
undeclared resolves to `experimental`, never to `stable`.
The strongest promise dtctl makes is not one a command can acquire by nobody
having thought about it.

Deprecation is not a tier: a deprecated command is still stable in shape and is
merely scheduled for removal. It is recorded on the same line.

A flag line ending in `(declared)` carries a tier declaration of its own; any
other flag inherits its command's. The flags that predate this rule may keep
inheriting, but a flag that newly becomes `stable` under a command that was
already `stable` has to be declared — `stability.MarkFlagStable`, or
`stability.MarkFlag` with a weaker tier — and `make stability-compat`
refuses one that is not. Otherwise a flag would be stable by omission, the one
tier nobody chose deliberately.

The `(global)` group at the top is not a command. It is the root command's
persistent flags — the ones every command accepts. They are listed because a
flag that appears nowhere in this file is stable by omission, which is the one
tier nobody chose deliberately. The floor applies to them on whichever command
you type, and their tier is necessarily tree-wide: cobra gives every subcommand
the same flag the root declared, so a global flag cannot be stable on one
command and experimental on another.

## Choosing what this environment accepts

The **stability floor** is the weakest contract a command or flag may offer and
still be usable. It defaults to `experimental`, so interactive use keeps
getting new (badged) surface. Automation should pin `stable`:

```bash
# Per context, persisted in the config file
dtctl config set-context prod-agent --min-stability stable

# Per process — this is what an embedding service sets, and it is deliberately
# not a command-line flag: the embedder controls argv, so a flag would hand the
# opt-in to exactly the party the floor exists to constrain.
DTCTL_MIN_STABILITY=stable dtctl get workflows
```

A single below-floor command or flag can be admitted without lowering the floor
for everything. Each entry is one audited risk acceptance. Naming a command
also admits the flags that are below the floor only because that command is;
a flag with a weaker contract of its own needs its own entry, because there
the flag — not the command — is the risk being accepted:

```bash
dtctl config set-context prod-agent --min-stability stable \
  --stability-exception 'inventory' \
  --stability-exception 'query --decode-snapshots'
```

Development-tier features are enabled individually and never by a floor:

```bash
dtctl config list-development                 # what this build carries
dtctl config set development.serve on         # persistent
DTCTL_DEVELOPMENT=serve dtctl serve http      # one process
```

## Finding out early what a removal will break

`DTCTL_NO_DEPRECATED=1` makes dtctl behave as if every deprecated command
and flag had already been removed (commands and flags only:
output fields carry no deprecation record, see "What `stable` promises before
1.0"). A deprecation warning in a log is
easy to miss; a failing pipeline is not. Run a CI job with it set and it fails
now, while that is a fixable build, rather than on the day the removal release
lands:

```bash
# Set it once for a whole pipeline, not per command: the point is to find out
# which of a hundred invocations still depends on dated surface.
export DTCTL_NO_DEPRECATED=1
./scripts/nightly-report.sh
# error: this context accepts no deprecated surface: flag --params of command
#        "exec workflow", deprecated: ... use --input instead.
```

Orthogonal to the floor, and not a tier: a deprecated command is still stable
in shape and satisfies any floor, it merely has a removal date. No floor and no
exception will bring it back — only a migration will.

The mode is off unless asked for and can be made durable per context
(`no-deprecated: true`). The environment variable overrides the context
in *both* directions, so the one run that is still mid-migration can set
`DTCTL_NO_DEPRECATED=0` without editing a config it shares with every
other run. Embedded callers use `engine.Request.NoDeprecated`; the
variable is scrubbed from a session-backed run, like the floor is.

### Embedded and service callers

When dtctl is embedded (`pkg/engine`, `cmd.Session`) the floor is a
per-request field, and it defaults to `stable` rather than
`experimental`. A request is unattended automation: nobody reads the
`[Experimental]` badge on its behalf. Widen it deliberately, and prefer
naming the individual commands the host has tested over granting the whole tier:

```go
engine.Request{
    Command:             "inventory --agent",
    MinStability:        "",                      // stable, the safe default
    StabilityExceptions: []string{"inventory"},   // just this one, audited
}
```

Environment variables do not reach a session-backed run: `DTCTL_MIN_STABILITY`
and `DTCTL_DEVELOPMENT` are scrubbed, because which commands exist is the
request's decision and not the host process's. See
`docs/dev/SERVICE_ENGINE_DESIGN.md`.

## What `stable` promises before 1.0

dtctl is pre-1.0, and `release-please-config.json` sets
`bump-minor-pre-major`: a breaking change ships as a minor bump, and semver's
own 0.x clause says anything may change at any time. Read literally, that would
make every badge in this file worthless on the day it shipped. So the promise is
carved out of the version policy rather than derived from it.

**The tier carries the contract, not the version number.** A `stable`
command's invocation and output are additive-only from the release that declared
it, whatever the version number does next. dtctl does not get to break a
`stable` command in 0.40.0 on the grounds that 0.x permits it.

**A removal needs a deprecation cycle, and the deprecation record is the
signal.** A minor bump cannot be read as "safe" here, so the warning cannot come
from the version number. It comes from the surface instead:

- The removal is announced by deprecating the command or flag, naming the
  version that deprecated it and the version it will disappear in.
- That removal version is at least **two minor releases** after the deprecating
  one, so there is always a release you can run that both warns and still works.
- `DTCTL_NO_DEPRECATED=1` turns the warning into a failing build on demand,
  and this file records every deprecation. Together they are what replaces
  "watch the major version".

`stability.Lint` enforces the window, so a deprecation that skips it fails the
build rather than a review.

**Surface that a known breaking change already targets is not `stable`.**
This is what keeps the promise honest without a major version available: when an
accepted breaking-change document renames or removes a flag, the flag is
declared `experimental` *now*, ahead of the break, rather than carrying a
`stable` badge dtctl already knows it cannot keep. Most of the
`experimental` entries below are there for that reason and no other.

**Agent-mode output is shaped for agents, not frozen.** The additive-only
promise covers what a caller *types* (commands, flags, arguments, exit codes)
and the output outside agent mode. In agent mode (`--agent`, `-A`, or
auto-detected) the reader is a model that reads every response afresh and adapts
to it, so what the envelope carries may change in any minor release, on
`stable` commands too: which fields and how many items a default returns, how
rows are encoded, how numbers are rounded. Such a change is not a breaking
change: the release notes do not flag it as one, it needs no deprecation cycle
and no breaking-change document, and it does not demote the command. Three things still hold:

- The envelope's skeleton stays additive-only. `ok`, `error.code`,
  `result.kind` and the `context` keys keep their names, types and meanings,
  because host code parses those, not the model.
- A change describes itself. When a default drops, clips, rounds, pages or
  re-encodes data, the envelope says so (`context.format`, `context.has_more`,
  or a `context.suggestions` entry naming the flag that restores the full data).
- An explicit flag keeps its meaning. A program that needs a fixed shape passes
  the flags for it (for example `-o json`) rather than relying on agent-mode
  defaults, and that invocation is covered by the promise above.

**Output outside agent mode is promised per command, not per field.** A
`stable` command's output (JSON and YAML fields, table columns) is additive-only
as a whole, and what enforces that is the golden tests, which snapshot each
printer's output per resource and fail on any change. Content that comes from
the environment rather than from dtctl is outside that promise: the response
body `exec api` passes through, and the records a query returns, change when
the platform's APIs and data do. For those commands the promise covers the
invocation and how dtctl frames what it prints, not the payload. There is no
finer grain: an output field cannot declare a tier of its own, so there is no
experimental or deprecated field on a `stable` command, this file lists no
fields, and `DTCTL_NO_DEPRECATED` covers commands and flags but not fields.
A new field that should not carry the promise ships behind an experimental flag
or on an experimental command, because that is the finest grain a tier can be
declared at.

**`experimental` and `development` are not covered.** They may
change in any release, patch releases included. That is the whole distinction.

**1.0 adds to this policy, it does not replace it.** Semver then applies on its
own terms: a removal from the stable surface needs a major bump *as well as* the
deprecation cycle. Nothing stated here is relaxed by reaching 1.0.

## Summary

- commands: 269 stable, 19 experimental, 14 development
- global flags (accepted on every command): 11
- entries below (commands + flags): 956

## Surface

```
(global)                             stable
  --agent                            stable
  --check-scopes                     stable
  --chunk-size                       stable
  --config                           stable
  --context                          stable
  --debug                            stable
  --jq                               stable
  --no-agent                         stable
  --output                           stable
  --plain                            stable
  --verbose                          stable
account                              development  (opt-in key: account)
account create                       development
account create service-user          development
  --description                      development
  --dry-run                          development
  --name                             development
account create token                 development
  --dry-run                          development
  --expires                          development
  --expires-at                       development
  --name                             development
  --resource                         development
  --scope                            development
  --tag                              development
  --user-uuid                        development
account delete                       development
account delete service-user          development
  --dry-run                          development
account delete token                 development
  --dry-run                          development
account list                         development
account list service-user            development
account list token                   development
account login                        development
  --account-uuid                     development
  --timeout                          development
account status                       development
alias                                stable
alias delete                         stable
alias export                         stable
  --file                             stable
alias import                         stable
  --file                             stable
  --overwrite                        stable
alias list                           stable
alias set                            stable
apply                                stable
  --admin-access                     experimental  since 0.44.0  (declared)
  --create-snapshot                  stable
  --dry-run                          stable
  --file                             stable
  --id                               stable
  --label                            stable
  --no-hooks                         stable
  --set                              stable
  --share-environment                stable
  --show-diff                        stable
  --snapshot-description             stable
  --type                             stable
  --write-id                         stable
apply extension-config               stable
  --dry-run                          stable
  --file                             stable
  --scope                            stable
  --set                              stable
auth                                 stable
auth login                           stable
  --account-urn                      stable
  --client-id                        stable
  --client-secret                    stable
  --context                          stable
  --environment                      stable
  --safety-level                     stable
  --scopes                           stable
  --timeout                          stable
  --token-name                       stable
auth logout                          stable
  --remove-context                   stable
auth refresh                         stable
auth status                          stable
auth whoami                          stable
  --id-only                          stable
  --refresh                          stable
claim                                experimental  since 0.41.0
claim environment-share              experimental  since 0.41.0
  --dry-run                          experimental
commands                             stable
  --brief                            stable
  --full                             stable
  --required-scopes                  stable
commands howto                       stable
completion                           stable
config                               stable
config current-context               stable
config delete-context                stable
  --delete-credentials               stable
  --dry-run                          stable
config delete-credentials            stable
  --dry-run                          stable
config describe-context              stable
config get-contexts                  stable
config init                          stable
  --context                          stable
  --force                            stable
config list-development              stable
config migrate-tokens                stable
config set                           stable
config set-context                   stable
  --description                      stable
  --environment                      stable
  --global                           stable
  --min-stability                    stable
  --profile                          stable
  --safety-level                     stable
  --stability-exception              stable
  --token-ref                        stable
  --usage-tracking                   experimental  since 0.43.0  (declared)
config set-credentials               stable
  --global                           stable
  --token                            stable
config use-context                   stable
config view                          stable
create                               stable
create anomaly-detector              stable
  --dry-run                          stable
  --file                             stable
  --set                              stable
create aws                           stable
create aws connection                stable
  --dry-run                          stable
  --name                             stable
  --roleArn                          experimental  since 0.39.0  (declared)
create aws monitoring                stable
  --central-enrichment               stable
  --credentials                      stable
  --dry-run                          stable
  --featureSets                      experimental  since 0.39.0  (declared)
  --name                             stable
  --regions                          stable
create azure                         stable
create azure connection              stable
  --applicationId                    experimental  since 0.39.0  (declared)
  --clientSecret                     experimental  since 0.39.0  (declared)
  --directoryId                      experimental  since 0.39.0  (declared)
  --dry-run                          stable
  --issuer                           stable
  --name                             stable
  --type                             stable
create azure monitoring              stable
  --central-enrichment               stable
  --credentials                      stable
  --dry-run                          stable
  --featureSets                      experimental  since 0.39.0  (declared)
  --featuresets                      experimental  since 0.39.0  (declared)
  --locationFiltering                experimental  since 0.39.0  (declared)
  --name                             stable
create breakpoint                    experimental  since 0.39.0
  --dry-run                          experimental
  --filters                          experimental
  --yes                              experimental
create bucket                        stable
  --display-name                     stable
  --dry-run                          stable
  --file                             stable
  --name                             stable
  --retention                        stable
  --table                            stable
create dashboard                     stable
  --description                      stable
  --dry-run                          stable
  --file                             stable
  --id                               stable
  --name                             stable
  --set                              stable
create document                      stable
  --description                      stable
  --dry-run                          stable
  --file                             stable
  --id                               stable
  --label                            stable
  --name                             stable
  --set                              stable
  --type                             stable
create edgeconnect                   stable
  --dry-run                          stable
  --file                             stable
  --host-patterns                    stable
  --name                             stable
create extension                     stable
  --dry-run                          stable
  --file                             stable
  --hub-extension                    stable
  --version                          stable
create gcp                           stable
create gcp connection                stable
  --dry-run                          stable
  --name                             stable
  --serviceAccountId                 experimental  since 0.39.0  (declared)
  --serviceaccountid                 experimental  since 0.39.0  (declared)
create gcp monitoring                stable
  --central-enrichment               stable
  --credentials                      stable
  --dry-run                          stable
  --featureSets                      experimental  since 0.39.0  (declared)
  --featuresets                      experimental  since 0.39.0  (declared)
  --locationFiltering                experimental  since 0.39.0  (declared)
  --name                             stable
create lookup                        stable
  --description                      stable
  --display-name                     stable
  --dry-run                          stable
  --file                             stable
  --locale                           stable
  --lookup-field                     stable
  --parse-pattern                    stable
  --path                             stable
  --skip-records                     stable
  --timezone                         stable
create notebook                      stable
  --description                      stable
  --dry-run                          stable
  --file                             stable
  --id                               stable
  --name                             stable
  --set                              stable
create scheduling-rule               stable
  --dry-run                          stable
  --file                             stable
  --set                              stable
create segment                       stable
  --dry-run                          stable
  --file                             stable
create settings                      stable
  --dry-run                          stable
  --file                             stable
  --schema                           stable
  --scope                            stable
  --set                              stable
  --validate-only                    stable
create slo                           stable
  --dry-run                          stable
  --file                             stable
  --set                              stable
create workflow                      stable
  --dry-run                          stable
  --file                             stable
  --set                              stable
ctx                                  stable
ctx current                          stable
ctx delete                           stable
  --delete-credentials               stable
  --dry-run                          stable
ctx describe                         stable
ctx set                              stable
  --description                      stable
  --environment                      stable
  --global                           stable
  --min-stability                    stable
  --profile                          stable
  --safety-level                     stable
  --stability-exception              stable
  --token-ref                        stable
  --usage-tracking                   experimental  since 0.43.0  (declared)
ctx token                            stable
delete                               stable
delete anomaly-detector              stable
  --dry-run                          stable
  --yes                              stable
delete app                           stable
  --dry-run                          stable
  --yes                              stable
delete aws                           stable
delete aws connection                stable
  --dry-run                          stable
delete aws monitoring                stable
  --dry-run                          stable
delete azure                         stable
delete azure connection              stable
  --dry-run                          stable
delete azure monitoring              stable
  --dry-run                          stable
delete breakpoint                    experimental  since 0.39.0
  --all                              experimental
  --dry-run                          experimental
  --yes                              experimental
delete bucket                        stable
  --confirm                          stable
  --dry-run                          stable
  --yes                              stable
delete dashboard                     stable
  --dry-run                          stable
  --yes                              stable
delete document                      stable
  --dry-run                          stable
  --yes                              stable
delete edgeconnect                   stable
  --dry-run                          stable
  --yes                              stable
delete gcp                           stable
delete gcp connection                stable
  --dry-run                          stable
delete gcp monitoring                stable
  --dry-run                          stable
delete lookup                        stable
  --dry-run                          stable
  --yes                              stable
delete notebook                      stable
  --dry-run                          stable
  --yes                              stable
delete notification                  stable
  --dry-run                          stable
  --yes                              stable
delete scheduling-rule               stable
  --dry-run                          stable
  --yes                              stable
delete segment                       stable
  --confirm                          stable
  --dry-run                          stable
  --yes                              stable
delete settings                      stable
  --dry-run                          stable
  --yes                              stable
delete slo                           stable
  --dry-run                          stable
  --yes                              stable
delete trash                         stable
  --dry-run                          stable
  --permanent                        stable
  --yes                              stable
delete workflow                      stable
  --admin-access                     experimental  since 0.44.0  (declared)
  --dry-run                          stable
  --yes                              stable
describe                             stable
describe analyzer                    stable
  --doc                              stable
describe anomaly-detector            stable
describe api                         stable
  --operation                        stable
  --raw                              stable
  --spill                            stable
  --spill-format                     stable
  --spill-threshold                  stable
  --spill-to                         stable
describe app                         stable
describe aws                         stable
describe aws connection              stable
describe aws monitoring              stable
describe azure                       stable
describe azure connection            stable
describe azure monitoring            stable
describe breakpoint                  experimental  since 0.39.0
describe bucket                      stable
describe dashboard                   stable
describe document                    stable
describe edgeconnect                 stable
describe environment                 stable
describe extension                   stable
  --active-gate-groups               stable
  --assets                           stable
  --feature-set-metrics              stable
  --full                             stable
  --monitoring-configuration-schema  stable
  --no-fluff                         stable
  --version                          stable
describe extension-config            stable
  --config-id                        stable
describe function                    stable
describe gcp                         stable
describe gcp connection              stable
describe gcp monitoring              stable
describe group                       stable
describe hub-extensions              stable
describe intent                      stable
describe license                     stable
describe lookup                      stable
describe notebook                    stable
describe scheduling-rule             stable
describe segment                     stable
describe settings                    stable
describe settings-schema             stable
describe slo                         stable
describe trash                       stable
describe user                        stable
describe workflow                    stable
  --admin-access                     experimental  since 0.44.0  (declared)
describe workflow-execution          stable
  --admin-access                     experimental  since 0.44.0  (declared)
diff                                 stable
  --color                            stable
  --context                          experimental  since 0.39.0  (declared)
  --file                             stable
  --format                           stable
  --ignore-metadata                  stable
  --ignore-order                     stable
  --output                           experimental  since 0.39.0  (declared)
  --quiet                            stable
  --semantic                         stable
  --side-by-side                     stable
disable                              stable
disable aws                          stable
disable aws monitoring               stable
  --dry-run                          stable
  --name                             stable
disable azure                        stable
disable azure monitoring             stable
  --dry-run                          stable
  --name                             stable
disable gcp                          stable
disable gcp monitoring               stable
  --dry-run                          stable
  --name                             stable
doctor                               stable
download                             stable
download extension                   stable
  --version                          stable
edit                                 stable
edit anomaly-detector                stable
edit aws                             stable
edit aws monitoring                  stable
  --format                           stable
  --name                             stable
edit azure                           stable
edit azure monitoring                stable
  --format                           stable
  --name                             stable
edit dashboard                       stable
  --create-snapshot                  stable
  --format                           stable
  --snapshot-description             stable
edit document                        stable
  --create-snapshot                  stable
  --format                           stable
  --snapshot-description             stable
edit gcp                             stable
edit gcp monitoring                  stable
  --format                           stable
  --name                             stable
edit notebook                        stable
  --create-snapshot                  stable
  --format                           stable
  --snapshot-description             stable
edit segment                         stable
  --format                           stable
edit setting                         stable
  --format                           stable
  --validate-only                    stable
edit workflow                        stable
  --admin-access                     experimental  since 0.44.0  (declared)
  --format                           stable
enable                               stable
enable aws                           stable
enable aws monitoring                stable
  --dry-run                          stable
  --name                             stable
  --roleArn                          experimental  since 0.39.0  (declared)
enable azure                         stable
enable azure monitoring              stable
  --applicationId                    experimental  since 0.39.0  (declared)
  --directoryId                      experimental  since 0.39.0  (declared)
  --dry-run                          stable
  --name                             stable
enable gcp                           stable
enable gcp monitoring                stable
  --dry-run                          stable
  --name                             stable
  --serviceAccountId                 experimental  since 0.39.0  (declared)
exec                                 stable
exec analyzer                        stable
  --file                             stable
  --input                            stable
  --precision                        experimental  since 0.42.0  (declared)
  --query                            stable
  --series                           experimental  since 0.42.0  (declared)
  --timeout                          experimental  since 0.39.0  (declared)
  --validate                         stable
  --wait                             experimental  since 0.39.0  (declared)
exec api                             stable
  --data                             stable
  --dry-run                          stable
  --header                           stable
  --method                           stable
exec copilot                         stable
  --context                          experimental  since 0.39.0  (declared)
  --file                             stable
  --instruction                      stable
  --no-docs                          stable
  --stream                           stable
exec copilot document-search         stable
  --collections                      stable
  --exclude                          stable
exec copilot dql2nl                  stable
  --file                             stable
exec copilot nl2dql                  stable
  --file                             stable
exec dql                             stable
  --file                             stable
exec function                        stable
  --code                             stable
  --data                             stable
  --defer                            stable
  --file                             stable
  --method                           stable
  --payload                          stable
exec preview-processor               stable
  --config-id                        stable
  --file                             stable
exec slo                             stable
  --timeout                          experimental  since 0.39.0  (declared)
exec workflow                        experimental  since 0.39.0
  --input                            experimental
  --params                           experimental
  --show-results                     experimental
  --timeout                          experimental
  --wait                             experimental
find                                 stable
find intents                         stable
  --data                             stable
  --data-file                        stable
  --limit                            stable
get                                  stable
  --fields                           experimental  since 0.40.0  (declared)
  --limit                            experimental  since 0.40.0  (declared)
get analyzers                        stable
  --filter                           stable
get anomaly-detectors                stable
  --enabled                          stable
get apis                             stable
  --ops-count                        stable
  --uncovered                        stable
get apps                             stable
get aws                              stable
get aws connections                  stable
get aws monitoring                   stable
get aws monitoring-feature-sets      stable
get aws monitoring-regions           stable
get azure                            stable
get azure connections                stable
get azure monitoring                 stable
get azure monitoring-feature-sets    stable
get azure monitoring-locations       stable
get breakpoints                      experimental  since 0.39.0
get buckets                          stable
get copilot-skills                   stable
get dashboards                       stable
  --add-fields                       stable
  --admin-access                     stable
  --filter                           stable
  --interval                         stable
  --mine                             stable
  --name                             stable
  --sort                             stable
  --watch                            stable
  --watch-only                       stable
get documents                        stable
  --add-fields                       stable
  --admin-access                     stable
  --filter                           stable
  --interval                         stable
  --mine                             stable
  --name                             stable
  --sort                             stable
  --type                             stable
  --types                            stable
  --watch                            stable
  --watch-only                       stable
get edgeconnects                     stable
get environment                      stable
get extension-configs                stable
  --config-id                        stable
  --version                          stable
get extensions                       stable
  --name                             stable
get functions                        stable
  --app                              stable
get gcp                              stable
get gcp connections                  stable
get gcp connections principal        stable
get gcp monitoring                   stable
get gcp monitoring-feature-sets      stable
get gcp monitoring-locations         stable
get groups                           stable
  --filter                           stable
get hub-extension-releases           stable
get hub-extensions                   stable
  --filter                           stable
get intents                          stable
  --app                              stable
get license                          stable
get license-settings                 stable
get lookups                          stable
get notebooks                        stable
  --add-fields                       stable
  --admin-access                     stable
  --filter                           stable
  --interval                         stable
  --mine                             stable
  --name                             stable
  --sort                             stable
  --watch                            stable
  --watch-only                       stable
get notifications                    stable
  --type                             stable
get scheduling-rules                 stable
  --interval                         stable
  --limit                            stable
  --watch                            stable
  --watch-only                       stable
get sdk-versions                     stable
get segments                         stable
get settings                         stable
  --schema                           stable
  --scope                            stable
get settings-schemas                 stable
get slo-templates                    stable
  --filter                           stable
get slos                             stable
  --filter                           stable
get snapshots                        experimental  since 0.39.0
  --decode-snapshots                 experimental
  --default-timeframe-end            experimental
  --default-timeframe-start          experimental
  --limit                            experimental
  --max-result-records               experimental
  --metadata                         experimental
  --no-progress                      experimental
get trash                            stable
  --deleted-after                    stable
  --deleted-before                   stable
  --deleted-by                       stable
  --interval                         stable
  --type                             stable
  --watch                            stable
  --watch-only                       stable
get users                            stable
  --filter                           stable
get wfe-task-result                  stable
  --admin-access                     experimental  since 0.44.0  (declared)
  --task                             stable
get workflow-executions              stable
  --admin-access                     experimental  since 0.44.0  (declared)
  --limit                            stable
  --started-since                    stable
  --started-until                    stable
  --state                            stable
  --trigger                          stable
  --workflow                         stable
get workflows                        stable
  --admin-access                     experimental  since 0.44.0  (declared)
  --filter                           stable
  --interval                         stable
  --limit                            stable
  --mine                             stable
  --trigger                          experimental  since 0.39.0  (declared)
  --type                             stable
  --watch                            stable
  --watch-only                       stable
history                              stable
history dashboard                    stable
history document                     stable
history notebook                     stable
history workflow                     stable
  --admin-access                     experimental  since 0.44.0  (declared)
inspect                              stable
  --fields                           stable
  --head                             stable
  --limit                            stable
  --list                             stable
  --offset                           stable
  --page                             stable
  --sample                           stable
  --schema                           stable
  --spill                            stable
  --spill-format                     stable
  --spill-threshold                  stable
  --spill-to                         stable
  --stats                            stable
  --tail                             stable
inventory                            experimental  since 0.39.0
  --budget-queries                   experimental
  --budget-seconds                   experimental
  --definitions                      experimental
  --no-builtin-definitions           experimental
  --scan-limit-gbytes                experimental
inventory arrivals                   experimental
  --budget-queries                   experimental
  --budget-seconds                   experimental
  --definitions                      experimental
  --no-builtin-definitions           experimental
  --no-sample                        experimental
  --require                          experimental
  --scan-limit-gbytes                experimental
  --scope                            experimental
  --signals                          experimental
  --since                            experimental
  --stale-after                      experimental
logs                                 stable
logs workflow-execution              experimental  since 0.39.0
  --admin-access                     experimental  since 0.44.0  (declared)
  --all                              experimental
  --follow                           experimental  since 0.39.0  (declared)
  --task                             experimental
  --tasks                            experimental
open                                 stable
open intent                          stable
  --browser                          stable
  --data                             stable
  --data-file                        stable
plugin                               stable
plugin list                          stable
query                                stable
  --client-context                   stable
  --compact                          experimental  since 0.40.0  (declared)
  --decode-snapshots                 experimental  since 0.39.0  (declared)
  --default-sampling-ratio           stable
  --default-scan-limit-gbytes        stable
  --default-timeframe-end            stable
  --default-timeframe-start          stable
  --dql                              stable
  --enable-preview                   stable
  --enforce-query-consumption-limit  stable
  --fetch-timeout-seconds            stable
  --file                             stable
  --fullscreen                       stable
  --height                           stable
  --include-contributions            stable
  --include-types                    stable
  --interval                         stable
  --live                             stable
  --locale                           stable
  --max-field-chars                  experimental  since 0.40.0  (declared)
  --max-output-bytes                 experimental  since 0.40.0  (declared)
  --max-output-tokens                experimental  since 0.40.0  (declared)
  --max-result-bytes                 stable
  --max-result-records               stable
  --metadata                         stable
  --no-progress                      stable
  --no-query-limits                  stable
  --precision                        experimental  since 0.40.0  (declared)
  --segment                          stable
  --segment-var                      stable
  --segments-file                    stable
  --series                           experimental  since 0.40.0  (declared)
  --set                              stable
  --spill                            stable
  --spill-format                     stable
  --spill-threshold                  stable
  --spill-to                         stable
  --timezone                         stable
  --typed                            stable
  --width                            stable
restore                              stable
restore dashboard                    stable
  --dry-run                          stable
  --force                            experimental  since 0.39.0  (declared)
restore document                     stable
  --dry-run                          stable
  --force                            experimental  since 0.39.0  (declared)
restore notebook                     stable
  --dry-run                          stable
  --force                            experimental  since 0.39.0  (declared)
restore trash                        stable
  --dry-run                          stable
  --force                            stable
  --new-name                         stable
restore workflow                     stable
  --admin-access                     experimental  since 0.44.0  (declared)
  --dry-run                          stable
  --force                            experimental  since 0.39.0  (declared)
serve                                development  (opt-in key: serve)
serve http                           development
  --addr                             development
  --idle-timeout                     development
  --max-duration                     development
  --max-queued                       development
  --max-request-bytes                development
  --read-timeout                     development
  --write-timeout                    development
share                                stable
share dashboard                      stable
  --access                           stable
  --dry-run                          stable
  --group                            stable
  --no-notify                        experimental  since 0.40.0  (declared)
  --user                             stable
share document                       stable
  --access                           stable
  --dry-run                          stable
  --group                            stable
  --no-notify                        experimental  since 0.40.0  (declared)
  --user                             stable
share notebook                       stable
  --access                           stable
  --dry-run                          stable
  --group                            stable
  --no-notify                        experimental  since 0.40.0  (declared)
  --user                             stable
skills                               stable
skills install                       stable
  --cross-client                     stable
  --for                              stable
  --force                            stable
  --global                           stable
  --list                             stable
skills status                        stable
  --for                              stable
skills uninstall                     stable
  --cross-client                     stable
  --for                              stable
token-scopes                         stable
translate                            stable
translate classic-pipelines          stable
  --include-sample-data              stable
  --skip-builtin-processing-rules    stable
  --skip-disabled-rules              stable
translate lql-to-dql                 stable
  --file                             stable
unshare                              stable
unshare dashboard                    stable
  --access                           stable
  --all                              stable
  --dry-run                          stable
  --group                            stable
  --user                             stable
unshare document                     stable
  --access                           stable
  --all                              stable
  --dry-run                          stable
  --group                            stable
  --user                             stable
unshare notebook                     stable
  --access                           stable
  --all                              stable
  --dry-run                          stable
  --group                            stable
  --user                             stable
update                               stable
update aws                           stable
update aws connection                experimental  since 0.39.0
  --dry-run                          experimental
  --name                             experimental
  --roleArn                          experimental  since 0.39.0  (declared)
update aws monitoring                stable
  --dry-run                          stable
  --featureSets                      experimental  since 0.39.0  (declared)
  --name                             stable
  --regions                          stable
update azure                         stable
update azure connection              experimental  since 0.39.0
  --aplicationID                     experimental  since 0.39.0  (declared)
  --applicationID                    experimental  since 0.39.0  (declared)
  --applicationId                    experimental  since 0.39.0  (declared)
  --clientSecret                     experimental  since 0.39.0  (declared)
  --directoryID                      experimental  since 0.39.0  (declared)
  --directoryId                      experimental  since 0.39.0  (declared)
  --dry-run                          experimental
  --name                             experimental
update azure monitoring              experimental  since 0.39.0
  --dry-run                          experimental
  --featureSets                      experimental  since 0.39.0  (declared)
  --featuresets                      experimental  since 0.39.0  (declared)
  --locationFiltering                experimental  since 0.39.0  (declared)
  --name                             experimental
update breakpoint                    experimental  since 0.39.0
  --condition                        experimental
  --dry-run                          experimental
  --enabled                          experimental
  --filters                          experimental
  --log-message                      experimental
  --yes                              experimental
update document                      stable
  --create-snapshot                  stable
  --dry-run                          stable
  --file                             stable
  --id                               stable
  --label                            stable
  --set                              stable
  --show-diff                        stable
  --snapshot-description             stable
  --type                             stable
update extension                     stable
  --dry-run                          stable
  --hub-latest                       stable
  --latest                           stable
  --version                          stable
  --with-configurations              stable
update extensions                    stable
  --all                              stable
  --dry-run                          stable
  --hub-latest                       stable
  --latest                           stable
  --with-configurations              stable
update gcp                           stable
update gcp connection                experimental  since 0.39.0
  --dry-run                          experimental
  --name                             experimental
  --serviceAccountId                 experimental  since 0.39.0  (declared)
  --serviceaccountid                 experimental  since 0.39.0  (declared)
update gcp monitoring                experimental  since 0.39.0
  --dry-run                          experimental
  --featureSets                      experimental  since 0.39.0  (declared)
  --featuresets                      experimental  since 0.39.0  (declared)
  --locationFiltering                experimental  since 0.39.0  (declared)
  --name                             experimental
update settings                      stable
usage                                experimental  since 0.43.0
  --all-contexts                     experimental
usage reset                          experimental  since 0.43.0
  --all-contexts                     experimental
  --yes                              experimental
verify                               stable
verify analyzer                      stable
  --file                             stable
  --input                            stable
  --query                            stable
verify openpipeline-dql-processor    stable
  --config-id                        stable
  --file                             stable
verify openpipeline-matcher          stable
  --config-id                        stable
  --context                          experimental  since 0.39.0  (declared)
  --file                             stable
verify query                         stable
  --canonical                        stable
  --client-context                   stable
  --fail-on-warn                     stable
  --file                             stable
  --locale                           stable
  --set                              stable
  --timezone                         stable
version                              stable
wait                                 stable
wait query                           stable
  --backoff-multiplier               stable
  --default-sampling-ratio           stable
  --default-scan-limit-gbytes        stable
  --default-timeframe-end            stable
  --default-timeframe-start          stable
  --fetch-timeout-seconds            stable
  --file                             stable
  --for                              stable
  --initial-delay                    stable
  --locale                           stable
  --max-attempts                     stable
  --max-interval                     stable
  --max-result-bytes                 stable
  --max-result-records               stable
  --min-interval                     stable
  --no-query-limits                  stable
  --quiet                            stable
  --set                              stable
  --timeout                          stable
  --timezone                         stable
  --verbose                          experimental  since 0.39.0  (declared)
```
