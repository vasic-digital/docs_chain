package adapter

import (
	"database/sql"
	"fmt"
	"strings"

	"digital.vasic.docs_chain/internal/graph"
	"digital.vasic.docs_chain/internal/hash"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// SQLiteAdapter backs a sqlite node. Crucially, its hashed *content* is NOT
// the raw .db file bytes — WAL pages, free-list churn, and VACUUM noise make
// raw bytes non-deterministic across logically-identical databases. Instead
// Read produces a CANONICAL DUMP: the rows returned by a caller-supplied
// query, serialized deterministically (the query MUST carry its own
// ORDER BY for row order; columns are emitted in the SELECT order, each row
// tab-joined, rows newline-joined). Identical row sets therefore hash
// identically regardless of insertion order or page layout (§11.4.93 /
// DESIGN §9).
//
// Write applies a caller-supplied apply func inside a single transaction —
// the orchestrator's atomic-commit phase owns when that runs relative to the
// file renames (DESIGN §8).
type SQLiteAdapter struct {
	dsn       string // e.g. "file:/path/to.db" or a plain path
	dumpQuery string // deterministic SELECT ... ORDER BY ...
	// apply turns new "content" bytes into DB mutations inside a txn. For a
	// pure read-for-hashing node this may be nil; a sync md→db node supplies
	// it. content is whatever the upstream transform produced.
	apply  func(tx *sql.Tx, content []byte) error
	hasher hash.Hasher
}

// SQLiteConfig parameterizes a SQLiteAdapter.
type SQLiteConfig struct {
	// DSN is the database source name. A bare path is accepted; it is
	// prefixed with "file:" for the modernc driver.
	DSN string
	// DumpQuery is the deterministic content query. It MUST include an
	// ORDER BY so the dump is stable. Required.
	DumpQuery string
	// Apply, if non-nil, performs md→db mutation inside a transaction.
	Apply func(tx *sql.Tx, content []byte) error
}

// NewSQLiteAdapter constructs a SQLiteAdapter. DumpQuery is required.
func NewSQLiteAdapter(cfg SQLiteConfig) (*SQLiteAdapter, error) {
	if strings.TrimSpace(cfg.DumpQuery) == "" {
		return nil, fmt.Errorf("adapter: SQLite DumpQuery is required (must be a deterministic ORDER BY query)")
	}
	dsn := cfg.DSN
	if dsn != "" && !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	return &SQLiteAdapter{
		dsn:       dsn,
		dumpQuery: cfg.DumpQuery,
		apply:     cfg.Apply,
		hasher:    hash.NewByteContentHasher(),
	}, nil
}

// Kind reports the sqlite node kind.
func (a *SQLiteAdapter) Kind() graph.NodeKind { return graph.KindSQLite }

// Hasher returns the content hasher (byte-content over the canonical dump).
func (a *SQLiteAdapter) Hasher() hash.Hasher { return a.hasher }

// open opens the database. Each Read/Write opens + closes its own handle to
// keep the adapter stateless and concurrency-safe at the engine layer.
func (a *SQLiteAdapter) open() (*sql.DB, error) {
	db, err := sql.Open("sqlite", a.dsn)
	if err != nil {
		return nil, fmt.Errorf("adapter: sqlite open %q: %w", a.dsn, err)
	}
	return db, nil
}

// Read runs the deterministic dump query and returns its canonical
// serialization. A non-existent DB / empty result yields empty content,
// which hashes to the empty-content hash (naturally "dirty" for a fresh
// derived sync target). The serialization is:
//
//	row := col0 \t col1 \t ... \t colN
//	dump := row0 \n row1 \n ... \n rowM \n   (one trailing newline if non-empty)
//
// NULL columns serialize to the literal token "\x00NULL\x00" so they are
// distinguishable from an empty string. Because the query carries ORDER BY,
// the dump is a pure function of the row SET — insert order is irrelevant.
func (a *SQLiteAdapter) Read() ([]byte, error) {
	db, err := a.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(a.dumpQuery)
	if err != nil {
		// Treat "no such table" / missing DB as empty content rather than a
		// hard error: a not-yet-populated sync target is legitimately empty.
		return nil, nil
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("adapter: sqlite columns: %w", err)
	}

	var b strings.Builder
	for rows.Next() {
		raw := make([]sql.RawBytes, len(cols))
		scanArgs := make([]any, len(cols))
		for i := range raw {
			scanArgs[i] = &raw[i]
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, fmt.Errorf("adapter: sqlite scan: %w", err)
		}
		fields := make([]string, len(cols))
		for i, rb := range raw {
			if rb == nil {
				fields[i] = "\x00NULL\x00"
			} else {
				fields[i] = string(rb)
			}
		}
		b.WriteString(strings.Join(fields, "\t"))
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adapter: sqlite rows: %w", err)
	}
	return []byte(b.String()), nil
}

// Write applies new content to the DB via the configured Apply func inside a
// single transaction. If no Apply func was configured this node is read-only
// (hashing-only) and Write is an error — a sync md→db node MUST configure
// Apply.
func (a *SQLiteAdapter) Write(content []byte) error {
	if a.apply == nil {
		return fmt.Errorf("adapter: sqlite node is read-only (no Apply func); cannot Write")
	}
	db, err := a.open()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("adapter: sqlite begin: %w", err)
	}
	if err := a.apply(tx, content); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("adapter: sqlite apply: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("adapter: sqlite commit: %w", err)
	}
	return nil
}
