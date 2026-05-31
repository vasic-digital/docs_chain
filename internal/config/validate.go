package config

import (
	"fmt"
	"sort"
	"strings"

	"digital.vasic.docs_chain/internal/graph"
)

// ConfigError is the typed validation/parse failure. The CLI maps it to
// exit 4 (cycle/config-error) per docs/USER_GUIDE.md §7.
type ConfigError struct {
	Path   string // the context file
	Reason string
}

func (e *ConfigError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("config: %s: %s", e.Path, e.Reason)
	}
	return "config: " + e.Reason
}

// validKinds is the closed set of node kinds (CONFIG_SCHEMA §3.1).
var validKinds = map[graph.NodeKind]bool{
	graph.KindMarkdown:      true,
	graph.KindHTML:          true,
	graph.KindPDF:           true,
	graph.KindDOCX:          true,
	graph.KindSQLite:        true,
	graph.KindSummary:       true,
	graph.KindStatus:        true,
	graph.KindStatusSummary: true,
	graph.KindFingerprint:   true,
}

// Validate runs every rule in CONFIG_SCHEMA §8 plus the cycle check, returning
// the FIRST violation as a *ConfigError. (Duplicate-id detection is inherent:
// YAML map keys are unique, so a literal duplicate id is rejected by the YAML
// decoder before reaching here; this is documented in TestParse_DuplicateKey.)
func (c *Context) Validate() error {
	fail := func(reason string) error { return &ConfigError{Path: c.SourcePath, Reason: reason} }

	// (1) context name present.
	if strings.TrimSpace(c.Name) == "" {
		return fail("`context` is empty or missing")
	}
	// nodes present.
	if len(c.Nodes) == 0 {
		return fail("`nodes` must have at least one entry")
	}

	// node-level rules: valid kind, non-empty path, fingerprint members.
	for _, id := range c.NodeIDs() {
		n := c.Nodes[id]
		if !validKinds[n.Kind] {
			return fail(fmt.Sprintf("node %q has unknown kind %q", id, n.Kind))
		}
		if strings.TrimSpace(n.Path) == "" {
			return fail(fmt.Sprintf("node %q has empty `path`", id))
		}
		if n.Kind == graph.KindFingerprint && strings.TrimSpace(n.Members) == "" {
			return fail(fmt.Sprintf("fingerprint node %q lacks `members`", id))
		}
	}

	// transform-level rules: exactly one of builtin/exec; known builtin name.
	for _, name := range c.transformNames() {
		t := c.Transforms[name]
		hasBuiltin := strings.TrimSpace(t.Builtin) != ""
		hasExec := strings.TrimSpace(t.Exec) != ""
		switch {
		case hasBuiltin && hasExec:
			return fail(fmt.Sprintf("transform %q sets both `builtin` and `exec` (mutually exclusive)", name))
		case !hasBuiltin && !hasExec:
			return fail(fmt.Sprintf("transform %q sets neither `builtin` nor `exec`", name))
		}
		if hasBuiltin && !knownBuiltins[t.Builtin] {
			return fail(fmt.Sprintf("transform %q references unknown builtin %q", name, t.Builtin))
		}
		if len(t.Args) > 0 && !hasExec {
			return fail(fmt.Sprintf("transform %q sets `args` without `exec`", name))
		}
	}

	// edge-level rules: endpoints exist, transforms exist, sync authority.
	for i, e := range c.Edges {
		ref := fmt.Sprintf("edge[%d]", i)
		switch e.Type {
		case EdgeDeriveFrom:
			if len(e.From) == 0 {
				return fail(ref + " (derive-from) has no `from`")
			}
			for _, f := range e.From {
				if _, ok := c.Nodes[f]; !ok {
					return fail(fmt.Sprintf("%s references unknown node %q in `from`", ref, f))
				}
			}
			if _, ok := c.Nodes[e.To]; !ok {
				return fail(fmt.Sprintf("%s references unknown node %q in `to`", ref, e.To))
			}
			if strings.TrimSpace(e.Transform) == "" {
				return fail(ref + " (derive-from) has no `transform`")
			}
			if _, ok := c.Transforms[e.Transform]; !ok {
				return fail(fmt.Sprintf("%s references unknown transform %q", ref, e.Transform))
			}
		case EdgeSync:
			if _, ok := c.Nodes[e.A]; !ok {
				return fail(fmt.Sprintf("%s references unknown node %q in `a`", ref, e.A))
			}
			if _, ok := c.Nodes[e.B]; !ok {
				return fail(fmt.Sprintf("%s references unknown node %q in `b`", ref, e.B))
			}
			if e.A == e.B {
				return fail(fmt.Sprintf("%s sync edge has a == b (%q)", ref, e.A))
			}
			if e.Authority != e.A && e.Authority != e.B {
				return fail(fmt.Sprintf("%s sync `authority` %q is neither `a` (%q) nor `b` (%q)", ref, e.Authority, e.A, e.B))
			}
			// transforms, when named, must exist.
			for _, tn := range []string{e.TransformAToB, e.TransformBToA} {
				if tn == "" {
					continue
				}
				if _, ok := c.Transforms[tn]; !ok {
					return fail(fmt.Sprintf("%s references unknown transform %q", ref, tn))
				}
			}
		default:
			return fail(fmt.Sprintf("%s has unknown `type` %q (want derive-from | sync)", ref, e.Type))
		}
	}

	// (7) cycle check + structural soundness: build the graph and Validate.
	if _, err := c.BuildGraph(); err != nil {
		return fail(err.Error())
	}
	return nil
}

// transformNames returns transform names in deterministic sorted order.
func (c *Context) transformNames() []string {
	names := make([]string, 0, len(c.Transforms))
	for n := range c.Transforms {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// BuildGraph constructs a graph.Graph from the (structurally parsed) context.
// It adds nodes in sorted-id order for determinism, then edges. A multi-input
// derive-from edge becomes one graph.Edge per source (the engine's adjacency
// is per (from,to) pair). A sync edge becomes one graph.Edge of type
// EdgeSync. BuildGraph returns the underlying graph error (which Validate
// wraps in a ConfigError); graph.AddEdge/Validate surface cycles + bad
// authorities.
func (c *Context) BuildGraph() (*graph.Graph, error) {
	g := graph.New()
	for _, id := range c.NodeIDs() {
		n := c.Nodes[id]
		if err := g.AddNode(&graph.Node{ID: n.ID, Kind: n.Kind, Path: n.Path}); err != nil {
			return nil, err
		}
	}
	for _, e := range c.Edges {
		switch e.Type {
		case EdgeDeriveFrom:
			for _, f := range e.From {
				if err := g.AddEdge(graph.Edge{From: f, To: e.To, Type: graph.EdgeDeriveFrom}); err != nil {
					return nil, err
				}
			}
		case EdgeSync:
			if err := g.AddEdge(graph.Edge{From: e.A, To: e.B, Type: graph.EdgeSync, Authority: e.Authority}); err != nil {
				return nil, err
			}
		}
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return g, nil
}
