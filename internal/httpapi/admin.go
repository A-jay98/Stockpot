package httpapi

// Admin panel handlers (/api/v1/admin/*). Authorization (x-roles: [admin])
// is enforced by the middleware before these run.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/a-jay98/stockpot/internal/admin"
	"github.com/a-jay98/stockpot/internal/settings"
)

// adminError maps an admin service error to a problem. Unexpected errors
// return ok=false and go to the strict handler's 500 path.
func adminError(ctx context.Context, err error) (Problem, bool) {
	var verr *settings.ValidationError
	switch {
	case errors.As(err, &verr):
		return problemCtx(ctx, http.StatusUnprocessableEntity, CodeInvalidValue, verr.Error()), true
	case errors.Is(err, admin.ErrNotFound):
		return problemCtx(ctx, http.StatusNotFound, CodeNotFound, ""), true
	case errors.Is(err, admin.ErrNotChanged):
		return problemCtx(ctx, http.StatusConflict, CodeNotChanged, "Nothing to do in its current state."), true
	case errors.Is(err, admin.ErrBadCursor):
		return problemCtx(ctx, http.StatusBadRequest, CodeBadRequest, "Invalid cursor; start from the first page."), true
	}
	return Problem{}, false
}

// GetAdminOverview implements StrictServerInterface.
func (s *Server) GetAdminOverview(ctx context.Context, _ GetAdminOverviewRequestObject) (GetAdminOverviewResponseObject, error) {
	o := s.admin.Overview(ctx)
	out := AdminOverview{
		Version:       o.Build.Version,
		Commit:        o.Build.Commit,
		BuiltAt:       o.Build.BuiltAt,
		StartedAt:     o.StartedAt.UTC(),
		UptimeSeconds: int64(o.Uptime / time.Second),
		Database: AdminDatabaseStatus{
			Reachable:     o.Database.Reachable,
			LatencyMs:     float32(o.Database.Latency.Seconds() * 1000),
			SizeBytes:     o.Database.SizeBytes,
			SchemaCurrent: o.Database.SchemaCurrent,
		},
		Errors24h:   o.Errors24h,
		Warnings24h: o.Warnings24h,
	}
	if v := o.Database.Versions; v != nil {
		out.Database.AppVersion, out.Database.AppTarget = &v.App, &v.AppTarget
		out.Database.RiverVersion, out.Database.RiverTarget = &v.River, &v.RiverTarget
	}
	if o.Jobs != nil {
		out.Jobs = &o.Jobs
	}
	if o.LogSink != nil {
		out.LogSink = &struct {
			Dropped int64 `json:"dropped"`
			Written int64 `json:"written"`
		}{Dropped: int64(o.LogSink.Dropped), Written: int64(o.LogSink.Written)} //nolint:gosec // counters
	}
	if o.LLM != nil {
		out.Llm = &AdminLLMToday{Requests: o.LLM.Requests, Tokens: o.LLM.Tokens, RequestCap: o.LLM.RequestCap, Available: o.LLM.Available}
	}
	return GetAdminOverview200JSONResponse(out), nil
}

var logLevels = map[AdminLogLevel]slog.Level{
	AdminLogLevelDebug: slog.LevelDebug,
	AdminLogLevelInfo:  slog.LevelInfo,
	AdminLogLevelWarn:  slog.LevelWarn,
	AdminLogLevelError: slog.LevelError,
}

func levelName(l slog.Level) AdminLogLevel {
	switch {
	case l < slog.LevelInfo:
		return AdminLogLevelDebug
	case l < slog.LevelWarn:
		return AdminLogLevelInfo
	case l < slog.LevelError:
		return AdminLogLevelWarn
	}
	return AdminLogLevelError
}

// ListAdminLogs implements StrictServerInterface.
func (s *Server) ListAdminLogs(ctx context.Context, req ListAdminLogsRequestObject) (ListAdminLogsResponseObject, error) {
	p := req.Params
	q := admin.LogQuery{
		MinLevel: slog.LevelDebug,
		Text:     deref(p.Q),
		Since:    p.Since,
		Until:    p.Until,
		AfterID:  p.After,
		Cursor:   deref(p.Cursor),
		Limit:    deref(p.Limit),
	}
	if p.Level != nil {
		q.MinLevel = logLevels[*p.Level]
	}
	if p.RequestId != nil {
		q.RequestID = *p.RequestId
	}
	page, err := s.admin.Logs(ctx, q)
	if err != nil {
		if prob, ok := adminError(ctx, err); ok {
			return ListAdminLogsdefaultApplicationProblemPlusJSONResponse{Body: prob, StatusCode: prob.Status}, nil
		}
		return nil, err
	}
	out := AdminLogPage{Items: make([]AdminLogEntry, len(page.Entries)), NextCursor: optional(page.NextCursor)}
	for i, e := range page.Entries {
		attrs := map[string]any{}
		if len(e.Attrs) > 0 {
			if err := json.Unmarshal(e.Attrs, &attrs); err != nil {
				attrs = map[string]any{"_raw": string(e.Attrs)}
			}
		}
		out.Items[i] = AdminLogEntry{
			Id:        e.ID,
			Time:      e.Time.UTC(),
			Level:     levelName(e.Level),
			Msg:       e.Msg,
			Attrs:     attrs,
			RequestId: optional(e.RequestID),
			UserId:    e.UserID,
		}
	}
	return ListAdminLogs200JSONResponse(out), nil
}

// ListAdminJobs implements StrictServerInterface.
func (s *Server) ListAdminJobs(ctx context.Context, req ListAdminJobsRequestObject) (ListAdminJobsResponseObject, error) {
	p := req.Params
	q := admin.JobQuery{Cursor: deref(p.Cursor), Limit: deref(p.Limit)}
	if p.State != nil {
		for _, st := range *p.State {
			q.States = append(q.States, rivertype.JobState(st))
		}
	}
	if p.Kind != nil {
		q.Kinds = *p.Kind
	}
	page, err := s.admin.Jobs(ctx, q)
	if err != nil {
		if prob, ok := adminError(ctx, err); ok {
			return ListAdminJobsdefaultApplicationProblemPlusJSONResponse{Body: prob, StatusCode: prob.Status}, nil
		}
		return nil, err
	}
	out := AdminJobPage{Items: make([]AdminJob, len(page.Jobs)), NextCursor: optional(page.NextCursor)}
	for i, j := range page.Jobs {
		out.Items[i] = jobOut(j)
	}
	return ListAdminJobs200JSONResponse(out), nil
}

// RetryAdminJob implements StrictServerInterface.
func (s *Server) RetryAdminJob(ctx context.Context, req RetryAdminJobRequestObject) (RetryAdminJobResponseObject, error) {
	j, err := s.admin.RetryJob(ctx, req.Id)
	if err != nil {
		if prob, ok := adminError(ctx, err); ok {
			return RetryAdminJobdefaultApplicationProblemPlusJSONResponse{Body: prob, StatusCode: prob.Status}, nil
		}
		return nil, err
	}
	return RetryAdminJob200JSONResponse(jobOut(j)), nil
}

// CancelAdminJob implements StrictServerInterface.
func (s *Server) CancelAdminJob(ctx context.Context, req CancelAdminJobRequestObject) (CancelAdminJobResponseObject, error) {
	j, err := s.admin.CancelJob(ctx, req.Id)
	if err != nil {
		if prob, ok := adminError(ctx, err); ok {
			return CancelAdminJobdefaultApplicationProblemPlusJSONResponse{Body: prob, StatusCode: prob.Status}, nil
		}
		return nil, err
	}
	return CancelAdminJob200JSONResponse(jobOut(j)), nil
}

func jobOut(j *rivertype.JobRow) AdminJob {
	out := AdminJob{
		Id:          j.ID,
		Kind:        j.Kind,
		State:       AdminJobState(j.State),
		Queue:       j.Queue,
		Attempt:     j.Attempt,
		MaxAttempts: j.MaxAttempts,
		Priority:    j.Priority,
		Args:        json.RawMessage(j.EncodedArgs),
		Errors:      make([]AdminJobError, len(j.Errors)),
		CreatedAt:   j.CreatedAt.UTC(),
		ScheduledAt: j.ScheduledAt.UTC(),
		AttemptedAt: utc(j.AttemptedAt),
		FinalizedAt: utc(j.FinalizedAt),
		Tags:        j.Tags,
	}
	if len(j.EncodedArgs) == 0 {
		out.Args = map[string]any{}
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	for i, e := range j.Errors {
		out.Errors[i] = AdminJobError{At: e.At.UTC(), Attempt: e.Attempt, Error: e.Error}
	}
	return out
}

// ListAdminSettings implements StrictServerInterface.
func (s *Server) ListAdminSettings(context.Context, ListAdminSettingsRequestObject) (ListAdminSettingsResponseObject, error) {
	views := s.admin.Settings()
	out := AdminSettingList{Items: make([]AdminSetting, len(views))}
	for i, v := range views {
		out.Items[i] = settingOut(v)
	}
	return ListAdminSettings200JSONResponse(out), nil
}

// PutAdminSetting implements StrictServerInterface.
func (s *Server) PutAdminSetting(ctx context.Context, req PutAdminSettingRequestObject) (PutAdminSettingResponseObject, error) {
	problem := func(prob Problem) (PutAdminSettingResponseObject, error) {
		return PutAdminSettingdefaultApplicationProblemPlusJSONResponse{Body: prob, StatusCode: prob.Status}, nil
	}
	if req.Body == nil || req.Body.Value == nil {
		// JSON null would decode as the zero value; reset is DELETE.
		return problem(problemCtx(ctx, http.StatusUnprocessableEntity, CodeInvalidValue, "A value is required; reset the setting to use its default."))
	}
	raw, err := json.Marshal(req.Body.Value)
	if err != nil {
		return nil, err
	}
	v, err := s.admin.SetSetting(ctx, req.Key, raw)
	if err != nil {
		if prob, ok := adminError(ctx, err); ok {
			return problem(prob)
		}
		return nil, err
	}
	return PutAdminSetting200JSONResponse(settingOut(v)), nil
}

// ResetAdminSetting implements StrictServerInterface.
func (s *Server) ResetAdminSetting(ctx context.Context, req ResetAdminSettingRequestObject) (ResetAdminSettingResponseObject, error) {
	v, err := s.admin.ResetSetting(ctx, req.Key)
	if err != nil {
		if prob, ok := adminError(ctx, err); ok {
			return ResetAdminSettingdefaultApplicationProblemPlusJSONResponse{Body: prob, StatusCode: prob.Status}, nil
		}
		return nil, err
	}
	return ResetAdminSetting200JSONResponse(settingOut(v)), nil
}

func settingOut(v settings.View) AdminSetting {
	out := AdminSetting{
		Key:         v.Key,
		Kind:        AdminSettingKind(v.Kind),
		Scope:       AdminSettingScope(v.Scope),
		Description: v.Description,
		Secret:      v.Secret,
		IsSet:       v.IsSet,
		UpdatedBy:   v.UpdatedBy,
	}
	if !v.Secret {
		out.Default, out.Value = v.Default, v.Value
		if len(v.Options) > 0 {
			opts := v.Options
			out.Options = &opts
		}
	}
	if v.IsSet {
		t := v.UpdatedAt.UTC()
		out.UpdatedAt = &t
	}
	return out
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
