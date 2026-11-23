package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/reqctx"
)

// requestID gives each request an id (UUIDv7, so ids sort by time), echoes
// it in X-Request-Id, and starts the request's reqctx.Info.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.Must(uuid.NewV7()).String()
		w.Header().Set("X-Request-Id", id)
		info := &reqctx.Info{RequestID: id, UserAgent: r.UserAgent()}
		next.ServeHTTP(w, r.WithContext(reqctx.With(r.Context(), info)))
	})
}

// recoverPanics turns a handler panic into a logged 500 problem.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler { //nolint:errorlint,err113 // sentinel compared as net/http does
				panic(rec)
			}
			slog.ErrorContext(r.Context(), "panic", "panic", rec, "stack", string(debug.Stack()))
			writeProblem(w, r, http.StatusInternalServerError, CodeInternal, "")
		}()
		next.ServeHTTP(w, r)
	})
}

// realIP records the client address. On cardea the server only listens on a
// unix socket that only Caddy can reach, so X-Forwarded-For is trusted there,
// and only there; Caddy appends the peer it saw, so the last entry is used.
// Over TCP (development) the peer address is the client.
func realIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info := reqctx.From(r.Context()); info != nil {
			info.ClientIP = clientIP(r)
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && la.Network() == "unix" {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			last := xff[len(xff)-1]
			if i := strings.LastIndexByte(last, ','); i >= 0 {
				last = last[i+1:]
			}
			if ip := net.ParseIP(strings.TrimSpace(last)); ip != nil {
				return ip.String()
			}
		}
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}

// AccessRecord is one served request.
type AccessRecord struct {
	Time     time.Time
	Duration time.Duration
	Method   string
	// Route is the matched pattern (/api/v1/items/{id}); empty on 404.
	Route  string
	Path   string
	Status int
	Bytes  int
	// Request is a snapshot of who made the request, after session load.
	Request reqctx.Info
}

// AccessLogger receives a record for every request. Implementations must not
// block (the identity package's writer batches into access_log).
type AccessLogger interface {
	LogAccess(ctx context.Context, rec AccessRecord)
}

// SlogAccessLogger writes access records as log lines.
type SlogAccessLogger struct{}

// LogAccess implements AccessLogger.
func (SlogAccessLogger) LogAccess(ctx context.Context, rec AccessRecord) {
	slog.LogAttrs(ctx, slog.LevelInfo, "request",
		slog.String("method", rec.Method),
		slog.String("route", rec.Route),
		slog.String("path", rec.Path),
		slog.Int("status", rec.Status),
		slog.Int("bytes", rec.Bytes),
		slog.Duration("duration", rec.Duration),
		slog.String("ip", rec.Request.ClientIP),
	)
}

func accessLog(al AccessLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				rec := recover()
				status := ww.Status()
				if rec != nil {
					status = http.StatusInternalServerError
				} else if status == 0 {
					status = http.StatusOK
				}
				ar := AccessRecord{
					Time:     start,
					Duration: time.Since(start),
					Method:   r.Method,
					Path:     r.URL.Path,
					Status:   status,
					Bytes:    ww.BytesWritten(),
				}
				if rctx := chi.RouteContext(r.Context()); rctx != nil {
					ar.Route = rctx.RoutePattern()
				}
				if info := reqctx.From(r.Context()); info != nil {
					ar.Request = *info
				}
				al.LogAccess(r.Context(), ar)
				if rec != nil {
					panic(rec)
				}
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

// sameOrigin rejects cross-site requests with unsafe methods. Browsers send
// Origin (and Sec-Fetch-Site) on such requests; a request carrying neither
// is not from a browser, so it carries no ambient cookie a page could abuse.
// Session cookies are also SameSite=Lax.
func sameOrigin(publicURL *url.URL) func(http.Handler) http.Handler {
	want := publicURL.Scheme + "://" + publicURL.Host
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if err := checkOrigin(r, want); err != nil {
				slog.InfoContext(r.Context(), "cross-origin request refused", "err", err,
					"origin", r.Header.Get("Origin"), "sec_fetch_site", r.Header.Get("Sec-Fetch-Site"))
				writeProblem(w, r, http.StatusForbidden, CodeCrossOrigin, "Cross-origin request refused.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func checkOrigin(r *http.Request, want string) error {
	if origin := r.Header.Get("Origin"); origin != "" {
		if !strings.EqualFold(origin, want) {
			return errors.New("origin mismatch")
		}
		return nil
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return nil
	default:
		return errors.New("cross-site fetch")
	}
}

// limitBody caps request bodies so a client can't make the server buffer
// megabytes for the validator.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
