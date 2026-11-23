-- name: DeleteAppLogBefore :execrows
-- Bounded batches keep each delete's locks and WAL small.
DELETE FROM app_log
WHERE id IN (
    SELECT l.id FROM app_log AS l
    WHERE l.time < sqlc.arg(before)::timestamptz
    ORDER BY l.id
    LIMIT sqlc.arg(batch_size)::int
);
