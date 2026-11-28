package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/reports"
)

// CodeReportConflict answers a report action that does not fit.
const CodeReportConflict = "report_conflict"

// ReportService is the part of reports.Service the API uses.
type ReportService interface {
	Submit(ctx context.Context, in reports.Input) (uuid.UUID, error)
	List(ctx context.Context, status string, limit int) (reports.Overview, error)
	Resolve(ctx context.Context, id uuid.UUID, action, value, note string) error
	RunNow(ctx context.Context) error
	Defaults(ctx context.Context, q string, limit int) ([]reports.DefaultRow, []reports.DefaultChange, error)
	SetDefaultUnit(ctx context.Context, id uuid.UUID, unit *string) error
}

var _ ReportService = (*reports.Service)(nil)

func reportProblem(ctx context.Context, err error) (Problem, bool) {
	switch {
	case errors.Is(err, reports.ErrInvalid), errors.Is(err, reports.ErrBadUnit):
		return problemCtx(ctx, http.StatusUnprocessableEntity, CodeInvalidValue, err.Error()), true
	case errors.Is(err, reports.ErrNoSubject), errors.Is(err, reports.ErrNotFound):
		return problemCtx(ctx, http.StatusNotFound, CodeNotFound, err.Error()), true
	case errors.Is(err, reports.ErrTooMany):
		return problemCtx(ctx, http.StatusTooManyRequests, CodeInvalidValue, err.Error()), true
	case errors.Is(err, reports.ErrBadAction), errors.Is(err, reports.ErrNoTarget):
		return problemCtx(ctx, http.StatusConflict, CodeReportConflict, err.Error()), true
	}
	if st, code, detail, ok := identityProblem(err); ok {
		return problemCtx(ctx, st, code, detail), true
	}
	return Problem{}, false
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CreateReport stores a problem report from the caller's household.
func (s *Server) CreateReport(ctx context.Context, req CreateReportRequestObject) (CreateReportResponseObject, error) {
	if s.reports == nil {
		return nil, errNotWired
	}
	id, err := s.reports.Submit(ctx, reports.Input{
		Kind: string(req.Body.Kind), Source: string(req.Body.Source), SubjectID: req.Body.SubjectId,
		Suggested: strOr(req.Body.Suggested), Note: strOr(req.Body.Note),
	})
	if p, ok := reportProblem(ctx, err); ok {
		return CreateReportdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return CreateReport201JSONResponse{Id: id}, nil
}

func reportContextOut(c reports.Context) ReportContext {
	out := ReportContext{
		Brand: strPtr(c.Brand), Category: strPtr(c.Category), Icon: strPtr(c.Icon), IngredientId: c.IngredientID,
		IngredientName: strPtr(c.IngredientName), IngredientSlug: strPtr(c.IngredientSlug), IngredientUnit: strPtr(c.IngredientUnit),
		InheritedUnit: strPtr(c.InheritedUnit), ItemText: strPtr(c.ItemText), ProductDefaultUnit: strPtr(c.ProductDefaultUnit),
		ProductId: c.ProductID, ProductName: strPtr(c.ProductName), Qty: strPtr(c.Qty), Unit: strPtr(c.Unit),
	}
	if c.Global {
		g := true
		out.Global = &g
	}
	return out
}

// GetAdminReports lists reports for the admin.
func (s *Server) GetAdminReports(ctx context.Context, req GetAdminReportsRequestObject) (GetAdminReportsResponseObject, error) {
	if s.reports == nil {
		return nil, errNotWired
	}
	status, limit := "", 0
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	o, err := s.reports.List(ctx, status, limit)
	if err != nil {
		return nil, err
	}
	out := AdminReports{
		Enabled: o.Enabled, AutoApply: o.AutoApply, IntervalMinutes: o.IntervalMinutes, Counts: o.Counts,
		Headroom: o.Headroom, RetryAt: o.RetryAt, Reports: make([]AdminReport, 0, len(o.Reports)),
	}
	for _, r := range o.Reports {
		ar := AdminReport{
			Id: r.ID, CreatedAt: r.CreatedAt, Household: r.Household, Reporter: r.Reporter, Kind: ReportKind(r.Kind),
			Source: ReportSource(r.Source), Status: ReportStatus(r.Status), Suggested: r.Suggested, Note: r.Note,
			Context: reportContextOut(r.Context), Attempts: r.Attempts, LlmValue: r.LLMValue, LlmReason: r.LLMReason,
			LlmConfidence: r.LLMConfidence, LlmAt: r.LLMAt, ResolvedBy: r.ResolvedBy, ResolvedAt: r.ResolvedAt, AdminNote: r.AdminNote,
		}
		if r.LLMVerdict != nil {
			v := AdminReportLlmVerdict(*r.LLMVerdict)
			ar.LlmVerdict = &v
		}
		if r.Applied != nil {
			chs := make([]ReportFieldChange, 0, len(r.Applied.Changes))
			for _, c := range r.Applied.Changes {
				fc := ReportFieldChange{Target: ReportFieldChangeTarget(c.Target), Id: c.ID, Name: c.Name, Field: c.Field, Before: c.Before, After: c.After}
				if c.Global {
					g := true
					fc.Global = &g
				}
				chs = append(chs, fc)
			}
			ar.Changes = &chs
		}
		out.Reports = append(out.Reports, ar)
	}
	return GetAdminReports200JSONResponse(out), nil
}

// RunAdminReportReview queues a review run.
func (s *Server) RunAdminReportReview(ctx context.Context, _ RunAdminReportReviewRequestObject) (RunAdminReportReviewResponseObject, error) {
	if s.reports == nil {
		return nil, errNotWired
	}
	if err := s.reports.RunNow(ctx); err != nil {
		return nil, err
	}
	return RunAdminReportReview202Response{}, nil
}

// ResolveAdminReport applies the admin's decision.
func (s *Server) ResolveAdminReport(ctx context.Context, req ResolveAdminReportRequestObject) (ResolveAdminReportResponseObject, error) {
	if s.reports == nil {
		return nil, errNotWired
	}
	err := s.reports.Resolve(ctx, req.Id, string(req.Body.Action), strOr(req.Body.Value), strOr(req.Body.Note))
	if p, ok := reportProblem(ctx, err); ok {
		return ResolveAdminReportdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return ResolveAdminReport204Response{}, nil
}

// GetAdminCatalogDefaults lists ingredient default units and their history.
func (s *Server) GetAdminCatalogDefaults(ctx context.Context, req GetAdminCatalogDefaultsRequestObject) (GetAdminCatalogDefaultsResponseObject, error) {
	if s.reports == nil {
		return nil, errNotWired
	}
	limit := 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	rows, hist, err := s.reports.Defaults(ctx, strOr(req.Params.Q), limit)
	if err != nil {
		return nil, err
	}
	out := AdminCatalogDefaults{Ingredients: make([]AdminDefaultRow, 0, len(rows)), History: make([]AdminDefaultChange, 0, len(hist))}
	for _, r := range rows {
		out.Ingredients = append(out.Ingredients, AdminDefaultRow{
			Id: r.ID, Slug: r.Slug, Name: r.Name, Household: r.Household, Parent: r.Parent, Unit: r.Unit,
			InheritedUnit: r.InheritedUnit, Source: r.Source, Products: r.Products, OpenReports: r.OpenReports,
		})
	}
	for _, h := range hist {
		out.History = append(out.History, AdminDefaultChange{
			Time: h.Time, TargetType: h.TargetType, TargetId: h.TargetID, Name: h.Name, Before: h.Before, After: h.After,
			Actor: h.Actor, ReportId: h.ReportID,
		})
	}
	return GetAdminCatalogDefaults200JSONResponse(out), nil
}

// PutAdminIngredientDefaultUnit sets or clears an ingredient's default unit.
func (s *Server) PutAdminIngredientDefaultUnit(ctx context.Context, req PutAdminIngredientDefaultUnitRequestObject) (PutAdminIngredientDefaultUnitResponseObject, error) {
	if s.reports == nil {
		return nil, errNotWired
	}
	err := s.reports.SetDefaultUnit(ctx, req.Id, req.Body.Unit)
	if p, ok := reportProblem(ctx, err); ok {
		return PutAdminIngredientDefaultUnitdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return PutAdminIngredientDefaultUnit204Response{}, nil
}
