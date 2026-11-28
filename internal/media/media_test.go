package media_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/media"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

type env struct {
	pool  *pgxpool.Pool
	store *blob.FS
	svc   *media.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	store, err := blob.NewFS(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return &env{pool: pool, store: store, svc: media.New(pool, store)}
}

// member returns a context signed in as a new user of a new household.
func (e *env) member(t *testing.T) (context.Context, uuid.UUID) {
	t.Helper()
	var hh, user uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Home') RETURNING id`).Scan(&hh); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO app_user (id, display_name, status) VALUES (gen_random_uuid(), 'Cook', 'active') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	return identity.WithPrincipal(t.Context(), &identity.Principal{UserID: user, Status: identity.StatusActive, HouseholdID: hh}), hh
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(t.Context(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUploadAndOwnership(t *testing.T) {
	e := newEnv(t)
	ctxA, hhA := e.member(t)
	ctxB, hhB := e.member(t)
	data := encodeJPEG(t, 64, 32)

	info, err := e.svc.Upload(ctxA, "image/jpeg", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Ref.Valid() || info.Size != int64(len(data)) || info.Width != 64 || info.Height != 32 || info.ContentType != "image/jpeg" {
		t.Fatalf("info %+v", info)
	}

	// Household B cannot read A's photo, not even knowing the ref.
	if _, err := e.svc.Open(ctxB, info.Ref, 0); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("B open before upload: %v", err)
	}
	if ok, err := media.Owned(t.Context(), e.pool, hhB, string(info.Ref)); err != nil || ok {
		t.Fatalf("owned by B: %v %v", ok, err)
	}

	b, err := e.svc.Open(ctxA, info.Ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(b.Body)
	_ = b.Body.Close()
	if !bytes.Equal(got, data) || b.ContentType != "image/jpeg" || b.Ref != info.Ref {
		t.Fatalf("served %d bytes %s", len(got), b.ContentType)
	}

	// Duplicate upload by A is a no-op; the same bytes from B add B as owner.
	again, err := e.svc.Upload(ctxA, "image/jpeg; charset=binary", bytes.NewReader(data))
	if err != nil || again.Ref != info.Ref {
		t.Fatalf("again: %v %v", again.Ref, err)
	}
	if _, err := e.svc.Upload(ctxB, "image/jpeg", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM blob_owner WHERE ref = $1`, string(info.Ref)); n != 2 {
		t.Fatalf("owners %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_event WHERE action = 'blob.upload' AND target_id = $1`, string(info.Ref)); n != 2 {
		t.Fatalf("audit events %d", n)
	}
	for _, hh := range []uuid.UUID{hhA, hhB} {
		if ok, err := media.Owned(t.Context(), e.pool, hh, string(info.Ref)); err != nil || !ok {
			t.Fatalf("owned by %v: %v %v", hh, ok, err)
		}
	}
	if _, err := e.svc.Open(ctxB, info.Ref, 0); err != nil {
		t.Fatalf("B open after upload: %v", err)
	}
}

func TestUploadRejects(t *testing.T) {
	e := newEnv(t)
	ctx, _ := e.member(t)
	var gifData bytes.Buffer
	if err := gif.Encode(&gifData, image.NewPaletted(image.Rect(0, 0, 4, 4), []color.Color{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	jpg := encodeJPEG(t, 40, 40)
	big := make([]byte, media.MaxUploadBytes+1)
	copy(big, jpg)
	for _, tc := range []struct {
		name string
		ct   string
		data []byte
		want error
	}{
		{"declared gif", "image/gif", gifData.Bytes(), media.ErrUnsupportedType},
		{"gif bytes as png", "image/png", gifData.Bytes(), media.ErrUnsupportedType},
		{"jpeg declared png", "image/png", jpg, media.ErrUnsupportedType},
		{"text", "image/jpeg", []byte("hello, not an image"), media.ErrBadImage},
		{"truncated", "image/jpeg", jpg[:len(jpg)/2], media.ErrBadImage},
		{"over 8 MB", "image/jpeg", big, media.ErrTooLarge},
		{"over 4096 px", "image/png", encodePNG(t, media.MaxSide+1, 1), media.ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.svc.Upload(ctx, tc.ct, bytes.NewReader(tc.data)); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if n := e.count(t, `SELECT count(*) FROM blob_owner`); n != 0 {
		t.Fatalf("owners after rejects: %d", n)
	}
}

func TestThumbnail(t *testing.T) {
	e := newEnv(t)
	ctx, _ := e.member(t)
	info, err := e.svc.Upload(ctx, "image/png", bytes.NewReader(encodePNG(t, 600, 300)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Open(ctx, info.Ref, 300); !errors.Is(err, media.ErrBadWidth) {
		t.Fatalf("odd width: %v", err)
	}

	thumb := func() media.Blob {
		t.Helper()
		b, err := e.svc.Open(ctx, info.Ref, 256)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = b.Body.Close() }()
		cfg, format, err := image.DecodeConfig(b.Body)
		if err != nil || format != "jpeg" || cfg.Width != 256 || cfg.Height != 128 || b.ContentType != "image/jpeg" {
			t.Fatalf("thumb %s %dx%d %v", format, cfg.Width, cfg.Height, err)
		}
		return b
	}
	first := thumb()
	if first.Ref == info.Ref {
		t.Fatal("thumbnail served the original")
	}
	second := thumb()
	if second.Ref != first.Ref || e.count(t, `SELECT count(*) FROM blob_variant WHERE ref = $1`, string(info.Ref)) != 1 {
		t.Fatalf("variant not reused: %s %s", first.Ref, second.Ref)
	}

	// Lost variant bytes are rebuilt.
	if err := e.store.Delete(t.Context(), first.Ref); err != nil {
		t.Fatal(err)
	}
	thumb()

	// A width at or above the original serves the original.
	b, err := e.svc.Open(ctx, info.Ref, 1024)
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Body.Close()
	if b.Ref != info.Ref || b.ContentType != "image/png" {
		t.Fatalf("w=1024: %s %s", b.Ref, b.ContentType)
	}

	// Another household can't get the thumbnail either.
	other, _ := e.member(t)
	if _, err := e.svc.Open(other, info.Ref, 256); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("other: %v", err)
	}
}
