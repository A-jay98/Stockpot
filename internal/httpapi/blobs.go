package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/media"
)

// Problem codes of the blob endpoints.
const (
	CodeUnsupportedMedia = "unsupported_media_type"
	CodeBadImage         = "bad_image"
)

// BlobService is the part of media.Service the API uses.
type BlobService interface {
	Upload(ctx context.Context, contentType string, r io.Reader) (media.Info, error)
	Open(ctx context.Context, ref blob.Ref, width int) (media.Blob, error)
}

// blobCacheControl: only the owning household may read a blob, so it is
// private, and the browser revalidates (a cheap 304: the ETag is the content
// hash) rather than keeping it in its HTTP cache past sign-out. Offline
// copies live in the service worker's cache, which sign-out clears.
const blobCacheControl = "private, no-cache"

func blobProblem(ctx context.Context, err error) (Problem, bool) {
	switch {
	case errors.Is(err, media.ErrUnsupportedType):
		return problemCtx(ctx, http.StatusUnsupportedMediaType, CodeUnsupportedMedia, "Upload a JPEG, PNG or WebP image."), true
	case errors.Is(err, media.ErrTooLarge):
		return problemCtx(ctx, http.StatusRequestEntityTooLarge, CodeTooLarge, "Images may be at most 8 MB and 4096 px per side."), true
	case errors.Is(err, media.ErrBadImage):
		return problemCtx(ctx, http.StatusUnprocessableEntity, CodeBadImage, "The image could not be read."), true
	case errors.Is(err, media.ErrBadWidth):
		return problemCtx(ctx, http.StatusBadRequest, CodeInvalidValue, "Unsupported width."), true
	case errors.Is(err, media.ErrNotFound):
		return problemCtx(ctx, http.StatusNotFound, CodeNotFound, ""), true
	}
	return Problem{}, false
}

// UploadBlob stores a photo for the caller's household.
func (s *Server) UploadBlob(ctx context.Context, req UploadBlobRequestObject) (UploadBlobResponseObject, error) {
	if s.blobs == nil {
		return nil, errNotWired
	}
	info, err := s.blobs.Upload(ctx, req.ContentType, req.Body)
	if p, ok := blobProblem(ctx, err); ok {
		return UploadBlobdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return UploadBlob201JSONResponse{
		Ref: string(info.Ref), Size: info.Size, ContentType: info.ContentType, Width: info.Width, Height: info.Height,
	}, nil
}

// GetBlob serves a photo (or a resized copy) the caller's household owns.
func (s *Server) GetBlob(ctx context.Context, req GetBlobRequestObject) (GetBlobResponseObject, error) {
	if s.blobs == nil {
		return nil, errNotWired
	}
	width := 0
	if req.Params.W != nil {
		width = int(*req.Params.W)
	}
	b, err := s.blobs.Open(ctx, blob.Ref(req.Ref), width)
	if p, ok := blobProblem(ctx, err); ok {
		return GetBlobdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	etag := `"` + string(b.Ref) + `"`
	if req.Params.IfNoneMatch != nil && etagMatches(*req.Params.IfNoneMatch, etag) {
		_ = b.Body.Close()
		return GetBlob304Response{}, nil
	}
	cc := blobCacheControl
	return GetBlob200ImageResponse{
		Body: b.Body, ContentType: b.ContentType, ContentLength: b.Size,
		Headers: GetBlob200ResponseHeaders{CacheControl: &cc, ETag: &etag},
	}, nil
}

// etagMatches implements If-None-Match's weak comparison over a tag list.
func etagMatches(header, etag string) bool {
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimPrefix(strings.TrimSpace(tag), "W/")
		if tag == "*" || tag == etag {
			return true
		}
	}
	return false
}
