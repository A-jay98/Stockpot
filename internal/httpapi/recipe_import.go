package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/recipes/importer"
)

// RecipeImports is the part of importer.Service the admin API uses.
type RecipeImports interface {
	AdminStatus(ctx context.Context) (importer.AdminStatus, error)
	AdminRetry(ctx context.Context, id uuid.UUID) error
}

var _ RecipeImports = (*importer.Service)(nil)

func noImports(ctx context.Context) Problem {
	return problemCtx(ctx, http.StatusServiceUnavailable, CodeNotFound, "Recipe import is not available in this build.")
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// GetAdminRecipes implements StrictServerInterface.
func (s *Server) GetAdminRecipes(ctx context.Context, _ GetAdminRecipesRequestObject) (GetAdminRecipesResponseObject, error) {
	if s.imports == nil {
		p := noImports(ctx)
		return GetAdminRecipesdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	st, err := s.imports.AdminStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := AdminRecipes{
		WebImport: st.WebImport, Instagram: st.Instagram, Last24h: st.Last24h, Last14d: st.Last14d,
		Failures: make([]AdminRecipeImport, 0, len(st.Failures)), Fetches: make([]AdminRecipeFetch, 0, len(st.Fetches)),
		Tools: AdminRecipeTools{
			YtDlp: st.Tools.YtDLP, YtDlpVersion: optStr(st.Tools.YtDLPVersion),
			Ffmpeg: st.Tools.FFmpeg, FfmpegVersion: optStr(st.Tools.FFmpegVersion), Error: optStr(st.Tools.Error),
		},
		Transcription: st.Transcription,
	}
	for _, f := range st.Failures {
		out.Failures = append(out.Failures, AdminRecipeImport{
			Id: f.ID, Household: f.Household, Kind: f.Kind, Input: f.Input, Status: f.Status,
			Code: f.Code, Detail: f.Detail, Attempts: f.Attempts, UpdatedAt: f.UpdatedAt,
		})
	}
	for _, f := range st.Fetches {
		out.Fetches = append(out.Fetches, AdminRecipeFetch{
			At: f.At, Purpose: AdminRecipeFetchPurpose(f.Purpose), Url: f.URL, FinalUrl: f.FinalURL,
			Status: f.Status, Bytes: f.Bytes, DurationMs: f.DurationMs, Error: f.Error,
		})
	}
	return GetAdminRecipes200JSONResponse(out), nil
}

// RetryAdminRecipeImport implements StrictServerInterface.
func (s *Server) RetryAdminRecipeImport(ctx context.Context, req RetryAdminRecipeImportRequestObject) (RetryAdminRecipeImportResponseObject, error) {
	if s.imports == nil {
		p := noImports(ctx)
		return RetryAdminRecipeImportdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err := s.imports.AdminRetry(ctx, req.ImportId); err != nil {
		if errors.Is(err, importer.ErrImportNotFound) {
			p := problemCtx(ctx, http.StatusNotFound, CodeNotFound, "Import not found.")
			return RetryAdminRecipeImportdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
		}
		return nil, err
	}
	return RetryAdminRecipeImport202Response{}, nil
}
