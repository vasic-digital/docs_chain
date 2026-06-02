#!/usr/bin/env bash
# Comprehensive end-to-end smoke for the docs_chain binary against the REAL
# engine (§11.4.106(E) anti-bluff: every check exercises observable behaviour of
# the actual built binary; tool-absent surfaces an honest SKIP, never a fake
# pass). Captures a transcript under qa-results/docs_chain/<run-id>/ as §11.4.69
# evidence. Exits non-zero on any FAIL.
set -uo pipefail
cd "$(dirname "$0")/.."
TS="${1:-$(date -u +%Y%m%dT%H%M%SZ)}"
EVID="qa-results/docs_chain/e2e-${TS}"
mkdir -p "$EVID"
LOG="$EVID/transcript.txt"
: > "$LOG"
pass=0; fail=0; skip=0
say(){ printf '%s\n' "$*" | tee -a "$LOG"; }
# check NAME WANT_CODE -- cmd...
check(){ local name="$1" want="$2"; shift 3
  "$@" >/tmp/dc_e2e.out 2>&1; local got=$?
  if [ "$got" = "$want" ]; then say "PASS  $name (exit $got)"; pass=$((pass+1))
  else say "FAIL  $name (got exit $got, want $want)"; sed 's/^/        /' /tmp/dc_e2e.out | tee -a "$LOG"; fail=$((fail+1)); fi
}
assert_file(){ local name="$1" f="$2" needle="${3:-}"
  if [ ! -s "$f" ]; then say "FAIL  $name (missing/empty $f)"; fail=$((fail+1)); return; fi
  if [ -n "$needle" ] && ! grep -qF "$needle" "$f"; then say "FAIL  $name ($f lacks '$needle')"; fail=$((fail+1)); return; fi
  say "PASS  $name ($f ok)"; pass=$((pass+1))
}

say "# docs_chain e2e — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
say "go: $(go version)"
say ""
say "## build the real binary"
if ! go build -o /tmp/dc_e2e_bin ./cmd/docs_chain 2>>"$LOG"; then say "FAIL  build"; exit 1; fi
BIN=/tmp/dc_e2e_bin
say "PASS  build (/tmp/dc_e2e_bin)"; pass=$((pass+1))

ROOT="$(mktemp -d)"; trap 'rm -rf "$ROOT"' EXIT
mkdir -p "$ROOT/.docs_chain/contexts" "$ROOT/data"

say ""
say "## pure-Go md→sqlite→md chain (no external tool — always runs)"
cat > "$ROOT/.docs_chain/contexts/tables.yaml" <<'YAML'
context: tables
nodes:
  src:  { kind: markdown, path: data/items.md }
  db:   { kind: sqlite,   path: data/items.db }
  view: { kind: markdown, path: data/items.view.md }
edges:
  - { type: derive-from, from: src, to: db,   transform: m2d }
  - { type: derive-from, from: db,  to: view, transform: d2m }
transforms:
  m2d: { builtin: md-to-sqlite }
  d2m: { builtin: sqlite-to-md }
YAML
printf '## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n' > "$ROOT/data/items.md"

check "doctor tables"        0 -- "$BIN" doctor --root "$ROOT" tables
check "graph tables"         0 -- "$BIN" graph  --root "$ROOT" tables
check "sync tables"          0 -- "$BIN" sync   --root "$ROOT" tables
assert_file "db produced"    "$ROOT/data/items.db"
assert_file "view produced"  "$ROOT/data/items.view.md" "| 1 | alpha |"
check "verify in-sync"       0 -- "$BIN" verify --root "$ROOT" tables
# edit the source -> verify must report STALE (exit 1)
printf '## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n| 2 | beta |\n' > "$ROOT/data/items.md"
check "verify STALE on edit" 1 -- "$BIN" verify --root "$ROOT" tables
check "re-sync"              0 -- "$BIN" sync   --root "$ROOT" tables
assert_file "row propagated" "$ROOT/data/items.view.md" "| 2 | beta |"
check "unknown context err"  1 -- "$BIN" sync   --root "$ROOT" nope
check "unknown subcommand"   1 -- "$BIN" frobnicate

say ""
say "## §11.4.23 md→html→colorize chain"
if command -v pandoc >/dev/null 2>&1; then
  cat > "$ROOT/.docs_chain/contexts/tracker.yaml" <<'YAML'
context: tracker
nodes:
  iss:   { kind: markdown, path: Issues.md }
  html:  { kind: html,     path: Issues.html }
  color: { kind: html,     path: Issues.colored.html }
edges:
  - { type: derive-from, from: iss,  to: html,  transform: m2h }
  - { type: derive-from, from: html, to: color, transform: cz }
transforms:
  m2h: { builtin: pandoc-html }
  cz:  { builtin: colorize-html }
YAML
  printf '| ID | Type | Status |\n| --- | --- | --- |\n| H-1 | bug | open |\n| H-2 | task | fixed |\n| H-3 | feature | blocker |\n' > "$ROOT/Issues.md"
  check "sync tracker"        0 -- "$BIN" sync   --root "$ROOT" tracker
  assert_file "colorized html" "$ROOT/Issues.colored.html" "background-color"
  assert_file "§11.4.23 bug pale-red"  "$ROOT/Issues.colored.html" "#ffdce0"
  assert_file "§11.4.23 fixed green"   "$ROOT/Issues.colored.html" "#d6f5d9"
  assert_file "§11.4.23 blocker red"   "$ROOT/Issues.colored.html" "#ff6b6b"
  check "verify tracker in-sync" 0 -- "$BIN" verify --root "$ROOT" tracker
else
  say "SKIP  md→html→colorize chain — pandoc absent (honest §11.4.3 SKIP-with-reason, not a fake pass)"
  skip=$((skip+1))
fi

say ""
say "## anti-bluff: a builtin needing an absent tool surfaces honest SKIP/rollback (exit 3), never a fake pass"
if command -v pandoc >/dev/null 2>&1; then
  cat > "$ROOT/.docs_chain/contexts/needtool.yaml" <<'YAML'
context: needtool
nodes:
  m: { kind: markdown, path: T.md }
  h: { kind: html,     path: T.html }
edges:
  - { type: derive-from, from: m, to: h, transform: m2h }
transforms:
  m2h: { builtin: pandoc-html }
YAML
  printf '# T\n\nx\n' > "$ROOT/T.md"
  # run with a PATH that has NO pandoc -> ToolAbsentError -> exit 3 rolled-back, NO T.html
  PATH=/usr/bin:/bin "$BIN" sync --root "$ROOT" needtool >/tmp/dc_e2e.out 2>&1; got=$?
  if command -v pandoc >/dev/null 2>&1 && [ -x /usr/bin/pandoc ]; then
    say "SKIP  tool-absent test — pandoc is in /usr/bin (cannot construct an absent-PATH); covered by unit test TestSync_ToolAbsent_HonestSkip"; skip=$((skip+1))
  elif [ "$got" = "3" ] && [ ! -f "$ROOT/T.html" ]; then
    say "PASS  tool-absent honest rollback (exit 3, no fake T.html written)"; pass=$((pass+1))
  else
    say "FAIL  tool-absent path (exit $got, T.html exists=$([ -f "$ROOT/T.html" ] && echo yes || echo no))"; sed 's/^/        /' /tmp/dc_e2e.out | tee -a "$LOG"; fail=$((fail+1))
  fi
else
  say "SKIP  tool-absent test — pandoc not installed (covered by unit test TestSync_ToolAbsent_HonestSkip)"; skip=$((skip+1))
fi

say ""
say "## evidence: every sync wrote a §11.4.69 qa-results artefact"
if [ -d "$ROOT/qa-results/docs_chain" ] && [ -n "$(ls -A "$ROOT/qa-results/docs_chain" 2>/dev/null)" ]; then
  say "PASS  qa-results evidence artefacts produced ($(ls "$ROOT/qa-results/docs_chain" | wc -l | tr -d ' ') run dir(s))"; pass=$((pass+1))
else
  say "FAIL  no qa-results evidence written"; fail=$((fail+1))
fi

say ""
say "===================================================="
say "Result: ${pass} PASS / ${fail} FAIL / ${skip} SKIP"
[ "$fail" -eq 0 ] && say "docs_chain e2e GREEN — all observable behaviour verified against the real binary." || say "docs_chain e2e RED."
exit "$fail"
