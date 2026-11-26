package admin

// Telemetry views: the audit trail (who changed what) and the access log
// (every API request). Both are written by internal/audit.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// AuditQuery filters audit events.
type AuditQuery struct {
	ActorID *uuid.UUID
	// Action matches exactly or as a dotted prefix ("setting" ⊃ "setting.update").
	Action     string
	TargetType string
	TargetID   string
	Since      *time.Time
	Until      *time.Time
	Cursor     string
	Limit      int
}

// AuditEvent is one audit_event row with the actor's display name.
type AuditEvent struct {
	ID          int64
	Time        time.Time
	ActorID     *uuid.UUID
	ActorName   string
	HouseholdID *uuid.UUID
	Action      string
	Target      Target
	Before      json.RawMessage
	After       json.RawMessage
	RequestID   string
	SessionID   *uuid.UUID
	IP          string
}

// AuditPage is a page of events, newest first.
type AuditPage struct {
	Events     []AuditEvent
	NextCursor string
}

// AuditEvents lists audit events, newest first.
func (s *Service) AuditEvents(ctx context.Context, q AuditQuery) (AuditPage, error) {
	limit := clampLimit(q.Limit)
	p := dbq.ListAuditEventsParams{
		ActorID:    q.ActorID,
		Action:     nonEmpty(q.Action),
		TargetType: nonEmpty(q.TargetType),
		TargetID:   nonEmpty(q.TargetID),
		Since:      q.Since,
		Until:      q.Until,
		RowLimit:   int32(limit) + 1, //nolint:gosec // clamped
	}
	var err error
	if p.BeforeID, err = cursorID(q.Cursor); err != nil {
		return AuditPage{}, err
	}
	rows, err := s.q.ListAuditEvents(ctx, p)
	if err != nil {
		return AuditPage{}, err
	}
	var page AuditPage
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextCursor = encodeID(rows[limit-1].ID)
	}
	page.Events = make([]AuditEvent, len(rows))
	for i, r := range rows {
		page.Events[i] = AuditEvent{
			ID: r.ID, Time: r.Time, ActorID: r.ActorID, ActorName: deref(r.ActorName),
			HouseholdID: r.HouseholdID, Action: r.Action,
			Target: Target{Type: r.TargetType, ID: r.TargetID},
			Before: r.Before, After: r.After, RequestID: deref(r.RequestID),
			SessionID: r.SessionID, IP: r.Ip,
		}
	}
	return page, nil
}

// AccessQuery filters the access log.
type AccessQuery struct {
	UserID *uuid.UUID
	Method string
	// Route is a case-insensitive substring of the route pattern.
	Route                string
	MinStatus, MaxStatus *int
	Since, Until         *time.Time
	Cursor               string
	Limit                int
}

// AccessRecord is one access_log row with the user's display name.
type AccessRecord struct {
	ID          int64
	Time        time.Time
	RequestID   string
	UserID      *uuid.UUID
	UserName    string
	SessionID   *uuid.UUID
	HouseholdID *uuid.UUID
	Method      string
	Route       string
	Path        string
	Status      int
	Duration    time.Duration
	IP          string
	UserAgent   string
}

// AccessPage is a page of requests, newest first.
type AccessPage struct {
	Records    []AccessRecord
	NextCursor string
}

// AccessLog lists served requests, newest first.
func (s *Service) AccessLog(ctx context.Context, q AccessQuery) (AccessPage, error) {
	limit := clampLimit(q.Limit)
	p := dbq.ListAccessLogParams{
		UserID:    q.UserID,
		Method:    nonEmpty(q.Method),
		MinStatus: int32Ptr(q.MinStatus),
		MaxStatus: int32Ptr(q.MaxStatus),
		Since:     q.Since,
		Until:     q.Until,
		RowLimit:  int32(limit) + 1, //nolint:gosec // clamped
	}
	if q.Route != "" {
		pat := "%" + likeEscaper.Replace(q.Route) + "%"
		p.Route = &pat
	}
	var err error
	if p.BeforeID, err = cursorID(q.Cursor); err != nil {
		return AccessPage{}, err
	}
	rows, err := s.q.ListAccessLog(ctx, p)
	if err != nil {
		return AccessPage{}, err
	}
	var page AccessPage
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextCursor = encodeID(rows[limit-1].ID)
	}
	page.Records = make([]AccessRecord, len(rows))
	for i, r := range rows {
		page.Records[i] = AccessRecord{
			ID: r.ID, Time: r.Time, RequestID: r.RequestID, UserID: r.UserID, UserName: deref(r.UserName),
			SessionID: r.SessionID, HouseholdID: r.HouseholdID, Method: r.Method, Route: r.Route,
			Path: r.Path, Status: int(r.Status), Duration: time.Duration(r.DurationMs) * time.Millisecond,
			IP: r.Ip, UserAgent: r.UserAgent,
		}
	}
	return page, nil
}

func cursorID(cursor string) (*int64, error) {
	if cursor == "" {
		return nil, nil //nolint:nilnil // no cursor: first page
	}
	id, err := decodeID(cursor)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func int32Ptr(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p) //nolint:gosec // HTTP status, validated by the API
	return &v
}
