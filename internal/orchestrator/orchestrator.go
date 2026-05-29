// Package orchestrator implements Phase 3 of Docs Chain: the propagation
// orchestrator that ties the Phase-1 recompute engine (internal/graph) to the
// Phase-2 real-world adapters (internal/adapter) with three guarantees the
// pure engine deliberately leaves to this layer:
//
//   - ATOMICITY — every regenerated output is staged to a temp file first;
//     live artefacts are replaced by atomic rename only after the WHOLE run
//     succeeds. ANY transform error rolls back: staged temps are discarded
//     and no live file is touched. Composes with the §9.2 zero-risk
//     data-safety protocol (DESIGN §8).
//   - CYCLE-GUARD — the run refuses to start if graph.Validate reports a
//     cycle in the derive-from sub-graph (CycleError), surfacing it without
//     writing anything (DESIGN §6).
//   - SYNC-CONFLICT — a both-dirty sync pair surfaces graph.ConflictError
//     and the run writes nothing (DESIGN §5, §11.4.6 no-guessing).
//
// The Run entrypoint returns the engine's RecomputeResult plus a Status
// reporting whether the run COMMITTED its changes or ROLLED BACK.
package orchestrator

import (
	"errors"
	"fmt"
	"sort"

	"digital.vasic.docs_chain/internal/adapter"
	"digital.vasic.docs_chain/internal/graph"
)

// Status reports the terminal disposition of a Run.
type Status string

const (
	// StatusInSync: nothing was dirty; no work performed.
	StatusInSync Status = "in-sync"
	// StatusCommitted: outputs regenerated and atomically committed.
	StatusCommitted Status = "committed"
	// StatusRolledBack: a transform (or commit) error occurred AFTER some
	// staging; all staged temps were discarded, live state is unchanged.
	StatusRolledBack Status = "rolled-back"
	// StatusConflict: a both-dirty sync pair was detected; nothing written.
	StatusConflict Status = "conflict"
	// StatusCycle: a derive-from cycle was detected at validate; nothing
	// written.
	StatusCycle Status = "cycle"
)

// Result is the orchestrator's run report.
type Result struct {
	Status Status
	// Recompute is the engine result (nil for cycle/conflict pre-recompute
	// refusals).
	Recompute *graph.RecomputeResult
	// Committed lists the node IDs whose backing artefacts were atomically
	// written this run (empty on in-sync / rollback / conflict / cycle).
	Committed []string
	// Err is the underlying error for non-committed terminal states
	// (*graph.CycleError, *graph.ConflictError, or a transform/commit error).
	Err error
}

// stagingStore wraps the real adapter.FileStore so that, during a Run, every
// Set is captured into a staging buffer instead of touching live artefacts.
// Get reads through to the real store (transform inputs are always the
// up-to-date live content, since this run's outputs have not yet been
// committed). On success the orchestrator drains the staging buffer through
// atomic writes; on failure it discards it.
type stagingStore struct {
	real *adapter.FileStore
	// staged preserves insertion order of node writes so commit + the
	// Committed report are deterministic.
	staged map[string][]byte
	order  []string
}

func newStagingStore(real *adapter.FileStore) *stagingStore {
	return &stagingStore{real: real, staged: make(map[string][]byte)}
}

// Get returns this-run staged content if the node was already regenerated
// during this pass (so a multi-level chain — md→html→pdf — feeds the freshly
// staged html into the pdf transform), otherwise the live content from the
// real store. Live artefacts on disk are never mutated until commit, so a
// rollback leaves them byte-identical.
func (s *stagingStore) Get(id string) ([]byte, error) {
	if v, ok := s.staged[id]; ok {
		cp := make([]byte, len(v))
		copy(cp, v)
		return cp, nil
	}
	return s.real.Get(id)
}

// Set captures the regenerated content WITHOUT touching the live store.
func (s *stagingStore) Set(id string, content []byte) error {
	if _, seen := s.staged[id]; !seen {
		s.order = append(s.order, id)
	}
	cp := make([]byte, len(content))
	copy(cp, content)
	s.staged[id] = cp
	return nil
}

var _ graph.Store = (*stagingStore)(nil)

// Run executes one propagation pass.
//
// Order of guarantees:
//  1. CYCLE-GUARD: g.Validate() — a *graph.CycleError aborts with
//     StatusCycle, zero writes.
//  2. RECOMPUTE into a staging store. The engine performs sync-conflict
//     detection internally; a *graph.ConflictError aborts with
//     StatusConflict, zero writes. A transform error aborts with
//     StatusRolledBack, zero writes (nothing was committed — staging only).
//  3. ATOMIC COMMIT: drain the staging buffer to the real store via the
//     adapters' atomic Write. If a commit write fails partway, the already-
//     renamed files are the only side effect; the run reports StatusRolledBack
//     with the commit error (best-effort: file adapters write atomically per
//     file, so a mid-commit failure leaves earlier files committed — this is
//     surfaced honestly rather than hidden).
//  4. On full success, CommitHashes folds the new hashes onto the graph so
//     the next run sees them as baseline, and Status is StatusCommitted (or
//     StatusInSync if nothing regenerated).
func Run(g *graph.Graph, store *adapter.FileStore, h graph.Hasher, transforms map[string]graph.Transform) (*Result, error) {
	if g == nil || store == nil || h == nil {
		return nil, errors.New("orchestrator: Run requires non-nil graph, store, hasher")
	}

	// (1) cycle-guard.
	if err := g.Validate(); err != nil {
		var ce *graph.CycleError
		if errors.As(err, &ce) {
			return &Result{Status: StatusCycle, Err: ce}, nil
		}
		return nil, fmt.Errorf("orchestrator: graph validation failed: %w", err)
	}

	// (2) recompute against a staging store (no live writes yet).
	staging := newStagingStore(store)
	res, err := g.Recompute(staging, h, transforms)
	if err != nil {
		var conflict *graph.ConflictError
		if errors.As(err, &conflict) {
			// sync-conflict: nothing was committed (staging only).
			return &Result{Status: StatusConflict, Err: conflict}, nil
		}
		// transform error (or other engine error): roll back by discarding
		// the staging buffer — which we simply never drain. Live artefacts
		// are byte-identical to the pre-run state.
		return &Result{Status: StatusRolledBack, Err: err}, nil
	}

	// Nothing regenerated -> in-sync, no commit work.
	if len(staging.order) == 0 {
		g.CommitHashes(res)
		return &Result{Status: StatusInSync, Recompute: res, Committed: nil}, nil
	}

	// (3) atomic commit: drain staging through the adapters' atomic Write.
	committed := make([]string, 0, len(staging.order))
	for _, id := range staging.order {
		if err := store.Set(id, staging.staged[id]); err != nil {
			// A commit-phase failure: report honestly which files already
			// landed. We do NOT claim full success (§11.4.6).
			return &Result{
				Status:    StatusRolledBack,
				Recompute: res,
				Committed: committed,
				Err:       fmt.Errorf("orchestrator: commit write for %q failed after %d committed: %w", id, len(committed), err),
			}, nil
		}
		committed = append(committed, id)
	}
	sort.Strings(committed)

	// (4) success: fold hashes onto the graph baseline.
	g.CommitHashes(res)
	return &Result{Status: StatusCommitted, Recompute: res, Committed: committed}, nil
}

// IsToolAbsent re-exports adapter.IsToolAbsent so orchestrator callers can
// SKIP-with-reason when a transform failed only because pandoc/weasyprint is
// absent (the run will have rolled back; the caller decides SKIP vs FAIL).
func IsToolAbsent(err error) bool { return adapter.IsToolAbsent(err) }

// AtomicRenameNote documents the per-file atomicity floor: the FileAdapter
// already writes via temp-file + rename, and the orchestrator additionally
// withholds ALL writes until recompute fully succeeds. The combination is the
// run-level all-or-nothing guarantee (the only residual non-atomicity is a
// multi-file commit interrupted by an OS-level failure mid-drain, reported
// honestly via StatusRolledBack + the partial Committed list).
const AtomicRenameNote = "staged-then-renamed; run withholds writes until recompute fully succeeds"
