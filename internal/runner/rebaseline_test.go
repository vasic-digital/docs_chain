package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"digital.vasic.docs_chain/internal/adapter"
	"digital.vasic.docs_chain/internal/orchestrator"
	"digital.vasic.docs_chain/internal/state"
)

// fileSHA returns the sha256 of a file's RAW bytes (the unforgeable proof a
// binary artefact was not touched — independent of any text normalization).
func fileSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// buildSyncFixture writes a sync-edge context whose AUTHORITY is the SQLite DB
// (db), regenerating the markdown view (src) from it via sqlite-to-md
// (db-to-md), and (dangerously) able to regenerate the DB from the markdown via
// md-to-sqlite (md-to-db). It seeds src.md + db so they are byte-consistent,
// then returns the loaded context + the live DB path + its pre-run raw sha.
func buildSyncFixture(t *testing.T, root string) (ctxName, dbPath, dbSHA0 string) {
	t.Helper()
	mdPath := filepath.Join(root, "data", "src.md")
	if err := os.MkdirAll(filepath.Dir(mdPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// The canonical rendered form produced by sqlite-to-md (so the round-trip is
	// byte-stable and src is already "in sync" with the DB after the first sync).
	src := "## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n| 2 | beta |\n"
	if err := os.WriteFile(mdPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// authority: db. transform_b_to_a (B=db is authority -> regenerate A=src
	// from B) = sqlite-to-md (the SAFE db-to-md direction). transform_a_to_b
	// (src->db) = md-to-sqlite (the DANGEROUS md-to-db direction we must NEVER
	// invoke in re-baseline).
	yaml := `
context: sync_fixture
nodes:
  src: { kind: markdown, path: data/src.md }
  db:  { kind: sqlite,   path: data/items.db }
edges:
  - { type: sync, a: src, b: db, authority: db,
      transform_a_to_b: m2d, transform_b_to_a: d2m }
transforms:
  m2d: { builtin: md-to-sqlite }
  d2m: { builtin: sqlite-to-md }
`
	c := writeContext(t, root, "sync_fixture", yaml)

	// Seed the DB once via the md-to-sqlite builtin directly (NOT through
	// re-baseline) so the DB exists and is byte-consistent with src before we
	// test that re-baseline never writes it.
	dbPath = filepath.Join(root, "data", "items.db")
	seed := adapter.MarkdownToSQLite(dbPath)
	if _, err := seed(map[string][]byte{"src": []byte(src)}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	if fi, err := os.Stat(dbPath); err != nil || fi.Size() == 0 {
		t.Fatalf("seed db not produced (err=%v)", err)
	}
	return c.Name, dbPath, fileSHA(t, dbPath)
}

// TestReBaseline_NeverWritesAuthorityDB is the SAFETY proof: re-baselining a
// sync edge whose authority is the DB regenerates ONLY the non-authority
// markdown side and leaves the authority DB file BYTE-IDENTICAL (raw-sha
// unchanged). It also asserts the DB node is reported as a write-guarded
// (protected) node and never appears in the committed list.
func TestReBaseline_NeverWritesAuthorityDB(t *testing.T) {
	root := t.TempDir()
	_, dbPath, dbSHA0 := buildSyncFixture(t, root)

	c := writeContext(t, root, "sync_fixture", mustReadContext(t, root, "sync_fixture"))
	st := state.New()
	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	res, err := prep.ReBaseline(st)
	if err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	if res.Status != orchestrator.StatusCommitted && res.Status != orchestrator.StatusInSync {
		t.Fatalf("rebaseline status=%s want committed/in-sync (err=%v)", res.Status, res.Err)
	}

	// THE PASS/FAIL LINE: the authority DB file's RAW bytes are unchanged.
	dbSHA1 := fileSHA(t, dbPath)
	if dbSHA1 != dbSHA0 {
		t.Fatalf("AUTHORITY DB WAS WRITTEN by re-baseline — sha %s -> %s (MUST be byte-identical)", dbSHA0, dbSHA1)
	}
	t.Logf("EVIDENCE: authority DB raw-sha unchanged after rebaseline: %s", dbSHA1)

	// The DB node is reported as write-guarded (read-only) and NEVER committed.
	if !contains(res.ProtectedNodes, "db") {
		t.Fatalf("db not reported as a protected (write-guarded) node: %v", res.ProtectedNodes)
	}
	if contains(res.Committed, "db") {
		t.Fatalf("db appears in the committed list — it MUST never be written: %v", res.Committed)
	}
	if !contains(res.Regenerated, "src") {
		t.Fatalf("src (non-authority) not reported regenerated: %v", res.Regenerated)
	}
	t.Logf("EVIDENCE: protected=%v regenerated=%v committed=%v", res.ProtectedNodes, res.Regenerated, res.Committed)

	// A subsequent plain verify is now conflict-free (the derive-from chain is
	// empty here, so verify is trivially in-sync; the key assertion above is the
	// untouched DB). The src markdown is still the canonical rendered form.
	srcAfter, _ := os.ReadFile(filepath.Join(root, "data", "src.md"))
	for _, want := range []string{"| 1 | alpha |", "| 2 | beta |"} {
		if !strings.Contains(string(srcAfter), want) {
			t.Fatalf("regenerated src missing %q:\n%s", want, srcAfter)
		}
	}
}

// TestReBaseline_WriteGuardRejectsAuthorityWrite proves the guard is
// STRUCTURAL, not incidental: even when a (deliberately mis-bound) transform
// tries to Set the authority node, the writeGuardStore returns
// *ProtectedWriteError and the run rolls back with the DB byte-identical. This
// is the §1.1 paired-mutation: a transform that attempts the forbidden write
// MUST be refused, never silently allowed.
func TestReBaseline_WriteGuardRejectsAuthorityWrite(t *testing.T) {
	root := t.TempDir()
	_, dbPath, dbSHA0 := buildSyncFixture(t, root)

	c := writeContext(t, root, "sync_fixture", mustReadContext(t, root, "sync_fixture"))
	st := state.New()
	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	// Directly exercise the guard with a malicious write to the authority node.
	guard := newWriteGuardStore(prep.Store, map[string]bool{"db": true})
	err = guard.Set("db", []byte("CORRUPTION ATTEMPT — this must never reach disk"))
	var pwe *ProtectedWriteError
	if err == nil {
		t.Fatal("write-guard ALLOWED a write to the authority DB node (MUST be refused)")
	}
	if !errorsAs(err, &pwe) {
		t.Fatalf("expected *ProtectedWriteError, got %T: %v", err, err)
	}
	// The DB on disk is untouched (the guard refused before any Write).
	if got := fileSHA(t, dbPath); got != dbSHA0 {
		t.Fatalf("DB raw-sha changed despite guard refusal: %s -> %s", dbSHA0, got)
	}
	t.Logf("EVIDENCE: guard refused authority write (%v); DB raw-sha unchanged: %s", err, dbSHA0)

	// And a non-authority write through the SAME guard DOES go through (the
	// guard is selective, not a blanket read-only).
	if werr := guard.Set("src", []byte("## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n| 2 | beta |\n")); werr != nil {
		t.Fatalf("guard wrongly refused a NON-authority write: %v", werr)
	}
	t.Logf("EVIDENCE: guard permits the non-authority (src) write — selective, not blanket")
}

// TestReBaseline_NeverExecutesSyncTransform is the strongest safety proof: the
// real authority->non-authority transform (e.g. the project's `workable-items`
// db-to-md) mutates the authority file as a SIDE EFFECT of opening it
// read-write — a write the store-level guard CANNOT intercept. ReBaseline's
// safety therefore rests on NEVER EXECUTING that transform at all. This test
// binds an authority->non-authority EXEC transform that, if ever run, writes a
// SENTINEL into the authority file (mimicking the side-effecting real tool), and
// asserts the sentinel NEVER appears — i.e. the transform was never executed and
// the authority file is byte-identical. The paired §1.1 mutation: if a future
// edit re-introduces running the sync transform, this sentinel WILL appear and
// the test FAILs.
func TestReBaseline_NeverExecutesSyncTransform(t *testing.T) {
	root := t.TempDir()
	mdPath := filepath.Join(root, "data", "src.md")
	dbPath := filepath.Join(root, "data", "items.db")
	if err := os.MkdirAll(filepath.Dir(mdPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte("## items\n\nseed markdown\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The authority "DB" is a plain file here (kind: sqlite is not required to
	// prove the no-execute property — the property is about the transform never
	// running). Seed it with known bytes.
	authBytes := []byte("AUTHORITY-ORIGINAL-CONTENT-DO-NOT-MUTATE\n")
	if err := os.WriteFile(dbPath, authBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	dbSHA0 := fileSHA(t, dbPath)

	// A side-effecting "db-to-md" script: it APPENDS a sentinel to the AUTHORITY
	// file (its --db side effect), exactly like a real RW DB open would, then
	// writes the engine output file. If ReBaseline ever runs it, the sentinel
	// lands in the authority file and dbSHA changes.
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "bin", "side_effecting_db_to_md.sh")
	scriptBody := "#!/usr/bin/env bash\nset -e\n" +
		"# Mimic the real tool's RW side effect on the authority file:\n" +
		"printf 'SENTINEL-TRANSFORM-WAS-EXECUTED\\n' >> '" + dbPath + "'\n" +
		"# Last positional is the engine output file:\n" +
		"out=\"${@: -1}\"\n" +
		"printf '## items\\n\\nregenerated\\n' > \"$out\"\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}

	// Path-bearing exec ref (contains a separator) so it resolves relative to the
	// project root — making the mutation proof SHARP: if the transform were ever
	// run, the sentinel WOULD land in the authority file (not merely fail to
	// resolve on PATH).
	yaml := `
context: sync_sideeffect
nodes:
  src: { kind: markdown, path: data/src.md }
  db:  { kind: markdown, path: data/items.db }
edges:
  - { type: sync, a: src, b: db, authority: db,
      transform_b_to_a: d2m }
transforms:
  d2m: { exec: "bin/side_effecting_db_to_md.sh" }
`
	c := writeContext(t, root, "sync_sideeffect", yaml)
	st := state.New()
	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	res, err := prep.ReBaseline(st)
	if err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	if res.Status != orchestrator.StatusCommitted && res.Status != orchestrator.StatusInSync {
		t.Fatalf("rebaseline status=%s (err=%v)", res.Status, res.Err)
	}

	// THE PROOF: the side-effecting transform was NEVER executed.
	authAfter, _ := os.ReadFile(dbPath)
	if strings.Contains(string(authAfter), "SENTINEL-TRANSFORM-WAS-EXECUTED") {
		t.Fatalf("the sync transform WAS executed (sentinel present) — ReBaseline MUST never run it:\n%s", authAfter)
	}
	if got := fileSHA(t, dbPath); got != dbSHA0 {
		t.Fatalf("authority file raw-sha changed %s -> %s (transform side-effect leaked)", dbSHA0, got)
	}
	t.Logf("EVIDENCE: sync transform NEVER executed; authority file raw-sha unchanged: %s", dbSHA0)
}

// --- small local helpers (kept test-local to avoid touching production code) --

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// errorsAs is a tiny local wrapper so the test reads cleanly.
func errorsAs(err error, target interface{}) bool {
	return errors.As(err, target)
}

// mustReadContext re-reads the on-disk context YAML so each test gets a fresh
// *config.Context (buildSyncFixture already wrote it; this returns its text).
func mustReadContext(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".docs_chain", "contexts", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
