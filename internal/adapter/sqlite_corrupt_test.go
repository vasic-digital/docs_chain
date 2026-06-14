package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSQLiteRead_CorruptDBSurfacesError is a reproduce-first regression guard
// for the silent-corruption bug: a SQLite node file that EXISTS but is not a
// readable/valid database (corruption, partial write, wrong-format bytes) MUST
// surface an error from the dump path — NEVER be silently reported as an EMPTY
// database. Conflating "corrupt/unreadable" with "fresh/empty" is a §11.4.6
// (no-guessing) + §11.4.93 (DB-as-SSoT integrity) PASS-bluff: a sync would then
// treat the corrupt DB as a brand-new node and DROP+recreate it (data loss),
// and a verify would report a false in-sync/stale verdict on a broken SSoT.
//
// Distinct from a genuinely MISSING file (no such path), which legitimately maps
// to empty content (the natural "dirty-vs-empty" cold-start of a fresh node) —
// see TestSQLiteRead_MissingDBIsEmpty below, which MUST keep passing.
func TestSQLiteRead_CorruptDBSurfacesError(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "data.db")

	// First write a real, valid table so the path is unambiguously a DB node.
	if _, err := MarkdownToSQLite(db)(map[string][]byte{"s": []byte("## T\n| A |\n| - |\n| x |\n")}); err != nil {
		t.Fatalf("setup md-to-sqlite: %v", err)
	}

	// Now corrupt the on-disk file (simulate a torn write / disk damage / wrong
	// bytes). The file EXISTS but is not a valid SQLite database.
	if err := os.WriteFile(db, []byte("NOT A SQLITE FILE \x00\x01\x02 garbage bytes"), 0o644); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}

	// canonicalDumpAllTables MUST report the corruption, not pretend zero tables.
	dump, derr := canonicalDumpAllTables(db)
	if derr == nil {
		t.Fatalf("canonicalDumpAllTables on a CORRUPT db returned no error (silent bluff); dump=%q", dump)
	}

	// The adapter Read() MUST likewise surface the error rather than returning
	// empty content (which the engine would treat as a fresh/empty node).
	_, rerr := NewSQLiteRowDumpAdapter(db).Read()
	if rerr == nil {
		t.Fatalf("SQLiteRowDumpAdapter.Read() on a CORRUPT db returned nil error (silent bluff): corruption indistinguishable from empty")
	}
}

// TestSQLiteRead_MissingDBIsEmpty pins the legitimate behaviour the fix must
// PRESERVE: a path that does not exist at all maps to empty content with no
// error (cold-start of a fresh derived node).
func TestSQLiteRead_MissingDBIsEmpty(t *testing.T) {
	dir := t.TempDir()
	b, err := NewSQLiteRowDumpAdapter(filepath.Join(dir, "never.db")).Read()
	if err != nil {
		t.Fatalf("missing DB must read as empty with no error, got err=%v", err)
	}
	if len(b) != 0 {
		t.Fatalf("missing DB must read as empty, got %q", b)
	}
}
