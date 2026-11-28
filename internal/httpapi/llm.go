package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/a-jay98/stockpot/internal/llm"
)

// LLMGateway is the part of llm.Router the API uses.
type LLMGateway interface {
	Status(ctx context.Context) (llm.Status, error)
	Usage(ctx context.Context, days int) ([]llm.UsageRow, string, error)
	TestPrompt(ctx context.Context, provider, prompt string) (llm.Response, error)
	SetAPIKey(ctx context.Context, id, key string) error
}

var _ LLMGateway = (*llm.Router)(nil)

// LLMProblem maps an error from the LLM gateway to the problem a feature
// handler answers with: 503 llm_unavailable (the client falls back to
// manual entry) when no provider could give a usable answer. ok is false for
// other errors, which are the handler's to report.
func LLMProblem(ctx context.Context, err error) (Problem, bool) {
	if errors.Is(err, llm.ErrUnavailable) || errors.Is(err, llm.ErrBadOutput) {
		return problemCtx(ctx, http.StatusServiceUnavailable, CodeLLMUnavailable,
			"The assistant is unavailable right now; enter it manually."), true
	}
	return Problem{}, false
}

func noLLM(ctx context.Context) Problem {
	return problemCtx(ctx, http.StatusServiceUnavailable, CodeLLMUnavailable, "The LLM gateway is not configured.")
}

// GetLLMStatus reports providers, state and today's quota.
func (s *Server) GetLLMStatus(ctx context.Context, _ GetLLMStatusRequestObject) (GetLLMStatusResponseObject, error) {
	if s.llm == nil {
		p := noLLM(ctx)
		return GetLLMStatusdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	st, err := s.llm.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := LLMStatus{
		Enabled:          st.Enabled,
		Available:        st.Available,
		Vision:           st.Vision,
		Providers:        make([]LLMProviderStatus, 0, len(st.Providers)),
		Tz:               st.TZ,
		RequestsToday:    st.RequestsToday,
		TokensToday:      st.TokensToday,
		GlobalRequestCap: st.GlobalRequestCap,
		GlobalRemaining:  st.GlobalRemaining,
		ResetAt:          st.ResetAt,
	}
	for _, p := range st.Providers {
		ps := LLMProviderStatus{
			Config: LLMProviderConfig{
				Id:              p.Config.ID,
				Kind:            p.Config.Kind,
				BaseUrl:         p.Config.BaseURL,
				Model:           p.Config.Model,
				Vision:          p.Config.Vision,
				JsonSchema:      p.Config.JSONSchema,
				Enabled:         p.Config.Enabled,
				DailyRequestCap: p.Config.DailyRequestCap,
				DailyTokenCap:   p.Config.DailyTokenCap,
				ResetTz:         p.Config.ResetTZ,
			},
			HasKey:            p.HasKey,
			State:             p.State,
			Until:             p.Until,
			RequestsToday:     p.RequestsToday,
			TokensToday:       p.TokensToday,
			RemainingRequests: p.RemainingRequests,
			RemainingTokens:   p.RemainingTokens,
			ResetAt:           p.ResetAt,
		}
		if p.Config.TranscriptionModel != "" {
			m := p.Config.TranscriptionModel
			ps.Config.TranscriptionModel = &m
		}
		if p.Config.TimeoutSeconds > 0 {
			t := p.Config.TimeoutSeconds
			ps.Config.TimeoutSeconds = &t
		}
		if p.Detail != "" {
			d := p.Detail
			ps.Detail = &d
		}
		out.Providers = append(out.Providers, ps)
	}
	return GetLLMStatus200JSONResponse(out), nil
}

// GetLLMUsage reports usage per day, provider and feature.
func (s *Server) GetLLMUsage(ctx context.Context, req GetLLMUsageRequestObject) (GetLLMUsageResponseObject, error) {
	if s.llm == nil {
		p := noLLM(ctx)
		return GetLLMUsagedefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	days := 7
	if req.Params.Days != nil {
		days = *req.Params.Days
	}
	rows, tz, err := s.llm.Usage(ctx, days)
	if err != nil {
		return nil, err
	}
	out := LLMUsage{Tz: tz, Rows: make([]LLMUsageRow, 0, len(rows))}
	for _, r := range rows {
		out.Rows = append(out.Rows, LLMUsageRow{
			Day:          dateOf(r.Day),
			Provider:     r.Provider,
			Feature:      r.Feature,
			Requests:     r.Requests,
			CacheHits:    r.CacheHits,
			Failures:     r.Failures,
			TokensIn:     r.TokensIn,
			TokensOut:    r.TokensOut,
			AvgLatencyMs: r.AvgLatencyMs,
		})
	}
	return GetLLMUsage200JSONResponse(out), nil
}

// TestLLMPrompt sends an admin's test prompt.
func (s *Server) TestLLMPrompt(ctx context.Context, req TestLLMPromptRequestObject) (TestLLMPromptResponseObject, error) {
	if s.llm == nil {
		p := noLLM(ctx)
		return TestLLMPromptdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	provider := ""
	if req.Body.Provider != nil {
		provider = *req.Body.Provider
	}
	resp, err := s.llm.TestPrompt(ctx, provider, req.Body.Prompt)
	if errors.Is(err, llm.ErrUnavailable) || errors.Is(err, llm.ErrBadOutput) {
		// Admins see why: provider names and their error messages, no secrets.
		p := problemCtx(ctx, http.StatusServiceUnavailable, CodeLLMUnavailable, err.Error())
		return TestLLMPromptdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return TestLLMPrompt200JSONResponse(LLMTestResult{
		Text:      resp.Text,
		Provider:  resp.Provider,
		Model:     resp.Model,
		TokensIn:  resp.Usage.InputTokens,
		TokensOut: resp.Usage.OutputTokens,
		LatencyMs: resp.Latency.Milliseconds(),
	}), nil
}

// SetLLMProviderKey stores a provider's API key.
func (s *Server) SetLLMProviderKey(ctx context.Context, req SetLLMProviderKeyRequestObject) (SetLLMProviderKeyResponseObject, error) {
	p, err := s.setLLMKey(ctx, req.Id, req.Body.ApiKey)
	if p != nil {
		return SetLLMProviderKeydefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return SetLLMProviderKey204Response{}, nil
}

// DeleteLLMProviderKey removes a provider's API key.
func (s *Server) DeleteLLMProviderKey(ctx context.Context, req DeleteLLMProviderKeyRequestObject) (DeleteLLMProviderKeyResponseObject, error) {
	p, err := s.setLLMKey(ctx, req.Id, "")
	if p != nil {
		return DeleteLLMProviderKeydefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return DeleteLLMProviderKey204Response{}, nil
}

// setLLMKey stores or (key == "") removes a key. It returns the problem to
// answer with for expected failures, or an unexpected error.
func (s *Server) setLLMKey(ctx context.Context, id, key string) (*Problem, error) {
	if s.llm == nil {
		p := noLLM(ctx)
		return &p, nil
	}
	err := s.llm.SetAPIKey(ctx, id, key)
	if errors.Is(err, llm.ErrUnknownProvider) {
		p := problemCtx(ctx, http.StatusNotFound, CodeNotFound, "No LLM provider with that id.")
		return &p, nil
	}
	return nil, err
}

// dateOf is t's calendar date (in t's own zone).
func dateOf(t time.Time) openapi_types.Date {
	return openapi_types.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)}
}
