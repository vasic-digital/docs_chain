package adapter

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"digital.vasic.docs_chain/internal/graph"
	"digital.vasic.docs_chain/internal/hash"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// SQLiteRowDumpAdapter backs a sqlite node whose hashed content is the FULL
// canonical schema+rows dump (canonicalDumpAllTables) — so ROW changes drive
// drift detection, not just schema changes. Writes are a deliberate no-op: the
// bound transform (the md-to-sqlite builtin, or an exec md-to-db) mutates the
// .db file directly and the engine re-reads the dump to confirm (DESIGN §8/§9).
type SQLiteRowDumpAdapter struct {
	path   string
	hasher hash.Hasher
}

// NewSQLiteRowDumpAdapter constructs a full schema+rows sqlite node adapter.
func NewSQLiteRowDumpAdapter(path string) *SQLiteRowDumpAdapter {
	return &SQLiteRowDumpAdapter{path: path, hasher: hash.NewByteContentHasher()}
}

// Kind reports the sqlite node kind.
func (a *SQLiteRowDumpAdapter) Kind() graph.NodeKind { return graph.KindSQLite }

// Hasher returns the byte-content hasher over the canonical dump.
func (a *SQLiteRowDumpAdapter) Hasher() hash.Hasher { return a.hasher }

// Read returns the canonical schema+rows dump. A genuinely MISSING DB file
// yields empty content (the natural "dirty vs empty" state for a fresh derived
// node). A file that EXISTS but cannot be read as a valid database (corruption,
// torn write, lock, permission) surfaces the error rather than masquerading as
// an empty DB — conflating "corrupt" with "fresh/empty" is a §11.4.6 /
// §11.4.93 silent-corruption bluff (a sync would DROP+recreate the broken SSoT,
// a verify would emit a false verdict). See TestSQLiteRead_CorruptDBSurfacesError.
func (a *SQLiteRowDumpAdapter) Read() ([]byte, error) {
	if _, statErr := os.Stat(a.path); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, nil // genuinely missing -> empty (cold-start fresh node)
		}
		return nil, fmt.Errorf("sqlite read: stat %q: %w", a.path, statErr)
	}
	b, err := canonicalDumpAllTables(a.path)
	if err != nil {
		return nil, fmt.Errorf("sqlite read: %q exists but is not a readable database: %w", a.path, err)
	}
	return b, nil
}

// Write is a no-op: the bound transform owns the .db mutation; the engine
// confirms by re-reading the dump.
func (a *SQLiteRowDumpAdapter) Write(_ []byte) error { return nil }

// Generic bidirectional Markdown-data-table ↔ SQLite transforms backing the
// `md-to-sqlite` / `sqlite-to-md` builtins (§11.4.106 "document-AND-database"
// engine; §11.4.93 DB-as-SSoT). Pure Go — no external `sqlite3` binary, no cgo.
//
// CONTRACT (anti-bluff — do NOT overclaim): these operate on the TABULAR
// PROJECTION of a Markdown document. Every GitHub-flavoured pipe table becomes
// one SQLite table; the table's name is the nearest preceding `#`/`##`/`###…`
// heading (slugified), or `table_N` (1-based) when no heading precedes it. All
// columns are TEXT. Prose OUTSIDE tables is NOT represented in the database and
// is therefore NOT reproduced by `sqlite-to-md` — for prose+metadata documents
// (e.g. an HRD tracker) use an `exec:` transform (the workable-items tool), not
// these generic builtins. Within that contract the round-trip is byte-stable:
// `sqlite-to-md` emits a normalized form (single-space cell padding, `---`
// separators, blank line after each heading) so a post-sync `verify` exit 0s.

var (
	// tableSep matches a GFM table separator row, e.g. `| --- | :--: |`.
	tableSep = regexp.MustCompile(`^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$`)
	// headingLine matches an ATX heading, capturing the text.
	headingLine = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.+?)\s*#*\s*$`)
	// nonIdent collapses runs of non-identifier chars into a single `_`.
	nonIdent = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// mdTable is one parsed pipe table.
type mdTable struct {
	name string
	cols []string
	rows [][]string
}

// MarkdownToSQLite returns the `md-to-sqlite` builtin transform. It parses the
// Markdown input's pipe tables and (re)writes them into the SQLite database at
// outPath inside a single transaction (drop-and-recreate each table for
// idempotent determinism), then returns the canonical dump bytes (matching
// SQLiteAdapter.Read's serialization) so the engine's content hash of the DB
// node is a pure function of logical table state.
func MarkdownToSQLite(outPath string) func(ins map[string][]byte) ([]byte, error) {
	return func(ins map[string][]byte) ([]byte, error) {
		md := concatSortedInputs(ins)
		tables := parseMarkdownTables(md)
		if err := writeTablesToSQLite(outPath, tables); err != nil {
			return nil, err
		}
		return canonicalDumpAllTables(outPath)
	}
}

// SQLiteToMarkdown returns the `sqlite-to-md` builtin transform. It reads every
// user table from the SOURCE database at srcDBPath (NOT outPath — the source is
// never rebound by Verify) and renders them as normalized Markdown. The
// returned bytes are written to the markdown target by its adapter.
func SQLiteToMarkdown(srcDBPath string) func(ins map[string][]byte) ([]byte, error) {
	return func(_ map[string][]byte) ([]byte, error) {
		return renderSQLiteAsMarkdown(srcDBPath)
	}
}

// concatSortedInputs joins multiple inputs in deterministic source-id order.
func concatSortedInputs(ins map[string][]byte) []byte {
	ids := make([]string, 0, len(ins))
	for id := range ins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.Write(ins[id])
	}
	return []byte(b.String())
}

// parseMarkdownTables extracts every GFM pipe table, naming each by the nearest
// preceding heading (slugified) or `table_N`.
func parseMarkdownTables(md []byte) []mdTable {
	lines := strings.Split(string(md), "\n")
	var tables []mdTable
	lastHeading := ""
	n := 0
	for i := 0; i < len(lines); i++ {
		if m := headingLine.FindStringSubmatch(lines[i]); m != nil {
			lastHeading = m[1]
			continue
		}
		// A table is a header row immediately followed by a separator row.
		if i+1 < len(lines) && isTableRow(lines[i]) && tableSep.MatchString(lines[i+1]) {
			header := splitTableRow(lines[i])
			j := i + 2
			var rows [][]string
			for j < len(lines) && isTableRow(lines[j]) {
				cells := splitTableRow(lines[j])
				// normalize row width to the header
				for len(cells) < len(header) {
					cells = append(cells, "")
				}
				if len(cells) > len(header) {
					cells = cells[:len(header)]
				}
				rows = append(rows, cells)
				j++
			}
			n++
			name := slugify(lastHeading)
			if name == "" {
				name = fmt.Sprintf("table_%d", n)
			}
			tables = append(tables, mdTable{name: name, cols: header, rows: rows})
			lastHeading = "" // a heading binds to at most one table
			i = j - 1
		}
	}
	return tables
}

func isTableRow(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "|") || (strings.Contains(t, "|") && !tableSep.MatchString(s))
}

// splitTableRow splits a `| a | b |` row into trimmed, unescaped cells.
func splitTableRow(s string) []string {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	// split on unescaped pipes
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] == '\\' && i+1 < len(t) && t[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if t[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(t[i])
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

// slugify turns heading text into a stable SQL-safe lower-snake identifier.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonIdent.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s != "" && s[0] >= '0' && s[0] <= '9' {
		s = "t_" + s
	}
	return s
}

func openDB(path string) (*sql.DB, error) {
	dsn := path
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	return sql.Open("sqlite", dsn)
}

// writeTablesToSQLite drops + recreates each parsed table inside one txn.
func writeTablesToSQLite(outPath string, tables []mdTable) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	db, err := openDB(outPath)
	if err != nil {
		return fmt.Errorf("md-to-sqlite: open %q: %w", outPath, err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("md-to-sqlite: begin: %w", err)
	}
	for _, t := range tables {
		if _, err := tx.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", quoteIdent(t.name))); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("md-to-sqlite: drop %q: %w", t.name, err)
		}
		colDefs := make([]string, len(t.cols))
		for i, c := range t.cols {
			colDefs[i] = quoteIdent(c) + " TEXT"
		}
		create := fmt.Sprintf("CREATE TABLE %s (%s)", quoteIdent(t.name), strings.Join(colDefs, ", "))
		if _, err := tx.Exec(create); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("md-to-sqlite: create %q: %w", t.name, err)
		}
		if len(t.cols) == 0 {
			continue
		}
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(t.cols)), ", ")
		cols := make([]string, len(t.cols))
		for i, c := range t.cols {
			cols[i] = quoteIdent(c)
		}
		ins := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoteIdent(t.name), strings.Join(cols, ", "), ph)
		stmt, err := tx.Prepare(ins)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("md-to-sqlite: prepare %q: %w", t.name, err)
		}
		for _, row := range t.rows {
			args := make([]any, len(row))
			for i, v := range row {
				args[i] = v
			}
			if _, err := stmt.Exec(args...); err != nil {
				stmt.Close()
				_ = tx.Rollback()
				return fmt.Errorf("md-to-sqlite: insert into %q: %w", t.name, err)
			}
		}
		stmt.Close()
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("md-to-sqlite: commit: %w", err)
	}
	return nil
}

// canonicalDumpAllTables returns a deterministic schema+rows serialization of
// every user table (tables sorted by name; rows in rowid order; columns in
// definition order), matching the tab/newline shape SQLiteAdapter.Read uses so
// the DB node's content hash is stable.
func canonicalDumpAllTables(path string) ([]byte, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite dump: open %q: %w", path, err)
	}
	defer db.Close()
	names, err := userTableNames(db)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, name := range names {
		cols, err := tableColumns(db, name)
		if err != nil {
			return nil, err
		}
		// schema header line: TABLE<TAB>name<TAB>col1<TAB>col2...
		b.WriteString("TABLE\t" + name + "\t" + strings.Join(cols, "\t") + "\n")
		rows, err := db.Query(fmt.Sprintf("SELECT %s FROM %s ORDER BY rowid", quoteCols(cols), quoteIdent(name)))
		if err != nil {
			return nil, fmt.Errorf("sqlite dump: select %q: %w", name, err)
		}
		for rows.Next() {
			raw := make([]sql.RawBytes, len(cols))
			args := make([]any, len(cols))
			for i := range raw {
				args[i] = &raw[i]
			}
			if err := rows.Scan(args...); err != nil {
				rows.Close()
				return nil, fmt.Errorf("sqlite dump: scan %q: %w", name, err)
			}
			fields := make([]string, len(cols))
			for i, rb := range raw {
				if rb == nil {
					fields[i] = "\x00NULL\x00"
				} else {
					fields[i] = string(rb)
				}
			}
			b.WriteString("ROW\t" + strings.Join(fields, "\t") + "\n")
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlite dump: rows %q: %w", name, err)
		}
		rows.Close()
	}
	return []byte(b.String()), nil
}

// renderSQLiteAsMarkdown reads every user table and renders normalized GFM.
func renderSQLiteAsMarkdown(path string) ([]byte, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite-to-md: open %q: %w", path, err)
	}
	defer db.Close()
	names, err := userTableNames(db)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for ti, name := range names {
		if ti > 0 {
			b.WriteByte('\n')
		}
		cols, err := tableColumns(db, name)
		if err != nil {
			return nil, err
		}
		b.WriteString("## " + name + "\n\n")
		b.WriteString(renderRow(cols))
		seps := make([]string, len(cols))
		for i := range seps {
			seps[i] = "---"
		}
		b.WriteString(renderRow(seps))
		rows, err := db.Query(fmt.Sprintf("SELECT %s FROM %s ORDER BY rowid", quoteCols(cols), quoteIdent(name)))
		if err != nil {
			return nil, fmt.Errorf("sqlite-to-md: select %q: %w", name, err)
		}
		for rows.Next() {
			raw := make([]sql.RawBytes, len(cols))
			args := make([]any, len(cols))
			for i := range raw {
				args[i] = &raw[i]
			}
			if err := rows.Scan(args...); err != nil {
				rows.Close()
				return nil, fmt.Errorf("sqlite-to-md: scan %q: %w", name, err)
			}
			cells := make([]string, len(cols))
			for i, rb := range raw {
				if rb == nil {
					cells[i] = ""
				} else {
					cells[i] = string(rb)
				}
			}
			b.WriteString(renderRow(cells))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlite-to-md: rows %q: %w", name, err)
		}
		rows.Close()
	}
	return []byte(b.String()), nil
}

// renderRow renders one GFM table row with single-space padding + pipe-escaping.
func renderRow(cells []string) string {
	esc := make([]string, len(cells))
	for i, c := range cells {
		esc[i] = strings.ReplaceAll(strings.ReplaceAll(c, "\n", " "), "|", "\\|")
	}
	return "| " + strings.Join(esc, " | ") + " |\n"
}

// userTableNames returns non-internal table names sorted ascending.
func userTableNames(db *sql.DB) ([]string, error) {
	// A genuinely fresh/empty SQLite DB returns ZERO ROWS here (no error). The
	// ONLY way this query errors is a real problem — corruption, lock,
	// permission, wrong-format file. Propagate it instead of masking corruption
	// as "no tables" (§11.4.6 no-guessing, §11.4.93 DB-as-SSoT integrity);
	// previously this returned (nil, nil) and silently treated a corrupt DB as
	// empty. See TestSQLiteRead_CorruptDBSurfacesError.
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list tables: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// tableColumns returns a table's column names in definition order.
func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", quoteIdent(table)))
	if err != nil {
		return nil, fmt.Errorf("table_info %q: %w", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func quoteCols(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = quoteIdent(c)
	}
	return strings.Join(q, ", ")
}
