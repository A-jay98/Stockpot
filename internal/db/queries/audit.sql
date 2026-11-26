-- name: InsertAuditEvent :exec
INSERT INTO audit_event (
    actor_id, household_id, action, target_type, target_id, before, after,
    request_id, session_id, ip
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: InsertAccessLog :copyfrom
INSERT INTO access_log (
    time, request_id, user_id, session_id, household_id, method, route, path,
    status, duration_ms, ip, user_agent
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: DeleteAccessLogBefore :execrows
DELETE FROM access_log
WHERE id IN (
    SELECT l.id FROM access_log AS l
    WHERE l.time < sqlc.arg(before)::timestamptz
    ORDER BY l.id
    LIMIT sqlc.arg(batch_size)::int
);

-- name: DeleteAuditEventsBefore :execrows
DELETE FROM audit_event
WHERE id IN (
    SELECT e.id FROM audit_event AS e
    WHERE e.time < sqlc.arg(before)::timestamptz
    ORDER BY e.id
    LIMIT sqlc.arg(batch_size)::int
);
