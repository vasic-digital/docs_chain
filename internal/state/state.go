// Package state persists per-context content hashes between runs. The engine
// (graph.Recompute) derives the dirty set by comparing each node's freshly
// computed hash against its stored baseline (the node's Hash field). That
// baseline must survive across process invocations, so it is serialized to
// `<project-root>/.docs_chain/state.json` (gitignored, regenerable per
// §11.4.77).
//
// state.json is keyed by context name -> node id -> hash. A missing file (or
// a missing context/node) yields an empty baseline, which makes every node
// dirty on the first run — the correct cold-start behaviour (a
// not-yet-generated derived node hashes to the empty-content hash and is
// regenerated).
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// State is the on-disk hash baseline for all contexts.
type State struct {
	// Version is the schema version (1).
	Version int `json:"version"`
	// Contexts maps context name -> (node id -> hash).
	Contexts map[string]map[string]string `json:"contexts"`
}

// New returns an empty State.
func New() *State {
	return &State{Version: 1, Contexts: make(map[string]map[string]string)}
}

// DefaultPath returns the state.json path under the given project root.
func DefaultPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".docs_chain", "state.json")
}

// Load reads state from path. A non-existent file is NOT an error: it returns
// a fresh empty State (cold start).
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, fmt.Errorf("state: read %q: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("state: parse %q: %w", path, err)
	}
	if s.Contexts == nil {
		s.Contexts = make(map[string]map[string]string)
	}
	if s.Version == 0 {
		s.Version = 1
	}
	return &s, nil
}

// Hashes returns the stored hash map for a context (a copy; never nil).
func (s *State) Hashes(context string) map[string]string {
	out := make(map[string]string)
	for k, v := range s.Contexts[context] {
		out[k] = v
	}
	return out
}

// SetHashes records the post-run hash map for a context (replacing any prior).
func (s *State) SetHashes(context string, hashes map[string]string) {
	cp := make(map[string]string, len(hashes))
	for k, v := range hashes {
		cp[k] = v
	}
	s.Contexts[context] = cp
}

// Save writes state atomically (temp-then-rename) to path, creating the
// parent directory as needed. The JSON is indented + key-stable (Go's
// encoding/json sorts map keys) so the file is diff-friendly and
// deterministic.
func (s *State) Save(path string) error {
	if s.Version == 0 {
		s.Version = 1
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("state: mkdir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".state_*.json.tmp")
	if err != nil {
		return fmt.Errorf("state: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("state: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("state: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("state: rename: %w", err)
	}
	return nil
}
