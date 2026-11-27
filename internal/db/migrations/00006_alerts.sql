-- Expiry alerts: in-app notifications, Telegram linking and per-user preferences.
-- Additive only.

-- +goose Up

-- One row per notification shown in the inbox (not synced: plain REST).
-- dedupe_key makes creating the same notification twice a no-op.
CREATE TABLE notification (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    household_id     uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    kind             text        NOT NULL,
    title            text        NOT NULL,
    body             text        NOT NULL,
    payload          jsonb       NOT NULL DEFAULT '{}',
    dedupe_key       text        NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    read_at          timestamptz,
    sent_telegram_at timestamptz,
    CONSTRAINT notification_dedupe UNIQUE (user_id, dedupe_key)
);
CREATE INDEX notification_inbox ON notification (user_id, created_at DESC);
CREATE INDEX notification_unread ON notification (user_id) WHERE read_at IS NULL;
CREATE INDEX notification_unsent ON notification (created_at) WHERE sent_telegram_at IS NULL;

-- A user's alert preferences. NULL means "use the system setting".
CREATE TABLE notification_pref (
    user_id      uuid        PRIMARY KEY REFERENCES app_user (id) ON DELETE CASCADE,
    digest_time  text        CHECK (digest_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    lead_days    int         CHECK (lead_days BETWEEN 0 AND 30),
    quiet_start  text        CHECK (quiet_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    quiet_end    text        CHECK (quiet_end ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Where a user receives notifications besides the app. One row per kind.
CREATE TABLE notification_channel (
    user_id    uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('telegram')),
    chat_id    bigint      NOT NULL,
    username   text,
    enabled    boolean     NOT NULL DEFAULT true,
    last_error text,
    linked_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kind),
    CONSTRAINT notification_channel_chat UNIQUE (kind, chat_id)
);

-- Pending "Connect Telegram" codes; only the hash is stored.
CREATE TABLE telegram_link (
    code_hash  bytea       PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);
CREATE INDEX telegram_link_user ON telegram_link (user_id);
CREATE INDEX telegram_link_expires ON telegram_link (expires_at);

-- The day's digest was evaluated for this user and household (even if there
-- was nothing to say), so it is evaluated once.
CREATE TABLE digest_run (
    user_id         uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    household_id    uuid NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    day             date NOT NULL,
    notification_id uuid REFERENCES notification (id) ON DELETE SET NULL,
    PRIMARY KEY (user_id, household_id, day)
);

-- Single row: the next getUpdates offset of the Telegram poller.
CREATE TABLE telegram_poll (
    id            boolean PRIMARY KEY DEFAULT true CHECK (id),
    update_offset bigint  NOT NULL DEFAULT 0
);
INSERT INTO telegram_poll DEFAULT VALUES;

-- +goose Down
DROP TABLE telegram_poll, digest_run, telegram_link, notification_channel, notification_pref, notification;
