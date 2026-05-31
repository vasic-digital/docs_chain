package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"digital.vasic.docs_chain/internal/hash"
)

// normHash computes the engine's content hash for a string, so a test can
// pre-seed state.json with a baseline that matches a file's current content.
func normHash(s string) string {
	return hash.NewByteContentHasher().Hash([]byte(s))
}

// buildCLI compiles the docs_chain binary once into the test's temp dir and
// returns its path. This is an anti-bluff requirement: the CLI claims are
// proven by running the REAL built binary, not by calling internal funcs.
func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docs_chain")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build docs_chain: %v\n%s", err, out)
	}
	return bin
}

// runCLI runs the binary with args in workdir and returns stdout+stderr and
// the exit code.
func runCLI(t *testing.T, bin, workdir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return string(out), code
}

func writeFixture(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "docs", "G.md"), "# Guide\n\nReal **content** here.\n")
	ctxDir := filepath.Join(root, ".docs_chain", "contexts")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ctxDir, "guide.yaml"), `
context: guide
description: One markdown doc -> html export
nodes:
  guide_md:   { kind: markdown, path: docs/G.md }
  guide_html: { kind: html,     path: docs/G.html }
edges:
  - { type: derive-from, from: guide_md, to: guide_html, transform: m2h }
transforms:
  m2h: { builtin: pandoc-html }
`)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func havePandoc() bool {
	_, err := exec.LookPath("pandoc")
	return err == nil
}

// TestCLI_Doctor proves `doctor` validates a real fixture and exits 0.
func TestCLI_Doctor(t *testing.T) {
	bin := buildCLI(t)
	root := t.TempDir()
	writeFixture(t, root)
	out, code := runCLI(t, bin, root, "doctor", "guide")
	if code != 0 {
		t.Fatalf("doctor exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "parse + graph: OK") {
		t.Fatalf("doctor output missing OK line:\n%s", out)
	}
	t.Logf("EVIDENCE doctor:\n%s", out)
}

// TestCLI_DoctorConfigError proves a malformed context exits 4.
func TestCLI_DoctorConfigError(t *testing.T) {
	bin := buildCLI(t)
	root := t.TempDir()
	ctxDir := filepath.Join(root, ".docs_chain", "contexts")
	mustWrite(t, filepath.Join(ctxDir, "bad.yaml"), `
context: bad
nodes:
  a: { kind: markdown, path: a.md }
edges:
  - { type: derive-from, from: a, to: nowhere, transform: t }
transforms:
  t: { builtin: pandoc-html }
`)
	out, code := runCLI(t, bin, root, "doctor", "bad")
	if code != 4 {
		t.Fatalf("doctor(bad) exit = %d, want 4 (config error)\n%s", code, out)
	}
	t.Logf("EVIDENCE config-error exit 4:\n%s", out)
}

// TestCLI_SyncVerifyCycle is the headline e2e: sync produces a real html,
// verify reports in-sync (exit 0), a hand-edit of the source makes verify
// report stale (exit 1), and a re-sync restores in-sync.
func TestCLI_SyncVerifyCycle(t *testing.T) {
	if !havePandoc() {
		t.Skip("pandoc absent — honest SKIP; the html builtin needs pandoc")
	}
	bin := buildCLI(t)
	root := t.TempDir()
	writeFixture(t, root)

	// sync -> applied, exit 0, real html on disk.
	out, code := runCLI(t, bin, root, "sync", "guide")
	if code != 0 {
		t.Fatalf("sync exit = %d, want 0\n%s", code, out)
	}
	htmlPath := filepath.Join(root, "docs", "G.html")
	html, err := os.ReadFile(htmlPath)
	if err != nil || len(html) == 0 {
		t.Fatalf("sync did not produce html: %v", err)
	}
	if !strings.Contains(string(html), "content") {
		t.Fatalf("produced html missing body content")
	}
	t.Logf("EVIDENCE sync (exit %d):\n%s", code, out)

	// verify -> in-sync, exit 0.
	out, code = runCLI(t, bin, root, "verify", "guide")
	if code != 0 {
		t.Fatalf("verify(in-sync) exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "in-sync") {
		t.Fatalf("verify output not in-sync:\n%s", out)
	}
	t.Logf("EVIDENCE verify in-sync (exit %d):\n%s", code, out)

	// Hand-edit source -> verify must report STALE, exit 1.
	mustWrite(t, filepath.Join(root, "docs", "G.md"), "# Guide\n\nEDITED body.\n")
	out, code = runCLI(t, bin, root, "verify", "guide")
	if code != 1 {
		t.Fatalf("verify(stale) exit = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "STALE") {
		t.Fatalf("verify output not STALE:\n%s", out)
	}
	t.Logf("EVIDENCE verify STALE after edit (exit %d):\n%s", code, out)

	// Re-sync -> applied; verify -> in-sync again.
	if _, code = runCLI(t, bin, root, "sync", "guide"); code != 0 {
		t.Fatalf("re-sync exit = %d, want 0", code)
	}
	if _, code = runCLI(t, bin, root, "verify", "guide"); code != 0 {
		t.Fatalf("verify after re-sync exit = %d, want 0", code)
	}

	// state.json must exist now.
	if _, err := os.Stat(filepath.Join(root, ".docs_chain", "state.json")); err != nil {
		t.Fatalf("state.json not written: %v", err)
	}
	// evidence dir must exist.
	evRoot := filepath.Join(root, "qa-results", "docs_chain")
	if entries, _ := os.ReadDir(evRoot); len(entries) == 0 {
		t.Fatalf("no run-evidence written under %s", evRoot)
	}
	t.Logf("EVIDENCE: state.json + qa-results/docs_chain/<run-id>/ written")
}

// TestCLI_EarlyCutoff proves the second sync of an unchanged chain is in-sync
// (no rewrite) — the content-hash early-cutoff working through the CLI.
func TestCLI_EarlyCutoff(t *testing.T) {
	if !havePandoc() {
		t.Skip("pandoc absent — honest SKIP")
	}
	bin := buildCLI(t)
	root := t.TempDir()
	writeFixture(t, root)

	if _, code := runCLI(t, bin, root, "sync", "guide"); code != 0 {
		t.Fatal("first sync failed")
	}
	htmlPath := filepath.Join(root, "docs", "G.html")
	info1, _ := os.Stat(htmlPath)

	out, code := runCLI(t, bin, root, "sync", "guide")
	if code != 0 {
		t.Fatalf("second sync exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "in-sync") {
		t.Fatalf("second sync not in-sync (early cutoff failed):\n%s", out)
	}
	info2, _ := os.Stat(htmlPath)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("early cutoff failed: html rewritten on no-op sync")
	}
	t.Logf("EVIDENCE early-cutoff via CLI: second sync in-sync, html untouched\n%s", out)
}

// TestCLI_ConflictExit2 proves a both-dirty sync edge exits 2 through the CLI.
// We pre-seed state.json with a baseline that matches the CURRENT file
// contents, then edit BOTH sides so the next sync sees both as dirty -> the
// engine refuses to merge and the CLI exits 2.
func TestCLI_ConflictExit2(t *testing.T) {
	bin := buildCLI(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "x.md"), "A v1\n")
	mustWrite(t, filepath.Join(root, "y.md"), "B v1\n")
	mustWrite(t, filepath.Join(root, "cp.sh"), "#!/bin/sh\ncat \"$1\" > \"$2\"\n")
	if err := os.Chmod(filepath.Join(root, "cp.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctxDir := filepath.Join(root, ".docs_chain", "contexts")
	mustWrite(t, filepath.Join(ctxDir, "s.yaml"), `
context: s
nodes:
  a: { kind: markdown, path: x.md }
  b: { kind: markdown, path: y.md }
edges:
  - { type: sync, a: a, b: b, authority: a,
      transform_a_to_b: cp, transform_b_to_a: cp }
transforms:
  cp: { exec: "./cp.sh" }
`)
	// Seed state.json so the baseline equals the current file contents (a
	// "clean" prior sync). The ByteContentHasher normalizes, so we compute the
	// hashes the same way the engine does: sha256 over normalized content.
	mustWrite(t, filepath.Join(root, ".docs_chain", "state.json"),
		`{"version":1,"contexts":{"s":{"a":"`+normHash("A v1\n")+`","b":"`+normHash("B v1\n")+`"}}}`+"\n")

	// Edit BOTH sides since that baseline -> both dirty -> conflict.
	mustWrite(t, filepath.Join(root, "x.md"), "A EDITED\n")
	mustWrite(t, filepath.Join(root, "y.md"), "B EDITED\n")

	out, code := runCLI(t, bin, root, "sync", "s")
	if code != 2 {
		t.Fatalf("both-dirty sync exit = %d, want 2 (conflict)\n%s", code, out)
	}
	if !strings.Contains(out, "CONFLICT") {
		t.Fatalf("output missing CONFLICT:\n%s", out)
	}
	// Zero writes: both files unchanged.
	a, _ := os.ReadFile(filepath.Join(root, "x.md"))
	b, _ := os.ReadFile(filepath.Join(root, "y.md"))
	if string(a) != "A EDITED\n" || string(b) != "B EDITED\n" {
		t.Fatalf("conflict run mutated files (a=%q b=%q) — must be zero-write", a, b)
	}
	t.Logf("EVIDENCE conflict exit 2 (zero writes):\n%s", out)
}

// TestCLI_Graph proves the graph subcommand prints topo order.
func TestCLI_Graph(t *testing.T) {
	bin := buildCLI(t)
	root := t.TempDir()
	writeFixture(t, root)
	out, code := runCLI(t, bin, root, "graph", "guide")
	if code != 0 {
		t.Fatalf("graph exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "topo order") || !strings.Contains(out, "guide_md") {
		t.Fatalf("graph output unexpected:\n%s", out)
	}
	t.Logf("EVIDENCE graph:\n%s", out)
}
