-- Recipes and cooking (M2, architecture-m2 §3). Additive only. Every synced
-- table and column M2 needs is created here, so wave-2 feature threads add
-- only non-synced tables and never touch the sync table list or Dexie.

-- +goose Up

CREATE TABLE recipe (
    id                 uuid        PRIMARY KEY,
    household_id       uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    rev                bigint      NOT NULL DEFAULT 0,
    deleted_at         timestamptz,
    title              text        NOT NULL,
    subtitle           text,
    source_kind        text        NOT NULL DEFAULT 'manual' CHECK (source_kind IN ('manual', 'url', 'instagram', 'photo', 'text', 'idea')),
    source_url         text,
    source_lang        text,
    current_version_id uuid,
    servings           int,
    total_minutes      int,
    cover_blob         text,
    tags               text[]      NOT NULL DEFAULT '{}',
    saved              boolean     NOT NULL DEFAULT true,
    idea_expires_at    timestamptz,
    cooked_count       int         NOT NULL DEFAULT 0,
    last_cooked_at     timestamptz,
    import_id          uuid
);
CREATE INDEX recipe_title_trgm ON recipe USING gin (immutable_unaccent(title) gin_trgm_ops);
CREATE INDEX recipe_idea_expiry ON recipe (idea_expires_at) WHERE NOT saved AND deleted_at IS NULL;

-- Immutable snapshots. Only the match of a line (ingredients[].ingredient_id,
-- match_confidence) changes in place; content changes make a new version.
CREATE TABLE recipe_version (
    id                  uuid        PRIMARY KEY,
    household_id        uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    rev                 bigint      NOT NULL DEFAULT 0,
    deleted_at          timestamptz,
    recipe_id           uuid        NOT NULL REFERENCES recipe (id),
    n                   int         NOT NULL CHECK (n > 0),
    parent_version_id   uuid        REFERENCES recipe_version (id),
    origin              text        NOT NULL CHECK (origin IN ('import', 'manual', 'chat', 'restore', 'idea')),
    change_summary      text,
    title               text        NOT NULL,
    servings            int,
    total_minutes       int,
    ingredients         jsonb       NOT NULL DEFAULT '[]',
    steps               jsonb       NOT NULL DEFAULT '[]',
    notes               text,
    original            jsonb,
    original_version_id uuid        REFERENCES recipe_version (id),
    created_by          uuid,
    UNIQUE (recipe_id, n)
);

ALTER TABLE recipe ADD CONSTRAINT recipe_current_version FOREIGN KEY (current_version_id) REFERENCES recipe_version (id);

CREATE TABLE cook_session (
    id                  uuid        PRIMARY KEY,
    household_id        uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    rev                 bigint      NOT NULL DEFAULT 0,
    deleted_at          timestamptz,
    recipe_id           uuid        NOT NULL REFERENCES recipe (id),
    version_id          uuid        REFERENCES recipe_version (id),
    cooked_on           date        NOT NULL,
    cooked_by           uuid,
    portions_made       int         NOT NULL DEFAULT 0,
    stored              jsonb       NOT NULL DEFAULT '[]',
    uses                jsonb       NOT NULL DEFAULT '[]',
    subs                jsonb       NOT NULL DEFAULT '[]',
    leftover_product_id uuid        REFERENCES product (id),
    note                text,
    undone_at           timestamptz
);
CREATE INDEX cook_session_recipe ON cook_session (recipe_id, cooked_on);

CREATE TABLE recipe_photo (
    id              uuid        PRIMARY KEY,
    household_id    uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    rev             bigint      NOT NULL DEFAULT 0,
    deleted_at      timestamptz,
    recipe_id       uuid        NOT NULL REFERENCES recipe (id),
    cook_session_id uuid        REFERENCES cook_session (id),
    blob_ref        text        NOT NULL,
    taken_on        date,
    caption         text,
    created_by      uuid
);
CREATE INDEX recipe_photo_recipe ON recipe_photo (recipe_id);

CREATE TABLE recipe_import (
    id            uuid        PRIMARY KEY,
    household_id  uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    rev           bigint      NOT NULL DEFAULT 0,
    deleted_at    timestamptz,
    batch_id      uuid,
    kind          text        NOT NULL CHECK (kind IN ('url', 'instagram', 'photo', 'text')),
    input         text,
    blob_refs     text[]      NOT NULL DEFAULT '{}',
    status        text        NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'fetching', 'parsing', 'translating', 'done', 'failed', 'needs_input', 'waiting_quota')),
    error         jsonb,
    recipe_id     uuid        REFERENCES recipe (id),
    attempts      int         NOT NULL DEFAULT 0,
    detected_lang text
);
CREATE INDEX recipe_import_batch ON recipe_import (batch_id);

ALTER TABLE recipe ADD CONSTRAINT recipe_import_fk FOREIGN KEY (import_id) REFERENCES recipe_import (id);

-- Global (household_id NULL, seeded) and household substitutions.
CREATE TABLE substitution (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  uuid        REFERENCES household (id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    rev           bigint      NOT NULL DEFAULT 0,
    deleted_at    timestamptz,
    ingredient_id uuid        NOT NULL REFERENCES ingredient (id),
    uses          jsonb       NOT NULL DEFAULT '[]',
    note          text,
    recipe_id     uuid        REFERENCES recipe (id),
    source        text        NOT NULL DEFAULT 'user' CHECK (source IN ('seed', 'llm', 'user'))
);
CREATE INDEX substitution_ingredient ON substitution (ingredient_id);

-- Cross-dimension conversions, {"ea": "150 g", "cup": "185 g"}.
ALTER TABLE ingredient ADD COLUMN conversions jsonb NOT NULL DEFAULT '{}';
ALTER TABLE product ADD COLUMN conversions jsonb NOT NULL DEFAULT '{}';
-- Leftover products ("Leftover chili") are one per recipe.
ALTER TABLE product ADD COLUMN recipe_id uuid REFERENCES recipe (id);
CREATE UNIQUE INDEX product_leftover ON product (household_id, recipe_id) WHERE recipe_id IS NOT NULL AND deleted_at IS NULL;
ALTER TABLE stock_item ADD COLUMN cook_session_id uuid REFERENCES cook_session (id);
ALTER TABLE stock_event ADD COLUMN cook_session_id uuid REFERENCES cook_session (id);
ALTER TABLE shopping_item ADD COLUMN recipe_id uuid REFERENCES recipe (id);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['recipe', 'recipe_version', 'recipe_photo', 'cook_session', 'recipe_import', 'substitution']
    LOOP
        EXECUTE format('CREATE INDEX %I ON %I (household_id, rev)', t || '_hh_rev', t);
        EXECUTE format('CREATE TRIGGER %I BEFORE INSERT OR UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION set_rev_and_updated_at()', t || '_rev', t);
        EXECUTE format('CREATE TRIGGER %I AFTER INSERT ON %I REFERENCING NEW TABLE AS changed FOR EACH STATEMENT EXECUTE FUNCTION sync_notify()', t || '_notify_ins', t);
        EXECUTE format('CREATE TRIGGER %I AFTER UPDATE ON %I REFERENCING NEW TABLE AS changed FOR EACH STATEMENT EXECUTE FUNCTION sync_notify()', t || '_notify_upd', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- Leftovers: what "I cooked this" stores. Fridge 4 days, freezer 90.
INSERT INTO category (slug, name, icon, sort) VALUES ('leftovers', 'Leftovers', 'cat-leftovers', 135)
ON CONFLICT DO NOTHING;
INSERT INTO food_rule (category_id, shelf_life)
SELECT id, '{"fridge":4,"freezer":90,"thawed_fridge":2}'::jsonb FROM category WHERE slug = 'leftovers' AND household_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM food_rule WHERE category_id IN (SELECT id FROM category WHERE slug = 'leftovers' AND household_id IS NULL) AND ingredient_id IS NULL;
DELETE FROM category WHERE slug = 'leftovers' AND household_id IS NULL;
ALTER TABLE shopping_item DROP COLUMN recipe_id;
ALTER TABLE stock_event DROP COLUMN cook_session_id;
ALTER TABLE stock_item DROP COLUMN cook_session_id;
ALTER TABLE product DROP COLUMN recipe_id, DROP COLUMN conversions;
ALTER TABLE ingredient DROP COLUMN conversions;
ALTER TABLE recipe DROP CONSTRAINT recipe_import_fk, DROP CONSTRAINT recipe_current_version;
DROP TABLE substitution, recipe_import, recipe_photo, cook_session, recipe_version, recipe;
