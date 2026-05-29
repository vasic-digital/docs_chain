package adapter

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"digital.vasic.docs_chain/internal/graph"
	"digital.vasic.docs_chain/internal/hash"
)

// FileAdapter is a content adapter backed by a single regular file. It is the
// implementation behind the markdown node kind (and is reused as the on-disk
// backing for html/pdf/docx outputs once a derived adapter has produced their
// bytes). Read returns the file's bytes; a missing file is reported as
// (nil, nil) — "never written" — so a not-yet-generated node hashes to the
// empty-content hash and is therefore naturally dirty on first run.
//
// Normalization is supplied by the injected hash.Hasher. The markdown
// constructor wires the default ByteContentHasher (LF normalization,
// trailing-whitespace strip, single trailing newline) so two markdown files
// differing only in those respects collide by design.
type FileAdapter struct {
	path   string
	kind   graph.NodeKind
	hasher hash.Hasher
}

// NewMarkdownAdapter returns a FileAdapter for a .md file using the default
// ByteContentHasher.
func NewMarkdownAdapter(path string) *FileAdapter {
	return &FileAdapter{
		path:   path,
		kind:   graph.KindMarkdown,
		hasher: hash.NewByteContentHasher(),
	}
}

// NewFileAdapter returns a FileAdapter for an arbitrary kind/path with an
// explicit hasher. Used internally by the derived (html/pdf/docx) adapters to
// expose their produced file's bytes through the Store.
func NewFileAdapter(path string, kind graph.NodeKind, h hash.Hasher) *FileAdapter {
	if h == nil {
		h = hash.NewByteContentHasher()
	}
	return &FileAdapter{path: path, kind: kind, hasher: h}
}

// Path returns the backing file path.
func (a *FileAdapter) Path() string { return a.path }

// Kind reports the node kind this adapter backs.
func (a *FileAdapter) Kind() graph.NodeKind { return a.kind }

// Hasher returns the per-kind hasher.
func (a *FileAdapter) Hasher() hash.Hasher { return a.hasher }

// Read returns the file's current bytes, or (nil, nil) if the file does not
// exist yet.
func (a *FileAdapter) Read() ([]byte, error) {
	b, err := os.ReadFile(a.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

// Write persists content to the file, creating parent directories as needed.
// It writes via a temp file in the same directory + atomic rename so a
// reader never observes a half-written file (the orchestrator layers its own
// all-or-nothing staging on top of this, but per-file atomicity here is the
// floor).
func (a *FileAdapter) Write(content []byte) error {
	dir := filepath.Dir(a.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".docs_chain_md_*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, a.path)
}
