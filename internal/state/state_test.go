package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".docs_chain", "state.json")

	// Missing file -> empty state, no error.
	s, err := Load(p)
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if len(s.Contexts) != 0 {
		t.Fatalf("fresh state not empty: %v", s.Contexts)
	}

	s.SetHashes("guide", map[string]string{"md": "h1", "html": "h2"})
	if err := s.Save(p); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reload and confirm round-trip.
	s2, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := s2.Hashes("guide")
	if got["md"] != "h1" || got["html"] != "h2" {
		t.Fatalf("round-trip mismatch: %v", got)
	}
	// Unknown context -> empty (never nil).
	if m := s2.Hashes("nope"); m == nil || len(m) != 0 {
		t.Fatalf("unknown context should be empty non-nil, got %v", m)
	}
}

func TestStateAtomicSave(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	s := New()
	s.SetHashes("c", map[string]string{"n": "x"})
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	// No leftover temp files in the dir.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("unexpected leftover file %q after atomic save", e.Name())
		}
	}
}
