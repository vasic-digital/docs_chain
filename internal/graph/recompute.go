package graph

import (
	"errors"
	"fmt"
	"sort"
)

// Transform regenerates the content of a derived node from the (already
// up-to-date) content of its source nodes. Phase 1 transforms are pure
// injected functions over in-memory content; Phase 2 supplies filesystem-
// and exec-backed adapters.
//
// ins maps source node ID -> current content. The returned bytes are the
// new content of the derived node.
type Transform func(ins map[string][]byte) ([]byte, error)

// Store provides current content for nodes during a recompute run. Phase 1
// uses an in-memory map; Phase 2 backs it with files/DB.
type Store interface {
	// Get returns the current content of a node by ID.
	Get(id string) ([]byte, error)
	// Set records new content for a node by ID (used for regenerated nodes).
	Set(id string, content []byte) error
}

// MemStore is an in-memory Store for Phase 1 / unit tests.
type MemStore struct{ m map[string][]byte }

// NewMemStore builds a MemStore from an initial id->content map (copied).
func NewMemStore(initial map[string][]byte) *MemStore {
	m := make(map[string][]byte, len(initial))
	for k, v := range initial {
		cp := make([]byte, len(v))
		copy(cp, v)
		m[k] = cp
	}
	return &MemStore{m: m}
}

// Get returns a copy of the stored content; absent IDs yield empty content.
func (s *MemStore) Get(id string) ([]byte, error) {
	v, ok := s.m[id]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

// Set stores a copy of content.
func (s *MemStore) Set(id string, content []byte) error {
	cp := make([]byte, len(content))
	copy(cp, content)
	s.m[id] = cp
	return nil
}

// Hasher is the minimal hashing contract the recompute engine needs. It is
// satisfied by hash.ByteContentHasher and any Phase 2 per-kind hasher.
type Hasher interface {
	Hash(content []byte) string
}

// PerNodeHasher is an OPTIONAL capability a Store may implement to supply a
// per-node (per-kind) Hasher. When the store passed to Recompute implements
// it, the engine hashes each node with ITS kind-specific hasher instead of the
// single fallback Hasher.
//
// BUG FIX (binary-hash verify defect): binary kinds (pdf, docx) must be hashed
// by raw bytes while text kinds (markdown, html) keep their text
// normalization. Threading the per-node hasher here lets the SYNC-RECORD path
// (this Recompute) and the VERIFY-CHECK path (runner.Verify) select the SAME
// hasher per node — eliminating the inconsistency where one path normalized a
// binary payload and the other did not. Stores that do not implement it (the
// Phase-1 MemStore, the orchestrator's stagingStore wrapper) fall back to the
// single Hasher, preserving existing behaviour.
type PerNodeHasher interface {
	// NodeHasher returns the Hasher for a node id. A nil error with a nil
	// Hasher means "use the fallback".
	NodeHasher(id string) (Hasher, error)
}

// hasherFor returns the per-node hasher for id when store supports it and
// yields a non-nil hasher, else the fallback h.
func hasherFor(store Store, h Hasher, id string) (Hasher, error) {
	if pnh, ok := store.(PerNodeHasher); ok {
		nh, err := pnh.NodeHasher(id)
		if err != nil {
			return nil, err
		}
		if nh != nil {
			return nh, nil
		}
	}
	return h, nil
}

// RecomputeResult is the deterministic record of one recompute run.
type RecomputeResult struct {
	// Dirty is the set of source nodes whose content changed (sorted).
	Dirty []string
	// Recomputed lists derived nodes whose transform actually ran, in topo
	// order.
	Recomputed []string
	// Pruned lists derived nodes that were candidates but whose recomputed
	// hash matched the stored hash — their downstream was cut off (Salsa
	// early-cutoff). Sorted within the topo walk.
	Pruned []string
	// NewHashes maps every node touched to its post-run hash.
	NewHashes map[string]string
}

// ConflictError reports a both-dirty sync pair: both authoritative views
// changed concurrently, so docs_chain refuses to merge (DESIGN.md §3,
// §11.4.6 no-guessing).
type ConflictError struct {
	A, B      string
	Authority string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("graph: sync conflict — both %q and %q are dirty (authority %q); refusing silent merge",
		e.A, e.B, e.Authority)
}

// ResolveSync inspects every sync edge against the dirty set and applies the
// authority contract (DESIGN.md §3):
//   - exactly one side dirty  -> that side is the run's source of truth;
//   - both sides dirty        -> *ConflictError (no writes);
//   - neither dirty           -> nothing to do.
//
// It returns, for each acted-upon sync pair, the (authoritative source,
// regenerated target) so the caller can fold the target back into the dirty
// set for derive-from propagation. Phase 1 does not itself rewrite content
// for sync (that is the Phase 3 orchestrator); it implements the DETECTION +
// authority decision contract deterministically.
func (g *Graph) ResolveSync(dirty map[string]bool) (sources []string, regenTargets []string, err error) {
	type pair struct{ src, tgt string }
	var acted []pair
	for _, e := range g.edges {
		if e.Type != EdgeSync {
			continue
		}
		aDirty, bDirty := dirty[e.From], dirty[e.To]
		switch {
		case aDirty && bDirty:
			return nil, nil, &ConflictError{A: e.From, B: e.To, Authority: e.Authority}
		case aDirty || bDirty:
			// The dirty side is the source of truth; the other is regenerated.
			src := e.From
			tgt := e.To
			if bDirty {
				src, tgt = e.To, e.From
			}
			acted = append(acted, pair{src: src, tgt: tgt})
		default:
			// neither dirty: skip
		}
	}
	for _, p := range acted {
		sources = append(sources, p.src)
		regenTargets = append(regenTargets, p.tgt)
	}
	return sources, regenTargets, nil
}

// Recompute performs early-cutoff incremental recomputation.
//
// Algorithm (DESIGN.md §4):
//  1. Hash every node's current content; compare to the node's stored Hash
//     to build the dirty set.
//  2. Resolve sync pairs (authority / conflict). A both-dirty pair aborts
//     with *ConflictError.
//  3. Walk the derive-from sub-graph in Kahn topo order. A derived node is a
//     recompute candidate iff at least one of its derive-from sources is
//     dirty. Run its transform, hash the output: if the hash equals the
//     node's stored hash, PRUNE — the node is NOT marked dirty, so its own
//     downstream sees no dirty source and is skipped (early cutoff). If the
//     hash differs, store the new content and mark the node dirty so its
//     downstream propagates.
//
// transforms maps a derived node ID -> its Transform. A derived node with no
// transform but a dirty source is an error (incomplete chain).
func (g *Graph) Recompute(store Store, h Hasher, transforms map[string]Transform) (*RecomputeResult, error) {
	if store == nil || h == nil {
		return nil, errors.New("graph: Recompute requires non-nil store and hasher")
	}

	res := &RecomputeResult{NewHashes: make(map[string]string)}

	// Step 1: initial dirty set from content-hash vs stored hash.
	dirty := make(map[string]bool, len(g.nodes))
	for _, id := range g.Nodes() {
		n := g.nodes[id]
		content, err := store.Get(id)
		if err != nil {
			return nil, fmt.Errorf("graph: store.Get(%q): %w", id, err)
		}
		nh, herr := hasherFor(store, h, id)
		if herr != nil {
			return nil, fmt.Errorf("graph: hasher for %q: %w", id, herr)
		}
		cur := nh.Hash(content)
		res.NewHashes[id] = cur
		if cur != n.Hash {
			dirty[id] = true
		}
	}

	// Step 2: sync-edge resolution (conflict detection + authority).
	_, regenTargets, err := g.ResolveSync(dirty)
	if err != nil {
		return nil, err
	}
	// A regenerated sync target participates as a dirty input to derive-from
	// propagation (its authoritative source changed). Phase 1 marks it dirty
	// without rewriting content; the Phase 3 orchestrator owns the rewrite.
	for _, t := range regenTargets {
		dirty[t] = true
	}

	// Build the sorted dirty list for the result.
	for id := range dirty {
		res.Dirty = append(res.Dirty, id)
	}
	sort.Strings(res.Dirty)

	// Map derived node -> its derive-from source IDs.
	sources := make(map[string][]string)
	for _, e := range g.edges {
		if e.Type == EdgeDeriveFrom {
			sources[e.To] = append(sources[e.To], e.From)
		}
	}

	// Step 3: topo walk with early cutoff.
	order, err := g.TopoOrder()
	if err != nil {
		return nil, err
	}
	for _, id := range order {
		srcs, isDerived := sources[id]
		if !isDerived {
			continue // input node; already hashed in step 1
		}
		anyDirty := false
		for _, s := range srcs {
			if dirty[s] {
				anyDirty = true
				break
			}
		}
		if !anyDirty {
			continue // no dirty source -> no candidacy (pruned upstream)
		}

		tf, ok := transforms[id]
		if !ok {
			return nil, fmt.Errorf("graph: derived node %q has dirty source but no transform", id)
		}
		ins := make(map[string][]byte, len(srcs))
		for _, s := range srcs {
			c, gerr := store.Get(s)
			if gerr != nil {
				return nil, fmt.Errorf("graph: store.Get(%q): %w", s, gerr)
			}
			ins[s] = c
		}
		out, terr := tf(ins)
		if terr != nil {
			return nil, fmt.Errorf("graph: transform for %q failed: %w", id, terr)
		}
		nh, herr := hasherFor(store, h, id)
		if herr != nil {
			return nil, fmt.Errorf("graph: hasher for %q: %w", id, herr)
		}
		newHash := nh.Hash(out)
		if newHash == g.nodes[id].Hash {
			// Early cutoff: output unchanged. Do NOT mark dirty; downstream
			// is pruned.
			res.Pruned = append(res.Pruned, id)
			res.NewHashes[id] = newHash
			continue
		}
		// Output changed: persist, mark dirty, propagate downstream.
		if serr := store.Set(id, out); serr != nil {
			return nil, fmt.Errorf("graph: store.Set(%q): %w", id, serr)
		}
		dirty[id] = true
		res.Recomputed = append(res.Recomputed, id)
		res.NewHashes[id] = newHash
	}

	return res, nil
}

// CommitHashes writes the post-run hashes back onto the graph nodes so the
// next run sees them as the stored baseline.
func (g *Graph) CommitHashes(res *RecomputeResult) {
	for id, hh := range res.NewHashes {
		if n := g.nodes[id]; n != nil {
			n.Hash = hh
		}
	}
}
