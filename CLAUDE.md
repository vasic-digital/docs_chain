# CLAUDE.md — docs_chain Module

## INHERITED FROM constitution/CLAUDE.md

All rules in `constitution/CLAUDE.md` (and the `constitution/Constitution.md`
it references) apply unconditionally. This file's rules below extend them —
they MUST NOT weaken any inherited rule. See parent project's root `CLAUDE.md`
§6.AD for the Lava-specific incorporation context (29th §6.L cycle, 2026-05-14)
and §6.AD-debt for the implementation-gap inventory. Use
`constitution/find_constitution.sh` from the parent project root to resolve the
absolute path of the constitution submodule from any nested location.

## Definition of Done

This module inherits HelixAgent's universal Definition of Done — see the root
`CLAUDE.md` and `docs/development/definition-of-done.md`. In one line: **no
task is done without pasted output from a real run of the real system in the
same session as the change.** Coverage and green suites are not evidence.

### Acceptance demo for this module

```bash
cd docs_chain
GOMAXPROCS=2 nice -n 19 go test -race -count=1 ./...
```

Expect: PASS; exercises all phases 1–5 of the DAG, hash, adapter,
orchestrator, config, state, runner, and CLI packages.

## Overview

`digital.vasic.docs_chain` is a universal, Go-implemented, bidirectional
document-and-database dependency-propagation engine. It detects changes by
content hash and propagates them through a DAG of registered chain members
atomically.

**Module:** `digital.vasic.docs_chain` (Go 1.25+)

## Build & Test

```bash
go build ./...
go test ./... -race -count=1
go build -o ./docs_chain ./cmd/docs_chain
```

## Commit Style

Conventional Commits: `feat(graph): add early-cutoff optimisation`

## No sudo/su (§6.U)

ALL operations MUST run at local user level ONLY. No `sudo` or `su` in any
committed script, Makefile, or tool call.

## Host Power Management — Hard Ban

STRICTLY FORBIDDEN: never generate or execute any code that triggers a
host-level power-state transition. See parent `CLAUDE.md` §Host Machine
Stability Directive for the full forbidden command list.

## §6.S — Continuation Document Maintenance (inherited)

See parent root `CLAUDE.md` §6.S. The parent `docs/CONTINUATION.md` is the
single-file source-of-truth handoff. Every commit that changes this submodule's
phase status or ships a release artifact MUST update `docs/CONTINUATION.md` in
the SAME parent commit.

## §6.W — GitHub + GitLab Only Remotes (inherited)

See parent root `CLAUDE.md` §6.W. Only GitHub (`vasic-digital/docs_chain`) and
GitLab (`vasic-digital/docs_chain`) are permitted as Git remotes. All push fan-out
MUST go through both.

## Anti-Bluff Testing Pact (inherited §6.J / §6.L / Sixth + Seventh Laws)

Every test, every CI gate, has exactly one job: confirm the feature works for a
real user end-to-end. CI green is necessary, NEVER sufficient. Tests must
guarantee the product works — anything else is theatre. See parent root
`CLAUDE.md` §6.J + §6.L for the full mandate.
