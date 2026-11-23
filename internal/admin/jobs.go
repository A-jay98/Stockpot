package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// JobQuery filters background jobs.
type JobQuery struct {
	States []rivertype.JobState
	Kinds  []string
	Cursor string
	Limit  int
}

// JobPage is a page of jobs, newest first. NextCursor is empty on the last page.
type JobPage struct {
	Jobs       []*rivertype.JobRow
	NextCursor string
}

// Jobs lists River jobs, newest first.
func (s *Service) Jobs(ctx context.Context, q JobQuery) (JobPage, error) {
	limit := clampLimit(q.Limit)
	p := river.NewJobListParams().First(limit).OrderBy(river.JobListOrderByID, river.SortOrderDesc)
	if len(q.States) > 0 {
		p = p.States(q.States...)
	}
	if len(q.Kinds) > 0 {
		p = p.Kinds(q.Kinds...)
	}
	if q.Cursor != "" {
		var c river.JobListCursor
		if err := c.UnmarshalText([]byte(q.Cursor)); err != nil {
			return JobPage{}, ErrBadCursor
		}
		p = p.After(&c)
	}
	res, err := s.d.Jobs.JobList(ctx, p)
	if err != nil {
		return JobPage{}, fmt.Errorf("list jobs: %w", err)
	}
	page := JobPage{Jobs: res.Jobs}
	if len(res.Jobs) == limit && res.LastCursor != nil {
		b, err := res.LastCursor.MarshalText()
		if err != nil {
			return JobPage{}, err
		}
		page.NextCursor = string(b)
	}
	return page, nil
}

// RetryJob makes a finished or scheduled job available to run now. A
// running job is left alone.
func (s *Service) RetryJob(ctx context.Context, id int64) (*rivertype.JobRow, error) {
	return s.changeJob(ctx, id, "job.retry", s.d.Jobs.JobRetryTx)
}

// CancelJob cancels a job; a running one is told to stop.
func (s *Service) CancelJob(ctx context.Context, id int64) (*rivertype.JobRow, error) {
	return s.changeJob(ctx, id, "job.cancel", s.d.Jobs.JobCancelTx)
}

type jobChange func(ctx context.Context, tx pgx.Tx, id int64) (*rivertype.JobRow, error)

func (s *Service) changeJob(ctx context.Context, id int64, action string, change jobChange) (*rivertype.JobRow, error) {
	var after *rivertype.JobRow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		before, err := s.d.Jobs.JobGetTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if after, err = change(ctx, tx, id); err != nil {
			return err
		}
		return s.d.Audit(ctx, tx, action, Target{Type: "job", ID: strconv.FormatInt(id, 10)},
			jobSnapshot(before), jobSnapshot(after))
	})
	if errors.Is(err, rivertype.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%s %d: %w", action, id, err)
	}
	return after, nil
}

// jobSnapshot is what the audit trail keeps of a job (its args may be large).
func jobSnapshot(j *rivertype.JobRow) map[string]any {
	return map[string]any{"kind": j.Kind, "state": j.State, "attempt": j.Attempt, "scheduled_at": j.ScheduledAt}
}
