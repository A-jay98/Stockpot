-- name: DeleteAppLogBefore :execrows
-- Bounded batches keep each delete's locks and WAL small.
DELETE FROM app_log
WHERE id IN (
    SELECT l.id FROM app_log AS l
    WHERE l.time < sqlc.arg(before)::timestamptz
    ORDER BY l.id
    LIMIT sqlc.arg(batch_size)::int
);

-- name: ListAppLog :many
-- Newest first. before_id pages back (keyset), after_id fetches what arrived
-- since the newest line the client has (live tail). pattern is an ILIKE
-- pattern matched against the message and the attributes.
SELECT id, time, level, msg, attrs, request_id, user_id
FROM app_log
WHERE level >= sqlc.arg(min_level)::smallint
  AND (sqlc.narg(before_id)::bigint IS NULL OR id < sqlc.narg(before_id)::bigint)
  AND (sqlc.narg(after_id)::bigint IS NULL OR id > sqlc.narg(after_id)::bigint)
  AND (sqlc.narg(since)::timestamptz IS NULL OR time >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(until)::timestamptz IS NULL OR time < sqlc.narg(until)::timestamptz)
  AND (sqlc.narg(request_id)::text IS NULL OR request_id = sqlc.narg(request_id)::text)
  AND (sqlc.narg(pattern)::text IS NULL
       OR msg ILIKE sqlc.narg(pattern)::text
       OR attrs::text ILIKE sqlc.narg(pattern)::text)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit)::int;

-- name: CountAppLogProblemsSince :one
SELECT
    count(*) FILTER (WHERE level >= 8)::bigint AS errors,
    count(*) FILTER (WHERE level >= 4 AND level < 8)::bigint AS warnings
FROM app_log
WHERE time >= sqlc.arg(since)::timestamptz AND level >= 4;
