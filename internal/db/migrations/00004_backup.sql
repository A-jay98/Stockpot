-- Nightly encrypted backups to a Telegram channel: one row per run.

-- +goose Up
CREATE TABLE backup_run (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger             text        NOT NULL CHECK (trigger IN ('scheduled', 'manual')),
    status              text        NOT NULL CHECK (status IN ('running', 'ok', 'failed')),
    -- River job executing the run; a running row whose job is gone is stale.
    job_id              bigint,
    started_at          timestamptz NOT NULL DEFAULT now(),
    finished_at         timestamptz,
    size_bytes          bigint      NOT NULL DEFAULT 0,
    parts               integer     NOT NULL DEFAULT 0,
    app_version         text        NOT NULL DEFAULT '',
    schema_version      bigint,
    error               text,
    manifest            jsonb,
    -- Telegram messages holding the run (parts + manifest), for retention.
    message_ids         bigint[]    NOT NULL DEFAULT '{}',
    -- Set once retention tried to delete the messages; delete_note says what
    -- could not be deleted (Telegram only lets bots delete recent messages).
    pruned_at           timestamptz,
    delete_note         text
);
CREATE INDEX backup_run_started_at ON backup_run (started_at DESC);
-- At most one run at a time.
CREATE UNIQUE INDEX backup_run_one_running ON backup_run ((true)) WHERE status = 'running';

-- +goose Down
DROP TABLE backup_run;
