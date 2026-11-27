-- Inventory (m1-2): shelf-life rules and purchase history. Additive only.
-- Neither table is synced: rules reach the client over REST (cached for
-- offline estimates) and the purchase log only feeds product_stat.

-- +goose Up

-- Shelf-life rules in days, keyed fridge, freezer, pantry, opened_fridge,
-- thawed_fridge (absent key = unknown). household_id NULL = Stockpot default;
-- a household row overrides the default for the same ingredient or category.
CREATE TABLE food_rule (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  uuid        REFERENCES household (id) ON DELETE CASCADE,
    ingredient_id uuid        REFERENCES ingredient (id),
    category_id   uuid        REFERENCES category (id),
    shelf_life    jsonb       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((ingredient_id IS NULL) <> (category_id IS NULL))
);
CREATE UNIQUE INDEX food_rule_ingredient ON food_rule (coalesce(household_id, '00000000-0000-0000-0000-000000000000'::uuid), ingredient_id) WHERE ingredient_id IS NOT NULL;
CREATE UNIQUE INDEX food_rule_category ON food_rule (coalesce(household_id, '00000000-0000-0000-0000-000000000000'::uuid), category_id) WHERE category_id IS NOT NULL;

-- Stockpot defaults per category. Ingredient seeds carry the specific
-- fridge/freezer/pantry days; these cover everything the seed leaves out,
-- including the opened and thawed rules that the seed has none of.
INSERT INTO food_rule (category_id, shelf_life)
SELECT c.id, v.shelf_life::jsonb
FROM (VALUES
    ('produce-fruit',     '{"fridge":7,"freezer":240,"pantry":5,"opened_fridge":3,"thawed_fridge":3}'),
    ('produce-veg',       '{"fridge":7,"freezer":240,"pantry":5,"opened_fridge":3,"thawed_fridge":3}'),
    ('herbs',             '{"fridge":7,"freezer":180,"pantry":3,"opened_fridge":3,"thawed_fridge":2}'),
    ('dairy-eggs',        '{"fridge":14,"freezer":90,"opened_fridge":5,"thawed_fridge":3}'),
    ('meat',              '{"fridge":3,"freezer":180,"opened_fridge":2,"thawed_fridge":2}'),
    ('seafood',           '{"fridge":2,"freezer":120,"opened_fridge":1,"thawed_fridge":1}'),
    ('deli',              '{"fridge":5,"freezer":60,"opened_fridge":3,"thawed_fridge":2}'),
    ('bakery',            '{"fridge":7,"freezer":90,"pantry":4,"opened_fridge":4,"thawed_fridge":2}'),
    ('pantry',            '{"fridge":365,"freezer":365,"pantry":365,"opened_fridge":7,"thawed_fridge":3}'),
    ('baking',            '{"fridge":365,"freezer":365,"pantry":365,"opened_fridge":180,"thawed_fridge":3}'),
    ('spices-condiments', '{"fridge":365,"freezer":365,"pantry":365,"opened_fridge":90,"thawed_fridge":7}'),
    ('oils-sauces',       '{"fridge":365,"freezer":180,"pantry":365,"opened_fridge":60,"thawed_fridge":3}'),
    ('frozen',            '{"fridge":3,"freezer":180,"opened_fridge":3,"thawed_fridge":2}'),
    ('snacks-sweets',     '{"fridge":120,"freezer":180,"pantry":120,"opened_fridge":30,"thawed_fridge":3}'),
    ('drinks',            '{"fridge":30,"freezer":90,"pantry":180,"opened_fridge":7,"thawed_fridge":3}'),
    ('other',             '{"fridge":7,"freezer":90,"pantry":30,"opened_fridge":5,"thawed_fridge":3}')
) AS v (slug, shelf_life)
JOIN category c ON c.slug = v.slug AND c.household_id IS NULL
ON CONFLICT DO NOTHING;

-- One row per product purchase (manual stock adds and, in m1-4, checked-off
-- shopping items). product_stat is recomputed from the last 10 of them.
CREATE TABLE purchase_log (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    product_id   uuid        NOT NULL REFERENCES product (id),
    store_id     uuid        REFERENCES store (id),
    qty          numeric,
    unit         text,
    bought_at    timestamptz NOT NULL
);
CREATE INDEX purchase_log_product ON purchase_log (household_id, product_id, bought_at DESC);

-- +goose Down
DROP TABLE purchase_log;
DROP TABLE food_rule;
