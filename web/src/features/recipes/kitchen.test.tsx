import 'fake-indexeddb/auto'
import { fireEvent, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import type { RecipeLine } from '../../offline/replica'
import { jsonResponse, renderApp } from '../../test-utils'
import { addDays, today } from '../inventory/dates'

const HH = '0190a1b2-0000-7000-8000-0000000000a1'
const base = {
  householdId: HH,
  createdAt: '2026-11-01T12:00:00Z',
  updatedAt: '2026-11-01T12:00:00Z',
  rev: 1,
}
let calls: Request[] = []
let staples: string[] | null = null

beforeEach(async () => {
  calls = []
  staples = null
  vi.stubGlobal('fetch', async (req: Request) => {
    calls.push(req)
    if (req.url.endsWith('/v1/recipes/staples') && staples) return jsonResponse({ slugs: staples })
    if (req.url.endsWith('/v1/recipes/ideas'))
      return jsonResponse(
        { type: 'about:blank', title: 'Service Unavailable', status: 503, code: 'llm_unavailable' },
        503,
        'application/problem+json',
      )
    throw new TypeError('offline')
  })
  await db.meta.put({ key: 'sync.household', value: HH })
  await seedKitchen()
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await Promise.all(db.tables.map((t) => t.clear()))
})

const ing = (id: string, parentId: string | null = null) => ({
  ...base,
  householdId: null,
  id,
  slug: id,
  name: id,
  plural: null,
  parentId,
  categoryId: null,
  icon: null,
  aliases: [],
  shelfLife: {},
  defaultUnit: null,
  source: 'seed' as const,
})

const product = (id: string, name: string, ingredientId: string) => ({
  ...base,
  id,
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
  matchSource: 'rule' as const,
  matchConfidence: 1,
  enrichState: 'done' as const,
  enrichAttempts: 0,
  offCode: null,
  note: null,
})

const stock = (id: string, productId: string, quantity: string, expiresOn: string | null) => ({
  ...base,
  id,
  productId,
  locationId: null,
  amountKind: 'exact' as const,
  quantity,
  unit: 'ea',
  portions: null,
  level: null,
  low: false,
  expiresOn,
  expiryKind: null,
  openedOn: null,
  thawedOn: null,
  frozenOn: null,
  source: 'manual' as const,
  parentItemId: null,
  note: null,
})

const line = (
  id: string,
  raw: string,
  name: string,
  ingredient_id?: string,
  more = {},
): RecipeLine => ({
  line_id: id,
  raw,
  name,
  optional: false,
  staple: false,
  ...(ingredient_id ? { ingredient_id } : {}),
  ...more,
})

async function seedKitchen() {
  const now = today()
  await db.ingredient.bulkPut([
    ing('chicken'),
    ing('chicken-thigh', 'chicken'),
    ing('cilantro'),
    ing('greek-yogurt'),
    ing('coconut-milk'),
    ing('onion'),
    ing('rice'),
    ing('water'),
  ])
  await db.product.bulkPut([
    product('p-thigh', 'Chicken thighs', 'chicken-thigh'),
    product('p-cil', 'Cilantro', 'cilantro'),
    product('p-yog', 'Greek yogurt', 'greek-yogurt'),
    product('p-onion', 'Yellow onions', 'onion'),
    product('p-rice', 'Basmati', 'rice'),
    product('p-coco', 'Coconut milk', 'coconut-milk'),
  ])
  await db.stock_item.bulkPut([
    stock('s-thigh', 'p-thigh', '800', addDays(now, 1)),
    stock('s-cil', 'p-cil', '1', addDays(now, 2)),
    stock('s-yog', 'p-yog', '1', addDays(now, 12)),
    stock('s-onion', 'p-onion', '0', null),
  ])
  await db.substitution.put({
    ...base,
    householdId: null,
    id: 'sub-1',
    ingredientId: 'coconut-milk',
    uses: [
      { ingredient_id: 'greek-yogurt', ratio: 0.5 },
      { ingredient_id: 'water', ratio: 0.25 },
    ],
    note: 'Use Greek yogurt thinned with water.',
    recipeId: null,
    source: 'seed',
  })
  await db.recipe.put({
    ...base,
    id: 'curry',
    title: 'Chicken curry',
    subtitle: null,
    sourceKind: 'manual',
    sourceUrl: null,
    sourceLang: null,
    currentVersionId: 'curry-v1',
    servings: 4,
    totalMinutes: 45,
    coverBlob: null,
    tags: [],
    saved: true,
    ideaExpiresAt: null,
    cookedCount: 0,
    lastCookedAt: null,
    importId: null,
  })
  await db.recipe_version.put({
    ...base,
    id: 'curry-v1',
    recipeId: 'curry',
    n: 1,
    parentVersionId: null,
    origin: 'manual',
    changeSummary: null,
    title: 'Chicken curry',
    servings: 4,
    totalMinutes: 45,
    ingredients: [
      line('l1', '800 g chicken thighs', 'chicken thighs', 'chicken'),
      line('l2', '1 can coconut milk', 'coconut milk', 'coconut-milk', { qty: '1', unit: 'can' }),
      line('l3', '1 bunch cilantro', 'cilantro', 'cilantro'),
      line('l4', '2 onions', 'onions', 'onion', { qty: '2', unit: 'ea' }),
      line('l5', '1 cup basmati rice', 'basmati rice', 'rice', { qty: '1', unit: 'cup' }),
      line('l6', '1 pinch saffron', 'saffron'),
      line('l7', 'salt to taste', 'salt', undefined, { optional: true }),
    ],
    steps: [],
    notes: null,
    original: null,
    originalVersionId: null,
    createdBy: null,
  })
}

test('kitchen tab ranks recipes with what is in stock, soon chips and the first swap', async () => {
  renderApp('/recipes')
  expect(await screen.findByRole('link', { name: 'Chicken curry' })).toBeInTheDocument()
  expect(screen.getByText(/^2 of 6/)).toBeInTheDocument()
  expect(screen.getByText('Chicken thighs, tomorrow')).toBeInTheDocument()
  expect(screen.getByText('Cilantro, 2 days')).toBeInTheDocument()
  expect(
    screen.getByText('No coconut milk? Use Greek yogurt thinned with water.'),
  ).toBeInTheDocument()
  expect(screen.getByText(/Your library/)).toBeInTheDocument()

  // The assistant is down: the manual path is explained, nothing breaks.
  fireEvent.click(screen.getByRole('button', { name: /More ideas/ }))
  expect(await screen.findByText(/The assistant is unavailable right now/)).toBeInTheDocument()
})

test("the household's staples are skipped, not the defaults", async () => {
  staples = ['water', 'rice']
  renderApp('/recipes')
  // Rice is a staple here: 2 of 5 instead of 2 of 6.
  expect(await screen.findByText(/^2 of 5/)).toBeInTheDocument()
  expect((await db.meta.get('recipes.staples'))?.value).toEqual(['water', 'rice'])
})

test('cook tonight needs 70% in stock and something urgent', async () => {
  renderApp('/')
  await screen.findByText('Use soon')
  // 2 of 6 in stock: not enough for Cook tonight.
  expect(screen.queryByText('Cook tonight')).not.toBeInTheDocument()

  await db.stock_item.bulkPut([
    stock('s-onion', 'p-onion', '3', null),
    stock('s-rice', 'p-rice', '2', null),
    stock('s-coco', 'p-coco', '1', null),
  ])
  expect(await screen.findByText('Cook tonight')).toBeInTheDocument()
  expect(
    screen.getByText(
      /Uses the chicken thighs \(tomorrow\) and the cilantro \(2 days\)\. You have 5 of 6/,
    ),
  ).toBeInTheDocument()
})

test('missing to list adds missing lines with products and the recipe, once', async () => {
  await db.shopping_list.put({ ...base, id: 'list-1', name: 'Groceries', sort: 10 })
  renderApp('/recipes/curry')
  const button = await screen.findByRole('button', { name: 'Missing to list (3)' })
  fireEvent.click(button)
  expect(await screen.findByText('Added 3 to Groceries')).toBeInTheDocument()
  const items = await db.shopping_item.toArray()
  const byText = Object.fromEntries(items.map((i) => [i.text, i]))
  expect(Object.keys(byText).sort()).toEqual(['Basmati', 'Yellow onions', 'saffron'])
  expect(byText['Yellow onions']).toMatchObject({
    productId: 'p-onion',
    qty: '2',
    unit: 'ea',
    recipeId: 'curry',
    listId: 'list-1',
  })
  expect(byText.Basmati).toMatchObject({ productId: 'p-rice', qty: '1', unit: 'cup' })
  // A line with no product gets a new one named after it (the server creates it).
  expect(byText.saffron).toMatchObject({ qty: null, recipeId: 'curry' })
  expect(byText.saffron?.productId).toBeTruthy()
  expect((await db.product.get(byText.saffron?.productId ?? ''))?.name).toBe('saffron')

  // A second tap adds nothing: they're already open on the list.
  fireEvent.click(screen.getByRole('button', { name: 'Missing to list (3)' }))
  expect(await screen.findByText('Already on Groceries')).toBeInTheDocument()
  expect(await db.shopping_item.count()).toBe(3)

  // The shopping list says why they're there.
  renderApp('/shopping')
  expect((await screen.findAllByText(/Recipe: Chicken curry/)).length).toBeGreaterThan(0)
})
