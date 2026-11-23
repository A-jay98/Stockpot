-- Foundation: sync revision convention, runtime settings, application log.

-- +goose Up

-- Every household-owned table carries rev bigint, set from this sequence on
-- each insert and update, so clients pull changes with ?since=<rev>.
CREATE SEQUENCE change_rev AS bigint;

-- Reused by every household table:
--   CREATE TRIGGER t_rev BEFORE INSERT OR UPDATE ON <table>
--     FOR EACH ROW EXECUTE FUNCTION set_rev_and_updated_at();
-- +goose StatementBegin
CREATE FUNCTION set_rev_and_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.rev := nextval('change_rev');
    IF TG_OP = 'UPDATE' THEN
        NEW.updated_at := now();
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

-- Runtime settings edited in the admin panel. Only keys declared in the Go
-- registry (internal/settings) are read; unknown rows are ignored. Exactly
-- one of value (plain JSON) and secret (secretbox ciphertext of the JSON) is
-- set. household_id is NULL for system scope; household scope comes later.
CREATE TABLE setting (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key          text        NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'),
    household_id uuid,
    value        jsonb,
    secret       bytea,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid,
    CONSTRAINT setting_one_value CHECK ((value IS NULL) <> (secret IS NULL)),
    CONSTRAINT setting_key_scope UNIQUE NULLS NOT DISTINCT (key, household_id)
);

-- Tell every server process which key changed, so caches reload it.
-- +goose StatementBegin
CREATE FUNCTION notify_setting_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('setting_changed', COALESCE(NEW.key, OLD.key));
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER setting_changed
    AFTER INSERT OR UPDATE OR DELETE ON setting
    FOR EACH ROW EXECUTE FUNCTION notify_setting_changed();

-- Application log, written in batches by the slog DB sink and shown in the
-- admin panel. level is the slog level (-4 debug, 0 info, 4 warn, 8 error).
CREATE TABLE app_log (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    time       timestamptz NOT NULL,
    level      smallint    NOT NULL,
    msg        text        NOT NULL,
    attrs      jsonb       NOT NULL DEFAULT '{}',
    request_id text,
    user_id    uuid
);
CREATE INDEX app_log_time ON app_log (time);
CREATE INDEX app_log_level_time ON app_log (level, time);
CREATE INDEX app_log_request_id ON app_log (request_id) WHERE request_id IS NOT NULL;

-- +goose Down
DROP TABLE app_log;
DROP TRIGGER setting_changed ON setting;
DROP FUNCTION notify_setting_changed();
DROP TABLE setting;
DROP FUNCTION set_rev_and_updated_at();
DROP SEQUENCE change_rev;
