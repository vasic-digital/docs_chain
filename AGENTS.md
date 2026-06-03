# AGENTS.md — docs_chain Module Multi-Agent Coordination

## INHERITED FROM constitution/AGENTS.md

All rules in `constitution/AGENTS.md` (and the `constitution/Constitution.md`
it references) apply unconditionally. This file's rules below extend them —
they MUST NOT weaken any inherited rule. See parent project's root `CLAUDE.md`
§6.AD for the Lava-specific incorporation context (29th §6.L cycle, 2026-05-14)
and §6.AD-debt for the implementation-gap inventory.

## Module Identity

- **Module:** `digital.vasic.docs_chain`
- **Role:** Bidirectional document-and-database dependency-propagation engine
- **Packages:** `internal/hash`, `internal/graph`, `internal/adapter`,
  `internal/orchestrator`, `internal/config`, `internal/state`,
  `internal/runner`, `cmd/docs_chain`
- **Go Version:** 1.25+

## Agent Responsibilities

The docs_chain agent owns all packages in this module. It is responsible for:

1. **Core engine** (`internal/hash`, `internal/graph`) — content-hash change
   detection, DAG topology, Kahn ordering, early-cutoff recomputation, sync
   conflict resolution.
2. **Node adapters** (`internal/adapter`) — FileAdapter (markdown, html, pdf,
   docx), SQLiteAdapter (canonical row dump, byte-stable), FileStore.
3. **Propagation orchestrator** (`internal/orchestrator`) — atomic multi-file
   propagation with rollback on error, cycle-guard, sync-conflict surfacing.
4. **Config + state + runner** (`internal/config`, `internal/state`,
   `internal/runner`) — per-context YAML loading, state.json baseline, full
   orchestration pipeline.
5. **CLI** (`cmd/docs_chain`) — `sync`, `verify`, `doctor`, `graph`, `watch`
   subcommands with exit-code contract.

## Cross-Agent Coordination

This submodule is standalone. When consumed by a parent project (e.g. Lava),
the agent works at the parent level to register the consuming project's
`.docs_chain/contexts/<name>.yaml` files; changes to the engine itself go
upstream to this repo first, then a parent pin bump follows.

## Anti-Bluff Mandate

Every test added to this module MUST satisfy all Sixth + Seventh Law clauses
inherited from the parent. See parent root `CLAUDE.md` §6.J / §6.L.
