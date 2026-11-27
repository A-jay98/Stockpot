package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/media"
)

const testRef = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

type fakeBlobs struct {
	gotType  string
	gotBytes int
	gotWidth int
	err      error
}

func (f *fakeBlobs) Upload(_ context.Context, ct string, r io.Reader) (media.Info, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return media.Info{}, err
	}
	f.gotType, f.gotBytes = ct, len(b)
	return media.Info{Ref: testRef, Size: int64(len(b)), ContentType: ct, Width: 10, Height: 5}, f.err
}

func (f *fakeBlobs) Open(_ context.Context, ref blob.Ref, w int) (media.Blob, error) {
	f.gotWidth = w
	if f.err != nil {
		return media.Blob{}, f.err
	}
	return media.Blob{Body: io.NopCloser(strings.NewReader("img")), Ref: ref, Size: 3, ContentType: "image/jpeg"}, nil
}

func blobRequest(t *testing.T, ct string, body []byte) *http.Request {
	t.Helper()
	r := llmRequest(t, http.MethodPost, "/api/v1/blobs", "")
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Type", ct)
	return r
}

func TestUploadBlob(t *testing.T) {
	f := &fakeBlobs{}
	h := newHandler(t, NewServer(Deps{Blobs: f}), Options{PublicURL: testOrigin, Authorizer: allowAll{}})

	// Bigger than the default 1 MB cap: uploads have their own limit.
	rec := do(h, blobRequest(t, "image/jpeg", make([]byte, 3<<20)))
	var out BlobUploaded
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Ref != testRef || f.gotBytes != 3<<20 || f.gotType != "image/jpeg" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	if rec := do(h, blobRequest(t, "image/jpeg", make([]byte, media.MaxUploadBytes+1))); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, blobRequest(t, "text/plain", []byte("hi"))); rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("text/plain: %d %s", rec.Code, rec.Body)
	}
	// The default cap still applies elsewhere.
	big := llmRequest(t, http.MethodPost, "/api/v1/commands", strings.Repeat(" ", 2<<20)+"{}")
	if rec := do(h, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("commands cap: %d", rec.Code)
	}

	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{media.ErrUnsupportedType, 415, CodeUnsupportedMedia},
		{media.ErrBadImage, 422, CodeBadImage},
		{media.ErrTooLarge, 413, CodeTooLarge},
	} {
		f.err = tc.err
		rec := do(h, blobRequest(t, "image/png", []byte("x")))
		if p := decodeProblem(t, rec); rec.Code != tc.status || p.Code != tc.code {
			t.Errorf("%v: %d %s", tc.err, rec.Code, p.Code)
		}
	}
}

func TestGetBlob(t *testing.T) {
	f := &fakeBlobs{}
	h := newHandler(t, NewServer(Deps{Blobs: f}), Options{PublicURL: testOrigin, Authorizer: allowAll{}})

	rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/blobs/"+testRef+"?w=512", ""))
	if rec.Code != http.StatusOK || rec.Body.String() != "img" || f.gotWidth != 512 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control %q", cc)
	}
	if rec.Header().Get("Content-Type") != "image/jpeg" || rec.Header().Get("ETag") != `"`+testRef+`"` {
		t.Fatalf("headers %v", rec.Header())
	}

	r := llmRequest(t, http.MethodGet, "/api/v1/blobs/"+testRef, "")
	r.Header.Set("If-None-Match", `"other", W/"`+testRef+`"`)
	if rec := do(h, r); rec.Code != http.StatusNotModified || f.gotWidth != 0 {
		t.Fatalf("conditional: %d", rec.Code)
	}

	for _, path := range []string{"/api/v1/blobs/" + testRef + "?w=300", "/api/v1/blobs/NOTAREF"} {
		if rec := do(h, llmRequest(t, http.MethodGet, path, "")); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	f.err = media.ErrNotFound
	if rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/blobs/"+testRef, "")); rec.Code != http.StatusNotFound {
		t.Fatalf("not owned: %d", rec.Code)
	}
}

func TestBlobsNeedSignIn(t *testing.T) {
	h := newHandler(t, NewServer(Deps{Blobs: &fakeBlobs{}}), Options{PublicURL: testOrigin})
	if rec := do(h, blobRequest(t, "image/jpeg", []byte("x"))); rec.Code != http.StatusUnauthorized {
		t.Fatalf("upload: %d", rec.Code)
	}
	if rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/blobs/"+testRef, "")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("get: %d", rec.Code)
	}
}
