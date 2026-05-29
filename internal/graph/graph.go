// Package graph implements the docs_chain DAG model: nodes, edges, Kahn
// topological ordering with explicit cycle detection, content-hash
// incremental recomputation with Salsa-style early cutoff, and the
// bidirectional sync-edge authority/conflict contract.
//
// Phase 1 is pure and in-memory: transforms are injected functions, no
// filesystem, no SQLite, no watcher. See DESIGN.md §2–§4.
package graph

import (
	"errors"
	"fmt"
	"sort"
)

// NodeKind enumerates the typed node kinds (DESIGN.md §2). Phase 1 treats
// kind opaquely; adapters in Phase 2 attach behaviour per kind.
type NodeKind string

const (
	KindMarkdown      NodeKind = "markdown"
	KindHTML          NodeKind = "html"
	KindPDF           NodeKind = "pdf"
	KindDOCX          NodeKind = "docx"
	KindSQLite        NodeKind = "sqlite"
	KindSummary       NodeKind = "summary"
	KindStatus        NodeKind = "status"
	KindStatusSummary NodeKind = "status_summary"
	KindFingerprint   NodeKind = "fingerprint"
)

// Node is a member of a chain. Phase 1 stores the last-known content hash
// in-memory on the node.
type Node struct {
	ID   string
	Kind NodeKind
	Path string
	// Hash is the last-recorded content hash (sha256 of normalized content).
	// Empty string means "never hashed".
	Hash string
}

// EdgeType distinguishes one-way derivation from bidirectional sync.
type EdgeType string

const (
	// EdgeDeriveFrom: To is regenerated from From via a transform (one-way).
	EdgeDeriveFrom EdgeType = "derive-from"
	// EdgeSync: From and To are mutually authoritative views of the same
	// data; Authority names the side that wins a both-dirty conflict.
	EdgeSync EdgeType = "sync"
)

// Edge connects two nodes by ID. For derive-from, From is the source and To
// the derived target. For sync, From and To are the two paired endpoints and
// Authority MUST equal one of them.
type Edge struct {
	From      string
	To        string
	Type      EdgeType
	Authority string // sync edges only; node ID that wins both-dirty
}

// Graph is a directed graph of nodes and edges.
type Graph struct {
	nodes map[string]*Node
	edges []Edge
	order []string // deterministic node-insertion order for stable iteration
}

// New returns an empty Graph.
func New() *Graph {
	return &Graph{nodes: make(map[string]*Node)}
}

// AddNode registers a node. Duplicate IDs are an error.
func (g *Graph) AddNode(n *Node) error {
	if n == nil {
		return errors.New("graph: nil node")
	}
	if n.ID == "" {
		return errors.New("graph: node with empty ID")
	}
	if _, exists := g.nodes[n.ID]; exists {
		return fmt.Errorf("graph: duplicate node ID %q", n.ID)
	}
	cp := *n
	g.nodes[n.ID] = &cp
	g.order = append(g.order, n.ID)
	return nil
}

// AddEdge registers an edge. Both endpoints must already exist. Sync edges
// require Authority to be one of the two endpoints.
func (g *Graph) AddEdge(e Edge) error {
	if _, ok := g.nodes[e.From]; !ok {
		return fmt.Errorf("graph: edge from unknown node %q", e.From)
	}
	if _, ok := g.nodes[e.To]; !ok {
		return fmt.Errorf("graph: edge to unknown node %q", e.To)
	}
	switch e.Type {
	case EdgeDeriveFrom:
		// ok
	case EdgeSync:
		if e.Authority != e.From && e.Authority != e.To {
			return fmt.Errorf("graph: sync edge (%s,%s) authority %q must be one endpoint",
				e.From, e.To, e.Authority)
		}
	default:
		return fmt.Errorf("graph: unknown edge type %q", e.Type)
	}
	g.edges = append(g.edges, e)
	return nil
}

// Node returns the stored node by ID (nil if absent).
func (g *Graph) Node(id string) *Node { return g.nodes[id] }

// Nodes returns node IDs in insertion order (a stable, deterministic view).
func (g *Graph) Nodes() []string {
	out := make([]string, len(g.order))
	copy(out, g.order)
	return out
}

// Edges returns a copy of the edge list.
func (g *Graph) Edges() []Edge {
	out := make([]Edge, len(g.edges))
	copy(out, g.edges)
	return out
}

// Validate runs structural checks: every edge endpoint exists, sync
// authorities are valid, and the derive-from sub-graph is acyclic.
func (g *Graph) Validate() error {
	for _, e := range g.edges {
		if _, ok := g.nodes[e.From]; !ok {
			return fmt.Errorf("graph: edge from unknown node %q", e.From)
		}
		if _, ok := g.nodes[e.To]; !ok {
			return fmt.Errorf("graph: edge to unknown node %q", e.To)
		}
		if e.Type == EdgeSync && e.Authority != e.From && e.Authority != e.To {
			return fmt.Errorf("graph: sync edge (%s,%s) authority %q invalid",
				e.From, e.To, e.Authority)
		}
	}
	if _, err := g.TopoOrder(); err != nil {
		return err
	}
	return nil
}

// CycleError reports a cycle in the derive-from sub-graph, identifying the
// nodes that could not be ordered.
type CycleError struct {
	// Cycle is the set of node IDs participating in (or downstream of) the
	// cycle, in deterministic sorted order.
	Cycle []string
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("graph: cycle detected in derive-from sub-graph among nodes %v", e.Cycle)
}

// deriveAdjacency builds out-edges and in-degrees over ONLY derive-from
// edges. Sync edges are collapsed to their authority (DESIGN.md §4), so they
// never contribute to the propagation DAG's cyclicity.
func (g *Graph) deriveAdjacency() (out map[string][]string, indeg map[string]int) {
	out = make(map[string][]string, len(g.nodes))
	indeg = make(map[string]int, len(g.nodes))
	for id := range g.nodes {
		indeg[id] = 0
	}
	for _, e := range g.edges {
		if e.Type != EdgeDeriveFrom {
			continue
		}
		out[e.From] = append(out[e.From], e.To)
		indeg[e.To]++
	}
	// Sort adjacency for deterministic Kahn ordering.
	for k := range out {
		sort.Strings(out[k])
	}
	return out, indeg
}

// TopoOrder returns the nodes in Kahn topological order over the derive-from
// sub-graph. It is deterministic: at each step the lowest-ID ready node is
// chosen. A residual (non-empty after processing) signals a cycle, returned
// as *CycleError.
func (g *Graph) TopoOrder() ([]string, error) {
	out, indeg := g.deriveAdjacency()

	// Seed the ready set with all zero-in-degree nodes, sorted.
	ready := make([]string, 0, len(indeg))
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	order := make([]string, 0, len(g.nodes))
	for len(ready) > 0 {
		// Pop the lowest-ID ready node (ready stays sorted).
		n := ready[0]
		ready = ready[1:]
		order = append(order, n)
		for _, m := range out[n] {
			indeg[m]--
			if indeg[m] == 0 {
				ready = insertSorted(ready, m)
			}
		}
	}

	if len(order) != len(g.nodes) {
		// Residual nodes (in-degree never reached 0) are in or downstream of
		// a cycle.
		var cyc []string
		for id, d := range indeg {
			if d > 0 {
				cyc = append(cyc, id)
			}
		}
		sort.Strings(cyc)
		return nil, &CycleError{Cycle: cyc}
	}
	return order, nil
}

// insertSorted inserts s into the already-sorted slice, preserving order.
func insertSorted(slice []string, s string) []string {
	i := sort.SearchStrings(slice, s)
	slice = append(slice, "")
	copy(slice[i+1:], slice[i:])
	slice[i] = s
	return slice
}
