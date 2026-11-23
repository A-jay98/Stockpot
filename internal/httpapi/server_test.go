package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type pingFunc func(context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		db         error
		wantStatus int
		want       HealthStatus
	}{
		{name: "healthy", wantStatus: http.StatusOK, want: Ok},
		{name: "database down", db: errors.New("connection refused"), wantStatus: http.StatusServiceUnavailable, want: Degraded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := NewHandler(NewServer(map[string]Pinger{
				"database": pingFunc(func(context.Context) error { return tt.db }),
			}))
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/health", nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			var got Health
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.want || got.Version == "" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	h, err := NewHandler(NewServer(nil))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
