-- Background catalog enrichment (LLM fills what the rules could not): run
-- history for the admin panel and a per-name result cache. Neither is synced.

-- +goose Up
CREATE TABLE enrich_run (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger     text        NOT NULL CHECK (trigger IN ('scheduled', 'manual')),
    -- River job executing the run.
    job_id      bigint,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    outcome     text        NOT NULL DEFAULT 'running' CHECK (outcome IN ('running', 'ok', 'partial', 'deferred', 'error')),
    -- Products looked at, applied, failed (invalid answer, one more attempt used) and served from the cache.
    items       integer     NOT NULL DEFAULT 0,
    applied     integer     NOT NULL DEFAULT 0,
    failed      integer     NOT NULL DEFAULT 0,
    cached      integer     NOT NULL DEFAULT 0,
    calls       integer     NOT NULL DEFAULT 0,
    provider    text,
    -- Why the run was deferred or failed.
    reason      text
);
CREATE INDEX enrich_run_started_at ON enrich_run (started_at DESC);

-- The validated answer for one normalized (name, brand): repeated names across
-- households cost no LLM call. Answers name ingredients by slug, so each
-- household's application is validated again.
CREATE TABLE enrich_cache (
    key        text        PRIMARY KEY,
    result     jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX enrich_cache_created_at ON enrich_cache (created_at);

-- Admin review lists.
CREATE INDEX ingredient_llm ON ingredient (created_at DESC) WHERE source = 'llm' AND deleted_at IS NULL;

-- +goose Down
DROP INDEX ingredient_llm;
DROP TABLE enrich_cache;
DROP TABLE enrich_run;
