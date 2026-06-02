package adapter

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ColorizeHTML returns the `colorize-html` builtin transform: a deterministic
// `html → html` post-processor implementing the §11.4.23 visual-cue mandate for
// tracker docs (Issues / Fixed / Status). For every table it finds a "Type"
// and/or "Status" header column and applies background colors per the §11.4.23
// matrix:
//
//	type   bug      → pale red    (#ffdce0)
//	type   task     → pale blue   (#dbe9ff)
//	type   feature  → pale yellow (#fff5cc)
//	status queued       → (no color)
//	status fixed        → BOTH the status AND the type cell pale green (#d6f5d9)
//	status in progress  → status cell pale green (#d6f5d9)
//	status reopened     → status cell pale red  (#ffdce0)
//	status blocker      → status cell red, readable (#ff6b6b)
//
// Tables without a Type/Status header are left untouched. The transform is
// deterministic (same input HTML → byte-identical output) so a post-sync
// `verify` exit 0s. NON-tracker HTML is a no-op pass-through (re-serialized).
func ColorizeHTML() func(ins map[string][]byte) ([]byte, error) {
	return func(ins map[string][]byte) ([]byte, error) {
		in := concatSortedInputs(ins)
		doc, err := html.Parse(bytes.NewReader(in))
		if err != nil {
			return nil, err
		}
		forEachTable(doc, colorizeTable)
		var out bytes.Buffer
		if err := html.Render(&out, doc); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
}

const (
	colPaleRed    = "#ffdce0"
	colPaleBlue   = "#dbe9ff"
	colPaleYellow = "#fff5cc"
	colPaleGreen  = "#d6f5d9"
	colRed        = "#ff6b6b"
)

func typeColor(v string) string {
	switch v {
	case "bug":
		return colPaleRed
	case "task":
		return colPaleBlue
	case "feature":
		return colPaleYellow
	default:
		return ""
	}
}

func statusColor(v string) string {
	switch v {
	case "fixed", "in progress", "in-progress", "in_progress", "completed", "done":
		return colPaleGreen
	case "reopened":
		return colPaleRed
	case "blocker", "blocked":
		return colRed
	case "queued", "open", "pending", "":
		return ""
	default:
		return ""
	}
}

// colorizeTable applies the §11.4.23 color matrix to one <table>.
func colorizeTable(table *html.Node) {
	headers := headerTexts(table)
	typeIdx, statusIdx := -1, -1
	for i, h := range headers {
		switch normalizeCell(h) {
		case "type":
			typeIdx = i
		case "status":
			statusIdx = i
		}
	}
	if typeIdx < 0 && statusIdx < 0 {
		return // not a tracker table
	}
	for _, tr := range bodyRows(table) {
		cells := cellNodes(tr)
		var typeVal, statusVal string
		if typeIdx >= 0 && typeIdx < len(cells) {
			typeVal = normalizeCell(textOf(cells[typeIdx]))
		}
		if statusIdx >= 0 && statusIdx < len(cells) {
			statusVal = normalizeCell(textOf(cells[statusIdx]))
		}
		// Type cell: base color, overridden to pale green when status==fixed.
		if typeIdx >= 0 && typeIdx < len(cells) {
			tc := typeColor(typeVal)
			if statusVal == "fixed" {
				tc = colPaleGreen
			}
			if tc != "" {
				setBackground(cells[typeIdx], tc)
			}
		}
		// Status cell: per the status matrix.
		if statusIdx >= 0 && statusIdx < len(cells) {
			if sc := statusColor(statusVal); sc != "" {
				setBackground(cells[statusIdx], sc)
			}
		}
	}
}

// --- HTML tree helpers (deterministic, dependency-light) ---

func forEachTable(n *html.Node, fn func(*html.Node)) {
	if n.Type == html.ElementNode && n.DataAtom == atom.Table {
		fn(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		forEachTable(c, fn)
	}
}

// headerTexts returns the text of the first header row's cells (<th>, or the
// first row's <td> if there are no <th>).
func headerTexts(table *html.Node) []string {
	var ths, firstTDs []string
	var firstRowDone bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if firstRowDone {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Tr {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && c.DataAtom == atom.Th {
					ths = append(ths, textOf(c))
				} else if c.Type == html.ElementNode && c.DataAtom == atom.Td {
					firstTDs = append(firstTDs, textOf(c))
				}
			}
			if len(ths) > 0 || len(firstTDs) > 0 {
				firstRowDone = true
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(table)
	if len(ths) > 0 {
		return ths
	}
	return firstTDs
}

// bodyRows returns the data rows (<tr> containing <td>, i.e. not the header).
func bodyRows(table *html.Node) []*html.Node {
	var rows []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Tr {
			if len(cellNodes(n)) > 0 {
				rows = append(rows, n)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(table)
	return rows
}

// cellNodes returns the direct <td> children of a row.
func cellNodes(tr *html.Node) []*html.Node {
	var tds []*html.Node
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Td {
			tds = append(tds, c)
		}
	}
	return tds
}

// textOf returns the concatenated text content of a node.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// normalizeCell lowercases + trims + collapses whitespace for matching.
func normalizeCell(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// setBackground sets/merges a background-color into the node's style attribute,
// deterministically (replaces any prior docs_chain background-color).
func setBackground(n *html.Node, color string) {
	want := "background-color: " + color
	for i := range n.Attr {
		if n.Attr[i].Key == "style" {
			parts := splitStyle(n.Attr[i].Val)
			var kept []string
			for _, p := range parts {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), "background-color") {
					continue
				}
				if strings.TrimSpace(p) != "" {
					kept = append(kept, strings.TrimSpace(p))
				}
			}
			kept = append(kept, want)
			n.Attr[i].Val = strings.Join(kept, "; ")
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: "style", Val: want})
}

func splitStyle(s string) []string { return strings.Split(s, ";") }
