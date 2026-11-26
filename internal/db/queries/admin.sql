-- name: DatabaseSize :one
SELECT pg_database_size(current_database())::bigint;

-- name: ListAuditEvents :many
-- Newest first, keyset by id. action matches exactly or as a prefix
-- ("setting" matches "setting.update").
SELECT e.id, e.time, e.actor_id, u.display_name AS actor_name, e.household_id,
       e.action, e.target_type, e.target_id, e.before, e.after, e.request_id,
       e.session_id, COALESCE(host(e.ip), '')::text AS ip
FROM audit_event AS e
LEFT JOIN app_user AS u ON u.id = e.actor_id
WHERE (sqlc.narg(before_id)::bigint IS NULL OR e.id < sqlc.narg(before_id)::bigint)
  AND (sqlc.narg(actor_id)::uuid IS NULL OR e.actor_id = sqlc.narg(actor_id)::uuid)
  AND (sqlc.narg(action)::text IS NULL
       OR e.action = sqlc.narg(action)::text
       OR starts_with(e.action, sqlc.narg(action)::text || '.'))
  AND (sqlc.narg(target_type)::text IS NULL OR e.target_type = sqlc.narg(target_type)::text)
  AND (sqlc.narg(target_id)::text IS NULL OR e.target_id = sqlc.narg(target_id)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.time >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(until)::timestamptz IS NULL OR e.time < sqlc.narg(until)::timestamptz)
ORDER BY e.id DESC
LIMIT sqlc.arg(row_limit)::int;

-- name: ListAccessLog :many
-- Newest first, keyset by id. route is a case-insensitive ILIKE pattern.
SELECT a.id, a.time, a.request_id, a.user_id, u.display_name AS user_name,
       a.session_id, a.household_id, a.method, a.route, a.path, a.status,
       a.duration_ms, COALESCE(host(a.ip), '')::text AS ip, a.user_agent
FROM access_log AS a
LEFT JOIN app_user AS u ON u.id = a.user_id
WHERE (sqlc.narg(before_id)::bigint IS NULL OR a.id < sqlc.narg(before_id)::bigint)
  AND (sqlc.narg(user_id)::uuid IS NULL OR a.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.narg(method)::text IS NULL OR a.method = sqlc.narg(method)::text)
  AND (sqlc.narg(route)::text IS NULL OR a.route ILIKE sqlc.narg(route)::text)
  AND (sqlc.narg(min_status)::int IS NULL OR a.status >= sqlc.narg(min_status)::int)
  AND (sqlc.narg(max_status)::int IS NULL OR a.status <= sqlc.narg(max_status)::int)
  AND (sqlc.narg(since)::timestamptz IS NULL OR a.time >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(until)::timestamptz IS NULL OR a.time < sqlc.narg(until)::timestamptz)
ORDER BY a.id DESC
LIMIT sqlc.arg(row_limit)::int;
