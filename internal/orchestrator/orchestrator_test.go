package orchestrator

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"digital.vasic.docs_chain/internal/adapter"
	"digital.vasic.docs_chain/internal/graph"
	"digital.vasic.docs_chain/internal/hash"
)

// upper is a deterministic in-process transform: summary = uppercased source.
// Used so the integration test does not depend on any external tool, while
// still exercising the real FileStore (read/write actual temp files).
func upper(ins map[string][]byte) ([]byte, error) {
	for _, v := range ins {
		out := make([]byte, len(v))
		for i, b := range v {
			if b >= 'a' && b <= 'z' {
				out[i] = b - 32
			} else {
				out[i] = b
			}
		}
		return out, nil
	}
	return nil, errors.New("no input")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// TestRun_RealChain_CommitAndNoOp builds a real temp-dir chain
// Issues.md -> Summary.md (derive via upper) and asserts: the run commits,
// the summary file is written, hashes are stable, and a second run with no
// edits is a no-op (early-cutoff — nothing recomputed).
func TestRun_RealChain_CommitAndNoOp(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "Issues.md")
	sumPath := filepath.Join(dir, "Summary.md")
	if err := os.WriteFile(mdPath, []byte("# issues\nhello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := graph.New()
	must(t, g.AddNode(&graph.Node{ID: "md", Kind: graph.KindMarkdown, Path: mdPath}))
	must(t, g.AddNode(&graph.Node{ID: "sum", Kind: graph.KindSummary, Path: sumPath}))
	must(t, g.AddEdge(graph.Edge{From: "md", To: "sum", Type: graph.EdgeDeriveFrom}))

	store := adapter.NewFileStore()
	must(t, store.Register("md", adapter.NewMarkdownAdapter(mdPath)))
	must(t, store.Register("sum", adapter.NewMarkdownAdapter(sumPath)))

	h := hash.NewByteContentHasher()
	tfs := map[string]graph.Transform{"sum": upper}

	// First run: commits, writes summary.
	r1, err := Run(g, store, h, tfs)
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if r1.Status != StatusCommitted {
		t.Fatalf("run1 status = %s want committed (err=%v)", r1.Status, r1.Err)
	}
	got := readFile(t, sumPath)
	if got != "# ISSUES\nHELLO\n" {
		t.Fatalf("summary content = %q", got)
	}
	if len(r1.Committed) != 1 || r1.Committed[0] != "sum" {
		t.Fatalf("committed = %v want [sum]", r1.Committed)
	}

	// Second run, no edits: early-cutoff -> in-sync, nothing recomputed.
	r2, err := Run(g, store, h, tfs)
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if r2.Status != StatusInSync {
		t.Fatalf("run2 status = %s want in-sync; recomputed=%v", r2.Status, r2.Recompute.Recomputed)
	}
	if len(r2.Recompute.Recomputed) != 0 {
		t.Fatalf("run2 recomputed = %v want none (early-cutoff)", r2.Recompute.Recomputed)
	}
}

// TestRun_MultiLevelChain proves the staging store feeds a freshly-staged
// intermediate to the next transform: md -> a (upper) -> b (upper-again,
// no-op since already upper). Both 'a' and 'b' commit on first run.
func TestRun_MultiLevelChain(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "src.md")
	aP := filepath.Join(dir, "a.md")
	bP := filepath.Join(dir, "b.md")
	if err := os.WriteFile(md, []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	must(t, g.AddNode(&graph.Node{ID: "md", Path: md}))
	must(t, g.AddNode(&graph.Node{ID: "a", Path: aP}))
	must(t, g.AddNode(&graph.Node{ID: "b", Path: bP}))
	must(t, g.AddEdge(graph.Edge{From: "md", To: "a", Type: graph.EdgeDeriveFrom}))
	must(t, g.AddEdge(graph.Edge{From: "a", To: "b", Type: graph.EdgeDeriveFrom}))

	store := adapter.NewFileStore()
	must(t, store.Register("md", adapter.NewMarkdownAdapter(md)))
	must(t, store.Register("a", adapter.NewMarkdownAdapter(aP)))
	must(t, store.Register("b", adapter.NewMarkdownAdapter(bP)))

	r, err := Run(g, store, hash.NewByteContentHasher(), map[string]graph.Transform{"a": upper, "b": upper})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r.Status != StatusCommitted {
		t.Fatalf("status = %s", r.Status)
	}
	if readFile(t, aP) != "ABC\n" || readFile(t, bP) != "ABC\n" {
		t.Fatalf("a=%q b=%q", readFile(t, aP), readFile(t, bP))
	}
}

// TestRun_TransformError_RollsBack: a transform that errors must leave NO
// partial files — the derived artefact is never created, live state is
// byte-identical to pre-run.
func TestRun_TransformError_RollsBack(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "Issues.md")
	sum := filepath.Join(dir, "Summary.md")
	if err := os.WriteFile(md, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	must(t, g.AddNode(&graph.Node{ID: "md", Path: md}))
	must(t, g.AddNode(&graph.Node{ID: "sum", Path: sum}))
	must(t, g.AddEdge(graph.Edge{From: "md", To: "sum", Type: graph.EdgeDeriveFrom}))

	store := adapter.NewFileStore()
	must(t, store.Register("md", adapter.NewMarkdownAdapter(md)))
	must(t, store.Register("sum", adapter.NewMarkdownAdapter(sum)))

	boom := map[string]graph.Transform{
		"sum": func(map[string][]byte) ([]byte, error) { return nil, errors.New("boom") },
	}
	r, err := Run(g, store, hash.NewByteContentHasher(), boom)
	if err != nil {
		t.Fatalf("run returned hard error: %v", err)
	}
	if r.Status != StatusRolledBack {
		t.Fatalf("status = %s want rolled-back", r.Status)
	}
	if _, statErr := os.Stat(sum); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ROLLBACK violated: summary file exists after transform error (stat err=%v)", statErr)
	}
	// Source untouched.
	if readFile(t, md) != "hello\n" {
		t.Fatal("source mutated during rolled-back run")
	}
}

// TestRun_Cycle refuses to run a cyclic derive-from graph and writes nothing.
func TestRun_Cycle(t *testing.T) {
	dir := t.TempDir()
	pA := filepath.Join(dir, "a.md")
	pB := filepath.Join(dir, "b.md")
	if err := os.WriteFile(pA, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pB, []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	must(t, g.AddNode(&graph.Node{ID: "a", Path: pA}))
	must(t, g.AddNode(&graph.Node{ID: "b", Path: pB}))
	must(t, g.AddEdge(graph.Edge{From: "a", To: "b", Type: graph.EdgeDeriveFrom}))
	must(t, g.AddEdge(graph.Edge{From: "b", To: "a", Type: graph.EdgeDeriveFrom}))

	store := adapter.NewFileStore()
	must(t, store.Register("a", adapter.NewMarkdownAdapter(pA)))
	must(t, store.Register("b", adapter.NewMarkdownAdapter(pB)))

	r, err := Run(g, store, hash.NewByteContentHasher(), map[string]graph.Transform{"a": upper, "b": upper})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r.Status != StatusCycle {
		t.Fatalf("status = %s want cycle", r.Status)
	}
	var ce *graph.CycleError
	if !errors.As(r.Err, &ce) {
		t.Fatalf("err = %v want *graph.CycleError", r.Err)
	}
}

// TestRun_SyncConflict: both sides of a sync pair dirty -> ConflictError,
// no writes. We model md<->db as two markdown files for a tool-free test;
// both differ from their stored hash (empty), so both are dirty.
func TestRun_SyncConflict(t *testing.T) {
	dir := t.TempDir()
	pA := filepath.Join(dir, "view_a.md")
	pB := filepath.Join(dir, "view_b.md")
	if err := os.WriteFile(pA, []byte("edited A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pB, []byte("edited B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	must(t, g.AddNode(&graph.Node{ID: "a", Path: pA}))
	must(t, g.AddNode(&graph.Node{ID: "b", Path: pB}))
	// Both stored hashes empty -> both dirty on first run.
	must(t, g.AddEdge(graph.Edge{From: "a", To: "b", Type: graph.EdgeSync, Authority: "a"}))

	store := adapter.NewFileStore()
	must(t, store.Register("a", adapter.NewMarkdownAdapter(pA)))
	must(t, store.Register("b", adapter.NewMarkdownAdapter(pB)))

	r, err := Run(g, store, hash.NewByteContentHasher(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r.Status != StatusConflict {
		t.Fatalf("status = %s want conflict", r.Status)
	}
	var ce *graph.ConflictError
	if !errors.As(r.Err, &ce) {
		t.Fatalf("err = %v want *graph.ConflictError", r.Err)
	}
	// No writes: both files byte-identical to what we wrote.
	if readFile(t, pA) != "edited A\n" || readFile(t, pB) != "edited B\n" {
		t.Fatal("conflict run must not write")
	}
}

// TestRun_ToolAbsentRollback (optional path): if pandoc is absent, a real
// md->html transform fails with ToolAbsentError; the run rolls back (no
// .html written) and the caller can detect tool-absence to SKIP. If pandoc
// IS present, the run commits a real HTML file.
func TestRun_PandocHTMLChain(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "Doc.md")
	htmlP := filepath.Join(dir, "Doc.html")
	if err := os.WriteFile(md, []byte("# Heading\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	must(t, g.AddNode(&graph.Node{ID: "md", Kind: graph.KindMarkdown, Path: md}))
	must(t, g.AddNode(&graph.Node{ID: "html", Kind: graph.KindHTML, Path: htmlP}))
	must(t, g.AddEdge(graph.Edge{From: "md", To: "html", Type: graph.EdgeDeriveFrom}))

	store := adapter.NewFileStore()
	must(t, store.Register("md", adapter.NewMarkdownAdapter(md)))
	must(t, store.Register("html", adapter.NewHTMLAdapter(htmlP)))

	tfs := map[string]graph.Transform{"html": adapter.PandocMarkdownToHTML(htmlP)}
	r, err := Run(g, store, hash.NewByteContentHasher(), tfs)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, lpErr := exec.LookPath("pandoc"); lpErr != nil {
		// pandoc absent: must roll back, tool-absence detectable, no html.
		if r.Status != StatusRolledBack {
			t.Fatalf("pandoc absent: status = %s want rolled-back", r.Status)
		}
		if !IsToolAbsent(r.Err) {
			t.Fatalf("pandoc absent: err = %v want ToolAbsent", r.Err)
		}
		if _, st := os.Stat(htmlP); !errors.Is(st, os.ErrNotExist) {
			t.Fatal("pandoc absent: html must not exist")
		}
		t.Skip("SKIP-with-reason: pandoc not installed — committed-path unverifiable (never faked)")
	}

	// pandoc present: committed, real HTML on disk with rendered heading.
	if r.Status != StatusCommitted {
		t.Fatalf("status = %s want committed (err=%v)", r.Status, r.Err)
	}
	html := readFile(t, htmlP)
	if !contains(html, "Heading") {
		t.Fatalf("html missing heading: %s", html)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return len(sub) == 0
}
