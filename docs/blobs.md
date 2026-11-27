# Blobs (photos)

Recipe and cook photos now, receipt photos in M3. Spec: `architecture-m2.md` §6.

## Flow

1. The PWA prepares each picked photo on the device (`web/src/lib/photos.ts`): downscale to at
   most 2048 px on the long side, re-encode as JPEG q0.85 (drops EXIF/GPS), SHA-256 the bytes.
   The hex digest is the **ref**, the same one the server computes.
2. The bytes go to Dexie `pending_blob`, and commands reference the ref straight away (offline
   too). Commands declare their refs with `registerReducer(type, fn, { blobs })`.
3. Before sending a batch, the outbox flush uploads every pending blob the batch references
   (`POST /api/v1/blobs`) and deletes it locally once stored. A network or 5xx failure leaves
   both photo and commands queued; a permanent refusal (400/413/415/422) drops the photo, and
   the server then rejects the command, which shows on the "Couldn't sync" sheet.
4. The server (`internal/media`) checks type (from the bytes, matching `Content-Type`), size
   (8 MB), dimensions (4096 px per side) and fully decodes the image, stores it in the
   content-addressed `blob.Store` and records `blob_owner (ref, household_id)`. Re-uploading
   known bytes only adds an owner row. Command handlers check `media.Owned` before accepting
   a ref.
5. `GET /api/v1/blobs/{ref}` answers 404 unless the active household owns the ref, with
   `Cache-Control: private, max-age=31536000, immutable` and `ETag: "<ref>"`. `?w=256|512|1024`
   serves a JPEG thumbnail, built on first request, stored as its own blob and recorded in
   `blob_variant`. The service worker serves `/api/v1/blobs/*` cache-first (300 entries); the
   cache is cleared on sign-out and household switch.

Backups include every blob (the whole blob directory).

## Deferred: garbage collection

Nothing deletes blobs yet. A blob uploaded but never referenced (a photo picked and then
discarded), or no longer referenced (a removed recipe photo), stays on disk and in backups.
Planned: a periodic River job that deletes `blob_owner` rows whose ref no household row
references (recipe cover, recipe photos, receipts, …) for 30 days, then deletes bytes with no
owner left, plus their `blob_variant` rows and variant bytes. Each feature that stores refs
registers a "refs in use" query with the job, so the job never has to know every table.
