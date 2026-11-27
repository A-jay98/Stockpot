-- Purchase history behind product_stat (autocomplete and the guess list).
-- Not synced: the server derives product_stat from it and clients read that.

-- +goose Up
CREATE TABLE purchase (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id     uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    product_id       uuid        NOT NULL REFERENCES product (id),
    store_id         uuid        REFERENCES store (id),
    -- The ticked shopping item; unticking removes the purchase again.
    shopping_item_id uuid        REFERENCES shopping_item (id),
    qty              numeric,
    unit             text,
    bought_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX purchase_item ON purchase (shopping_item_id) WHERE shopping_item_id IS NOT NULL;
CREATE INDEX purchase_product ON purchase (household_id, product_id, bought_at DESC);

-- +goose Down
DROP TABLE purchase;
