package graph

import (
	"errors"
	"testing"

	"digital.vasic.docs_chain/internal/hash"
)

// chainGraph builds src -> mid -> leaf over derive-from edges.
func chainGraph(t *testing.T) *Graph {
	t.Helper()
	g := New()
	mustAdd(t, g, "src", "mid", "leaf")
	if err := g.AddEdge(Edge{From: "src", To: "mid", Type: EdgeDeriveFrom}); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge(Edge{From: "mid", To: "leaf", Type: EdgeDeriveFrom}); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRecompute_RequiresStoreAndHasher(t *testing.T) {
	g := chainGraph(t)
	if _, err := g.Recompute(nil, hash.NewByteContentHasher(), nil); err == nil {
		t.Fatal("expected error on nil store")
	}
	if _, err := g.Recompute(NewMemStore(nil), nil, nil); err == nil {
		t.Fatal("expected error on nil hasher")
	}
}

// TestRecompute_EarlyCutoff: a dirty source whose derived node regenerates to
// UNCHANGED content must PRUNE — the leaf transform must NOT run (Salsa
// early-cutoff). This is the core §4 guarantee that md->db->summary->exports
// cannot infinite-loop and that unchanged intermediates cut downstream work.
func TestRecompute_EarlyCutoff(t *testing.T) {
	h := hash.NewByteContentHasher()
	g := chainGraph(t)

	fixedMidOut := []byte("MID-STABLE\n")
	leafOut := []byte("LEAF-STABLE\n")

	// Baseline: mid + leaf already at the hashes their transforms would yield.
	g.Node("mid").Hash = h.Hash(fixedMidOut)
	g.Node("leaf").Hash = h.Hash(leafOut)
	// src has no stored hash -> it will be seen as dirty.

	store := NewMemStore(map[string][]byte{
		"src":  []byte("src content changed"),
		"mid":  fixedMidOut,
		"leaf": leafOut,
	})

	leafCalls := 0
	transforms := map[string]Transform{
		"mid":  func(map[string][]byte) ([]byte, error) { return fixedMidOut, nil },
		"leaf": func(map[string][]byte) ([]byte, error) { leafCalls++; return leafOut, nil },
	}

	res, err := g.Recompute(store, h, transforms)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	if leafCalls != 0 {
		t.Fatalf("early-cutoff failed: leaf transform ran %d times (expected 0 — mid pruned)", leafCalls)
	}
	if !contains(res.Pruned, "mid") {
		t.Fatalf("expected mid in Pruned, got Pruned=%v", res.Pruned)
	}
	if contains(res.Recomputed, "mid") || contains(res.Recomputed, "leaf") {
		t.Fatalf("nothing should be Recomputed on a pure prune, got %v", res.Recomputed)
	}
}

// TestRecompute_PropagatesOnChange: when the intermediate's output genuinely
// changes, the downstream transform DOES run.
func TestRecompute_PropagatesOnChange(t *testing.T) {
	h := hash.NewByteContentHasher()
	g := chainGraph(t)

	// mid baseline is OLD; transform yields NEW -> mid changes -> leaf runs.
	g.Node("mid").Hash = h.Hash([]byte("MID-OLD\n"))
	g.Node("leaf").Hash = h.Hash([]byte("LEAF-OLD\n"))

	store := NewMemStore(map[string][]byte{
		"src":  []byte("trigger"),
		"mid":  []byte("MID-OLD\n"),
		"leaf": []byte("LEAF-OLD\n"),
	})

	leafCalls := 0
	transforms := map[string]Transform{
		"mid":  func(map[string][]byte) ([]byte, error) { return []byte("MID-NEW\n"), nil },
		"leaf": func(ins map[string][]byte) ([]byte, error) { leafCalls++; return []byte("LEAF-NEW\n"), nil },
	}

	res, err := g.Recompute(store, h, transforms)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	if leafCalls != 1 {
		t.Fatalf("leaf transform ran %d times, expected 1", leafCalls)
	}
	if !contains(res.Recomputed, "mid") || !contains(res.Recomputed, "leaf") {
		t.Fatalf("expected mid+leaf Recomputed, got %v", res.Recomputed)
	}
	// order must be topo: mid before leaf
	if indexOf(res.Recomputed, "mid") > indexOf(res.Recomputed, "leaf") {
		t.Fatalf("Recomputed not in topo order: %v", res.Recomputed)
	}
	g.CommitHashes(res)
	if g.Node("mid").Hash != h.Hash([]byte("MID-NEW\n")) {
		t.Fatal("CommitHashes did not update mid baseline")
	}
}

func TestRecompute_DirtySourceMissingTransform(t *testing.T) {
	h := hash.NewByteContentHasher()
	g := chainGraph(t)
	store := NewMemStore(map[string][]byte{"src": []byte("x")})
	// no transform for mid, but src is dirty -> incomplete chain error
	_, err := g.Recompute(store, h, map[string]Transform{})
	if err == nil {
		t.Fatal("expected error: dirty source with no transform")
	}
}

func TestResolveSync_AuthorityAndConflict(t *testing.T) {
	g := New()
	mustAdd(t, g, "md", "db")
	if err := g.AddEdge(Edge{From: "md", To: "db", Type: EdgeSync, Authority: "db"}); err != nil {
		t.Fatal(err)
	}

	// both dirty -> ConflictError
	_, _, err := g.ResolveSync(map[string]bool{"md": true, "db": true})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConflictError on both-dirty, got %v", err)
	}

	// only md dirty -> md is source, db regenerated
	src, tgt, err := g.ResolveSync(map[string]bool{"md": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(src) != 1 || src[0] != "md" || len(tgt) != 1 || tgt[0] != "db" {
		t.Fatalf("md-dirty: src=%v tgt=%v, want [md] [db]", src, tgt)
	}

	// only db dirty -> db is source, md regenerated
	src, tgt, err = g.ResolveSync(map[string]bool{"db": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(src) != 1 || src[0] != "db" || len(tgt) != 1 || tgt[0] != "md" {
		t.Fatalf("db-dirty: src=%v tgt=%v, want [db] [md]", src, tgt)
	}

	// neither dirty -> no action
	src, tgt, err = g.ResolveSync(map[string]bool{})
	if err != nil || len(src) != 0 || len(tgt) != 0 {
		t.Fatalf("neither-dirty: src=%v tgt=%v err=%v, want empty/nil", src, tgt, err)
	}
}

// TestRecompute_SyncConflictAborts: a both-dirty sync pair aborts the whole
// run with *ConflictError and writes nothing.
func TestRecompute_SyncConflictAborts(t *testing.T) {
	h := hash.NewByteContentHasher()
	g := New()
	mustAdd(t, g, "md", "db")
	if err := g.AddEdge(Edge{From: "md", To: "db", Type: EdgeSync, Authority: "db"}); err != nil {
		t.Fatal(err)
	}
	// both differ from their (empty) baselines -> both dirty
	store := NewMemStore(map[string][]byte{"md": []byte("a"), "db": []byte("b")})
	_, err := g.Recompute(store, h, nil)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConflictError, got %v", err)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
