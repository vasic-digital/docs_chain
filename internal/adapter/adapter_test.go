package adapter

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"digital.vasic.docs_chain/internal/graph"
)

// --- markdown FileAdapter round-trip -------------------------------------

func TestMarkdownAdapter_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Issues.md")
	a := NewMarkdownAdapter(p)

	// Missing file -> (nil, nil) "never written".
	got, err := a.Read()
	if err != nil {
		t.Fatalf("read missing: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing file, got %q", got)
	}

	body := []byte("# Issues\n\nContent line.\n")
	if err := a.Write(body); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err = a.Read()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("round-trip mismatch: wrote %q read %q", body, got)
	}
	if a.Kind() != graph.KindMarkdown {
		t.Fatalf("kind = %q want markdown", a.Kind())
	}

	// Hash is stable + whitespace-normalized: trailing spaces / CRLF must
	// collide with the canonical form.
	h := a.Hasher()
	canon := h.Hash([]byte("# Issues\n\nContent line.\n"))
	noisy := h.Hash([]byte("# Issues  \r\n\r\nContent line.   \r\n"))
	if canon != noisy {
		t.Fatalf("normalized hashes must collide: %s vs %s", canon, noisy)
	}
}

// --- pandoc md->html (SKIP-with-reason if absent, never fake) ------------

func TestPandocMarkdownToHTML(t *testing.T) {
	if _, err := exec.LookPath("pandoc"); err != nil {
		t.Skip("SKIP-with-reason: pandoc not installed — derived html transform unverifiable (§11.4.6, never faked)")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "Doc.html")
	tf := PandocMarkdownToHTML(out)
	produced, err := tf(map[string][]byte{"src": []byte("# Title\n\nHello **world**.\n")})
	if err != nil {
		t.Fatalf("pandoc transform: %v", err)
	}
	if len(produced) == 0 {
		t.Fatal("pandoc produced empty html")
	}
	onDisk, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read produced html: %v", err)
	}
	if string(onDisk) != string(produced) {
		t.Fatal("returned bytes must equal produced file bytes")
	}
	// Positive evidence: the rendered HTML contains the heading text and the
	// bold markup pandoc emits.
	s := string(produced)
	if !contains(s, "Title") || !contains(s, "<strong>world</strong>") {
		t.Fatalf("html missing expected rendered content:\n%s", s)
	}
}

func TestPandocMarkdownToDOCX(t *testing.T) {
	if _, err := exec.LookPath("pandoc"); err != nil {
		t.Skip("SKIP-with-reason: pandoc not installed — derived docx transform unverifiable (§11.4.6, never faked)")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "Doc.docx")
	tf := PandocMarkdownToDOCX(out)
	produced, err := tf(map[string][]byte{"src": []byte("# Title\n\nHello.\n")})
	if err != nil {
		t.Fatalf("pandoc docx transform: %v", err)
	}
	// DOCX is a zip container; its first bytes are the PK signature.
	if len(produced) < 4 || produced[0] != 'P' || produced[1] != 'K' {
		t.Fatalf("docx output is not a zip container (PK header), got %d bytes", len(produced))
	}
}

func TestWeasyprintHTMLToPDF(t *testing.T) {
	if _, err := exec.LookPath("weasyprint"); err != nil {
		t.Skip("SKIP-with-reason: weasyprint not installed — derived pdf transform unverifiable (§11.4.6, never faked)")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "Doc.pdf")
	tf := WeasyprintHTMLToPDF(out)
	produced, err := tf(map[string][]byte{"src": []byte("<html><body><h1>Title</h1></body></html>")})
	if err != nil {
		t.Fatalf("weasyprint transform: %v", err)
	}
	// PDF files start with "%PDF-".
	if len(produced) < 5 || string(produced[:5]) != "%PDF-" {
		t.Fatalf("pdf output missing %%PDF- header, got %d bytes", len(produced))
	}
}

// ToolAbsentError must be a typed, matchable error — a transform that fails
// because a tool is missing reports it WITHOUT writing output, and callers
// detect it via IsToolAbsent (so they SKIP, never fake success).
func TestToolAbsentError_Typed(t *testing.T) {
	err := &ToolAbsentError{Tool: "pandoc"}
	if !IsToolAbsent(err) {
		t.Fatal("IsToolAbsent must match a *ToolAbsentError")
	}
	wrapped := errors.Join(errors.New("context"), err)
	if !IsToolAbsent(wrapped) {
		t.Fatal("IsToolAbsent must match a wrapped *ToolAbsentError")
	}
	if IsToolAbsent(errors.New("unrelated")) {
		t.Fatal("IsToolAbsent must not match unrelated errors")
	}
}

// --- sqlite canonical-dump determinism -----------------------------------

// seedRows inserts (id, title) rows into a fresh DB at path in the given
// order, returning nothing — the test asserts the dump is order-independent.
func seedRows(t *testing.T, path string, rows [][2]string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items (id TEXT PRIMARY KEY, title TEXT);`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO items (id, title) VALUES (?, ?);`, r[0], r[1]); err != nil {
			t.Fatalf("insert %v: %v", r, err)
		}
	}
}

func TestSQLiteAdapter_CanonicalDumpDeterminism(t *testing.T) {
	dir := t.TempDir()
	dbA := filepath.Join(dir, "a.db")
	dbB := filepath.Join(dir, "b.db")

	// Same SET of rows, inserted in DIFFERENT order.
	seedRows(t, dbA, [][2]string{{"3", "gamma"}, {"1", "alpha"}, {"2", "beta"}})
	seedRows(t, dbB, [][2]string{{"1", "alpha"}, {"2", "beta"}, {"3", "gamma"}})

	q := `SELECT id, title FROM items ORDER BY id;`
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
	if string(cA) != string(cB) {
		t.Fatalf("dumps differ for identical row sets:\nA=%q\nB=%q", cA, cB)
	}
	// And the per-kind hash collides — the whole point of dumping rows (not
	// raw .db bytes) is order/page-layout independence.
	if adA.Hasher().Hash(cA) != adB.Hasher().Hash(cB) {
		t.Fatal("hashes must collide for identical row sets")
	}
	if adA.Kind() != graph.KindSQLite {
		t.Fatalf("kind = %q want sqlite", adA.Kind())
	}
	// Positive content evidence.
	if !contains(string(cA), "alpha") || !contains(string(cA), "gamma") {
		t.Fatalf("dump missing expected rows: %q", cA)
	}

	// DumpQuery is mandatory.
	if _, err := NewSQLiteAdapter(SQLiteConfig{DSN: dbA, DumpQuery: "  "}); err == nil {
		t.Fatal("expected error for empty DumpQuery")
	}
}

func TestSQLiteAdapter_WriteRequiresApply(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "ro.db")
	seedRows(t, db, [][2]string{{"1", "x"}})
	ad, err := NewSQLiteAdapter(SQLiteConfig{DSN: db, DumpQuery: "SELECT id FROM items ORDER BY id;"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Write([]byte("anything")); err == nil {
		t.Fatal("read-only sqlite node (no Apply) must reject Write")
	}
}

func TestSQLiteAdapter_WriteApplyTransaction(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "rw.db")
	seedRows(t, db, [][2]string{{"1", "old"}})
	q := "SELECT id, title FROM items ORDER BY id;"
	ad, err := NewSQLiteAdapter(SQLiteConfig{
		DSN:       db,
		DumpQuery: q,
		Apply: func(tx *sql.Tx, content []byte) error {
			_, e := tx.Exec(`UPDATE items SET title = ? WHERE id = '1';`, string(content))
			return e
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Write([]byte("new")); err != nil {
		t.Fatalf("apply write: %v", err)
	}
	got, err := ad.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(got), "new") {
		t.Fatalf("apply did not persist: %q", got)
	}
}

// --- FileStore as a graph.Store -------------------------------------------

func TestFileStore_GetSet(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "n.md")
	fs := NewFileStore()
	if err := fs.Register("n", NewMarkdownAdapter(p)); err != nil {
		t.Fatal(err)
	}
	if err := fs.Register("n", NewMarkdownAdapter(p)); err == nil {
		t.Fatal("duplicate register must error")
	}
	if _, err := fs.Get("absent"); err == nil {
		t.Fatal("Get on unregistered node must error")
	}
	if err := fs.Set("n", []byte("data\n")); err != nil {
		t.Fatal(err)
	}
	got, err := fs.Get("n")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data\n" {
		t.Fatalf("got %q", got)
	}
	if h, err := fs.Hasher("n"); err != nil || h == nil {
		t.Fatalf("hasher: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
