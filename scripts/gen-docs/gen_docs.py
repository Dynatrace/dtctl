#!/usr/bin/env python3
"""
gen_docs.py - deterministic documentation generator prototype for dtctl.

Feasibility proof: can per-resource reference tables, COMMANDS.md, and
TOKEN_SCOPES.md be GENERATED from dtctl's own `commands --full -o json`
metadata, so SMEs only hand-write prose (overview, output shape, examples)?

Input:  a JSON file produced by `dtctl commands --full -o json`
Output (written under --out-dir, default ./out):
  - <resource>.md   one per-resource reference page (default: workflows only,
                    pass --resource to do others; the generator is generic)
  - COMMANDS.md     full verb x resource operation matrix
  - TOKEN_SCOPES.md per-safety-level scope tables from `resource_scopes`

No third-party dependencies - standard library only. Deterministic: same
input JSON always produces byte-identical output (all iteration is over
explicitly sorted keys).

KNOWN GAPS (see also the prototype's final report to the human):
  - The catalog has NO per-resource-per-verb flags. Flags only exist at the
    VERB level (e.g. `apply`, `diff`, `inspect`, `inventory`, `query`, and
    the `exec copilot` subcommand) and apply identically to every resource
    that verb supports. There is no flag list for e.g. "get workflows"
    specifically - only dtctl's global flags apply there.
  - `required_args` is populated for almost no verbs (only `apply: [file]`
    in this catalog snapshot), so positional-argument requirements (e.g.
    "describe workflow <NAME>" needs a name) are NOT machine-derivable.
  - Resource-name casing is INCONSISTENT across verbs: `get` enumerates
    resources in the plural ("workflows"), nearly every other verb uses the
    singular ("workflow"), and a few (e.g. "settings"/"setting") disagree
    inconsistently. This script normalizes via `resource_scopes` (whose
    keys are the canonical singular form) plus a naive de-pluralizer, but
    that is a heuristic, not a documented contract.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

DEFAULT_RESOURCE = "workflows"


def load_catalog(path: Path) -> dict:
    with path.open("r", encoding="utf-8") as f:
        return json.load(f)


# A `resource_scopes` entry maps each access level to its scope list, plus an
# optional `alternatives` key: access level -> list of scope sets the endpoint
# accepts in place of that level's list (any one set suffices).
ALTERNATIVES_KEY = "alternatives"


def scope_levels(entry: dict) -> list[tuple[str, list[str]]]:
    """The (access level, scopes) pairs of a resource_scopes entry, sorted,
    without the `alternatives` key."""
    return [(level, scopes) for level, scopes in sorted(entry.items())
            if level != ALTERNATIVES_KEY]


def format_alternatives(alternatives: list[list[str]]) -> str:
    """Render alternative scope sets for a markdown table cell."""
    sets = [" + ".join(f"`{s}`" for s in alt) for alt in alternatives]
    return "or instead any one of: " + "; ".join(sets)


def scope_cell(scopes: list[str], alternatives: list[list[str]] | None) -> str:
    cell = ", ".join(f"`{s}`" for s in scopes)
    if alternatives:
        cell += " (" + format_alternatives(alternatives) + ")"
    return cell


def canonical_resource(raw: str, known: set[str]) -> str:
    """Map a verb's resource string (singular or plural, varies by verb) to
    the canonical singular id used as a key in `resource_scopes`.

    Heuristic only - see module docstring GAPS. Falls back to the raw string
    unchanged if no match is found (so unknown resources still show up
    somewhere rather than silently vanishing).
    """
    if raw in known:
        return raw
    if raw.endswith("s") and raw[:-1] in known:
        return raw[:-1]
    if raw.endswith("es") and raw[:-2] in known:
        return raw[:-2]
    return raw


def build_resource_index(catalog: dict) -> dict[str, list[dict]]:
    """Return canonical_resource -> list of operation records.

    Each operation record:
      {
        "verb": str,
        "resource_as_written": str,   # exact string dtctl's own catalog used
        "description": str,
        "mutating": bool,
        "access": str | None,
        "safety_operation": str | None,
        "required_scopes": list[str],
        "verb_flags": dict,           # flags that apply to the whole verb (if any)
        "required_args": list[str],
      }
    """
    known_resources = set(catalog.get("resource_scopes", {}).keys())
    index: dict[str, list[dict]] = {}

    for verb in sorted(catalog.get("verbs", {}).keys()):
        info = catalog["verbs"][verb]
        if not isinstance(info, dict):
            continue
        resources = info.get("resources") or []
        scopes_by_resource = info.get("required_scopes_by_resource") or {}
        for raw_resource in sorted(resources):
            canon = canonical_resource(raw_resource, known_resources)
            record = {
                "verb": verb,
                "resource_as_written": raw_resource,
                "description": info.get("description", ""),
                "mutating": bool(info.get("mutating", False)),
                "access": info.get("access"),
                "safety_operation": info.get("safety_operation"),
                "required_scopes": scopes_by_resource.get(raw_resource, []),
                "verb_flags": info.get("flags") or {},
                "required_args": info.get("required_args") or [],
            }
            index.setdefault(canon, []).append(record)

    return index


# --------------------------------------------------------------------------
# Markdown table helpers (tiny, dependency-free)
# --------------------------------------------------------------------------

def md_table(headers: list[str], rows: list[list[str]]) -> str:
    if not rows:
        return "_(none)_\n"
    lines = ["| " + " | ".join(headers) + " |"]
    lines.append("| " + " | ".join(["---"] * len(headers)) + " |")
    for row in rows:
        cells = [c.replace("|", "\\|").replace("\n", " ") for c in row]
        lines.append("| " + " | ".join(cells) + " |")
    return "\n".join(lines) + "\n"


# --------------------------------------------------------------------------
# Per-resource reference page
# --------------------------------------------------------------------------

def display_name_for(resource: str, ops: list[dict]) -> str:
    """Prefer the plural form `get` uses (e.g. "workflows") as the doc's
    display/file name, since that's the conventional way per-resource CLI
    reference pages are titled. Falls back to the canonical (usually
    singular) resource id if this resource has no `get` operation."""
    for op in ops:
        if op["verb"] == "get":
            return op["resource_as_written"]
    return resource


def render_resource_page(resource: str, ops: list[dict], catalog: dict, display_name: str | None = None) -> str:
    out = []
    out.append(f"# {display_name or resource}\n")
    out.append(
        "<!-- SME: Overview - one paragraph on what this resource represents, "
        "when to use it, and how it relates to neighboring resources. -->\n"
    )

    out.append("## Supported operations\n")
    rows = []
    for op in sorted(ops, key=lambda o: o["verb"]):
        syntax = f"`dtctl {op['verb']} {op['resource_as_written']}`"
        if op["required_args"]:
            syntax = syntax[:-1] + " " + " ".join(f"<{a}>" for a in op["required_args"]) + "`"
        mutating = "yes" if op["mutating"] else "no"
        rows.append([op["verb"], syntax, op["description"], mutating, op["access"] or ""])
    out.append(md_table(["Operation", "Command syntax", "Description", "Mutating", "Access"], rows))

    out.append("## Flags\n")
    flag_rows = []
    seen_flag_keys: set[tuple[str, str]] = set()
    for op in sorted(ops, key=lambda o: o["verb"]):
        for flag_name, flag_info in sorted(op["verb_flags"].items()):
            key = (op["verb"], flag_name)
            if key in seen_flag_keys:
                continue
            seen_flag_keys.add(key)
            flag_rows.append([
                f"`{flag_name}`",
                flag_info.get("type", ""),
                str(flag_info.get("default", "")),
                flag_info.get("description", ""),
                f"(applies to `{op['verb']}`, not specific to {resource})",
            ])
    if flag_rows:
        out.append(md_table(["Flag", "Type", "Default", "Description", "Scope"], flag_rows))
        out.append(
            "\n<!-- GAP: dtctl's catalog does not expose flags scoped to a single "
            "resource - only verb-level flags (shown above) plus dtctl's global "
            "flags (-o/--output, --context, etc.) apply here. -->\n"
        )
    else:
        out.append(
            "No resource-specific or verb-specific flags are declared in the "
            "catalog for the operations above; only dtctl's **global flags** "
            "apply (`-o/--output`, `--context`, `--jq`, `-v`, etc. "
            "- see `COMMANDS.md`).\n"
        )

    out.append("\n## Required token scopes\n")
    resource_scopes = catalog.get("resource_scopes", {}).get(resource, {})
    alternatives = resource_scopes.get(ALTERNATIVES_KEY, {})
    scope_rows = [[level, scope_cell(scopes, alternatives.get(level))]
                  for level, scopes in scope_levels(resource_scopes)]
    out.append(md_table(["Safety level", "Scopes"], scope_rows))

    out.append("\n## Output\n")
    out.append("<!-- SME: describe the shape of the returned resource (key fields, "
                "nesting, id/name conventions) and how -o json/-o wide differ. -->\n")

    out.append("\n## Examples\n")
    out.append("<!-- SME: add real invocations with sample output. -->\n")

    return "\n".join(out) + "\n"


# --------------------------------------------------------------------------
# COMMANDS.md - full verb x resource matrix
# --------------------------------------------------------------------------

def render_commands_matrix(catalog: dict) -> str:
    out = ["# COMMANDS\n",
           "Generated reference of every dtctl verb and the resources it operates on, "
           "with required token scopes.\n",
           "`mutating` and `read-only` say whether a verb changes anything in the environment. "
           "A read-only verb can still write files on this machine: `alias`, `config` and `ctx` "
           "write dtctl's configuration, and `repo-scope set` and `repo-scope delete` write the "
           "repository's `.dtctl-repo-scope.yaml`.\n"]
    verbs = catalog.get("verbs", {})
    for verb in sorted(verbs.keys()):
        info = verbs[verb]
        if not isinstance(info, dict):
            continue
        out.append(f"## {verb}\n")
        out.append(f"{info.get('description', '')}\n")
        meta_bits = []
        if "mutating" in info:
            meta_bits.append("mutating" if info["mutating"] else "read-only")
        if info.get("access"):
            meta_bits.append(f"access: {info['access']}")
        if info.get("safety_operation"):
            meta_bits.append(f"safety: {info['safety_operation']}")
        if meta_bits:
            out.append(f"_{' | '.join(meta_bits)}_\n")

        resources = info.get("resources") or []
        if resources:
            scopes_by_resource = info.get("required_scopes_by_resource") or {}
            rows = [
                [r, ", ".join(f"`{s}`" for s in scopes_by_resource.get(r, [])) or "_(none declared)_"]
                for r in sorted(resources)
            ]
            out.append(md_table(["Resource", "Required scopes"], rows))

        subcommands = info.get("subcommands") or {}
        if subcommands:
            sub_rows = [[name, sub.get("description", "")] for name, sub in sorted(subcommands.items())]
            out.append("Subcommands:\n")
            out.append(md_table(["Subcommand", "Description"], sub_rows))
        out.append("")
    return "\n".join(out) + "\n"


# --------------------------------------------------------------------------
# TOKEN_SCOPES.md - per safety level, from resource_scopes
# --------------------------------------------------------------------------

def render_token_scopes(catalog: dict) -> str:
    resource_scopes = catalog.get("resource_scopes", {})
    levels: dict[str, list[tuple[str, str]]] = {}
    for resource, by_level in sorted(resource_scopes.items()):
        alternatives = by_level.get(ALTERNATIVES_KEY, {})
        for level, scopes in scope_levels(by_level):
            for scope in scopes:
                levels.setdefault(level, []).append((resource, f"`{scope}`"))
            if alternatives.get(level):
                levels[level].append((resource, format_alternatives(alternatives[level])))

    out = ["# TOKEN_SCOPES\n",
           "Generated reference of the API token scopes each resource requires, "
           "grouped by safety level.\n"]
    for level in sorted(levels.keys()):
        out.append(f"## {level}\n")
        # Cells are pre-rendered; a backtick sorts before "or instead", so a
        # resource's alternatives row follows its scope rows.
        rows = [[resource, cell] for resource, cell in sorted(set(levels[level]))]
        out.append(md_table(["Resource", "Scope"], rows))
    return "\n".join(out) + "\n"


# --------------------------------------------------------------------------


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("catalog_json", type=Path, help="path to `dtctl commands --full -o json` output")
    parser.add_argument("--out-dir", type=Path, default=Path("out"), help="output directory (default: ./out)")
    parser.add_argument(
        "--resource", action="append", default=None,
        help=f"canonical resource id to render a per-resource page for "
             f"(repeatable; default: {DEFAULT_RESOURCE})",
    )
    args = parser.parse_args()

    catalog = load_catalog(args.catalog_json)
    args.out_dir.mkdir(parents=True, exist_ok=True)

    index = build_resource_index(catalog)

    resources_to_render = args.resource or [DEFAULT_RESOURCE]
    for resource in resources_to_render:
        ops = index.get(resource)
        if not ops:
            print(f"WARNING: no operations found for resource '{resource}'", file=sys.stderr)
            continue
        display_name = display_name_for(resource, ops)
        page = render_resource_page(resource, ops, catalog, display_name=display_name)
        out_path = args.out_dir / f"{display_name}.md"
        out_path.write_text(page, encoding="utf-8")
        print(f"wrote {out_path}")

    commands_md = render_commands_matrix(catalog)
    (args.out_dir / "COMMANDS.md").write_text(commands_md, encoding="utf-8")
    print(f"wrote {args.out_dir / 'COMMANDS.md'}")

    token_scopes_md = render_token_scopes(catalog)
    (args.out_dir / "TOKEN_SCOPES.md").write_text(token_scopes_md, encoding="utf-8")
    print(f"wrote {args.out_dir / 'TOKEN_SCOPES.md'}")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
