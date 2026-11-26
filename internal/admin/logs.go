package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// LogQuery filters the application log.
type LogQuery struct {
	MinLevel  slog.Level
	Text      string // substring of the message or attributes, case-insensitive
	RequestID string
	Since     *time.Time
	Until     *time.Time
	// Cursor continues a previous page (older lines).
	Cursor string
	// AfterID returns only lines newer than this id (live tail).
	AfterID *int64
	Limit   int
}

// LogEntry is one app_log line.
type LogEntry struct {
	ID        int64
	Time      time.Time
	Level     slog.Level
	Msg       string
	Attrs     json.RawMessage
	RequestID string
	UserID    *uuid.UUID
}

// LogPage is a page of lines, newest first. NextCursor is empty on the last page.
type LogPage struct {
	Entries    []LogEntry
	NextCursor string
}

// Logs lists application log lines, newest first.
func (s *Service) Logs(ctx context.Context, q LogQuery) (LogPage, error) {
	limit := clampLimit(q.Limit)
	p := dbq.ListAppLogParams{
		MinLevel: int16(q.MinLevel), //nolint:gosec // slog levels are small
		AfterID:  q.AfterID,
		Since:    q.Since,
		Until:    q.Until,
		RowLimit: int32(limit) + 1, //nolint:gosec // clamped
	}
	if q.Cursor != "" {
		id, err := decodeID(q.Cursor)
		if err != nil {
			return LogPage{}, err
		}
		p.BeforeID = &id
	}
	if q.RequestID != "" {
		p.RequestID = &q.RequestID
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		pat := "%" + likeEscaper.Replace(t) + "%"
		p.Pattern = &pat
	}
	rows, err := s.q.ListAppLog(ctx, p)
	if err != nil {
		return LogPage{}, err
	}
	var page LogPage
	if len(rows) > limit {
		rows = rows[:limit]
		// A tail fetch that overflows has a gap; the client reloads instead.
		page.NextCursor = encodeID(rows[limit-1].ID)
	}
	page.Entries = make([]LogEntry, len(rows))
	for i, r := range rows {
		e := LogEntry{ID: r.ID, Time: r.Time, Level: slog.Level(r.Level), Msg: r.Msg, Attrs: r.Attrs, UserID: r.UserID}
		if r.RequestID != nil {
			e.RequestID = *r.RequestID
		}
		page.Entries[i] = e
	}
	return page, nil
}

// likeEscaper makes user text literal inside an ILIKE pattern (backslash is
// Postgres's default LIKE escape).
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
