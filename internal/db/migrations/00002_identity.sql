-- Identity, households, sessions, invites, audit and access log.
-- Additive only: the previous release keeps working against this schema.

-- +goose Up

-- A person using Stockpot. Credentials live with their identity provider
-- (webauthn_credential for passkeys); identity_link maps them to the user.
CREATE TABLE app_user (
    id            uuid        PRIMARY KEY,
    display_name  text        NOT NULL CHECK (length(display_name) BETWEEN 1 AND 80),
    status        text        NOT NULL CHECK (status IN ('pending', 'active', 'disabled')),
    system_role   text        NOT NULL DEFAULT 'user' CHECK (system_role IN ('admin', 'user')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    approved_at   timestamptz,
    approved_by   uuid        REFERENCES app_user (id),
    last_login_at timestamptz
);
CREATE INDEX app_user_status ON app_user (status);

-- (provider, subject) is a verified identity from an identity.Provider:
-- passkey user handle today, OIDC/LDAP subjects later. A user may link several.
CREATE TABLE identity_link (
    id           uuid        PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    subject      text        NOT NULL,
    data         jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    CONSTRAINT identity_link_subject UNIQUE (provider, subject)
);
CREATE INDEX identity_link_user ON identity_link (user_id);

-- WebAuthn credentials, owned by the passkey provider and keyed by its user
-- handle (identity_link.subject for provider 'passkey').
CREATE TABLE webauthn_credential (
    id               bytea       PRIMARY KEY,
    user_handle      bytea       NOT NULL,
    name             text        NOT NULL DEFAULT '',
    public_key       bytea       NOT NULL,
    attestation_type text        NOT NULL DEFAULT '',
    aaguid           uuid,
    sign_count       bigint      NOT NULL DEFAULT 0,
    transports       text[]      NOT NULL DEFAULT '{}',
    user_present     boolean     NOT NULL DEFAULT false,
    user_verified    boolean     NOT NULL DEFAULT false,
    backup_eligible  boolean     NOT NULL DEFAULT false,
    backup_state     boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_used_at     timestamptz
);
CREATE INDEX webauthn_credential_user_handle ON webauthn_credential (user_handle);

-- In-flight login/enrolment ceremonies: the server half of a challenge,
-- consumed (deleted) exactly once by the matching finish call.
CREATE TABLE auth_ceremony (
    id         uuid        PRIMARY KEY,
    purpose    text        NOT NULL,
    provider   text        NOT NULL,
    state      bytea       NOT NULL,
    context    jsonb       NOT NULL DEFAULT '{}',
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_ceremony_expires ON auth_ceremony (expires_at);

CREATE TABLE household (
    id         uuid        PRIMARY KEY,
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid        REFERENCES app_user (id)
);

CREATE TABLE membership (
    user_id      uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    household_id uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    role         text        NOT NULL CHECK (role IN ('owner', 'member')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, household_id)
);
CREATE INDEX membership_household ON membership (household_id);

-- Browser sessions. Only the SHA-256 of the cookie token is stored.
CREATE TABLE session (
    id           uuid        PRIMARY KEY,
    token_hash   bytea       NOT NULL UNIQUE,
    user_id      uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    household_id uuid        REFERENCES household (id) ON DELETE SET NULL,
    provider     text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    ip           inet,
    user_agent   text        NOT NULL DEFAULT ''
);
CREATE INDEX session_by_user ON session (user_id);
CREATE INDEX session_expires ON session (expires_at);

-- Single-use household invites. Only the SHA-256 of the code is stored.
CREATE TABLE invite (
    id           uuid        PRIMARY KEY,
    code_hash    bytea       NOT NULL UNIQUE,
    household_id uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    role         text        NOT NULL CHECK (role IN ('owner', 'member')),
    note         text        NOT NULL DEFAULT '',
    created_by   uuid        NOT NULL REFERENCES app_user (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz,
    accepted_by  uuid        REFERENCES app_user (id),
    revoked_at   timestamptz
);
CREATE INDEX invite_household ON invite (household_id, created_at DESC);
CREATE INDEX invite_expires ON invite (expires_at);

-- One row per API request, written in batches. Retention: retention.access_log_days.
CREATE TABLE access_log (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    time         timestamptz NOT NULL,
    request_id   text        NOT NULL,
    user_id      uuid,
    session_id   uuid,
    household_id uuid,
    method       text        NOT NULL,
    route        text        NOT NULL,
    path         text        NOT NULL,
    status       smallint    NOT NULL,
    duration_ms  integer     NOT NULL,
    ip           inet,
    user_agent   text        NOT NULL DEFAULT ''
);
CREATE INDEX access_log_time ON access_log (time);
CREATE INDEX access_log_user_time ON access_log (user_id, time) WHERE user_id IS NOT NULL;

-- Security events and mutations, written in the same transaction as the
-- change. Retention: retention.audit_days.
CREATE TABLE audit_event (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    time         timestamptz NOT NULL DEFAULT now(),
    actor_id     uuid,
    household_id uuid,
    action       text        NOT NULL CHECK (action ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'),
    target_type  text        NOT NULL DEFAULT '',
    target_id    text        NOT NULL DEFAULT '',
    before       jsonb,
    after        jsonb,
    request_id   text,
    session_id   uuid,
    ip           inet
);
CREATE INDEX audit_event_time ON audit_event (time);
CREATE INDEX audit_event_actor_time ON audit_event (actor_id, time) WHERE actor_id IS NOT NULL;
CREATE INDEX audit_event_action_time ON audit_event (action, time);
CREATE INDEX audit_event_target ON audit_event (target_type, target_id);

-- +goose Down
DROP TABLE audit_event;
DROP TABLE access_log;
DROP TABLE invite;
DROP TABLE session;
DROP TABLE membership;
DROP TABLE household;
DROP TABLE auth_ceremony;
DROP TABLE webauthn_credential;
DROP TABLE identity_link;
DROP TABLE app_user;
