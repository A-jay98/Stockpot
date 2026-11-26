package identity

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/jobs"
)

const sweepBatch = 5000

// Sweepers delete sessions and invites that expired (or were revoked) more
// than retention.expired_auth_days ago. Expired ceremonies are pruned as new
// ones start.
func Sweepers(pool *pgxpool.Pool) []jobs.Sweeper {
	q := dbq.New(pool)
	return []jobs.Sweeper{
		{
			Name: "session",
			Days: RetentionExpiredAuthDays,
			Sweep: func(ctx context.Context, before time.Time) (int64, error) {
				return jobs.SweepInBatches(ctx, sweepBatch, func(ctx context.Context) (int64, error) {
					return q.DeleteSessionsBefore(ctx, dbq.DeleteSessionsBeforeParams{Before: before, BatchSize: sweepBatch})
				})
			},
		},
		{
			Name: "invite",
			Days: RetentionExpiredAuthDays,
			Sweep: func(ctx context.Context, before time.Time) (int64, error) {
				return jobs.SweepInBatches(ctx, sweepBatch, func(ctx context.Context) (int64, error) {
					return q.DeleteInvitesBefore(ctx, dbq.DeleteInvitesBeforeParams{Before: before, BatchSize: sweepBatch})
				})
			},
		},
	}
}
