import 'fake-indexeddb/auto'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import fixture from '../../../../internal/inventory/testdata/estimate.json'
import { StockpotDB } from '../../offline/db'
import { dispatch } from '../../offline/outbox'
import type { Category, Ingredient, Location, Product, StockItem } from '../../offline/replica'
import { pull } from '../../offline/sync'
import { addDays, daysBetween } from './dates'
import { add, cmp, splitEvenly, sub, subClamped } from './decimal'
import { type Chain, estimate, type Op } from './estimate'
import './commands'

const HH = '0190a1b2-0000-7000-8000-0000000000a1'
const db = new StockpotDB('inventory-test')
const base = { householdId: HH, createdAt: '2026-11-01T00:00:00Z', updatedAt: '', rev: 1 }

beforeEach(async () => {
  await db.open()
  await db.meta.put({ key: 'sync.household', value: HH })
})
afterEach(async () => {
  vi.unstubAllGlobals()
  await db.delete()
})

test('decimals are exact', () => {
  expect(add('0.1', '0.2')).toBe('0.3')
  expect(sub('1.5', '0.25')).toBe('1.25')
  expect(subClamped('1', '2.5')).toBe('0')
  expect(cmp('2.0', '2')).toBe(0)
  expect(splitEvenly('1.5', 4)).toEqual(['0.375', '0.375', '0.375', '0.375'])
  expect(splitEvenly('1', 3)).toEqual(['0.333', '0.333', '0.334'])
})

test('date arithmetic ignores time zones and months', () => {
  expect(addDays('2026-11-27', 21)).toBe('2026-12-18')
  expect(addDays('2026-12-31', 1)).toBe('2027-01-01')
  expect(daysBetween('2026-11-27', '2026-11-25')).toBe(-2)
})

// The same cases as the server's Go test: both estimators must agree.
test.each(fixture.map((c) => [c.name, c] as const))('estimate fixture: %s', (_name, c) => {
  expect(estimate(c.chain as Chain, c.op as Op, c.location as Location['kind'], c.on)).toBe(c.want)
})

const location = (id: string, kind: Location['kind']): Location => ({
  ...base,
  id,
  name: kind,
  kind,
  sort: 1,
})
const category: Category = {
  ...base,
  id: 'c-fruit',
  slug: 'produce-fruit',
  name: 'Fruit',
  icon: null,
  sort: 1,
}
const ingredient: Ingredient = {
  ...base,
  id: 'i-mandarin',
  slug: 'mandarin',
  name: 'Mandarin',
  plural: null,
  parentId: 'i-citrus',
  categoryId: 'c-fruit',
  icon: null,
  aliases: [],
  shelfLife: { fridge: 21 },
  defaultUnit: 'ea',
  source: 'seed',
}
const parent: Ingredient = {
  ...ingredient,
  id: 'i-citrus',
  slug: 'citrus',
  name: 'Citrus',
  parentId: null,
  shelfLife: { freezer: 200 },
}
const product: Product = {
  ...base,
  id: 'p-mand',
  name: 'Mandarins',
  brand: null,
  ingredientId: 'i-mandarin',
  extraIngredientIds: [],
  categoryId: null,
  icon: null,
  defaultStoreId: null,
  defaultQty: null,
  defaultUnit: null,
  package: null,
  defaultLocationKind: null,
  shelfLife: null,
  matchSource: 'rule',
  matchConfidence: 1,
  enrichState: 'done',
  enrichAttempts: 0,
  offCode: null,
  note: null,
}

async function seed() {
  await db.location.bulkPut([
    location('l-fridge', 'fridge'),
    location('l-freezer', 'freezer'),
    location('l-pantry', 'pantry'),
  ])
  await db.category.put(category)
  await db.ingredient.bulkPut([ingredient, parent])
  await db.product.put(product)
}

const ids = (n: number) => ({
  id: `0190a1b2-0000-7000-8000-00000000030${n}`,
  eventId: `0190a1b2-0000-7000-8000-00000000040${n}`,
})
const ON = '2026-11-27'

const exact = (over: Partial<StockItem> = {}): StockItem => ({
  ...base,
  id: ids(9).id,
  productId: 'p-mand',
  locationId: 'l-fridge',
  amountKind: 'exact',
  quantity: '5',
  unit: 'lb',
  portions: null,
  level: null,
  low: false,
  expiresOn: '2026-12-18',
  expiryKind: 'estimated',
  openedOn: null,
  thawedOn: null,
  frozenOn: null,
  source: 'manual',
  parentItemId: null,
  note: null,
  ...over,
})

test('add estimates the expiry from the seed and walks up to ancestors for missing keys', async () => {
  await seed()
  await dispatch(
    'inventory.stock.add',
    {
      ...ids(1),
      productId: 'p-mand',
      locationId: 'l-fridge',
      amountKind: 'exact',
      quantity: '2',
      unit: 'lb',
      source: 'manual',
      on: ON,
    },
    db,
  )
  expect(await db.stock_item.get(ids(1).id)).toMatchObject({
    expiresOn: '2026-12-18',
    expiryKind: 'estimated',
    quantity: '2',
  })
  // Freezer days come from the ancestor (citrus), not the mandarin seed.
  const f = ids(2)
  await dispatch(
    'inventory.stock.add',
    {
      ...f,
      productId: 'p-mand',
      locationId: 'l-freezer',
      amountKind: 'exact',
      quantity: '1',
      unit: 'lb',
      source: 'manual',
      on: ON,
    },
    db,
  )
  expect(await db.stock_item.get(f.id)).toMatchObject({ expiresOn: addDays(ON, 200), frozenOn: ON })
  expect(await db.stock_event.get(ids(1).eventId)).toMatchObject({ kind: 'add', delta: '2' })
})

test('a cached household rule overrides the seed', async () => {
  await seed()
  await db.meta.put({
    key: 'foodRules',
    value: [
      {
        subjectKind: 'ingredient',
        subjectId: 'i-mandarin',
        householdId: HH,
        shelfLife: { fridge: 10 },
        updatedAt: '',
      },
    ],
  })
  await dispatch(
    'inventory.stock.add',
    {
      ...ids(1),
      productId: 'p-mand',
      locationId: 'l-fridge',
      amountKind: 'exact',
      quantity: '2',
      unit: 'lb',
      source: 'manual',
      on: ON,
    },
    db,
  )
  expect((await db.stock_item.get(ids(1).id))?.expiresOn).toBe(addDays(ON, 10))
})

test('add by name creates the product first', async () => {
  await seed()
  await dispatch(
    'inventory.stock.add',
    {
      ...ids(1),
      newProduct: { id: 'p-new', name: 'Clementines', ingredientId: 'i-mandarin' },
      locationId: 'l-fridge',
      amountKind: 'level',
      level: 'full',
      source: 'manual',
      on: ON,
    },
    db,
  )
  expect(await db.product.get('p-new')).toMatchObject({
    name: 'Clementines',
    ingredientId: 'i-mandarin',
  })
  expect((await db.stock_item.get(ids(1).id))?.productId).toBe('p-new')
})

test('consume takes a delta, finishes at zero and logs what was used', async () => {
  await seed()
  await db.stock_item.put(exact())
  await dispatch(
    'inventory.stock.consume',
    { id: ids(9).id, eventId: ids(1).eventId, quantity: '1.5' },
    db,
  )
  expect((await db.stock_item.get(ids(9).id))?.quantity).toBe('3.5')
  expect(await db.stock_event.get(ids(1).eventId)).toMatchObject({
    kind: 'consume',
    delta: '-1.5',
    unit: 'lb',
  })
  await dispatch(
    'inventory.stock.consume',
    { id: ids(9).id, eventId: ids(2).eventId, quantity: '10' },
    db,
  )
  expect(await db.stock_item.get(ids(9).id)).toBeUndefined()
  expect(await db.stock_event.get(ids(2).eventId)).toMatchObject({ delta: '-3.5' })
})

test('freeze, thaw and open restart or shorten the clock', async () => {
  await seed()
  await db.stock_item.put(exact({ expiresOn: '2026-12-01', expiryKind: 'use_by' }))
  const id = ids(9).id
  await dispatch(
    'inventory.stock.freeze',
    { id, eventId: ids(1).eventId, locationId: 'l-freezer', on: ON },
    db,
  )
  expect(await db.stock_item.get(id)).toMatchObject({
    locationId: 'l-freezer',
    frozenOn: ON,
    expiresOn: addDays(ON, 200),
    expiryKind: 'estimated',
  })
  await dispatch(
    'inventory.stock.thaw',
    { id, eventId: ids(2).eventId, locationId: 'l-fridge', on: ON },
    db,
  )
  // No thawed_fridge rule in this replica: falls back to the fridge days.
  expect(await db.stock_item.get(id)).toMatchObject({ thawedOn: ON, expiresOn: addDays(ON, 21) })
  await dispatch(
    'inventory.stock.update',
    { id, eventId: ids(3).eventId, expiresOn: addDays(ON, 2), expiryKind: 'use_by' },
    db,
  )
  await db.meta.put({
    key: 'foodRules',
    value: [
      {
        subjectKind: 'category',
        subjectId: 'c-fruit',
        shelfLife: { opened_fridge: 5 },
        updatedAt: '',
      },
    ],
  })
  await dispatch('inventory.stock.open', { id, eventId: ids(4).eventId, on: ON }, db)
  // An earlier use-by is never pushed out by opening.
  expect(await db.stock_item.get(id)).toMatchObject({
    openedOn: ON,
    expiresOn: addDays(ON, 2),
    expiryKind: 'use_by',
  })
})

test('split cuts parts off and finishes the parent when nothing is left', async () => {
  await seed()
  await db.stock_item.put(exact({ quantity: '1.5', unit: 'kg' }))
  const parts = [1, 2, 3].map((n) => ({
    ...ids(n),
    id: `0190a1b2-0000-7000-8000-00000000050${n}`,
    quantity: '0.5',
  }))
  await dispatch(
    'inventory.stock.split',
    { id: ids(9).id, eventId: ids(8).eventId, parts, locationId: 'l-freezer', on: ON },
    db,
  )
  expect(await db.stock_item.get(ids(9).id)).toBeUndefined()
  expect(await db.stock_item.get(parts[0]?.id as string)).toMatchObject({
    quantity: '0.5',
    locationId: 'l-freezer',
    parentItemId: ids(9).id,
    source: 'split',
    frozenOn: ON,
  })
  expect(await db.stock_item.count()).toBe(3)
})

const stockRow = (item: StockItem, rev: number) => ({
  id: item.id,
  household_id: HH,
  rev,
  product_id: item.productId,
  location_id: item.locationId,
  amount_kind: item.amountKind,
  quantity: item.quantity,
  unit: item.unit,
  portions: null,
  level: null,
  low: false,
  expires_on: item.expiresOn,
  expiry_kind: item.expiryKind,
  source: 'manual',
  extra_ingredient_ids: [],
})
const storeRow = (id: string, rev: number) => ({ id, household_id: HH, rev, name: id, sort: 1 })

function stubSync(pages: unknown[]) {
  vi.stubGlobal('fetch', async (req: Request) => {
    if (new URL(req.url).pathname !== '/api/v1/sync') return new Response('{}', { status: 404 })
    return new Response(JSON.stringify(pages.shift()), {
      headers: { 'Content-Type': 'application/json' },
    })
  })
}

// A consume is a delta: it must land once on top of server state however the pull is paged.
test('a queued consume is applied once over a pull that spans two pages', async () => {
  await seed()
  await db.stock_item.put(exact({ quantity: '5' }))
  await dispatch(
    'inventory.stock.consume',
    { id: ids(9).id, eventId: ids(1).eventId, quantity: '1' },
    db,
  )
  expect((await db.stock_item.get(ids(9).id))?.quantity).toBe('4')

  stubSync([
    // Page 1 rewrites the item with server truth (5): the queued consume re-applies on it.
    {
      rows: { stock_item: [stockRow(exact({ quantity: '5' }), 10)] },
      tombstones: {},
      next: 10,
      nextGlobal: 0,
      more: true,
    },
    // Page 2 touches only unrelated rows: the consume must not run again.
    {
      rows: { store: [storeRow('s-1', 11)] },
      tombstones: {},
      next: 11,
      nextGlobal: 0,
      more: false,
    },
  ])
  expect(await pull(db)).toBe(true)
  expect((await db.stock_item.get(ids(9).id))?.quantity).toBe('4')
  expect(await db.store.count()).toBe(1)
})

test('a queued consume replays on each newer copy of its item, still only once on top', async () => {
  await seed()
  await db.stock_item.put(exact({ quantity: '5' }))
  await dispatch(
    'inventory.stock.consume',
    { id: ids(9).id, eventId: ids(1).eventId, quantity: '1' },
    db,
  )
  stubSync([
    {
      rows: { stock_item: [stockRow(exact({ quantity: '5' }), 10)] },
      tombstones: {},
      next: 10,
      nextGlobal: 0,
      more: true,
    },
    // The item changed on another device between pages (someone used 2 more).
    {
      rows: { stock_item: [stockRow(exact({ quantity: '3' }), 12)] },
      tombstones: {},
      next: 12,
      nextGlobal: 0,
      more: false,
    },
  ])
  await pull(db)
  expect((await db.stock_item.get(ids(9).id))?.quantity).toBe('2') // 3 from the server, minus our 1
})
