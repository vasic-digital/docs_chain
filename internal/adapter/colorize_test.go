package adapter

import (
	"bytes"
	"strings"
	"testing"
)

// TestColorizeHTML_Matrix proves the §11.4.23 color matrix is applied per-cell,
// including the status-affects-type interaction (fixed → both cells green).
func TestColorizeHTML_Matrix(t *testing.T) {
	in := `<html><body><table>
<thead><tr><th>ID</th><th>Type</th><th>Status</th></tr></thead>
<tbody>
<tr><td>1</td><td>bug</td><td>open</td></tr>
<tr><td>2</td><td>task</td><td>fixed</td></tr>
<tr><td>3</td><td>feature</td><td>blocker</td></tr>
<tr><td>4</td><td>bug</td><td>reopened</td></tr>
<tr><td>5</td><td>task</td><td>in progress</td></tr>
</tbody></table></body></html>`

	out, err := ColorizeHTML()(map[string][]byte{"src": []byte(in)})
	if err != nil {
		t.Fatalf("colorize: %v", err)
	}
	got := string(out)

	// Each assertion is a fully-colored cell string the deterministic renderer
	// emits (style attr precedes content).
	wants := []string{
		`<td style="background-color: #ffdce0">bug</td>`,         // row1 type: pale red (status open → none)
		`<td style="background-color: #d6f5d9">task</td>`,        // row2 type: pale GREEN (status fixed overrides)
		`<td style="background-color: #d6f5d9">fixed</td>`,       // row2 status: pale green
		`<td style="background-color: #fff5cc">feature</td>`,     // row3 type: pale yellow
		`<td style="background-color: #ff6b6b">blocker</td>`,     // row3 status: red (readable)
		`<td style="background-color: #ffdce0">reopened</td>`,    // row4 status: pale red
		`<td style="background-color: #d6f5d9">in progress</td>`, // row5 status: pale green
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Fatalf("missing colored cell %q in:\n%s", w, got)
		}
	}
	// row1 status "open" gets NO color: there must be no styled "open" cell.
	if strings.Contains(got, `background-color: #`) && strings.Contains(got, `">open</td>`) {
		// the open cell should be a bare <td>open</td>
		if !strings.Contains(got, `<td>open</td>`) {
			t.Fatalf("status=open should be uncolored:\n%s", got)
		}
	}

	// DETERMINISM / verify-stability: colorize is idempotent on its own output.
	out2, err := ColorizeHTML()(map[string][]byte{"src": out})
	if err != nil {
		t.Fatalf("colorize round-2: %v", err)
	}
	if !bytes.Equal(out, out2) {
		t.Fatalf("colorize not idempotent (verify would flap):\n--- 1 ---\n%s\n--- 2 ---\n%s", out, out2)
	}
}

// TestColorizeHTML_NonTrackerPassthrough proves a table without Type/Status
// headers is left uncolored (no false coloring) and HTML round-trips stably.
func TestColorizeHTML_NonTrackerPassthrough(t *testing.T) {
	in := `<html><body><table><thead><tr><th>Name</th><th>City</th></tr></thead>` +
		`<tbody><tr><td>bug</td><td>task</td></tr></tbody></table></body></html>`
	out, err := ColorizeHTML()(map[string][]byte{"src": []byte(in)})
	if err != nil {
		t.Fatalf("colorize: %v", err)
	}
	if strings.Contains(string(out), "background-color") {
		t.Fatalf("non-tracker table was colored (false positive):\n%s", out)
	}
	// idempotent
	out2, _ := ColorizeHTML()(map[string][]byte{"src": out})
	if !bytes.Equal(out, out2) {
		t.Fatal("non-tracker colorize not idempotent")
	}
}
