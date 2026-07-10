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

// TestRawByteHasher_NoNormalization pins the binary-kind hasher: it hashes raw
// bytes verbatim (identity Normalize) and therefore DISTINGUISHES inputs that
// the text ByteContentHasher would collide (CRLF vs LF, trailing whitespace,
// trailing newline). This is the contract the docx/pdf adapters rely on.
func TestRawByteHasher_NoNormalization(t *testing.T) {
	raw := NewRawByteHasher()
	txt := NewByteContentHasher()

	// Normalize is the identity for raw bytes.
	in := []byte{'P', 'K', 0x00, '\r', '\n', 'x', ' ', '\t', '\n'}
	if string(raw.Normalize(in)) != string(in) {
		t.Fatalf("RawByteHasher.Normalize mutated bytes: got %x want %x", raw.Normalize(in), in)
	}

	// Inputs the text hasher collides MUST stay distinct under the raw hasher.
	cases := [][2][]byte{
		{[]byte("a\r\nb"), []byte("a\nb")}, // CRLF vs LF
		{[]byte("a \nb"), []byte("a\nb")},  // trailing whitespace
		{[]byte("data\n"), []byte("data")}, // trailing newline
	}
	for i, c := range cases {
		if txt.Hash(c[0]) != txt.Hash(c[1]) {
			t.Fatalf("case %d: precondition failed — text hasher should collide these", i)
		}
		if raw.Hash(c[0]) == raw.Hash(c[1]) {
			t.Fatalf("case %d: raw hasher collided %q and %q — must be byte-distinct", i, c[0], c[1])
		}
	}

	// Determinism: same bytes -> same hash, 3x (§11.4.50).
	want := raw.Hash(in)
	for i := 0; i < 3; i++ {
		if got := raw.Hash(in); got != want {
			t.Fatalf("iter %d: raw hash drift", i)
		}
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

// TestFingerprintMembers_SeparatorInjection pins the §11.4.86 drift-proof
// contract at its core: two DISTINCT member SETS must NEVER share a fingerprint.
// The pre-fix implementation joins members with "\n" and hashes the join, so a
// member that itself contains the "\n" record separator collides distinct sets:
//
//	{"a", "b\nc"}  -> join "a\nb\nc"
//	{"a\nb", "c"}  -> join "a\nb\nc"   (SAME hash, DIFFERENT roster)
//
// A roster that transitions between these states (e.g. a file literally named
// "b\nc" replacing files "b" and "c") is MISSED by the drift-proof gate, so a
// stale Status/roster export passes verify — a §11.4 PASS-bluff at the
// fingerprint layer. Newline is a legal path byte on the POSIX targets, so the
// member encoding MUST be injective (length-prefix framing), not a bare join.
func TestFingerprintMembers_SeparatorInjection(t *testing.T) {
	a := FingerprintMembers([]string{"a", "b\nc"})
	b := FingerprintMembers([]string{"a\nb", "c"})
	if a == b {
		t.Fatalf("§11.4.86 collision: distinct member sets {\"a\", \"b\\nc\"} and "+
			"{\"a\\nb\", \"c\"} share fingerprint %q — a roster change is MISSED "+
			"(separator injection)", a)
	}
	// A single member carrying the separator must also stay distinct from the
	// two-member set whose join reconstructs it.
	one := FingerprintMembers([]string{"a\nb\nc"})
	three := FingerprintMembers([]string{"a", "b", "c"})
	if one == three {
		t.Fatalf("§11.4.86 collision: single member {\"a\\nb\\nc\"} shares fingerprint "+
			"%q with three-member set {\"a\",\"b\",\"c\"} — cardinality change MISSED", one)
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
