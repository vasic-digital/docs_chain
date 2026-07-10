package runner

import (
	"os"
	"path/filepath"
	"testing"

	"digital.vasic.docs_chain/internal/state"
)

// TestVerify_MultiLevelStaleness_NotMasked reproduces a Verify false-negative
// hypothesis: in a chain src -> mid -> leaf, Verify reads each derived node's
// SOURCES from their LIVE on-disk content (p.Store.Get), not from a freshly
// recomputed value. So when `mid` on disk is stale w.r.t. `src`, Verify
// computes `leaf` from the STALE on-disk `mid`. If on-disk `leaf` happens to
// match leaf-from-stale-mid, Verify reports leaf in-sync — even though a real
// sync would first regenerate mid, then leaf, changing leaf. That is a stale
// artifact the CI gate would pass.
//
// Transforms are exec scripts (no external tool): mid = "MID:" + src ;
// leaf = "LEAF:" + mid. We set up on-disk so:
//
//	src  = "NEW"
//	mid  = "MID:OLD"     (stale: real mid would be "MID:NEW")
//	leaf = "LEAF:MID:OLD" (consistent with the STALE on-disk mid)
//
// Real sync => mid="MID:NEW", leaf="LEAF:MID:NEW". So leaf IS stale.
// Correct Verify must report leaf STALE. The bug reports it in-sync.
func TestVerify_MultiLevelStaleness_NotMasked(t *testing.T) {
	root := t.TempDir()

	// exec transform script: reads ONLY the LAST input path before the output
	// path. docs_chain passes: <in1>...<inN> <out> <args...>. Our transforms are
	// single-input, so argv = <in> <out> <prefix>. Script writes prefix+contents.
	script := filepath.Join(root, "prefix.sh")
	body := "#!/bin/sh\nin=\"$1\"; out=\"$2\"; prefix=\"$3\"\nprintf '%s' \"$prefix$(cat \"$in\")\" > \"$out\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src.txt", "NEW")
	write("mid.txt", "MID:OLD")       // STALE vs src
	write("leaf.txt", "LEAF:MID:OLD") // consistent with the stale on-disk mid

	yaml := `
context: chain
nodes:
  src:  { kind: markdown, path: docs/src.txt }
  mid:  { kind: markdown, path: docs/mid.txt }
  leaf: { kind: markdown, path: docs/leaf.txt }
edges:
  - { type: derive-from, from: src, to: mid,  transform: t_mid }
  - { type: derive-from, from: mid, to: leaf, transform: t_leaf }
transforms:
  t_mid:  { exec: ./prefix.sh, args: ["MID:"] }
  t_leaf: { exec: ./prefix.sh, args: ["LEAF:"] }
`
	c := writeContext(t, root, "chain", yaml)
	st := state.New()
	prep, err := Prepare(c, root, st)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	vr, err := prep.Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	t.Logf("Stale=%v", vr.Stale)

	hasMid, hasLeaf := false, false
	for _, s := range vr.Stale {
		if s == "mid" {
			hasMid = true
		}
		if s == "leaf" {
			hasLeaf = true
		}
	}
	if !hasMid {
		t.Fatalf("expected mid stale (sanity); got %v", vr.Stale)
	}
	if !hasLeaf {
		t.Fatalf("VERIFY FALSE-NEGATIVE BUG: leaf is computed from the STALE on-disk "+
			"mid, so verify reports leaf in-sync; but a real sync regenerates mid then "+
			"leaf, changing leaf. CI gate passes on a stale artifact. Stale=%v", vr.Stale)
	}
}
