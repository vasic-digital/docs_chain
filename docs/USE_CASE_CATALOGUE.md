# docs_chain — Use-Case Catalogue

**Revision:** 1
**Last modified:** 2026-05-29T00:00:00Z
**Status:** Living registry. Every recipe below is a DESIGNED context. Engine support is PLANNED (Phases 1–4); ATMOSphere wiring is PLANNED (Phase 7). See per-recipe status tags.
**Authority:** Operator mandate 2026-05-29 (docs_chain initiative)
**Design provenance:** authoritative Phase-0 DESIGN / RESEARCH / PLAN live in the consuming project research tree (`docs/research/docs_chain/`); this document is the self-contained specification.

---

## Status legend

Per §11.4.6 (no-guessing):

- **PLANNED (Phase N)** — the recipe is designed; engine/wiring lands in
  Phase N of `PLAN.md`.
- A recipe is marked **IMPLEMENTED** only once its context is registered
  and green in a future revision of this file.

At the time of writing every recipe is PLANNED. These are ready-to-use
chain definitions a consuming project can stage now; they are not claims
that the engine processes them today.

This file is a **living registry** — new projects extend it by appending
a recipe section in the same shape (purpose · members · edge directions ·
transform · mandate satisfied · script superseded · YAML).

---

## Catalogue index

| # | Recipe | Mandate(s) satisfied | Supersedes (ad-hoc script) |
|---|--------|----------------------|----------------------------|
| (a) | [Issues chain](#a-issues-chain) | §11.4.93 · §11.4.12 · §11.4.95 | `sync_issues_docs.sh`, `generate_issues_summary.sh` |
| (b) | [Fixed chain](#b-fixed-chain) | §11.4.53 · §11.4.19 | `generate_fixed_summary.sh`, `sync_issues_docs.sh` (Fixed stage) |
| (c) | [Status-doc chain](#c-status-doc-chain) | §11.4.45 · §11.4.56 | `sync_integration_status.sh`, `generate_status_summary.sh` |
| (d) | [Roster / corpus chain](#d-roster--corpus-chain) | §11.4.86 | `sync_asset_player_status.sh` |
| (e) | [Changelog chain](#e-changelog-chain) | §11.4.65 | `export_changelog.sh` |
| (f) | [README doc-link chain](#f-readme-doc-link-chain) | §11.4.57 · §11.4.59 | `update_readme_doc_links.sh`, `sync_readme_export.sh` |
| (g) | [Universal markdown-export chain](#g-universal-markdown-export-chain) | §11.4.65 | `sync_all_markdown_exports.sh` |
| (h) | [CONTINUATION chain](#h-continuation-chain) | §12.10 · §11.4.44 | (manual) `sync_issues_docs.sh` CONTINUATION stage |

**Catalogued scenario count: 8.**

---

## (a) Issues chain

**Status: PLANNED (Phase 7).**

- **Purpose:** keep the workable-items tracker mutually consistent —
  `Issues.md` and `workable_items.db` are bidirectional views of the same
  data; `Issues_Summary.md` is generated from the Markdown; HTML + PDF
  exports are derived.
- **Members:** `workable_items.db` (sqlite), `Issues.md` (markdown),
  `Issues_Summary.md` (summary), `Issues.html` / `Issues.pdf` (exports),
  and the summary's own `.html` / `.pdf`.
- **Edge directions:** `Issues.md ↔ workable_items.db` (sync, authority =
  db); `Issues.md → Issues_Summary.md` (derive); `Issues.md → .html →
  .pdf` (derive); `Issues_Summary.md → .html → .pdf` (derive).
- **Transform:** `md-to-db` / `db-to-md` (the §11.4.93 `workable-items`
  binary), `gen-issues-summary`, `pandoc-html`, `weasyprint-pdf`.
- **Mandate:** §11.4.93 (DB single source of truth) · §11.4.12
  (Issues_Summary sync) · §11.4.95 (DB tracked + WAL-checkpoint commit).
- **Supersedes:** `sync_issues_docs.sh` (Issues stage),
  `generate_issues_summary.sh`.

```yaml
context: issues
description: Workable-items tracker chain (DB single source of truth)
nodes:
  items_db:       { kind: sqlite,   path: docs/workable_items.db }
  issues_md:      { kind: markdown, path: docs/Issues.md }
  issues_summary: { kind: summary,  path: docs/Issues_Summary.md }
  issues_html:    { kind: html,     path: docs/Issues.html }
  issues_pdf:     { kind: pdf,      path: docs/Issues.pdf }
  summary_html:   { kind: html,     path: docs/Issues_Summary.html }
  summary_pdf:    { kind: pdf,      path: docs/Issues_Summary.pdf }
edges:
  - { type: sync, a: issues_md, b: items_db, authority: items_db,
      transform_a_to_b: md-to-db, transform_b_to_a: db-to-md }
  - { type: derive-from, from: issues_md,      to: issues_summary, transform: gen-issues-summary }
  - { type: derive-from, from: issues_md,      to: issues_html,    transform: md-to-html }
  - { type: derive-from, from: issues_html,    to: issues_pdf,     transform: html-to-pdf }
  - { type: derive-from, from: issues_summary, to: summary_html,   transform: md-to-html }
  - { type: derive-from, from: summary_html,   to: summary_pdf,    transform: html-to-pdf }
transforms:
  md-to-db:           { exec: "scripts/testing/workable_items", args: ["sync", "md-to-db"] }
  db-to-md:           { exec: "scripts/testing/workable_items", args: ["sync", "db-to-md"] }
  gen-issues-summary: { exec: "scripts/testing/generate_issues_summary.sh" }
  md-to-html:         { builtin: pandoc-html }
  html-to-pdf:        { builtin: weasyprint-pdf }
```

---

## (b) Fixed chain

**Status: PLANNED (Phase 7).**

- **Purpose:** mirror of the issues chain for the closed-archive tracker —
  `Fixed_Summary.md` regenerated from `Fixed.md`, with synchronized
  exports.
- **Members:** `Fixed.md` (markdown), `Fixed_Summary.md` (summary), the
  `.html` / `.pdf` of each.
- **Edge directions:** `Fixed.md → Fixed_Summary.md` (derive); `Fixed.md →
  .html → .pdf` (derive); `Fixed_Summary.md → .html → .pdf` (derive).
- **Transform:** `gen-fixed-summary`, `pandoc-html`, `weasyprint-pdf`.
- **Mandate:** §11.4.53 (Fixed_Summary parity) · §11.4.19 (column
  alignment).
- **Supersedes:** `generate_fixed_summary.sh`, `sync_issues_docs.sh`
  (Fixed stage).

```yaml
context: fixed
description: Closed-archive tracker chain (Fixed_Summary parity)
nodes:
  fixed_md:      { kind: markdown, path: docs/Fixed.md }
  fixed_summary: { kind: summary,  path: docs/Fixed_Summary.md }
  fixed_html:    { kind: html,     path: docs/Fixed.html }
  fixed_pdf:     { kind: pdf,      path: docs/Fixed.pdf }
  fsum_html:     { kind: html,     path: docs/Fixed_Summary.html }
  fsum_pdf:      { kind: pdf,      path: docs/Fixed_Summary.pdf }
edges:
  - { type: derive-from, from: fixed_md,      to: fixed_summary, transform: gen-fixed-summary }
  - { type: derive-from, from: fixed_md,      to: fixed_html,    transform: md-to-html }
  - { type: derive-from, from: fixed_html,    to: fixed_pdf,     transform: html-to-pdf }
  - { type: derive-from, from: fixed_summary, to: fsum_html,     transform: md-to-html }
  - { type: derive-from, from: fsum_html,     to: fsum_pdf,      transform: html-to-pdf }
transforms:
  gen-fixed-summary: { exec: "scripts/testing/generate_fixed_summary.sh" }
  md-to-html:        { builtin: pandoc-html }
  html-to-pdf:       { builtin: weasyprint-pdf }
```

---

## (c) Status-doc chain

**Status: PLANNED (Phase 7).**

- **Purpose:** every per-domain integration `Status.md` keeps its
  `Status_Summary.md` (two-audience digest) and exports in sync.
- **Members:** `docs/<domain>/<integration>/Status.md`,
  `Status_Summary.md`, and the `.html` / `.pdf` of each.
- **Edge directions:** `Status.md → Status_Summary.md` (derive);
  `Status.md → .html → .pdf` (derive); `Status_Summary.md → .html → .pdf`
  (derive).
- **Transform:** `gen-status-summary` (two-page page-1 non-developer +
  page-2 engineer output, §11.4.56), `pandoc-html`, `weasyprint-pdf`.
- **Mandate:** §11.4.45 (Status.md maintenance) · §11.4.56
  (Status_Summary two-audience parity).
- **Supersedes:** `sync_integration_status.sh`,
  `generate_status_summary.sh`.

```yaml
context: status_dolby
description: Dolby integration status chain (per-domain instance)
nodes:
  status_md:   { kind: status,         path: docs/dolby/test_assets/Status.md }
  status_sum:  { kind: status_summary, path: docs/dolby/test_assets/Status_Summary.md }
  status_html: { kind: html,           path: docs/dolby/test_assets/Status.html }
  status_pdf:  { kind: pdf,            path: docs/dolby/test_assets/Status.pdf }
  sum_html:    { kind: html,           path: docs/dolby/test_assets/Status_Summary.html }
  sum_pdf:     { kind: pdf,            path: docs/dolby/test_assets/Status_Summary.pdf }
edges:
  - { type: derive-from, from: status_md,  to: status_sum,  transform: gen-status-summary }
  - { type: derive-from, from: status_md,  to: status_html, transform: md-to-html }
  - { type: derive-from, from: status_html,to: status_pdf,  transform: html-to-pdf }
  - { type: derive-from, from: status_sum, to: sum_html,    transform: md-to-html }
  - { type: derive-from, from: sum_html,   to: sum_pdf,     transform: html-to-pdf }
transforms:
  gen-status-summary: { exec: "scripts/testing/generate_status_summary.sh",
                        args: ["docs/dolby/test_assets/Status.md"] }
  md-to-html:         { builtin: pandoc-html }
  html-to-pdf:        { builtin: weasyprint-pdf }
```

Register one such context per integration domain (`status_dolby`,
`status_players`, …).

---

## (d) Roster / corpus chain

**Status: PLANNED (Phase 7).**

- **Purpose:** when a tracked roster (installed apps) or asset corpus
  changes membership, the backing `Status.md` re-syncs out of the box
  (drift-proof content fingerprint, NOT mtime).
- **Members:** a `fingerprint` node over the roster/corpus glob, the
  backing `Status.md`, its `Status_Summary.md`, and exports.
- **Edge directions:** `fingerprint → Status.md` (derive — a membership
  change re-derives the Status doc); `Status.md → Status_Summary.md →
  exports` (derive, as in recipe c).
- **Transform:** `members-fingerprint` (builtin, sha256 of sorted member
  list), `gen-roster-status`, `gen-status-summary`, exports.
- **Mandate:** §11.4.86 (roster/corpus-backed Status auto-sync,
  fingerprint not mtime).
- **Supersedes:** `sync_asset_player_status.sh`.

```yaml
context: asset_player
description: Pre-installed player roster -> players Status chain
nodes:
  roster_fp:   { kind: fingerprint, path: docs/players/.roster_fingerprint,
                 members: "device/rockchip/rk3588/prebuilt_apps/*.apk" }
  players_md:  { kind: status,         path: docs/players/Status.md }
  players_sum: { kind: status_summary, path: docs/players/Status_Summary.md }
  players_html:{ kind: html,           path: docs/players/Status.html }
  players_pdf: { kind: pdf,            path: docs/players/Status.pdf }
edges:
  - { type: derive-from, from: roster_fp,  to: players_md,   transform: gen-roster-status }
  - { type: derive-from, from: players_md, to: players_sum,  transform: gen-status-summary }
  - { type: derive-from, from: players_md, to: players_html, transform: md-to-html }
  - { type: derive-from, from: players_html, to: players_pdf, transform: html-to-pdf }
transforms:
  gen-roster-status:  { exec: "scripts/testing/sync_asset_player_status.sh" }
  gen-status-summary: { exec: "scripts/testing/generate_status_summary.sh",
                        args: ["docs/players/Status.md"] }
  md-to-html:         { builtin: pandoc-html }
  html-to-pdf:        { builtin: weasyprint-pdf }
```

The `fingerprint` node hashes the sorted APK list — adding / removing /
renaming a player APK changes the hash and re-derives the Status doc,
even though `git checkout` would reset mtime (§11.4.86 forensic lesson).
A second corpus context (`asset_corpus`) registers the same shape over a
test-asset directory glob with an `exclude:` for gitignored `downloaded/`.

---

## (e) Changelog chain

**Status: PLANNED (Phase 7).**

- **Purpose:** per-version `changelogs/<tag>.md` always carries
  synchronized `.html` / `.pdf` exports.
- **Members:** `docs/changelogs/<tag>.md` (markdown), its `.html` /
  `.pdf`.
- **Edge directions:** `<tag>.md → .html → .pdf` (derive).
- **Transform:** `pandoc-html`, `weasyprint-pdf` (and optionally an
  `export-changelog` exec wrapping the existing multi-format exporter).
- **Mandate:** §11.4.65 (universal markdown export).
- **Supersedes:** `export_changelog.sh`.

```yaml
context: changelog
description: Per-version changelog markdown -> html + pdf
nodes:
  cl_md:   { kind: markdown, path: docs/changelogs/1.1.5-dev.md }
  cl_html: { kind: html,     path: docs/changelogs/1.1.5-dev.html }
  cl_pdf:  { kind: pdf,      path: docs/changelogs/1.1.5-dev.pdf }
edges:
  - { type: derive-from, from: cl_md,   to: cl_html, transform: md-to-html }
  - { type: derive-from, from: cl_html, to: cl_pdf,  transform: html-to-pdf }
transforms:
  md-to-html:  { builtin: pandoc-html }
  html-to-pdf: { builtin: weasyprint-pdf }
```

For a directory of changelogs, register one context per active version
or use a glob-expanding wrapper context (see recipe g).

---

## (f) README doc-link chain

**Status: PLANNED (Phase 7).**

- **Purpose:** the README's Tracked-Items + Status Documents section
  (per-doc revision + last-modified) re-renders whenever any linked doc
  changes; README's own HTML + PDF stay in sync.
- **Members:** every tracked-doc node feeding the link table, `README.md`
  (summary-like — regenerated section), `README.html` / `README.pdf`.
- **Edge directions:** each linked doc `→ README.md` (derive — the
  doc-link generator reads each source's revision header); `README.md →
  .html → .pdf` (derive).
- **Transform:** `gen-readme-doc-links` (reads each source's §11.4.44
  revision header), `pandoc-html`, `weasyprint-pdf`.
- **Mandate:** §11.4.57 (README doc-link section) · §11.4.59 (README
  always-sync export).
- **Supersedes:** `update_readme_doc_links.sh`, `sync_readme_export.sh`.

```yaml
context: readme_doclinks
description: README Tracked-Items section + README exports
nodes:
  issues_md:  { kind: markdown, path: docs/Issues.md }
  fixed_md:   { kind: markdown, path: docs/Fixed.md }
  contin_md:  { kind: markdown, path: docs/CONTINUATION.md }
  readme_md:  { kind: summary,  path: README.md }
  readme_html:{ kind: html,     path: README.html }
  readme_pdf: { kind: pdf,      path: README.pdf }
edges:
  - { type: derive-from, from: [issues_md, fixed_md, contin_md], to: readme_md,
      transform: gen-readme-doc-links }
  - { type: derive-from, from: readme_md,   to: readme_html, transform: md-to-html }
  - { type: derive-from, from: readme_html, to: readme_pdf,  transform: html-to-pdf }
transforms:
  gen-readme-doc-links: { exec: "scripts/testing/update_readme_doc_links.sh" }
  md-to-html:           { builtin: pandoc-html }
  html-to-pdf:          { builtin: weasyprint-pdf }
```

The `from:` list (multi-input derive) means a change in ANY linked doc
re-renders the README section. Status docs auto-discovered by the
generator are added to the `from:` list as the project grows.

---

## (g) Universal markdown-export chain

**Status: PLANNED (Phase 7).**

- **Purpose:** the catch-all — any project Markdown that is not part of an
  application/service source tree gets synchronized `.html` + `.pdf`
  siblings.
- **Members:** for each in-scope `.md`: the markdown node + its `.html` +
  `.pdf`.
- **Edge directions:** `<doc>.md → .html → .pdf` (derive), repeated per
  doc.
- **Transform:** `pandoc-html`, `weasyprint-pdf` (the same builtins
  `sync_all_markdown_exports.sh` shells to).
- **Mandate:** §11.4.65 (universal markdown export — md → html + pdf for
  all governed docs).
- **Supersedes:** `sync_all_markdown_exports.sh`.

```yaml
context: markdown_exports
description: Universal md -> html + pdf for governed docs (one block per doc)
nodes:
  guide_arch_md:   { kind: markdown, path: docs/guides/SYSTEM_ARCHITECTURE.md }
  guide_arch_html: { kind: html,     path: docs/guides/SYSTEM_ARCHITECTURE.html }
  guide_arch_pdf:  { kind: pdf,      path: docs/guides/SYSTEM_ARCHITECTURE.pdf }
  # ... one node-triple + edge-pair per governed .md ...
edges:
  - { type: derive-from, from: guide_arch_md,   to: guide_arch_html, transform: md-to-html }
  - { type: derive-from, from: guide_arch_html, to: guide_arch_pdf,  transform: html-to-pdf }
transforms:
  md-to-html:  { builtin: pandoc-html }
  html-to-pdf: { builtin: weasyprint-pdf }
```

This context is the broadest; the §11.4.65 in-scope set (project-root
`*.md`, `docs/**`, `scripts/**` companion docs, owned-submodule top-level
docs, `constitution/**`) is enumerated by the same discovery rules the
legacy script uses. Excludes `external/`, `prebuilts/`,
`packages/modules/`, `kernel-5.10/`, `out/`, `build/`, and
application/service source trees.

---

## (h) CONTINUATION chain

**Status: PLANNED (Phase 7).**

- **Purpose:** the §12.10 resumption document keeps its `Last updated:`
  revision header current and its `.html` / `.pdf` exports in sync, so any
  agent resuming work reads a non-divergent view across formats.
- **Members:** `docs/CONTINUATION.md` (markdown), its `.html` / `.pdf`.
- **Edge directions:** `CONTINUATION.md → .html → .pdf` (derive).
- **Transform:** `pandoc-html`, `weasyprint-pdf`. (Revision-header
  freshness is the source-edit author's responsibility per §11.4.44 /
  §12.10; docs_chain keeps the exports from drifting behind the source.)
- **Mandate:** §12.10 (CONTINUATION maintenance) · §11.4.44 (revision
  header — the `Last updated:` line IS the §11.4.44 `Last modified:`
  line).
- **Supersedes:** the CONTINUATION stage of `sync_issues_docs.sh`.

```yaml
context: continuation
description: Resumption document + synchronized exports
nodes:
  cont_md:   { kind: markdown, path: docs/CONTINUATION.md }
  cont_html: { kind: html,     path: docs/CONTINUATION.html }
  cont_pdf:  { kind: pdf,      path: docs/CONTINUATION.pdf }
edges:
  - { type: derive-from, from: cont_md,   to: cont_html, transform: md-to-html }
  - { type: derive-from, from: cont_html, to: cont_pdf,  transform: html-to-pdf }
transforms:
  md-to-html:  { builtin: pandoc-html }
  html-to-pdf: { builtin: weasyprint-pdf }
```

---

## Extending this registry

A new project (or a new domain in this project) adds a scenario by
appending a section in the exact shape above:

1. **Purpose** — one sentence.
2. **Members** — the node list with kinds.
3. **Edge directions** — `derive-from` (one-way) and/or `sync`
   (bidirectional, with authority).
4. **Transform** — builtins and/or `exec:` adapters.
5. **Mandate satisfied** — the §11.4.x / §12.x anchor it enforces.
6. **Supersedes** — the ad-hoc script it retires.
7. **The YAML** — copy-paste ready.

Keep the [catalogue index](#catalogue-index) table and the **catalogued
scenario count** in sync when adding a recipe.
