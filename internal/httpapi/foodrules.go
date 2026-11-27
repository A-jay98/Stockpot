package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/inventory"
)

// FoodRuleService is the part of inventory.RuleService the API uses.
type FoodRuleService interface {
	List(ctx context.Context) ([]inventory.Rule, error)
	Put(ctx context.Context, kind string, subject uuid.UUID, sl inventory.ShelfLife) error
	Delete(ctx context.Context, kind string, subject uuid.UUID) error
	Admin(ctx context.Context) ([]inventory.Rule, []inventory.AdminRule, error)
}

func shelfOut(s inventory.ShelfLife) ShelfLife {
	get := func(k string) *int {
		if d, ok := s[k]; ok {
			return &d
		}
		return nil
	}
	return ShelfLife{
		Fridge: get(inventory.KeyFridge), Freezer: get(inventory.KeyFreezer), Pantry: get(inventory.KeyPantry),
		OpenedFridge: get(inventory.KeyOpenedFridge), ThawedFridge: get(inventory.KeyThawedFridge),
	}
}

func shelfIn(s ShelfLife) inventory.ShelfLife {
	out := inventory.ShelfLife{}
	for k, v := range map[string]*int{
		inventory.KeyFridge: s.Fridge, inventory.KeyFreezer: s.Freezer, inventory.KeyPantry: s.Pantry,
		inventory.KeyOpenedFridge: s.OpenedFridge, inventory.KeyThawedFridge: s.ThawedFridge,
	} {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

func ruleOut(r inventory.Rule) FoodRule {
	out := FoodRule{
		SubjectKind: FoodRuleSubjectKind(r.Subject), SubjectId: r.SubjectID,
		ShelfLife: shelfOut(r.ShelfLife), UpdatedAt: r.UpdatedAt,
	}
	out.HouseholdId = r.HouseholdID
	return out
}

func foodRuleProblem(ctx context.Context, err error) (Problem, bool) {
	switch {
	case errors.Is(err, inventory.ErrInvalidRule):
		return problemCtx(ctx, http.StatusBadRequest, CodeInvalidValue, err.Error()), true
	case errors.Is(err, inventory.ErrNoSubject):
		return problemCtx(ctx, http.StatusNotFound, CodeNotFound, "No such ingredient or category."), true
	}
	return Problem{}, false
}

// ListFoodRules returns the defaults and the household's overrides.
func (s *Server) ListFoodRules(ctx context.Context, _ ListFoodRulesRequestObject) (ListFoodRulesResponseObject, error) {
	if s.foodRules == nil {
		return nil, errNotWired
	}
	rules, err := s.foodRules.List(ctx)
	if err != nil {
		return nil, err
	}
	out := FoodRules{Rules: make([]FoodRule, 0, len(rules))}
	for _, r := range rules {
		out.Rules = append(out.Rules, ruleOut(r))
	}
	return ListFoodRules200JSONResponse(out), nil
}

// PutFoodRule sets the household's override.
func (s *Server) PutFoodRule(ctx context.Context, req PutFoodRuleRequestObject) (PutFoodRuleResponseObject, error) {
	if s.foodRules == nil {
		return nil, errNotWired
	}
	err := s.foodRules.Put(ctx, req.SubjectKind, req.SubjectId, shelfIn(req.Body.ShelfLife))
	if p, ok := foodRuleProblem(ctx, err); ok {
		return PutFoodRuledefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return PutFoodRule204Response{}, nil
}

// DeleteFoodRule removes the household's override.
func (s *Server) DeleteFoodRule(ctx context.Context, req DeleteFoodRuleRequestObject) (DeleteFoodRuleResponseObject, error) {
	if s.foodRules == nil {
		return nil, errNotWired
	}
	err := s.foodRules.Delete(ctx, req.SubjectKind, req.SubjectId)
	if p, ok := foodRuleProblem(ctx, err); ok {
		return DeleteFoodRuledefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return DeleteFoodRule204Response{}, nil
}

// GetAdminFoodRules is the admin's read-only view of every rule.
func (s *Server) GetAdminFoodRules(ctx context.Context, _ GetAdminFoodRulesRequestObject) (GetAdminFoodRulesResponseObject, error) {
	if s.foodRules == nil {
		return nil, errNotWired
	}
	defs, overs, err := s.foodRules.Admin(ctx)
	if err != nil {
		return nil, err
	}
	out := AdminFoodRules{Defaults: make([]FoodRule, 0, len(defs)), Overrides: make([]AdminFoodRule, 0, len(overs))}
	for _, r := range defs {
		out.Defaults = append(out.Defaults, ruleOut(r))
	}
	for _, r := range overs {
		out.Overrides = append(out.Overrides, AdminFoodRule{
			HouseholdId: r.HouseholdID, HouseholdName: r.HouseholdName, ShelfLife: shelfOut(r.ShelfLife),
			SubjectId: r.SubjectID, SubjectKind: AdminFoodRuleSubjectKind(r.Subject), UpdatedAt: r.UpdatedAt,
		})
	}
	return GetAdminFoodRules200JSONResponse(out), nil
}
