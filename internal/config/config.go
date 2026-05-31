// Package config implements Phase 4 of Docs Chain: the per-context YAML
// loader and validator. It parses a `.docs_chain/contexts/<name>.yaml` file
// (per docs/CONFIG_SCHEMA.md) into the in-memory model, builds a
// graph.Graph from it, validates it (cycles, dangling refs, unknown
// transforms, duplicate ids, fingerprint/sync rules), and exposes the
// transform definitions the CLI binds to real adapters at run time.
//
// This package is pure parsing + validation: it does NOT touch live
// artefacts, run transforms, or shell out. The CLI (cmd/docs_chain) wires a
// loaded Context to the Phase 1-3 engine + Phase 2 adapters.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"digital.vasic.docs_chain/internal/graph"
)

// Context is one parsed + validated chain definition (one YAML file).
type Context struct {
	// Name is the `context:` field.
	Name string
	// Description is the optional `description:` field.
	Description string
	// Nodes maps node id -> NodeSpec, in deterministic (sorted-id) order via
	// NodeIDs().
	Nodes map[string]NodeSpec
	// Edges is the ordered list of parsed edges.
	Edges []EdgeSpec
	// Transforms maps transform name -> TransformSpec.
	Transforms map[string]TransformSpec
	// SourcePath is the file this context was loaded from (diagnostics).
	SourcePath string
}

// NodeSpec is a single node declaration.
type NodeSpec struct {
	ID      string
	Kind    graph.NodeKind
	Path    string
	Members string   // fingerprint nodes only: glob enumerating members
	Exclude []string // fingerprint nodes only: member globs to exclude
}

// EdgeSpec is a single edge declaration. Type selects the shape.
type EdgeSpec struct {
	Type EdgeType
	// derive-from fields:
	From []string // one or more source node ids (multi-input transform)
	To   string
	// shared:
	Transform string
	// sync fields:
	A, B          string
	Authority     string
	TransformAToB string
	TransformBToA string
}

// EdgeType mirrors the documented edge `type` literal.
type EdgeType string

const (
	EdgeDeriveFrom EdgeType = "derive-from"
	EdgeSync       EdgeType = "sync"
)

// TransformSpec is either a builtin or an exec command (mutually exclusive).
type TransformSpec struct {
	Name    string
	Builtin string   // one of the allowed builtin names, or ""
	Exec    string   // command/script path, or ""
	Args    []string // exec args appended after the IO paths docs_chain passes
}

// IsBuiltin reports whether this transform is a builtin.
func (t TransformSpec) IsBuiltin() bool { return t.Builtin != "" }

// Allowed builtin transform names (CONFIG_SCHEMA §5.1).
const (
	BuiltinPandocHTML         = "pandoc-html"
	BuiltinWeasyprintPDF      = "weasyprint-pdf"
	BuiltinPandocDOCX         = "pandoc-docx"
	BuiltinColorizeHTML       = "colorize-html"
	BuiltinGenSummary         = "gen-summary"
	BuiltinMDToSQLite         = "md-to-sqlite"
	BuiltinSQLiteToMD         = "sqlite-to-md"
	BuiltinMembersFingerprint = "members-fingerprint"
)

// knownBuiltins is the closed set the loader validates against. Note that
// being a *known* builtin name and being *wired to a runnable transform* in
// the CLI are different: the loader accepts every documented name; the CLI's
// transform binder reports honestly which it can execute today.
var knownBuiltins = map[string]bool{
	BuiltinPandocHTML:         true,
	BuiltinWeasyprintPDF:      true,
	BuiltinPandocDOCX:         true,
	BuiltinColorizeHTML:       true,
	BuiltinGenSummary:         true,
	BuiltinMDToSQLite:         true,
	BuiltinSQLiteToMD:         true,
	BuiltinMembersFingerprint: true,
}

// NodeIDs returns the context's node ids in deterministic sorted order.
func (c *Context) NodeIDs() []string {
	ids := make([]string, 0, len(c.Nodes))
	for id := range c.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// rawContext is the on-disk YAML shape, parsed loosely then normalized +
// validated into Context. Keeping it separate lets us give precise field
// errors and accept the documented `from: <id>` OR `from: [<id>, ...]` union.
type rawContext struct {
	Context     string                  `yaml:"context"`
	Description string                  `yaml:"description"`
	Nodes       map[string]rawNode      `yaml:"nodes"`
	Edges       []rawEdge               `yaml:"edges"`
	Transforms  map[string]rawTransform `yaml:"transforms"`
}

type rawNode struct {
	Kind    string   `yaml:"kind"`
	Path    string   `yaml:"path"`
	Members string   `yaml:"members"`
	Exclude []string `yaml:"exclude"`
}

type rawEdge struct {
	Type string `yaml:"type"`
	// derive-from
	From yaml.Node `yaml:"from"` // string OR sequence
	To   string    `yaml:"to"`
	// shared
	Transform string `yaml:"transform"`
	// sync
	A             string `yaml:"a"`
	B             string `yaml:"b"`
	Authority     string `yaml:"authority"`
	TransformAToB string `yaml:"transform_a_to_b"`
	TransformBToA string `yaml:"transform_b_to_a"`
}

type rawTransform struct {
	Builtin string   `yaml:"builtin"`
	Exec    string   `yaml:"exec"`
	Args    []string `yaml:"args"`
}

// Load reads, parses, normalizes and validates a single context YAML file.
// A validation failure returns a *ConfigError (the CLI maps it to exit 4).
func Load(path string) (*Context, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	c, err := Parse(b, path)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// LoadDir loads every *.yaml / *.yml file under dir, returning the contexts
// sorted by name. Each is fully validated. The first failure aborts.
func LoadDir(dir string) ([]*Context, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("config: read contexts dir %q: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".yaml" || ext == ".yml" {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	var out []*Context
	for _, f := range files {
		c, lerr := Load(f)
		if lerr != nil {
			return nil, lerr
		}
		out = append(out, c)
	}
	return out, nil
}

// Parse parses YAML bytes into a Context WITHOUT running Validate (so callers
// can inspect a structurally-parsed-but-invalid context). srcPath is recorded
// for diagnostics.
func Parse(b []byte, srcPath string) (*Context, error) {
	var raw rawContext
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true) // reject unknown top-level/struct fields loudly
	if err := dec.Decode(&raw); err != nil {
		return nil, &ConfigError{Path: srcPath, Reason: fmt.Sprintf("YAML parse error: %v", err)}
	}

	c := &Context{
		Name:        raw.Context,
		Description: raw.Description,
		Nodes:       make(map[string]NodeSpec, len(raw.Nodes)),
		Transforms:  make(map[string]TransformSpec, len(raw.Transforms)),
		SourcePath:  srcPath,
	}

	for id, rn := range raw.Nodes {
		c.Nodes[id] = NodeSpec{
			ID:      id,
			Kind:    graph.NodeKind(rn.Kind),
			Path:    rn.Path,
			Members: rn.Members,
			Exclude: rn.Exclude,
		}
	}

	for name, rt := range raw.Transforms {
		c.Transforms[name] = TransformSpec{
			Name:    name,
			Builtin: rt.Builtin,
			Exec:    rt.Exec,
			Args:    rt.Args,
		}
	}

	for i, re := range raw.Edges {
		es := EdgeSpec{
			Type:          EdgeType(re.Type),
			To:            re.To,
			Transform:     re.Transform,
			A:             re.A,
			B:             re.B,
			Authority:     re.Authority,
			TransformAToB: re.TransformAToB,
			TransformBToA: re.TransformBToA,
		}
		from, ferr := decodeFrom(re.From)
		if ferr != nil {
			return nil, &ConfigError{Path: srcPath, Reason: fmt.Sprintf("edge[%d] `from`: %v", i, ferr)}
		}
		es.From = from
		c.Edges = append(c.Edges, es)
	}

	return c, nil
}

// decodeFrom accepts the `from` union: a scalar string, or a sequence of
// strings (multi-input transform). An absent `from` yields nil (legal for a
// sync edge; validation catches a missing one on a derive-from edge).
func decodeFrom(n yaml.Node) ([]string, error) {
	if n.IsZero() {
		return nil, nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return nil, err
		}
		if s == "" {
			return nil, nil
		}
		return []string{s}, nil
	case yaml.SequenceNode:
		var ss []string
		if err := n.Decode(&ss); err != nil {
			return nil, err
		}
		return ss, nil
	default:
		return nil, fmt.Errorf("must be a node id or a list of node ids")
	}
}
