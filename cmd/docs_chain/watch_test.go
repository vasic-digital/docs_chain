package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"digital.vasic.docs_chain/internal/config"
)

// TestWatch_FullAutomation proves the fsnotify watch daemon is REAL and fully
// automated (no manual step): runWatch syncs on startup, then a programmatic
// edit to a SOURCE file triggers a re-sync that propagates through the DAG. Uses
// the pure-Go md→sqlite→md chain so it never SKIPs on a missing external tool.
func TestWatch_FullAutomation(t *testing.T) {
	root := t.TempDir()
	ctxDir := filepath.Join(root, ".docs_chain", "contexts")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ctxDir, "w.yaml"), `
context: w
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
	srcPath := filepath.Join(root, "data", "items.md")
	mustWrite(t, srcPath, "## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n")
	viewPath := filepath.Join(root, "data", "items.view.md")

	c, err := config.Load(filepath.Join(ctxDir, "w.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	outF, _ := os.CreateTemp(root, "out")
	errF, _ := os.CreateTemp(root, "err")
	defer outF.Close()
	defer errF.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWatch(ctx, root, []*config.Context{c}, 40*time.Millisecond, outF, errF) }()

	// 1) Initial sync (runs on startup) must produce the view with row 1.
	if !waitForFileContains(viewPath, "| 1 | alpha |", 4*time.Second) {
		cancel()
		<-done
		t.Fatalf("initial watch sync did not produce the view:\n%s", readOr(viewPath))
	}

	// 2) Programmatic SOURCE edit -> the watcher must re-sync and propagate it.
	mustWrite(t, srcPath, "## items\n\n| id | name |\n| --- | --- |\n| 1 | alpha |\n| 2 | beta |\n")
	got := waitForFileContains(viewPath, "| 2 | beta |", 6*time.Second)

	cancel()
	if werr := <-done; werr != nil {
		t.Fatalf("runWatch returned error: %v", werr)
	}
	if !got {
		t.Fatalf("watch did NOT re-sync after a source edit (drift not propagated — bluff):\n%s", readOr(viewPath))
	}
	t.Logf("EVIDENCE: watch daemon auto-synced a source edit through md->sqlite->md; view now:\n%s", readOr(viewPath))
}

func waitForFileContains(path, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), want) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func readOr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	return string(b)
}
