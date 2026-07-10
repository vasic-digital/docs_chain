// Command docs_chain is the Phase-4 consumer-facing CLI for Docs Chain: it
// loads per-context YAML from `.docs_chain/contexts/`, wires each context to
// the Phase 1-3 engine + Phase 2 adapters, and exposes the documented
// subcommands with their exit-code contract.
//
// Subcommands (docs/ARCHITECTURE.md §12, docs/USER_GUIDE.md §7,§10):
//
//	doctor  [--all | <context>]   validate contexts (parse + graph + tools)
//	sync    [--all | <context>]   propagate atomically, update state.json
//	verify  [--all | <context>]   read-only drift check (CI/pre-build gate)
//	graph   <context>             print topo order / DAG (debug)
//
// Exit codes:
//
//	0  in-sync / applied / healthy
//	1  generic error (bad args, IO, missing contexts dir)
//	2  sync conflict (both sides of a sync edge dirty) — never silent-merge
//	3  a transform failed; run rolled back, no live changes
//	4  cycle or config/validation error
//
// `verify` exits non-zero (1) when any node is stale (the deterministic
// sink-side gate). A run's evidence is written under
// qa-results/docs_chain/<run-id>/.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"digital.vasic.docs_chain/internal/config"
	"digital.vasic.docs_chain/internal/graph"
	"digital.vasic.docs_chain/internal/orchestrator"
	"digital.vasic.docs_chain/internal/runner"
	"digital.vasic.docs_chain/internal/state"
)

// Exit codes (the documented contract).
const (
	exitOK        = 0
	exitError     = 1
	exitConflict  = 2
	exitTransform = 3
	exitConfig    = 4
)

const contextsRel = ".docs_chain/contexts"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		usage(stderr)
		return exitError
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "doctor":
		return cmdDoctor(rest, stdout, stderr)
	case "sync":
		return cmdSync(rest, stdout, stderr)
	case "rebaseline":
		return cmdReBaseline(rest, stdout, stderr)
	case "verify":
		return cmdVerify(rest, stdout, stderr)
	case "graph":
		return cmdGraph(rest, stdout, stderr)
	case "watch":
		return cmdWatch(rest, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "docs_chain (Phase 4 CLI) — sync | verify | doctor | graph | watch")
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "docs_chain: unknown subcommand %q\n", cmd)
		usage(stderr)
		return exitError
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `docs_chain — content-hash bidirectional doc/DB propagation

Usage:
  docs_chain doctor  [--all | <context>] [--root DIR]   validate contexts (no writes)
  docs_chain sync    [--all | <context>] [--root DIR]   propagate atomically, update state
  docs_chain rebaseline [--all | <context>] [--root DIR] re-baseline sync edges from their
                                                         AUTHORITY side only (non-authority side
                                                         regenerated + state recorded; authority
                                                         side READ-ONLY, never written)
  docs_chain verify  [--all | <context>] [--root DIR]   read-only drift check (CI gate)
  docs_chain graph   <context>            [--root DIR]   print topo order + edges (debug)
  docs_chain watch   [--all | <context>] [--root DIR] [--debounce 300ms]
                                                         sync on source change (fsnotify daemon)

Exit codes: 0 ok · 1 error · 2 conflict · 3 transform-fail · 4 cycle/config-error
Contexts live in <root>/.docs_chain/contexts/*.yaml ; state in <root>/.docs_chain/state.json
`)
}

// commonFlags parses --root and the positional/--all selector shared by
// doctor/sync/verify. It returns the project root, the contexts to act on
// (loaded + validated), and a non-zero exit code on failure.
func loadSelected(args []string, stderr *os.File) (root string, contexts []*config.Context, code int) {
	fs := flag.NewFlagSet("select", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "act on every context")
	rootFlag := fs.String("root", ".", "project root containing .docs_chain/")
	if err := fs.Parse(args); err != nil {
		return "", nil, exitError
	}
	root, _ = filepath.Abs(*rootFlag)
	dir := filepath.Join(root, contextsRel)
	pos := fs.Args()

	if *all {
		cs, err := config.LoadDir(dir)
		if err != nil {
			return root, nil, reportLoadErr(err, stderr)
		}
		if len(cs) == 0 {
			fmt.Fprintf(stderr, "docs_chain: no contexts found under %s\n", dir)
			return root, nil, exitError
		}
		return root, cs, exitOK
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "docs_chain: specify exactly one <context> or --all")
		return root, nil, exitError
	}
	path := filepath.Join(dir, pos[0]+".yaml")
	c, err := config.Load(path)
	if err != nil {
		return root, nil, reportLoadErr(err, stderr)
	}
	return root, []*config.Context{c}, exitOK
}

// reportLoadErr maps a config load/validation error to the right exit code: a
// *config.ConfigError (and any wrapped cycle) is a config error (4); anything
// else (missing file/dir) is a generic error (1).
func reportLoadErr(err error, stderr *os.File) int {
	var ce *config.ConfigError
	if errors.As(err, &ce) {
		fmt.Fprintf(stderr, "docs_chain: config error: %v\n", err)
		return exitConfig
	}
	fmt.Fprintf(stderr, "docs_chain: %v\n", err)
	return exitError
}

// cmdDoctor validates every selected context: parse + graph.Validate
// (already done by Load) + per-transform tool availability. It never mutates.
func cmdDoctor(args []string, stdout, stderr *os.File) int {
	root, contexts, code := loadSelected(args, stderr)
	if code != exitOK {
		return code
	}
	worst := exitOK
	for _, c := range contexts {
		fmt.Fprintf(stdout, "context %q (%s)\n", c.Name, relTo(root, c.SourcePath))
		fmt.Fprintf(stdout, "  parse + graph: OK (%d nodes, %d edges)\n", len(c.Nodes), len(c.Edges))
		// Tool availability per referenced builtin/exec transform.
		issues := checkTools(c, root)
		if len(issues) == 0 {
			fmt.Fprintf(stdout, "  transforms: OK (all required tools present)\n")
		} else {
			for _, is := range issues {
				fmt.Fprintf(stdout, "  transforms: WARN %s\n", is)
			}
			// Tool-absence is a WARN, not a doctor failure: it is honest
			// SKIP-with-reason, recoverable by installing the tool.
		}
	}
	return worst
}

// checkTools reports, per transform, whether its required external tool is on
// PATH. Returns human-readable WARN strings (empty => all good).
func checkTools(c *config.Context, root string) []string {
	var out []string
	for _, name := range sortedTransformNames(c) {
		t := c.Transforms[name]
		if t.IsBuiltin() {
			tool := builtinTool(t.Builtin)
			if tool == "" {
				continue // internal builtin (members-fingerprint), no external tool
			}
			if _, err := lookPath(tool); err != nil {
				out = append(out, fmt.Sprintf("transform %q needs %q (not on PATH) — runs will SKIP-with-reason", name, tool))
			}
		} else {
			// exec transform: check the binary resolves.
			bin := t.Exec
			if strings.ContainsRune(bin, os.PathSeparator) && !filepath.IsAbs(bin) {
				bin = filepath.Join(root, bin)
			}
			if _, err := resolveExec(bin); err != nil {
				out = append(out, fmt.Sprintf("transform %q exec %q not found/executable", name, t.Exec))
			}
		}
	}
	return out
}

// cmdSync propagates each selected context and updates state.json.
func cmdSync(args []string, stdout, stderr *os.File) int {
	root, contexts, code := loadSelected(args, stderr)
	if code != exitOK {
		return code
	}
	return runSyncContexts(root, contexts, stdout, stderr)
}

// runSyncContexts loads state, syncs every context atomically, persists state,
// and writes per-run §11.4.69 evidence. Shared by `sync` and the `watch` daemon
// so a watch-triggered sync carries the same evidence + exit-code contract.
func runSyncContexts(root string, contexts []*config.Context, stdout, stderr *os.File) int {
	statePath := state.DefaultPath(root)
	st, err := state.Load(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "docs_chain: %v\n", err)
		return exitError
	}

	runID := time.Now().UTC().Format("20060102T150405Z")
	evidenceDir := filepath.Join(root, "qa-results", "docs_chain", runID)
	worst := exitOK
	var evidence []string

	for _, c := range contexts {
		prep, perr := runner.Prepare(c, root, st)
		if perr != nil {
			fmt.Fprintf(stderr, "docs_chain: prepare %q: %v\n", c.Name, perr)
			worst = maxExit(worst, exitError)
			continue
		}
		res, rerr := prep.RunSync(st)
		if rerr != nil {
			fmt.Fprintf(stderr, "docs_chain: sync %q: %v\n", c.Name, rerr)
			worst = maxExit(worst, exitError)
			continue
		}
		line := formatSyncResult(c.Name, res)
		fmt.Fprintln(stdout, line)
		evidence = append(evidence, line)
		worst = maxExit(worst, exitForStatus(res))
	}

	// Persist state only if nothing catastrophic happened (committed/in-sync
	// folds were already applied to st in RunSync; a conflict/rollback left
	// the relevant context's baseline untouched).
	if serr := st.Save(statePath); serr != nil {
		fmt.Fprintf(stderr, "docs_chain: WARN could not save state: %v\n", serr)
	}
	if werr := writeEvidence(evidenceDir, "sync", evidence); werr != nil {
		fmt.Fprintf(stderr, "docs_chain: WARN evidence: %v\n", werr)
	} else {
		fmt.Fprintf(stdout, "evidence: %s\n", relTo(root, evidenceDir))
	}
	return worst
}

// cmdReBaseline re-baselines every selected context's sync edges from their
// AUTHORITY side only: the non-authority side is regenerated from the authority
// side (authority->non-authority transform, e.g. db-to-md), the derive-from
// exports are refreshed, and state.json is updated — WITHOUT ever writing the
// authority side. A context with no sync edges is skipped with a note (use sync).
func cmdReBaseline(args []string, stdout, stderr *os.File) int {
	root, contexts, code := loadSelected(args, stderr)
	if code != exitOK {
		return code
	}
	statePath := state.DefaultPath(root)
	st, err := state.Load(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "docs_chain: %v\n", err)
		return exitError
	}

	runID := time.Now().UTC().Format("20060102T150405Z")
	evidenceDir := filepath.Join(root, "qa-results", "docs_chain", runID)
	worst := exitOK
	var evidence []string

	for _, c := range contexts {
		prep, perr := runner.Prepare(c, root, st)
		if perr != nil {
			fmt.Fprintf(stderr, "docs_chain: prepare %q: %v\n", c.Name, perr)
			worst = maxExit(worst, exitError)
			continue
		}
		res, rerr := prep.ReBaseline(st)
		if rerr != nil {
			// No sync edges, or a config inconsistency we will not guess past.
			line := fmt.Sprintf("%-24s SKIP (re-baseline N/A): %v", c.Name, rerr)
			fmt.Fprintln(stdout, line)
			evidence = append(evidence, line)
			continue
		}
		line := formatReBaselineResult(c.Name, res)
		fmt.Fprintln(stdout, line)
		evidence = append(evidence, line)
		switch res.Status {
		case orchestrator.StatusCommitted, orchestrator.StatusInSync:
			// ok
		case orchestrator.StatusConflict:
			worst = maxExit(worst, exitConflict)
		case orchestrator.StatusCycle:
			worst = maxExit(worst, exitConfig)
		default:
			worst = maxExit(worst, exitTransform)
		}
	}

	if serr := st.Save(statePath); serr != nil {
		fmt.Fprintf(stderr, "docs_chain: WARN could not save state: %v\n", serr)
	}
	if werr := writeEvidence(evidenceDir, "rebaseline", evidence); werr != nil {
		fmt.Fprintf(stderr, "docs_chain: WARN evidence: %v\n", werr)
	} else {
		fmt.Fprintf(stdout, "evidence: %s\n", relTo(root, evidenceDir))
	}
	return worst
}

// formatReBaselineResult renders a one-line human report for a re-baseline run,
// always naming the write-guarded (read-only) authority nodes for auditability.
func formatReBaselineResult(name string, res *runner.ReBaselineResult) string {
	switch res.Status {
	case orchestrator.StatusInSync:
		return fmt.Sprintf("%-24s re-baselined: in-sync (read-only authority %v; regenerated %v)", name, res.ProtectedNodes, res.Regenerated)
	case orchestrator.StatusCommitted:
		return fmt.Sprintf("%-24s re-baselined: committed %v (read-only authority %v)", name, res.Committed, res.ProtectedNodes)
	case orchestrator.StatusRolledBack:
		return fmt.Sprintf("%-24s ROLLED-BACK: %v (no writes; authority %v stayed read-only)", name, res.Err, res.ProtectedNodes)
	case orchestrator.StatusConflict:
		return fmt.Sprintf("%-24s CONFLICT: %v (no writes)", name, res.Err)
	default:
		return fmt.Sprintf("%-24s %s: %v", name, res.Status, res.Err)
	}
}

// formatSyncResult renders a one-line human report for a run result.
func formatSyncResult(name string, res *orchestrator.Result) string {
	switch res.Status {
	case orchestrator.StatusInSync:
		return fmt.Sprintf("%-24s in-sync (no changes)", name)
	case orchestrator.StatusCommitted:
		return fmt.Sprintf("%-24s applied: committed %v", name, res.Committed)
	case orchestrator.StatusConflict:
		return fmt.Sprintf("%-24s CONFLICT: %v (no writes)", name, res.Err)
	case orchestrator.StatusCycle:
		return fmt.Sprintf("%-24s CYCLE: %v (no writes)", name, res.Err)
	case orchestrator.StatusRolledBack:
		reason := "transform failed"
		if res.Err != nil && orchestrator.IsToolAbsent(res.Err) {
			reason = "SKIP (tool absent)"
		}
		return fmt.Sprintf("%-24s ROLLED-BACK: %s: %v", name, reason, res.Err)
	default:
		return fmt.Sprintf("%-24s %s", name, res.Status)
	}
}

// exitForStatus maps an orchestrator status to its exit code.
func exitForStatus(res *orchestrator.Result) int {
	switch res.Status {
	case orchestrator.StatusInSync, orchestrator.StatusCommitted:
		return exitOK
	case orchestrator.StatusConflict:
		return exitConflict
	case orchestrator.StatusCycle:
		return exitConfig
	case orchestrator.StatusRolledBack:
		// A tool-absent rollback is an honest SKIP, not a hard transform
		// failure — surface it as transform-fail (3) only when the tool was
		// present and the transform genuinely failed; tool-absent maps to 3
		// too per the documented contract (a transform did not produce
		// output), but the message distinguishes them.
		return exitTransform
	default:
		return exitError
	}
}

// cmdVerify runs the read-only drift check.
func cmdVerify(args []string, stdout, stderr *os.File) int {
	root, contexts, code := loadSelected(args, stderr)
	if code != exitOK {
		return code
	}
	statePath := state.DefaultPath(root)
	// A missing OR unreadable/corrupt state.json is fine for verify: the
	// read-only drift check recomputes every derived node and compares the
	// freshly produced bytes against on-disk content, so it never consults the
	// stored hash baseline. Fall back to an empty (cold) baseline rather than
	// dereferencing the nil *State that state.Load returns on a parse/read
	// error — passing nil to runner.Prepare (st.Hashes) panics and crashes the
	// CI gate on a truncated/corrupt state file (§11.4.6 no silent crash).
	st, serr := state.Load(statePath)
	if serr != nil || st == nil {
		st = state.New()
	}

	worst := exitOK
	anyStale := false
	for _, c := range contexts {
		prep, perr := runner.Prepare(c, root, st)
		if perr != nil {
			fmt.Fprintf(stderr, "docs_chain: prepare %q: %v\n", c.Name, perr)
			worst = maxExit(worst, exitError)
			continue
		}
		vr, verr := prep.Verify()
		if verr != nil {
			var ce *graph.ConflictError
			if errors.As(verr, &ce) {
				fmt.Fprintf(stdout, "%-24s CONFLICT: %v\n", c.Name, verr)
				worst = maxExit(worst, exitConflict)
				continue
			}
			fmt.Fprintf(stderr, "docs_chain: verify %q: %v\n", c.Name, verr)
			worst = maxExit(worst, exitError)
			continue
		}
		switch {
		case vr.ToolAbsent:
			fmt.Fprintf(stdout, "%-24s SKIP (tool absent): %s\n", c.Name, vr.ToolReason)
		case len(vr.Stale) == 0:
			fmt.Fprintf(stdout, "%-24s in-sync\n", c.Name)
		default:
			fmt.Fprintf(stdout, "%-24s STALE: %v\n", c.Name, vr.Stale)
			anyStale = true
		}
	}
	if anyStale {
		worst = maxExit(worst, exitError)
	}
	return worst
}

// cmdGraph prints the topo order + edges of one context (debug).
func cmdGraph(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootFlag := fs.String("root", ".", "project root")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	pos := fs.Args()
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "docs_chain graph: specify exactly one <context>")
		return exitError
	}
	root, _ := filepath.Abs(*rootFlag)
	path := filepath.Join(root, contextsRel, pos[0]+".yaml")
	c, err := config.Load(path)
	if err != nil {
		return reportLoadErr(err, stderr)
	}
	g, err := c.BuildGraph()
	if err != nil {
		fmt.Fprintf(stderr, "docs_chain: %v\n", err)
		return exitConfig
	}
	order, err := g.TopoOrder()
	if err != nil {
		fmt.Fprintf(stderr, "docs_chain: %v\n", err)
		return exitConfig
	}
	fmt.Fprintf(stdout, "context %q — topo order (derive-from):\n", c.Name)
	for i, id := range order {
		n := g.Node(id)
		fmt.Fprintf(stdout, "  %d. %s [%s] %s\n", i+1, id, n.Kind, n.Path)
	}
	fmt.Fprintln(stdout, "edges:")
	for _, e := range g.Edges() {
		if e.Type == graph.EdgeSync {
			fmt.Fprintf(stdout, "  %s <-sync-> %s (authority %s)\n", e.From, e.To, e.Authority)
		} else {
			fmt.Fprintf(stdout, "  %s --derive--> %s\n", e.From, e.To)
		}
	}
	return exitOK
}

func writeEvidence(dir, kind string, lines []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# docs_chain %s evidence\n# run %s UTC\n\n", kind, time.Now().UTC().Format(time.RFC3339))
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return os.WriteFile(filepath.Join(dir, kind+".log"), []byte(b.String()), 0o644)
}

func sortedTransformNames(c *config.Context) []string {
	names := make([]string, 0, len(c.Transforms))
	for n := range c.Transforms {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// builtinTool returns the external tool a builtin needs, or "" for internal
// builtins (members-fingerprint).
func builtinTool(builtin string) string {
	switch builtin {
	case config.BuiltinPandocHTML, config.BuiltinPandocDOCX:
		return "pandoc"
	case config.BuiltinWeasyprintPDF:
		return "weasyprint"
	default:
		return ""
	}
}

func relTo(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}

func maxExit(a, b int) int {
	if b > a {
		return b
	}
	return a
}
