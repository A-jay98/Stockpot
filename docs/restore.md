# Restoring a backup

Backups are nightly, encrypted with [age](https://age-encryption.org) and kept
as files in a private Telegram channel (design: `architecture-m1.md` §9).
Each run is a set of numbered parts (`stockpot-<run>-db.zst.age.001`, …,
`-blobs.zst.age.001`, …) plus `stockpot-<run>-manifest.json`, which lists every
part with its size, SHA-256 and Telegram file id. Parts stay under 20 MB
because bots can only download files up to that size.

You need: the **age private key** (the file `age-keygen` wrote; the server
never has it), the **manifest**, and either the **bot token** or the parts
downloaded by hand.

## One-time setup (before you need it)

1. `age-keygen -o stockpot.agekey` on your own machine. Keep this file
   somewhere safe and *not* only on the server. Without it backups are
   unreadable.
2. Paste the `age1…` public key it prints into Admin → Settings →
   `backup.age_recipient`.
3. Create a private Telegram channel, add the bot as an administrator, and
   set `backup.telegram_chat_id` (the numeric id, `-100…`) and the bot token
   (`telegram.bot_token`).
4. Copy the server's **secret key** (`STOCKPOT_SECRET_KEY_FILE`, e.g.
   `/var/lib/credentials/stockpot/secret-key`) next to the age key, off the
   server. It is *not* in the backup: without it the encrypted settings
   (`telegram.bot_token`, LLM API keys) cannot be read after a restore and
   would have to be re-entered.
5. Turn on `flag.backups`. Press **Back up now** on Admin → Backups once and
   check a run appears. Do a trial restore (below) into a scratch database.

## Restore onto a fresh server

1. Install the same Stockpot release and `postgresql` (client tools
   `pg_restore` of the same major version as the dump).
2. Create an **empty, unmigrated** database and role, e.g.
   `createdb stockpot` (with the owner the app will use). Do not run
   `stockpot migrate` first: the restore brings the schema. Restore refuses to
   overwrite: it runs `pg_restore` in a single transaction without `--clean`,
   so a non-empty database fails and is left unchanged.
3. Get the manifest: open the channel, download the newest
   `stockpot-<run>-manifest.json` (its caption says date, parts, size).
4. Run, as the user that owns the blob directory:

   ```sh
   export STOCKPOT_BOT_TOKEN=123456:ABC…
   stockpot restore \
     --manifest ./stockpot-1a2b3c4d-manifest.json \
     --identity ./stockpot.agekey \
     --database-url 'postgres:///stockpot?host=/run/postgresql' \
     --blob-dir /var/lib/stockpot/blobs
   ```

   It downloads and verifies every part (size and SHA-256) before touching
   anything, then decrypts, restores the database and unpacks the blobs.
   `--manifest` also accepts a message link (`https://t.me/c/<id>/<n>`).
5. Point the new install at the database (`STOCKPOT_DATABASE_URL`, and the
   saved `STOCKPOT_SECRET_KEY_FILE`: encrypted settings such as API keys can
   only be read with the old key; otherwise re-enter them), start it; `stockpot migrate` runs on
   start and applies any migrations newer than the backup's `schema_version`.
6. Check Admin → Overview. A restored database carries the old settings, so
   `flag.backups` is **still on** and the chat id unchanged: before starting a
   trial or second instance against a restored copy, turn `flag.backups` off
   (or the copy will post backups to the same channel).

## If the bot or its token is gone

File ids only work for the bot that uploaded them. Download all
`stockpot-<run>-*` files of one run from the channel by hand into a directory
and add `--parts-dir ./parts` instead of `STOCKPOT_BOT_TOKEN`.

## Notes

- Wrong key: `decrypt (wrong identity?)`. A corrupt or missing part is caught
  by its hash before anything is restored.
- Retention keeps `backup.keep` runs (default 14). Telegram only lets bots
  delete messages younger than 48 hours, so older runs may stay in the
  channel; Admin → Backups says when that happened.
- Restoring does not need the old server to be reachable. Postgres major
  versions: restore with `pg_restore` at least as new as the `pg_dump` that
  made the backup.
