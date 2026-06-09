package runner

// ReBaseline is the SAFE affordance for re-baselining a sync edge whose
// authority is one side (DESIGN.md §3, §11.4.6, §11.4.106).
//
// Motivation. The bidirectional `sync` edge contract (graph.ResolveSync) is:
// exactly-one-side-dirty -> that side wins; both-sides-dirty -> *ConflictError
// (refuse silent merge). A context whose sync pair has NO stored baseline (the
// first run, or a state.json that never recorded the pair) sees BOTH endpoints
// dirty-against-empty, so a plain `sync` aborts with a conflict even though the
// two sides are already byte-consistent — there is nothing to merge, only a
// baseline to record. Re-running the authority->non-authority transform and
// recording the resulting hashes resolves this WITHOUT writing the authority
// side: the authority is the source of truth and is only READ.
//
// Hard safety invariant (the whole reason this is a separate, audited path):
// ReBaseline regenerates ONLY the NON-authority side of each sync edge, from
// the authority side, using the authority->non-authority transform (here:
// db-to-md — read the DB, write the markdown). It NEVER invokes the
// non-authority->authority transform (md-to-db — which would write the DB), and
// it NEVER calls Set on an authority node. The guard is structural: writes go
// through a writeGuardStore that returns an error (and the run rolls back) if
// any protected (authority) node id is ever the target of a Set. A bug, a
// mis-bound transform, or a future edit that tries to write the authority side
// turns into a rolled-back run with a clear error — never a silent DB write.
//
// After regenerating the non-authority side, ReBaseline re-derives the
// derive-from sub-graph from it (so the summary/html/pdf exports refresh) and
// folds EVERY node's fresh hash (including the authority node's READ hash) into
// state, so the next plain `verify`/`sync` sees an in-sync, conflict-free
// baseline.

import (
	"errors"
	"fmt"
	"sort"

	"digital.vasic.docs_chain/internal/adapter"
	"digital.vasic.docs_chain/internal/config"
	"digital.vasic.docs_chain/internal/graph"
	"digital.vasic.docs_chain/internal/orchestrator"
	"digital.vasic.docs_chain/internal/state"
)

// ProtectedWriteError reports that a re-baseline run attempted to write a
// protected (sync-authority) node. It is the structural guard that makes the
// db-to-md-only direction unbypassable: any Set against an authority node id
// surfaces this error and the orchestrator rolls the whole run back with zero
// live writes.
type ProtectedWriteError struct {
	NodeID string
}

func (e *ProtectedWriteError) Error() string {
	return fmt.Sprintf("runner: re-baseline refused to write protected sync-authority node %q (authority side is read-only; only the non-authority side is regenerated)", e.NodeID)
}

// writeGuardStore wraps a real *adapter.FileStore and refuses every Set against
// a protected node id, returning *ProtectedWriteError. Reads pass through
// unchanged (the authority side MUST be readable — it is the source). It also
// re-exposes the per-node hasher so the engine keeps binary/text hashing
// consistent with the unguarded path.
type writeGuardStore struct {
	real      *adapter.FileStore
	protected map[string]bool
}

func newWriteGuardStore(real *adapter.FileStore, protected map[string]bool) *writeGuardStore {
	return &writeGuardStore{real: real, protected: protected}
}

func (s *writeGuardStore) Get(id string) ([]byte, error) { return s.real.Get(id) }

func (s *writeGuardStore) Set(id string, content []byte) error {
	if s.protected[id] {
		return &ProtectedWriteError{NodeID: id}
	}
	return s.real.Set(id, content)
}

func (s *writeGuardStore) NodeHasher(id string) (graph.Hasher, error) {
	return s.real.Hasher(id)
}

var _ graph.Store = (*writeGuardStore)(nil)
var _ graph.PerNodeHasher = (*writeGuardStore)(nil)

// ReBaselineResult reports the disposition of a re-baseline run.
type ReBaselineResult struct {
	// Status is the orchestrator status of the underlying run (committed /
	// in-sync / rolled-back / conflict / cycle).
	Status orchestrator.Status
	// Regenerated lists the NON-authority node ids that were regenerated from
	// their authority (the sync targets re-baselined this run), sorted.
	Regenerated []string
	// Committed lists every node id whose backing artefact was atomically
	// written this run (the regenerated non-authority side plus any refreshed
	// derive-from exports). The authority node id is NEVER in this list.
	Committed []string
	// ProtectedNodes lists the authority node ids that were write-guarded this
	// run (read-only), sorted — the auditable proof of which files were
	// off-limits to writes.
	ProtectedNodes []string
	// Err is the underlying error for a non-committed terminal state.
	Err error
}

// authorityNonAuthority returns, for every sync edge in the context, the
// authority node id and the non-authority node id plus the transform that
// regenerates the non-authority side from the authority side. It errors if a
// sync edge has no authority or no authority->non-authority transform (we will
// not guess a direction — §11.4.6).
type syncRebaseEdge struct {
	authority    string
	nonAuthority string
	transform    string // name of the authority->non-authority transform
}

func collectSyncRebaseEdges(c *config.Context) ([]syncRebaseEdge, error) {
	var out []syncRebaseEdge
	for _, e := range c.Edges {
		if e.Type != config.EdgeSync {
			continue
		}
		if e.Authority == "" {
			return nil, fmt.Errorf("runner: re-baseline requires an explicit authority on sync edge (%s,%s)", e.A, e.B)
		}
		if e.Authority != e.A && e.Authority != e.B {
			return nil, fmt.Errorf("runner: re-baseline sync edge authority %q is neither endpoint (%s,%s)", e.Authority, e.A, e.B)
		}
		var nonAuth, tf string
		if e.Authority == e.A {
			// authority is A -> regenerate B from A via transform_a_to_b.
			nonAuth = e.B
			tf = e.TransformAToB
		} else {
			// authority is B -> regenerate A from B via transform_b_to_a.
			nonAuth = e.A
			tf = e.TransformBToA
		}
		if tf == "" {
			return nil, fmt.Errorf("runner: re-baseline sync edge (%s,%s) authority %q has no authority->non-authority transform; cannot regenerate the non-authority side without writing the authority", e.A, e.B, e.Authority)
		}
		out = append(out, syncRebaseEdge{authority: e.Authority, nonAuthority: nonAuth, transform: tf})
	}
	return out, nil
}

// ReBaseline records an already-consistent sync pair as the run's baseline and
// refreshes the derive-from exports — WITHOUT executing the sync edge's
// transforms and WITHOUT writing the authority side.
//
// Why no sync transform is run. The authority->non-authority transform (here
// db-to-md, an exec shelling to the project's `workable-items` binary) opens the
// authority store READ-WRITE and mutates the authority file as a side effect of
// its own file handle — a write the engine's store-level guard CANNOT intercept
// (it happens outside store.Set). The only way to guarantee the authority file
// stays byte-identical is to NEVER invoke that transform. Re-baseline therefore
// treats BOTH sync endpoints as already-consistent INPUTS (the documented
// precondition: the DB and Issues.md are bidirectional views of the same data,
// db-to-md is a 0-diff no-op on content), records their CURRENT content hashes
// as the baseline, and recomputes ONLY the derive-from sub-graph — whose
// transforms read the non-authority markdown (and the summary) and never touch
// the DB. If the two endpoints were NOT already consistent, that is a genuine
// conflict the operator must resolve with the real `workable-items` tooling
// (§11.4.6) — re-baseline neither hides nor silently merges it; it only records
// the agreed baseline.
//
// Safety invariant. ReBaseline runs ONLY derive-from transforms and writes ONLY
// derive-from target nodes. The authority node and the non-authority node are
// inputs; the write-guard store additionally refuses any store-level Set against
// the authority node as a belt-and-suspenders. The authority file is provably
// only ever READ.
//
// Flow:
//  1. Collect sync edges + their authority/non-authority split. No sync edge =>
//     nothing to re-baseline (caller should use plain sync/verify).
//  2. Build the overlay graph = the derive-from edges ONLY (sync edges dropped,
//     no synthetic authority->non-authority edge added). Both sync endpoints are
//     inputs.
//  3. Run a guarded recompute that may write ONLY derive-from targets; the
//     authority node is write-guarded.
//  4. Fold every node's CURRENT hash (authority + non-authority read hashes,
//     plus refreshed export hashes) into st, so the next plain `verify`/`sync`
//     sees an in-sync, conflict-free baseline.
func (p *Prepared) ReBaseline(st *state.State) (*ReBaselineResult, error) {
	syncEdges, err := collectSyncRebaseEdges(p.Context)
	if err != nil {
		return nil, err
	}
	if len(syncEdges) == 0 {
		return nil, errors.New("runner: ReBaseline called on a context with no sync edges (use sync/verify)")
	}

	protected := make(map[string]bool, len(syncEdges))
	nonAuthSet := make(map[string]bool, len(syncEdges))
	for _, se := range syncEdges {
		protected[se.authority] = true
		nonAuthSet[se.nonAuthority] = true
	}
	// A non-authority node that is ALSO some edge's authority would be both
	// written and protected — an inconsistent config we refuse rather than
	// guess (§11.4.6).
	for _, se := range syncEdges {
		if protected[se.nonAuthority] {
			return nil, fmt.Errorf("runner: re-baseline cannot proceed — node %q is both a sync authority and a non-authority target", se.nonAuthority)
		}
	}

	// (2) transform map: derive-from transforms ONLY. Drop any transform keyed
	// by a sync endpoint (authority OR non-authority) so the DB-writing sync
	// exec transform is NEVER invoked; both endpoints are treated as inputs.
	overlayTransforms := make(map[string]graph.Transform, len(p.Transforms))
	for id, tf := range p.Transforms {
		if protected[id] || nonAuthSet[id] {
			continue
		}
		overlayTransforms[id] = tf
	}

	// (3) overlay graph: copy nodes, keep ONLY derive-from edges. Sync edges are
	// dropped (so graph.ResolveSync never runs → no conflict), and NO synthetic
	// authority->non-authority edge is added (so the sync transform is never a
	// candidate). Both sync endpoints are inputs whose current on-disk content is
	// the recorded baseline.
	overlay := graph.New()
	for _, id := range p.Graph.Nodes() {
		n := p.Graph.Node(id)
		// Carry the hydrated baseline hash so the dirty-set is computed against
		// the last committed run, identical to Prepare's hydration.
		if aerr := overlay.AddNode(&graph.Node{ID: n.ID, Kind: n.Kind, Path: n.Path, Hash: n.Hash}); aerr != nil {
			return nil, fmt.Errorf("runner: re-baseline overlay add node %q: %w", id, aerr)
		}
	}
	for _, e := range p.Graph.Edges() {
		if e.Type == graph.EdgeDeriveFrom {
			if aerr := overlay.AddEdge(graph.Edge{From: e.From, To: e.To, Type: graph.EdgeDeriveFrom}); aerr != nil {
				return nil, fmt.Errorf("runner: re-baseline overlay add derive edge %s->%s: %w", e.From, e.To, aerr)
			}
		}
	}

	// (4) Force the derive-from exports to refresh by marking the non-authority
	// node's baseline cold, so any derive-from target reading it is recomputed.
	// The non-authority CONTENT is only read; this is a baseline nudge, not a
	// write. The authority node is left as-is (read-only input).
	for _, se := range syncEdges {
		if n := overlay.Node(se.nonAuthority); n != nil {
			n.Hash = ""
		}
	}

	// Run against the write-guard store (authority nodes are read-only at the
	// store level). The structural safety is that overlayTransforms contains NO
	// sync transform, so the DB-writing exec is never executed; the guard is the
	// additional belt-and-suspenders against any engine-level Set to the
	// authority.
	guard := newWriteGuardStore(p.Store, protected)
	res, rerr := runGuarded(overlay, p.Store, guard, p.Hasher, overlayTransforms)
	if rerr != nil {
		return nil, rerr
	}

	out := &ReBaselineResult{Status: res.Status, Err: res.Err, Committed: res.Committed}
	for id := range nonAuthSet {
		out.Regenerated = append(out.Regenerated, id)
	}
	sort.Strings(out.Regenerated)
	for id := range protected {
		out.ProtectedNodes = append(out.ProtectedNodes, id)
	}
	sort.Strings(out.ProtectedNodes)

	// (5) fold fresh hashes into state on a clean (committed/in-sync) run.
	if (res.Status == orchestrator.StatusCommitted || res.Status == orchestrator.StatusInSync) && res.Recompute != nil {
		// Start from the existing baseline so nodes the overlay did not touch
		// keep their recorded hash, then overlay the fresh hashes.
		merged := st.Hashes(p.Context.Name)
		for id, h := range res.Recompute.NewHashes {
			merged[id] = h
		}
		st.SetHashes(p.Context.Name, merged)
	}
	return out, nil
}

// runGuarded is orchestrator.Run with the write target being a guarded store:
// recompute stages into a wrapper, and on success the staged writes are drained
// through the writeGuardStore (which refuses protected-node writes). It mirrors
// orchestrator.Run's atomicity (stage-then-commit, roll back on any error) but
// commits through the guard so an authority-node write is impossible.
func runGuarded(g *graph.Graph, real *adapter.FileStore, guard *writeGuardStore, h graph.Hasher, transforms map[string]graph.Transform) (*orchestrator.Result, error) {
	if g == nil || real == nil || guard == nil || h == nil {
		return nil, errors.New("runner: runGuarded requires non-nil graph, store, guard, hasher")
	}
	if err := g.Validate(); err != nil {
		var ce *graph.CycleError
		if errors.As(err, &ce) {
			return &orchestrator.Result{Status: orchestrator.StatusCycle, Err: ce}, nil
		}
		return nil, fmt.Errorf("runner: runGuarded validate: %w", err)
	}

	// Stage recompute against an in-memory staging wrapper over the REAL store
	// for reads (so transforms see live content) but capture writes.
	staging := newStagingForGuard(real)
	rres, rerr := g.Recompute(staging, h, transforms)
	if rerr != nil {
		var conflict *graph.ConflictError
		if errors.As(rerr, &conflict) {
			return &orchestrator.Result{Status: orchestrator.StatusConflict, Err: conflict}, nil
		}
		return &orchestrator.Result{Status: orchestrator.StatusRolledBack, Err: rerr}, nil
	}
	if len(staging.order) == 0 {
		g.CommitHashes(rres)
		return &orchestrator.Result{Status: orchestrator.StatusInSync, Recompute: rres}, nil
	}

	// Commit phase: drain staged writes through the GUARD. A protected-node
	// write returns *ProtectedWriteError -> roll back (report what landed).
	committed := make([]string, 0, len(staging.order))
	for _, id := range staging.order {
		if err := guard.Set(id, staging.staged[id]); err != nil {
			return &orchestrator.Result{
				Status:    orchestrator.StatusRolledBack,
				Recompute: rres,
				Committed: committed,
				Err:       fmt.Errorf("runner: runGuarded commit %q failed after %d committed: %w", id, len(committed), err),
			}, nil
		}
		committed = append(committed, id)
	}
	sort.Strings(committed)
	g.CommitHashes(rres)
	return &orchestrator.Result{Status: orchestrator.StatusCommitted, Recompute: rres, Committed: committed}, nil
}

// stagingForGuard is the runner-local staging store used by runGuarded: it
// reads through to the real store and captures Set into an ordered buffer,
// never touching live files until the guarded commit phase. It re-exposes the
// per-node hasher so binary/text hashing stays consistent.
type stagingForGuard struct {
	real   *adapter.FileStore
	staged map[string][]byte
	order  []string
}

func newStagingForGuard(real *adapter.FileStore) *stagingForGuard {
	return &stagingForGuard{real: real, staged: make(map[string][]byte)}
}

func (s *stagingForGuard) Get(id string) ([]byte, error) {
	if v, ok := s.staged[id]; ok {
		cp := make([]byte, len(v))
		copy(cp, v)
		return cp, nil
	}
	return s.real.Get(id)
}

func (s *stagingForGuard) Set(id string, content []byte) error {
	if _, seen := s.staged[id]; !seen {
		s.order = append(s.order, id)
	}
	cp := make([]byte, len(content))
	copy(cp, content)
	s.staged[id] = cp
	return nil
}

func (s *stagingForGuard) NodeHasher(id string) (graph.Hasher, error) {
	return s.real.Hasher(id)
}

var _ graph.Store = (*stagingForGuard)(nil)
var _ graph.PerNodeHasher = (*stagingForGuard)(nil)
