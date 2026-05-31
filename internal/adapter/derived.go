package adapter

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"digital.vasic.docs_chain/internal/graph"
	"digital.vasic.docs_chain/internal/hash"
)

// lookTool resolves an external tool on PATH, returning a typed
// *ToolAbsentError (NOT a generic error) when absent so callers SKIP-with-
// reason instead of faking success (§11.4.6 / §11.4.27).
func lookTool(tool string) (string, error) {
	p, err := exec.LookPath(tool)
	if err != nil {
		return "", &ToolAbsentError{Tool: tool}
	}
	return p, nil
}

// runProducer executes argv (with the produced file at outPath), then reads
// the produced file's bytes back. argvFn receives the resolved tool path and
// the output path and returns the full argv. tmpInputs lets the caller stage
// input bytes to a temp file the command can consume.
func runProducer(toolPath string, argv []string, outPath string) ([]byte, error) {
	// AUDIT (CWE-94): toolPath is always the result of exec.LookPath("pandoc")
	// / exec.LookPath("weasyprint") — never caller-supplied. argv is built
	// entirely from internally-controlled flags + a temp-file path Docs Chain
	// itself created. There is no shell (exec.Command, not sh -c) and no
	// interpolation of untrusted data, so no command-injection surface.
	cmd := exec.Command(toolPath, argv...) //nolint:gosec // see AUDIT above

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("adapter: %s failed: %w; stderr: %s",
			filepath.Base(toolPath), err, stderr.String())
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("adapter: %s produced no readable output at %q: %w",
			filepath.Base(toolPath), outPath, err)
	}
	return out, nil
}

// DerivedAdapter backs html/pdf/docx nodes. Read/Write delegate to an
// embedded FileAdapter (the produced artefact on disk). The actual md→html /
// md→docx / html→pdf transformation is performed by Transform, which the
// orchestrator (or a transforms map) invokes; the adapter itself does not
// auto-run on Read. Transform shells out to pandoc/weasyprint and returns a
// *ToolAbsentError when the tool is missing.
type DerivedAdapter struct {
	*FileAdapter
}

// NewHTMLAdapter returns a DerivedAdapter for an .html output file.
func NewHTMLAdapter(path string) *DerivedAdapter {
	return &DerivedAdapter{FileAdapter: NewFileAdapter(path, graph.KindHTML, hash.NewByteContentHasher())}
}

// NewDOCXAdapter returns a DerivedAdapter for a .docx output file. DOCX is a
// binary container (zip), so its hasher is the raw ByteContentHasher over the
// produced bytes — Normalize is a no-op for binary payloads in practice
// (there are no CRLF/trailing-whitespace lines to canonicalize), and the
// hash is the document bytes.
func NewDOCXAdapter(path string) *DerivedAdapter {
	return &DerivedAdapter{FileAdapter: NewFileAdapter(path, graph.KindDOCX, hash.NewByteContentHasher())}
}

// NewPDFAdapter returns a DerivedAdapter for a .pdf output file.
func NewPDFAdapter(path string) *DerivedAdapter {
	return &DerivedAdapter{FileAdapter: NewFileAdapter(path, graph.KindPDF, hash.NewByteContentHasher())}
}

// PandocMarkdownToHTML returns a graph.Transform that converts the single
// markdown source's bytes to standalone HTML via pandoc, writing to outPath
// and returning the produced HTML bytes. If pandoc is absent it returns a
// *ToolAbsentError WITHOUT producing any file.
func PandocMarkdownToHTML(outPath string) func(ins map[string][]byte) ([]byte, error) {
	return func(ins map[string][]byte) ([]byte, error) {
		src, err := singleInput(ins)
		if err != nil {
			return nil, err
		}
		tool, err := lookTool("pandoc")
		if err != nil {
			return nil, err
		}
		in, cleanup, err := stageTemp(outPath, "*.md", src)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return nil, err
		}
		// `--standalone` derives <title> from the input *filename* by default,
		// which is a randomized temp name — that would leak into the output and
		// break the byte-stability contract (CONFIG_SCHEMA §5.2 / §11.4.50).
		// Pin the title to the stable output basename so identical markdown
		// always yields identical HTML regardless of the staging temp name.
		title := strings.TrimSuffix(filepath.Base(outPath), filepath.Ext(outPath))
		argv := []string{"--standalone", "--from=markdown", "--to=html",
			"--metadata", "title=" + title, "-o", outPath, in}
		return runProducer(tool, argv, outPath)
	}
}

// PandocMarkdownToDOCX returns a graph.Transform that converts a markdown
// source to a .docx via pandoc. *ToolAbsentError when pandoc is absent.
func PandocMarkdownToDOCX(outPath string) func(ins map[string][]byte) ([]byte, error) {
	return func(ins map[string][]byte) ([]byte, error) {
		src, err := singleInput(ins)
		if err != nil {
			return nil, err
		}
		tool, err := lookTool("pandoc")
		if err != nil {
			return nil, err
		}
		in, cleanup, err := stageTemp(outPath, "*.md", src)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return nil, err
		}
		title := strings.TrimSuffix(filepath.Base(outPath), filepath.Ext(outPath))
		argv := []string{"--from=markdown", "--to=docx",
			"--metadata", "title=" + title, "-o", outPath, in}
		return runProducer(tool, argv, outPath)
	}
}

// WeasyprintHTMLToPDF returns a graph.Transform that converts an HTML source
// to a .pdf via weasyprint. *ToolAbsentError when weasyprint is absent.
func WeasyprintHTMLToPDF(outPath string) func(ins map[string][]byte) ([]byte, error) {
	return func(ins map[string][]byte) ([]byte, error) {
		src, err := singleInput(ins)
		if err != nil {
			return nil, err
		}
		tool, err := lookTool("weasyprint")
		if err != nil {
			return nil, err
		}
		in, cleanup, err := stageTemp(outPath, "*.html", src)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return nil, err
		}
		argv := []string{in, outPath}
		return runProducer(tool, argv, outPath)
	}
}

// singleInput extracts the one source from a single-source transform input
// map, erroring if the count is not exactly one (the derived adapters are
// 1:1 source→output).
func singleInput(ins map[string][]byte) ([]byte, error) {
	if len(ins) != 1 {
		return nil, fmt.Errorf("adapter: derived transform expects exactly 1 source, got %d", len(ins))
	}
	for _, v := range ins {
		return v, nil
	}
	return nil, fmt.Errorf("adapter: derived transform got empty input map")
}

// stageTemp writes content to a temp file (with the given suffix pattern) in
// the same directory as nearPath, returning the temp path and a cleanup
// func. Co-locating the temp avoids cross-device rename issues for any
// downstream consumer and keeps the staged input next to its output.
func stageTemp(nearPath, pattern string, content []byte) (path string, cleanup func(), err error) {
	dir := filepath.Dir(nearPath)
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return "", func() {}, mkErr
	}
	f, ferr := os.CreateTemp(dir, "docs_chain_in_"+pattern)
	if ferr != nil {
		return "", func() {}, ferr
	}
	name := f.Name()
	if _, werr := f.Write(content); werr != nil {
		f.Close()
		os.Remove(name)
		return "", func() {}, werr
	}
	if cerr := f.Close(); cerr != nil {
		os.Remove(name)
		return "", func() {}, cerr
	}
	return name, func() { os.Remove(name) }, nil
}
