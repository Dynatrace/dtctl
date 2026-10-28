#!/usr/bin/env python3
"""
gen_all.py - project driver on top of gen_docs.py.

Maps dtctl's catalog resources onto the PLANNED ~21-file docs structure for
PRODUCT-18391 (including multi-resource grouped pages) and emits one .md per
planned file, plus COMMANDS.md and TOKEN_SCOPES.md.

Each generated resource page carries ONE generator-managed block, wrapped in
`<!-- GENERATED:<resource>:start -->` / `<!-- GENERATED:<resource>:end -->`
markers, holding the three derived tables (Supported operations, Flags,
Required token scopes). Everything else - `## Overview`, `## Output`,
`## Examples`, and the free-form `## Notes` section - is hand-authored prose
that the generator NEVER touches. Regeneration is therefore durable: it injects
only the managed block into an existing page and leaves all prose in place (and
leaves marker-less authored pages entirely alone), so CI's
`git diff --exit-code` stays clean while SME prose survives. Only a brand-new
file gets the full skeleton written from scratch.

Input:  `dtctl commands --full -o json`
Output: <out-dir>/resources/<file>.md, <out-dir>/COMMANDS.md,
        <out-dir>/TOKEN_SCOPES.md, and scripts/gen-docs/INDEX.md (maintainer
        coverage report, not a published doc)
"""
from __future__ import annotations

import sys
from pathlib import Path

import gen_docs  # reuse the proven core (md_table, matrix, token scopes)

# planned file -> (Title, [singular resource stems], authored_only, note)
PLAN = {
    "workflows":            ("Workflows", ["workflow", "workflow-execution", "wfe-task-result",
                                          "scheduling-rule"], False,
                             "scheduling rules live on the same Automation API and are documented here."),
    "dql-queries":          ("DQL Queries", [], True,
                             "DQL is the `query`/`exec` verb, not a CRUD resource - author prose + examples; no per-resource ops to generate."),
    "dashboards-notebooks": ("Dashboards & Notebooks", ["dashboard", "notebook"], False, ""),
    "slos":                 ("SLOs", ["slo", "slo-template"], False, ""),
    "settings-api":         ("Settings", ["setting", "settings-schema"], False, ""),
    "cloud-integrations":   ("Cloud Integrations", ["aws", "azure", "gcp"], True,
                             "NOT a CRUD resource in the CLI - no verb exposes aws/azure/gcp; managed via the Settings API (scopes: settings:objects, extensions:configurations). Author prose + examples."),
    "grail-buckets":        ("Grail Buckets", ["bucket"], False, ""),
    "filter-segments":      ("Filter Segments", ["segment"], False, ""),
    "lookup-tables":        ("Lookup Tables", ["lookup"], False, ""),
    "extensions":           ("Extensions", ["extension", "extension-config", "hub-extension", "hub-extension-release"], False,
                             "hub-extensions folded in here per the plan."),
    "app-engine":           ("App Engine", ["app", "function", "intent"], False, ""),
    "analyzers":            ("Analyzers", ["analyzer"], False, ""),
    "copilot":              ("Davis CoPilot", ["copilot", "copilot-skill"], False,
                             "kept split from analyzers; grouped as 'Davis AI' on the docs.dynatrace.com page only."),
    "anomaly-detectors":    ("Anomaly Detectors", ["anomaly-detector"], False, ""),
    "live-debugger":        ("Live Debugger", ["breakpoint", "snapshot"], False, ""),
    "documents":            ("Documents & Trash", ["document", "trash"], False, ""),
    "edgeconnect":          ("EdgeConnect", ["edgeconnect"], False, ""),
    "notifications":        ("Notifications", ["notification"], False, ""),
    "api-discovery":        ("API Discovery", ["api"], False, ""),
    "platform-tokens":      ("Platform Tokens", ["platform-token", "account-token", "token"], False,
                             "verify: may have no resource_scopes entry in the catalog."),
    "users-groups":         ("Users & Groups", ["user", "group"], False, ""),
    "environment-license":  ("Environment & License", ["environment", "license", "license-settings"], False,
                             "read-only platform management surface; license-settings has no describe form."),
}


def matches(key: str, stems: set[str]) -> bool:
    """Does a catalog resource key (singular or plural - casing varies by verb)
    belong to this file's set of singular stems?

    Folds in the naive de-pluralization that used to live in a separate helper:
    a catalog key matches if it is a stem verbatim, if stripping a trailing
    `s`/`es` yields a stem, or if adding one to a stem yields the key."""
    singular = key
    if key.endswith("es") and len(key) > 3:
        singular = key[:-2]
    elif key.endswith("s") and len(key) > 2:
        singular = key[:-1]
    return (
        key in stems
        or singular in stems
        or any(key == s + "s" or key == s + "es" for s in stems)
    )


def resource_managed_body(title: str, stems: list[str], catalog: dict,
                          raw_index: dict[str, list[dict]]) -> str:
    """Build the generator-managed body for a resource page: the three tables
    (Supported operations, Flags, Required token scopes). This is the ONLY part
    of a resource page the generator owns; it is wrapped in
    `<!-- GENERATED:<resource>:start/end -->` markers and injected in place,
    leaving all hand-authored prose (Overview / Output / Examples / Notes)
    untouched on regeneration."""
    stemset = set(stems)
    out = []

    # Breadcrumb / back-links (inside the managed block so hand-editing can't drop it)
    out.append(f"[dtctl docs](../README.md) / [Resources](README.md) / {title}\n")

    # Supported operations, merged across all matching catalog resource forms
    out.append("## Supported operations\n")
    rows = []
    seen = set()
    for canon in sorted(raw_index.keys()):
        if not matches(canon, stemset):
            continue
        for op in sorted(raw_index[canon], key=lambda o: (o["verb"], o["resource_as_written"])):
            k = (op["verb"], op["resource_as_written"])
            if k in seen:
                continue
            seen.add(k)
            syntax = f"`dtctl {op['verb']} {op['resource_as_written']}`"
            rows.append([op["verb"], op["resource_as_written"], syntax,
                         op["description"], "yes" if op["mutating"] else "no", op["access"] or ""])
    out.append(gen_docs.md_table(
        ["Operation", "Resource", "Command syntax", "Description", "Mutating", "Access"], rows))

    # Flags: the catalog carries flags for verbs only, never per resource
    # (issue #500), so this section can describe what is generatable and must
    # point at --help for the rest. It must not claim the resource has no flags
    # of its own: most of them do (`get dashboards --mine`, `delete bucket
    # --confirm`, `logs workflow-execution --follow`), and asserting their
    # absence sends a reader away from flags that exist.
    out.append("\n## Flags\n")
    out.append("These commands take dtctl's **global flags** (`-o/--output`, "
               "`--context`, `--jq`, `-v`, and more). Some verbs add their own flags "
               "(`apply`, `diff`, `query`, `inventory`); see the verb in "
               "**[COMMANDS.md](../COMMANDS.md)**. Individual commands may take further "
               "flags of their own \u2014 run `dtctl <verb> <resource> --help` for the "
               "authoritative list.\n")

    # Required token scopes, unioned across matching resource_scopes keys
    out.append("\n## Required token scopes\n")
    resource_scopes = catalog.get("resource_scopes", {})
    by_level: dict[str, set[str]] = {}
    # Alternatives per level, one entry per contributing resource key. They are
    # shown only when every key at that level declares the same ones: an
    # alternative to one resource's scopes does not stand in for another's.
    alts_by_level: dict[str, set[tuple]] = {}
    for key, levels in resource_scopes.items():
        if not matches(key, stemset):
            continue
        alternatives = levels.get(gen_docs.ALTERNATIVES_KEY, {})
        for level, scopes in gen_docs.scope_levels(levels):
            by_level.setdefault(level, set()).update(scopes)
            alts_by_level.setdefault(level, set()).add(
                tuple(tuple(alt) for alt in alternatives.get(level, [])))

    def level_alternatives(level: str) -> list[list[str]] | None:
        alts = alts_by_level.get(level, set())
        if len(alts) != 1:
            return None
        only = next(iter(alts))
        return [list(alt) for alt in only] or None

    scope_rows = [[level, gen_docs.scope_cell(sorted(by_level[level]), level_alternatives(level))]
                  for level in sorted(by_level.keys())]
    out.append(gen_docs.md_table(["Safety level", "Scopes"], scope_rows))

    return "\n".join(out).rstrip() + "\n"


def render_grouped_page(fname: str, title: str, stems: list[str], catalog: dict,
                        raw_index: dict[str, list[dict]], note: str) -> str:
    """Full skeleton for a NEW generated resource page (no markers present yet).

    Section order: `# Title`, `## Overview` (placeholder), the GENERATED managed
    block (the three tables), `## Output` (placeholder), `## Examples`
    (placeholder), `## Notes` (placeholder for free-form concepts / advanced
    usage / edge cases / troubleshooting). On regeneration of an EXISTING page,
    `inject_managed` replaces only the managed block, so every placeholder that
    an SME has since filled in is preserved."""
    body = resource_managed_body(title, stems, catalog, raw_index)
    parts = [f"# {title}\n"]
    if note:
        parts.append(f"<!-- NOTE: {note} -->\n")
    parts.append("## Overview\n")
    parts.append("<!-- SME: Overview - what this resource is in the Dynatrace platform, "
                 "when to use it, and how it relates to neighboring resources (1-2 sentences). -->\n")
    parts.append(managed_wrapper(fname, body) + "\n")
    parts.append("## Output\n")
    parts.append("<!-- SME: describe the returned shape (key fields, id/name conventions) "
                 "and how -o json / -o wide differ. -->\n")
    parts.append("## Examples\n")
    parts.append("<!-- SME: 3-5 real invocations with sample output. -->\n")
    parts.append("## Notes\n")
    parts.append("<!-- SME: free-form concepts, advanced usage, edge cases, and "
                 "troubleshooting that do not fit the sections above. -->\n")
    return "\n".join(parts) + "\n"


def render_authored_stub(title: str, note: str) -> str:
    """Skeleton for a NEW authored page (dql-queries, cloud-integrations): these
    have no generator-owned tables, so they carry NO managed block. Once the file
    exists, the generator never overwrites it (see `emit_resource_page`)."""
    return (f"# {title}\n\n<!-- NOTE: {note} -->\n\n"
            "## Overview\n<!-- SME -->\n\n"
            "## Usage\n<!-- SME: this page is authored, not generated -->\n\n"
            "## Examples\n<!-- SME -->\n\n"
            "## Notes\n<!-- SME: free-form concepts, advanced usage, edge cases, "
            "and troubleshooting. -->\n")


def build_raw_index(catalog: dict) -> dict[str, list[dict]]:
    """Like gen_docs.build_resource_index but we keep it here to control merge."""
    return gen_docs.build_resource_index(catalog)


def token_scopes_body(catalog: dict) -> str:
    """The generated scope tables, as a body to inject into the authored
    TOKEN_SCOPES.md (H1 title + intro line stripped, level headings demoted to h3)."""
    lines = gen_docs.render_token_scopes(catalog).splitlines()
    body = []
    for l in lines:
        if l.startswith("# TOKEN_SCOPES") or l.startswith("Generated per-safety-level"):
            continue
        body.append("###" + l[2:] if l.startswith("## ") else l)
    return "## Required scopes by resource (generated)\n" + "\n".join(body).strip() + "\n"


def managed_wrapper(tag: str, body: str) -> str:
    """The full generator-managed block for a tag: the start marker, the
    do-not-edit notice, the generated `body`, and the end marker. Used both when
    emitting a fresh skeleton and when injecting into an existing file, so the two
    paths are byte-identical (this is what keeps CI's `git diff --exit-code`
    clean after regeneration)."""
    start = f"<!-- GENERATED:{tag}:start -->"
    end = f"<!-- GENERATED:{tag}:end -->"
    return (f"{start}\n"
            f"<!-- Do not edit by hand. Generated by scripts/gen-docs from "
            f"`dtctl commands --full -o json`; run `make docs-generate`. -->\n\n"
            f"{body.rstrip()}\n"
            f"{end}")


def inject_managed(path: Path, tag: str, body: str) -> None:
    """Replace the content between <!-- GENERATED:<tag>:start --> and :end markers
    in an existing authored file. Refuses to write if the markers are absent, so a
    hand-authored file is never silently overwritten."""
    start = f"<!-- GENERATED:{tag}:start -->"
    end = f"<!-- GENERATED:{tag}:end -->"
    if not path.exists():
        raise SystemExit(f"{path} does not exist; expected an authored file with '{start}' markers")
    text = path.read_text(encoding="utf-8")
    n_start, n_end = text.count(start), text.count(end)
    if n_start != 1 or n_end != 1:
        # 0 markers -> authored/hand-edited page (never overwrite); >1 -> a
        # duplicated block that a single split would silently leave half-stale.
        raise SystemExit(
            f"expected exactly one '{tag}' GENERATED block in {path} "
            f"(found {n_start} start / {n_end} end markers); refusing to overwrite")
    if text.index(start) > text.index(end):
        # a hand-edit mistake that split() would turn into an opaque unpack error
        raise SystemExit(
            f"'{tag}' GENERATED end marker precedes its start marker in {path}; "
            f"refusing to overwrite")
    pre, rest = text.split(start, 1)
    _, post = rest.split(end, 1)
    path.write_text(pre + managed_wrapper(tag, body) + post, encoding="utf-8")


def has_markers(path: Path, tag: str) -> bool:
    if not path.exists():
        return False
    text = path.read_text(encoding="utf-8")
    return f"<!-- GENERATED:{tag}:start -->" in text and f"<!-- GENERATED:{tag}:end -->" in text


def emit_resource_page(path: Path, fname: str, title: str, stems: list[str],
                       catalog: dict, raw_index: dict[str, list[dict]],
                       authored: bool, note: str) -> str:
    """Write/refresh one docs/resources/<fname>.md, durable against regeneration:

      - file does NOT exist            -> write the full skeleton (grouped page
                                          with a managed block, or an authored
                                          stub with no block).
      - file exists, HAS markers       -> inject ONLY the managed block; all prose
                                          outside the markers is left untouched.
      - file exists, NO markers         -> leave the file entirely alone. This
                                          protects hand-authored pages (the two
                                          authored pages, or a generated page a
                                          human is still migrating) from being
                                          clobbered.

    Returns a short status string for the run log."""
    if not path.exists():
        page = (render_authored_stub(title, note) if authored
                else render_grouped_page(fname, title, stems, catalog, raw_index, note))
        path.write_text(page, encoding="utf-8")
        return "created"
    if has_markers(path, fname):
        inject_managed(path, fname, resource_managed_body(title, stems, catalog, raw_index))
        return "injected"
    return "skipped (no markers; authored/hand-edited page left untouched)"


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: gen_all.py <commands-full.json> [out-dir]", file=sys.stderr)
        return 2
    catalog = gen_docs.load_catalog(Path(sys.argv[1]))
    out_dir = Path(sys.argv[2]) if len(sys.argv) > 2 else Path("out-all")
    (out_dir / "resources").mkdir(parents=True, exist_ok=True)

    raw_index = build_raw_index(catalog)
    all_keys = set(raw_index.keys()) | set(catalog.get("resource_scopes", {}).keys())

    index_rows = []
    covered_keys: set[str] = set()
    for fname, (title, stems, authored, note) in PLAN.items():
        gen = "authored" if authored else "generated"
        # A file "covers" its catalog stems whether it is generated or authored:
        # authored pages (e.g. cloud-integrations -> aws/azure/gcp) map real
        # catalog keys too, so they must not surface as "unmapped".
        covered_keys.update(k for k in all_keys if matches(k, set(stems)))
        path = out_dir / "resources" / f"{fname}.md"
        status = emit_resource_page(path, fname, title, stems, catalog, raw_index, authored, note)
        print(f"  resources/{fname}.md: {status}")
        index_rows.append([f"`resources/{fname}.md`", title, ", ".join(stems) or "-", gen])

    # Resource index (item 1): a generated per-page list, used as both the
    # docs/resources/ folder index and injected into the docs/README.md hub.
    res_rows = sorted((title, fname) for fname, (title, *_rest) in PLAN.items())
    def _res_list(prefix: str) -> str:
        return "".join(f"- [{title}]({prefix}{fname}.md)\n" for title, fname in res_rows)
    (out_dir / "resources" / "README.md").write_text(
        "# dtctl resource reference\n\n"
        "<!-- Do not edit by hand. Generated by scripts/gen-docs from "
        "`dtctl commands --full -o json`; run `make docs-generate`. -->\n\n"
        "Per-resource reference for [`dtctl`](https://github.com/Dynatrace/dtctl). Each "
        "page lists supported operations, flags, required token scopes, output, and examples.\n\n"
        + _res_list("")
        + "\nBack to the [dtctl documentation hub](../README.md).\n",
        encoding="utf-8")
    # docs/README.md stays hand-authored but hosts the generated per-resource list
    # between GENERATED:resources-index markers.
    inject_managed(out_dir / "README.md", "resources-index", _res_list("resources/").rstrip() + "\n")

    # cross-cutting generated files (reuse proven core)
    (out_dir / "COMMANDS.md").write_text(gen_docs.render_commands_matrix(catalog), encoding="utf-8")
    # TOKEN_SCOPES.md is a HAND-AUTHORED doc (caveats, guidance) with a generator-managed
    # block. Inject the generated scope tables between markers; never overwrite the file.
    inject_managed(out_dir / "TOKEN_SCOPES.md", "token-scopes", token_scopes_body(catalog))

    # coverage report: which catalog resource keys did NOT land in any file.
    # `covered_keys` already holds every key that matches() any file's stems in
    # either singular or plural form, so a plain set difference is exact.
    uncovered = sorted(k for k in all_keys if k not in covered_keys)
    idx = ["# INDEX - planned file -> catalog resources\n",
           "<!-- Coverage report for maintainers, NOT published documentation. Written "
           "next to the generator (scripts/gen-docs/), regenerated by `make docs-generate`, "
           "and checked by CI so new/renamed catalog resources surface here. -->\n",
           gen_docs.md_table(["File", "Title", "Catalog stems", "Mode"], index_rows),
           "\n## Catalog resource keys not mapped to any file\n",
           "<!-- SME: these catalog keys did not map to any doc page - expected verbs "
           "and subcommands (login, set-context, openpipeline-*, sdk-version, ...), not "
           "resources; confirm none needs its own page. -->\n\n",
           gen_docs.md_table(["Unmapped key"], [[k] for k in uncovered]) if uncovered else "_none_\n"]
    # INDEX is a maintainer coverage report, not a published doc: write it next to
    # the generator instead of into the docs tree.
    index_path = Path(__file__).resolve().parent / "INDEX.md"
    index_path.write_text("\n".join(idx), encoding="utf-8")

    print(f"wrote {len(PLAN)} resource files + resources/README.md + README.md list + COMMANDS.md + TOKEN_SCOPES.md to {out_dir}")
    print(f"wrote coverage report to {index_path}")
    print(f"unmapped catalog keys: {uncovered}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
