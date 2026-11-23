package blob

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFS(t *testing.T) {
	ctx := t.Context()
	s, err := NewFS(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ref, n, err := s.Put(ctx, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if ref != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" || n != 5 {
		t.Fatalf("Put = %s, %d", ref, n)
	}
	if again, _, err := s.Put(ctx, strings.NewReader("hello")); err != nil || again != ref {
		t.Fatalf("second Put = %s, %v", again, err)
	}
	rc, err := s.Open(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(b) != "hello" {
		t.Fatalf("content = %q", b)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if _, err := s.Open(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open after delete: %v", err)
	}
	entries, _ := os.ReadDir(s.root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upload-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestFSRejectsBadRefs(t *testing.T) {
	s, _ := NewFS(t.TempDir())
	for _, ref := range []Ref{"", "../../etc/passwd", Ref(strings.Repeat("A", 64))} {
		if _, err := s.Open(t.Context(), ref); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("Open(%q): %v", ref, err)
		}
	}
}
