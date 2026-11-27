-- Sync engine and the M1 synced tables (catalog, inventory, shopping).
-- Additive only. All synced tables are created here so parallel feature work
-- never has to touch the sync table list or the client's Dexie schema.

-- +goose Up

CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;

-- unaccent() is only STABLE; index expressions need IMMUTABLE.
-- +goose StatementBegin
CREATE FUNCTION immutable_unaccent(text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$ SELECT public.unaccent('public.unaccent'::regdictionary, $1) $$;
-- +goose StatementEnd

-- array_to_string is only STABLE; for text[] it is safe to call immutable.
-- +goose StatementBegin
CREATE FUNCTION alias_text(text[]) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$ SELECT array_to_string($1, ' ') $$;
-- +goose StatementEnd

-- Commands applied by POST /v1/commands, one row per client command id.
-- status 'rejected' keeps the problem so a retry of the same id answers the same.
CREATE TABLE applied_command (
    id           uuid        PRIMARY KEY,
    household_id uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    user_id      uuid        NOT NULL,
    type         text        NOT NULL,
    client_time  timestamptz,
    applied_at   timestamptz NOT NULL DEFAULT now(),
    status       text        NOT NULL DEFAULT 'applied' CHECK (status IN ('applied', 'rejected')),
    problem      jsonb
);
CREATE INDEX applied_command_household ON applied_command (household_id, applied_at);

CREATE TABLE category (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid        REFERENCES household (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    rev          bigint      NOT NULL DEFAULT 0,
    deleted_at   timestamptz,
    slug         text        NOT NULL,
    name         text        NOT NULL,
    icon         text,
    sort         int         NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX category_slug ON category (coalesce(household_id, '00000000-0000-0000-0000-000000000000'::uuid), slug);

CREATE TABLE ingredient (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  uuid        REFERENCES household (id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    rev           bigint      NOT NULL DEFAULT 0,
    deleted_at    timestamptz,
    slug          text        NOT NULL,
    name          text        NOT NULL,
    plural        text,
    parent_id     uuid        REFERENCES ingredient (id),
    category_id   uuid        REFERENCES category (id),
    icon          text,
    aliases       text[]      NOT NULL DEFAULT '{}',
    shelf_life    jsonb       NOT NULL DEFAULT '{}',
    default_unit  text,
    source        text        NOT NULL DEFAULT 'household' CHECK (source IN ('seed', 'household', 'llm', 'admin'))
);
CREATE UNIQUE INDEX ingredient_slug ON ingredient (coalesce(household_id, '00000000-0000-0000-0000-000000000000'::uuid), slug);
CREATE INDEX ingredient_parent ON ingredient (parent_id);
CREATE INDEX ingredient_name_trgm ON ingredient USING gin (immutable_unaccent(name) gin_trgm_ops);
CREATE INDEX ingredient_aliases_trgm ON ingredient USING gin (immutable_unaccent(alias_text(aliases)) gin_trgm_ops);

CREATE TABLE store (
    id           uuid        PRIMARY KEY,
    household_id uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    rev          bigint      NOT NULL DEFAULT 0,
    deleted_at   timestamptz,
    name         text        NOT NULL,
    sort         int         NOT NULL DEFAULT 0,
    color        text
);

CREATE TABLE location (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    rev          bigint      NOT NULL DEFAULT 0,
    deleted_at   timestamptz,
    name         text        NOT NULL,
    kind         text        NOT NULL CHECK (kind IN ('fridge', 'freezer', 'pantry', 'other')),
    sort         int         NOT NULL DEFAULT 0
);

CREATE TABLE product (
    id                    uuid        PRIMARY KEY,
    household_id          uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    rev                   bigint      NOT NULL DEFAULT 0,
    deleted_at            timestamptz,
    name                  text        NOT NULL,
    brand                 text,
    ingredient_id         uuid        REFERENCES ingredient (id),
    extra_ingredient_ids  uuid[]      NOT NULL DEFAULT '{}',
    category_id           uuid        REFERENCES category (id),
    icon                  text,
    default_store_id      uuid        REFERENCES store (id),
    default_qty           numeric,
    default_unit          text,
    package               text,
    default_location_kind text        CHECK (default_location_kind IN ('fridge', 'freezer', 'pantry', 'other')),
    shelf_life            jsonb,
    match_source          text        NOT NULL DEFAULT 'none' CHECK (match_source IN ('none', 'rule', 'llm', 'user', 'barcode')),
    match_confidence      real,
    enrich_state          text        NOT NULL DEFAULT 'pending' CHECK (enrich_state IN ('pending', 'done', 'failed', 'skipped')),
    enrich_attempts       int         NOT NULL DEFAULT 0,
    off_code              text,
    note                  text
);
CREATE UNIQUE INDEX product_name ON product (household_id, lower(name), lower(coalesce(brand, ''))) WHERE deleted_at IS NULL;
CREATE INDEX product_ingredient ON product (ingredient_id);
CREATE INDEX product_enrich ON product (created_at) WHERE enrich_state = 'pending' AND deleted_at IS NULL;
CREATE INDEX product_name_trgm ON product USING gin (immutable_unaccent(name) gin_trgm_ops);

CREATE TABLE product_barcode (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    rev          bigint      NOT NULL DEFAULT 0,
    deleted_at   timestamptz,
    product_id   uuid        NOT NULL REFERENCES product (id),
    code         text        NOT NULL
);
CREATE UNIQUE INDEX product_barcode_code ON product_barcode (household_id, code) WHERE deleted_at IS NULL;

CREATE TABLE product_stat (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id         uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    rev                  bigint      NOT NULL DEFAULT 0,
    deleted_at           timestamptz,
    product_id           uuid        NOT NULL REFERENCES product (id),
    store_id             uuid        REFERENCES store (id),
    times_bought         int         NOT NULL DEFAULT 0,
    last_bought_at       timestamptz,
    typical_qty          numeric,
    typical_unit         text,
    median_interval_days real,
    last_qty             numeric
);
CREATE UNIQUE INDEX product_stat_key ON product_stat (product_id, coalesce(store_id, '00000000-0000-0000-0000-000000000000'::uuid));

CREATE TABLE stock_item (
    id             uuid        PRIMARY KEY,
    household_id   uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    rev            bigint      NOT NULL DEFAULT 0,
    deleted_at     timestamptz,
    product_id     uuid        NOT NULL REFERENCES product (id),
    location_id    uuid        REFERENCES location (id),
    amount_kind    text        NOT NULL DEFAULT 'exact' CHECK (amount_kind IN ('exact', 'portions', 'level')),
    quantity       numeric,
    unit           text,
    portions       int,
    level          text        CHECK (level IN ('full', 'most', 'half', 'low', 'almost_out')),
    low            boolean     NOT NULL DEFAULT false,
    expires_on     date,
    expiry_kind    text        CHECK (expiry_kind IN ('use_by', 'best_before', 'estimated')),
    opened_on      date,
    thawed_on      date,
    frozen_on      date,
    source         text        NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'shopping', 'capture', 'cook', 'split')),
    parent_item_id uuid        REFERENCES stock_item (id),
    note           text
);
CREATE INDEX stock_item_product ON stock_item (household_id, product_id) WHERE deleted_at IS NULL;
CREATE INDEX stock_item_expiry ON stock_item (household_id, expires_on) WHERE deleted_at IS NULL;

CREATE TABLE stock_event (
    id               uuid        PRIMARY KEY,
    household_id     uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    rev              bigint      NOT NULL DEFAULT 0,
    deleted_at       timestamptz,
    stock_item_id    uuid        NOT NULL REFERENCES stock_item (id),
    kind             text        NOT NULL CHECK (kind IN ('add', 'consume', 'discard', 'move', 'adjust', 'open', 'thaw', 'freeze', 'split', 'low')),
    delta            numeric,
    unit             text,
    from_location_id uuid        REFERENCES location (id),
    to_location_id   uuid        REFERENCES location (id),
    reason           text,
    actor_id         uuid,
    command_id       uuid
);
CREATE INDEX stock_event_item ON stock_event (stock_item_id, created_at);

CREATE TABLE shopping_list (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    rev          bigint      NOT NULL DEFAULT 0,
    deleted_at   timestamptz,
    name         text        NOT NULL,
    sort         int         NOT NULL DEFAULT 0
);

CREATE TABLE shopping_item (
    id            uuid        PRIMARY KEY,
    household_id  uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    rev           bigint      NOT NULL DEFAULT 0,
    deleted_at    timestamptz,
    list_id       uuid        NOT NULL REFERENCES shopping_list (id),
    product_id    uuid        REFERENCES product (id),
    text          text        NOT NULL DEFAULT '',
    qty           numeric,
    unit          text,
    category_id   uuid        REFERENCES category (id),
    store_ids     uuid[]      NOT NULL DEFAULT '{}',
    note          text,
    sort          int         NOT NULL DEFAULT 0,
    checked_at    timestamptz,
    checked_by    uuid,
    stock_item_id uuid        REFERENCES stock_item (id)
);
CREATE INDEX shopping_item_list ON shopping_item (list_id) WHERE deleted_at IS NULL;

-- Per-table: household/rev index for pulls, the rev trigger, and a
-- transition-table trigger that NOTIFYs sync_<household> (or sync_global)
-- once per statement after commit. Notifications carry no data.
-- +goose StatementBegin
CREATE FUNCTION sync_notify() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    hh uuid;
BEGIN
    FOR hh IN SELECT DISTINCT household_id FROM changed LOOP
        PERFORM pg_notify(CASE WHEN hh IS NULL THEN 'sync_global' ELSE 'sync_' || replace(hh::text, '-', '') END, '');
    END LOOP;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['category', 'ingredient', 'store', 'location', 'product', 'product_barcode',
                             'product_stat', 'stock_item', 'stock_event', 'shopping_list', 'shopping_item']
    LOOP
        EXECUTE format('CREATE INDEX %I ON %I (household_id, rev)', t || '_hh_rev', t);
        EXECUTE format('CREATE TRIGGER %I BEFORE INSERT OR UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION set_rev_and_updated_at()', t || '_rev', t);
        EXECUTE format('CREATE TRIGGER %I AFTER INSERT ON %I REFERENCING NEW TABLE AS changed FOR EACH STATEMENT EXECUTE FUNCTION sync_notify()', t || '_notify_ins', t);
        EXECUTE format('CREATE TRIGGER %I AFTER UPDATE ON %I REFERENCING NEW TABLE AS changed FOR EACH STATEMENT EXECUTE FUNCTION sync_notify()', t || '_notify_upd', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- Global categories (household_id NULL).
INSERT INTO category (slug, name, icon, sort) VALUES
    ('produce-fruit', 'Fruit', 'cat-produce-fruit', 10),
    ('produce-veg', 'Vegetables', 'cat-produce-veg', 20),
    ('herbs', 'Herbs', 'cat-herbs', 30),
    ('dairy-eggs', 'Dairy & eggs', 'cat-dairy-eggs', 40),
    ('meat', 'Meat & poultry', 'cat-meat', 50),
    ('seafood', 'Seafood', 'cat-seafood', 60),
    ('deli', 'Deli', 'cat-deli', 70),
    ('bakery', 'Bakery', 'cat-bakery', 80),
    ('pantry', 'Pantry', 'cat-pantry', 90),
    ('baking', 'Baking', 'cat-baking', 100),
    ('spices-condiments','Spices & condiments',   'cat-spices-condiments', 110),
    ('oils-sauces', 'Oils & sauces', 'cat-oils-sauces', 120),
    ('frozen', 'Frozen', 'cat-frozen', 130),
    ('snacks-sweets', 'Snacks & sweets', 'cat-snacks-sweets', 140),
    ('drinks', 'Drinks', 'cat-drinks', 150),
    ('household', 'Household', 'cat-household', 160),
    ('personal-care', 'Personal care', 'cat-personal-care', 170),
    ('other', 'Other', 'cat-other', 999)
ON CONFLICT DO NOTHING;

-- Every household starts with Fridge, Freezer, Pantry and a Groceries list.
-- A trigger (not application code) so every way of creating a household,
-- including tests and future import paths, gets them.
-- +goose StatementBegin
CREATE FUNCTION household_seed_defaults() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO location (household_id, name, kind, sort) VALUES
        (NEW.id, 'Fridge', 'fridge', 10),
        (NEW.id, 'Freezer', 'freezer', 20),
        (NEW.id, 'Pantry', 'pantry', 30);
    INSERT INTO shopping_list (household_id, name, sort) VALUES (NEW.id, 'Groceries', 0);
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER household_seed AFTER INSERT ON household
    FOR EACH ROW EXECUTE FUNCTION household_seed_defaults();

-- Backfill households that exist already.
INSERT INTO location (household_id, name, kind, sort)
SELECT h.id, v.name, v.kind, v.sort FROM household h
CROSS JOIN (VALUES ('Fridge', 'fridge', 10), ('Freezer', 'freezer', 20), ('Pantry', 'pantry', 30)) AS v (name, kind, sort)
WHERE NOT EXISTS (SELECT 1 FROM location l WHERE l.household_id = h.id);
INSERT INTO shopping_list (household_id, name, sort)
SELECT h.id, 'Groceries', 0 FROM household h
WHERE NOT EXISTS (SELECT 1 FROM shopping_list s WHERE s.household_id = h.id);

-- +goose Down
DROP TRIGGER household_seed ON household;
DROP FUNCTION household_seed_defaults();
DROP TABLE shopping_item, shopping_list, stock_event, stock_item, product_stat, product_barcode,
    product, location, store, ingredient, category, applied_command;
DROP FUNCTION sync_notify();
DROP FUNCTION alias_text(text[]);
DROP FUNCTION immutable_unaccent(text);
