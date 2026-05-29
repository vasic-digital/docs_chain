package hash

import "testing"

func TestByteContentHasher_Deterministic(t *testing.T) {
	h := NewByteContentHasher()
	content := []byte("alpha\nbeta\ngamma\n")
	want := h.Hash(content)
	// §11.4.50 determinism: same content -> same hash, run 3x.
	for i := 0; i < 3; i++ {
		if got := h.Hash(content); got != want {
			t.Fatalf("iteration %d: hash drift: got %q want %q", i, got, want)
		}
	}
}

func TestByteContentHasher_WhitespaceNormalization(t *testing.T) {
	h := NewByteContentHasher()
	// These three differ only in line endings / trailing whitespace and MUST
	// collide (§11.4.86 normalized-content hashing — NOT byte-literal).
	lf := []byte("a\nb\n")
	crlf := []byte("a\r\nb\r\n")
	trail := []byte("a   \nb\t\n")
	noFinalNL := []byte("a\nb")
	base := h.Hash(lf)
	for name, in := range map[string][]byte{
		"crlf":      crlf,
		"trailing":  trail,
		"noFinalNL": noFinalNL,
	} {
		if got := h.Hash(in); got != base {
			t.Errorf("%s should collide with LF baseline: got %q base %q", name, got, base)
		}
	}
}

func TestByteContentHasher_DistinctContentDiffers(t *testing.T) {
	h := NewByteContentHasher()
	if h.Hash([]byte("a\nb\n")) == h.Hash([]byte("a\nc\n")) {
		t.Fatal("distinct content must produce distinct hashes")
	}
}

func TestByteContentHasher_EmptyNoSpuriousNewline(t *testing.T) {
	h := NewByteContentHasher()
	if got := string(h.Normalize([]byte(""))); got != "" {
		t.Fatalf("empty content normalized to %q, want empty", got)
	}
	if got := string(h.Normalize([]byte("\n\n"))); got != "" {
		t.Fatalf("whitespace-only content normalized to %q, want empty", got)
	}
}

func TestFingerprintMembers_OrderIndependent(t *testing.T) {
	a := FingerprintMembers([]string{"vlc.apk", "mpv.apk", "kodi.apk"})
	b := FingerprintMembers([]string{"kodi.apk", "vlc.apk", "mpv.apk"})
	if a != b {
		t.Fatalf("fingerprint must be order-independent: %q != %q", a, b)
	}
}

func TestFingerprintMembers_MembershipSensitive(t *testing.T) {
	base := FingerprintMembers([]string{"vlc.apk", "mpv.apk"})
	added := FingerprintMembers([]string{"vlc.apk", "mpv.apk", "kodi.apk"})
	removed := FingerprintMembers([]string{"vlc.apk"})
	renamed := FingerprintMembers([]string{"vlc.apk", "mpv2.apk"})
	for name, fp := range map[string]string{
		"added":   added,
		"removed": removed,
		"renamed": renamed,
	} {
		if fp == base {
			t.Errorf("%s member change must alter fingerprint", name)
		}
	}
}

func TestFingerprintMembers_Deterministic(t *testing.T) {
	in := []string{"b", "a", "c"}
	want := FingerprintMembers(in)
	for i := 0; i < 3; i++ {
		if got := FingerprintMembers(in); got != want {
			t.Fatalf("iteration %d fingerprint drift", i)
		}
	}
	// Input slice must not be mutated by sorting.
	if in[0] != "b" || in[1] != "a" || in[2] != "c" {
		t.Fatalf("FingerprintMembers mutated its input slice: %v", in)
	}
}
