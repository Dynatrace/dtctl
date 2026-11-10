# Command Profiles Design

**Status:** Implemented
**Created:** 2026-07-10
**Author:** dtctl team

> **Implementation:** profile type, resolution, presets, and matching live in
> `pkg/config/profile.go`; the tree-walk filter, `ProfileError`, and pre-dispatch
> resolution live in `cmd/profile.go` (wired into `execute()` in `cmd/root.go`).
> Context binding is in `cmd/ctx.go` / `cmd/config.go`; catalog metadata in
> `pkg/commands/listing.go` + `cmd/commands.go`. User docs:
> `docs/CONFIGURATION.md` (Command Profiles). Telemetry (open question 1) is deferred.

## Overview

Command profiles let an operator restrict *which commands dtctl exposes* to a
named subset. A profile is a **default-deny allowlist** of commands. It shapes
every discovery surface at once -- `--help`, the `dtctl commands` / `commands howto`
catalog, and shell completion -- and hard-blocks invocation of any command outside
the set.

The motivating use case is agentic products. dtctl is frequently embedded in AI
agents where only a slice of the CLI is relevant: an investigation agent may need
only `query` and `analyzers`, and commands like `auth login`, `config`, or the
cloud-provisioning verbs are noise at best and a footgun at worst. Agents degrade
when `dtctl commands` or `dtctl --help` return a large, mostly-irrelevant menu.

Today the only way to achieve this is to fork dtctl and strip commands. Profiles
make the subset a **configuration concern**, not a build concern -- one binary,
many surfaces.

## Goals

1. **Reduce agent confusion** -- Present only the commands relevant to the task.
2. **No fork** -- A single binary serves every surface; the subset is config-driven.
3. **One switch, all surfaces** -- Help, `commands`, `howto`, and completion all
   reflect the active profile with no per-surface code.
4. **Hard enforcement** -- Out-of-profile commands are not merely hidden; invoking
   one fails with a clear, signposted error.
5. **Zero-flag activation** -- An embedding product can pin a profile via context
   or environment so agents inherit it without passing anything.
6. **Stay orthogonal to safety levels** -- Profiles govern the *surface* axis; safety
   levels govern the *permission* axis. The two are separate knobs, composed on a
   context. Neither reimplements the other.
7. **Default-deny** -- A profile lists what is *allowed*; everything else is masked.
   Adding a new command to dtctl never silently widens an existing profile's surface.

## Non-Goals

- **A security boundary.** Like [safety levels](context-safety-levels.md), profiles
  are a client-side convenience, not a security control. A determined caller can
  unset `DTCTL_PROFILE` or edit config. For real restriction, scope the API token.
- **Per-flag filtering.** Profiles operate at command granularity, not flag
  granularity. Hiding individual flags of a visible command is out of scope.
- **Remote profiles.** dtctl never fetches a profile definition. A CLI profile
  is defined locally in config; an embedding host may hand the engine a
  definition in-process (see [Host-defined profiles](#host-defined-profiles-in-the-engine)),
  which is still local to the process that runs dtctl.
- **Read/write filtering.** A profile does not know or care whether a command
  mutates. "No writes" is a *safety level*, set independently on the context. A
  profile never enumerates mutating commands.
- **Denylists.** There is no `exclude`. Restricting the surface is always expressed
  as an allowlist (see [Design decisions](#design-decisions-why-so-small)).
- **Profile inheritance.** No `extends`. Profiles are flat, standalone lists.

---

## Concepts: Profiles vs. Safety Levels

dtctl already has [context safety levels](context-safety-levels.md). Profiles are a
**different, orthogonal axis**. Keeping them distinct is the central design
constraint of this document.

| | **Safety level** | **Profile** |
|---|---|---|
| Question | "What may this command *do*?" | "Which commands *exist* here?" |
| Axis | Permission / blast radius | Topic / surface / relevance |
| Ordered? | Yes: `readonly` < `readwrite-mine` < `readwrite-all` < `dangerously-unrestricted` | No -- arbitrary sets |
| Granularity | Per-*operation*, ownership-aware | Per-*command* identity |
| When enforced | Runtime, per operation | Startup, shapes the command tree |
| Effect on help/catalog | **None** -- a `readonly` context still lists `delete workflows`; it blocks at run time | Removes the command from help / `commands` / completion entirely |

The two are **filters in series**: a profile decides what is on the menu, then the
safety level polices what the chosen command is allowed to do. You compose them by
setting *both* on the same context -- e.g. a `prod-agent` context bound to the
`query` profile **and** the `readonly` safety level, which expresses "this agent
only sees `query`/`analyzers` **and** can never mutate."

Crucially, a profile has **no `safety-level` field**. It does not pin, default, or
otherwise touch the permission axis. This keeps the two concepts fully independent
and avoids the coupling that would otherwise blur them.

### The overlap trap and how we avoid it

The only place these axes could collide is the read/write dimension. If a profile
called `readonly` worked by *hiding every mutating command*, we would have two
"readonly" knobs with different semantics -- the profile version being a weaker,
non-ownership-aware reimplementation of the safety level. Users would then hit
confusing contradictions:

- "I set `safety=readonly`, why is `delete` still in `--help`?" (expecting profile
  behavior from safety)
- "I set `profile=query`, why does safety still complain about ownership?"
  (expecting safety behavior from a profile)

**Rules that keep the axes clean:**

1. **Profiles never encode read/write intent.** "No writes" is a safety level, set
   separately on the context. A profile is only ever a topical command allowlist.
2. **Profile names are topical, never permission words.** Ship `query`,
   `investigate`, `full` -- never a profile named `readonly`. The word `readonly`
   belongs to safety levels.
3. **Error messages are distinct and self-explaining.** A profile block says the
   command *does not exist here*; a safety block says the command is *not allowed
   to act*.

One sentence carries the whole mental model:

> A profile decides which commands are on the menu; the safety level decides what
> those commands are allowed to do.

---

## User Experience

### Defining profiles

Profiles live in the config file under a top-level `profiles` map. A profile is
just a `description` and a flat `commands` allowlist over the command tree.

```yaml
profiles:
  query:
    description: DQL queries plus Davis analyzers for investigation agents
    commands: [query, get analyzers, describe analyzer, exec analyzer, verify analyzer]

  investigate:
    description: "Read-only incident triage: query, logs, and resource discovery"
    commands: [query, logs, get, find, describe]
```

> Note on analyzer commands: Davis analyzers are reached through several verbs
> (`get analyzers`, `describe analyzer`, `exec analyzer`, `verify analyzer`)
> rather than a single top-level `analyzers` verb, so the `query` preset lists
> each. There is no `problems` resource today; incident triage uses `query`
> against Grail plus `get`/`logs`.

Selection semantics:

- **`commands`** -- a list of command-path *prefixes*: verbs (`query`), resources
  (`get workflows`), or full paths (`get workflows definition`). An entry matches a
  command if it equals or is a prefix of that command's path, so listing a parent
  (`describe`) includes its whole subtree. No globs -- prefix matching already
  covers subtrees.
- **Default-deny** -- any command not matched by `commands` (and not in the
  always-available set below) is masked. There is no `exclude`.

To restrict the surface, you list what the profile *allows*. There is deliberately
no way to say "everything except X" -- see [Design decisions](#design-decisions-why-so-small).

### Always-available commands

Only an irreducible core is allowed regardless of profile -- the machine-readable
catalog an agent bootstraps from and the universal help escape hatch:

- `commands`, `commands howto` -- agents must always be able to read the catalog.
- `help`.

Both are pure discovery: they run no operation, read no data, and mutate nothing.
They are merged into every profile's allowlist so a minimal profile never hides
the very catalog an agent needs to discover its surface.

**Everything else is subject to the allowlist -- including `config`, `ctx`,
`completion`, and `version`.** This is deliberate. Earlier drafts kept
`config`/`ctx` always-available for "context management", but those verbs can
rotate credentials (`config set-credentials`) and switch environments
(`ctx set`), which a locked-down agent profile must be able to withhold. In an
embedding, the product provisions the context out-of-band (it writes the config);
the agent has no reason to touch `config`. So the floor is the two discovery
commands only, and a profile that genuinely wants `config`/`completion`/etc.
lists it explicitly (`commands: [query, config]`). Keeping the floor this small
also means "not listing it" is the disable mechanism -- no separate override knob
is needed (see [Open Questions](#open-questions)).

### Selecting the active profile

Precedence (highest wins):

```
DTCTL_PROFILE env  >  context-bound profile  >  none (= full)
```

- **`DTCTL_PROFILE`** -- for products that wrap the binary in a controlled
  environment.
- **Context binding** -- a `profile:` field on a context; shipped products pin it so
  agents inherit the right surface with zero flags. This is the recommended path
  for embedding. Compose with a `safety-level` on the same context when you also
  want to constrain what those commands may do.
- **None** -- the full command tree (today's behavior; fully backward compatible).

There is no global `current-profile` default: a config-wide profile is a footgun
(set once, forgotten, every invocation silently restricted). Activation is always
either explicit per-environment (`DTCTL_PROFILE`) or scoped to a context.

> A hidden `--profile` flag is intentionally **not** part of the documented surface.
> It may exist as an internal escape hatch for debugging a locked-down context, but
> it is deliberately not advertised so agents are not tempted to widen their own
> surface.

### Managing profiles

There is no `dtctl profile` command group in v1. Profiles are pure configuration:
you define them in the config file and bind them via `config set-context` or the
environment. To inspect the *resolved* surface of the active profile, run the
catalog command that agents already use:

```bash
# Bind a profile to a context
dtctl config set-context prod-agent --profile=query

# Inspect the resolved surface (already profile-aware)
DTCTL_PROFILE=query dtctl commands
```

`dtctl commands` reflects the active profile, so it *is* the "show" command -- no
separate tooling needed.

### What a blocked invocation looks like

```
$ DTCTL_PROFILE=query dtctl auth login
Error: command "auth login" is not available in profile "query"

  This profile exposes a reduced command set. Run 'dtctl commands' to see
  what is available, or unset DTCTL_PROFILE to use the full CLI.
```

Compare with a safety block (unchanged), which is about *permission*, not surface:

```
Error: context 'prod' (readonly) does not allow delete operations
```

### Built-in presets

Shipped so products need not hand-author YAML. All names are **topical**. If a
preset should also forbid writes, that is done by pairing it with a `readonly`
safety level on the context -- the preset itself never encodes permission.

| Preset | Surface (plus always-available) |
|--------|---------------------------------|
| `full` | Everything (default; today's behavior) |
| `query` | `query`, `get analyzers`, `describe analyzer`, `exec analyzer`, `verify analyzer` |
| `investigate` | `query`, `logs`, `get`, `find`, `describe` |

User-defined profiles are standalone; there is no inheritance from presets. If you
want a variant, copy the list -- profiles are short by design.

---

## Design

### Where the filter runs

dtctl already post-processes the fully-registered command tree inside `execute()`
(`cmd/root.go`) -- `setupErrorHandlers(rootCmd)` and `installScopePreflight(rootCmd)`
both run after every `AddCommand`. The profile filter slots into the same phase:

```go
func execute() int {
	setupErrorHandlers(rootCmd)
	installScopePreflight(rootCmd)
	applyProfile(rootCmd, resolveProfile(cfg))   // <- new pass
	// ... alias resolution, tracing, rootCmd.Execute()
}
```

`resolveProfile(cfg)` walks the precedence chain (env -> context -> none) and
returns the named profile (or nil for `full`).

### The filter mechanism: `Hidden` + a `RunE` guard

The key enabling fact: **every discovery surface we care about already honors
`cmd.Hidden`.**

- Cobra's help and shell completion skip hidden commands natively.
- `pkg/commands/listing.go` (which backs `dtctl commands` and `commands howto`)
  already skips hidden commands -- e.g. `listing.go:196`
  (`if hiddenCommands[name] || cmd.Hidden { continue }`) and the analogous checks
  for nested subcommands and flags.

So `applyProfile` needs to do only two things per out-of-profile command:

1. **Set `cmd.Hidden = true`** -- removes it from help, `commands`, `howto`, and
   completion for free, across all three surfaces, with no per-surface code.
2. **Wrap `RunE` with a guard** -- to satisfy the hard-enforcement goal, since a
   hidden command is still *runnable* by name in cobra.

```go
func applyProfile(root *cobra.Command, p *Profile) {
	if p == nil { // "full"
		return
	}
	walk(root, func(cmd *cobra.Command) {
		if p.Allows(commandPath(cmd)) {   // allowlist + always-available set
			return
		}
		cmd.Hidden = true
		if cmd.RunE != nil || cmd.Run != nil {
			cmd.RunE = blockedRunE(cmd, p.Name)   // returns the "not available" error
			cmd.Run = nil
		}
	})
}
```

Note there is no safety handling here -- `applyProfile` touches only the surface.
The safety level is resolved independently from the effective context, exactly as
it is today.

Because the guard wraps `RunE` rather than removing the command, cobra still parses
the command and its args -- which lets us return a *specific* "not available in
profile X" message instead of a generic "unknown command". This is why we mask
rather than `RemoveCommand`.

### Parent/child masking rules

- Masking a **parent** (e.g. `create`) masks the whole subtree; the guard on the
  parent covers `create workflow`, `create azure ...`, etc.
- A profile can **allow a parent but only some children** by listing the specific
  child paths (`commands: [get analyzers, get slos]` rather than `commands: [get]`).
  The walk evaluates each node against the allowlist, so a masked child under a
  visible parent is fine (cobra already renders parents with a partially hidden
  child set).
- The **always-available** set is merged into the allowlist, so those commands
  survive regardless of how narrow the profile is.

### A second mask on the same mechanics: `BlockedCommands`

Embedded callers apply a second, orthogonal mask built from `Hidden` + a `RunE`
guard exactly as above: `RunOptions.BlockedCommands` (`applyBlockedCommands`,
`cmd/blocked.go`). The two answer different questions — a profile answers "which
commands exist for this caller" and blocks with `profile_blocked`, while
`BlockedCommands` answers "which commands make sense in this environment at all"
and blocks with `unsupported_in_service`. They **compose**: both masks apply to a
run, and the per-invocation tree restore undoes both before the next one.

One deliberate difference: the blocked mask also guards **non-runnable parents**
(and neutralizes their arg/flag parsing), so a masked bare `config` cannot answer
with its own help text and exit 0. The profile mask guards only runnable nodes.
See [SERVICE_ENGINE_DESIGN.md](SERVICE_ENGINE_DESIGN.md).

### Host-defined profiles in the engine

A service embedding dtctl (`pkg/engine`) often owns its allowlist: it is part
of the service's own contract, reviewed and versioned with the service, and a
preset in dtctl would have to change in lockstep with it. So besides selecting
a preset by name (`Request.Profile`), a request can carry the definition
itself:

```go
engine.Execute(ctx, engine.Request{
	// ...
	ProfileDefinition: &engine.ProfileDefinition{
		Name:     "my-service",
		Commands: []string{"query", "get analyzers", "describe analyzer"},
	},
})
```

The definition goes onto the session (`cmd.Session.Profile`), whose synthetic
config carries it in `profiles:` and binds the session context to it. From
there it is an ordinary config-file profile: the same `Allows`, the same mask,
the same `profile_blocked` error, the same catalog metadata. The host keeps the
list; dtctl keeps the semantics — alias resolution, parent reachability, the
always-available floor, and composition with the stability floor and
`BlockedCommands`.

Rules that keep the definition from meaning something other than what it says:

- `Profile` and `ProfileDefinition` are mutually exclusive.
- The session pins `DTCTL_PROFILE` to the definition's name for the run, so
  nothing in the process environment selects a preset — or `full` — in its
  place. An `Env` entry naming a different profile is an error, not silently
  overridden: it means the host believes some other profile applies. Because
  the host pinned the profile, a `profile_blocked` error does not suggest
  unsetting `DTCTL_PROFILE`.
- The name is required, has no whitespace, and is not `full`.
- A blank entry is refused, and so is an entry that starts with `dtctl` or
  contains a flag token (`query --dql`): entries are command paths relative to
  the root, and allow every flag of the command they name. An empty list is
  valid and leaves only the always-available commands.
- The definition is copied once when the request is accepted; what is
  validated is what runs.
- Entries are canonical paths. An alias a caller types (`get az`) still runs,
  because cobra resolves it to `get analyzers` before the profile is consulted.
  An alias or a misspelling *in the list* selects no command of its own, but
  still allows its ancestors (`get`).
- An entry allows every ancestor, and an ancestor that runs on its own runs its
  own body: `apply extension-config` also allows `apply -f <any file>`. For a
  group that only lists subcommands (`get`) that is harmless; list an entry
  under a runnable parent only if that parent may run too.

dtctl does not check that each entry names an existing command: an entry for a
command the stability floor hides, or one added in a later release, is not an
error. A host that wants an upgrade to fail when a listed command is renamed or
removed checks that in its own tests: `engine.Parse` on `<entry> --help` for
every entry, with the request's floor, and compare `Verb`/`Resource` with the
entry. An unknown verb is a `*UsageError`, but an unknown resource is not — it
falls back to the verb's help, with `Help` set and `Resource` empty.

### Config schema changes

`pkg/config/config.go`:

```go
type Config struct {
	// ... existing fields ...
	Profiles map[string]Profile `yaml:"profiles,omitempty"`
}

type Profile struct {
	Description string   `yaml:"description,omitempty"`
	Commands    []string `yaml:"commands,omitempty"` // allowlist of path prefixes
}

// NamedContext gains a profile binding:
type Context struct {
	// ... existing fields ...
	Profile string `yaml:"profile,omitempty"`
}
```

### Selector matching

`Allows` matches a command by its space-joined path (`get workflows`):

1. If the path is in the always-available set -> **allowed**.
2. Else if any `commands` entry equals or is a prefix of the path -> **allowed**.
3. Else -> **masked**.

Prefix matching is segment-aware: `get` matches `get workflows` but a bare `get`
entry does not accidentally match an unrelated `getterm`-style command. Reusing the
existing catalog vocabulary matters: `pkg/commands` already models verbs and
resources (`FilterByResource`, `RequiredScopesForResource`), so matching can share
that command-path model rather than inventing a new one.

### Interaction with `dtctl commands` metadata

The catalog and the `--agent` envelope should advertise both active constraints so
an agent (and a human debugging) can see the two independent axes at once:

```json
{
  "profile": "query-only",
  "safety_level": "readonly",
  "verbs": { "...": "..." }
}
```

This is emitted from the same `commands.Build(rootCmd)` path; the profile name comes
from the resolved profile, the safety level from the effective context.

---

## Design decisions (why so small)

This design was deliberately trimmed. The rationale for each cut:

- **Allowlist, no denylist (`exclude`).** A denylist is default-*allow*: adding a
  new command to dtctl would silently appear in every agent's surface -- the exact
  "too many options" problem re-emerging. An allowlist is default-*deny*: new
  commands stay masked until explicitly added. That is both simpler (no
  include/exclude precedence ladder) and the correct posture for embedding.
- **No `safety-level` on the profile.** Coupling the surface axis to the permission
  axis reintroduces the very confusion the concepts section works to avoid, and
  spawns a precedence question. Keeping them separate -- composed on the context --
  makes "profiles are purely the surface axis" literally true.
- **No `extends` / inheritance.** Inheritance brings cycle detection and flattening
  for a config that is realistically a handful of short profiles. Repeating a list
  is cheaper than the machinery.
- **No global `current-profile`.** A config-wide default profile is a footgun. The
  two real activation paths -- context binding and `DTCTL_PROFILE` -- cover the
  cases without a silent global switch.
- **No `dtctl profile` command group.** `dtctl commands` is already profile-aware,
  so it serves as "show"; with no `current-profile` there is nothing for `use` to
  set. Profiles stay pure config in v1.
- **No globs.** Segment-aware prefix matching already includes subtrees (`describe`
  covers `describe analyzers`), so a glob engine buys nothing.

If real demand appears later (e.g. deep profile reuse), inheritance or a management
command group can be added without breaking the schema above.

## Security & Safety Notes

- **Client-side only.** Profiles are convenience/UX, not a security boundary --
  identical caveat to safety levels. The authoritative restriction is the API token
  scope. This must be stated wherever profiles are documented.
- **Not a substitute for `--check-scopes`.** Profiles limit surface, not
  entitlements. A `query` profile with a broadly-scoped token can still run every
  query the token permits.
- **Unknown profile name** (env or context references a profile that does not
  exist) should fail fast with a clear error rather than silently falling back to
  `full`, which would be a surprising surface expansion.

## Backward Compatibility

- No `profiles` and no context `profile` -> resolves to `full` -> identical to
  today. Fully backward compatible.
- The change is purely additive to the config schema.

## Open Questions

1. **Telemetry.** Should the active profile be attached to the root OTel span (like
   command name is today) for usage analysis? Likely yes, low cost.
2. ~~**Should the always-available set be user-overridable?**~~ **Resolved:** no
   override knob. The always-available floor was shrunk to just `commands` and
   `help` (see [Always-available commands](#always-available-commands)), so
   `config`, `ctx`, `completion`, and `version` are now maskable by simply not
   listing them. "Not listing it" is the disable mechanism; a configurable
   always-available set would be redundant surface.

## Implementation Sketch (phased)

1. **Config schema + resolution** -- `Profile` type, `resolveProfile` (env ->
   context -> none), validation (unknown profile name). Unit-tested in isolation.
2. **`applyProfile` pass** -- the tree walk, `Hidden` + `RunE` guard, always-available
   merge, segment-aware prefix matching. Verified against help, `commands`, and
   completion output.
3. **Built-in presets** -- `full`/`query`/`investigate`, merged under user config.
4. **Context binding** -- `profile:` field on `Context` + `config set-context --profile`.
5. **Catalog metadata + `--agent` envelope** -- surface `profile` + `safety_level`.
6. **Docs** -- user-facing page under `docs/site/_docs/`, cross-linked with
   `context-safety-levels.md` and `ai-agent-mode.md`.
   (As shipped: the page is the "Command Profiles" section of
   [../CONFIGURATION.md](../CONFIGURATION.md#command-profiles) -- `docs/site/` is
   retired and holds only redirect stubs.)
