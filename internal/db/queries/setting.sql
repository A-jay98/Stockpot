-- name: ListSystemSettings :many
SELECT key, value, secret, updated_at, updated_by
FROM setting
WHERE household_id IS NULL;

-- name: GetSystemSetting :one
SELECT key, value, secret, updated_at, updated_by
FROM setting
WHERE household_id IS NULL AND key = $1;

-- name: UpsertSystemSetting :exec
INSERT INTO setting (key, value, secret, updated_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (key, household_id) DO UPDATE
SET value = EXCLUDED.value,
    secret = EXCLUDED.secret,
    updated_at = now(),
    updated_by = EXCLUDED.updated_by;

-- name: DeleteSystemSetting :exec
DELETE FROM setting
WHERE household_id IS NULL AND key = $1;

-- name: LockSettingKey :exec
-- Serializes writers of one key, including a first write, which has no row
-- for FOR UPDATE to lock.
SELECT pg_advisory_xact_lock(hashtextextended('setting:' || sqlc.arg(key)::text, 0));

-- name: LockSystemSetting :one
-- Reads the current row inside a write transaction, so the audit trail's
-- "before" matches what the write replaces.
SELECT key, value, secret, updated_at, updated_by
FROM setting
WHERE household_id IS NULL AND key = $1
FOR UPDATE;
