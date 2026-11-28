// Package importer turns links, Instagram reels, photos and pasted text
// into saved recipes (architecture-m2 §7). Commands queue a recipe_import
// row and a River job; the job runs the stage machine (fetch, extract,
// parse, translate) and writes the recipe through recipes.WriteVersion.
package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/media"
	"github.com/a-jay98/stockpot/internal/recipes"
	"github.com/a-jay98/stockpot/internal/settings"
)

// Flags and settings (Admin → Recipes and Settings).
var (
	FlagWebImport = settings.Define(settings.Def[bool]{
		Key:         "flag.recipe_web_import",
		Default:     true,
		Description: "Recipe import from links: fetch recipe pages from the web.",
	})
	FlagInstagram = settings.Define(settings.Def[bool]{
		Key:         "flag.recipe_instagram",
		Default:     true,
		Description: "Read Instagram reels with yt-dlp (caption and spoken transcript, no login). Off: the user pastes the caption.",
	})
	SettingFetchRetentionDays = settings.Define(settings.Def[int]{
		Key:         "retention.recipe_fetch_days",
		Default:     30,
		Validate:    settings.Between(1, 3650),
		Description: "Days to keep the recipe fetch log.",
	})
)

// Concurrency (architecture-m2 §7).
const (
	maxGlobal       = 4 // River queue workers
	maxPerHousehold = 2
	// QueueName is the River queue import jobs run on.
	QueueName = "recipe_import"
	// backgroundFrom is the batch position (0-based) from which a bulk
	// import's rows use Background LLM priority.
	backgroundFrom = 3
	maxBatch       = 50
	maxTextInput   = 20000
	maxPhotos      = 4
)

// Statuses of recipe_import.
const (
	StatusQueued       = "queued"
	StatusFetching     = "fetching"
	StatusParsing      = "parsing"
	StatusTranslating  = "translating"
	StatusDone         = "done"
	StatusFailed       = "failed"
	StatusNeedsInput   = "needs_input"
	StatusWaitingQuota = "waiting_quota"
)

// Kinds of recipe_import.
const (
	KindURL       = "url"
	KindInstagram = "instagram"
	KindPhoto     = "photo"
	KindText      = "text"
)

// Covers stores a downloaded cover image for a household and returns its
// blob ref (the photo pipeline records the household as its owner).
type Covers interface {
	SaveCover(ctx context.Context, household uuid.UUID, contentType string, data []byte) (string, error)
}

// BlobOwned reports whether a household may reference a blob (a photo it
// uploaded), for photo imports.
type BlobOwned func(ctx context.Context, tx pgx.Tx, household uuid.UUID, ref string) (bool, error)

// Deps are what the importer needs.
type Deps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	LLM      LLM
	Fetcher  *Fetcher
	Reels    Reels
	Blobs    blob.Store
	Covers   Covers
	Owned    BlobOwned
	Writer   recipes.Writer
	// TranscriptionAvailable says whether reel audio is worth downloading;
	// nil means always try.
	TranscriptionAvailable func(context.Context) bool
	Now                    func() time.Time
}

// Service runs recipe imports.
type Service struct {
	d      Deps
	client *jobs.Client

	mu      sync.Mutex
	running map[uuid.UUID]int // imports in flight per household
}

// New returns a service over d.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d, running: map[uuid.UUID]int{}}
}

// Attach gives the service the job client commands enqueue on. Call once,
// after the client is built and before serving.
func (s *Service) Attach(client *jobs.Client) { s.client = client }

// ErrNotAttached means a command ran before Attach.
var ErrNotAttached = errors.New("recipe import: not attached to the job client")

// acquire takes a household slot; false when the household already has
// maxPerHousehold imports running in this process (the app runs one).
func (s *Service) acquire(hh uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[hh] >= maxPerHousehold {
		return false
	}
	s.running[hh]++
	return true
}

func (s *Service) release(hh uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[hh]--; s.running[hh] <= 0 {
		delete(s.running, hh)
	}
}

// BlobCovers stores covers straight in the blob store (no owner record).
type BlobCovers struct{ Store blob.Store }

// SaveCover implements Covers.
func (b BlobCovers) SaveCover(ctx context.Context, _ uuid.UUID, _ string, data []byte) (string, error) {
	ref, _, err := b.Store.Put(ctx, bytes.NewReader(data))
	return string(ref), err
}

// sniffImage returns the MIME type of a stored photo.
func sniffImage(ctx context.Context, store blob.Store, ref blob.Ref) (string, error) {
	rc, err := store.Open(ctx, ref)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	head := make([]byte, 512)
	n, err := io.ReadFull(rc, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	mt := http.DetectContentType(head[:n])
	switch mt {
	case "image/jpeg", "image/png", "image/webp":
		return mt, nil
	}
	return "", fmt.Errorf("photo %s is %s, not JPEG, PNG or WebP", ref, mt)
}

// MediaCovers stores covers through the photo pipeline, which checks the
// image and records the household as its owner (so it may be the cover).
type MediaCovers struct{ Media *media.Service }

// SaveCover implements Covers.
func (m MediaCovers) SaveCover(ctx context.Context, household uuid.UUID, contentType string, data []byte) (string, error) {
	info, err := m.Media.UploadFor(ctx, household, contentType, bytes.NewReader(data))
	return string(info.Ref), err
}

// MediaOwned is BlobOwned backed by blob_owner.
func MediaOwned(ctx context.Context, tx pgx.Tx, household uuid.UUID, ref string) (bool, error) {
	return media.Owned(ctx, tx, household, ref)
}
