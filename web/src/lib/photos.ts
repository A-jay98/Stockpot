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

/** Statuses that mean the server will never accept these bytes. */
const permanent = new Set([400, 413, 415, 422])

/**
 * Upload every ref in `refs` that is still waiting in `pending_blob`, deleting each once the
 * server has it. Stops with 'offline' on a network or server failure (the caller retries later).
 * Bytes the server refuses for good are dropped; the command that references them is then
 * rejected by the server and shows on the "Couldn't sync" sheet.
 */
export async function uploadPendingBlobs(
  refs: Iterable<string>,
  database: StockpotDB = db,
): Promise<'done' | 'offline'> {
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
      return 'offline'
    }
    if (status === 201 && got === ref) {
      await database.pending_blob.delete(ref)
    } else if (permanent.has(status) || status === 201) {
      // 201 with another ref means the bytes changed on the way: they can't stand for `ref`.
      console.warn('photo upload refused', ref, status)
      await database.pending_blob.delete(ref)
    } else {
      return 'offline'
    }
  }
  return 'done'
}

/** Drop cached photos (sign-out, household switch): they are household-private. */
export async function clearBlobCache(): Promise<void> {
  if (typeof caches === 'undefined') return
  await caches.delete(BLOB_CACHE).catch(() => false)
}
