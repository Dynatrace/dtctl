package stability

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/sdk/session"
)

// ManifestHeader is the preamble of the generated manifest. It states the
// promise the manifest encodes, because the file is the artifact reviewers and
// downstream consumers read.
const ManifestHeader = `# dtctl stability manifest

<!-- GENERATED FILE — do not edit. Regenerate with: make stability-manifest -->

Every command and flag dtctl exposes, with the contract it carries. This file is
generated from the live command tree and checked in, so a change to the
contract shows up as a reviewable diff and a failing build rather than as a
silent break.

| Tier | Promise |
|---|---|
| ` + "`stable`" + ` | Invocation and output contract are additive-only. Removal or an incompatible change requires a deprecation cycle. |
| ` + "`experimental`" + ` | May change or be removed in any release. Not covered by dtctl's stability guarantees. |
| ` + "`development`" + ` | Unfinished, no guarantees — of any kind, including the backend's. Not registered unless opted in via ` + "`dtctl config set development.<feature> on`" + `. |

A tier describes the whole path, not just dtctl's side of it. A ` + "`development`" + `
command commonly targets an unreleased or feature-flagged API, so it may return
404, 403 or 500 on an environment where the feature is simply not deployed —
and dtctl does **not** translate that into "not available here". Read a failure
from a development command as "this does not work yet", not as a bug report.

Stable is never implied. Every command declares its tier where it is built and
the build fails on one that does not; a command that somehow reaches runtime
undeclared resolves to ` + "`experimental`" + `, never to ` + "`stable`" + `.
The strongest promise dtctl makes is not one a command can acquire by nobody
having thought about it.

Deprecation is not a tier: a deprecated command is still stable in shape and is
merely scheduled for removal. It is recorded on the same line.

A flag line ending in ` + "`(declared)`" + ` carries a tier declaration of its own; any
other flag inherits its command's. The flags that predate this rule may keep
inheriting, but a flag that newly becomes ` + "`stable`" + ` under a command that was
already ` + "`stable`" + ` has to be declared — ` + "`stability.MarkFlagStable`" + `, or
` + "`stability.MarkFlag`" + ` with a weaker tier — and ` + "`make stability-compat`" + `
refuses one that is not. Otherwise a flag would be stable by omission, the one
tier nobody chose deliberately.

The ` + "`(global)`" + ` group at the top is not a command. It is the root command's
persistent flags — the ones every command accepts. They are listed because a
flag that appears nowhere in this file is stable by omission, which is the one
tier nobody chose deliberately. The floor applies to them on whichever command
you type, and their tier is necessarily tree-wide: cobra gives every subcommand
the same flag the root declared, so a global flag cannot be stable on one
command and experimental on another.

## Choosing what this environment accepts

The **stability floor** is the weakest contract a command or flag may offer and
still be usable. It defaults to ` + "`experimental`" + `, so interactive use keeps
getting new (badged) surface. Automation should pin ` + "`stable`" + `:

` + "```bash" + `
# Per context, persisted in the config file
dtctl config set-context prod-agent --min-stability stable

# Per process — this is what an embedding service sets, and it is deliberately
# not a command-line flag: the embedder controls argv, so a flag would hand the
# opt-in to exactly the party the floor exists to constrain.
DTCTL_MIN_STABILITY=stable dtctl get workflows
` + "```" + `

A single below-floor command or flag can be admitted without lowering the floor
for everything. Each entry is one audited risk acceptance. Naming a command
also admits the flags that are below the floor only because that command is;
a flag with a weaker contract of its own needs its own entry, because there
the flag — not the command — is the risk being accepted:

` + "```bash" + `
dtctl config set-context prod-agent --min-stability stable \
  --stability-exception 'inventory' \
  --stability-exception 'query --decode-snapshots'
` + "```" + `

Development-tier features are enabled individually and never by a floor:

` + "```bash" + `
dtctl config list-development                 # what this build carries
dtctl config set development.serve on         # persistent
DTCTL_DEVELOPMENT=serve dtctl serve http      # one process
` + "```" + `

## Finding out early what a removal will break

` + "`DTCTL_NO_DEPRECATED=1`" + ` makes dtctl behave as if every deprecated command
and flag had already been removed (commands and flags only:
output fields carry no deprecation record, see "What ` + "`stable`" + ` promises before
1.0"). A deprecation warning in a log is
easy to miss; a failing pipeline is not. Run a CI job with it set and it fails
now, while that is a fixable build, rather than on the day the removal release
lands:

` + "```bash" + `
# Set it once for a whole pipeline, not per command: the point is to find out
# which of a hundred invocations still depends on dated surface.
export DTCTL_NO_DEPRECATED=1
./scripts/nightly-report.sh
# error: this context accepts no deprecated surface: flag --params of command
#        "exec workflow", deprecated: ... use --input instead.
` + "```" + `

Orthogonal to the floor, and not a tier: a deprecated command is still stable
in shape and satisfies any floor, it merely has a removal date. No floor and no
exception will bring it back — only a migration will.

The mode is off unless asked for and can be made durable per context
(` + "`no-deprecated: true`" + `). The environment variable overrides the context
in *both* directions, so the one run that is still mid-migration can set
` + "`DTCTL_NO_DEPRECATED=0`" + ` without editing a config it shares with every
other run. Embedded callers use ` + "`engine.Request.NoDeprecated`" + `; the
variable is scrubbed from a session-backed run, like the floor is.

### Embedded and service callers

When dtctl is embedded (` + "`pkg/engine`" + `, ` + "`cmd.Session`" + `) the floor is a
per-request field, and it defaults to ` + "`stable`" + ` rather than
` + "`experimental`" + `. A request is unattended automation: nobody reads the
` + "`[Experimental]`" + ` badge on its behalf. Widen it deliberately, and prefer
naming the individual commands the host has tested over granting the whole tier:

` + "```go" + `
engine.Request{
    Command:             "inventory --agent",
    MinStability:        "",                      // stable, the safe default
    StabilityExceptions: []string{"inventory"},   // just this one, audited
}
` + "```" + `

Environment variables do not reach a session-backed run: ` + "`DTCTL_MIN_STABILITY`" + `
and ` + "`DTCTL_DEVELOPMENT`" + ` are scrubbed, because which commands exist is the
request's decision and not the host process's. See
` + "`docs/dev/SERVICE_ENGINE_DESIGN.md`" + `.

## What ` + "`stable`" + ` promises before 1.0

dtctl is pre-1.0, and ` + "`release-please-config.json`" + ` sets
` + "`bump-minor-pre-major`" + `: a breaking change ships as a minor bump, and semver's
own 0.x clause says anything may change at any time. Read literally, that would
make every badge in this file worthless on the day it shipped. So the promise is
carved out of the version policy rather than derived from it.

**The tier carries the contract, not the version number.** A ` + "`stable`" + `
command's invocation and output are additive-only from the release that declared
it, whatever the version number does next. dtctl does not get to break a
` + "`stable`" + ` command in 0.40.0 on the grounds that 0.x permits it.

**A removal needs a deprecation cycle, and the deprecation record is the
signal.** A minor bump cannot be read as "safe" here, so the warning cannot come
from the version number. It comes from the surface instead:

- The removal is announced by deprecating the command or flag, naming the
  version that deprecated it and the version it will disappear in.
- That removal version is at least **two minor releases** after the deprecating
  one, so there is always a release you can run that both warns and still works.
- ` + "`DTCTL_NO_DEPRECATED=1`" + ` turns the warning into a failing build on demand,
  and this file records every deprecation. Together they are what replaces
  "watch the major version".

` + "`stability.Lint`" + ` enforces the window, so a deprecation that skips it fails the
build rather than a review.

**Surface that a known breaking change already targets is not ` + "`stable`" + `.**
This is what keeps the promise honest without a major version available: when an
accepted breaking-change document renames or removes a flag, the flag is
declared ` + "`experimental`" + ` *now*, ahead of the break, rather than carrying a
` + "`stable`" + ` badge dtctl already knows it cannot keep. Most of the
` + "`experimental`" + ` entries below are there for that reason and no other.

**Agent-mode output is shaped for agents, not frozen.** The additive-only
promise covers what a caller *types* (commands, flags, arguments, exit codes)
and the output outside agent mode. In agent mode (` + "`--agent`" + `, ` + "`-A`" + `, or
auto-detected) the reader is a model that reads every response afresh and adapts
to it, so what the envelope carries may change in any minor release, on
` + "`stable`" + ` commands too: which fields and how many items a default returns, how
rows are encoded, how numbers are rounded. Such a change is not a breaking
change: the release notes do not flag it as one, it needs no deprecation cycle
and no breaking-change document, and it does not demote the command. Three things still hold:

- The envelope's skeleton stays additive-only. ` + "`ok`" + `, ` + "`error.code`" + `,
  ` + "`result.kind`" + ` and the ` + "`context`" + ` keys keep their names, types and meanings,
  because host code parses those, not the model.
- A change describes itself. When a default drops, clips, rounds, pages or
  re-encodes data, the envelope says so (` + "`context.format`" + `, ` + "`context.has_more`" + `,
  or a ` + "`context.suggestions`" + ` entry naming the flag that restores the full data).
- An explicit flag keeps its meaning. A program that needs a fixed shape passes
  the flags for it (for example ` + "`-o json`" + `) rather than relying on agent-mode
  defaults, and that invocation is covered by the promise above.

**Output outside agent mode is promised per command, not per field.** A
` + "`stable`" + ` command's output (JSON and YAML fields, table columns) is additive-only
as a whole, and what enforces that is the golden tests, which snapshot each
printer's output per resource and fail on any change. Content that comes from
the environment rather than from dtctl is outside that promise: the response
body ` + "`exec api`" + ` passes through, and the records a query returns, change when
the platform's APIs and data do. For those commands the promise covers the
invocation and how dtctl frames what it prints, not the payload. There is no
finer grain: an output field cannot declare a tier of its own, so there is no
experimental or deprecated field on a ` + "`stable`" + ` command, this file lists no
fields, and ` + "`DTCTL_NO_DEPRECATED`" + ` covers commands and flags but not fields.
A new field that should not carry the promise ships behind an experimental flag
or on an experimental command, because that is the finest grain a tier can be
declared at.

**` + "`experimental`" + ` and ` + "`development`" + ` are not covered.** They may
change in any release, patch releases included. That is the whole distinction.

**1.0 adds to this policy, it does not replace it.** Semver then applies on its
own terms: a removal from the stable surface needs a major bump *as well as* the
deprecation cycle. Nothing stated here is relaxed by reaching 1.0.

`

// entry is one manifest line: a command, or a flag under a command.
type entry struct {
	path        string
	flag        string // "" for a command line
	level       Level
	since       string
	feature     string // development feature key
	deprecation *Deprecation
	// declared marks a flag that carries a tier annotation of its own rather
	// than inheriting its command's. Rendered as "(declared)" so the compat
	// check can tell a chosen stable flag from one that merely inherited.
	declared bool
}

// Manifest renders the checked-in stability manifest for a command tree.
//
// Only non-default declarations and deprecations are listed in the per-tier
// detail sections; the stable surface is enumerated in full so that *removing*
// a stable command or flag is also a diff. That asymmetry is the point: the
// file exists to make breaking a promise visible.
func Manifest(root *cobra.Command) string {
	entries := collect(root)

	var b strings.Builder
	b.WriteString(ManifestHeader)

	counts := map[Level]int{}
	globals := 0
	for _, e := range entries {
		switch {
		case e.path == globalFlagPath:
			// The global group is not a command, and counting its header line
			// as one would overstate the command surface by exactly one.
			if e.flag != "" {
				globals++
			}
		case e.flag == "":
			counts[e.level]++
		}
	}
	b.WriteString("## Summary\n\n")
	b.WriteString(fmt.Sprintf("- commands: %d stable, %d experimental, %d development\n",
		counts[Stable], counts[Experimental], counts[Development]))
	b.WriteString(fmt.Sprintf("- global flags (accepted on every command): %d\n", globals))
	b.WriteString(fmt.Sprintf("- entries below (commands + flags): %d\n\n", len(entries)))

	b.WriteString("## Surface\n\n")
	b.WriteString("```\n")
	width := 0
	for _, e := range entries {
		if n := len(e.label()); n > width {
			width = n
		}
	}
	for _, e := range entries {
		b.WriteString(e.render(width))
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}

// label is the left-hand column: a command path, or an indented flag name.
func (e entry) label() string {
	if e.flag == "" {
		return e.path
	}
	return "  --" + e.flag
}

// render formats one manifest line, padded to align the tier column.
func (e entry) render(width int) string {
	line := fmt.Sprintf("%-*s  %s", width, e.label(), e.level)
	if e.since != "" {
		line += "  since " + e.since
	}
	if e.feature != "" {
		line += "  (opt-in key: " + e.feature + ")"
	}
	if e.deprecation != nil {
		d := *e.deprecation
		line += fmt.Sprintf("  deprecated %s", d.Since)
		if d.RemoveIn != "" {
			line += " → remove " + d.RemoveIn
		}
		if d.Replacement != "" {
			line += ", use `" + d.Replacement + "`"
		}
	}
	if e.declared {
		line += "  " + declaredMarker
	}
	return strings.TrimRight(line, " ")
}

// collect walks the tree and returns every command and local flag, sorted by
// command path with each command's flags immediately beneath it.
//
// The caller is responsible for handing in a *complete* tree: the generator
// enables every development feature first, so development commands appear even
// though a released build does not register them. The manifest is a
// maintainer-facing inventory checked into the repo, not a runtime discovery
// surface, so it does not participate in the non-disclosure rules that govern
// help and the catalog.
func collect(root *cobra.Command) []entry {
	var entries []entry
	Walk(root, func(cmd *cobra.Command) {
		path := Path(cmd, root)
		if path == "" {
			// The root command carries no contract of its own, but its
			// persistent flags do: --agent, --jq and the rest are
			// usable on every command, and until they were listed here they
			// were stable purely by omission — the one tier nobody chose. They
			// are grouped under a synthetic path so that the group sorts ahead
			// of the commands and reads as what it is.
			entries = append(entries, globalFlagEntries(cmd)...)
			return
		}
		if cmd.Hidden && !Declared(cmd) {
			// Internal plumbing with no declared contract. Every hidden
			// command dtctl ships does declare one now, so this is a guard
			// for a future one rather than a live exclusion -- and a hidden
			// command that *did* declare belongs in the file, because a
			// caller can still run it.
			return
		}
		e := entry{
			path:    path,
			level:   Effective(cmd),
			since:   Since(cmd),
			feature: Feature(cmd),
		}
		if d, ok := DeprecationOf(cmd); ok {
			e.deprecation = &d
		}
		entries = append(entries, e)

		visitFlags(cmd, func(f flagInfo) {
			entries = append(entries, entry{
				path: path,
				flag: f.name,
				// The flag's own promise is capped by its command's: a stable
				// flag on an experimental command is stable in name only.
				level:    session.Weakest(e.level, f.level),
				since:    f.since,
				declared: f.declared,
			})
		})
	})

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].path != entries[j].path {
			return entries[i].path < entries[j].path
		}
		// Command line first, then its flags alphabetically.
		if (entries[i].flag == "") != (entries[j].flag == "") {
			return entries[i].flag == ""
		}
		return entries[i].flag < entries[j].flag
	})
	return entries
}

// declaredMarker tags a flag line whose tier the flag declared itself.
// scripts/stability/check_compat.py matches this exact text: a new stable flag
// under an already-stable command without it is refused as stable by omission.
const declaredMarker = "(declared)"

// globalFlagPath is the synthetic command path the root command's persistent
// flags are listed under. The parentheses keep it out of the namespace of real
// commands and sort it ahead of them.
const globalFlagPath = "(global)"

// globalFlagEntries renders the root command's persistent flags — the flags
// every command accepts.
//
// A hidden persistent flag is left out. It is a parser shim, not a flag every
// command accepts: the root keeps --dry-run hidden only so cobra can parse it
// ahead of the subcommand, and a command without a dry run rejects it. Listing
// it here would promise a stable global contract dtctl no longer makes.
//
// The group itself is stable: dtctl promises that a global flag keeps working
// on every command. An individual flag may still promise less, and
// session.Weakest in collect already lets a flag be weaker than the surface it
// hangs off, so nothing special is needed to demote one.
//
// Because the group is a stable entry like any command, a new global flag is
// held to the same rule as a new flag on a stable command: it must be declared
// (MarkFlagStable or MarkFlag on the root), or the compat check refuses it. A
// global flag is the widest surface dtctl has, so it is the last place silence
// should buy a tree-wide stable promise.
func globalFlagEntries(root *cobra.Command) []entry {
	entries := []entry{{path: globalFlagPath, level: Stable}}

	visitFlags(root, func(f flagInfo) {
		if f.hidden {
			return
		}
		if f.name == "help" {
			// cobra synthesises --help lazily, so whether it exists here
			// depends on whether anything has asked for usage yet. Listing it
			// would make the manifest depend on call order.
			return
		}
		entries = append(entries, entry{
			path:     globalFlagPath,
			flag:     f.name,
			level:    f.level,
			since:    f.since,
			declared: f.declared,
		})
	})
	return entries
}
