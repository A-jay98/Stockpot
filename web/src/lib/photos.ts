import { api } from '../api/client'
import { db, type StockpotDB } from '../offline/db'

/**
 * Photos (architecture-m2 §6). Every photo is prepared on the device before it goes anywhere:
 * downscaled, re-encoded as JPEG (which drops EXIF and GPS) and hashed. The SHA-256 is the
 * server's blob ref, so commands can reference the photo at once, offline; the bytes wait in
 * Dexie `pending_blob` until the outbox uploads them ahead of those commands.
 */

/** Longest side after downscaling, in px. */
export const MAX_SIDE = 2048
export const JPEG_QUALITY = 0.85
/** Service worker cache for `/api/v1/blobs/*` (see vite.config.ts). */
export const BLOB_CACHE = 'stockpot-blobs'
/** Widths the server serves as thumbnails. */
export type ThumbWidth = 256 | 512 | 1024

/** Size that fits `width`×`height` within `max` px per side, keeping the aspect ratio. */
export function fitWithin(width: number, height: number, max = MAX_SIDE) {
  const scale = Math.min(1, max / Math.max(width, height))
  return {
    width: Math.max(1, Math.round(width * scale)),
    height: Math.max(1, Math.round(height * scale)),
  }
}

/** Downscale and re-encode a picked image (JPEG, PNG, WebP; HEIC where the browser decodes it). */
export async function preparePhoto(file: Blob): Promise<Blob> {
  // from-image applies the EXIF orientation, which the re-encode would otherwise lose.
  const bitmap = await createImageBitmap(file, { imageOrientation: 'from-image' })
  try {
    const { width, height } = fitWithin(bitmap.width, bitmap.height)
    if (typeof OffscreenCanvas !== 'undefined') {
      const canvas = new OffscreenCanvas(width, height)
      draw(canvas.getContext('2d'), bitmap, width, height)
      return await canvas.convertToBlob({ type: 'image/jpeg', quality: JPEG_QUALITY })
    }
    const canvas = document.createElement('canvas')
    canvas.width = width
    canvas.height = height
    draw(canvas.getContext('2d'), bitmap, width, height)
    return await new Promise<Blob>((resolve, reject) =>
      canvas.toBlob(
        (b) => (b ? resolve(b) : reject(new Error('Could not encode the photo'))),
        'image/jpeg',
        JPEG_QUALITY,
      ),
    )
  } finally {
    bitmap.close()
  }
}

type Ctx2D = CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D

function draw(ctx: Ctx2D | null, img: ImageBitmap, width: number, height: number) {
  if (!ctx) throw new Error('Canvas is not available')
  // JPEG has no alpha: transparent areas become white, as on the server.
  ctx.fillStyle = '#fff'
  ctx.fillRect(0, 0, width, height)
  ctx.imageSmoothingQuality = 'high'
  ctx.drawImage(img, 0, 0, width, height)
}

/** Lower-case hex SHA-256 of the bytes: the blob ref the server computes for them. */
export async function sha256Hex(blob: Blob): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await blob.arrayBuffer())
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
}

/** Store already prepared bytes for upload and return their ref. */
export async function addPendingBlob(blob: Blob, database: StockpotDB = db): Promise<string> {
  const ref = await sha256Hex(blob)
  await database.pending_blob.put({ ref, blob, createdAt: new Date().toISOString() })
  return ref
}

/** Prepare a picked file and queue it for upload. Returns its ref for use in commands. */
export async function savePhoto(file: Blob, database: StockpotDB = db): Promise<string> {
  return addPendingBlob(await preparePhoto(file), database)
}

/** URL of a blob (or its thumbnail at `w` px wide). */
export const blobUrl = (ref: string, w?: ThumbWidth) => `/api/v1/blobs/${ref}${w ? `?w=${w}` : ''}`

/** Statuses that mean the server will never accept these bytes (403: this user can't upload). */
const permanent = new Set([400, 403, 413, 415, 422])

/** Failed upload tries (5xx, 429, …) before the commands using the photo are set aside. */
export const MAX_UPLOAD_ATTEMPTS = 5

/** How long a photo no queued command references is kept (picked, then discarded). */
const ORPHAN_MS = 7 * 24 * 60 * 60 * 1000

export type UploadResult = {
  /** 'offline': stop the flush and try later (network down, signed out, a retryable failure). */
  status: 'done' | 'offline'
  /** Refs that can't be uploaded (refused, or out of attempts): their commands are set aside. */
  failed: string[]
}

/**
 * Upload every ref in `refs` that is still waiting in `pending_blob`, deleting each once the
 * server has it. A network failure or 401 stops with 'offline' without counting. Other failures
 * count an attempt; after MAX_UPLOAD_ATTEMPTS, or at once for a permanent refusal, the ref is
 * reported in `failed` so the outbox sets its commands aside ("Couldn't sync") instead of letting
 * one photo block everything queued behind it. Retrying such a command tries the photo again.
 */
export async function uploadPendingBlobs(
  refs: Iterable<string>,
  database: StockpotDB = db,
): Promise<UploadResult> {
  const failed: string[] = []
  for (const ref of new Set(refs)) {
    const pending = await database.pending_blob.get(ref)
    if (!pending) continue
    let status: number
    let got: string | undefined
    try {
      // The bytes, not the Blob: every fetch implementation sends an ArrayBuffer as is.
      const body = await pending.blob.arrayBuffer()
      const res = await api.POST('/v1/blobs', {
        body: body as never,
        bodySerializer: (b: unknown) => b as never,
        headers: { 'Content-Type': pending.blob.type || 'image/jpeg' },
      })
      status = res.response.status
      got = res.data?.ref
    } catch {
      return { status: 'offline', failed }
    }
    if (status === 201 && got === ref) {
      await database.pending_blob.delete(ref)
    } else if (status === 401) {
      return { status: 'offline', failed } // signed out: sign-in is handled elsewhere
    } else if (permanent.has(status) || status === 201) {
      // 201 with another ref means the bytes changed on the way: they can't stand for `ref`.
      console.warn('photo upload refused', ref, status)
      await database.pending_blob.put({ ...pending, attempts: MAX_UPLOAD_ATTEMPTS })
      failed.push(ref)
    } else {
      const attempts = (pending.attempts ?? 0) + 1
      await database.pending_blob.put({ ...pending, attempts })
      if (attempts < MAX_UPLOAD_ATTEMPTS) return { status: 'offline', failed }
      failed.push(ref)
    }
  }
  return { status: 'done', failed }
}

/** Let a photo be tried again (the user retried a command that was set aside because of it). */
// put, not update: Dexie's update deep-clones the row, which must keep the Blob as is.
export async function resetUploadAttempts(refs: string[], database: StockpotDB = db) {
  for (const ref of refs) {
    const pending = await database.pending_blob.get(ref)
    if (pending?.attempts) await database.pending_blob.put({ ...pending, attempts: 0 })
  }
}

/**
 * Delete photos older than a week that no queued or set-aside command references: picks the user
 * discarded before saving. Photos a command still needs are never touched.
 */
export async function sweepPendingBlobs(
  referenced: (type: string, payload: unknown) => string[],
  database: StockpotDB = db,
  now = Date.now(),
): Promise<number> {
  return database.transaction('rw', database.pending_blob, database.outbox, async () => {
    const cutoff = new Date(now - ORPHAN_MS).toISOString()
    const old = await database.pending_blob.filter((p) => p.createdAt < cutoff).primaryKeys()
    if (old.length === 0) return 0
    const inUse = new Set<string>()
    await database.outbox.each((row) => {
      for (const ref of referenced(row.type, row.payload)) inUse.add(ref)
    })
    const orphans = old.filter((ref) => !inUse.has(ref))
    await database.pending_blob.bulkDelete(orphans)
    return orphans.length
  })
}

/** Drop cached photos (sign-out, household switch): they are household-private. */
export async function clearBlobCache(): Promise<void> {
  if (typeof caches === 'undefined') return
  await caches.delete(BLOB_CACHE).catch(() => false)
}
