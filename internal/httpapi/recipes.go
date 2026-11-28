package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/recipes"
)

// CodeLineUnmatched answers a swap request for a line without an ingredient.
const CodeLineUnmatched = "line_unmatched"

// RecipeAssistant serves the recipe LLM helpers.
type RecipeAssistant interface {
	Substitute(ctx context.Context, hh, recipe uuid.UUID, version *uuid.UUID, line uuid.UUID) ([]recipes.SwapResult, error)
	Ideas(ctx context.Context, hh uuid.UUID, user *uuid.UUID, note string) ([]uuid.UUID, error)
}

var _ RecipeAssistant = (*recipes.Assistant)(nil)

// recipeProblem maps assistant errors to problems; ok is false for unexpected errors.
func recipeProblem(ctx context.Context, err error) (Problem, bool) {
	if p, ok := LLMProblem(ctx, err); ok {
		return p, true
	}
	switch {
	case errors.Is(err, recipes.ErrRecipeNotFound):
		return problemCtx(ctx, http.StatusNotFound, CodeNotFound, "Recipe not found."), true
	case errors.Is(err, recipes.ErrLineNotFound):
		return problemCtx(ctx, http.StatusNotFound, CodeNotFound, "That ingredient line isn't in this recipe."), true
	case errors.Is(err, recipes.ErrLineUnmatched):
		return problemCtx(ctx, http.StatusUnprocessableEntity, CodeLineUnmatched, "Pick what this ingredient is first."), true
	}
	return Problem{}, false
}

// FindRecipeSubstitutes implements StrictServerInterface.
func (s *Server) FindRecipeSubstitutes(ctx context.Context, req FindRecipeSubstitutesRequestObject) (FindRecipeSubstitutesResponseObject, error) {
	if s.recipes == nil {
		p := noLLM(ctx)
		return FindRecipeSubstitutesdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if req.Body == nil {
		p := problemCtx(ctx, http.StatusUnprocessableEntity, CodeInvalidValue, "lineId is required.")
		return FindRecipeSubstitutesdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	swaps, err := s.recipes.Substitute(ctx, hh, req.Id, req.Body.VersionId, req.Body.LineId)
	if err != nil {
		if p, ok := recipeProblem(ctx, err); ok {
			return FindRecipeSubstitutesdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
		}
		return nil, err
	}
	out := RecipeSubstitutes{Substitutions: make([]RecipeSubstitute, 0, len(swaps))}
	for _, sw := range swaps {
		out.Substitutions = append(out.Substitutions, RecipeSubstitute{Id: sw.ID, Note: sw.Note})
	}
	return FindRecipeSubstitutes200JSONResponse(out), nil
}

// CreateRecipeIdeas implements StrictServerInterface.
func (s *Server) CreateRecipeIdeas(ctx context.Context, req CreateRecipeIdeasRequestObject) (CreateRecipeIdeasResponseObject, error) {
	if s.recipes == nil {
		p := noLLM(ctx)
		return CreateRecipeIdeasdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	var user *uuid.UUID
	if p := identity.PrincipalFrom(ctx); p != nil {
		id := p.UserID
		user = &id
	}
	note := ""
	if req.Body != nil && req.Body.Note != nil {
		note = *req.Body.Note
	}
	ids, err := s.recipes.Ideas(ctx, hh, user, note)
	if err != nil {
		if p, ok := recipeProblem(ctx, err); ok {
			return CreateRecipeIdeasdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
		}
		return nil, err
	}
	return CreateRecipeIdeas200JSONResponse(RecipeIdeas{RecipeIds: ids}), nil
}
