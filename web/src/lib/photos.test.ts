import 'fake-indexeddb/auto'
import { Blob as NodeBlob } from 'node:buffer'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { StockpotDB } from '../offline/db'
import { dispatch } from '../offline/outbox'
import { registerReducer } from '../offline/reducers'
import { flushOutbox, retryRejected, syncNow } from '../offline/sync'
import {
  addPendingBlob,
  blobUrl,
  fitWithin,
  MAX_UPLOAD_ATTEMPTS,
  sha256Hex,
  sweepPendingBlobs,
} from './photos'

declare module '../offline/commands' {
  interface CommandPayloads {
    'test.photo': { id: string; refs: string[] }
    'test.plain': { id: string }
  }
}
registerReducer('test.photo', () => {}, { blobs: (p) => p.refs })
registerReducer('test.plain', () => {})
const refsOf = (type: string, payload: unknown) =>
  type === 'test.photo' ? (payload as { refs: string[] }).refs : []

const HH = '0190a1b2-0000-7000-8000-0000000000a1'
const HELLO = '2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824'
const db = new StockpotDB('stockpot-photos-test')

type Server = { online: boolean; blobStatus: number; calls: string[]; uploaded: string[] }
let server: Server

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

beforeEach(async () => {
  server = { online: true, blobStatus: 201, calls: [], uploaded: [] }
  vi.stubGlobal('fetch', async (req: Request) => {
    if (!server.online) throw new TypeError('offline')
    const url = new URL(req.url)
    server.calls.push(url.pathname)
    if (url.pathname === '/api/v1/blobs') {
      const bytes = new Uint8Array(await req.arrayBuffer())
      const ref = await sha256Hex(new Blob([bytes]))
      if (server.blobStatus !== 201) return json({ code: 'bad_image' }, server.blobStatus)
      server.uploaded.push(ref)
      return json({ ref, size: bytes.length, contentType: req.headers.get('Content-Type') }, 201)
    }
    if (url.pathname === '/api/v1/commands') {
      const { commands } = (await req.json()) as { commands: { id: string }[] }
      return json({ results: commands.map((c) => ({ id: c.id, status: 'applied' })) })
    }
    return json({}, 404)
  })
  await db.open()
  await db.meta.put({ key: 'sync.household', value: HH })
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await db.delete()
})

// Node's Blob: fake-indexeddb clones with Node's structuredClone, which can't clone jsdom's.
const jpeg = (text: string) => new NodeBlob([text], { type: 'image/jpeg' })

test('fitWithin keeps the aspect ratio and never upscales', () => {
  expect(fitWithin(4032, 3024)).toEqual({ width: 2048, height: 1536 })
  expect(fitWithin(3024, 4032)).toEqual({ width: 1536, height: 2048 })
  expect(fitWithin(800, 600)).toEqual({ width: 800, height: 600 })
  expect(fitWithin(10000, 1)).toEqual({ width: 2048, height: 1 })
})

test('the ref is the SHA-256 of the bytes, as the server computes it', async () => {
  expect(await sha256Hex(new Blob(['hello']))).toBe(HELLO)
  expect(await addPendingBlob(jpeg('hello'), db)).toBe(HELLO)
  expect(await db.pending_blob.get(HELLO)).toMatchObject({ ref: HELLO })
  expect(blobUrl(HELLO, 512)).toBe(`/api/v1/blobs/${HELLO}?w=512`)
})

test('flush uploads referenced photos before sending their commands', async () => {
  const a = await addPendingBlob(jpeg('photo a'), db)
  const b = await addPendingBlob(jpeg('photo b'), db)
  const unrelated = await addPendingBlob(jpeg('not referenced yet'), db)
  await dispatch('test.photo', { id: '1', refs: [a, b, a] }, db)
  expect(await flushOutbox(db)).toBe('done')
  expect(server.calls).toEqual(['/api/v1/blobs', '/api/v1/blobs', '/api/v1/commands'])
  expect(server.uploaded).toEqual([a, b])
  expect(await db.pending_blob.toCollection().primaryKeys()).toEqual([unrelated])
  expect(await db.outbox.count()).toBe(0)
})

test('a photo the server already has is not uploaded again', async () => {
  await dispatch('test.photo', { id: '1', refs: [HELLO] }, db)
  expect(await flushOutbox(db)).toBe('done')
  expect(server.calls).toEqual(['/api/v1/commands'])
})

test('offline or failing uploads keep the photo and its command for later', async () => {
  const a = await addPendingBlob(jpeg('photo a'), db)
  await dispatch('test.photo', { id: '1', refs: [a] }, db)
  server.online = false
  expect(await flushOutbox(db)).toBe('offline')
  server.online = true
  server.blobStatus = 503
  expect(await flushOutbox(db)).toBe('offline')
  expect(server.calls).toEqual(['/api/v1/blobs'])
  expect(await db.pending_blob.count()).toBe(1)
  expect(await db.outbox.where('status').equals('pending').count()).toBe(1)

  server.blobStatus = 201
  expect(await flushOutbox(db)).toBe('done')
  expect(await db.pending_blob.count()).toBe(0)
  expect(await db.outbox.count()).toBe(0)
})

test('a photo that keeps failing is set aside with its command and never blocks the rest', async () => {
  const a = await addPendingBlob(jpeg('photo a'), db)
  await dispatch('test.photo', { id: '1', refs: [a] }, db)
  await dispatch('test.plain', { id: '2' }, db)
  server.blobStatus = 500
  for (let i = 1; i < MAX_UPLOAD_ATTEMPTS; i++) expect(await flushOutbox(db)).toBe('offline')
  expect(server.calls.filter((c) => c === '/api/v1/commands')).toEqual([])

  expect(await flushOutbox(db)).toBe('rejected')
  const rows = await db.outbox.toArray()
  expect(rows.map((r) => [r.type, r.status])).toEqual([['test.photo', 'rejected']])
  expect(JSON.parse(rows[0]?.error ?? '')).toMatchObject({ code: 'photo_upload_failed' })
  expect(await db.pending_blob.get(a)).toMatchObject({ attempts: MAX_UPLOAD_ATTEMPTS })

  // Retrying tries the photo again from scratch; the photo was kept.
  server.blobStatus = 201
  await retryRejected(rows[0]?.seq as number, db)
  await syncNow(db) // joins the sync the retry started
  expect(server.uploaded).toEqual([a])
  expect(await db.outbox.count()).toBe(0)
})

test('a photo the server refuses for good sets its command aside at once', async () => {
  const a = await addPendingBlob(jpeg('not allowed'), db)
  await dispatch('test.photo', { id: '1', refs: [a] }, db)
  await dispatch('test.plain', { id: '2' }, db)
  server.blobStatus = 403
  expect(await flushOutbox(db)).toBe('rejected')
  expect((await db.outbox.toArray()).map((r) => r.status)).toEqual(['rejected'])
  expect(server.calls).toEqual(['/api/v1/blobs', '/api/v1/commands'])
})

test('old photos no command references are swept; referenced and recent ones stay', async () => {
  const old = '2026-01-01T00:00:00.000Z'
  const now = Date.parse('2026-01-20T00:00:00Z')
  for (const ref of ['orphan', 'used', 'rejected-use']) {
    await db.pending_blob.put({ ref, blob: jpeg(ref), createdAt: old })
  }
  await db.pending_blob.put({
    ref: 'fresh',
    blob: jpeg('fresh'),
    createdAt: '2026-01-19T00:00:00.000Z',
  })
  await dispatch('test.photo', { id: '1', refs: ['used'] }, db)
  await dispatch('test.photo', { id: '2', refs: ['rejected-use'] }, db)
  await db.outbox.where('seq').equals(2).modify({ status: 'rejected' })
  expect(await sweepPendingBlobs(refsOf, db, now)).toBe(1)
  expect((await db.pending_blob.toCollection().primaryKeys()).sort()).toEqual([
    'fresh',
    'rejected-use',
    'used',
  ])
})
