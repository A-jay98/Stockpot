package secretbox

import (
	"bytes"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	box, err := New([]byte("0123456789abcdef0123456789abcdef"), "settings")
	if err != nil {
		t.Fatal(err)
	}
	a := box.Seal([]byte("sk-live"), []byte("llm.key"))
	b := box.Seal([]byte("sk-live"), []byte("llm.key"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same value must differ (random nonce)")
	}
	if bytes.Contains(a, []byte("sk-live")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	got, err := box.Open(a, []byte("llm.key"))
	if err != nil || string(got) != "sk-live" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestOpenRejects(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef")
	box, _ := New(master, "settings")
	sealed := box.Seal([]byte("secret"), []byte("k"))

	other, _ := New([]byte("another key, also thirty-two bytes"), "settings")
	otherPurpose, _ := New(master, "sessions")
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1

	cases := map[string]func() ([]byte, error){
		"wrong ad":      func() ([]byte, error) { return box.Open(sealed, []byte("other")) },
		"wrong key":     func() ([]byte, error) { return other.Open(sealed, []byte("k")) },
		"wrong purpose": func() ([]byte, error) { return otherPurpose.Open(sealed, []byte("k")) },
		"tampered":      func() ([]byte, error) { return box.Open(tampered, []byte("k")) },
		"truncated":     func() ([]byte, error) { return box.Open(sealed[:5], []byte("k")) },
	}
	for name, open := range cases {
		if _, err := open(); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: err = %v, want ErrDecrypt", name, err)
		}
	}
}
