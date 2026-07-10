package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLI_Verify_CorruptState_NoPanic pins a verify robustness defect: the
// read-only drift gate MUST tolerate a corrupt/unreadable `.docs_chain/state.json`
// exactly the way it tolerates a MISSING one — by proceeding with a cold (empty)
// baseline — because Verify recomputes every derived node and compares the freshly
// produced bytes to on-disk content, so it never consults the stored hash baseline
// at all.
//
// RED (pre-fix): cmdVerify did `st, _ := state.Load(statePath)`, discarding the
// error. state.Load returns (nil, err) on a parse/read failure, so a corrupt
// state.json left st == nil; runner.Prepare then dereferenced the nil *State
// (baseline := st.Hashes(...)) and the CLI PANICKED (Go runtime exit 2, a "panic:"
// stack on stderr) — a CI gate crashing on a truncated state file instead of
// degrading gracefully.
//
// GREEN (post-fix): a corrupt state.json is tolerated (cold baseline); verify runs
// the drift check and reports in-sync, exit 0.
//
// The fixture uses an exec transform (no external tool) so the drift check is
// deterministic and self-contained: out.md already equals cp(src.md), so a
// correct verify reports in-sync.
func TestCLI_Verify_CorruptState_NoPanic(t *testing.T) {
	bin := buildCLI(t)
	root := t.TempDir()

	mustWrite(t, filepath.Join(root, "src.md"), "hello\n")
	mustWrite(t, filepath.Join(root, "out.md"), "hello\n") // already == cp(src)
	cp := filepath.Join(root, "cp.sh")
	mustWrite(t, cp, "#!/bin/sh\ncat \"$1\" > \"$2\"\n")
	if err := os.Chmod(cp, 0o755); err != nil {
		t.Fatal(err)
	}
	ctxDir := filepath.Join(root, ".docs_chain", "contexts")
	mustWrite(t, filepath.Join(ctxDir, "c.yaml"), `
context: c
nodes:
  src: { kind: markdown, path: src.md }
  out: { kind: summary,  path: out.md }
edges:
  - { type: derive-from, from: src, to: out, transform: cp }
transforms:
  cp: { exec: "./cp.sh" }
`)

	// A state.json that EXISTS but does NOT parse (truncated / corrupt write).
	// state.Load returns (nil, error) for this — the exact input that used to
	// panic the verify path.
	mustWrite(t, filepath.Join(root, ".docs_chain", "state.json"), "{ this is not valid json\n")

	out, code := runCLI(t, bin, root, "verify", "c")

	if strings.Contains(out, "panic:") {
		t.Fatalf("VERIFY PANICKED on a corrupt state.json (nil *State dereference in Prepare); "+
			"the CI gate must tolerate an unreadable state file, not crash.\nexit=%d\n%s", code, out)
	}
	if code != 0 {
		t.Fatalf("verify(corrupt-state) exit = %d, want 0 (tolerate corrupt state via cold baseline)\n%s", code, out)
	}
	if !strings.Contains(out, "in-sync") {
		t.Fatalf("verify(corrupt-state) did not run the drift check (no in-sync verdict):\n%s", out)
	}
	t.Logf("EVIDENCE: verify tolerates corrupt state.json (cold baseline), reports in-sync, exit 0:\n%s", out)
}
