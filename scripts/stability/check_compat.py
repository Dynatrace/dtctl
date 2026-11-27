#!/usr/bin/env python3
"""Reject a change that withdraws stable surface or misdates a since-version.

The checked-in manifest is gated for *freshness*: `go test ./test/stability/`
fails when `docs/STABILITY.md` disagrees with the command tree. That catches an
undeclared change, but it cannot catch a declared one -- delete a stable flag,
run `make stability-manifest`, commit the regenerated file, and the gate is
green again. The promise the manifest exists to record would then be broken by
the very commit that updated the record of it.

So this compares the manifest against its state on another ref (the PR base, or
the last release) and refuses two transitions:

  * a stable entry disappearing -- unless it was deprecated on the base, which
    is exactly the cycle a stable removal is supposed to go through;
  * a stable entry coming back weaker, which is a demotion and always needs a
    major release rather than a manifest regeneration.

Promotions, additions, and anything below stable are free: the tiers below
stable promise nothing, and this check exists to protect a promise.

It also owns the half of the since-version guard that needs the base to decide.
`experimental since 0.39.0` is a claim about which release changed the
contract. test/stability can only check that a since-version names a release
that already happened or is the next one; whether it is the *right* release
depends on what the base looked like. Three more things are refused here:

  * a since-version that is new or changed relative to the base but names a
    release that had already shipped at the base -- that release went out
    without the change, so the claim is false from the start;
  * a released since-version rewritten (to any version) or removed while its
    entry keeps its tier -- the change it records is history, and a different
    number, or none, misdates it;
  * a since-version naming a release the base had not reached that is not this
    release or the next one by the head's pkg/version -- on release-please's
    version-bump PR, that is a declaration written for a release that is now
    being numbered differently.

"Shipped at the base" means at or below the base's pkg/version.Version, which
release-please bumps only in the release commit itself.

Finally it refuses stable surface that nobody chose. A flag with no tier of its
own inherits its command's, so a flag added to an already-stable command is
stable on its first day unless someone says otherwise. The manifest tags each
flag that declared its own tier with "(declared)", and a flag that is stable in
the head, was not stable at the base, sits under a command that was stable in
both, and lacks the tag is refused. Flags that were already stable at the base
are left alone (they predate the rule), and so are the flags of a command that
is new or newly stable: there the command's own MarkStable is the decision.
The (global) group is a stable entry like any command, so a new global flag is
held to the same rule. A base manifest written before the tag existed parses
the same way, because only the head's tags are read.

Usage:
    check_compat.py [--base REF_OR_FILE] [--head FILE]
                    [--base-version X.Y.Z] [--head-version X.Y.Z]
"""

import argparse
import re
import subprocess
import sys

MANIFEST = "docs/STABILITY.md"
VERSION_FILE = "pkg/version/version.go"
VERSION_LINE = re.compile(r'^var Version = "(?P<v>[^"]+)"', re.M)
ROW = re.compile(
    r"^(?P<indent>\s*)(?P<name>\S+(?: \S+)*?)\s{2,}"
    r"(?P<level>stable|experimental|development)\b(?P<rest>.*)$"
)
SINCE = re.compile(r"\bsince (?P<v>\S+)")
# Must match declaredMarker in pkg/stability/manifest.go.
DECLARED = "(declared)"
FLAG_SEP = " --"


def surface(text):
    """Parse the manifest's Surface block into
    {entry key: (level, deprecated, since-version or None, declared)}.

    A flag is keyed by the command it hangs off, so `query --spill` vanishing is
    a different finding from `query` vanishing. `declared` is True for a flag
    line tagged "(declared)"; it is always False for a command line and for any
    line of a manifest generated before the tag existed.
    """
    _, _, body = text.partition("## Surface")
    if not body:
        sys.exit("manifest has no '## Surface' section: refusing to compare")

    entries, command = {}, None
    for line in body.split("\n"):
        m = ROW.match(line)
        if not m:
            continue
        name, level, rest = m["name"], m["level"], m["rest"]
        if not m["indent"]:
            command = name
            key = name
        elif command and name.startswith("--"):
            key = "%s %s" % (command, name)
        else:
            continue
        since = SINCE.search(rest)
        declared = bool(m["indent"]) and rest.rstrip().endswith(DECLARED)
        entries[key] = (level, "deprecated" in rest, since["v"] if since else None,
                        declared)
    if not entries:
        sys.exit("parsed no entries out of the Surface section: refusing to compare")
    return entries


def withdrawal_findings(base, head):
    """(removed, replaced, demoted) keys of stable base entries that changed.

    A stable `<command> --<flag>` that vanished is not a withdrawal when the
    head has a stable `(global) --<flag>`: the command still accepts the flag
    with the same name and a stable contract, because a command-local flag was
    replaced by the root persistent one. Those go in `replaced`, which is
    reported but does not fail the check.

    The rule is deliberately narrow: a stable global of the same name. The
    manifest records tiers, not flag types or meanings, so whether the global
    is truly equivalent is a review call, not something this check can prove.
    """
    removed, replaced, demoted = [], [], []
    for key, (level, deprecated, _, _) in sorted(base.items()):
        if level != "stable":
            continue
        if key not in head:
            if deprecated:
                continue
            command, sep, flag = key.partition(FLAG_SEP)
            glob = head.get("(global) --" + flag) if sep and command != "(global)" else None
            if glob and glob[0] == "stable":
                replaced.append(key)
            else:
                removed.append(key)
            continue
        if head[key][0] != "stable":
            demoted.append((key, head[key][0]))
    return removed, replaced, demoted


def parse_version(s):
    """X.Y.Z (leading v allowed, prerelease/build suffix dropped) -> tuple."""
    core = re.split(r"[-+]", s.strip().lstrip("v"), maxsplit=1)[0]
    parts = core.split(".")
    if len(parts) != 3 or not all(p.isdigit() for p in parts):
        raise ValueError("%r is not X.Y.Z" % s)
    return tuple(int(p) for p in parts)


def show(v):
    return "%d.%d.%d" % v


def this_or_next(v):
    """This release and the two release-please can cut next below 1.0
    (bump-minor-pre-major: a fix-only patch or a feature minor)."""
    return sorted({v, (v[0], v[1], v[2] + 1), (v[0], v[1] + 1, 0)})


def since_findings(base, head, base_version, head_version):
    """Since-versions in head that misdate the change they record.

    Returns (entry key, message) pairs; the rules are in the module docstring.
    """
    allowed = this_or_next(head_version)
    # What a new declaration may name: the next release, as the head numbers
    # it. On an ordinary PR that excludes pkg/version itself, which shipped at
    # the base; on the release PR it is the release being cut.
    unreleased_text = ", ".join(show(v) for v in allowed if v > base_version)
    out = []
    for key, (level, _, since, _) in sorted(head.items()):
        old = base.get(key)
        if (old is not None and old[0] == level and old[2] is not None
                and old[2] != since and _released(old[2], base_version)):
            # A since-version that shipped is history while the tier it dates
            # stands: moving it to any other version, released or not, or
            # dropping it, misdates the change.
            out.append((key, "since %s %s without a tier change; it already shipped"
                             % (old[2], "removed" if since is None
                                else "rewritten to %s" % since)))
            continue
        if since is None:
            continue
        try:
            v = parse_version(since)
        except ValueError:
            out.append((key, "since %s is not a version" % since))
            continue
        if v > base_version:
            # Not released at the base: whichever release carries it is this
            # one or the next, as the head numbers them.
            if v not in allowed:
                out.append((key, "since %s is not this release or the next one "
                                 "(pkg/version is %s; expected one of %s)"
                                 % (since, show(head_version), unreleased_text)))
        elif old is None or old[2] != since:
            out.append((key, "since %s is new here, but %s had already shipped "
                             "without it (expected one of %s)"
                             % (since, since, unreleased_text)))
    return out


def undeclared_findings(base, head):
    """Flags that became stable in head only by inheriting from a command that
    was already stable at the base. Returns sorted entry keys.
    """
    out = []
    for key, (level, _, _, declared) in sorted(head.items()):
        if FLAG_SEP not in key or level != "stable" or declared:
            continue
        old = base.get(key)
        if old is not None and old[0] == "stable":
            continue  # already promised at the base; predates the rule
        command = key.rsplit(FLAG_SEP, 1)[0]
        was, now = base.get(command), head.get(command)
        if was is None or was[0] != "stable" or now is None or now[0] != "stable":
            continue  # new or newly stable command: its MarkStable decided
        out.append(key)
    return out


def _released(since, base_version):
    try:
        return parse_version(since) <= base_version
    except ValueError:
        return False


def git_show(ref, path):
    out = subprocess.run(
        ["git", "show", "%s:%s" % (ref, path)],
        capture_output=True, text=True, check=False,
    )
    if out.returncode != 0:
        return None, out.stderr.strip()
    return out.stdout, None


def version_of(ref):
    """pkg/version.Version at a git ref, or in the working tree for None."""
    if ref is None:
        with open(VERSION_FILE, encoding="utf-8") as fh:
            text = fh.read()
    else:
        text, err = git_show(ref, VERSION_FILE)
        if text is None:
            sys.exit("cannot read %s at %r: %s (pass --base-version)" % (VERSION_FILE, ref, err))
    m = VERSION_LINE.search(text)
    if not m:
        sys.exit("no 'var Version = \"...\"' in %s at %s" % (VERSION_FILE, ref or "the working tree"))
    return parse_version(m["v"])


def read_file(ref):
    """The file's contents if ref names a readable file, else None."""
    if "/" in ref or ref.endswith(".md"):
        try:
            with open(ref, encoding="utf-8") as fh:
                return fh.read()
        except OSError:
            pass  # not a path after all; try it as a git ref
    return None


def read(ref):
    text = read_file(ref)
    if text is not None:
        return text
    text, err = git_show(ref, MANIFEST)
    if text is None:
        sys.exit("cannot read %s at %r: %s" % (MANIFEST, ref, err))
    return text


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="origin/main",
                    help="git ref or file to compare against (default: origin/main)")
    ap.add_argument("--head", default=MANIFEST, help="manifest to check")
    ap.add_argument("--base-version",
                    help="pkg/version.Version on the base (default: read at --base; "
                         "required when --base is a file)")
    ap.add_argument("--head-version",
                    help="pkg/version.Version on the head (default: the working tree's)")
    args = ap.parse_args()

    base, head = surface(read(args.base)), surface(read(args.head))

    if args.base_version:
        base_version = parse_version(args.base_version)
    elif read_file(args.base) is not None:
        sys.exit("--base is a file, so its pkg/version is unknown: pass --base-version")
    else:
        base_version = version_of(args.base)
    head_version = (parse_version(args.head_version) if args.head_version
                    else version_of(None))

    removed, replaced, demoted = withdrawal_findings(base, head)
    misdated = since_findings(base, head, base_version, head_version)
    undeclared = undeclared_findings(base, head)

    for key in replaced:
        print("note: %s replaced by global flag --%s (stable)"
              % (key, key.partition(FLAG_SEP)[2]))
    if not removed and not demoted and not misdated and not undeclared:
        print("OK Stable surface intact against %s (%d stable entries checked); "
              "since-versions consistent with %s; no new flag is stable by omission."
              % (args.base, sum(1 for e in base.values() if e[0] == "stable"),
                 show(head_version)))
        return 0

    sections = 0
    if removed or demoted:
        sections += 1
        print("Stable surface withdrawn relative to %s:\n" % args.base)
        for key in removed:
            print("  removed   %s" % key)
        for key, now in demoted:
            print("  demoted   %s  stable -> %s" % (key, now))
        print(
            "\nStable means additive-only, so neither is a cleanup:\n"
            "  * to remove it, deprecate it first (stability.Deprecate) and remove it\n"
            "    in a later release -- a deprecated entry may then disappear;\n"
            "  * to weaken it, that is a breaking change and needs a major release,\n"
            "    not a regenerated manifest.\n"
            "If the entry was never really stable, say so in the commit and in\n"
            "dtctl-contrib breaking-changes/ before overriding this check."
        )
    if misdated:
        if sections:
            print()
        sections += 1
        print("Since-versions that misdate their change relative to %s "
              "(base pkg/version %s, head %s):\n"
              % (args.base, show(base_version), show(head_version)))
        for key, msg in misdated:
            print("  %s: %s" % (key, msg))
        print(
            "\nA since-version records the release that changed the contract, so it\n"
            "names the release the change first ships in and never changes after.\n"
            "Fix the declaration in cmd/ and run `make stability-manifest`."
        )
    if undeclared:
        if sections:
            print()
        next_release = " or ".join(show(v) for v in this_or_next(head_version)
                                   if v > base_version)
        print("New flags that are stable only because their command is "
              "(relative to %s):\n" % args.base)
        for key in undeclared:
            print("  undeclared  %s" % key)
        print(
            "\nA flag without a tier of its own inherits its command's, so each of these\n"
            "would ship an additive-only promise that nobody chose. Declare the tier\n"
            "where the flag is defined (for a (global) flag, on the root command):\n"
            "  * stability.MarkFlag(cmd, \"<flag>\", stability.Experimental, \"<version>\")\n"
            "    if its name or behaviour may still change; <version> is the release\n"
            "    it first ships in (%s);\n"
            "  * stability.MarkFlagStable(cmd, \"<flag>\") if it is additive-only from\n"
            "    its first release on.\n"
            "Then run `make stability-manifest`; the line gains \"(declared)\"."
            % next_release
        )
    return 1


if __name__ == "__main__":
    sys.exit(main())
