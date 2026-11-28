import 'fake-indexeddb/auto'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import { pendingCommands } from '../../offline/outbox'
import type { Ingredient, Product, RecipeLine, StockItem } from '../../offline/replica'
import { renderApp } from '../../test-utils'

const HH = '0190a1b2-0000-7000-8000-0000000000a1'
const R = '0190a1b2-0000-7000-8000-00000000f001'
const V = '0190a1b2-0000-7000-8000-0000000000b1'
const T = '2026-11-20T10:00:00Z'
const base = (id: string) => ({ id, householdId: HH, createdAt: T, updatedAt: T, rev: 5 })

const ing = (id: string, conversions = {}): Ingredient => ({
  ...base(id),
  slug: id,
  name: id,
  plural: null,
  parentId: null,
  categoryId: null,
  icon: null,
  aliases: [],
  shelfLife: {},
  defaultUnit: null,
  source: 'seed',
  conversions,
})
const prod = (id: string, name: string, ingredientId: string): Product => ({
  ...base(id),
  name,
  brand: null,
  ingredientId,
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
})
const stock = (id: string, productId: string, a: Partial<StockItem>): StockItem => ({
  ...base(id),
  productId,
  locationId: 'l-fridge',
  amountKind: 'exact',
  quantity: null,
  unit: null,
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
  ...a,
})
const line = (n: string, raw: string, l: Partial<RecipeLine>): RecipeLine => ({
  line_id: `0190a1b2-0000-7000-8000-0000000001${n}`,
  raw,
  name: raw,
  optional: false,
  staple: false,
  ...l,
})

beforeEach(async () => {
  vi.stubGlobal('fetch', async () => {
    throw new TypeError('offline')
  })
  await db.meta.put({ key: 'sync.household', value: HH })
  await db.location.bulkPut([
    { ...base('l-fridge'), name: 'Fridge', kind: 'fridge', sort: 10 },
    { ...base('l-freezer'), name: 'Freezer', kind: 'freezer', sort: 20 },
  ])
  await db.shopping_list.put({ ...base('list'), name: 'Groceries', sort: 0 })
  await db.ingredient.bulkPut([ing('chicken'), ing('onion', { ea: '150 g' }), ing('coconut-milk')])
  await db.product.bulkPut([
    prod('p-ch', 'Chicken thighs', 'chicken'),
    prod('p-on', 'Onions', 'onion'),
  ])
  await db.product_stat.put({
    ...base('st'),
    productId: 'p-on',
    storeId: null,
    timesBought: 4,
    lastBoughtAt: null,
    typicalQty: null,
    typicalUnit: null,
    medianIntervalDays: null,
    lastQty: null,
  })
  await db.stock_item.bulkPut([
    stock('s-ch', 'p-ch', { quantity: '900', unit: 'g', expiresOn: '2026-12-01' }),
    stock('s-on', 'p-on', { quantity: '2', unit: 'ea' }),
  ])
  await db.recipe.put({
    ...base(R),
    title: 'Chicken curry',
    subtitle: null,
    sourceKind: 'manual',
    sourceUrl: null,
    sourceLang: null,
    currentVersionId: V,
    servings: 4,
    totalMinutes: null,
    coverBlob: null,
    tags: [],
    saved: true,
    ideaExpiresAt: null,
    cookedCount: 0,
    lastCookedAt: null,
    importId: null,
  })
  await db.recipe_version.put({
    ...base(V),
    recipeId: R,
    n: 1,
    parentVersionId: null,
    origin: 'manual',
    changeSummary: null,
    title: 'Chicken curry',
    servings: 4,
    totalMinutes: null,
    ingredients: [
      line('01', '800 g chicken', {
        qty: '800',
        unit: 'g',
        name: 'chicken',
        ingredient_id: 'chicken',
      }),
      line('02', '2 onions', { qty: '2', unit: 'ea', name: 'onions', ingredient_id: 'onion' }),
      line('03', '1 can coconut milk', {
        qty: '1',
        unit: 'can',
        name: 'coconut milk',
        ingredient_id: 'coconut-milk',
      }),
      line('04', 'salt to taste', { name: 'salt', optional: true, staple: true }),
    ],
    steps: [],
    notes: null,
    original: null,
    originalVersionId: null,
    createdBy: null,
  })
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await Promise.all(db.tables.map((t) => t.clear()))
})

test('the cook sheet suggests amounts and sends one recipe.cook', async () => {
  renderApp(`/recipes/${R}/cook`)
  expect(await screen.findByRole('heading', { name: 'Chicken curry' })).toBeInTheDocument()
  // Chicken: 800 g of 900 g; onions: 2 × 150 g = 300 g, but the item is 2 ea → uses it all.
  expect(await screen.findByText('100 g')).toBeInTheDocument()
  expect(screen.getByText('Uses it all.')).toBeInTheDocument()
  expect(screen.getByRole('switch', { name: 'Add onions to the shopping list' })).toBeChecked()
  expect(screen.getByText('Not in stock')).toBeInTheDocument()
  expect(screen.queryByText('salt')).not.toBeInTheDocument()

  // Two portions: chicken halves.
  fireEvent.click(screen.getByRole('button', { name: 'Less portions made' }))
  fireEvent.click(screen.getByRole('button', { name: 'Less portions made' }))
  expect(await screen.findByText('500 g')).toBeInTheDocument()

  // One portion to the freezer, the other stays in the fridge.
  fireEvent.click(screen.getByRole('button', { name: 'More portions in the freezer' }))
  fireEvent.click(screen.getByRole('button', { name: 'Update stock and store 2 portions' }))

  await waitFor(async () => {
    const cmds = await pendingCommands()
    expect(cmds.map((c) => c.type)).toContain('recipe.cook')
  })
  const cook = (await pendingCommands()).find((c) => c.type === 'recipe.cook')
  const p = cook?.payload as {
    portionsMade: number
    uses: { stockItemId: string; quantity?: string; finished?: boolean; toShopping?: unknown }[]
    store: { locationId: string; portions: number }[]
  }
  expect(p.portionsMade).toBe(2)
  expect(p.uses).toEqual([
    expect.objectContaining({ stockItemId: 's-ch', quantity: '400' }),
    expect.objectContaining({ stockItemId: 's-on', quantity: '1' }),
  ])
  expect(p.store).toEqual([
    expect.objectContaining({ locationId: 'l-fridge', portions: 1 }),
    expect.objectContaining({ locationId: 'l-freezer', portions: 1 }),
  ])
  expect((await db.stock_item.get('s-ch'))?.quantity).toBe('500')
})
