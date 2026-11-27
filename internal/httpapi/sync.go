package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/syncdb"
)

// Syncer is what the sync endpoints need from the sync engine.
type Syncer interface {
	Pull(ctx context.Context, household *uuid.UUID, since int64, limit int) (syncdb.Page, error)
	Listen(ctx context.Context, household uuid.UUID) (<-chan struct{}, func(), error)
}

// CommandApplier applies command batches.
type CommandApplier interface {
	Apply(ctx context.Context, cmds []commands.Command) ([]commands.Result, error)
}

// sseHeartbeat keeps proxies from closing an idle event stream.
const sseHeartbeat = 25 * time.Second

// ApplyCommands applies a batch of offline commands.
func (s *Server) ApplyCommands(ctx context.Context, req ApplyCommandsRequestObject) (ApplyCommandsResponseObject, error) {
	if s.commands == nil {
		return nil, errNotWired
	}
	cmds := make([]commands.Command, 0, len(req.Body.Commands))
	for _, c := range req.Body.Commands {
		raw, err := c.MarshalJSON()
		if err != nil {
			return nil, err
		}
		var cmd commands.Command
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return nil, err
		}
		cmds = append(cmds, cmd)
	}
	results, err := s.commands.Apply(ctx, cmds)
	if err != nil {
		return nil, err
	}
	out := CommandResults{Results: make([]CommandResult, 0, len(results))}
	for _, r := range results {
		cr := CommandResult{Id: r.ID, Status: CommandResultStatus(r.Status)}
		if r.Problem != nil {
			cr.Problem = &struct {
				Code   CommandResultProblemCode `json:"code"`
				Detail string                   `json:"detail"`
			}{CommandResultProblemCode(r.Problem.Code), r.Problem.Detail}
		}
		out.Results = append(out.Results, cr)
	}
	return ApplyCommands200JSONResponse(out), nil
}

// syncPageResponse writes a SyncPage, keeping row JSON as it came from Postgres.
type syncPageResponse struct {
	Rows       map[string][]json.RawMessage `json:"rows"`
	Tombstones map[string][]uuid.UUID       `json:"tombstones"`
	Next       int64                        `json:"next"`
	NextGlobal int64                        `json:"nextGlobal"`
	More       bool                         `json:"more"`
}

func (r syncPageResponse) VisitGetSyncResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(r)
}

// GetSync returns household and global-catalog changes since the cursors.
func (s *Server) GetSync(ctx context.Context, req GetSyncRequestObject) (GetSyncResponseObject, error) {
	if s.sync == nil {
		return nil, errNotWired
	}
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	since, sinceGlobal, limit := int64(0), int64(0), 1000
	if p := req.Params.Since; p != nil {
		since = *p
	}
	if p := req.Params.SinceGlobal; p != nil {
		sinceGlobal = *p
	}
	if p := req.Params.Limit; p != nil {
		limit = *p
	}
	own, err := s.sync.Pull(ctx, &hh, since, limit)
	if err != nil {
		return nil, err
	}
	global, err := s.sync.Pull(ctx, nil, sinceGlobal, limit)
	if err != nil {
		return nil, err
	}
	out := syncPageResponse{Rows: own.Rows, Tombstones: own.Tombstones, Next: own.Next, NextGlobal: global.Next, More: own.More || global.More}
	for t, rows := range global.Rows {
		out.Rows[t] = append(out.Rows[t], rows...)
	}
	for t, ids := range global.Tombstones {
		out.Tombstones[t] = append(out.Tombstones[t], ids...)
	}
	return out, nil
}

// syncEventsResponse streams change nudges until the client goes away.
type syncEventsResponse struct {
	ctx     context.Context
	changes <-chan struct{}
	stop    func()
}

func (r syncEventsResponse) VisitGetSyncEventsResponse(w http.ResponseWriter) error {
	defer r.stop()
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the server's write timeout would cut the stream
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // reverse proxies must not buffer
	w.WriteHeader(http.StatusOK)
	write := func(s string) bool {
		if _, err := w.Write([]byte(s)); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write(": connected\n\n") {
		return nil
	}
	tick := time.NewTicker(sseHeartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return nil
		case _, ok := <-r.changes:
			if !ok || !write("event: changed\ndata: 1\n\n") {
				return nil
			}
		case <-tick.C:
			if !write(": ping\n\n") {
				return nil
			}
		}
	}
}

// GetSyncEvents opens the change-notification stream for the household.
func (s *Server) GetSyncEvents(ctx context.Context, _ GetSyncEventsRequestObject) (GetSyncEventsResponseObject, error) {
	if s.sync == nil {
		return nil, errNotWired
	}
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	changes, stop, err := s.sync.Listen(ctx, hh)
	if err != nil {
		return nil, err
	}
	return syncEventsResponse{ctx: ctx, changes: changes, stop: stop}, nil
}
