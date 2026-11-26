-- name: InsertLLMCall :exec
INSERT INTO llm_call (time, feature, provider, model, user_id, household_id, tokens_in, tokens_out, latency_ms, status, cache_hit, error)
VALUES (sqlc.arg(time), sqlc.arg(feature), sqlc.arg(provider), sqlc.arg(model), sqlc.narg(user_id), sqlc.narg(household_id),
        sqlc.arg(tokens_in), sqlc.arg(tokens_out), sqlc.arg(latency_ms), sqlc.arg(status), sqlc.arg(cache_hit), sqlc.narg(error));

-- name: LLMProviderUsageSince :one
-- Requests and tokens a provider has spent since a reset time. Cache hits
-- and attempts refused for quota cost nothing.
SELECT count(*)::bigint AS requests,
       coalesce(sum(tokens_in + tokens_out), 0)::bigint AS tokens
FROM llm_call
WHERE provider = sqlc.arg(provider) AND time >= sqlc.arg(since)::timestamptz
  AND NOT cache_hit AND status <> 'quota';

-- name: LLMUsageSince :one
-- Requests and tokens spent across all providers since a reset time.
SELECT count(*)::bigint AS requests,
       coalesce(sum(tokens_in + tokens_out), 0)::bigint AS tokens
FROM llm_call
WHERE time >= sqlc.arg(since)::timestamptz AND NOT cache_hit AND status <> 'quota';

-- name: LLMUsageByDay :many
-- Usage per local day (in tz), provider and feature, newest day first.
SELECT (llm_call.time AT TIME ZONE sqlc.arg(tz)::text)::date AS day,
       provider,
       feature,
       count(*) FILTER (WHERE NOT cache_hit)::bigint AS requests,
       count(*) FILTER (WHERE cache_hit)::bigint AS cache_hits,
       count(*) FILTER (WHERE status <> 'ok')::bigint AS failures,
       coalesce(sum(tokens_in), 0)::bigint AS tokens_in,
       coalesce(sum(tokens_out), 0)::bigint AS tokens_out,
       coalesce(avg(latency_ms) FILTER (WHERE NOT cache_hit), 0)::integer AS avg_latency_ms
FROM llm_call
WHERE llm_call.time >= sqlc.arg(since)::timestamptz
GROUP BY 1, 2, 3
ORDER BY 1 DESC, 2, 3;

-- name: DeleteLLMCallsBefore :execrows
DELETE FROM llm_call
WHERE id IN (
    SELECT c.id FROM llm_call AS c
    WHERE c.time < sqlc.arg(before)::timestamptz
    ORDER BY c.id
    LIMIT sqlc.arg(batch_size)::int
);

-- name: GetLLMCache :one
SELECT provider, model, response FROM llm_cache
WHERE key = sqlc.arg(key) AND expires_at > sqlc.arg(now)::timestamptz;

-- name: PutLLMCache :exec
INSERT INTO llm_cache (key, feature, provider, model, response, created_at, expires_at)
VALUES (sqlc.arg(key), sqlc.arg(feature), sqlc.arg(provider), sqlc.arg(model), sqlc.arg(response), sqlc.arg(created_at), sqlc.arg(expires_at))
ON CONFLICT (key) DO UPDATE
SET feature = EXCLUDED.feature, provider = EXCLUDED.provider, model = EXCLUDED.model,
    response = EXCLUDED.response, created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at;

-- name: DeleteLLMCacheExpired :execrows
DELETE FROM llm_cache
WHERE key IN (
    SELECT c.key FROM llm_cache AS c
    WHERE c.expires_at <= sqlc.arg(before)::timestamptz
    LIMIT sqlc.arg(batch_size)::int
);
