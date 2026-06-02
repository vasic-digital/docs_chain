package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"digital.vasic.docs_chain/internal/config"
)

// cmdWatch runs the fsnotify watch daemon (USER_GUIDE §8): it watches the
// SOURCE files of the selected context(s) and runs `sync` (debounced) whenever
// a source changes. Derived-output writes are NOT watched, so a sync never
// re-triggers itself. Runs until SIGINT/SIGTERM, then exits 0.
func cmdWatch(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "watch every context")
	rootFlag := fs.String("root", ".", "project root containing .docs_chain/")
	debounce := fs.Duration("debounce", 300*time.Millisecond, "coalesce rapid edits before syncing")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	// Delegate context selection to the shared loader (reconstruct its args).
	sel := []string{"--root", *rootFlag}
	if *all {
		sel = append(sel, "--all")
	} else {
		sel = append(sel, fs.Args()...)
	}
	root, contexts, code := loadSelected(sel, stderr)
	if code != exitOK {
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runWatch(ctx, root, contexts, *debounce, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "docs_chain: watch: %v\n", err)
		return exitError
	}
	return exitOK
}

// runWatch is the testable watch loop: watch the source dirs, debounce events
// on known source paths, run sync on the trigger, exit when ctx is cancelled.
func runWatch(ctx context.Context, root string, contexts []*config.Context, debounce time.Duration, stdout, stderr *os.File) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	sources := sourcePaths(root, contexts)
	dirs := parentDirs(sources)
	for d := range dirs {
		if err := w.Add(d); err != nil {
			fmt.Fprintf(stderr, "docs_chain: watch: cannot watch %s: %v\n", d, err)
		}
	}
	fmt.Fprintf(stdout, "docs_chain: watching %d source dir(s) for %d context(s) (debounce %s); Ctrl-C to stop\n", len(dirs), len(contexts), debounce)

	// Initial sync so the tree is consistent at startup.
	runSyncContexts(root, contexts, stdout, stderr)

	trigger := make(chan struct{}, 1)
	var timer *time.Timer
	fire := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounce, func() {
			select {
			case trigger <- struct{}{}:
			default:
			}
		})
	}
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			fmt.Fprintln(stdout, "docs_chain: watch stopped")
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if isSourceEvent(ev, sources) {
				fire()
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(stderr, "docs_chain: watch error: %v\n", err)
		case <-trigger:
			runSyncContexts(root, contexts, stdout, stderr)
		}
	}
}

// sourcePaths returns the absolute paths of every node that is an INPUT —
// the `from` side of a derive edge or either side of a sync edge. Derived-only
// outputs are deliberately excluded so a sync write never re-triggers a sync.
func sourcePaths(root string, contexts []*config.Context) map[string]bool {
	set := make(map[string]bool)
	add := func(c *config.Context, id string) {
		if ns, ok := c.Nodes[id]; ok {
			abs := ns.Path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(root, abs)
			}
			set[filepath.Clean(abs)] = true
		}
	}
	for _, c := range contexts {
		for _, e := range c.Edges {
			switch e.Type {
			case config.EdgeDeriveFrom:
				for _, f := range e.From {
					add(c, f)
				}
			case config.EdgeSync:
				add(c, e.A)
				add(c, e.B)
			}
		}
	}
	return set
}

// parentDirs returns the unique parent directories of the given file paths
// (fsnotify watches directories; file events bubble up to them).
func parentDirs(paths map[string]bool) map[string]bool {
	dirs := make(map[string]bool)
	for p := range paths {
		dirs[filepath.Dir(p)] = true
	}
	return dirs
}

// isSourceEvent reports whether a watcher event is a write/create/rename/remove
// of one of the watched SOURCE files (ignores chmod-only + derived/output noise).
func isSourceEvent(ev fsnotify.Event, sources map[string]bool) bool {
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
		return false
	}
	return sources[filepath.Clean(ev.Name)]
}
