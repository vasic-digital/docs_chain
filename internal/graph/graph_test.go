package graph

import (
	"errors"
	"reflect"
	"testing"
)

// helper: build a node quickly.
func n(id string) *Node { return &Node{ID: id, Kind: KindMarkdown, Path: id + ".md"} }

func mustAdd(t *testing.T, g *Graph, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := g.AddNode(n(id)); err != nil {
			t.Fatalf("AddNode(%q): %v", id, err)
		}
	}
}

func TestAddNode_DuplicateAndEmpty(t *testing.T) {
	g := New()
	if err := g.AddNode(nil); err == nil {
		t.Fatal("expected error on nil node")
	}
	if err := g.AddNode(&Node{ID: ""}); err == nil {
		t.Fatal("expected error on empty ID")
	}
	if err := g.AddNode(n("a")); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := g.AddNode(n("a")); err == nil {
		t.Fatal("expected duplicate-ID error")
	}
}

func TestAddEdge_UnknownEndpointsAndSyncAuthority(t *testing.T) {
	g := New()
	mustAdd(t, g, "a", "b")
	if err := g.AddEdge(Edge{From: "a", To: "x", Type: EdgeDeriveFrom}); err == nil {
		t.Fatal("expected error: edge to unknown node")
	}
	if err := g.AddEdge(Edge{From: "x", To: "a", Type: EdgeDeriveFrom}); err == nil {
		t.Fatal("expected error: edge from unknown node")
	}
	// sync edge: authority must be one endpoint
	if err := g.AddEdge(Edge{From: "a", To: "b", Type: EdgeSync, Authority: "c"}); err == nil {
		t.Fatal("expected error: sync authority not an endpoint")
	}
	if err := g.AddEdge(Edge{From: "a", To: "b", Type: EdgeSync, Authority: "a"}); err != nil {
		t.Fatalf("valid sync edge rejected: %v", err)
	}
	// unknown edge type
	if err := g.AddEdge(Edge{From: "a", To: "b", Type: EdgeType("bogus")}); err == nil {
		t.Fatal("expected error: unknown edge type")
	}
}

func TestValidate_AcyclicOK(t *testing.T) {
	g := New()
	mustAdd(t, g, "src", "mid", "leaf")
	if err := g.AddEdge(Edge{From: "src", To: "mid", Type: EdgeDeriveFrom}); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge(Edge{From: "mid", To: "leaf", Type: EdgeDeriveFrom}); err != nil {
		t.Fatal(err)
	}
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate on acyclic graph: %v", err)
	}
}

func TestTopoOrder_CycleDetected(t *testing.T) {
	g := New()
	mustAdd(t, g, "a", "b", "c")
	// a -> b -> c -> a  (cycle)
	for _, e := range []Edge{
		{From: "a", To: "b", Type: EdgeDeriveFrom},
		{From: "b", To: "c", Type: EdgeDeriveFrom},
		{From: "c", To: "a", Type: EdgeDeriveFrom},
	} {
		if err := g.AddEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	_, err := g.TopoOrder()
	if err == nil {
		t.Fatal("expected CycleError, got nil")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *CycleError, got %T: %v", err, err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(ce.Cycle, want) {
		t.Fatalf("cycle nodes = %v, want %v", ce.Cycle, want)
	}
	if err := g.Validate(); err == nil {
		t.Fatal("Validate must reject a cyclic graph")
	}
}

// TestTopoOrder_Deterministic asserts §11.4.50 determinism: identical input
// yields identical Kahn order across repeated runs, and the order respects
// all derive-from precedence on a diamond.
func TestTopoOrder_Deterministic(t *testing.T) {
	build := func() *Graph {
		g := New()
		// Insert in a non-sorted order to prove order is content-driven, not
		// insertion-driven.
		mustAdd(t, g, "d", "b", "c", "a")
		// diamond: a -> b, a -> c, b -> d, c -> d
		for _, e := range []Edge{
			{From: "a", To: "b", Type: EdgeDeriveFrom},
			{From: "a", To: "c", Type: EdgeDeriveFrom},
			{From: "b", To: "d", Type: EdgeDeriveFrom},
			{From: "c", To: "d", Type: EdgeDeriveFrom},
		} {
			if err := g.AddEdge(e); err != nil {
				t.Fatal(err)
			}
		}
		return g
	}

	var first []string
	for i := 0; i < 3; i++ {
		ord, err := build().TopoOrder()
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if i == 0 {
			first = ord
			// precedence checks
			pos := map[string]int{}
			for p, id := range ord {
				pos[id] = p
			}
			if !(pos["a"] < pos["b"] && pos["a"] < pos["c"] && pos["b"] < pos["d"] && pos["c"] < pos["d"]) {
				t.Fatalf("topo precedence violated: %v", ord)
			}
			// lowest-ID-ready tie-break → deterministic exact order
			if !reflect.DeepEqual(ord, []string{"a", "b", "c", "d"}) {
				t.Fatalf("deterministic order = %v, want [a b c d]", ord)
			}
		} else if !reflect.DeepEqual(ord, first) {
			t.Fatalf("iter %d order %v != first %v (non-deterministic)", i, ord, first)
		}
	}
}
