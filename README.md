# Stockpot

Household kitchen app: fridge, freezer and pantry inventory with expiry dates,
fast capture, shopping lists and recipes. A Go API and a mobile-first,
offline-first React PWA, with a typed OpenAPI contract between them.

## Develop

Everything runs inside the Nix dev shell (`direnv allow`, or `nix develop`).
`make` lists the commands.

```sh
make web-install   # once
make serve         # Postgres in .data/, migrations, API on 127.0.0.1:8080
make web           # PWA on http://localhost:5173, proxying /api
make check         # what CI runs: lint, tests, web checks, formatting
make db-down       # stop the local Postgres when done
```

Spec first: edit `api/openapi.yaml` (every operation needs `x-roles`), or a
query in `internal/db/queries/`, run `make gen` (oapi-codegen, sqlc,
openapi-typescript), then implement. Generated files are committed; CI fails
if they drift.

Go tests that need Postgres get their own migrated database from
`internal/testdb`, which starts a throwaway cluster per test run (initdb
refuses root; set `STOCKPOT_TEST_DATABASE_URL` to an existing server instead).

Design and conventions: [docs/architecture.md](docs/architecture.md).

## Layout

```
cmd/stockpot/        serve | migrate | version
api/openapi.yaml     HTTP contract (OpenAPI 3.1)
internal/app/        wiring: start and stop everything in order
internal/config/     bootstrap settings from STOCKPOT_* (everything else lives in the DB)
internal/httpapi/    generated strict server (api.gen.go), middleware, handlers
internal/db/         pgx pool, embedded goose + River migrations, queries/ → dbq/ (sqlc)
internal/settings/   typed runtime settings, cached, live via LISTEN/NOTIFY, secrets encrypted
internal/logging/    slog JSON to stdout + batched copy into app_log
internal/jobs/       River workers and periodic jobs (retention)
internal/blob/       content-addressed file store ($STATE_DIRECTORY/blobs)
internal/testdb/     throwaway Postgres for tests
web/                 Vite + React + TypeScript PWA (pnpm, Biome, Vitest)
  src/app/           router, app shell (tab bar / side nav), theme
  src/features/      one folder per area (home, inventory, add, shopping, recipes, auth)
  src/admin/         /admin route group
  src/ui/            owned UI primitives (Radix + Tailwind, styled only via tokens)
  src/api/           generated schema.d.ts + openapi-fetch client (problem+json, 401 → /login)
  src/offline/       Dexie DB (meta, outbox) and enqueueCommand()
  src/pwa/           service worker registration, update prompt, offline banner
  src/styles/        tokens.css (design export, verbatim) + Tailwind theme mapping
```

Visual changes go through `web/src/styles/tokens.css`: replace it with a new design
export (drop the Google Fonts `@import`; fonts are self-hosted via Fontsource).

## Release and deploy

Every push to `main` runs CI, then publishes GitHub Release `r<run number>`
with `stockpot.tar.gz` (static Linux binary + built PWA) and
`stockpot.tar.gz.sig` (`ssh-keygen -Y sign`, namespace `stockpot-release`,
key from the `RELEASE_SIGNING_KEY` secret). cardea's updater
(`hosted.apps.stockpot` in nixConfig) verifies, swaps, runs `stockpot migrate`,
restarts, polls `/api/health` and rolls back if it fails. The app is served at
https://stockpot.jamadi.me.

Bootstrap environment (set by nixConfig):

| Variable | Meaning |
| --- | --- |
| `STOCKPOT_DATABASE_URL` | libpq string; empty uses `PG*` (dev) |
| `STOCKPOT_PUBLIC_URL` | public origin, e.g. `https://stockpot.jamadi.me` |
| `STOCKPOT_LISTEN` | TCP address when no socket is passed (dev only; `make serve` sets it) |
| `STOCKPOT_SECRET_KEY_FILE` | systemd credential (≥ 32 bytes), encrypts secret settings; required (`make migrate` creates a dev one) |
| `STOCKPOT_ADMIN_SETUP_TOKEN_FILE` | systemd credential, claims the first admin |
| `STOCKPOT_LOG_LEVEL` | log level until the `log.level` setting loads (default `info`) |
| `STATE_DIRECTORY` | set by systemd; blobs go to `$STATE_DIRECTORY/blobs` (dev: `.data/blobs`) |

Secrets can also be passed directly (`STOCKPOT_SECRET_KEY`) in development;
setting both forms is an error. `serve` refuses to start while migrations are
pending.
