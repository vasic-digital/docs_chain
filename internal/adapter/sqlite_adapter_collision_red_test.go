package adapter

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestSQLiteAdapter_NoCollisionAcrossDistinctRowStates proves the
// SQLiteAdapter (the caller-supplied-DumpQuery adapter documented as "the
// documented query hook" in docs/ARCHITECTURE.md §9, exported via
// NewSQLiteAdapter/SQLiteConfig and exercised end-to-end by
// TestSQLiteAdapter_CanonicalDumpDeterminism etc. in adapter_test.go) actually
// distinguishes DISTINCT logical row states through its content hash.
//
// Read's dump format joins a row's columns with "\t" and rows with "\n"
// (see the Read doc comment). Before this fix, a non-NULL cell was emitted
// VERBATIM (string(rb)) with no escaping, so a literal TAB byte inside a cell
// value is indistinguishable from the field separator the dump format itself
// uses. That is exactly the collision class already found, fixed, and
// regression-tested for canonicalDumpAllTables in sqlite_table.go (see
// dump_collision_red_test.go) — but this twin implementation was left
// unfixed.
//
// Concretely: state A has one row with columns ["a\tb", "c"]; state B has one
// row with columns ["a", "b\tc"]. Both are genuinely different logical DB
// states, yet the unescaped dump for BOTH is the literal bytes "a\tb\tc\n" —
// an identical dump, hence an identical content hash. A `verify` run would
// then falsely report state B as in-sync against a baseline recorded from
// state A (or vice versa), and a `sync` run would silently miss the change —
// the §11.4.6 / §11.4.86 change-detection PASS-bluff this engine's whole
// content-hash design exists to prevent.
func TestSQLiteAdapter_NoCollisionAcrossDistinctRowStates(t *testing.T) {
	dir := t.TempDir()

	mk := func(t *testing.T, name, c1, c2 string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE t (c1 TEXT, c2 TEXT);`); err != nil {
			t.Fatalf("create table %s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO t (c1, c2) VALUES (?, ?);`, c1, c2); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		return path
	}

	// State A: one row, two columns: ["a\tb", "c"].
	dbA := mk(t, "a.db", "a\tb", "c")
	// State B: SAME schema, DIFFERENT row values: ["a", "b\tc"].
	dbB := mk(t, "b.db", "a", "b\tc")

	q := "SELECT c1, c2 FROM t ORDER BY rowid;"
	adA, err := NewSQLiteAdapter(SQLiteConfig{DSN: dbA, DumpQuery: q})
	if err != nil {
		t.Fatal(err)
	}
	adB, err := NewSQLiteAdapter(SQLiteConfig{DSN: dbB, DumpQuery: q})
	if err != nil {
		t.Fatal(err)
	}

	cA, err := adA.Read()
	if err != nil {
		t.Fatalf("read a: %v", err)
	}
	cB, err := adB.Read()
	if err != nil {
		t.Fatalf("read b: %v", err)
	}

	// Positive evidence: both dumps genuinely carry the expected content.
	if !contains(string(cA), "a") || !contains(string(cA), "b") || !contains(string(cA), "c") {
		t.Fatalf("dump A missing expected content: %q", cA)
	}
	if !contains(string(cB), "a") || !contains(string(cB), "b") || !contains(string(cB), "c") {
		t.Fatalf("dump B missing expected content: %q", cB)
	}

	if adA.Hasher().Hash(cA) == adB.Hasher().Hash(cB) {
		t.Fatalf("COLLISION: distinct row states share a content hash\nA=%q (%q)\nB=%q (%q)",
			string(cA), adA.Hasher().Hash(cA), string(cB), adB.Hasher().Hash(cB))
	}
}
