package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"digital.vasic.docs_chain/internal/config"
	"digital.vasic.docs_chain/internal/orchestrator"
	"digital.vasic.docs_chain/internal/state"
)

// writeContext writes a context yaml + returns the loaded+validated Context.
func writeContext(t *testing.T, root, name, yaml string) *config.Context {
	t.Helper()
	dir := filepath.Join(root, ".docs_chain", "contexts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return c
}

func haveTool(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// TestSync_MarkdownToHTML_RealPandoc proves the md->html derive-from chain
// actually produces a real .html file via pandoc, and that a SECOND run with
// no input change is early-cut-off (in-sync, no rewrite).
func TestSync_MarkdownToHTML_RealPandoc(t *testing.T) {
	if !haveTool("pandoc") {
		t.Skip("pandoc absent — honest SKIP (the md->html builtin needs pandoc); not a fake pass")
	}
	root := t.TempDir()
	// Real markdown source on disk.
	mdPath := filepath.Join(root, "docs", "G.md")
	if err := os.MkdirAll(filepath.Dir(mdPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte("# Title\n\nHello **world**.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	yaml := `
context: g
nodes:
  md:   { kind: markdown, path: docs/G.md }
  html: { kind: html,     path: docs/G.html }
edges:
  - { type: derive-from, from: md, to: html, transform: m2h }
transforms:
  m2h: { builtin: pandoc-html }
`
	c := writeContext(t, root, "g", yaml)
	st := state.New()

	// --- Run 1: html does not exist -> committed.
	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	res, err := prep.RunSync(st)
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if res.Status != orchestrator.StatusCommitted {
		t.Fatalf("run1 status = %s, want committed (err=%v)", res.Status, res.Err)
	}
	htmlPath := filepath.Join(root, "docs", "G.html")
	produced, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("html not produced: %v", err)
	}
	if len(produced) == 0 {
		t.Fatal("produced html is empty")
	}
	// Real evidence: pandoc emits a standalone document mentioning the body.
	if !containsAll(string(produced), "<html", "Hello", "world") {
		t.Fatalf("produced html missing expected content; got first 200 bytes: %.200s", produced)
	}
	t.Logf("EVIDENCE run1: produced %s (%d bytes)", htmlPath, len(produced))

	// --- Run 2: nothing changed -> early-cutoff -> in-sync, NO rewrite.
	infoBefore, _ := os.Stat(htmlPath)
	prep2, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare2: %v", err)
	}
	res2, err := prep2.RunSync(st)
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if res2.Status != orchestrator.StatusInSync {
		t.Fatalf("run2 status = %s, want in-sync (early cutoff)", res2.Status)
	}
	if res2.Recompute != nil && len(res2.Recompute.Recomputed) != 0 {
		t.Fatalf("run2 recomputed %v, want none (early cutoff skip)", res2.Recompute.Recomputed)
	}
	infoAfter, _ := os.Stat(htmlPath)
	if !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Errorf("run2 rewrote html (mtime changed) — early cutoff failed to skip the unchanged input")
	}
	t.Logf("EVIDENCE run2: early-cutoff skip — html untouched, status=%s", res2.Status)
}

// TestVerify_StaleThenInSync proves verify FAILS (stale) when the derived
// file is out of date and PASSES (in-sync) once it is regenerated.
func TestVerify_StaleThenInSync(t *testing.T) {
	if !haveTool("pandoc") {
		t.Skip("pandoc absent — honest SKIP; verify of an html derive needs pandoc")
	}
	root := t.TempDir()
	mdPath := filepath.Join(root, "a.md")
	if err := os.WriteFile(mdPath, []byte("# A\n\nfirst.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write a stale html that does NOT match what pandoc would produce.
	if err := os.WriteFile(filepath.Join(root, "a.html"), []byte("<html>STALE</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := `
context: a
nodes:
  md:   { kind: markdown, path: a.md }
  html: { kind: html,     path: a.html }
edges:
  - { type: derive-from, from: md, to: html, transform: m2h }
transforms:
  m2h: { builtin: pandoc-html }
`
	c := writeContext(t, root, "a", yaml)
	st := state.New()

	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	vr, err := prep.Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(vr.Stale) != 1 || vr.Stale[0] != "html" {
		t.Fatalf("verify stale = %v, want [html]", vr.Stale)
	}
	t.Logf("EVIDENCE: verify correctly reports STALE before sync: %v", vr.Stale)

	// Now sync to regenerate, then verify must be in-sync.
	if _, err := prep.RunSync(st); err != nil {
		t.Fatal(err)
	}
	prep2, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	vr2, err := prep2.Verify()
	if err != nil {
		t.Fatalf("verify2: %v", err)
	}
	if len(vr2.Stale) != 0 {
		t.Fatalf("verify2 stale = %v, want none after sync", vr2.Stale)
	}
	t.Logf("EVIDENCE: verify is in-sync after sync (stale=%v)", vr2.Stale)
}

// TestSync_Conflict_Exit2 proves a both-dirty sync pair surfaces a conflict
// (orchestrator StatusConflict) with zero writes. We use two markdown nodes
// joined by a sync edge whose regen transforms are no-op exec scripts; the
// conflict is detected BEFORE any transform runs.
func TestSync_Conflict_Exit2(t *testing.T) {
	root := t.TempDir()
	aPath := filepath.Join(root, "x.md")
	bPath := filepath.Join(root, "y.md")
	if err := os.WriteFile(aPath, []byte("A original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("B original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A trivial exec transform that copies input->output (never reached in the
	// conflict case, but must resolve so Prepare succeeds).
	cp := filepath.Join(root, "cp.sh")
	if err := os.WriteFile(cp, []byte("#!/bin/sh\ncat \"$1\" > \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `
context: s
nodes:
  a: { kind: markdown, path: x.md }
  b: { kind: markdown, path: y.md }
edges:
  - { type: sync, a: a, b: b, authority: a,
      transform_a_to_b: cp, transform_b_to_a: cp }
transforms:
  cp: { exec: "./cp.sh" }
`
	c := writeContext(t, root, "s", yaml)

	// First run: establish a baseline (both sides committed to state). Both
	// are "dirty" vs empty baseline on the very first run, which WOULD be a
	// conflict; so we seed state with the current hashes to simulate a clean
	// prior sync, then mutate BOTH sides.
	st := state.New()
	prep0, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	// Hash both via the prepared hasher and store as baseline.
	hAB := map[string]string{}
	for _, id := range []string{"a", "b"} {
		cur, _ := prep0.Store.Get(id)
		hAB[id] = prep0.Hasher.Hash(cur)
	}
	st.SetHashes("s", hAB)

	// Mutate BOTH sides since the baseline.
	if err := os.WriteFile(aPath, []byte("A EDITED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("B EDITED\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	res, err := prep.RunSync(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != orchestrator.StatusConflict {
		t.Fatalf("status = %s, want conflict", res.Status)
	}
	// Zero writes: both files unchanged.
	a, _ := os.ReadFile(aPath)
	b, _ := os.ReadFile(bPath)
	if string(a) != "A EDITED\n" || string(b) != "B EDITED\n" {
		t.Fatalf("conflict run mutated files: a=%q b=%q", a, b)
	}
	t.Logf("EVIDENCE: both-dirty sync -> conflict, zero writes (err=%v)", res.Err)
}

// TestSync_ExecTransform_RealRun proves an exec: transform shells to a real
// script, stages input/output temp files per the §5.2 contract, and the
// produced content is committed.
func TestSync_ExecTransform_RealRun(t *testing.T) {
	root := t.TempDir()
	srcPath := filepath.Join(root, "in.md")
	if err := os.WriteFile(srcPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// upper.sh: input path $1, output path $2 -> uppercases input to output.
	up := filepath.Join(root, "upper.sh")
	script := "#!/bin/sh\ntr a-z A-Z < \"$1\" > \"$2\"\n"
	if err := os.WriteFile(up, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `
context: e
nodes:
  src: { kind: markdown, path: in.md }
  out: { kind: summary,  path: out.md }
edges:
  - { type: derive-from, from: src, to: out, transform: up }
transforms:
  up: { exec: "./upper.sh" }
`
	c := writeContext(t, root, "e", yaml)
	st := state.New()
	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	res, err := prep.RunSync(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != orchestrator.StatusCommitted {
		t.Fatalf("status = %s, want committed (err=%v)", res.Status, res.Err)
	}
	got, err := os.ReadFile(filepath.Join(root, "out.md"))
	if err != nil {
		t.Fatalf("out not produced: %v", err)
	}
	if string(got) != "HELLO\n" {
		t.Fatalf("exec transform output = %q, want HELLO", got)
	}
	t.Logf("EVIDENCE: exec transform produced %q from %q", got, "hello\n")
}

// TestSync_ToolAbsent_HonestSkip proves that when a builtin's tool is absent,
// the run rolls back with a ToolAbsentError (honest), and verify SKIPs — it
// NEVER fakes a pass. We force absence by pointing PATH at an empty dir.
func TestSync_ToolAbsent_HonestSkip(t *testing.T) {
	root := t.TempDir()
	mdPath := filepath.Join(root, "d.md")
	if err := os.WriteFile(mdPath, []byte("# D\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := `
context: d
nodes:
  md:   { kind: markdown, path: d.md }
  html: { kind: html,     path: d.html }
edges:
  - { type: derive-from, from: md, to: html, transform: m2h }
transforms:
  m2h: { builtin: pandoc-html }
`
	c := writeContext(t, root, "d", yaml)

	// Hide pandoc by setting PATH to an empty temp dir for this test.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)
	if haveTool("pandoc") {
		t.Skip("pandoc still resolvable after PATH override (statically-known abs path?) — cannot force absence here")
	}

	st := state.New()
	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	res, err := prep.RunSync(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != orchestrator.StatusRolledBack {
		t.Fatalf("status = %s, want rolled-back (tool absent)", res.Status)
	}
	if !orchestrator.IsToolAbsent(res.Err) {
		t.Fatalf("err = %v, want ToolAbsentError", res.Err)
	}
	// Crucially: no html file faked into existence.
	if _, statErr := os.Stat(filepath.Join(root, "d.html")); !os.IsNotExist(statErr) {
		t.Fatalf("html was created despite absent tool — fake success!")
	}
	t.Logf("EVIDENCE: tool absent -> honest rollback, no fake html (err=%v)", res.Err)

	// verify must report ToolAbsent (SKIP), not stale/clean.
	prep2, err := Prepare(c, root, st)
	if err != nil {
		t.Fatal(err)
	}
	vr, err := prep2.Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !vr.ToolAbsent {
		t.Fatalf("verify ToolAbsent = false, want true (honest SKIP)")
	}
	t.Logf("EVIDENCE: verify honest SKIP — %s", vr.ToolReason)
}

// TestVerify_BinaryNode_InSyncAfterSync is the regression pin for the
// binary-hash verify defect: after `sync`, an immediate read-only `verify` of
// a BINARY node kind (docx/pdf) MUST report in-sync (no stale) — proving the
// sync-record path and the verify-check path hash the binary identically (raw
// bytes, no text normalization) AND that the producer output is reproducible
// across the sync→verify time gap.
//
// Before the fix, all docx nodes (and the timestamp/dir-sensitive pdf nodes)
// were falsely flagged STALE immediately after a clean sync because (a) the
// docx/pdf hashers text-normalized binary bytes, and (b) pandoc/weasyprint
// embedded wall-clock timestamps + directory-relative URIs into the output, so
// the verify re-derivation never byte-matched the committed artefact.
//
// Two layers, so the pin holds whether or not pandoc is on PATH:
//   - real pandoc-docx derivation when pandoc is present (exercises the actual
//     bug end-to-end);
//   - a synthetic binary node via an exec transform whose output deliberately
//     contains bytes a text-normalizer WOULD alter (CRLF, a NUL, trailing
//     whitespace before a newline, a trailing newline). The docx adapter's
//     RawByteHasher must hash those verbatim, identically in both paths.
func TestVerify_BinaryNode_InSyncAfterSync(t *testing.T) {
	t.Run("real_pandoc_docx", func(t *testing.T) {
		if !haveTool("pandoc") {
			t.Skip("pandoc absent — honest SKIP; the docx derivation needs pandoc")
		}
		root := t.TempDir()
		mdPath := filepath.Join(root, "b.md")
		if err := os.WriteFile(mdPath, []byte("# Binary\n\nDocx body **bold**.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		yaml := `
context: b
nodes:
  md:   { kind: markdown, path: b.md }
  docx: { kind: docx,     path: b.docx }
edges:
  - { type: derive-from, from: md, to: docx, transform: m2d }
transforms:
  m2d: { builtin: pandoc-docx }
`
		c := writeContext(t, root, "b", yaml)
		st := state.New()

		prep, err := Prepare(c, root, st)
		if err != nil {
			t.Fatal(err)
		}
		res, err := prep.RunSync(st)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		if res.Status != orchestrator.StatusCommitted {
			t.Fatalf("sync status = %s, want committed (err=%v)", res.Status, res.Err)
		}
		// Real evidence: a non-empty .docx (zip — starts with "PK") was written.
		got, err := os.ReadFile(filepath.Join(root, "b.docx"))
		if err != nil || len(got) == 0 {
			t.Fatalf("docx not produced: %v (len=%d)", err, len(got))
		}
		if len(got) < 2 || got[0] != 'P' || got[1] != 'K' {
			t.Fatalf("produced docx is not a zip container (first bytes %x)", got[:min(4, len(got))])
		}

		// THE PIN: verify immediately after sync must be in-sync (exit 0). Run
		// it 3x to catch any timestamp/dir flapping.
		for i := 0; i < 3; i++ {
			prep2, err := Prepare(c, root, st)
			if err != nil {
				t.Fatal(err)
			}
			vr, err := prep2.Verify()
			if err != nil {
				t.Fatalf("verify iter %d: %v", i, err)
			}
			if len(vr.Stale) != 0 {
				t.Fatalf("verify iter %d: docx falsely STALE %v — binary-hash regression", i, vr.Stale)
			}
		}
		t.Logf("EVIDENCE: real pandoc docx (%d bytes) verifies in-sync 3x after sync", len(got))
	})

	t.Run("synthetic_binary", func(t *testing.T) {
		root := t.TempDir()
		srcPath := filepath.Join(root, "s.md")
		if err := os.WriteFile(srcPath, []byte("seed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// emit.sh writes DETERMINISTIC synthetic binary bytes to its output ($2)
		// containing exactly the sequences a text-normalizer would alter: a NUL,
		// a CRLF, trailing spaces+tab before a newline, and a trailing newline.
		// (It ignores its input so the output is fixed and self-cleaning.)
		emit := filepath.Join(root, "emit.sh")
		// printf octal escapes: \000 NUL, \015\012 CRLF, "x  \t\n", trailing \n.
		script := "#!/bin/sh\nprintf 'A\\000B\\015\\012x  \\t\\nC\\n' > \"$2\"\n"
		if err := os.WriteFile(emit, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		yaml := `
context: s
nodes:
  src:  { kind: markdown, path: s.md }
  blob: { kind: docx,     path: s.bin }
edges:
  - { type: derive-from, from: src, to: blob, transform: emit }
transforms:
  emit: { exec: "./emit.sh" }
`
		c := writeContext(t, root, "s", yaml)
		st := state.New()

		prep, err := Prepare(c, root, st)
		if err != nil {
			t.Fatal(err)
		}
		res, err := prep.RunSync(st)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		if res.Status != orchestrator.StatusCommitted {
			t.Fatalf("sync status = %s, want committed (err=%v)", res.Status, res.Err)
		}
		// Confirm the on-disk bytes are the raw synthetic payload (a
		// text-normalizer would have collapsed CRLF and stripped the trailing
		// whitespace/newline — the raw bytes prove no such mangling on write).
		want := []byte{'A', 0x00, 'B', '\r', '\n', 'x', ' ', ' ', '\t', '\n', 'C', '\n'}
		got, err := os.ReadFile(filepath.Join(root, "s.bin"))
		if err != nil {
			t.Fatalf("blob not produced: %v", err)
		}
		if string(got) != string(want) {
			t.Fatalf("on-disk blob = %x, want raw %x", got, want)
		}

		// THE PIN: verify must be in-sync (the docx adapter's RawByteHasher
		// hashes these bytes verbatim in BOTH the record and check paths). A
		// text-normalizing hasher would hash produced!=onDisk (or mask the NUL)
		// and falsely flag stale. Run 3x for determinism.
		for i := 0; i < 3; i++ {
			prep2, err := Prepare(c, root, st)
			if err != nil {
				t.Fatal(err)
			}
			vr, err := prep2.Verify()
			if err != nil {
				t.Fatalf("verify iter %d: %v", i, err)
			}
			if len(vr.Stale) != 0 {
				t.Fatalf("verify iter %d: synthetic binary falsely STALE %v — raw-hash regression", i, vr.Stale)
			}
		}
		t.Logf("EVIDENCE: synthetic binary (NUL+CRLF+trailing-ws, %d bytes) verifies in-sync 3x", len(got))
	})
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
