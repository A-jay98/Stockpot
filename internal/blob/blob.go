// Package blob stores opaque files (receipt photos, product images) by
// content hash. Store is the interface the app uses; FS keeps them on local
// disk under $STATE_DIRECTORY/blobs.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// ErrNotFound means no blob has that hash.
var ErrNotFound = errors.New("blob not found")

// ErrInvalidRef means the reference is not a SHA-256 hex digest.
var ErrInvalidRef = errors.New("invalid blob reference")

// Ref names a blob: the lower-case hex SHA-256 of its content.
type Ref string

var refPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Valid reports whether r is a well-formed reference.
func (r Ref) Valid() bool { return refPattern.MatchString(string(r)) }

// Store keeps blobs by content hash. Putting the same content twice is a
// no-op that returns the same Ref.
type Store interface {
	Put(ctx context.Context, r io.Reader) (ref Ref, size int64, err error)
	Open(ctx context.Context, ref Ref) (io.ReadCloser, error)
	Delete(ctx context.Context, ref Ref) error
}

// FS is a Store on the local filesystem. Files live at
// <root>/<ab>/<cd>/<hash>, written to a temp file and renamed into place,
// so readers never see a partial blob.
type FS struct {
	root string
}

var _ Store = (*FS)(nil)

// NewFS returns a store rooted at dir, creating it (mode 0700) if needed.
func NewFS(dir string) (*FS, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("blob store: %w", err)
	}
	return &FS{root: dir}, nil
}

// Put copies r into the store.
func (s *FS) Put(ctx context.Context, r io.Reader) (Ref, int64, error) {
	tmp, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("blob put: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), ctxReader{ctx, r})
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, fmt.Errorf("blob put: %w", err)
	}
	ref := Ref(hex.EncodeToString(h.Sum(nil)))
	path := s.path(ref)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", 0, fmt.Errorf("blob put: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", 0, fmt.Errorf("blob put: %w", err)
	}
	return ref, n, nil
}

// Open returns the blob's content; the caller closes it.
func (s *FS) Open(_ context.Context, ref Ref) (io.ReadCloser, error) {
	if !ref.Valid() {
		return nil, ErrInvalidRef
	}
	f, err := os.Open(s.path(ref))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blob open: %w", err)
	}
	return f, nil
}

// Delete removes the blob. Deleting a missing blob is not an error.
func (s *FS) Delete(_ context.Context, ref Ref) error {
	if !ref.Valid() {
		return ErrInvalidRef
	}
	if err := os.Remove(s.path(ref)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob delete: %w", err)
	}
	return nil
}

func (s *FS) path(ref Ref) string {
	r := string(ref)
	return filepath.Join(s.root, r[0:2], r[2:4], r)
}

// ctxReader stops a long copy when ctx is cancelled.
type ctxReader struct {
	ctx context.Context //nolint:containedctx // scoped to one Put call
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
