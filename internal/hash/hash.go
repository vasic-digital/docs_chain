// Package hash provides content-hash change detection for docs_chain.
//
// Per DESIGN.md §2 and the §11.4.86 mandate, change detection MUST be by
// content hash over NORMALIZED content — never by mtime. This package
// defines the Hasher interface (so Phase 2 node adapters plug in per-kind
// normalization), a default byte-content hasher, and a sorted-member-list
// fingerprint for roster/corpus sidecars (§11.4.86).
package hash

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Hasher computes a stable sha256 over normalized content. Implementations
// decide how to canonicalize raw bytes before hashing so that semantically
// equivalent inputs (e.g. trailing-whitespace variants) collide as designed.
//
// Phase 2 node adapters supply per-kind Hashers; Phase 1 ships the default
// byte-content hasher below.
type Hasher interface {
	// Hash returns the hex-encoded sha256 of the normalized form of content.
	Hash(content []byte) string
	// Normalize returns the canonical form Hash operates on. Exposed so
	// callers and tests can reason about collisions deterministically.
	Normalize(content []byte) []byte
}

// sum returns the lowercase hex sha256 of b.
func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ByteContentHasher is the default Hasher. Its normalization is line-based
// and whitespace-canonical: CRLF/CR are unified to LF, trailing whitespace
// on each line is stripped, and a single trailing newline is enforced.
// Two inputs that differ only in those respects produce an identical hash.
type ByteContentHasher struct{}

// NewByteContentHasher returns the default content hasher.
func NewByteContentHasher() ByteContentHasher { return ByteContentHasher{} }

// Normalize canonicalizes line endings and trailing whitespace.
func (ByteContentHasher) Normalize(content []byte) []byte {
	s := string(content)
	// Unify line endings to LF first (handle CRLF before lone CR).
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	out := strings.Join(lines, "\n")
	// Enforce exactly one trailing newline for non-empty content; empty
	// content normalizes to empty (no spurious newline).
	out = strings.TrimRight(out, "\n")
	if out != "" {
		out += "\n"
	}
	return []byte(out)
}

// Hash returns the sha256 of the normalized content.
func (h ByteContentHasher) Hash(content []byte) string {
	return sum(h.Normalize(content))
}

// RawByteHasher is the Hasher for BINARY node kinds (pdf, docx, and any other
// non-text payload). Its Normalize is the IDENTITY — it hashes the raw bytes
// verbatim, with NO line-ending / trailing-whitespace / trailing-newline
// canonicalization.
//
// BUG FIX (binary-hash verify defect): the default ByteContentHasher applies a
// text normalizer (CRLF→LF, trailing-whitespace strip, single trailing
// newline). Run over a binary container (a docx zip, a pdf), that normalizer
// REWRITES bytes — e.g. it stripped a trailing 0x0A and collapsed CR/LF byte
// sequences inside the compressed streams, shortening a docx by 1 byte and a
// pdf by ~7 bytes. The normalization is itself deterministic, but applying a
// text transform to binary content is semantically wrong: it can mask a real
// change or, combined with non-reproducible producer output, make the
// sync-record path and the verify-check path disagree about a node's identity.
// Binary kinds MUST be hashed by their raw bytes, identically, in BOTH the
// sync-record path (graph.Recompute) and the verify-check path (runner.Verify).
// RawByteHasher is that hasher; the runner now selects it for pdf/docx.
type RawByteHasher struct{}

// NewRawByteHasher returns the identity (raw-bytes) hasher for binary kinds.
func NewRawByteHasher() RawByteHasher { return RawByteHasher{} }

// Normalize is the identity for raw binary content (no canonicalization).
func (RawByteHasher) Normalize(content []byte) []byte { return content }

// Hash returns the sha256 of the raw bytes, verbatim.
func (RawByteHasher) Hash(content []byte) string { return sum(content) }

// FingerprintMembers returns a drift-proof sha256 over the SORTED member
// list (§11.4.86). Order of the input is irrelevant; the fingerprint is a
// pure function of the SET of members. Each member is newline-joined after
// sorting so that adding, removing, or renaming a member changes the hash
// while reordering does not.
func FingerprintMembers(members []string) string {
	cp := make([]string, len(members))
	copy(cp, members)
	sort.Strings(cp)
	// Join with a separator that cannot appear inside a member path on the
	// systems docs_chain targets; "\n" is the canonical record separator.
	return sum([]byte(strings.Join(cp, "\n")))
}
