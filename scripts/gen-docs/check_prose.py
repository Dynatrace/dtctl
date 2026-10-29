#!/usr/bin/env python3
"""Validate the hand-authored parts of docs/ against the real CLI.

`make docs-generate` + the docs-generate workflow only guard the
`<!-- GENERATED:x:start/end -->` blocks. Everything outside them -- overviews,
examples, notes, cross-links -- is prose, and prose is where the wrong flags
live: a docs migration shipped `dtctl apply --diff` (the flag is `--show-diff`),
a `DTCTL_ENVIRONMENT` variable that does not exist, and five relative links
missing their `.md`.

Three checks, all cheap:

  commands   every ``dtctl <...>`` in the docs resolves to a real command path,
             and every long flag on it is accepted by that command's own --help.
  links      every relative markdown link resolves on disk, and every `#anchor`
             matches a heading in the target file.
  redirects  every page of the retired docs/site/ is still a redirect stub, and
             still points at a repo doc that exists.

Usage:  check_prose.py <path-to-dtctl-binary> [docs-dir]
Exit 0 when clean, 1 with a per-finding report otherwise.
"""
from __future__ import annotations

import json
import os
import pathlib
import re
import shlex
import subprocess
import sys

# docs/dev/ is design documentation: it describes commands that were proposed,
# renamed, or rejected, so "the binary disagrees" is its normal state and not a
# defect. docs/site/ is the retired Jekyll site, now redirect stubs only; it has
# its own check (see check_redirects) and no prose to validate.
COMMAND_SKIP = ("dev", "site")
LINK_SKIP = ("dev", "site")

# The retired site under docs/site/_docs/ must stay redirect-only. Two pages of
# the same documentation, both hand-edited, is exactly the drift that put wrong
# flags on docs.dynatrace.com for months.
SITE_DOCS = pathlib.Path("site/_docs")
REDIRECT_PREFIX = "https://github.com/Dynatrace/dtctl/blob/main/"

# A long flag. Case-sensitive on purpose: a few flags are camelCase
# (`--serviceAccountId`, `--hostPatterns`), and a lowercase-only pattern would
# silently truncate them to a prefix that happens to be a real flag.
FLAG = r"(--[A-Za-z0-9][A-Za-z0-9-]*)"

# Commands hidden behind a registration gate are absent from a stock binary, so
# probing them would report every documented `serve`/`account` example as
# missing. The docs describe them (clearly marked experimental), so the checker
# has to see them too.
PROBE_ENV = {
    **os.environ,
    "DTCTL_EXPERIMENTAL_SERVE": "1",
    "DTCTL_EXPERIMENTAL_ACCOUNT": "1",
    # Never touch the developer's real keyring or config while probing --help.
    "DTCTL_DISABLE_KEYRING": "1",
}


# Docs legitimately show commands the CLI rejects -- "these commands have been
# removed", "this protocol is not supported, here is the error". Mark those:
#
#   <!-- prose-check:ignore -->      suppress findings on the next line
#   <!-- prose-check:ignore 6 -->    ... on the next 6 lines
IGNORE = re.compile(r"<!--\s*prose-check:ignore(?:\s+(\d+))?\s*-->")


def suppressed(path: pathlib.Path) -> set[int]:
    out: set[int] = set()
    for i, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        m = IGNORE.search(line)
        if m:
            out.update(range(i + 1, i + 1 + int(m.group(1) or 1)))
    return out


def md_files(docs: pathlib.Path, skip: tuple[str, ...]) -> list[pathlib.Path]:
    out = []
    for p in sorted(docs.rglob("*.md")):
        rel = p.relative_to(docs)
        if rel.parts and rel.parts[0] in skip:
            continue
        out.append(p)
    return out


def invocations(path: pathlib.Path):
    """Yield (line_no, command_text) for each `dtctl ...` in the file.

    Only code counts -- a fenced block, or an inline `backtick span`. Docs are
    full of sentences like "the dtctl config file lives in ~/.dtctl", and
    parsing those as invocations produces nothing but noise.

    Backslash continuations inside a fence are joined first; otherwise the
    trailing `\\` makes shlex raise and the whole (usually long, flag-heavy)
    command goes unchecked.
    """
    fence = None
    pending: tuple[int, str] | None = None
    for i, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        marker = re.match(r"\s*(```+|~~~+)", line)
        if marker:
            pending = None
            if fence is None:
                fence = marker.group(1)[:3]
            elif marker.group(1).startswith(fence):
                fence = None
            continue
        if fence and pending:
            i, line = pending[0], pending[1] + " " + line.strip()
            pending = None
        if fence and line.rstrip().endswith("\\"):
            pending = (i, line.rstrip()[:-1].rstrip())
            continue
        if fence and line.lstrip().startswith("#"):
            continue  # a shell comment, not an invocation
        spans = [line] if fence else re.findall(r"`([^`]+)`", line)
        for span in spans:
            for m in re.finditer(r"\bdtctl\s+([^\n`|]*)", span):
                # Stop at a redirect, a shell conjunction, or a comment.
                # `<` is not a redirect here: docs write `<placeholder>` far
                # more often than stdin, and splitting on it truncates the
                # command before the flags that follow the placeholder.
                text = re.split(r"\s+>{1,2}\s|\s+&&\s+|;|\s+#",
                                m.group(1).strip())[0].strip()
                if text:
                    yield i, text


class Help:
    """One command's --help, parsed into the three things the checks need."""

    def __init__(self, ok: bool, text: str):
        self.ok = ok
        self.text = text
        self.flags = set(re.findall(FLAG, text))
        # "Available Commands:" up to the next blank-line-separated section.
        self.subcommands: set[str] = set()
        block = re.search(r"^Available Commands:\n(.*?)(?:\n\n|\Z)", text,
                          re.S | re.M)
        if block:
            self.subcommands = set(re.findall(r"^\s{2,}(\S+)", block.group(1),
                                              re.M))
        # Does any Usage line accept a positional? `[flags]` and `[command]`
        # are cobra's own placeholders and do not count as one.
        usage = re.search(r"^Usage:\n(.*?)(?:\n\n|\Z)", text, re.S | re.M)
        body = (usage.group(1) if usage else "")
        body = body.replace("[flags]", "").replace("[command]", "")
        self.takes_positional = bool(re.search(r"[<\[]", body))


class Probe:
    """Caches `dtctl <path...> --help` so each command path is run at most once."""

    def __init__(self, binary: str):
        self.binary = binary
        self.cache: dict[tuple[str, ...], Help] = {}

    def __call__(self, parts: list[str]) -> Help:
        key = tuple(parts)
        if key not in self.cache:
            proc = subprocess.run([self.binary, *parts, "--help"],
                                  capture_output=True, text=True, timeout=60,
                                  env=PROBE_ENV)
            text = proc.stdout + proc.stderr
            ok = proc.returncode == 0 and "Error:" not in text
            self.cache[key] = Help(ok, text)
        return self.cache[key]

    def resolve(self, positional: list[str]) -> tuple[list[str], list[str], Help | None]:
        """Split a token run into (command path, leftover arguments, help).

        cobra answers `--help` with rc=0 for an unknown subcommand -- it just
        prints the parent's help -- so trusting the exit code alone would
        silently accept `get gcp feature-sets`, where the real subcommand is
        `monitoring-feature-sets`. Comparing the help text against the parent's
        catches that while still admitting aliases (`get document`), which are
        real commands but never appear under "Available Commands".
        """
        if not positional:
            return [], positional, None
        path: list[str] = []
        help_ = self([])
        if not help_.ok:
            return [], positional, None
        for token in positional:
            if token not in help_.subcommands:
                child = self([*path, token])
                if not child.ok or child.text == help_.text:
                    break
            path.append(token)
            help_ = self(path)
        if not path:
            return [], positional, None
        return path, positional[len(path):], help_


def verbs(binary: str) -> set[str]:
    """The CLI's own verb list, used to tell a command from a sentence.

    Docs open plenty of sentences with the product name -- "dtctl lets you list
    SLOs" -- and those are not invocations. Requiring a real verb in first
    position separates the two without a hand-maintained stop-word list.
    """
    proc = subprocess.run([binary, "commands", "--full", "-o", "json"],
                          capture_output=True, text=True, timeout=120,
                          env=PROBE_ENV)
    proc.check_returncode()
    return set(json.loads(proc.stdout)["verbs"])


def check_commands(docs: pathlib.Path, probe: Probe, known_verbs: set[str]) -> list[str]:
    findings = []
    global_flags = probe([]).flags
    for path in md_files(docs, COMMAND_SKIP):
        skip_lines = suppressed(path)
        for line_no, text in invocations(path):
            if line_no in skip_lines:
                continue
            try:
                tokens = [t for t in shlex.split(text) if t]
            except ValueError:
                continue  # unbalanced quotes in an illustrative snippet
            if not tokens or tokens[0] not in known_verbs:
                continue  # prose that merely mentions the binary
            # The command path is the leading run of literal positionals. A
            # `<placeholder>` or `[optional]` ends it: we cannot resolve it,
            # but the flags after it are still worth checking.
            positional = []
            for t in tokens:
                if t.startswith(("-", "<", "[")):
                    break
                positional.append(t)

            resolved, leftover, help_ = probe.resolve(positional)
            if help_ is None:
                findings.append(
                    f"{path}:{line_no}: no such command: "
                    f"`dtctl {' '.join(positional)}`")
                continue
            # A command that lists subcommands and declares no positional in
            # its Usage cannot take a bare word -- that word is a misspelt
            # subcommand, not an ID.
            if leftover and help_.subcommands and not help_.takes_positional:
                findings.append(
                    f"{path}:{line_no}: `dtctl {' '.join(resolved)}` has no "
                    f"subcommand {leftover[0]!r} "
                    f"(have: {', '.join(sorted(help_.subcommands))})")
                continue

            known = help_.flags | global_flags
            # Scan token by token rather than the raw line, and skip any token
            # that still carries whitespace: shlex has already stripped its
            # quotes, so a `--flag` inside it is part of an argument *value* --
            # `--stability-exception 'query --decode-snapshots'`, a DQL string,
            # a JSON body -- and not a flag this command is being passed.
            # Checking it against the outer command's help is a false positive.
            # The `=` split keeps `--input='{"a": 1}'` checkable on its name.
            for token in tokens:
                name = token.partition("=")[0]
                if any(c.isspace() for c in name):
                    continue
                for flag in re.findall(rf"(?<![\w-]){FLAG}", name):
                    if flag not in known:
                        findings.append(
                            f"{path}:{line_no}: `dtctl {' '.join(resolved)}` "
                            f"has no flag {flag}")
    return findings


def anchors(path: pathlib.Path) -> set[str]:
    body = path.read_text(encoding="utf-8")
    found = set()
    for heading in re.findall(r"^#{1,6}\s+(.+?)\s*$", body, re.M):
        slug = re.sub(r"[^\w\s-]", "", heading.lower()).strip().replace(" ", "-")
        found.add(slug)
    found |= set(re.findall(r'<a\s+(?:id|name)="([^"]+)"', body))
    return found


def check_links(docs: pathlib.Path) -> list[str]:
    findings = []
    for path in md_files(docs, LINK_SKIP):
        skip_lines = suppressed(path)
        # Blank out fenced code rather than delete it, so line numbers hold.
        body = re.sub(r"```.*?```",
                      lambda m: "\n" * m.group(0).count("\n"),
                      path.read_text(encoding="utf-8"), flags=re.S)
        for line_no, line in enumerate(body.splitlines(), 1):
            if line_no in skip_lines:
                continue
            for m in re.finditer(r"\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)", line):
                target = m.group(1)
                if target.startswith(("http://", "https://", "mailto:", "#", "{{")):
                    continue
                fragment = None
                if "#" in target:
                    target, fragment = target.split("#", 1)
                if not target:
                    continue
                dest = path.parent / target
                if not dest.exists():
                    findings.append(f"{path}:{line_no}: broken link: {m.group(1)}")
                elif fragment and dest.suffix == ".md":
                    if fragment.lower() not in anchors(dest):
                        findings.append(
                            f"{path}:{line_no}: no such anchor: {m.group(1)}")
    return findings


def check_redirects(docs: pathlib.Path, repo: pathlib.Path) -> list[str]:
    """Every page of the retired site is a redirect stub to a file that exists.

    This is what keeps the site from growing a second copy of the docs again,
    and it turns a renamed doc into a CI failure instead of a dead redirect
    that nobody notices until a user reports it.
    """
    findings = []
    site = docs / SITE_DOCS
    if not site.is_dir():
        return findings
    for path in sorted(site.glob("*.md")):
        text = path.read_text(encoding="utf-8")
        fm = re.match(r"\A---\n(.*?)\n---\n*\Z", text, re.S)
        if not fm:
            findings.append(
                f"{path}: not a redirect stub -- the site is retired, so this "
                f"page must contain only front matter with a redirect_to "
                f"(put the content in docs/ instead)")
            continue
        target = re.search(r"^redirect_to:\s*(\S+)\s*$", fm.group(1), re.M)
        if not target:
            findings.append(f"{path}: redirect stub has no redirect_to")
            continue
        url = target.group(1)
        if not url.startswith(REDIRECT_PREFIX):
            findings.append(f"{path}: redirect_to is not a repo doc: {url}")
            continue
        rel = url[len(REDIRECT_PREFIX):].split("#")[0]
        if not (repo / rel).exists():
            findings.append(
                f"{path}: redirect_to points at a file that does not "
                f"exist: {rel}")
    return findings


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    binary = sys.argv[1]
    docs = pathlib.Path(sys.argv[2] if len(sys.argv) > 2 else "docs")
    if not docs.is_dir():
        print(f"no such docs directory: {docs}")
        return 2

    findings = (check_commands(docs, Probe(binary), verbs(binary))
                + check_links(docs)
                + check_redirects(docs, docs.parent))
    if findings:
        print()
        print("❌ Documentation prose disagrees with the CLI.")
        print()
        for f in findings:
            print(f"   {f}")
        print()
        print("📌 These are in hand-authored prose, so `make docs-generate` will")
        print("   not fix them -- correct the text, or the command if the docs")
        print("   describe the behaviour you actually want.")
        print()
        print(f"{len(findings)} problem(s) found.")
        return 1

    print("✅ Documentation prose matches the CLI (commands, flags, links, "
          "anchors, site redirects).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
