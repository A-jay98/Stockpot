-- Users -----------------------------------------------------------------

-- name: CreateUser :one
INSERT INTO app_user (id, display_name, status, system_role, approved_at, approved_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetUser :one
SELECT * FROM app_user WHERE id = $1;

-- name: GetUserForUpdate :one
SELECT * FROM app_user WHERE id = $1 FOR UPDATE;

-- name: ListUsers :many
-- Newest first; status filters when given.
SELECT * FROM app_user
WHERE sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text
ORDER BY created_at DESC, id DESC;

-- name: SetUserStatus :one
UPDATE app_user
SET status = $2,
    approved_at = COALESCE(sqlc.narg(approved_at)::timestamptz, approved_at),
    approved_by = COALESCE(sqlc.narg(approved_by)::uuid, approved_by),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetUserSystemRole :one
UPDATE app_user SET system_role = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetUserDisplayName :one
UPDATE app_user SET display_name = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: TouchUserLogin :exec
UPDATE app_user SET last_login_at = now() WHERE id = $1;

-- name: AdminExists :one
SELECT EXISTS (SELECT 1 FROM app_user WHERE system_role = 'admin');

-- name: CountActiveAdmins :one
SELECT count(*) FROM app_user WHERE system_role = 'admin' AND status = 'active';

-- Identity links ----------------------------------------------------------

-- name: CreateIdentityLink :exec
INSERT INTO identity_link (id, user_id, provider, subject, data)
VALUES ($1, $2, $3, $4, $5);

-- name: GetIdentityLink :one
SELECT * FROM identity_link WHERE provider = $1 AND subject = $2;

-- name: ListIdentityLinks :many
SELECT * FROM identity_link WHERE user_id = $1 ORDER BY created_at;

-- name: TouchIdentityLink :exec
UPDATE identity_link SET last_used_at = now() WHERE id = $1;

-- Passkey provider ----------------------------------------------------------

-- name: InsertWebauthnCredential :exec
INSERT INTO webauthn_credential (
    id, user_handle, name, public_key, attestation_type, aaguid, sign_count,
    transports, user_present, user_verified, backup_eligible, backup_state
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ListWebauthnCredentials :many
SELECT * FROM webauthn_credential WHERE user_handle = $1 ORDER BY created_at;

-- name: UpdateWebauthnCredentialUse :exec
UPDATE webauthn_credential
SET sign_count = $2, backup_state = $3, user_verified = $4, last_used_at = now()
WHERE id = $1;

-- name: DeleteWebauthnCredential :execrows
DELETE FROM webauthn_credential WHERE id = $1 AND user_handle = $2;

-- name: CountWebauthnCredentials :one
SELECT count(*) FROM webauthn_credential WHERE user_handle = $1;

-- Ceremonies ---------------------------------------------------------------

-- name: InsertCeremony :exec
INSERT INTO auth_ceremony (id, purpose, provider, state, context, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: TakeCeremony :one
-- Single use: the row is gone whether or not the ceremony then succeeds.
DELETE FROM auth_ceremony
WHERE id = $1 AND purpose = $2
RETURNING *;

-- name: PruneExpiredCeremonies :execrows
-- Called on every new ceremony, so the table stays bounded by the rate of
-- ceremonies times their lifetime without a separate job.
DELETE FROM auth_ceremony
WHERE id IN (
    SELECT c.id FROM auth_ceremony c
    WHERE c.expires_at < now()
    LIMIT 20
);

-- Households -----------------------------------------------------------------

-- name: CreateHousehold :one
INSERT INTO household (id, name, created_by) VALUES ($1, $2, $3)
RETURNING *;

-- name: GetHousehold :one
SELECT * FROM household WHERE id = $1;

-- name: ListHouseholds :many
SELECT h.*, (SELECT count(*) FROM membership m WHERE m.household_id = h.id)::int AS member_count
FROM household h
ORDER BY h.name, h.id;

-- name: AddMembership :exec
INSERT INTO membership (user_id, household_id, role) VALUES ($1, $2, $3)
ON CONFLICT (user_id, household_id) DO NOTHING;

-- name: GetMembership :one
SELECT * FROM membership WHERE user_id = $1 AND household_id = $2;

-- name: ListMembershipsForUser :many
SELECT m.household_id, m.role, h.name AS household_name
FROM membership m JOIN household h ON h.id = m.household_id
WHERE m.user_id = $1
ORDER BY m.created_at, h.id;

-- name: ListMembershipsForUsers :many
SELECT m.user_id, m.household_id, m.role, h.name AS household_name
FROM membership m JOIN household h ON h.id = m.household_id
WHERE m.user_id = ANY(sqlc.arg(user_ids)::uuid[])
ORDER BY m.created_at, h.id;

-- Sessions ---------------------------------------------------------------------

-- name: CreateSession :exec
INSERT INTO session (id, token_hash, user_id, household_id, provider, expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetSessionByTokenHash :one
SELECT sqlc.embed(s), sqlc.embed(u)
FROM session s JOIN app_user u ON u.id = s.user_id
WHERE s.token_hash = $1;

-- name: GetSession :one
SELECT * FROM session WHERE id = $1;

-- name: TouchSession :exec
UPDATE session SET last_seen_at = $2, expires_at = $3, ip = $4
WHERE id = $1 AND revoked_at IS NULL;

-- name: SetSessionHousehold :exec
UPDATE session SET household_id = $2 WHERE id = $1;

-- name: ListActiveSessionsForUser :many
SELECT * FROM session
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: RevokeSession :one
UPDATE session SET revoked_at = now()
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeSessionsForUser :execrows
UPDATE session SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now();

-- name: DeleteSessionsBefore :execrows
-- Expired or revoked before the cutoff.
DELETE FROM session
WHERE id IN (
    SELECT s.id FROM session s
    WHERE s.expires_at < sqlc.arg(before)::timestamptz
       OR s.revoked_at < sqlc.arg(before)::timestamptz
    LIMIT sqlc.arg(batch_size)::int
);

-- Invites -------------------------------------------------------------------------

-- name: CreateInvite :one
INSERT INTO invite (id, code_hash, household_id, role, note, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetInviteByCodeHash :one
SELECT i.*, h.name AS household_name
FROM invite i JOIN household h ON h.id = i.household_id
WHERE i.code_hash = $1;

-- name: LockInvite :one
SELECT * FROM invite WHERE id = $1 FOR UPDATE;

-- name: GetInvite :one
SELECT * FROM invite WHERE id = $1;

-- name: AcceptInvite :exec
UPDATE invite SET accepted_at = now(), accepted_by = $2 WHERE id = $1;

-- name: RevokeInvite :one
UPDATE invite SET revoked_at = now()
WHERE id = $1 AND revoked_at IS NULL AND accepted_at IS NULL
RETURNING *;

-- name: ListInvites :many
-- household_id NULL lists every household's invites (admin).
SELECT i.*, h.name AS household_name, u.display_name AS created_by_name
FROM invite i
JOIN household h ON h.id = i.household_id
JOIN app_user u ON u.id = i.created_by
WHERE sqlc.narg(household_id)::uuid IS NULL OR i.household_id = sqlc.narg(household_id)::uuid
ORDER BY i.created_at DESC, i.id DESC
LIMIT 200;

-- name: DeleteInvitesBefore :execrows
-- Expired before the cutoff; accepted invites stay for the record.
DELETE FROM invite
WHERE id IN (
    SELECT i.id FROM invite i
    WHERE i.expires_at < sqlc.arg(before)::timestamptz AND i.accepted_at IS NULL
    LIMIT sqlc.arg(batch_size)::int
);
