import 'fake-indexeddb/auto'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import '../features/catalog/commands'
import { StockpotDB } from './db'
import { dispatch } from './outbox'
import { adoptHousehold, discardRejected, flushOutbox, pull, retryRejected, syncNow } from './sync'

const HH = '0190a1b2-0000-7000-8000-0000000000a1'
const db = new StockpotDB('stockpot-sync-test')

type Cmd = { id: string; type: string; payload: Record<string, unknown> }
type Server = {
  online: boolean
  rejectTypes: Set<string>
  applied: Cmd[]
  pages: unknown[]
  syncCalls: string[]
  failNames: Set<string>
}

let server: Server

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

beforeEach(async () => {
  server = {
    online: true,
    rejectTypes: new Set(),
    applied: [],
    pages: [],
    syncCalls: [],
    failNames: new Set(),
  }
  vi.stubGlobal('fetch', async (req: Request) => {
    if (!server.online) throw new TypeError('offline')
    const url = new URL(req.url)
    if (url.pathname === '/api/v1/commands') {
      const { commands } = (await req.json()) as { commands: Cmd[]; householdId: string }
      if (commands.some((c) => server.failNames.has(String(c.payload.name))))
        return json({ title: 'boom' }, 500)
      return json({
        results: commands.map((c) => {
          if (server.rejectTypes.has(c.type)) {
            return {
              id: c.id,
              status: 'rejected',
              problem: { code: 'gone', detail: 'It was deleted.' },
            }
          }
          server.applied.push(c)
          return { id: c.id, status: 'applied' }
        }),
      })
    }
    if (url.pathname === '/api/v1/sync') {
      server.syncCalls.push(url.search)
      const page = server.pages.shift() ?? {
        rows: {},
        tombstones: {},
        next: 0,
        nextGlobal: 0,
        more: false,
      }
      return json(page)
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

const storeRow = (id: string, name: string, rev: number) => ({
  id,
  household_id: HH,
  created_at: '2026-11-27T00:00:00Z',
  updated_at: '2026-11-27T00:00:00Z',
  rev,
  name,
  sort: 10,
  color: null,
})

const sid = (n: number) => `0190a1b2-0000-7000-8000-00000000010${n}`

test('dispatch queues the command and applies it to the replica at once', async () => {
  await dispatch('catalog.store.create', { id: sid(1), name: 'Costco', sort: 10 }, db)
  expect(await db.store.get(sid(1))).toMatchObject({ name: 'Costco', householdId: HH, rev: 0 })
  expect(await db.outbox.count()).toBe(1)
  await dispatch('catalog.store.update', { id: sid(1), name: 'Costco Wholesale' }, db)
  expect((await db.store.get(sid(1)))?.name).toBe('Costco Wholesale')
  await dispatch('catalog.store.delete', { id: sid(1) }, db)
  expect(await db.store.get(sid(1))).toBeUndefined()
})

test('product update sets given fields, clears nulls, leaves absent ones', async () => {
  await dispatch(
    'catalog.product.create',
    { id: sid(2), name: 'Mandarins', brand: 'Kirkland', note: 'x' },
    db,
  )
  await dispatch(
    'catalog.product.update',
    { id: sid(2), brand: null, defaultQty: '2', defaultUnit: 'lb' },
    db,
  )
  expect(await db.product.get(sid(2))).toMatchObject({
    name: 'Mandarins',
    brand: null,
    note: 'x',
    defaultQty: '2',
    defaultUnit: 'lb',
  })
})

test('flush sends in seq order and removes applied commands', async () => {
  await dispatch('catalog.store.create', { id: sid(1), name: 'A' }, db)
  await dispatch('catalog.store.create', { id: sid(2), name: 'B' }, db)
  expect(await flushOutbox(db)).toBe('done')
  expect(server.applied.map((c) => c.payload.name)).toEqual(['A', 'B'])
  expect(await db.outbox.count()).toBe(0)
})

test('flush stops when offline and keeps commands pending', async () => {
  await dispatch('catalog.store.create', { id: sid(1), name: 'A' }, db)
  server.online = false
  expect(await flushOutbox(db)).toBe('offline')
  const [row] = await db.outbox.toArray()
  expect(row).toMatchObject({ status: 'pending', attempts: 1 })
  server.online = true
  expect(await flushOutbox(db)).toBe('done')
  expect(await db.outbox.count()).toBe(0)
})

test('pull pages through rows and tombstones, camelizes, and keeps the cursors', async () => {
  server.pages = [
    {
      rows: { store: [storeRow(sid(1), 'Costco', 5)] },
      tombstones: {},
      next: 5,
      nextGlobal: 2,
      more: true,
    },
    {
      rows: {
        store: [storeRow(sid(3), 'Loblaws', 7)],
        product: [
          {
            id: sid(4),
            household_id: HH,
            rev: 8,
            name: 'Rice',
            default_qty: '2.5',
            extra_ingredient_ids: [],
          },
        ],
      },
      tombstones: { store: [sid(1)] },
      next: 8,
      nextGlobal: 2,
      more: false,
    },
  ]
  expect(await pull(db)).toBe(true)
  expect((await db.store.toArray()).map((s) => s.name)).toEqual(['Loblaws'])
  expect(await db.product.get(sid(4))).toMatchObject({ defaultQty: '2.5', extraIngredientIds: [] })
  expect(server.syncCalls[1]).toContain('since=5')
  expect(server.syncCalls[1]).toContain('sinceGlobal=2')
  expect((await db.meta.get('sync.rev'))?.value).toBe(8)
})

test('a pull does not wipe optimistic changes still waiting in the outbox', async () => {
  await dispatch('catalog.store.create', { id: sid(1), name: 'Pending store' }, db)
  server.pages = [
    {
      rows: { store: [storeRow(sid(3), 'Server store', 3)] },
      tombstones: {},
      next: 3,
      nextGlobal: 0,
      more: false,
    },
  ]
  await pull(db)
  expect((await db.store.toArray()).map((s) => s.name).sort()).toEqual([
    'Pending store',
    'Server store',
  ])
})

test('optimistic rows the server never created are dropped once the outbox drains', async () => {
  // Two creates; the server applies both but sends back only one (it chose an existing row for
  // the other), so that one's optimistic copy would linger with rev 0.
  await dispatch('catalog.store.create', { id: sid(1), name: 'Kept' }, db)
  await dispatch('catalog.store.create', { id: sid(2), name: 'Ghost' }, db)
  server.pages = [
    {
      rows: { store: [storeRow(sid(1), 'Kept', 4)] },
      tombstones: {},
      next: 4,
      nextGlobal: 0,
      more: false,
    },
  ]
  await syncNow(db)
  expect((await db.store.toArray()).map((s) => s.name)).toEqual(['Kept'])

  // A row still waiting to be sent is not a ghost.
  server.online = false
  await dispatch('catalog.store.create', { id: sid(3), name: 'Queued' }, db)
  server.online = true
  await db.meta.put({ key: 'sync.settle', value: true })
  await pull(db)
  expect((await db.store.toArray()).map((s) => s.name).sort()).toEqual(['Kept', 'Queued'])
})

test('a rejected command is kept for the sheet and its optimistic change disappears', async () => {
  await dispatch('catalog.store.create', { id: sid(1), name: 'Ghost' }, db)
  server.rejectTypes.add('catalog.store.create')
  server.pages = [
    {
      rows: { store: [storeRow(sid(3), 'Real', 3)] },
      tombstones: {},
      next: 3,
      nextGlobal: 0,
      more: false,
    },
  ]
  await syncNow(db)
  const [row] = await db.outbox.toArray()
  expect(row).toMatchObject({ status: 'rejected' })
  expect(JSON.parse(row?.error ?? '{}')).toMatchObject({ code: 'gone' })
  expect((await db.store.toArray()).map((s) => s.name)).toEqual(['Real'])
  expect(server.syncCalls[0]).toContain('since=0') // full re-pull

  server.pages = []
  await discardRejected(row?.seq as number, db)
  await syncNow(db)
  expect(await db.outbox.count()).toBe(0)
})

test('retrying a rejected command queues it again under a new id', async () => {
  await dispatch('catalog.store.create', { id: sid(1), name: 'Again' }, db)
  server.rejectTypes.add('catalog.store.create')
  await syncNow(db)
  const [rejected] = await db.outbox.toArray()
  server.rejectTypes.clear()
  await retryRejected(rejected?.seq as number, db)
  await vi.waitFor(async () => expect(await db.outbox.count()).toBe(0))
  expect(server.applied.map((c) => c.payload.name)).toEqual(['Again'])
  expect(server.applied[0]?.id).not.toBe(rejected?.id)
})

test('switching household parks the outbox and replays it on return', async () => {
  const B = '0190a1b2-0000-7000-8000-0000000000a2'
  await dispatch('catalog.store.create', { id: sid(1), name: 'A' }, db)
  await adoptHousehold(HH, db) // same household: untouched
  expect(await db.store.count()).toBe(1)
  await adoptHousehold(B, db)
  expect(await db.store.count()).toBe(0)
  expect(await db.outbox.count()).toBe(1)
  expect((await db.meta.get('sync.household'))?.value).toBe(B)
  await flushOutbox(db)
  expect(server.applied).toEqual([]) // A's command is never sent to B
  await adoptHousehold(HH, db)
  expect((await db.store.toArray()).map((s) => s.name)).toEqual(['A'])
  await flushOutbox(db)
  expect(server.applied.map((c) => c.payload.name)).toEqual(['A'])
})

test('a poison command that makes the server 500 is isolated and the queue moves on', async () => {
  await dispatch('catalog.store.create', { id: sid(1), name: 'Poison' }, db)
  await dispatch('catalog.store.create', { id: sid(2), name: 'Fine' }, db)
  server.failNames.add('Poison')
  for (let i = 0; i < 6; i++) await flushOutbox(db)
  expect(server.applied.map((c) => c.payload.name)).toEqual(['Fine'])
  const rows = await db.outbox.toArray()
  expect(rows.filter((r) => r.status === 'rejected')).toHaveLength(1)
})
