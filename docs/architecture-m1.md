# Stockpot M1 architecture: catalog, inventory, shopping, matching, alerts, backups

Status: decided by the M1 architect thread 2026-11-27. Extends `architecture.md` (M0, committed as `docs/architecture.md`); commit this file as `docs/architecture-m1.md` in m1-1. Design is final: mockups https://claude.ai/artifact/92etCtPUKkamR5t6zDvn4s (read db collections `reviews` and `reviews2` for AJ's per-screen comments), design system https://claude.ai/artifact/HdzdTKRDEW71WXVL4u7mmF, exports in `/mnt/project-files/stockpot/design/`.

## 1. M1 scope

M1 = everything the household needs to keep the kitchen in Stockpot by hand, offline, with alerts and backups:

- Catalog: categories, canonical **ingredients** (global seed + household), household **products** (what we buy: name, brand, store, usual size) linked to ingredients, stores, locations.
- Inventory: stock items with exact / portions / rough-level amounts, expiry (given or estimated from food rules), open/thaw, move, split into portions, consume, discard, Low in one tap; item timeline.
- Shopping list (pulled forward from M3 because AJ asked for it now): add with **history-learned autocomplete and a guess list**, grouping by category, stores, check off → inventory. Shopping mode (current store, scan to tick) stays M3.
- Ingredient matching: every product resolves to a canonical ingredient (deterministic resolver, then an LLM enrichment job that only spends **spare** quota). M4 recipe matching uses it.
- Food icons, in-app + Telegram expiry alerts, nightly Telegram backups.
- Offline-first sync engine (architecture §10) for all of the above.

Out of M1: barcode/receipt/text capture (M2), shopping mode (M3), recipes (M4), MCP (M5).

## 2. Sync engine (m1-1)

- **Writes are commands only.** `POST /api/v1/commands` takes `{commands: [{id, type, payload, clientTime}]}` (max 100) and answers per command `applied | duplicate | rejected{problem}`. Each command runs in its own transaction: `applied_command(id pk, household_id, user_id, type, client_time, applied_at, status, problem jsonb)` insert first (conflict → `duplicate`), then the handler, then `audit.Record` (action = command type, e.g. `inventory.stock.consume`). Household from `identity.HouseholdID(ctx)` only.
- Go registry: `commands.Register[P any](r, "inventory.stock.consume", handler func(ctx, tx pgx.Tx, p P) error)`. Payload JSON is decoded strictly (unknown fields rejected) and validated by the handler. OpenAPI documents each payload as a schema under `components/schemas/Cmd*` with a `oneOf` + `discriminator` on `type`, so the TS `CommandPayloads` map is generated, not hand-written.
- Created entities use **client-generated UUIDv7 ids** in the payload; handlers are idempotent on id.
- Quantity changes are deltas; other fields last-writer-wins in apply order. A command against a deleted row → `rejected` with code `gone`; the UI shows rejected outbox items in a "Couldn't sync" sheet with discard/retry.
- **Reads**: `GET /api/v1/sync?since=<rev>&limit=1000` returns `{rows: {<table>: [...]}, tombstones: {<table>: [ids]}, next: <rev>, more: bool}` over the synced tables below, filtered `household_id = $hh` (and `OR household_id IS NULL` for global catalog rows). Rows are ordered by `rev`; the cursor is the max rev returned. **Commit order must equal rev order** or a slow transaction could commit a lower rev after a client advanced past it, so every transaction that writes household-synced rows first takes `pg_advisory_xact_lock(hashtext('hh:'||household_id))` (helper `syncdb.LockHousehold(ctx, tx, hh)`, used by command handlers, jobs like the enricher, and admin writes). Writes per household are low volume, so serializing them is cheap. Global catalog rows have their own lock (`'hh:global'`) and their own cursor: the request is `?since=<rev>&since_global=<rev>` and the response returns both `next` and `next_global`.
- Server pushes "something changed" via **SSE** `GET /api/v1/sync/events` (household channel from Postgres `LISTEN sync_<hh>` notified by a statement trigger); clients pull on event, on focus/online, and every 60 s while visible. No data in events.
- **Client** (`web/src/offline/`): Dexie `version(2)` adds one table per synced server table (same names, camelCase fields, primary key `id`, indexes listed in m1-1), plus `meta.sync.rev`. `syncNow()` = flush outbox (in `seq` order, stop on network error) then pull until `more=false`. **Optimistic apply**: each command type has a client reducer that updates the replica immediately; the next pull overwrites with server truth. UI reads only the replica (`useLiveQuery`), never the API, for household data. Household switch or logout clears the replica.
- Synced tables (M1, all created by m1-1's migration so parallel threads never bump Dexie or the sync table list): `category, ingredient, store, location, product, product_barcode, product_stat, stock_item, stock_event, shopping_list, shopping_item`.

## 3. Data model (synced tables; m1-1 migration)

All household-owned tables carry `id uuid pk, household_id, created_at, updated_at, rev, deleted_at` and the rev trigger. A test must prove the lock: two concurrent writers, a reader between their commits, no row missed. Global rows (`household_id IS NULL`) exist only in `category` and `ingredient` and are written only by seed migrations or the admin.

- `category`: `slug, name, icon, sort`. Seed (global): produce-fruit, produce-veg, herbs, dairy-eggs, meat, seafood, deli, bakery, pantry (grains/pasta/canned), baking, spices-condiments, oils-sauces, frozen, snacks-sweets, drinks, household, personal-care, other.
- `ingredient`: `slug, name, plural, parent_id (self, nullable), category_id, icon, aliases text[]` (synonyms, regional names, common misspellings, Persian names), `shelf_life jsonb` (`{fridge,freezer,pantry,opened_fridge,thawed_fridge}` in days, nulls allowed), `default_unit, source (seed|household|llm|admin)`. Unique `(coalesce(household_id,'0'), slug)`. Hierarchy is "is a kind of": `clementine → mandarin → citrus`, `greek-yogurt → yogurt`, `chicken-thigh → chicken`. Indexes: GIN trigram on `name` and on `immutable_unaccent(array_to_string(aliases,' '))`.
- `store`: `name, sort, color` (household). Seed none; the UI offers Costco, No Frills, Loblaws, Walmart, Farm Boy as quick picks.
- `location`: `name, kind (fridge|freezer|pantry|other), sort`. Seed Fridge, Freezer, Pantry on household create (and backfill existing).
- `product`: what the household buys. `name` (display, e.g. "Mandarins"), `brand` (nullable, e.g. "Kirkland"), `ingredient_id` (nullable), `extra_ingredient_ids uuid[]` (e.g. "stir-fry veg mix"), `category_id`, `icon` (nullable override), `default_store_id`, `default_qty numeric, default_unit`, `package text` ("2 lb bag"), `default_location_kind`, `shelf_life jsonb` (override), `match_source (none|rule|llm|user|barcode)`, `match_confidence real`, `enrich_state (pending|done|failed|skipped)`, `enrich_attempts int`, `off_code`, `note`. Unique per household on `(lower(name), lower(coalesce(brand,'')))` where not deleted.
- `product_barcode`: `product_id, code` unique per household.
- `product_stat`: server-maintained purchase history per `(product_id, store_id nullable)`: `times_bought, last_bought_at, typical_qty, typical_unit` (mode of last 10), `median_interval_days` (of last 10 purchases), `last_qty`. Updated by `shopping.item.check` and `inventory.stock.add` (source shopping/manual) in the same tx. This is what autocomplete and the guess list read.
- `stock_item`: `product_id, location_id, amount_kind (exact|portions|level), quantity numeric, unit, portions int, level (full|most|half|low|almost_out)`, `low bool`, `expires_on date, expiry_kind (use_by|best_before|estimated)`, `opened_on, thawed_on, frozen_on date`, `source (manual|shopping|capture|cook|split)`, `parent_item_id` (split), `note`. Rough level never forces counting.
- `stock_event`: immutable ledger: `stock_item_id, kind (add|consume|discard|move|adjust|open|thaw|freeze|split|low), delta numeric, unit, from_location_id, to_location_id, reason, actor_id, command_id`. Waste tracking later reads `discard`.
- `shopping_list`: `name, sort`; seed "Groceries" per household.
- `shopping_item`: `list_id, product_id nullable, text` (what was typed if no product), `qty, unit, category_id, store_ids uuid[]` (any of these stores), `note, sort, checked_at, checked_by, stock_item_id` (set when moved to inventory).

Non-synced tables belong to their feature thread's own migration: `food_rule` (m1-2), `enrich_run` (m1-3), `notification, notification_channel, telegram_link` (m1-5), `backup_run` (m1-6).

**Migrations**: goose here rejects out-of-order versions. Each thread takes the **next free number at rebase time** right before merging (renumber the file if main moved). Expand-then-contract only.

## 4. Units and amounts

`unit` is a code from a fixed Go+TS table (no DB table in M1): mass g, kg, lb, oz; volume ml, l, tsp, tbsp, cup, fl_oz; count `ea`, plus package words (`pack, bag, can, jar, bottle, box, bunch, head, clove, slice, portion`) as count units. Conversion only within a dimension; product-level conversions (1 can = 400 g) arrive with recipes in M4. Quantities are `numeric` on the server and decimal strings on the wire.

## 5. Ingredient matching (m1-3)

Goal: "good mandarins", "Kirkland mandarins 2lb", "manderins" and "X brand avocado" resolve to ingredient `mandarin` / `avocado`, so M4 recipes needing mandarin or avocado match them.

**Deterministic resolver** (`internal/catalog/resolve`, pure Go plus one trigram query; same code serves products now and recipe lines in M4):
1. Normalize: NFKC, strip diacritics, lowercase, punctuation → space.
2. Strip quantity/size/package tokens (`2 lb`, `500g`, `x6`, `pack of 12`), the product's brand and known store names, and descriptor stopwords (good, fresh, organic, large, small, jumbo, ripe, local, premium, seedless, family size, value, brand, bag, pack, box, …; list in code with tests). Keep words that change the ingredient (baby spinach stays a spinach child if seeded; "frozen" is stripped but sets location hint freezer).
3. Singularize (rules + exceptions: leaves→leaf, tomatoes→tomato, cous cous unchanged).
4. Match, longest phrase first over token n-grams: exact slug/name/plural/alias in household ∪ global ingredients (confidence 1.0 for the whole string, 0.9 for a sub-phrase), else trigram similarity on name+aliases ≥ 0.45 (confidence = similarity; picks "mandarin" for "manderin").
5. Result `{ingredient_id, confidence, location_hint, extracted_brand}`. Confidence ≥ 0.85 → `match_source=rule, enrich_state=done`; lower or no match → set the best guess (if any) and `enrich_state=pending`.

The resolver runs inside `catalog.product.create/update` handlers whenever the name or brand changes and `match_source != user`. A user pick in the UI ("Matches recipes as: Mandarin", editable chip on item detail) sets `match_source=user` and is never overwritten.

**Matching semantics (for M4 and the match API)**: a recipe ingredient I matches product P when P.ingredient_id or any of P.extra_ingredient_ids is I or a descendant of I (recursive CTE over `parent_id`). `GET /api/v1/catalog/match?q=<text>` returns the resolved ingredient plus in-stock products that match; used by tests now and recipes later.

**LLM enrichment job** (fills the gaps, never blocks a user):
- New `llm.Request.Priority`: `Interactive` (default) or `Background`. The router serves a Background request only if, for the chosen provider, remaining daily requests and tokens are above a reserve (`llm.background_reserve_pct`, default 40% of the cap, and never when the global cap has less than that share left); otherwise it returns `llm.ErrDeferred` (wraps ErrUnavailable) without calling. Plus `Router.Headroom(ctx, needs) (ok bool, retryAt time.Time)` so jobs can skip cheaply. Interactive features are therefore never starved by enrichment.
- River periodic job `catalog.enrich` (interval setting `enrich.interval`, default 20 min; flag `flag.catalog_enrich`, default on; unique per run). Each run: if no headroom, exit. Else take up to `enrich.batch_size` (15) products with `enrich_state=pending` (oldest first, across households), build one request: per item the raw name, brand, store, and its top 8 trigram ingredient candidates (slug+name); the allowed category slugs; the allowed icon keys. Up to `enrich.max_calls_per_run` (4) calls per run, re-checking headroom between calls.
- Structured output (JSON schema, strict): per item `{ref, ingredient_slug | new_ingredient{name, plural, parent_slug, category_slug, aliases[]}, extra_ingredient_slugs[], category_slug, icon, brand, display_name, shelf_life{fridge,freezer,pantry,opened_fridge}, confidence}`. Validate every field against the closed lists; an invalid item stays pending with `enrich_attempts+1`; at 3 attempts → `failed` (the rule result stands). New ingredients are created household-scoped with `source=llm` and must have a valid parent or category.
- Apply in a tx per item as actor `system:enricher` with `audit.Record`; never touch fields the user set (`match_source=user`, user-set icon/category). Only name/brand text goes to the LLM, no household data. Cache: feature `catalog.enrich` with 180-day TTL on the normalized single-item request so repeated names across households cost nothing.
- Admin → Catalog page: counts by `enrich_state`, last runs (`enrich_run`: started, items, calls, provider, outcome, deferred reason), "Run now", "Retry failed", list of `source=llm` ingredients with promote-to-global / merge / delete, low-confidence products for review.

## 6. Autocomplete and guess list (m1-4)

Runs **client-side** over the replica (instant, works offline). Engine in `web/src/features/catalog/suggest/`, pure TS with no DOM, unit-tested with table tests.

- Index entries: (a) each household product × each store it was bought at (from `product_stat`, plus a store-less entry), label "Mandarins · Kirkland · Costco · 2 lb bag"; (b) every ingredient (global + household) that has no product yet, label "Mandarin" with a "new" hint.
- Query parse first: leading/trailing quantity+unit (`2kg`, `x3`, `3 `), a store token (`@costco`, `from costco`, or a word fuzzy-matching a store name) which filters/boosts that store.
- Text score per entry, over fields name, plural, brand, aliases, ingredient name: for each query token take the best of (word prefix match 1.0), (Damerau-Levenshtein ≤ 1 for tokens of 4–7 chars, ≤ 2 for 8+, on the word or its same-length prefix: 0.85), (trigram Dice ≥ 0.4: Dice). Every token must match some field; entry score = mean token score.
- Rank = text × (1 + ln(1 + times_bought)) × recency (half-life 60 days on last_bought_at) + small boost for the parsed store. Ingredients-without-product rank below products at equal text score. Top 6.
- UI: the first suggestion shows as **inline ghost completion** when its score clears a threshold (Tab/→ or tapping the ghost accepts); the dropdown lists the rest. Accepting fills the whole item: product, store(s), qty+unit (parsed qty wins, else `typical_qty/unit`), category. Enter with no suggestion accepted adds free text (`product_id` null; server creates/links a product on check-off).
- Required test cases: "manderins" → Mandarins (Costco, 2 lb) when bought there before; "mand" → same; "avo" → Avocado; "costco mand" and "mand @costco" → the Costco entry; "2 manderins" → qty 2; "kirkland" → Kirkland products; "oat milk" vs "milk" ranks the exact phrase first; unseen "pomegranite" → ingredient Pomegranate (new).
- **Guess list** ("Suggested" chips above the input before typing): products whose `last_bought_at + median_interval_days × 0.9 ≤ today` (≥ 2 purchases), stock items marked low or consumed to zero in the last 14 days, and expired staples; minus items already unchecked on the list; max 12, ordered by how overdue. One tap adds with usual store/qty.
- `ProductPicker` (`web/src/features/catalog/ProductPicker.tsx`) wraps the engine and is shared by shopping add and inventory add. Contract: `props {context: 'shopping'|'inventory', onPick(p: PickedItem), autoFocus?}`, `PickedItem {productId?: string, ingredientId?: string, name: string, brand?: string, storeIds: string[], qty?: string, unit?: string, categoryId?: string}`. m1-1 ships a basic substring version; m1-4 replaces the engine behind the same contract.

## 7. Food icons (m1-7)

- **Fluent Emoji** by Microsoft (MIT, https://github.com/microsoft/fluentui-emoji), via `@iconify-json/fluent-emoji-high-contrast` and `@iconify-json/fluent-emoji-flat`. ~140 Food & Drink glyphs (tangerine, avocado, leafy-green, ginger-root, garlic, onion, bell-pepper, hot-pepper, canned-food, jar, butter, glass-of-milk, cheese-wedge, cut-of-meat, poultry-leg, bacon, shrimp, lobster, oyster, cooked-rice, spaghetti, baguette-bread, croissant…) plus household objects (roll-of-paper, soap, sponge, lotion-bottle, toothbrush, broom). Best coverage of any permissive set we checked (Tabler, IconPark, Mingcute, Phosphor, Material, Health Icons all miss 40–60 of 96 grocery staples).
- Default style **High Contrast** (monochrome, takes `currentColor`) so thumbs match the final design (ink glyph on `surface-2` tile). **Flat** (colour) is a per-user display preference "Colourful food icons" (default off), same keys.
- Gaps (tofu, flour, sugar, yogurt, sauce, cereal, spices) fall back to Tabler outline (MIT, already in the design) under the same registry.
- Registry `web/src/ui/food-icons/registry.json` (also embedded into Go at `internal/catalog/icons.json` via `go generate` copy, checked in CI): key → `{set: "fe"|"tb", name, label, tags[]}`. Keys are stable slugs (`tangerine`, `avocado`, `tb-bottle`). Seeds and the LLM may only use registry keys. Icon resolution: product.icon → ingredient.icon → ancestor icon → category.icon → `basket`.
- Build: a script emits one SVG sprite per style with only registry keys (no runtime CDN), `FoodIcon` component `<FoodIcon k="tangerine" size={22} />`. Attribution in `web/THIRD_PARTY.md` and the About screen.

## 8. Alerts (m1-5)

- `notification(user_id, household_id, kind, title, body, payload jsonb, dedupe_key unique, created_at, read_at, sent_telegram_at)`, `notification_channel(user_id, kind telegram, chat_id, enabled, quiet_hours, digest_time)`, `telegram_link(code, user_id, expires_at)`. In-app inbox = REST (`GET /v1/notifications`, mark read), not synced.
- Periodic job `alerts.expiry` hourly: per household, items expiring within `alerts.lead_days` (household setting, default 2) or expired today, opened-item rules from food rules; one **digest** per user per day at their digest time (default 08:00 America/Toronto), dedupe by `(user, day)`. Low items go into the digest too.
- Telegram: one Stockpot bot (token setting `telegram.bot_token`, secret). Linking: user taps "Connect Telegram" → deep link `t.me/<bot>?start=<code>` → long-polling worker (`getUpdates`, single consumer, offset stored in a setting) matches the code and stores `chat_id`. Backups use the same client (§9). Messages are short with a link back to the app.

## 9. Backups (m1-6)

- Nightly River job `backup.run` (time setting, default 03:30 America/Toronto; flag `flag.backups`, default off until a channel is set): `pg_dump --format=custom` of the app DB + a tar of `$STATE_DIRECTORY/blobs`, streamed through zstd and **age** encryption (recipient public key in setting `backup.age_recipient`; the private key never touches the server; AJ keeps it), split into parts ≤ 45 MB (bot upload limit 50 MB), uploaded with `sendDocument` to `backup.telegram_chat_id` with a caption manifest (run id, part i/n, sha256). A final message holds the manifest JSON (parts, file_ids, sizes, hashes, app version, migration version). `backup_run` records status, size, duration, error.
- Retention: keep last `backup.keep` (14) runs; older messages deleted via `deleteMessage` where the API allows (48 h limit means old ones may stay; record that).
- Restore: `stockpot restore --manifest <message-link|file> --identity <age key file>` downloads parts with `getFile`, verifies hashes, decrypts, `pg_restore --clean` into an empty DB and unpacks blobs. Documented in `docs/restore.md`; tested end-to-end in CI against a fake Telegram server.
- Admin → Backups page per mockup: last runs, sizes, "Back up now", channel status, restore instructions. nixConfig needs `pg_dump`/`pg_restore` on the unit path (thread opens a nixConfig PR like Rally's).
- Use the Go `filippo.io/age` library and `klauspost/compress/zstd` (no extra binaries besides pg_dump/pg_restore).

## 10. Task split

| Brief | Depends on | Wave |
|---|---|---|
| m1-1 sync + catalog core | M0 | 1 (now) |
| m1-6 backups + Telegram client | M0 | 1 (now) |
| m1-7 food icons | M0 | 1 (now) |
| m1-2 inventory | m1-1 | 2 |
| m1-3 ingredient matching + enrichment | m1-1 | 2 |
| m1-4 shopping list + autocomplete | m1-1 (m1-7 for icons, optional) | 2 |
| m1-5 alerts + Telegram linking | m1-1, m1-6 (telegram pkg) | 2 |

Shared-file rules for parallel threads: OpenAPI paths go in the thread's own tag section; command payload schemas are named `Cmd<Area><Thing><Verb>`; each thread registers its command handlers from its own package (`internal/inventory`, `internal/shopping`, …) in `app.go` (one line each); client reducers live in `web/src/features/<area>/commands.ts` and register via `registerReducer(type, fn)`.

## Implementation notes (m1-1)

- `CommandBatch` is household-bound: the client sends `householdId`; a mismatch with the session household is a 409 `household_mismatch`. The outbox stamps each command with its household, parks other households' commands on switch (never sends them to the wrong one) and replays them on return.
- Delta reducers (e.g. "add 1 to qty") must declare `targets` in `registerReducer(type, fn, {targets})`. After a pull the engine replays only queued commands whose targets the page touched, so a delta is applied exactly once on top of server state. Reducers without targets are replayed every pull.
- Delete rules: a location with live stock cannot be deleted (`invalid`); deleting a store detaches it from products' default store and shopping items' `store_ids`.
- Offline-created duplicate product names are not merged server-side; later commands referencing a rejected create fail and surface in the "Couldn't sync" sheet (m1-4 should resolve by name before create).
- Seed ingredients: rows with no parent have their parent cleared on reseed; seed never deletes. `opened_fridge`/`thawed_fridge` shelf-life values are not in the seed; the m1-2 `food_rule` is expected to cover them.
- A command that makes the server fail (400/5xx) is isolated by sending it alone, and set aside as rejected after 5 failed attempts so it cannot block the queue.

## Implementation notes (m1-5)

- Package `internal/alerts`. Job `alerts.expiry` runs every 15 minutes (not hourly: digest times are HH:MM), unique so only one run at a time: it creates due digests, then delivers unsent notifications to Telegram. `flag.alerts` (default on) gates it.
- Settings are system scope (the registry has no household scope yet): `alerts.lead_days`, `alerts.digest_time`, `alerts.quiet_start`, `alerts.quiet_end`, `retention.notification_days`. A user's own values live in `notification_pref` and override them one field at a time (a PUT stores only the fields sent); the digest is per user and household.
- Dedupe: `digest_run (user, household, local day)` is the claim, so an empty day is evaluated once and a digest is never created late in the day. A digest is only created within 6 h after its time (the window may cross midnight). `notification.dedupe_key` is a second guard.
- Groups: expired = up to 7 days before today (items expired longer ago are only counted as "+ N older expired" and never make a digest alone; groups are capped at 8), expiring = today through today + lead days, low = flagged low and not in another group. Food-rule adjustments for opened items are expected to be already reflected in `stock_item.expires_on` (m1-2).
- Telegram delivery: 3 attempts per message with backoff (Telegram's `retry_after` up to 30 s), then retried by later runs for 24 h. Messages are held during quiet hours (inbox is not). A refused chat (403, or 400 `chat not found`) disables the channel; other 400s are logged and the channel stays on. Texts are cut to 4096 characters. Disabled users get no digests, deliveries, `/expiring` or link redemption with `last_error` until the user reconnects. Only notifications created after the chat was linked are sent.
- Linking: single-use code (hash stored, 15 min) → `t.me/<bot>?start=<code>`. `RunBot` long-polls `getUpdates` in a goroutine; one consumer across processes via a session advisory lock; the offset is stored in `telegram_poll` after each update. A Telegram chat belongs to one user. Commands: `/start <code>`, `/stop`, `/expiring`.
- Link/unlink are audited (`telegram.link`, `telegram.unlink`). Admin → Settings shows `GET /v1/admin/telegram` (getMe, linked chats, poller health).
