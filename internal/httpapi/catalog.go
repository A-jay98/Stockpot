package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/identity"
)

// CodeIngredientConflict answers an admin ingredient change that cannot apply.
const CodeIngredientConflict = "ingredient_conflict"

// CatalogEnricher is the part of catalog.Enricher the admin API uses.
type CatalogEnricher interface {
	Status(ctx context.Context) (catalog.EnrichStatus, error)
	RunNow(ctx context.Context) error
	RetryFailed(ctx context.Context) (int, error)
	Promote(ctx context.Context, id uuid.UUID) error
	Merge(ctx context.Context, id uuid.UUID, targetSlug string) error
	DeleteIngredient(ctx context.Context, id uuid.UUID) error
}

// CatalogMatcher serves the match endpoint.
type CatalogMatcher interface {
	Match(ctx context.Context, household uuid.UUID, text string) (catalog.MatchResult, error)
}

var (
	_ CatalogEnricher = (*catalog.Enricher)(nil)
	_ CatalogMatcher  = catalog.Matcher{}
)

func ingredientProblem(ctx context.Context, err error) (Problem, bool) {
	switch {
	case errors.Is(err, catalog.ErrIngredientNotFound):
		return problemCtx(ctx, http.StatusNotFound, CodeNotFound, "Ingredient not found."), true
	case errors.Is(err, catalog.ErrNotReviewable), errors.Is(err, catalog.ErrSlugTaken),
		errors.Is(err, catalog.ErrParentNotGlobal), errors.Is(err, catalog.ErrBadMergeTarget):
		return problemCtx(ctx, http.StatusConflict, CodeIngredientConflict, err.Error()), true
	}
	return Problem{}, false
}

func noCatalog(ctx context.Context) Problem {
	return problemCtx(ctx, http.StatusServiceUnavailable, CodeNotFound, "Catalog enrichment is not available in this build.")
}

// GetAdminCatalog implements StrictServerInterface.
func (s *Server) GetAdminCatalog(ctx context.Context, _ GetAdminCatalogRequestObject) (GetAdminCatalogResponseObject, error) {
	if s.enrich == nil {
		p := noCatalog(ctx)
		return GetAdminCatalogdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	st, err := s.enrich.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := AdminCatalog{
		Enabled: st.Enabled, IntervalMinutes: st.IntervalMinutes, Counts: st.Counts, Headroom: st.Headroom, RetryAt: st.RetryAt,
		Runs: []AdminEnrichRun{}, LlmIngredients: []AdminLLMIngredient{}, Review: []AdminReviewProduct{},
	}
	for _, r := range st.Runs {
		out.Runs = append(out.Runs, AdminEnrichRun{
			Id: r.ID, Trigger: AdminEnrichRunTrigger(r.Trigger), Outcome: AdminEnrichRunOutcome(r.Outcome), StartedAt: r.StartedAt,
			FinishedAt: r.FinishedAt, Items: r.Items, Applied: r.Applied, Failed: r.Failed, Cached: r.Cached, Calls: r.Calls,
			Provider: r.Provider, Reason: r.Reason,
		})
	}
	for _, i := range st.LLMIngredients {
		out.LlmIngredients = append(out.LlmIngredients, AdminLLMIngredient{
			Id: i.ID, Slug: i.Slug, Name: i.Name, Parent: i.Parent, Household: i.Household, Products: i.Products, CreatedAt: i.CreatedAt,
		})
	}
	for _, p := range st.Review {
		out.Review = append(out.Review, AdminReviewProduct{
			Id: p.ID, Name: p.Name, Brand: p.Brand, Household: p.Household, Ingredient: p.Ingredient, MatchSource: p.MatchSource,
			Confidence: p.Confidence, State: p.State, Attempts: p.Attempts,
		})
	}
	return GetAdminCatalog200JSONResponse(out), nil
}

// RunAdminCatalogEnrich implements StrictServerInterface.
func (s *Server) RunAdminCatalogEnrich(ctx context.Context, _ RunAdminCatalogEnrichRequestObject) (RunAdminCatalogEnrichResponseObject, error) {
	if s.enrich == nil {
		p := noCatalog(ctx)
		return RunAdminCatalogEnrichdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err := s.enrich.RunNow(ctx); err != nil {
		return nil, err
	}
	return RunAdminCatalogEnrich202Response{}, nil
}

// RetryAdminCatalogFailed implements StrictServerInterface.
func (s *Server) RetryAdminCatalogFailed(ctx context.Context, _ RetryAdminCatalogFailedRequestObject) (RetryAdminCatalogFailedResponseObject, error) {
	if s.enrich == nil {
		p := noCatalog(ctx)
		return RetryAdminCatalogFaileddefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	n, err := s.enrich.RetryFailed(ctx)
	if err != nil {
		return nil, err
	}
	return RetryAdminCatalogFailed200JSONResponse{Retried: n}, nil
}

// PromoteAdminIngredient implements StrictServerInterface.
func (s *Server) PromoteAdminIngredient(ctx context.Context, req PromoteAdminIngredientRequestObject) (PromoteAdminIngredientResponseObject, error) {
	if s.enrich == nil {
		p := noCatalog(ctx)
		return PromoteAdminIngredientdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err := s.enrich.Promote(ctx, req.Id); err != nil {
		if p, ok := ingredientProblem(ctx, err); ok {
			return PromoteAdminIngredientdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
		}
		return nil, err
	}
	return PromoteAdminIngredient204Response{}, nil
}

// MergeAdminIngredient implements StrictServerInterface.
func (s *Server) MergeAdminIngredient(ctx context.Context, req MergeAdminIngredientRequestObject) (MergeAdminIngredientResponseObject, error) {
	if s.enrich == nil {
		p := noCatalog(ctx)
		return MergeAdminIngredientdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err := s.enrich.Merge(ctx, req.Id, req.Body.TargetSlug); err != nil {
		if p, ok := ingredientProblem(ctx, err); ok {
			return MergeAdminIngredientdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
		}
		return nil, err
	}
	return MergeAdminIngredient204Response{}, nil
}

// DeleteAdminIngredient implements StrictServerInterface.
func (s *Server) DeleteAdminIngredient(ctx context.Context, req DeleteAdminIngredientRequestObject) (DeleteAdminIngredientResponseObject, error) {
	if s.enrich == nil {
		p := noCatalog(ctx)
		return DeleteAdminIngredientdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err := s.enrich.DeleteIngredient(ctx, req.Id); err != nil {
		if p, ok := ingredientProblem(ctx, err); ok {
			return DeleteAdminIngredientdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
		}
		return nil, err
	}
	return DeleteAdminIngredient204Response{}, nil
}

// MatchCatalog implements StrictServerInterface.
func (s *Server) MatchCatalog(ctx context.Context, req MatchCatalogRequestObject) (MatchCatalogResponseObject, error) {
	if s.matcher == nil {
		return nil, errNotWired
	}
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(req.Params.Q)
	if q == "" {
		p := problemCtx(ctx, http.StatusUnprocessableEntity, CodeInvalidValue, "q must not be empty.")
		return MatchCatalogdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	res, err := s.matcher.Match(ctx, hh, q)
	if err != nil {
		return nil, err
	}
	out := CatalogMatch{Products: make([]CatalogMatchProduct, 0, len(res.Products))}
	if m := res.Ingredient; m != nil {
		ing := &CatalogMatchIngredient{Id: m.IngredientID, Slug: m.Slug, Name: m.Name, Confidence: m.Confidence, CategorySlug: optString(m.CategorySlug)}
		if m.LocationHint != "" {
			h := CatalogMatchIngredientLocationHint(m.LocationHint)
			ing.LocationHint = &h
		}
		out.Ingredient = ing
	}
	for _, p := range res.Products {
		out.Products = append(out.Products, CatalogMatchProduct{Id: p.ID, Name: p.Name, Brand: p.Brand})
	}
	return MatchCatalog200JSONResponse(out), nil
}
