package secretbox

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func key(t *testing.T) *Box {
	t.Helper()
	box, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSealOpen(t *testing.T) {
	box := key(t)
	sealed := box.Seal([]byte("sk-proj-123"), []byte("profile-a"))
	if bytes.Contains(sealed, []byte("sk-proj-123")) {
		t.Fatal("plaintext visible in the sealed value")
	}
	got, err := box.Open(sealed, []byte("profile-a"))
	if err != nil || string(got) != "sk-proj-123" {
		t.Fatalf("open = %q, %v", got, err)
	}
}

func TestSealIsRandomised(t *testing.T) {
	box := key(t)
	if bytes.Equal(box.Seal([]byte("x"), nil), box.Seal([]byte("x"), nil)) {
		t.Fatal("two seals of the same value are identical")
	}
}

func TestOpenRejectsAnotherProfileOrTampering(t *testing.T) {
	box := key(t)
	sealed := box.Seal([]byte("sk-proj-123"), []byte("profile-a"))
	if _, err := box.Open(sealed, []byte("profile-b")); err == nil {
		t.Fatal("opened with the wrong associated data")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := box.Open(sealed, []byte("profile-a")); err == nil {
		t.Fatal("opened a tampered value")
	}
	if _, err := box.Open([]byte{1, 2}, nil); err == nil {
		t.Fatal("opened a truncated value")
	}
}

func TestParseKey(t *testing.T) {
	raw := bytes.Repeat([]byte{9}, 32)
	for _, encoded := range []string{hex.EncodeToString(raw), base64.StdEncoding.EncodeToString(raw), " " + hex.EncodeToString(raw) + "\n"} {
		got, err := ParseKey(encoded)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("ParseKey(%q) = %x, %v", encoded, got, err)
		}
	}
	for _, bad := range []string{"", "short", hex.EncodeToString(raw[:16]), strings.Repeat("z", 64)} {
		if _, err := ParseKey(bad); err == nil {
			t.Fatalf("ParseKey(%q) accepted", bad)
		}
	}
}
