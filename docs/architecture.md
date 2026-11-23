# Stockpot architecture (v1, M0 detailed)

Status: decided 2026-11-23. Cheap-to-change choices are final unless AJ objects.

## 1. Shape

One Go binary (`stockpot`) serves a JSON API on a unix socket; Caddy on cardea serves the built PWA from the release's `web/` directory and proxies `/api/*` to the binary. Postgres is the only datastore. Everything the household and admin do goes through the typed OpenAPI contract.

```
Browser PWA (React, IndexedDB outbox)
   │ HTTPS stockpot.jamadi.me
Caddy ── /api/* ──► stockpot serve (socket-activated, sandboxed, static binary)
   │                    ├── Postgres (peer auth, /run/postgresql)
   └── web/ (static)    ├── River job workers (same process)
                        └── outbound HTTPS: LLM providers, Open Food Facts, recipe sites, Telegram
```

## 2. Hosting contract (from nixConfig `modules/hosted/apps`)

- Release: GitHub Release with tag `r<run_number>` (updater rejects anything not matching `^r[0-9]+$`), assets `stockpot.tar.gz` + `stockpot.tar.gz.sig`, signed `ssh-keygen -Y sign -n stockpot-release` with repo secret `RELEASE_SIGNING_KEY` (shared with Rally for now). Tarball root: `./stockpot` (binary) and `./web/` (Vite dist).
- Binary must be **static** (`CGO_ENABLED=0`): it runs from `/var/lib/stockpot-updater/releases/current` inside a confinement chroot that only holds the unit's store closure.
- Process: `run = "stockpot serve"`, `preStart = "stockpot migrate"`, `health = "/api/health"`. The unit is **socket-activated** (`/run/sandboxed/stockpot.sock` arrives as `LISTEN_FDS`); never bind in prod. Dev fallback: `STOCKPOT_LISTEN=:8080`.
- `postgres = true` (DB `stockpot`, peer auth: `host=/run/postgresql user=stockpot dbname=stockpot`), `internet = true`, `webhook = true`, `sandbox.state = true` (blob storage under `$STATE_DIRECTORY`).
- Extra binaries the app shells out to (later: `tesseract` for receipts) are added in nixConfig via `systemd.services.stockpot.path`, as Rally does for `pg_dump`.
- Secrets: `secrets = [ "secret-key" "admin-setup-token" ]` → `$CREDENTIALS_DIRECTORY/<name>`, referenced by `STOCKPOT_SECRET_KEY_FILE=%d/secret-key`, `STOCKPOT_ADMIN_SETUP_TOKEN_FILE=%d/admin-setup-token`.
- Deploy rollback: health check fails → updater rolls back. `/api/health` returns 200 only when DB is reachable and migrations are at the embedded version.

## 3. Repo layout

```
cmd/stockpot/            main: subcommands serve, migrate, version (stdlib flag)
api/openapi.yaml         single source of the HTTP contract (OpenAPI 3.1)
internal/
  app/                   wiring (construct services, start server + workers)
  config/                bootstrap env parsing + validation
  httpapi/               oapi-codegen strict server (gen.go), handlers, middleware
  db/                    pgx pool, goose runner; migrations/ (embedded .sql); queries/ (.sql) → dbq/ (sqlc)
  settings/              typed runtime settings + flags registry, DB store, secret encryption
  logging/               slog setup, DB sink handler, request-id
  jobs/                  River client/workers, periodic jobs (retention, …)
  identity/              users, households, memberships, invites, sessions, authz policy
  identity/passkey/      WebAuthn provider (go-webauthn)
  audit/                 audit + access log writer and queries
  llm/                   provider interface, router, metering, cache, caps
  llm/openaicompat/      first adapter
  blob/                  BlobStore interface + filesystem impl ($STATE_DIRECTORY/blobs)
  (M1+) catalog/ inventory/ shopping/ recipes/ capture/ notify/
web/                     Vite + React + TS PWA (pnpm)
  src/api/               generated types (openapi-typescript) + openapi-fetch client
  src/app/ src/features/<area>/ src/admin/ src/ui/ src/offline/
nix/                     package.nix (Go + web derivations, tarball), checks
flake.nix                devShell, packages, checks, formatter (treefmt-nix)
.github/workflows/       ci.yml (PRs), release.yml (main)
docs/architecture.md
```

## 4. Toolchain

- **Go** (current stable in nixpkgs), modules, `go test ./...`. Lint: golangci-lint (default + `errcheck, gosec, revive, sqlclosecheck, bodyclose`). Format: gofumpt + goimports.
- **Web**: Node LTS + pnpm, Vite, React 19, TypeScript strict, React Router (data routers), TanStack Query, Tailwind v4 with designer tokens as CSS variables, Radix primitives in owned components (shadcn-style), Dexie (IndexedDB), vite-plugin-pwa (Workbox). Lint+format: Biome. Tests: Vitest + Testing Library; Playwright e2e later.
- **Nix**: `nix develop` gives go, gopls, golangci-lint, sqlc, goose, oapi-codegen, node, pnpm, postgresql, biome, treefmt. `nix fmt` = treefmt (gofumpt, nixfmt, biome). `nix flake check` = builds, go vet + golangci-lint, biome check, `git diff --exit-code` after codegen. `nix build .#tarball` produces the release tarball (web via `pnpm.fetchDeps`).
- **Codegen is committed** (sqlc, oapi-codegen, openapi-typescript); CI fails if regenerating changes anything (`make gen` equivalent: `go generate ./...` + `pnpm gen`).
- **Tests needing Postgres**: `internal/testdb` starts a throwaway cluster (`initdb` in a temp dir, unix socket) once per `go test` run if `STOCKPOT_TEST_DATABASE_URL` is unset; each test gets its own database from a migrated template. Works in dev shell and CI identically.
- **CI**: GitHub Actions with `cachix/install-nix-action` + magic-nix-cache; PR: `nix flake check` + `nix develop -c go test ./...` + `pnpm test`. Push to main: same, then `nix build .#tarball`, sign, publish release `r${{ github.run_number }}`.

## 5. Backend

- **HTTP**: chi v5 router; oapi-codegen strict-server interfaces generated from `api/openapi.yaml`. Routes under `/api/v1`, plus `/api/health`. Errors are RFC 9457 `application/problem+json` with a stable `code`. IDs are UUIDv7 (clients may generate them, which the offline outbox needs). Times are RFC 3339 UTC; expiry dates are `date`.
- **Middleware order**: request-id → recover → real-IP (trust Caddy `X-Forwarded-For` only from the unix socket peer) → access log → session load → CSRF (Origin check on unsafe methods + `SameSite=Lax`) → authz per route (OpenAPI `x-roles` extension mapped to policy) → handler.
- **Postgres**: pgx v5 pool; sqlc for queries (typed, no ORM); goose for migrations, SQL files embedded with `embed`. `stockpot migrate` runs goose up under an advisory lock; `serve` refuses to start if DB is behind. Conventions: every household-owned table has `household_id`, `created_at`, `updated_at`, `rev bigint` (from sequence `change_rev`, set by trigger on insert/update) and soft-delete `deleted_at` (tombstone) so offline sync can pull deltas.
- **Config split**:
  - *Bootstrap env* (only what's needed before the DB is reachable or must stay outside it): `STOCKPOT_DATABASE_URL`, `STOCKPOT_PUBLIC_URL`, `STOCKPOT_LISTEN` (dev), `STOCKPOT_SECRET_KEY_FILE`, `STOCKPOT_ADMIN_SETUP_TOKEN_FILE`, `STOCKPOT_LOG_LEVEL` (startup default).
  - *Runtime settings* (DB table `setting`, edited in admin panel, cached in memory, invalidated with Postgres `LISTEN/NOTIFY`): a Go registry declares each key with type, default, validation, description, `secret` bool, and scope (`system` now, `household` later). Flags are boolean settings under `flag.*`. Secret settings (API keys, bot tokens) are encrypted with AES-256-GCM using a key derived (HKDF) from `secret-key`; admin UI shows them write-only.
- **Logging**: `log/slog` JSON to stdout (journald) plus a DB sink handler: async, batched, bounded queue (drops and counts on overflow), writes `app_log` (level, msg, attrs jsonb, request_id, user_id). Runtime log level is a setting. Retention 14 days (setting).
- **Background jobs**: River (Postgres-backed, transactional enqueue, periodic jobs, retries) running in-process. Admin panel lists/retries/cancels jobs by querying River's tables via its client API. M0 periodic jobs: retention sweeps (logs, access log, sessions, expired invites, LLM cache).
- **Blobs**: `blob.Store` interface (Put/Get/Delete by content hash); filesystem impl at `$STATE_DIRECTORY/blobs`. Used from M2 for receipt photos.

## 6. Identity

- **Provider adapter**: `identity.Provider` interface. Each provider turns a ceremony into a verified `(provider, subject)` and the core maps it to a user via `identity_link(user_id, provider, subject, data jsonb)`.

```go
type Provider interface {
    ID() string                                   // "passkey", later "oidc:<name>", "ldap"
    Kind() Kind                                   // Interactive ceremony vs Redirect vs Password
    BeginLogin(ctx, LoginStart) (Challenge, error)
    FinishLogin(ctx, LoginFinish) (Identity, error)
    BeginEnroll(ctx, EnrollStart) (Challenge, error)   // add credential to existing/new user
    FinishEnroll(ctx, EnrollFinish) (Identity, error)
}
```
Passkeys first (go-webauthn, resident keys, user verification required, RP ID `stockpot.jamadi.me`). OIDC (one SSO for all AJ's apps) or LDAP slot in as new providers plus admin settings; a user can link several.
- **Sessions**: opaque 256-bit token in cookie `__Host-stockpot_session` (Secure, HttpOnly, SameSite=Lax, Path=/); DB stores SHA-256 of token, user, created/last-seen, IP, UA, expiry (30 days sliding, setting). Admin can list/revoke. No JWTs.
- **Bootstrap**: when no admin exists, `/setup` accepts the `admin-setup-token` credential once and enrolls the first passkey as system admin + creates the first household.
- **Household model**: `household`, `membership(user_id, household_id, role owner|member)`. A user can belong to several households (one active, chosen in UI); every domain query is scoped by the active household id from the session.
- **Invites + approval**: an admin or household owner creates an invite (single-use code, expiry 7 days, target household + role, optional note). Invitee opens the link, enrolls a passkey → user `status = pending`. Admin approves or rejects in the panel (setting `auth.invite_requires_approval`, default true). Pending users can only see a waiting screen. Users can be disabled (sessions revoked).
- **Roles**: system role `admin | user`; household role `owner | member`. Authorization lives in `identity/policy.go` as explicit functions; routes declare required roles via `x-roles` in OpenAPI.

## 7. Audit and telemetry

- `access_log`: every API request (time, request id, user, session, household, method, route pattern, status, latency, IP, UA). Retention 90 days (setting). Written async in batches.
- `audit_event`: every security event and mutation (login ok/fail, enroll, invite create/accept, approve/reject, setting change, role change, session revoke, and from M1 every domain mutation): actor, household, `action` (`inventory.item.consume`), target type/id, before/after diff (jsonb), request id. Retention 2 years (setting). Written in the same transaction as the change via `audit.Record(ctx, tx, ...)`.
- Telemetry ("who did what when") = queries over these two tables; no third-party analytics.

## 8. Admin panel (M0)

Inside the same SPA under `/admin`, admin-only. Pages: Overview (version/tag, uptime, DB status, migration version, job queue depth, error count 24h, LLM usage today), Users (pending approvals, invites, users, roles, disable, sessions), Audit log (filter by user/action/time), Access log, System logs (level/text filter, live tail via polling), Jobs (River: list, retry, cancel), Settings & flags (typed editor, secrets write-only, change history from audit), LLM (providers, keys, models, caps, usage per day/feature, test prompt).

## 9. LLM

- Deterministic/free first: Open Food Facts for barcodes, schema.org Recipe JSON-LD for recipe URLs, rule-based parsing for free text ("2L milk fridge"), tesseract OCR for receipts (M2). LLM only fills gaps, and every LLM feature has a manual path.
- Interface: `llm.Provider{ ID(); Capabilities() (vision, jsonSchema, maxContext); Complete(ctx, Request) (Response, error) }`. Request carries messages, optional images (blob refs), JSON schema for structured output, `feature` tag.
- `llm.Router` picks the first enabled provider (ordered list in settings) that has the needed capability and remaining quota; on 429/quota error marks it exhausted until its reset time and tries the next; if none → `llm.ErrUnavailable`, which handlers map to a `503 llm_unavailable` problem the UI shows as "do it manually".
- First adapter: **OpenAI-compatible chat completions** (`llm/openaicompat`), which covers Google Gemini's OpenAI-compatible endpoint, Groq, OpenRouter, Cerebras and GitHub Models with one implementation (base URL + key + model in settings).
- Default config: **Gemini Flash-Lite** (free AI Studio key; vision for receipts, large context) as primary, **Groq** (free, fast, text only, no training on data) as fallback. Free quotas change often (Google no longer publishes free-tier numbers; they show per key in AI Studio, RPD reset at midnight Pacific; Groq about 30 RPM / 1,000 RPD), so caps are settings, not constants. Note: Google may use free-tier prompts for training outside the EU; we send no personal data beyond grocery/recipe text.
- Metering: `llm_call` row per call (time, feature, provider, model, user, tokens in/out, latency, status, cache hit). Daily caps per provider (requests, tokens) and global, enforced before calling; admin sees usage and remaining.
- Cache: `llm_cache` keyed by SHA-256 of (provider-agnostic normalized request); TTL by feature (recipe parse 90 days, substitutions 7 days); retention job prunes.
- M5: an MCP server endpoint (separate auth, off by default flag) lets AJ's own Claude connect on demand; the LLM router is not involved.

## 10. Offline and sync (decided now, built in M1)

- Server is the source of truth. Client keeps a per-household replica in IndexedDB (Dexie) and an **outbox** of commands.
- Writes are **commands** (`POST /api/v1/commands` batch): `{id: uuidv7, type: "inventory.consume", payload, client_time}`. The server applies each idempotently (command id stored in `applied_command`), so retries are safe. Quantity changes are deltas (commutative), other fields last-writer-wins by server apply order; conflicts that can't apply (item deleted) come back as rejected and the UI surfaces them.
- Reads: `GET /api/v1/sync?since=<rev>` returns changed rows and tombstones across household tables ordered by `rev`, plus the new cursor. Initial load = `since=0`.
- Service worker (Workbox via vite-plugin-pwa) precaches the shell; API calls are not cached by the SW, they go through the replica. Background Sync where supported, otherwise flush on `online`/focus.
- M0 delivers only: installable PWA, offline shell, the `rev`/tombstone conventions, Dexie wiring. No domain sync yet.

## 11. Data model sketch (M1–M4)

All household-owned tables carry `household_id, created_at, updated_at, rev, deleted_at`.

- **Catalog (M1)**: `category` (product type for grouping, seeded, household-extensible), `product` (name, category, default unit, default shelf life per location kind, image blob, `off_code`/OFF json snapshot), `product_barcode` (barcode → product, unique per household), `unit` (code, dimension mass/volume/count, factor to base; metric with Canadian common units), `product_unit_conversion` (e.g. 1 can = 400 g).
- **Inventory (M1)**: `location` (name, kind fridge|freezer|pantry|other, sort), `stock_item` (product, location, quantity numeric, unit, expires_on, opened_on, best_before kind, note, source capture/shopping/cook), `stock_event` ledger (item, kind add|consume|discard|move|adjust, delta qty, unit, reason, cook_session_id, actor) — waste tracking later reads `discard` events.
- **Alerts (M1)**: `notification_channel` (user, kind telegram|inapp, config/link state), `notification` (user, kind, payload, read_at, sent_at), periodic job `expiry-alerts` per household with settings for lead days.
- **Capture (M2)**: `capture` (kind barcode|receipt|text|voice, blob, raw text, parse result jsonb, status, provider used), `capture_line` (proposed product/qty/unit/location/expiry, accepted → stock_item).
- **Shopping (M3)**: `store` (name, sort), `store_category_order` (aisle order per store), `shopping_list`, `shopping_item` (product or free text, qty, unit, category, preferred store, checked_at, checked_by, moved_to stock_item).
- **Recipes (M4)**: `recipe` (title, source_url, servings, prep/cook time, instructions jsonb, image, raw JSON-LD, origin manual|url|llm), `recipe_ingredient` (raw text, product nullable, qty, unit, optional flag, group), `recipe_tag`, `cook_session` (recipe, servings cooked, portions eaten now, leftover portions, leftover stock_item id, cooked_by), deductions recorded as `stock_event`s. Suggestions = SQL match of recipe ingredients against stock (expiring-first ranking), LLM only for substitutions and free-form ideas.
- **Later**: `meal_plan`, `par_level`, `price`, dietary prefs on `membership`.

## 12. M0 task split

M0a (already running): scaffold + release pipeline + nixConfig PR. Then:

| Brief | Depends on | Parallel with |
|---|---|---|
| m0-1 backend foundation | M0a merged | m0-4 (web shell) |
| m0-2 identity + audit | m0-1 | m0-3, m0-5 |
| m0-3 admin panel (API + UI) | m0-1, m0-4; m0-2 for users pages | m0-5 |
| m0-4 web app shell + PWA | M0a merged | m0-1 |
| m0-5 LLM gateway | m0-1 | m0-2, m0-3 |

## 13. One-time steps for AJ (beyond M0a's list)

1. On cardea: `head -c 32 /dev/urandom | base64 | install -Dm600 /dev/stdin /var/lib/credentials/stockpot/secret-key` and the same for `admin-setup-token` (keep a copy of the setup token to claim admin once).
2. Get free LLM keys and paste them in Admin → LLM: Google AI Studio API key (aistudio.google.com/apikey) and a Groq key (console.groq.com/keys). Both need an account login, so Claude can't create them.
3. Later (M1, alerts): create a Telegram bot with @BotFather for Stockpot (Rally's bot can't be shared because both would consume its updates) and paste the token in Admin → Settings.
