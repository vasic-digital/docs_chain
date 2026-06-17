package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// e2e drives the REAL built `docs_chain` binary as a subprocess across every
// subcommand + the documented exit-code contract + every builtin, asserting
// real produced artefacts and real exit codes. Fully automated (no manual step,
// §11.4.98) — `go test` builds + runs the binary and checks observable output.

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docs_chain_e2e")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}
	return bin
}

type runOut struct {
	code   int
	stdout string
	stderr string
}

func runBin(t *testing.T, bin string, args ...string) runOut {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var so, se strings.Builder
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	return runOut{code: code, stdout: so.String(), stderr: se.String()}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestE2E_RealBinary_PureGoChain exercises the pure-Go md→sqlite→md chain end to
// end through the real binary (never SKIPs — no external tool). It asserts the
// documented exit codes for doctor/sync/verify/graph + verify-stale + an error.
func TestE2E_RealBinary_PureGoChain(t *testing.T) {
	bin := buildBinary(t)
	root := t.TempDir()
	ctxDir := filepath.Join(root, ".docs_chain", "contexts")
	write(t, filepath.Join(ctxDir, "tables.yaml"), `
context: tables
nodes:
  src:  { kind: markdown, path: data/items.md }
  db:   { kind: sqlite,   path: data/items.db }
  view: { kind: markdown, path: data/items.view.md }
edges:
  - { type: derive-from, from: src, to: db,   transform: m2d }
  - { type: derive-from, from: db,  to: view, transform: d2m }
transforms:
  m2d: { builtin: md-to-sqlite }
  d2m: { builtin: sqlite-to-md }
`)
	write(t, filepath.Join(root, "data", "items.md"), "## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n")

	// doctor → exit 0
	r := runBin(t, bin, "doctor", "--root", root, "tables")
	if r.code != 0 {
		t.Fatalf("doctor exit=%d want 0\n%s%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "OK (3 nodes, 2 edges)") {
		t.Fatalf("doctor output unexpected:\n%s", r.stdout)
	}

	// graph → topo order printed
	r = runBin(t, bin, "graph", "--root", root, "tables")
	if r.code != 0 || !strings.Contains(r.stdout, "src") || !strings.Contains(r.stdout, "view") {
		t.Fatalf("graph exit=%d output=%s", r.code, r.stdout)
	}

	// sync → exit 0, real db + view produced, evidence dir written
	r = runBin(t, bin, "sync", "--root", root, "tables")
	if r.code != 0 {
		t.Fatalf("sync exit=%d want 0\n%s%s", r.code, r.stdout, r.stderr)
	}
	if fi, err := os.Stat(filepath.Join(root, "data", "items.db")); err != nil || fi.Size() == 0 {
		t.Fatalf("db not produced by binary sync")
	}
	view, _ := os.ReadFile(filepath.Join(root, "data", "items.view.md"))
	if !strings.Contains(string(view), "| 1 | alpha |") {
		t.Fatalf("view not produced:\n%s", view)
	}
	if !strings.Contains(r.stdout, "evidence:") {
		t.Fatalf("sync did not report an evidence artefact:\n%s", r.stdout)
	}
	// the §11.4.69 evidence artefact really exists on disk
	if matches, _ := filepath.Glob(filepath.Join(root, "qa-results", "docs_chain", "*", "*")); len(matches) == 0 {
		t.Fatalf("no qa-results evidence artefact written")
	}

	// verify → exit 0 (in-sync) right after sync
	r = runBin(t, bin, "verify", "--root", root, "tables")
	if r.code != 0 {
		t.Fatalf("verify exit=%d want 0 (in-sync)\n%s%s", r.code, r.stdout, r.stderr)
	}

	// edit source → verify → exit 1 (STALE) — the deterministic drift gate bites
	write(t, filepath.Join(root, "data", "items.md"), "## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n| 2 | beta |\n")
	r = runBin(t, bin, "verify", "--root", root, "tables")
	if r.code != 1 {
		t.Fatalf("verify after edit exit=%d want 1 (stale)\n%s%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "STALE") {
		t.Fatalf("verify did not report STALE:\n%s", r.stdout)
	}
	t.Logf("EVIDENCE: real binary verify exit=1 STALE after source edit: %s", strings.TrimSpace(r.stdout))

	// re-sync → exit 0, new row propagated to view
	if r = runBin(t, bin, "sync", "--root", root, "tables"); r.code != 0 {
		t.Fatalf("resync exit=%d", r.code)
	}
	view2, _ := os.ReadFile(filepath.Join(root, "data", "items.view.md"))
	if !strings.Contains(string(view2), "| 2 | beta |") {
		t.Fatalf("resync did not propagate the new row:\n%s", view2)
	}

	// unknown context → exit 1 (generic error)
	if r = runBin(t, bin, "sync", "--root", root, "nope"); r.code != 1 {
		t.Fatalf("unknown context exit=%d want 1", r.code)
	}
	// unknown subcommand → exit 1
	if r = runBin(t, bin, "frobnicate"); r.code != 1 {
		t.Fatalf("unknown subcommand exit=%d want 1", r.code)
	}
}

// TestE2E_RealBinary_PandocColorize exercises the md→html→colorized-html chain
// through the real binary IF pandoc is present; honest SKIP otherwise (proves
// the §11.4.106(E) anti-bluff ToolAbsentError → SKIP, never a fake pass).
func TestE2E_RealBinary_PandocColorize(t *testing.T) {
	if _, err := exec.LookPath("pandoc"); err != nil {
		t.Skip("SKIP-OK: pandoc absent — honest SKIP (the md→html builtin needs pandoc); not a fake pass")
	}
	bin := buildBinary(t)
	root := t.TempDir()
	ctxDir := filepath.Join(root, ".docs_chain", "contexts")
	write(t, filepath.Join(ctxDir, "tracker.yaml"), `
context: tracker
nodes:
  iss:   { kind: markdown, path: Issues.md }
  html:  { kind: html,     path: Issues.html }
  color: { kind: html,     path: Issues.colored.html }
edges:
  - { type: derive-from, from: iss,  to: html,  transform: m2h }
  - { type: derive-from, from: html, to: color, transform: cz }
transforms:
  m2h: { builtin: pandoc-html }
  cz:  { builtin: colorize-html }
`)
	write(t, filepath.Join(root, "Issues.md"),
		"| ID | Type | Status |\n| --- | --- | --- |\n| H-1 | bug | open |\n| H-2 | task | fixed |\n| H-3 | feature | blocker |\n")

	if r := runBin(t, bin, "sync", "--root", root, "tracker"); r.code != 0 {
		t.Fatalf("sync exit=%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	colored, err := os.ReadFile(filepath.Join(root, "Issues.colored.html"))
	if err != nil {
		t.Fatalf("colorized html not produced: %v", err)
	}
	cs := string(colored)
	// §11.4.23 colors applied through the real binary chain.
	for _, want := range []string{"#ffdce0", "#d6f5d9", "#ff6b6b", "background-color"} {
		if !strings.Contains(cs, want) {
			t.Fatalf("colorized html missing %q:\n%s", want, cs)
		}
	}
	// verify in-sync (the whole md→html→colorize chain re-derives byte-stable)
	if r := runBin(t, bin, "verify", "--root", root, "tracker"); r.code != 0 {
		t.Fatalf("verify exit=%d want 0 (chain not byte-stable?)\n%s%s", r.code, r.stdout, r.stderr)
	}
	t.Logf("EVIDENCE: real binary md→html→colorize chain produced §11.4.23 colors + verify in-sync")
}
