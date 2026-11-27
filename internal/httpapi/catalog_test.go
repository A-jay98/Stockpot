package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/catalog"
)

type fakeEnrich struct {
	err  error
	ran  int
	last uuid.UUID
	slug string
}

func (f *fakeEnrich) Status(context.Context) (catalog.EnrichStatus, error) {
	return catalog.EnrichStatus{
		Enabled: true, IntervalMinutes: 20, Counts: map[string]int{"pending": 2, "done": 5},
		Runs:           []catalog.EnrichRun{{ID: uuid.New(), Trigger: "scheduled", Outcome: "deferred", StartedAt: time.Now()}},
		LLMIngredients: []catalog.ReviewIngredient{{ID: uuid.New(), Slug: "kumquat", Name: "Kumquat", Household: "Home", Products: 1, CreatedAt: time.Now()}},
	}, nil
}
func (f *fakeEnrich) RunNow(context.Context) error                  { f.ran++; return f.err }
func (f *fakeEnrich) RetryFailed(context.Context) (int, error)      { return 3, f.err }
func (f *fakeEnrich) Promote(_ context.Context, id uuid.UUID) error { f.last = id; return f.err }
func (f *fakeEnrich) Merge(_ context.Context, id uuid.UUID, slug string) error {
	f.last, f.slug = id, slug
	return f.err
}
func (f *fakeEnrich) DeleteIngredient(context.Context, uuid.UUID) error { return f.err }

func catalogHandler(t *testing.T, f CatalogEnricher, az Authorizer) http.Handler {
	return newHandler(t, NewServer(Deps{Enrich: f}), Options{PublicURL: testOrigin, Authorizer: az})
}

func TestCatalogAdmin(t *testing.T) {
	f := &fakeEnrich{}
	h := catalogHandler(t, f, allowAll{})
	rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/admin/catalog", ""))
	var c AdminCatalog
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &c) != nil || c.Counts["pending"] != 2 || len(c.LlmIngredients) != 1 || c.Review == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/admin/catalog/enrich/run", "")); rec.Code != http.StatusAccepted || f.ran != 1 {
		t.Fatalf("run: %d", rec.Code)
	}
	if rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/admin/catalog/enrich/retry-failed", "")); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"retried":3`) {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	id := uuid.New()
	if rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/admin/catalog/ingredients/"+id.String()+"/merge", `{"targetSlug":"orange"}`)); rec.Code != http.StatusNoContent || f.last != id || f.slug != "orange" {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{catalog.ErrIngredientNotFound, 404}, {catalog.ErrSlugTaken, 409}, {catalog.ErrBadMergeTarget, 409}} {
		f.err = tc.err
		rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/admin/catalog/ingredients/"+id.String()+"/promote", ""))
		if rec.Code != tc.status {
			t.Errorf("%v: %d", tc.err, rec.Code)
		}
	}
}

func TestCatalogEndpointsNeedSignIn(t *testing.T) {
	h := catalogHandler(t, &fakeEnrich{}, nil)
	for _, c := range [][2]string{{http.MethodGet, "/api/v1/admin/catalog"}, {http.MethodDelete, "/api/v1/admin/catalog/ingredients/" + uuid.NewString()}, {http.MethodGet, "/api/v1/catalog/match?q=milk"}} {
		if rec := do(h, llmRequest(t, c[0], c[1], "")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%v: %d", c, rec.Code)
		}
	}
}
