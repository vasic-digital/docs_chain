package runner

import (
	"os"
	"path/filepath"
	"testing"

	"digital.vasic.docs_chain/internal/orchestrator"
	"digital.vasic.docs_chain/internal/state"
)

// TestSync_DerivedNodeSelfDrift_SelfHealsAndStaysVerifyClean reproduces the
// root cause of task #80 (boba GOVERNANCE_AUDIT finding: `docs_chain sync
// --all` reports codegraph-status IN-SYNC while `docs_chain verify --all`
// reports `STALE: [status_docx]` for the SAME context, in the SAME state).
//
// Root cause: graph.Recompute's Step 1 hashes EVERY node (source AND
// derived) against its stored baseline to build the dirty set, but Step 3's
// topo-walk candidacy for a derive-from TARGET consults ONLY whether its
// SOURCE is dirty — never whether the target's OWN on-disk bytes drifted
// from ITS OWN stored baseline. When a derived artifact is overwritten
// out-of-band (a foreign/stale pandoc invocation, a leftover from before a
// context was wired into docs_chain, manual hand-edit, etc.) while its
// source is untouched, Step 1 correctly marks the derived node "dirty" —
// but the topo-walk skips it (no dirty source), so its transform never
// re-runs. `orchestrator.Run` then calls `g.CommitHashes(res)` UNCONDITIONALLY
// (even on the `len(staging.order)==0` in-sync fast path), which blesses
// `res.NewHashes[leaf]` — the CURRENT (drifted/foreign) on-disk hash computed
// in Step 1 — as the new baseline. From that moment on, `sync` reports
// "in-sync" forever (self-consistent with the now-corrupted baseline) while
// `runner.Verify()` — which never consults the baseline and always
// freshly re-derives every target from its live sources — correctly and
// PERMANENTLY reports the artifact STALE. The two commands never agree
// again until an operator manually intervenes.
//
// This test drives that EXACT sequence end-to-end through the real runner:
//  1. A real first sync commits a CORRECT leaf.
//  2. The leaf is overwritten out-of-band (simulating a foreign/stale write
//     docs_chain did not perform).
//  3. A second sync MUST detect the drift and self-heal the leaf back to
//     `transform(source)` — never silently adopt the foreign bytes as the
//     new baseline.
//  4. Verify() immediately after MUST report zero staleness — `sync` and
//     `verify` must never disagree about whether a context is in-sync.
func TestSync_DerivedNodeSelfDrift_SelfHealsAndStaysVerifyClean(t *testing.T) {
	root := t.TempDir()

	// exec transform script identical in shape to the one already proven in
	// TestVerify_MultiLevelStaleness_NotMasked: docs_chain stages the source
	// to a temp input, so argv = <in> <out> <prefix>.
	script := filepath.Join(root, "prefix.sh")
	body := "#!/bin/sh\nin=\"$1\"; out=\"$2\"; prefix=\"$3\"\nprintf '%s' \"$prefix$(cat \"$in\")\" > \"$out\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(docs, "src.txt")
	leafPath := filepath.Join(docs, "leaf.txt")
	if err := os.WriteFile(srcPath, []byte("SEED"), 0o644); err != nil {
		t.Fatal(err)
	}

	yaml := `
context: drift
nodes:
  src:  { kind: markdown, path: docs/src.txt }
  leaf: { kind: markdown, path: docs/leaf.txt }
edges:
  - { type: derive-from, from: src, to: leaf, transform: t_leaf }
transforms:
  t_leaf: { exec: ./prefix.sh, args: ["PFX:"] }
`
	c := writeContext(t, root, "drift", yaml)
	st := state.New()

	// --- Step 1: real first sync. leaf does not exist -> committed, and its
	// content is the REAL transform output (the correctness baseline).
	prep1, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare1: %v", err)
	}
	res1, err := prep1.RunSync(st)
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if res1.Status != orchestrator.StatusCommitted {
		t.Fatalf("run1 status = %s, want committed (err=%v)", res1.Status, res1.Err)
	}
	got, err := os.ReadFile(leafPath)
	if err != nil {
		t.Fatalf("leaf not produced: %v", err)
	}
	if string(got) != "PFX:SEED" {
		t.Fatalf("run1 leaf = %q, want %q (sanity: real transform ran)", got, "PFX:SEED")
	}
	t.Logf("EVIDENCE step1: real sync produced leaf=%q, baseline committed", got)

	// --- Step 2: simulate an out-of-band write that bypasses docs_chain
	// entirely (a foreign export tool, a manual pandoc invocation, a
	// leftover from before this context existed, etc.). The SOURCE is left
	// completely untouched.
	if err := os.WriteFile(leafPath, []byte("FOREIGN-CONTENT-NOT-FROM-TRANSFORM"), 0o644); err != nil {
		t.Fatal(err)
	}

	// --- Step 3: a second sync MUST notice the drift (leaf's own on-disk
	// bytes no longer match its recorded baseline) and self-heal — it must
	// NOT silently bless the foreign bytes as the new baseline while
	// reporting a peaceful "in-sync".
	prep2, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare2: %v", err)
	}
	res2, err := prep2.RunSync(st)
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	healed, err := os.ReadFile(leafPath)
	if err != nil {
		t.Fatalf("leaf unreadable after run2: %v", err)
	}
	if string(healed) != "PFX:SEED" {
		t.Fatalf("SILENT-CORRUPTION BUG: after the out-of-band write, sync (status=%s) left "+
			"leaf=%q on disk instead of self-healing it back to the real transform output %q. "+
			"The source (%q) never changed, so a correct engine MUST regenerate the derived "+
			"artifact from it rather than adopt the foreign bytes as the new baseline.",
			res2.Status, healed, "PFX:SEED", "SEED")
	}
	t.Logf("EVIDENCE step3: sync self-healed the drifted leaf back to %q (status=%s)", healed, res2.Status)

	// --- Step 4: the load-bearing contract — sync and verify must NEVER
	// disagree. Immediately after a sync run settles, verify (which always
	// independently re-derives every target from its live sources) MUST
	// report zero staleness.
	prep3, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare3: %v", err)
	}
	vr, err := prep3.Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(vr.Stale) != 0 {
		t.Fatalf("SYNC/VERIFY DISAGREEMENT BUG (task #80 root cause): sync (status=%s) reports "+
			"the context settled, but verify still reports STALE=%v for the very same state. "+
			"`docs_chain sync --all` and `docs_chain verify --all` must never disagree.",
			res2.Status, vr.Stale)
	}
	t.Logf("EVIDENCE step4: verify agrees with sync — zero staleness (stale=%v)", vr.Stale)
}
