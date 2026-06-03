# docs_chain — Constitution

## INHERITED FROM constitution/Constitution.md

All rules in `constitution/Constitution.md` (and the `constitution/Constitution.md`
it references) apply unconditionally. This file's rules below extend them —
they MUST NOT weaken any inherited rule. See parent project's root `CLAUDE.md`
§6.AD for the Lava-specific incorporation context (29th §6.L cycle, 2026-05-14)
and §6.AD-debt for the implementation-gap inventory.

> **Status:** Active. This document is the authoritative rule set for the
> `docs_chain` module. When a rule here conflicts with `CLAUDE.md`, `AGENTS.md`,
> or any guide, the Constitution wins.

---

## Module-Level Rules

All constitutional rules from the parent and the constitution submodule apply
unconditionally. Module-specific extensions:

1. **No faking transform success.** When pandoc / weasyprint / any required
   tool is absent, the run MUST return a `ToolAbsentError` and roll back —
   never write a partial or empty output file. (Composes with Sixth Law clause 3.)

2. **Sync conflicts are surfaced, never silently resolved.** A `both-dirty`
   sync pair MUST produce a `ConflictError` exit code (2); the caller decides
   resolution. Never auto-pick a winner. (Composes with §9.2 atomicity.)

3. **Content-hash change detection only, never mtime.** Any reversion to
   mtime-based change detection is a constitutional violation. The hash is the
   authority.

4. **Anti-Bluff Forensic Anchor** (cascaded from parent CONSTITUTION.md §Article
   XI §11.9): the bar for shipping is not "tests pass" but "users can use the
   feature." Every PASS MUST carry positive runtime evidence captured during
   execution.

---

## Amendment Process

Constitution amendments require:
1. Written proposal with rationale
2. Challenge demonstrating the need
3. Approval by project architect
4. Update to this file and cascade to parent governance docs
