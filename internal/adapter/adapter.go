// Package adapter implements Phase 2 of Docs Chain: node-content adapters
// that read/write the real backing stores behind each graph node, plus the
// per-kind content-hash normalization those nodes require.
//
// Phase 1 (internal/graph + internal/hash) is pure and in-memory: transforms
// are injected functions and content lives in a MemStore. Phase 2 binds the
// engine to reality. An Adapter knows how to Read the current bytes of a
// node from its backing store, Write new bytes to it, and supply the
// hash.Hasher whose normalization makes semantically-equivalent content
// collide for that kind.
//
// The FileStore at the bottom of this file implements the Phase-1
// graph.Store interface on top of a set of registered adapters, so
// graph.Recompute runs unmodified against real files and databases.
package adapter

import (
	"errors"
	"fmt"

	"digital.vasic.docs_chain/internal/graph"
	"digital.vasic.docs_chain/internal/hash"
)

// Adapter is the Phase-2 content adapter for a single node. Each concrete
// adapter is bound to one node's backing store (a file path, a DB+query)
// and exposes:
//
//   - Read: current content bytes of the node (for hashing + as transform
//     input). A missing backing store yields (nil, nil) — "never written",
//     which the ByteContentHasher hashes to the empty-content hash, so a
//     not-yet-generated derived node is naturally dirty.
//   - Write: persist new content bytes (used for regenerated/derived nodes).
//   - Hasher: the per-kind hash.Hasher whose Normalize defines collisions.
//
// Read/Write operate on the canonical *content* of the node, not raw store
// bytes — the sqlite adapter, for instance, reads a deterministic table
// dump (not the .db file's page bytes), so WAL/page noise never perturbs
// the hash (DESIGN §9 / §11.4.93).
type Adapter interface {
	Read() ([]byte, error)
	Write(content []byte) error
	Hasher() hash.Hasher
	// Kind reports the graph.NodeKind this adapter backs (diagnostics).
	Kind() graph.NodeKind
}

// ToolAbsentError is the typed error returned by a DERIVED adapter (html,
// pdf, docx) whose external tool (pandoc / weasyprint) is not installed.
// It is NEVER a fake-success: callers (and tests) match on it via
// errors.As to SKIP-with-reason rather than claim a transform ran.
type ToolAbsentError struct {
	Tool string // e.g. "pandoc", "weasyprint"
	Path string // where we looked, when known
}

func (e *ToolAbsentError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("adapter: required tool %q not found (looked at %q); refusing to fake success",
			e.Tool, e.Path)
	}
	return fmt.Sprintf("adapter: required tool %q not found on PATH; refusing to fake success", e.Tool)
}

// IsToolAbsent reports whether err is (or wraps) a *ToolAbsentError.
func IsToolAbsent(err error) bool {
	var t *ToolAbsentError
	return errors.As(err, &t)
}
