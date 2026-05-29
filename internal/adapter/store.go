package adapter

import (
	"fmt"

	"digital.vasic.docs_chain/internal/graph"
)

// FileStore implements the Phase-1 graph.Store interface on top of a set of
// per-node Adapters, so graph.Recompute runs UNMODIFIED against real files
// and databases. Get(id) reads the node's current content through its
// adapter; Set(id, content) writes it back. A node ID with no registered
// adapter is an error (the chain is incomplete).
//
// FileStore also exposes Hasher(id) so a recompute driver can use per-kind
// normalization — though graph.Recompute takes a single Hasher, the
// orchestrator (Phase 3) consults per-node hashers via this method when it
// needs kind-specific collision behaviour.
type FileStore struct {
	adapters map[string]Adapter
}

// NewFileStore builds an empty FileStore.
func NewFileStore() *FileStore {
	return &FileStore{adapters: make(map[string]Adapter)}
}

// Register binds a node ID to its adapter. Duplicate registration is an
// error.
func (s *FileStore) Register(nodeID string, a Adapter) error {
	if nodeID == "" {
		return fmt.Errorf("adapter: FileStore.Register empty node ID")
	}
	if a == nil {
		return fmt.Errorf("adapter: FileStore.Register nil adapter for %q", nodeID)
	}
	if _, exists := s.adapters[nodeID]; exists {
		return fmt.Errorf("adapter: FileStore duplicate adapter for node %q", nodeID)
	}
	s.adapters[nodeID] = a
	return nil
}

// Adapter returns the adapter for a node ID (nil if unregistered).
func (s *FileStore) Adapter(nodeID string) Adapter { return s.adapters[nodeID] }

// Get satisfies graph.Store: reads the node's current content.
func (s *FileStore) Get(id string) ([]byte, error) {
	a, ok := s.adapters[id]
	if !ok {
		return nil, fmt.Errorf("adapter: FileStore has no adapter for node %q", id)
	}
	return a.Read()
}

// Set satisfies graph.Store: writes new content to the node's backing store.
func (s *FileStore) Set(id string, content []byte) error {
	a, ok := s.adapters[id]
	if !ok {
		return fmt.Errorf("adapter: FileStore has no adapter for node %q", id)
	}
	return a.Write(content)
}

// Hasher returns the per-kind hasher for a node (nil if unregistered).
func (s *FileStore) Hasher(id string) (graph.Hasher, error) {
	a, ok := s.adapters[id]
	if !ok {
		return nil, fmt.Errorf("adapter: FileStore has no adapter for node %q", id)
	}
	return a.Hasher(), nil
}

// compile-time assertion: *FileStore is a graph.Store.
var _ graph.Store = (*FileStore)(nil)
