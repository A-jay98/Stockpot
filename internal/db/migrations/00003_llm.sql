-- LLM gateway: one metering row per call, and a response cache.

-- +goose Up

-- One row per attempt at a provider, plus one per cache hit. Daily caps
-- count rows since the provider's reset time (cache hits and attempts the
-- provider refused for quota excluded). provider/model are free text, since
-- providers come from settings and can be renamed.
CREATE TABLE llm_call (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    time         timestamptz NOT NULL DEFAULT now(),
    feature      text        NOT NULL,
    provider     text        NOT NULL,
    model        text        NOT NULL,
    user_id      uuid,
    household_id uuid,
    tokens_in    integer     NOT NULL DEFAULT 0 CHECK (tokens_in >= 0),
    tokens_out   integer     NOT NULL DEFAULT 0 CHECK (tokens_out >= 0),
    latency_ms   integer     NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    status       text        NOT NULL CHECK (status IN ('ok', 'quota', 'bad_output', 'rejected', 'error')),
    cache_hit    boolean     NOT NULL DEFAULT false,
    error        text
);
CREATE INDEX llm_call_time ON llm_call (time);
CREATE INDEX llm_call_provider_time ON llm_call (provider, time) WHERE NOT cache_hit;

-- Responses keyed by SHA-256 of the provider-agnostic normalized request.
-- Rows past expires_at are ignored and pruned by the retention job.
CREATE TABLE llm_cache (
    key        bytea       PRIMARY KEY CHECK (length(key) = 32),
    feature    text        NOT NULL,
    provider   text        NOT NULL,
    model      text        NOT NULL,
    response   text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX llm_cache_expires_at ON llm_cache (expires_at);

-- +goose Down
DROP TABLE llm_cache;
DROP TABLE llm_call;
