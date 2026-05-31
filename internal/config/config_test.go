package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"digital.vasic.docs_chain/internal/graph"
)

const guideYAML = `
context: guide
description: One markdown doc with html + pdf exports
nodes:
  guide_md:   { kind: markdown, path: docs/MY_GUIDE.md }
  guide_html: { kind: html,     path: docs/MY_GUIDE.html }
  guide_pdf:  { kind: pdf,      path: docs/MY_GUIDE.pdf }
edges:
  - { type: derive-from, from: guide_md,   to: guide_html, transform: md-to-html }
  - { type: derive-from, from: guide_html, to: guide_pdf,  transform: html-to-pdf }
transforms:
  md-to-html:  { builtin: pandoc-html }
  html-to-pdf: { builtin: weasyprint-pdf }
`

func TestLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "guide.yaml")
	if err := os.WriteFile(p, []byte(guideYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Name != "guide" {
		t.Errorf("name = %q, want guide", c.Name)
	}
	if len(c.Nodes) != 3 {
		t.Errorf("nodes = %d, want 3", len(c.Nodes))
	}
	if c.Nodes["guide_md"].Kind != graph.KindMarkdown {
		t.Errorf("guide_md kind = %q", c.Nodes["guide_md"].Kind)
	}
	if len(c.Edges) != 2 {
		t.Errorf("edges = %d, want 2", len(c.Edges))
	}
	if got := c.Edges[0].From; len(got) != 1 || got[0] != "guide_md" {
		t.Errorf("edge[0].From = %v", got)
	}

	// Build the graph and assert deterministic topo order.
	g, err := c.BuildGraph()
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	order, err := g.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	// md must come before html must come before pdf.
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if !(pos["guide_md"] < pos["guide_html"] && pos["guide_html"] < pos["guide_pdf"]) {
		t.Errorf("topo order violates derive chain: %v", order)
	}
}

func TestParse_MultiInputFrom(t *testing.T) {
	y := `
context: multi
nodes:
  a: { kind: markdown, path: a.md }
  b: { kind: markdown, path: b.md }
  out: { kind: summary, path: out.md }
edges:
  - { type: derive-from, from: [a, b], to: out, transform: combine }
transforms:
  combine: { exec: "./combine.sh" }
`
	c, err := Parse([]byte(y), "multi.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	from := c.Edges[0].From
	if len(from) != 2 || from[0] != "a" || from[1] != "b" {
		t.Errorf("multi-input From = %v, want [a b]", from)
	}
}

func TestValidate_DanglingNodeRef(t *testing.T) {
	y := `
context: bad
nodes:
  a: { kind: markdown, path: a.md }
edges:
  - { type: derive-from, from: a, to: missing, transform: t }
transforms:
  t: { builtin: pandoc-html }
`
	_, err := Load2(t, y)
	assertConfigErr(t, err, "unknown node \"missing\"")
}

func TestValidate_UnknownTransform(t *testing.T) {
	y := `
context: bad
nodes:
  a: { kind: markdown, path: a.md }
  b: { kind: html, path: b.html }
edges:
  - { type: derive-from, from: a, to: b, transform: nope }
transforms:
  t: { builtin: pandoc-html }
`
	_, err := Load2(t, y)
	assertConfigErr(t, err, "unknown transform \"nope\"")
}

func TestValidate_Cycle(t *testing.T) {
	y := `
context: cyc
nodes:
  a: { kind: markdown, path: a.md }
  b: { kind: html, path: b.html }
edges:
  - { type: derive-from, from: a, to: b, transform: t }
  - { type: derive-from, from: b, to: a, transform: t }
transforms:
  t: { builtin: pandoc-html }
`
	_, err := Load2(t, y)
	assertConfigErr(t, err, "cycle")
}

func TestValidate_BuiltinAndExecBothSet(t *testing.T) {
	y := `
context: bad
nodes:
  a: { kind: markdown, path: a.md }
  b: { kind: html, path: b.html }
edges:
  - { type: derive-from, from: a, to: b, transform: t }
transforms:
  t: { builtin: pandoc-html, exec: "./x.sh" }
`
	_, err := Load2(t, y)
	assertConfigErr(t, err, "mutually exclusive")
}

func TestValidate_SyncAuthorityNotEndpoint(t *testing.T) {
	y := `
context: bad
nodes:
  md: { kind: markdown, path: a.md }
  db: { kind: sqlite, path: a.db }
  other: { kind: markdown, path: o.md }
edges:
  - { type: sync, a: md, b: db, authority: other,
      transform_a_to_b: t, transform_b_to_a: t }
transforms:
  t: { exec: "./x.sh" }
`
	_, err := Load2(t, y)
	assertConfigErr(t, err, "authority")
}

func TestValidate_FingerprintNeedsMembers(t *testing.T) {
	y := `
context: bad
nodes:
  fp: { kind: fingerprint, path: roster.sha256 }
edges: []
`
	_, err := Load2(t, y)
	assertConfigErr(t, err, "lacks `members`")
}

func TestValidate_UnknownKind(t *testing.T) {
	y := `
context: bad
nodes:
  a: { kind: wat, path: a.x }
edges: []
`
	_, err := Load2(t, y)
	assertConfigErr(t, err, "unknown kind")
}

func TestParse_UnknownFieldRejected(t *testing.T) {
	y := `
context: bad
bogus_top_field: 1
nodes:
  a: { kind: markdown, path: a.md }
edges: []
`
	_, err := Parse([]byte(y), "bad.yaml")
	if err == nil {
		t.Fatal("expected unknown-field parse error")
	}
}

// Load2 writes y to a temp file and Loads it (exercising the real file path).
func Load2(t *testing.T, y string) (*Context, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "ctx.yaml")
	if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func assertConfigErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	var ce *ConfigError
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q does not contain %q", err.Error(), substr)
	}
	_ = ce
}
