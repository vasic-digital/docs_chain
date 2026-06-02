// Package runner wires a parsed config.Context to the Phase 1-3 engine: it
// builds the graph, registers Phase-2 adapters per node, binds each config
// transform to a real graph.Transform, hydrates the per-node hash baseline
// from state.json, and invokes the orchestrator (sync) or a read-only
// recompute (verify). It is the seam the CLI subcommands share.
//
// Anti-bluff posture: a builtin/exec transform is bound to the REAL adapter
// (pandoc/weasyprint shell-out, sqlite txn, members-fingerprint hash). When a
// tool is absent the bound transform returns the adapter's typed
// *ToolAbsentError unchanged — the orchestrator rolls back and the CLI
// SKIP-with-reasons instead of faking success.
package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"digital.vasic.docs_chain/internal/adapter"
	"digital.vasic.docs_chain/internal/config"
	"digital.vasic.docs_chain/internal/graph"
	"digital.vasic.docs_chain/internal/hash"
	"digital.vasic.docs_chain/internal/orchestrator"
	"digital.vasic.docs_chain/internal/state"
)

// Prepared holds everything needed to run one context: the built graph (with
// hashes hydrated from state), the FileStore of adapters, the bound transform
// map (keyed by derived/target node id, as graph.Recompute expects), and the
// project root (paths in the config are resolved relative to it).
type Prepared struct {
	Context     *config.Context
	Graph       *graph.Graph
	Store       *adapter.FileStore
	Transforms  map[string]graph.Transform
	Hasher      graph.Hasher
	ProjectRoot string
	// binders records, per derived/target node id, how to rebind its
	// transform to a different output path (used by Verify so builtin
	// producers write to a TEMP path, never the live artefact).
	binders map[string]nodeBinder
}

// nodeBinder captures the inputs needed to rebind a node's transform to an
// arbitrary output path.
type nodeBinder struct {
	spec   config.TransformSpec
	target config.NodeSpec
	srcIDs []string
}

// Prepare builds the graph + adapters + transforms for a context, resolving
// node paths against projectRoot and hydrating each node's hash baseline from
// st. It does NOT run anything.
func Prepare(c *config.Context, projectRoot string, st *state.State) (*Prepared, error) {
	g, err := c.BuildGraph()
	if err != nil {
		return nil, fmt.Errorf("runner: build graph for context %q: %w", c.Name, err)
	}

	// Hydrate hash baseline from state so the engine's dirty-set is computed
	// against the last committed run (not a cold empty baseline every time).
	baseline := st.Hashes(c.Name)
	for _, id := range g.Nodes() {
		if h, ok := baseline[id]; ok {
			if n := g.Node(id); n != nil {
				n.Hash = h
			}
		}
	}

	store := adapter.NewFileStore()
	for _, id := range c.NodeIDs() {
		ns := c.Nodes[id]
		a, aerr := newAdapter(ns, projectRoot)
		if aerr != nil {
			return nil, aerr
		}
		if rerr := store.Register(id, a); rerr != nil {
			return nil, rerr
		}
	}

	transforms, binders, terr := bindTransforms(c, projectRoot)
	if terr != nil {
		return nil, terr
	}

	return &Prepared{
		Context:     c,
		Graph:       g,
		Store:       store,
		Transforms:  transforms,
		Hasher:      hash.NewByteContentHasher(),
		ProjectRoot: projectRoot,
		binders:     binders,
	}, nil
}

// resolve joins a config-relative path against the project root (absolute
// paths pass through unchanged).
func resolve(projectRoot, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(projectRoot, p)
}

// newAdapter constructs the Phase-2 adapter for a node by kind.
func newAdapter(ns config.NodeSpec, projectRoot string) (adapter.Adapter, error) {
	path := resolve(projectRoot, ns.Path)
	switch ns.Kind {
	case graph.KindMarkdown, graph.KindSummary, graph.KindStatus, graph.KindStatusSummary, graph.KindFingerprint:
		// All text/markdown-family kinds (and the fingerprint sidecar, whose
		// content is the hash string) are backed by a plain file adapter.
		return adapter.NewFileAdapter(path, ns.Kind, hash.NewByteContentHasher()), nil
	case graph.KindHTML:
		return adapter.NewHTMLAdapter(path), nil
	case graph.KindPDF:
		return adapter.NewPDFAdapter(path), nil
	case graph.KindDOCX:
		return adapter.NewDOCXAdapter(path), nil
	case graph.KindSQLite:
		// Content = the FULL canonical schema+ROWS dump (canonicalDumpAllTables)
		// so ROW-level changes drive drift detection, not just schema changes.
		// Writes are a no-op: the bound transform (the md-to-sqlite builtin, or
		// an exec md-to-db) mutates the .db file directly and Docs Chain re-reads
		// the dump to confirm. (The schema-only single-query path was inadequate
		// for row-level DB-as-SSoT sync; see internal/adapter/sqlite_table.go.)
		return adapter.NewSQLiteRowDumpAdapter(path), nil
	default:
		return nil, fmt.Errorf("runner: node %q has unsupported kind %q", ns.ID, ns.Kind)
	}
}

// bindTransforms turns each config edge into the graph.Transform the engine
// invokes, keyed by the DERIVED/target node id (graph.Recompute looks up the
// transform by the node being recomputed). The output path passed to the
// builtin producers is the target node's resolved path.
func bindTransforms(c *config.Context, projectRoot string) (map[string]graph.Transform, map[string]nodeBinder, error) {
	out := make(map[string]graph.Transform)
	binders := make(map[string]nodeBinder)
	for _, e := range c.Edges {
		switch e.Type {
		case config.EdgeDeriveFrom:
			ts := c.Transforms[e.Transform]
			target := c.Nodes[e.To]
			tf, err := bindOne(ts, target, e.From, c, projectRoot)
			if err != nil {
				return nil, nil, err
			}
			out[e.To] = tf
			binders[e.To] = nodeBinder{spec: ts, target: target, srcIDs: e.From}
		case config.EdgeSync:
			// Sync edges: Phase 1 marks the regenerated side dirty; the actual
			// regeneration of the non-authoritative side flows through the
			// exec transform named on the edge. We bind the b<-a and a<-b
			// transforms keyed by the side being regenerated so a future
			// orchestrator pass can invoke them. The current orchestrator
			// handles sync detection/conflict; per-side regeneration via these
			// transforms is wired but only exercised once a side is the lone
			// dirty endpoint (the engine folds the regen target into dirty).
			if e.TransformBToA != "" {
				ts := c.Transforms[e.TransformBToA]
				target := c.Nodes[e.A]
				tf, err := bindOne(ts, target, []string{e.B}, c, projectRoot)
				if err != nil {
					return nil, nil, err
				}
				out[e.A] = tf
				binders[e.A] = nodeBinder{spec: ts, target: target, srcIDs: []string{e.B}}
			}
			if e.TransformAToB != "" {
				ts := c.Transforms[e.TransformAToB]
				target := c.Nodes[e.B]
				tf, err := bindOne(ts, target, []string{e.A}, c, projectRoot)
				if err != nil {
					return nil, nil, err
				}
				out[e.B] = tf
				binders[e.B] = nodeBinder{spec: ts, target: target, srcIDs: []string{e.A}}
			}
		}
	}
	return out, binders, nil
}

// bindOne binds a single TransformSpec to a graph.Transform producing the
// target node's content at its live path. srcIDs are the source node ids (for
// multi-input builtins like members-fingerprint).
func bindOne(ts config.TransformSpec, target config.NodeSpec, srcIDs []string, c *config.Context, projectRoot string) (graph.Transform, error) {
	return bindOneAt(ts, resolve(projectRoot, target.Path), target, srcIDs, c, projectRoot)
}

// bindOneAt is bindOne with an explicit output path. Verify uses it to direct
// builtin producers (pandoc/weasyprint write to a real file) into a TEMP path
// so the read-only drift check never mutates the live artefact.
func bindOneAt(ts config.TransformSpec, outPath string, target config.NodeSpec, srcIDs []string, c *config.Context, projectRoot string) (graph.Transform, error) {
	if ts.IsBuiltin() {
		switch ts.Builtin {
		case config.BuiltinPandocHTML:
			return adapter.PandocMarkdownToHTML(outPath), nil
		case config.BuiltinWeasyprintPDF:
			// Pin weasyprint's --base-url to the LIVE target path (not outPath,
			// which Verify rebinds to a temp) so relative-link resolution — and
			// the produced PDF bytes — are independent of the staging directory.
			// This makes a post-sync verify byte-match the committed PDF.
			liveBase := resolve(projectRoot, target.Path)
			return adapter.WeasyprintHTMLToPDFAt(outPath, liveBase), nil
		case config.BuiltinPandocDOCX:
			return adapter.PandocMarkdownToDOCX(outPath), nil
		case config.BuiltinMembersFingerprint:
			// Resolve the member glob from the first source fingerprint node,
			// or from the target node itself when it is the fingerprint.
			fp := target
			return membersFingerprintTransform(fp, projectRoot), nil
		case config.BuiltinMDToSQLite:
			// Parse the markdown source's pipe tables into the target SQLite DB
			// (outPath is the live .db on sync, a temp path on verify).
			return adapter.MarkdownToSQLite(outPath), nil
		case config.BuiltinSQLiteToMD:
			// Render the SOURCE sqlite node's tables back to markdown. The source
			// is never rebound by Verify (only the output target is), so resolve
			// the live source DB path from the edge's source node.
			if len(srcIDs) == 0 {
				return nil, fmt.Errorf("runner: builtin sqlite-to-md requires a source sqlite node (context %q, target %q)", c.Name, target.ID)
			}
			srcDB := resolve(projectRoot, c.Nodes[srcIDs[0]].Path)
			return adapter.SQLiteToMarkdown(srcDB), nil
		case config.BuiltinColorizeHTML:
			// §11.4.23 html→html post-process: background-color tracker-doc
			// Type/Status cells. Deterministic; non-tracker HTML passes through.
			return adapter.ColorizeHTML(), nil
		default:
			return nil, fmt.Errorf("runner: builtin %q is recognized by the schema but not yet wired to a runnable transform (context %q, target %q)", ts.Builtin, c.Name, target.ID)
		}
	}
	// exec transform: shell to the consumer's binary with IO paths + args.
	return execTransform(ts, outPath, srcIDs, c, projectRoot), nil
}

// membersFingerprintTransform returns a transform that enumerates the node's
// member glob (minus excludes), sorts them, and hashes the sorted member list
// (§11.4.86). The fingerprint node's *content* is the hex hash string.
func membersFingerprintTransform(fp config.NodeSpec, projectRoot string) graph.Transform {
	return func(_ map[string][]byte) ([]byte, error) {
		members, err := enumerateMembers(fp, projectRoot)
		if err != nil {
			return nil, err
		}
		return []byte(hash.FingerprintMembers(members) + "\n"), nil
	}
}

// enumerateMembers expands the member glob relative to projectRoot, applies
// the exclude globs, and returns project-relative member paths.
func enumerateMembers(fp config.NodeSpec, projectRoot string) ([]string, error) {
	matches, err := filepath.Glob(resolve(projectRoot, fp.Members))
	if err != nil {
		return nil, fmt.Errorf("runner: fingerprint glob %q: %w", fp.Members, err)
	}
	var excl []string
	for _, ex := range fp.Exclude {
		m, _ := filepath.Glob(resolve(projectRoot, ex))
		excl = append(excl, m...)
	}
	exclSet := make(map[string]bool, len(excl))
	for _, e := range excl {
		exclSet[e] = true
	}
	var members []string
	for _, m := range matches {
		if exclSet[m] {
			continue
		}
		rel, rerr := filepath.Rel(projectRoot, m)
		if rerr != nil {
			rel = m
		}
		members = append(members, rel)
	}
	sort.Strings(members)
	return members, nil
}

// execTransform binds an exec: transform. Contract (CONFIG_SCHEMA §5.2): Docs
// Chain stages each input to a temp file and passes the temp input path(s)
// then the staged OUTPUT temp path, then the config args. The command writes
// its result to the output temp path; we read it back as the node's content.
// A non-zero exit is a transform failure (orchestrator rolls back, exit 3).
func execTransform(ts config.TransformSpec, outPath string, srcIDs []string, c *config.Context, projectRoot string) graph.Transform {
	return func(ins map[string][]byte) ([]byte, error) {
		dir := filepath.Dir(outPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		// Stage inputs in deterministic source-id order.
		ids := make([]string, 0, len(ins))
		for id := range ins {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var argv []string
		var cleanups []func()
		defer func() {
			for _, cl := range cleanups {
				cl()
			}
		}()
		for _, id := range ids {
			tmp, err := os.CreateTemp(dir, ".docs_chain_exec_in_*")
			if err != nil {
				return nil, err
			}
			name := tmp.Name()
			cleanups = append(cleanups, func() { os.Remove(name) })
			if _, err := tmp.Write(ins[id]); err != nil {
				tmp.Close()
				return nil, err
			}
			if err := tmp.Close(); err != nil {
				return nil, err
			}
			argv = append(argv, name)
		}
		outTmp, err := os.CreateTemp(dir, ".docs_chain_exec_out_*")
		if err != nil {
			return nil, err
		}
		outName := outTmp.Name()
		outTmp.Close()
		cleanups = append(cleanups, func() { os.Remove(outName) })
		argv = append(argv, outName)
		argv = append(argv, ts.Args...)

		bin := ts.Exec
		if !filepath.IsAbs(bin) && strings.ContainsRune(bin, os.PathSeparator) {
			bin = resolve(projectRoot, bin)
		}
		cmd := exec.Command(bin, argv...) //nolint:gosec // bin + args come from the project's own trusted config, never untrusted input
		cmd.Dir = projectRoot
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("runner: exec transform %q failed: %w; stderr: %s", ts.Name, err, stderr.String())
		}
		b, rerr := os.ReadFile(outName)
		if rerr != nil {
			return nil, fmt.Errorf("runner: exec transform %q produced no readable output: %w", ts.Name, rerr)
		}
		return b, nil
	}
}

// RunSync executes one propagation pass for a prepared context via the
// orchestrator and folds the resulting hashes into st (caller persists st).
func (p *Prepared) RunSync(st *state.State) (*orchestrator.Result, error) {
	res, err := orchestrator.Run(p.Graph, p.Store, p.Hasher, p.Transforms)
	if err != nil {
		return nil, err
	}
	// On a committed / in-sync run, fold the new baseline into state.
	if res.Status == orchestrator.StatusCommitted || res.Status == orchestrator.StatusInSync {
		if res.Recompute != nil {
			st.SetHashes(p.Context.Name, res.Recompute.NewHashes)
		}
	}
	return res, nil
}

// VerifyResult reports the read-only drift check for one context.
type VerifyResult struct {
	Context string
	// Stale lists derived node ids whose recomputed content differs from
	// what is on disk (sorted). Empty => in sync.
	Stale []string
	// ToolAbsent is true if a derived transform could not run because its
	// tool is missing (honest SKIP, not a stale claim).
	ToolAbsent bool
	ToolReason string
}

// Verify recomputes derived nodes WITHOUT mutating anything and reports which
// are stale relative to their on-disk content. It compares the freshly
// produced output bytes (via each bound transform) against the current
// on-disk bytes, using the same per-node hasher. A both-dirty sync pair or a
// cycle surfaces as an error (the CLI maps those to their exit codes).
func (p *Prepared) Verify() (*VerifyResult, error) {
	vr := &VerifyResult{Context: p.Context.Name}

	// Map derived node -> sources (derive-from only; verify is sink-side).
	sources := make(map[string][]string)
	for _, e := range p.Graph.Edges() {
		if e.Type == graph.EdgeDeriveFrom {
			sources[e.To] = append(sources[e.To], e.From)
		}
	}

	order, err := p.Graph.TopoOrder()
	if err != nil {
		return nil, err
	}
	// Verify must NOT mutate live artefacts. Builtin producers
	// (pandoc/weasyprint) write to a real file, so we rebind each derived
	// transform to a per-run TEMP output directory. exec transforms already
	// stage their own output temp, so rebinding is a no-op for them.
	tmpOut, err := os.MkdirTemp("", "docs_chain_verify_*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpOut)

	for _, id := range order {
		srcs, isDerived := sources[id]
		if !isDerived {
			continue
		}
		bnd, ok := p.binders[id]
		if !ok {
			return nil, fmt.Errorf("runner: verify: derived node %q has no bound transform", id)
		}
		// Use the SAME basename as the live target inside a per-node temp
		// subdir, so any builtin that derives output metadata from the output
		// filename (e.g. pandoc's pinned <title>) produces byte-identical
		// output to a real sync — otherwise verify would falsely flag drift.
		// The weasyprint producer additionally pins --base-url to the LIVE
		// target path (see bindOneAt) so its relative-link resolution — and
		// thus the PDF bytes — are INDEPENDENT of this temp staging directory.
		nodeDir := filepath.Join(tmpOut, id)
		if mkErr := os.MkdirAll(nodeDir, 0o755); mkErr != nil {
			return nil, mkErr
		}
		tmpPath := filepath.Join(nodeDir, filepath.Base(bnd.target.Path))
		tf, berr := bindOneAt(bnd.spec, tmpPath, bnd.target, bnd.srcIDs, p.Context, p.ProjectRoot)
		if berr != nil {
			return nil, berr
		}
		ins := make(map[string][]byte, len(srcs))
		for _, s := range srcs {
			cur, gerr := p.Store.Get(s)
			if gerr != nil {
				return nil, gerr
			}
			ins[s] = cur
		}
		produced, terr := tf(ins)
		if terr != nil {
			if adapter.IsToolAbsent(terr) {
				vr.ToolAbsent = true
				vr.ToolReason = terr.Error()
				return vr, nil
			}
			return nil, terr
		}
		onDisk, gerr := p.Store.Get(id)
		if gerr != nil {
			return nil, gerr
		}
		// BUG FIX (binary-hash verify defect): hash with the node's KIND-SPECIFIC
		// hasher (binary kinds → raw bytes, text kinds → text normalization),
		// the SAME hasher the sync-record path (graph.Recompute via the store's
		// PerNodeHasher) uses for this node. Previously this used the single
		// text-normalizing p.Hasher for every kind, which mangled binary docx/pdf
		// bytes and could disagree with the record path.
		nh, herr := p.Store.Hasher(id)
		if herr != nil {
			return nil, herr
		}
		if nh.Hash(produced) != nh.Hash(onDisk) {
			vr.Stale = append(vr.Stale, id)
		}
	}
	sort.Strings(vr.Stale)
	return vr, nil
}
