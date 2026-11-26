package httpapi

// Admin views of the audit trail and the access log.

import (
	"context"
	"encoding/json"

	"github.com/a-jay98/stockpot/internal/admin"
)

// ListAdminAudit implements StrictServerInterface.
func (s *Server) ListAdminAudit(ctx context.Context, req ListAdminAuditRequestObject) (ListAdminAuditResponseObject, error) {
	p := req.Params
	page, err := s.admin.AuditEvents(ctx, admin.AuditQuery{
		ActorID:    p.ActorId,
		Action:     deref(p.Action),
		TargetType: deref(p.TargetType),
		TargetID:   deref(p.TargetId),
		Since:      p.Since,
		Until:      p.Until,
		Cursor:     deref(p.Cursor),
		Limit:      deref(p.Limit),
	})
	if err != nil {
		if prob, ok := adminError(ctx, err); ok {
			return ListAdminAuditdefaultApplicationProblemPlusJSONResponse{Body: prob, StatusCode: prob.Status}, nil
		}
		return nil, err
	}
	out := AdminAuditPage{Items: make([]AdminAuditEvent, len(page.Events)), NextCursor: optional(page.NextCursor)}
	for i, e := range page.Events {
		out.Items[i] = AdminAuditEvent{
			Id:          e.ID,
			Time:        e.Time.UTC(),
			ActorId:     e.ActorID,
			ActorName:   optional(e.ActorName),
			HouseholdId: e.HouseholdID,
			Action:      e.Action,
			TargetType:  e.Target.Type,
			TargetId:    e.Target.ID,
			Before:      rawOrNil(e.Before),
			After:       rawOrNil(e.After),
			RequestId:   optional(e.RequestID),
			SessionId:   e.SessionID,
			Ip:          optional(e.IP),
		}
	}
	return ListAdminAudit200JSONResponse(out), nil
}

// statusClasses maps a status class filter to its inclusive range.
var statusClasses = map[ListAdminAccessParamsStatus][2]int{
	ListAdminAccessParamsStatusN2xx: {200, 299},
	ListAdminAccessParamsStatusN3xx: {300, 399},
	ListAdminAccessParamsStatusN4xx: {400, 499},
	ListAdminAccessParamsStatusN5xx: {500, 599},
}

// ListAdminAccess implements StrictServerInterface.
func (s *Server) ListAdminAccess(ctx context.Context, req ListAdminAccessRequestObject) (ListAdminAccessResponseObject, error) {
	p := req.Params
	q := admin.AccessQuery{
		UserID: p.UserId,
		Route:  deref(p.Route),
		Since:  p.Since,
		Until:  p.Until,
		Cursor: deref(p.Cursor),
		Limit:  deref(p.Limit),
	}
	if p.Method != nil {
		q.Method = string(*p.Method)
	}
	if p.Status != nil {
		r := statusClasses[*p.Status]
		q.MinStatus, q.MaxStatus = &r[0], &r[1]
	}
	page, err := s.admin.AccessLog(ctx, q)
	if err != nil {
		if prob, ok := adminError(ctx, err); ok {
			return ListAdminAccessdefaultApplicationProblemPlusJSONResponse{Body: prob, StatusCode: prob.Status}, nil
		}
		return nil, err
	}
	out := AdminAccessPage{Items: make([]AdminAccessRecord, len(page.Records)), NextCursor: optional(page.NextCursor)}
	for i, r := range page.Records {
		out.Items[i] = AdminAccessRecord{
			Id:          r.ID,
			Time:        r.Time.UTC(),
			RequestId:   r.RequestID,
			UserId:      r.UserID,
			UserName:    optional(r.UserName),
			SessionId:   r.SessionID,
			HouseholdId: r.HouseholdID,
			Method:      r.Method,
			Route:       r.Route,
			Path:        r.Path,
			Status:      r.Status,
			DurationMs:  int(r.Duration.Milliseconds()),
			Ip:          optional(r.IP),
			UserAgent:   r.UserAgent,
		}
	}
	return ListAdminAccess200JSONResponse(out), nil
}

// rawOrNil passes stored JSON through untouched; nil stays absent.
func rawOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}
