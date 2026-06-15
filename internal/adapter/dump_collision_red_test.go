package adapter

import (
	"path/filepath"
	"testing"

	"digital.vasic.docs_chain/internal/hash"
)

// TestCanonicalDump_NoCollisionAcrossDistinctRowStates proves the SSoT content
// hash actually distinguishes DISTINCT logical table states. The canonical dump
// is the HASHED content of a SQLite SSoT node (§11.4.86 change detection /
// §11.4.93 DB-as-SSoT). If two genuinely different row states serialize to the
// same dump bytes, their content hashes COLLIDE -> a real change to the DB is
// undetectable: a `verify` falsely reports in-sync on stale, a `sync` misses
// the change. That is a §11.4.6 change-detection PASS-bluff.
//
// The collision is driven by un-escaped TAB/NEWLINE field/row separators in
// canonicalDumpAllTables: a cell value containing a TAB is indistinguishable
// from a column boundary.
func TestCanonicalDump_NoCollisionAcrossDistinctRowStates(t *testing.T) {
	dir := t.TempDir()
	h := hash.NewByteContentHasher()

	// State A: one table, one row, two columns: ["a\tb", "c"].
	dbA := filepath.Join(dir, "a.db")
	if err := writeTablesToSQLite(dbA, []mdTable{{
		name: "t",
		cols: []string{"c1", "c2"},
		rows: [][]string{{"a\tb", "c"}},
	}}); err != nil {
		t.Fatalf("write A: %v", err)
	}
	dumpA, err := canonicalDumpAllTables(dbA)
	if err != nil {
		t.Fatalf("dump A: %v", err)
	}

	// State B: SAME schema, DIFFERENT row values: ["a", "b\tc"].
	// This is a genuinely different logical DB state.
	dbB := filepath.Join(dir, "b.db")
	if err := writeTablesToSQLite(dbB, []mdTable{{
		name: "t",
		cols: []string{"c1", "c2"},
		rows: [][]string{{"a", "b\tc"}},
	}}); err != nil {
		t.Fatalf("write B: %v", err)
	}
	dumpB, err := canonicalDumpAllTables(dbB)
	if err != nil {
		t.Fatalf("dump B: %v", err)
	}

	if h.Hash(dumpA) == h.Hash(dumpB) {
		t.Fatalf("COLLISION: distinct row states share a content hash\nA=%q (%q)\nB=%q (%q)",
			string(dumpA), h.Hash(dumpA), string(dumpB), h.Hash(dumpB))
	}
}
