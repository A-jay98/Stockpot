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
