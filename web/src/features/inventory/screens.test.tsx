import 'fake-indexeddb/auto'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import type { Location, Product, StockItem } from '../../offline/replica'
import { adminMe, renderApp } from '../../test-utils'
import { addDays, today } from './dates'

const HH = adminMe.activeHouseholdId as string
const base = { householdId: HH, createdAt: '2026-11-01T00:00:00Z', updatedAt: '', rev: 1 }

beforeEach(async () => {
  vi.stubGlobal('fetch', async () => {
    throw new TypeError('offline') // the replica and outbox are all the UI needs
  })
  await db.meta.put({ key: 'sync.household', value: HH })
  const loc = (id: string, name: string, kind: Location['kind'], sort: number): Location => ({
    ...base,
    id,
    name,
    kind,
    sort,
  })
  await db.location.bulkPut([
    loc('l-fridge', 'Fridge', 'fridge', 1),
    loc('l-freezer', 'Freezer', 'freezer', 2),
  ])
  const prod = (id: string, name: string): Product => ({
    ...base,
    id,
    name,
    brand: null,
    ingredientId: null,
    extraIngredientIds: [],
    categoryId: null,
    icon: null,
    defaultStoreId: null,
    defaultQty: null,
    defaultUnit: null,
    package: null,
    defaultLocationKind: null,
    shelfLife: null,
    matchSource: 'none',
    matchConfidence: null,
    enrichState: 'done',
    enrichAttempts: 0,
    offCode: null,
    note: null,
  })
  await db.product.bulkPut([prod('p-milk', 'Milk'), prod('p-rice', 'Rice'), prod('p-eggs', 'Eggs')])
  const item = (id: string, productId: string, over: Partial<StockItem>): StockItem => ({
    ...base,
    id,
    productId,
    locationId: 'l-fridge',
    amountKind: 'exact',
    quantity: '2',
    unit: 'l',
    portions: null,
    level: null,
    low: false,
    expiresOn: null,
    expiryKind: null,
    openedOn: null,
    thawedOn: null,
    frozenOn: null,
    source: 'manual',
    parentItemId: null,
    note: null,
    ...over,
  })
  await db.stock_item.bulkPut([
    item('s-milk', 'p-milk', { expiresOn: addDays(today(), -1), expiryKind: 'best_before' }),
    item('s-eggs', 'p-eggs', {
      expiresOn: addDays(today(), 2),
      expiryKind: 'use_by',
      quantity: '12',
      unit: 'ea',
    }),
    item('s-rice', 'p-rice', {
      expiresOn: addDays(today(), 300),
      amountKind: 'level',
      level: 'low',
      quantity: null,
      unit: null,
    }),
  ])
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await Promise.all(db.tables.map((t) => t.clear()))
})

const outbox = async () => (await db.outbox.toArray()).map((r) => r.type)

test('home counts what is expired, soon and low, and lists what to eat first', async () => {
  renderApp('/')
  const eat = await screen.findByRole('region', { name: 'Eat first' })
  const rows = within(eat).getAllByRole('listitem')
  expect(rows.map((r) => r.textContent)).toEqual([
    expect.stringContaining('Milk'), // expired comes before expiring soon
    expect.stringContaining('Eggs'),
  ])
  expect(within(rows[0] as HTMLElement).getByText('Expired yesterday')).toBeInTheDocument()
  const stat = (show: string) =>
    screen.getAllByRole('link').find((l) => l.getAttribute('href') === `/inventory?show=${show}`)
  expect(stat('expired')).toHaveTextContent('1')
  expect(stat('soon')).toHaveTextContent('1')
  expect(stat('low')).toHaveTextContent('1')
})

test('inventory groups by place, sorts by expiry, searches and filters', async () => {
  const user = userEvent.setup()
  renderApp('/inventory')
  const fridge = await screen.findByRole('region', { name: 'Fridge' })
  expect(
    within(fridge)
      .getAllByRole('listitem')
      .map((r) => r.textContent?.slice(0, 4)),
  ).toEqual(['Milk', 'Eggs', 'Rice'])
  await user.type(screen.getByRole('textbox', { name: 'Search' }), 'egg')
  expect(within(fridge).getAllByRole('listitem')).toHaveLength(1)
  await user.clear(screen.getByRole('textbox', { name: 'Search' }))
  await user.click(screen.getByRole('radio', { name: 'Name' }))
  expect(
    within(fridge)
      .getAllByRole('listitem')
      .map((r) => r.textContent?.slice(0, 4)),
  ).toEqual(['Eggs', 'Milk', 'Rice'])
})

test('item detail uses one less, marks low and discards, all through the outbox', async () => {
  const user = userEvent.setup()
  renderApp('/inventory/s-eggs')
  expect(await screen.findByRole('heading', { name: 'Eggs', level: 2 })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Use one less' }))
  await waitFor(async () => expect((await db.stock_item.get('s-eggs'))?.quantity).toBe('11'))
  await user.click(screen.getByRole('switch', { name: 'Running low' }))
  await waitFor(async () => expect((await db.stock_item.get('s-eggs'))?.low).toBe(true))
  await user.click(screen.getByRole('button', { name: 'Discard' }))
  await user.click(await screen.findByRole('button', { name: 'Spoiled' }))
  await waitFor(async () => expect(await db.stock_item.get('s-eggs')).toBeUndefined())
  expect(await outbox()).toEqual([
    'inventory.stock.consume',
    'inventory.stock.mark_low',
    'inventory.stock.discard',
  ])
  expect(await screen.findByRole('heading', { name: 'Inventory', level: 1 })).toBeInTheDocument()
})

test('a rough-level item shows level choices instead of a stepper', async () => {
  const user = userEvent.setup()
  renderApp('/inventory/s-rice')
  await screen.findByRole('heading', { name: 'Rice', level: 2 })
  expect(screen.queryByRole('button', { name: 'Use one less' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('radio', { name: 'Half' }))
  await waitFor(async () => expect((await db.stock_item.get('s-rice'))?.level).toBe('half'))
})

test('manual add: pick a product, choose where and how much, and it queues an add', async () => {
  const user = userEvent.setup()
  renderApp('/add')
  await user.type(await screen.findByRole('textbox', { name: 'Item name' }), 'rice')
  await user.click(await screen.findByRole('button', { name: /Rice/ }))
  await user.click(screen.getByRole('radio', { name: 'Freezer' }))
  const qty = screen.getByRole('textbox', { name: 'Quantity' })
  await user.clear(qty)
  await user.type(qty, '3')
  await user.click(screen.getByRole('button', { name: 'Add to inventory' }))
  await waitFor(async () => expect(await outbox()).toEqual(['inventory.stock.add']))
  const [row] = await db.outbox.toArray()
  expect(row?.payload).toMatchObject({
    productId: 'p-rice',
    locationId: 'l-freezer',
    amountKind: 'exact',
    quantity: '3',
    source: 'manual',
  })
})
