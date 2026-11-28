-- Problem reports ("the default unit is wrong", "wrong match", …) that people
-- send from the app. A background job asks an LLM about them with spare quota
-- only; an approved default-unit correction is applied to the catalog, the
-- rest wait for the admin. Not synced.

-- +goose Up
CREATE TABLE problem_report (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    household_id  uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    reporter_id   uuid        REFERENCES app_user (id) ON DELETE SET NULL,
    kind          text        NOT NULL CHECK (kind IN ('wrong_unit', 'wrong_match', 'wrong_icon', 'other')),
    -- Where it was reported from (shopping_item, product, stock_item, …) and the subject.
    source        text        NOT NULL,
    subject_id    uuid,
    product_id    uuid        REFERENCES product (id) ON DELETE SET NULL,
    ingredient_id uuid        REFERENCES ingredient (id) ON DELETE SET NULL,
    -- What the person saw when reporting (names, units, quantities).
    context       jsonb       NOT NULL DEFAULT '{}',
    -- What the person says it should be (a unit code for wrong_unit).
    suggested     text,
    note          text,
    -- open: waiting for the LLM; needs_admin: the LLM could not settle it (or the
    -- kind is not auto-applied); applied: a change was made; rejected: the
    -- LLM or admin said the current value is right; dismissed: admin closed it.
    status        text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'needs_admin', 'applied', 'rejected', 'dismissed')),
    attempts      int         NOT NULL DEFAULT 0,
    llm_verdict   text        CHECK (llm_verdict IN ('approve', 'reject', 'unsure')),
    llm_value     text,
    llm_reason    text,
    llm_confidence real,
    llm_provider  text,
    llm_at        timestamptz,
    -- The change made (by the job or an admin): {"target": …, "field": …, "before": …, "after": …}.
    applied       jsonb,
    resolved_by   text,
    resolved_at   timestamptz,
    admin_note    text
);
CREATE INDEX problem_report_open ON problem_report (created_at) WHERE status = 'open';
CREATE INDEX problem_report_status ON problem_report (status, created_at DESC);
CREATE INDEX problem_report_household ON problem_report (household_id, created_at DESC);

-- +goose Down
DROP TABLE problem_report;
