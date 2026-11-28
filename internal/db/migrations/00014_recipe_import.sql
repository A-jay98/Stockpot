-- Recipe import (m2-4, architecture-m2 §7): the fetch log for the admin
-- panel and per-import working state. Neither is synced; recipe_import
-- itself (synced) was created by 00010.

-- +goose Up

-- Every outbound fetch of a user-supplied URL (page or cover image), kept
-- retention.recipe_fetch_days (30).
CREATE TABLE recipe_fetch (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at   timestamptz NOT NULL DEFAULT now(),
    household_id uuid        REFERENCES household (id) ON DELETE CASCADE,
    import_id    uuid        REFERENCES recipe_import (id) ON DELETE CASCADE,
    purpose      text        NOT NULL CHECK (purpose IN ('page', 'image')),
    url          text        NOT NULL,
    final_url    text,
    status       int,
    bytes        bigint      NOT NULL DEFAULT 0,
    duration_ms  int         NOT NULL DEFAULT 0,
    redirects    int         NOT NULL DEFAULT 0,
    error        text
);
CREATE INDEX recipe_fetch_created ON recipe_fetch (created_at);

-- What an import gathered or was given beyond its synced row: the text the
-- user pasted for a blocked reel, and the reel's caption and transcript, so
-- a retry doesn't download and transcribe again.
CREATE TABLE recipe_import_detail (
    import_id     uuid        PRIMARY KEY REFERENCES recipe_import (id) ON DELETE CASCADE,
    provided_text text,
    caption       text,
    transcript    text,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Admin → Recipes counts imports by status and lists recent failures.
CREATE INDEX recipe_import_status ON recipe_import (status, updated_at);

-- +goose Down
DROP INDEX recipe_import_status;
DROP TABLE recipe_import_detail;
DROP TABLE recipe_fetch;
