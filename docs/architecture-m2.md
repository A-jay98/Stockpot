# Stockpot M2 architecture: recipes and cooking

Status: decided by the M2 architect thread 2026-11-27. Extends `architecture.md` (M0) and `architecture-m1.md` (M1), both committed under `docs/`. m2-1 commits this file as `docs/architecture-m2.md`. Design is final: mockups https://claude.ai/artifact/92etCtPUKkamR5t6zDvn4s screens **Recipe ideas**, **Recipe page**, **I cooked this** and the Home "Cook tonight" card (read db collections `reviews` and `reviews2` for AJ's comments), design system https://claude.ai/artifact/HdzdTKRDEW71WXVL4u7mmF, exports in `/mnt/project-files/stockpot/design/`.

## 1. Scope and milestone order

The original roadmap had M2 = fast capture, M4 = recipes. AJ agreed (2026-11-27) to do recipes next, so **M2 = recipes and cooking** (old M4). Fast capture (barcode, receipt photo, free text/voice) moves to **M3** together with shopping mode. M2 builds the shared blob/photo pipeline that capture reuses.

In M2:
- Recipe library: saved recipes with **immutable versions** (full history, restore), tags, cover and per-cook photos, offline reading in the kitchen.
- Import: recipe sites (schema.org JSON-LD first, LLM only for pages without it), **Instagram reels** (caption + spoken transcript), photo of a recipe (vision LLM), pasted text, **bulk import** (many links at once). Non-English (Persian) is translated to English and the original is kept and viewable.
- "From your kitchen": saved recipes ranked by what is in stock and what is expiring, Home "Cook tonight" card, substitutions for missing ingredients, on-demand AI ideas.
- **I cooked this**: portions made, scaled amounts, deduct stock with adjustable amounts (exact, portions, rough level), store leftover portions in fridge/freezer with eat-by dates, add used-up items to the shopping list, photo of the cook, undo.
- **Per-recipe chat**: "I tried it with X and it was better" → proposed change → saved as a new version.
- "Missing to list": add a recipe's missing ingredients to the shopping list (items remember the recipe).

Out of M2: meal planner / weekly plan (later list), fast capture and shopping mode (M3), MCP (M5).

## 2. Principles carried over

- Offline-first: household recipe data is synced and read from the replica; writes are commands (M1 §2). Only LLM features (import parsing, chat, AI ideas, LLM substitutions) need the network, and each has a manual path (`503 llm_unavailable` → "do it manually").
- Deterministic first: JSON-LD, the ingredient-line parser, the M1 resolver, seeded substitutions and conversions. LLM fills gaps.
- Quantity changes are deltas; created rows use client UUIDv7 ids; every household write takes `syncdb.LockHousehold`; every mutation calls `audit.Record`.
- Lint + format locally exactly as CI (`nix flake check`, `make gen` clean, `make lint`, `make web-check`, gofumpt via treefmt) before opening or pushing to any PR. AJ's standing rule.

## 3. Data model (m2-1 migration creates ALL of it)

As in M1, one migration in m2-1 creates every synced table and column M2 needs, and bumps Dexie to `version(3)` once, so wave-2 threads never touch the sync table list or Dexie. Feature threads may add only **non-synced** tables in their own migration (next free number at rebase).

New synced tables (household-owned columns `id, household_id, created_at, updated_at, rev, deleted_at` + rev trigger):

- `recipe`: `title, subtitle, source_kind (manual|url|instagram|photo|text|idea), source_url, source_lang` (BCP 47, null = en), `current_version_id`, `servings int, total_minutes int` (denormalized from current version for lists), `cover_blob` (nullable ref), `tags text[]`, `saved bool` (AI ideas start false), `idea_expires_at` (ideas not saved are tombstoned by a job after 7 days), `cooked_count int, last_cooked_at` (server-maintained by `recipe.cook`), `import_id` (nullable). Index trigram on title for search.
- `recipe_version`: **immutable** snapshot. `recipe_id, n int` (unique per recipe, 1..), `parent_version_id, origin (import|manual|chat|restore|idea), change_summary text, title, servings, total_minutes, ingredients jsonb, steps jsonb, notes text, original jsonb` (nullable: `{lang, title, ingredients: [raw], steps: [text], notes}` for translated imports; later versions carry `original_version_id` pointing at the version that holds it), `created_by`.
  - `ingredients[]`: `{line_id uuid, group?, raw, qty?, qty_max?, unit?, name, note?, optional bool, staple bool, ingredient_id?, match_confidence?}`. `qty` decimal string; `unit` from the M1 unit table or null ("2 onions" → qty 2, unit `ea`; "salt to taste" → no qty, optional).
  - `steps[]`: `{text, minutes?}`.
  - The only in-place change allowed is the **match** of a line (`ingredient_id`, `match_confidence`) via `recipe.line.set_ingredient`; content changes always create a new version.
- `recipe_photo`: `recipe_id, cook_session_id (nullable), blob_ref, taken_on date, caption, created_by`.
- `cook_session`: `recipe_id, version_id, cooked_on date, cooked_by, portions_made int, stored jsonb` (`[{location_id, portions, stock_item_id}]`), `uses jsonb` (summary of deductions for the timeline), `subs jsonb`, `leftover_product_id, note, undone_at`.
- `recipe_import`: `batch_id, kind (url|instagram|photo|text), input text` (URL or pasted text), `blob_refs text[]` (photo kind), `status (queued|fetching|parsing|translating|done|failed|needs_input|waiting_quota)`, `error jsonb` (problem), `recipe_id, attempts int, detected_lang`. Synced so the Import tab shows live progress (SSE nudge) and works when queued offline.
- `substitution` (global + household, like `ingredient`): `ingredient_id` (what is missing), `uses jsonb` (`[{ingredient_id, ratio, unit?, note?}]`), `note` (display, e.g. "Use the juice of 1 lime, added at the end"), `recipe_id` (nullable: recipe-specific LLM answer), `source (seed|llm|user)`. Global seed ~120 common swaps (m2-5 fills the data; m2-1 creates the table).

Columns added to existing synced tables (same migration):
- `ingredient.conversions jsonb`, `product.conversions jsonb`: cross-dimension conversions, `{"ea": "150 g", "cup": "185 g", "can": "400 g"}` (target is a mass or volume amount). Data filled by m2-6.
- `product.recipe_id` (nullable): leftover products ("Leftover chili") are one product per recipe, reused across cooks.
- `stock_item.cook_session_id`, `stock_event.cook_session_id` (nullable).
- `shopping_item.recipe_id` (nullable): "Recipe: Chicken curry" reason on the list (mockup).
- Global category seed adds `leftovers` (icon `tb-soup` or the registry's closest key) with default shelf life fridge 4 days, freezer 90 days.

Non-synced tables (owned by feature threads): `blob_owner` (m2-2), `recipe_chat_message` (m2-7), `recipe_fetch` (m2-4: per-URL fetch log/cache), none others.

`syncdb.Tables` adds `recipe, recipe_version, recipe_photo, cook_session, recipe_import, substitution` (`substitution` global = true). Dexie `version(3)` indexes: `recipe: 'id, saved, lastCookedAt, importId'`, `recipe_version: 'id, recipeId'`, `recipe_photo: 'id, recipeId, cookSessionId'`, `cook_session: 'id, recipeId, cookedOn'`, `recipe_import: 'id, batchId, status'`, `substitution: 'id, ingredientId, recipeId'`, plus the m2-2 table `pending_blob: 'ref'` (local only, never synced).

## 4. Ingredient lines (m2-1, `internal/recipes/lines`)

Pure Go parser, table-tested, used by every path that creates a version (manual edit, import, chat, AI ideas). TS does not parse; the client sends raw lines and the server returns structured lines through sync (optimistic reducer shows raw text until then).

1. Normalize (NFKC, unicode fractions ½ ¼ ¾ ⅓, `1 1/2`, `1-2` / `1 to 2` ranges → `qty`/`qty_max`, Persian/Arabic digits).
2. Leading quantity + unit with synonyms (`tablespoon(s)|tbsp|T`, `teaspoon|tsp|t`, `cup(s)`, `g|gram(s)`, `kg`, `ml|mL`, `l|litre|liter`, `oz`, `lb|pound`, `pinch`, `clove(s)`, `can(s)`, `bunch`, `slice(s)`, package words). Parenthetical sizes (`1 (400 mL) can coconut milk` → qty 1, unit can, note "400 mL", and learn nothing).
3. Trailing note after comma or in parentheses ("rinsed", "finely diced") → `note`. "to taste", "optional", "for garnish", "for serving" → `optional`.
4. Remaining text → `name`; resolve with the M1 `catalog.Resolver` (household ∪ global) → `ingredient_id, match_confidence`. Below 0.85 keeps the best guess and shows a "check match" chip.
5. `staple` = ingredient in the household's assumed-staples set (household setting `recipes.assumed_staples`, default `water, salt, black-pepper, ice`): never counts as missing, never deducted.
6. Section headers ("For the sauce:") become `group` for following lines.

Required tests include: "2 tbsp tomato paste", "1½ cups basmati rice, rinsed", "1 (400 mL) can coconut milk", "2-3 cloves garlic, minced", "salt to taste", "800 g boneless chicken thighs", "½ bunch cilantro", "1 tsp ground turmeric", "Juice of 1 lime", "For the rice:", a translated Persian line "1 cup yellow split peas (lappeh)".

`recipes.WriteVersion(ctx, tx, in VersionInput) (Version, error)` is the single function that parses lines, resolves, assigns `n`, writes `recipe_version`, moves `recipe.current_version_id` and the denormalized fields. Commands, import and chat call only this.

## 5. Commands (server handlers + client reducers)

m2-1 (`internal/recipes`): `recipe.create` (id, version payload, source fields), `recipe.version.create` (recipe_id, version id, content, origin manual|chat, `from_message_id?`, change_summary), `recipe.version.restore` (creates a new version copying an old one, origin restore; linear history, nothing is lost), `recipe.update` (tags, saved, cover_blob, subtitle; LWW), `recipe.delete` (soft), `recipe.line.set_ingredient` (version_id, line_id, ingredient_id; sets confidence 1 and is what the user's match pick does).
m2-2: `recipe.photo.add`, `recipe.photo.remove` (and the generic blob upload REST).
m2-4: `recipe.import.create` (id, batch_id, kind, input or blob_refs), `recipe.import.retry`, `recipe.import.cancel`, `recipe.import.provide` (needs_input: user pastes the caption/text).
m2-6: `recipe.cook`, `recipe.cook.undo`, `catalog.product.set_conversion`.
m2-5: no new commands (uses `shopping.item.add` with `recipe_id`, which m2-1 adds to that payload as an optional field).

Payload schemas named `CmdRecipe<Thing><Verb>`; reducers in `web/src/features/recipes/commands.ts` (cook ones in `features/cook/commands.ts`).

## 6. Blobs and photos (m2-2)

- `POST /api/v1/blobs` (raw body, `Content-Type` image/jpeg|png|webp, ≤ 8 MB, decoded to verify and ≤ 4096 px per side) → `{ref, size}`. Ref = SHA-256 of the bytes (existing `blob.Store`). Records `blob_owner(ref, household_id, uploaded_by, created_at)`; upload of an existing ref just adds the owner row.
- `GET /api/v1/blobs/{ref}`: allowed only if the active household owns the ref; `Cache-Control: private, max-age=31536000, immutable`; `?w=512` serves a cached thumbnail (pure Go resize, `golang.org/x/image/draw`, stored as its own blob keyed by `ref+w`).
- **Client** prepares every photo before upload: decode, downscale to ≤ 2048 px long side, re-encode JPEG q≈0.85 via canvas (strips EXIF/GPS; Safari hands HEIC as JPEG to canvas), compute SHA-256 with SubtleCrypto → the same ref the server computes. The bytes go to Dexie `pending_blob {ref, blob, createdAt}`; commands reference the ref immediately. The outbox flush uploads every pending blob a queued command references **before** sending that command batch, then deletes it locally. Works offline.
- `<BlobImage ref w>` resolves pending_blob (object URL) first, else the URL; the service worker caches `/api/v1/blobs/*` CacheFirst (max 300 entries) so recipe photos show offline. `<PhotoPicker onPicked(refs)>` (camera or library, multi-select).
- Backups already include all blobs (M1 §9). Blob GC (unreferenced for 30 days) is deferred; note it in `docs/`.
- M3 receipts reuse all of this unchanged.

## 7. Import pipeline (m2-4, `internal/recipes/importer`)

River job `recipe.import` per `recipe_import` row (unique by row id; max 2 concurrent per household, 4 global; bulk = one row per line in the textarea, same `batch_id`). Stages update `status` (and nudge SSE):

1. **Fetch** (url/instagram). Hardened HTTP client: http/https only, resolve DNS and **refuse private, loopback, link-local and cardea's own addresses** (SSRF), re-check on each redirect (max 5), 10 s timeout, 5 MB cap, `text/html` only, realistic UA. `recipe_fetch` logs URL, status, bytes, duration (admin view, 30-day retention).
2. **Extract**, deterministic first: schema.org `Recipe` from JSON-LD (incl. `@graph`, arrays, `HowToSection`/`HowToStep`), then microdata; map `recipeYield` → servings, ISO 8601 durations → minutes, `image` → download into blobs as the cover. No Recipe data → readability-style main text extraction → LLM parse (step 4).
3. **Instagram** (`kind=instagram`, detected from the URL): run `yt-dlp` (no Instagram login: AJ decided 2026-11-27) to get the caption/description and a ≤ 10 min audio track (`-x`, opus/m4a via ffmpeg). Transcribe the audio with the LLM gateway's new **transcription** capability (Groq Whisper `whisper-large-v3-turbo` through the OpenAI-compatible `/audio/transcriptions` endpoint; metered and capped like chat). LLM parse gets caption + transcript. If yt-dlp is blocked or not installed → status `needs_input`: the UI asks the user to paste the caption (and optionally say what the reel shows), then continues. Flag `flag.recipe_instagram`.
4. **LLM parse** (feature `recipes.parse`, cache 90 days on normalized input, strict JSON schema): `{lang, title, subtitle, servings, total_minutes, ingredients: [raw line strings, grouped], steps: [text], notes, tags}`. If `lang != en`, a second call (feature `recipes.translate`) translates title/lines/steps/notes to English preserving quantities, and the original goes in `version.original`. JSON-LD recipes in another language skip parse but still translate. Language detection: Unicode script heuristic first (Arabic script → `fa`), LLM confirms.
5. **Photo** (`kind=photo`, 1–4 images): vision request (feature `recipes.photo`, Gemini; Groq has no vision → `waiting_quota`/`llm_unavailable` manual path) returning the same schema as parse.
6. **Text** (`kind=text`): straight to LLM parse; if the text already looks structured (lines with quantities + numbered steps), the deterministic splitter builds it without the LLM.
7. Create `recipe` (`saved=true`, `source_*`) + version 1 via `recipes.WriteVersion` (origin import), set `import.recipe_id`, status `done`.

LLM unavailable → `waiting_quota` and the job snoozes until the router's reset time (River snooze); Interactive priority for a single import, Background for rows 4+ of a batch so bulk import never starves other features. Failures after 3 attempts → `failed` with problem; UI offers retry or "paste text instead". Flags `flag.recipe_web_import` (mockup), `flag.recipe_instagram`. PWA manifest gets a `share_target` (GET, `url`/`text`) → `/recipes/import?url=…` so Android "Share → Stockpot" works (iOS does not support it; paste stays).

Admin → Recipes page: imports by status (24 h / 14 d), recent failures with error and retry, fetch log, yt-dlp availability and version, flags.

## 8. From your kitchen (m2-5)

**Ranking runs client-side** over the replica (instant, offline), pure TS in `web/src/features/recipes/match/` with table tests and a JSON fixture:
- Build `ingredient → descendants` from the replica's `ingredient.parent_id`; in-stock set = ingredients (and extra ingredients) of products with a live stock item (quantity > 0, portions > 0, level not finished).
- Per saved recipe's current version: required lines = not optional and not staple. A line is **have** if its ingredient or a descendant is in stock (same "is a kind of" semantics as M1 §5), **sub** if a substitution (global seed, household, or recipe-specific) has all `uses` in stock, else **missing**.
- Score = (have + 0.6·sub) / required × (1 + 0.5·Σ urgency of stock it uses, urgency = 1 for expired-today/tomorrow, 0.6 within 3 days, 0.3 within 7) × (1 + 0.1·ln(1 + cooked_count)). Ties → recently cooked last (variety). Card shows "N of M", "soon" chips of expiring items it would use, and the first substitution line.
- Home "Cook tonight" card = top recipe with ≥ 70% have and at least one urgent item; hidden otherwise.
- "Missing to list" on the recipe page adds missing lines (scaled to servings) via `shopping.item.add` with `recipe_id`, ProductPicker matching to existing products by ingredient, skipping items already unchecked on the list.

**Substitutions**: seed ~120 global swaps (buttermilk ← milk + lemon juice, coconut milk ← Greek yogurt + water, dried lime ← lime juice, shallot ← onion, sour cream ↔ Greek yogurt, fresh ↔ dried herbs 3:1, …) as data in `internal/recipes/seed/substitutions.json`, validated against the ingredient seed in a test. "Find a swap" on a missing line (recipe page, cook sheet) calls `POST /api/v1/recipes/{id}/substitutes {line_id}` → LLM (feature `recipes.substitute`, cache 7 days on (version, line, in-stock ingredient set)) with the recipe and the household's in-stock ingredient names only → saved as a household `substitution` row with `recipe_id` (synced, so it shows on cards afterwards).

**AI ideas** (mockup "Idea from Stockpot AI"): **on demand only** ("More ideas" button), `POST /api/v1/recipes/ideas` → one LLM call (feature `recipes.ideas`, cache 1 day keyed by the in-stock + expiring ingredient set) → up to 3 recipes created with `source_kind=idea, saved=false, idea_expires_at=+7d` through `WriteVersion`. Save keeps them; a daily job tombstones expired unsaved ideas. Only ingredient names and dietary-free text go to the LLM.

## 9. I cooked this (m2-6)

Units (`internal/units` + `web/src/lib/units.ts`, shared fixture `internal/units/testdata/convert.json` run by Go and Vitest): `Convert(qty, from, to, conversions...)` within a dimension exactly (big.Rat), across dimensions through `product.conversions` then `ingredient.conversions` (then ancestors). m2-6 seeds conversions for ~100 staples (`ea`, `cup`, `clove`, `bunch`, `can` → g or ml) into the ingredient seed and adds `catalog.product.set_conversion`.

Cook sheet (`/recipes/:id/cook`, mockup):
- Portions made stepper (default version servings); everything scales by portions / servings.
- Per required line: the stock item to use, picked **first-expiring first** among matching items (user can switch), and a suggested amount in that item's own amount kind: exact → converted quantity (unknown conversion → show the recipe amount and ask for the stock amount, default 0, chip "set amount"); portions → `ceil` of scaled portions when the product has a portion conversion, else 1; level → the next level down ("most → half"), or "finished". Shows "Leaves X in stock" or "Uses it all. Add to shopping list? Yes" (default yes for staples-bought-regularly: product has ≥ 2 purchases).
- Substituted lines show "Swapped for …" and deduct the substitute's stock.
- Store the portions: fridge / freezer steppers, default all to the fridge; anything not stored is simply not tracked (no "eaten now" wording, per AJ). Eat-by dates from the leftovers category rule (fridge 4 d, freezer 90 d) through the M1 estimator, shown inline.
- Optional photo (PhotoPicker) and note.
- CTA "Update stock and store N portions" sends one `recipe.cook` command.

`recipe.cook` payload: `{id (cook_session), recipe_id, version_id, cooked_on, portions_made, uses: [{line_id?, stock_item_id, consume? (delta qty in item unit), portions? (delta), level? (new level), finished?: bool}], to_shopping: [{id, product_id, qty?, unit?}], store: [{stock_item_id, location_id, portions}], subs: [{line_id, substitution_id}], photo_refs: [], note}`. One transaction: household lock → for each use apply the same logic as `inventory.stock.consume`/`set_level` (shared internal functions, not command re-dispatch) with `stock_event.cook_session_id`, reason `cook` → ensure the leftover product (by `recipe_id`, category leftovers, name = recipe title) → create stored stock items (amount_kind portions, source cook, estimated expiry) → shopping items with `recipe_id` → `recipe_photo` rows → `cook_session` → `recipe.cooked_count++`, `last_cooked_at` → audit. Deltas keep concurrent edits safe; a used item deleted meanwhile → that use is skipped and reported in the result (not a whole-command reject), because the cook happened.

`recipe.cook.undo` (cook_session id): within 24 h, reverses the deltas with compensating `stock_event`s, removes stored leftover items that have no later events, removes shopping items still unchecked, sets `undone_at`, decrements counts. Toast "Updated 6 items · 4 portions stored · Undo".

## 10. Recipe chat and versions (m2-7)

- `recipe_chat_message(id, household_id, recipe_id, version_id, role user|assistant, text, proposal jsonb, proposal_state (none|pending|saved|dismissed), saved_version_id, user_id, created_at)`. REST, not synced: `GET /api/v1/recipes/{id}/chat`, `POST /api/v1/recipes/{id}/chat {text}`, `POST …/chat/{msgId}/dismiss`. Online only; offline the input is disabled with a note.
- LLM (feature `recipes.chat`, Interactive, no cache) gets the current version (English), the last 20 messages, the household's cook notes for this recipe, and a strict schema: `{reply, proposal?: {change_summary, title?, servings?, total_minutes?, ingredients?: [raw lines], steps?: [text], notes?}}`. Pure questions ("can I freeze this?") return no proposal.
- "Save as vN" sends `recipe.version.create` with origin chat, `from_message_id`, the proposal content merged onto the current version; the server re-parses lines via `WriteVersion` and marks the message saved. If the recipe moved on since the proposal (another version), the button reads "Save as vN (based on vK)" and still works: versions are snapshots, nothing merges.
- Versions UI (owned by m2-3): timeline with `n`, change_summary, date, origin; tap → view that version read-only with "Restore" (`recipe.version.restore`) and a diff of lines/steps against current.
- Language chip "English · فارسی" toggles the view to `original` when present (RTL, `lang="fa"`).

## 11. Web structure and shared-file rules

Routes (m2-1 adds them, pointing to stubs): `/recipes` (tabs From your kitchen | Saved | Import), `/recipes/:id`, `/recipes/:id/v/:n`, `/recipes/:id/edit`, `/recipes/:id/cook`, `/recipes/import`.

m2-1 creates **slot components** that return `null` (or a minimal placeholder) so wave-2 threads each fill their own file and never edit the same page:
- `features/recipes/slots/KitchenTab.tsx` (m2-5), `SavedTab.tsx` (m2-3), `ImportTab.tsx` (m2-4), `CooksSection.tsx` (m2-6: photos per cook + cook history), `ChatSection.tsx` (m2-7), `MissingToListButton.tsx` (m2-5), `CookButton.tsx` (m2-6), `FindSwapButton.tsx` (m2-5), `CookTonightCard.tsx` (m2-5, mounted on Home by m2-1).
- m2-3 owns `RecipesPage.tsx`, `RecipePage.tsx`, `VersionPage.tsx`, `RecipeEditor.tsx` and composes the slots.

OpenAPI: each thread's paths in its own tag section (`recipes`, `recipes-import`, `blobs`, `recipes-chat`, `cook`); command schemas `CmdRecipe…`. Each thread registers its handlers from its own package in `app.go` (one line each). Migrations: next free number at rebase. Regenerate openapi by re-inserting own blocks into main's file on conflicts (M1 tip). Merge main instead of rebasing when many generated-file conflicts.

## 12. LLM budget

New features: `recipes.parse` (90 d cache), `recipes.translate` (90 d), `recipes.photo` (vision), `recipes.transcribe` (audio), `recipes.substitute` (7 d), `recipes.ideas` (1 d), `recipes.chat` (no cache). All Interactive except bulk-import tails (Background). Admin → LLM usage lists them with their "without AI" fallback text, as in the mockup. No household data beyond recipe text and ingredient names is sent.

## 13. Task split

| Brief | Depends on | Wave |
|---|---|---|
| m2-1 recipe core: schema, sync, line parser, version writer, recipe commands, routes + slots | M1 | 1 (now) |
| m2-2 photos: blob upload/serve, client pending-blob outbox, PhotoPicker/BlobImage, recipe photo commands | M1 | 1 (now) |
| m2-3 recipe library UI: Saved tab, recipe page, editor, versions + restore + diff, language toggle | m2-1 (m2-2 for photos, swap in on rebase) | 2 |
| m2-4 import: web/JSON-LD, LLM parse, translation, photo, Instagram + transcription, bulk, Import tab, admin page | m2-1 (m2-2 for photo kind) | 2 |
| m2-5 from your kitchen: client ranking, Cook tonight, substitutions seed + LLM swaps, AI ideas, Missing to list | m2-1 | 2 |
| m2-6 I cooked this: unit conversions + seed, cook sheet, `recipe.cook` + undo, leftovers, cook photos section | m2-1, m2-2 | 2 |
| m2-7 recipe chat → versions | m2-1 | 2 |

m2-1 is deliberately thin so wave 2 can start within hours. Five wave-2 threads run in parallel.

## 14. One-time steps for AJ

1. Merge the nixConfig PR adding `yt-dlp` and `ffmpeg` to `systemd.services.stockpot.path` (Instagram import; AJ chose no Instagram login, 2026-11-27).
2. Add a free Google AI Studio (Gemini) key in Admin → LLM: photo-of-a-recipe import needs a vision model; Groq is text and audio only.
