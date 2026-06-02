package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"digital.vasic.docs_chain/internal/orchestrator"
	"digital.vasic.docs_chain/internal/state"
)

// TestSync_BidirectionalSQLiteBuiltins proves the md-to-sqlite + sqlite-to-md
// builtins are wired through the REAL sync/verify pipeline (not just the
// adapter funcs): a markdown data table derives a SQLite DB, the DB derives a
// rendered markdown view, sync commits both, verify is in-sync (exit 0), and a
// source edit is detected as STALE. Pure-Go (modernc sqlite) — no external tool,
// so this never SKIPs.
func TestSync_BidirectionalSQLiteBuiltins(t *testing.T) {
	root := t.TempDir()
	mdPath := filepath.Join(root, "data", "items.md")
	if err := os.MkdirAll(filepath.Dir(mdPath), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n| 2 | beta |\n"
	if err := os.WriteFile(mdPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	yaml := `
context: db
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
`
	c := writeContext(t, root, "db", yaml)
	st := state.New()

	// --- sync: both derived nodes committed.
	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	res, err := prep.RunSync(st)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Status != orchestrator.StatusCommitted {
		t.Fatalf("sync status=%s want committed (err=%v)", res.Status, res.Err)
	}

	// Real evidence: a non-empty .db file exists on disk.
	dbFi, err := os.Stat(filepath.Join(root, "data", "items.db"))
	if err != nil || dbFi.Size() == 0 {
		t.Fatalf("db not produced (size=%d err=%v)", dbFi.Size(), err)
	}
	// The rendered view holds the real rows in normalized form.
	view, err := os.ReadFile(filepath.Join(root, "data", "items.view.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## items", "| id | name |", "| --- | --- |", "| 1 | alpha |", "| 2 | beta |"} {
		if !strings.Contains(string(view), want) {
			t.Fatalf("rendered view missing %q:\n%s", want, view)
		}
	}
	t.Logf("EVIDENCE: md->sqlite->md sync committed; db=%dB; view=\n%s", dbFi.Size(), view)

	// --- verify is in-sync (exit 0): the bidirectional builtins re-derive byte-stable.
	prep2, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	vr, err := prep2.Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(vr.Stale) != 0 {
		t.Fatalf("verify stale=%v, want none after sync", vr.Stale)
	}
	t.Logf("EVIDENCE: verify in-sync after sync (stale=%v)", vr.Stale)

	// --- edit the source -> verify must detect the DB (and the view) as STALE.
	src2 := "## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n| 2 | beta |\n| 3 | gamma |\n"
	if err := os.WriteFile(mdPath, []byte(src2), 0o644); err != nil {
		t.Fatal(err)
	}
	prep3, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	vr3, err := prep3.Verify()
	if err != nil {
		t.Fatalf("verify3: %v", err)
	}
	if len(vr3.Stale) == 0 {
		t.Fatal("verify3 reported in-sync after a source edit — drift NOT detected (bluff)")
	}
	t.Logf("EVIDENCE: source edit detected as STALE: %v", vr3.Stale)

	// --- re-sync brings it back in-sync (exit 0) and the new row lands in the view.
	if _, err := prep3.RunSync(st); err != nil {
		t.Fatalf("resync: %v", err)
	}
	prep4, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	vr4, err := prep4.Verify()
	if err != nil {
		t.Fatalf("verify4: %v", err)
	}
	if len(vr4.Stale) != 0 {
		t.Fatalf("verify4 stale=%v, want none after resync", vr4.Stale)
	}
	view2, _ := os.ReadFile(filepath.Join(root, "data", "items.view.md"))
	if !strings.Contains(string(view2), "| 3 | gamma |") {
		t.Fatalf("resync did not propagate the new row to the view:\n%s", view2)
	}
	t.Logf("EVIDENCE: resync propagated the new row + verify back in-sync")
}
