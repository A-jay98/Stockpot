// Package media accepts photo uploads into the blob store, decides who may
// read them, and serves resized copies. The bytes are content-addressed
// (blob.Ref = SHA-256), so the client can compute a ref offline and use it in
// commands before the upload happens; blob_owner records which households
// uploaded a ref and may therefore read it.
package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // decoder registration
	"io"
	"mime"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decoder registration

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/identity"
)

// Limits on what an upload may be. The client downscales to 2048 px before
// uploading; these bound what the server is willing to decode.
const (
	MaxUploadBytes = 8 << 20
	MaxSide        = 4096
)

// ContentTypes are the accepted upload types.
var ContentTypes = []string{"image/jpeg", "image/png", "image/webp"}

// ThumbWidths are the widths GET ?w= may ask for. A fixed set keeps the
// number of stored variants per blob bounded.
var ThumbWidths = []int{256, 512, 1024}

// thumbQuality is the JPEG quality of resized copies.
const thumbQuality = 80

// Errors the API maps to problems.
var (
	ErrUnsupportedType = errors.New("unsupported image type")
	ErrTooLarge        = errors.New("image too large")
	ErrBadImage        = errors.New("not a valid image")
	ErrBadWidth        = errors.New("unsupported thumbnail width")
	// ErrNotFound means the ref does not exist or the household may not read it.
	ErrNotFound = errors.New("blob not found")
)

// formats maps image.Decode format names to content types.
var formats = map[string]string{"jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp"}

// Service uploads and serves household photos.
type Service struct {
	pool  *pgxpool.Pool
	store blob.Store
	// decode bounds concurrent full decodes: a 4096² image is 64 MB of pixels.
	decode chan struct{}
}

// New returns a service storing bytes in store.
func New(pool *pgxpool.Pool, store blob.Store) *Service {
	return &Service{pool: pool, store: store, decode: make(chan struct{}, 2)}
}

// Info describes an uploaded blob.
type Info struct {
	Ref         blob.Ref
	Size        int64
	ContentType string
	Width       int
	Height      int
}

// Upload stores the image in r for the caller's household. contentType is
// the declared type; it must match what the bytes decode as. Uploading bytes
// that already exist only records the household as an owner.
func (s *Service) Upload(ctx context.Context, contentType string, r io.Reader) (Info, error) {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return Info{}, err
	}
	// Parameters (image/jpeg; charset=binary) don't change the type.
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = mt
	}
	if !slices.Contains(ContentTypes, contentType) {
		return Info{}, fmt.Errorf("%w: %s", ErrUnsupportedType, contentType)
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return Info{}, fmt.Errorf("read upload: %w", err)
	}
	if len(data) > MaxUploadBytes {
		return Info{}, fmt.Errorf("%w: over %d bytes", ErrTooLarge, MaxUploadBytes)
	}
	info, err := s.verify(ctx, data)
	if err != nil {
		return Info{}, err
	}
	if info.ContentType != contentType {
		return Info{}, fmt.Errorf("%w: declared %s, got %s", ErrUnsupportedType, contentType, info.ContentType)
	}
	info.Ref, info.Size, err = s.store.Put(ctx, bytes.NewReader(data))
	if err != nil {
		return Info{}, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var uploader *uuid.UUID
		if p := identity.PrincipalFrom(ctx); p != nil && p.UserID != uuid.Nil {
			uploader = &p.UserID
		}
		tag, err := tx.Exec(ctx, `INSERT INTO blob_owner (ref, household_id, uploaded_by, size, content_type, width, height)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (ref, household_id) DO NOTHING`,
			string(info.Ref), hh, uploader, info.Size, info.ContentType, info.Width, info.Height)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return audit.Record(ctx, tx, "blob.upload", audit.Target{Type: "blob", ID: string(info.Ref)}, nil,
			map[string]any{"size": info.Size, "contentType": info.ContentType, "width": info.Width, "height": info.Height})
	})
	if err != nil {
		return Info{}, fmt.Errorf("record blob owner: %w", err)
	}
	return info, nil
}

// verify checks the header (type, dimensions) and then decodes the whole
// image, so truncated or corrupt files are refused at upload.
func (s *Service) verify(ctx context.Context, data []byte) (Info, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Info{}, fmt.Errorf("%w: %w", ErrBadImage, err)
	}
	ct, ok := formats[format]
	if !ok {
		return Info{}, fmt.Errorf("%w: %s", ErrUnsupportedType, format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Info{}, ErrBadImage
	}
	if cfg.Width > MaxSide || cfg.Height > MaxSide {
		return Info{}, fmt.Errorf("%w: %d×%d is over %d px per side", ErrTooLarge, cfg.Width, cfg.Height, MaxSide)
	}
	if _, err := s.decodeImage(ctx, bytes.NewReader(data)); err != nil {
		return Info{}, err
	}
	return Info{ContentType: ct, Width: cfg.Width, Height: cfg.Height}, nil
}

func (s *Service) decodeImage(ctx context.Context, r io.Reader) (image.Image, error) {
	select {
	case s.decode <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.decode }()
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadImage, err)
	}
	return img, nil
}

// Blob is an open blob for serving; the caller closes Body.
type Blob struct {
	Body        io.ReadCloser
	Ref         blob.Ref // of the bytes served (the variant for a thumbnail)
	Size        int64
	ContentType string
}

// Open returns the blob if the caller's household owns ref. width 0 serves
// the original; otherwise one of ThumbWidths, built on first request. A width
// at or above the original's serves the original.
func (s *Service) Open(ctx context.Context, ref blob.Ref, width int) (Blob, error) {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return Blob{}, err
	}
	if width != 0 && !slices.Contains(ThumbWidths, width) {
		return Blob{}, fmt.Errorf("%w: %d (want one of %v)", ErrBadWidth, width, ThumbWidths)
	}
	if !ref.Valid() {
		return Blob{}, ErrNotFound
	}
	var info Info
	err = s.pool.QueryRow(ctx, `SELECT size, content_type, width, height FROM blob_owner WHERE ref = $1 AND household_id = $2`,
		string(ref), hh).Scan(&info.Size, &info.ContentType, &info.Width, &info.Height)
	if errors.Is(err, pgx.ErrNoRows) {
		return Blob{}, ErrNotFound
	}
	if err != nil {
		return Blob{}, err
	}
	info.Ref = ref
	if width == 0 || width >= info.Width {
		return s.open(ctx, info)
	}
	return s.thumbnail(ctx, info, width)
}

func (s *Service) open(ctx context.Context, info Info) (Blob, error) {
	rc, err := s.store.Open(ctx, info.Ref)
	if errors.Is(err, blob.ErrNotFound) {
		return Blob{}, ErrNotFound
	}
	if err != nil {
		return Blob{}, err
	}
	return Blob{Body: rc, Ref: info.Ref, Size: info.Size, ContentType: info.ContentType}, nil
}

func (s *Service) thumbnail(ctx context.Context, orig Info, width int) (Blob, error) {
	var v Info
	err := s.pool.QueryRow(ctx, `SELECT variant_ref, size FROM blob_variant WHERE ref = $1 AND width = $2`,
		string(orig.Ref), width).Scan(&v.Ref, &v.Size)
	switch {
	case err == nil:
		v.ContentType = "image/jpeg"
		b, err := s.open(ctx, v)
		if !errors.Is(err, ErrNotFound) {
			return b, err
		}
		// The variant's bytes are gone (e.g. a restore without them): rebuild.
	case !errors.Is(err, pgx.ErrNoRows):
		return Blob{}, err
	}
	v, err = s.buildThumbnail(ctx, orig, width)
	if err != nil {
		return Blob{}, err
	}
	return s.open(ctx, v)
}

// buildThumbnail resizes the original to width (keeping its aspect), stores
// the JPEG as its own blob and records it. Two concurrent builds produce the
// same bytes and the same ref, so the race is harmless.
func (s *Service) buildThumbnail(ctx context.Context, orig Info, width int) (Info, error) {
	rc, err := s.store.Open(ctx, orig.Ref)
	if errors.Is(err, blob.ErrNotFound) {
		return Info{}, ErrNotFound
	}
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = rc.Close() }()
	src, err := s.decodeImage(ctx, rc)
	if err != nil {
		return Info{}, err
	}
	b := src.Bounds()
	height := max(1, (b.Dy()*width+b.Dx()/2)/b.Dx())
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	// JPEG has no alpha: transparent PNG/WebP areas become white, not black.
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: thumbQuality}); err != nil {
		return Info{}, fmt.Errorf("encode thumbnail: %w", err)
	}
	ref, size, err := s.store.Put(ctx, &buf)
	if err != nil {
		return Info{}, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO blob_variant (ref, width, variant_ref, size) VALUES ($1, $2, $3, $4)
		ON CONFLICT (ref, width) DO UPDATE SET variant_ref = excluded.variant_ref, size = excluded.size`,
		string(orig.Ref), width, string(ref), size); err != nil {
		return Info{}, fmt.Errorf("record thumbnail: %w", err)
	}
	return Info{Ref: ref, Size: size, ContentType: "image/jpeg", Width: width, Height: height}, nil
}

// Owned reports whether household hh may reference ref, e.g. in a
// recipe.photo.add command. Commands call it inside their transaction.
func Owned(ctx context.Context, q dbq.DBTX, hh uuid.UUID, ref string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM blob_owner WHERE ref = $1 AND household_id = $2)`, ref, hh).Scan(&ok)
	return ok, err
}
