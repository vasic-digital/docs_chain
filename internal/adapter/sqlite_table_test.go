package adapter

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestMarkdownToSQLite_RoundTrip proves the generic md↔sqlite builtins are a
// real, deterministic, verify-stable bidirectional transform (not a stub).
func TestMarkdownToSQLite_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "data.db")

	src := `# preamble prose (NOT a table, must be dropped on round-trip)

## Issues

| ID | Title | Owner |
| --- | --- | --- |
| HRD-1 | first thing | alice |
| HRD-2 | has a \| pipe | bob |

some prose between tables

## Fixed

| ID | Done |
| :-- | :-: |
| HRD-9 | yes |
`

	md2db := MarkdownToSQLite(db)
	dump1, err := md2db(map[string][]byte{"src": []byte(src)})
	if err != nil {
		t.Fatalf("md-to-sqlite: %v", err)
	}
	if len(dump1) == 0 {
		t.Fatal("md-to-sqlite produced empty dump")
	}

	// The canonical dump must name BOTH tables (sorted) + their rows.
	ds := string(dump1)
	for _, want := range []string{"TABLE\tfixed\t", "TABLE\tissues\t", "ROW\tHRD-1\t", "ROW\tHRD-2\t", "has a | pipe", "ROW\tHRD-9\t"} {
		if !strings.Contains(ds, want) {
			t.Fatalf("dump missing %q:\n%s", want, ds)
		}
	}
	// Tables sorted ascending: fixed before issues.
	if strings.Index(ds, "TABLE\tfixed") > strings.Index(ds, "TABLE\tissues") {
		t.Fatalf("tables not sorted ascending:\n%s", ds)
	}

	// sqlite-to-md renders the tables back as normalized markdown.
	db2md := SQLiteToMarkdown(db)
	mdOut, err := db2md(nil)
	if err != nil {
		t.Fatalf("sqlite-to-md: %v", err)
	}
	mo := string(mdOut)
	for _, want := range []string{"## fixed", "## issues", "| ID | Title | Owner |", "| --- | --- | --- |", "| HRD-2 | has a \\| pipe | bob |", "| HRD-9 | yes |"} {
		if !strings.Contains(mo, want) {
			t.Fatalf("rendered md missing %q:\n%s", want, mo)
		}
	}
	// Prose preamble must NOT survive (documented contract — DB holds only tables).
	if strings.Contains(mo, "preamble prose") {
		t.Fatalf("rendered md leaked non-table prose:\n%s", mo)
	}

	// IDEMPOTENCE / VERIFY-STABILITY: md(out) -> db -> md == md(out) exactly,
	// and the second dump == the first (byte-stable, which is what `verify` relies on).
	db2 := filepath.Join(dir, "data2.db")
	dump2, err := MarkdownToSQLite(db2)(map[string][]byte{"src": mdOut})
	if err != nil {
		t.Fatalf("md-to-sqlite round-2: %v", err)
	}
	if !bytes.Equal(dump1, dump2) {
		t.Fatalf("dump not stable across round-trip:\n--- dump1 ---\n%s\n--- dump2 ---\n%s", dump1, dump2)
	}
	mdOut2, err := SQLiteToMarkdown(db2)(nil)
	if err != nil {
		t.Fatalf("sqlite-to-md round-2: %v", err)
	}
	if !bytes.Equal(mdOut, mdOut2) {
		t.Fatalf("markdown not stable across round-trip:\n--- 1 ---\n%s\n--- 2 ---\n%s", mdOut, mdOut2)
	}

	// DETERMINISM at -count style: re-derive the dump a third time, identical.
	dump3, err := MarkdownToSQLite(filepath.Join(dir, "data3.db"))(map[string][]byte{"src": mdOut})
	if err != nil {
		t.Fatalf("md-to-sqlite round-3: %v", err)
	}
	if !bytes.Equal(dump1, dump3) {
		t.Fatal("dump non-deterministic across three derivations")
	}
}

// TestSQLiteToMarkdown_EmptyDB confirms an empty/fresh DB yields empty output
// (honest, not a crash) — the natural "dirty vs empty" state for a fresh node.
func TestSQLiteToMarkdown_EmptyDB(t *testing.T) {
	dir := t.TempDir()
	out, err := SQLiteToMarkdown(filepath.Join(dir, "nope.db"))(nil)
	if err != nil {
		t.Fatalf("sqlite-to-md on missing db should be empty not error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty output for empty db, got %q", out)
	}
}
