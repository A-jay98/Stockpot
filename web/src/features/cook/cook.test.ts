import 'fake-indexeddb/auto'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import leftoverNames from '../../../../internal/cook/testdata/leftover_names.json'
import { format } from '../../lib/units'
import { StockpotDB } from '../../offline/db'
import { dispatch } from '../../offline/outbox'
import type {
  Category,
  Ingredient,
  Location,
  Product,
  Recipe,
  RecipeLine,
  RecipeVersion,
  StockItem,
} from '../../offline/replica'
import { type CookUseRecord, leftoverName, undoEventId } from './commands'
import {
  type Catalog,
  leftAfter,
  matchingItems,
  nextLevel,
  scaledQty,
  suggest,
  usesAll,
} from './plan'

const HH = '0190a1b2-0000-7000-8000-0000000000a1'
const base = { householdId: HH, createdAt: '2026-11-01T00:00:00Z', updatedAt: '', rev: 1 }

const ingredient = (id: string, parentId: string | null, conversions = {}): Ingredient => ({
  ...base,
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
  source: 'seed',
  conversions,
})
const product = (
  id: string,
  ingredientId: string | null,
  extra: Partial<Product> = {},
): Product => ({
  ...base,
  id,
  name: id,
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
  ...extra,
})
const item = (id: string, productId: string, a: Partial<StockItem>): StockItem => ({
  ...base,
  id,
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
const line = (l: Partial<RecipeLine>): RecipeLine => ({
  line_id: 'line',
  raw: '',
  name: 'x',
  optional: false,
  staple: false,
  ...l,
})

const ingredients = [
  ingredient('chicken', null),
  ingredient('chicken-thigh', 'chicken'),
  ingredient('onion', null, { ea: '150 g' }),
  ingredient('rice', null, { cup: '185 g' }),
  ingredient('oil', null),
]
const products = [
  product('p-thighs', 'chicken-thigh'),
  product('p-chicken', 'chicken'),
  product('p-onion', 'onion'),
  product('p-rice', 'rice'),
  product('p-oil', 'oil'),
  product('p-stew', 'chicken', { conversions: { portion: '400 g' } }),
  product('p-leftover', 'chicken', { recipeId: 'r1' }),
]
const cat: Catalog = {
  products: new Map(products.map((p) => [p.id, p])),
  ingredients: new Map(ingredients.map((i) => [i.id, i])),
  timesBought: new Map([['p-onion', 3]]),
}

describe('plan', () => {
  const stock = [
    item('late', 'p-thighs', { quantity: '500', unit: 'g', expiresOn: '2026-12-10' }),
    item('soon', 'p-thighs', { quantity: '300', unit: 'g', expiresOn: '2026-12-01' }),
    item('undated', 'p-thighs', { quantity: '300', unit: 'g' }),
    item('broad', 'p-chicken', { quantity: '1', unit: 'kg' }),
    item('leftover', 'p-leftover', { amountKind: 'portions', portions: 2 }),
  ]

  test('matching is first-expiring first, then broader ingredients, never leftovers', () => {
    expect(matchingItems('chicken-thigh', stock, cat).map((i) => i.id)).toEqual([
      'soon',
      'late',
      'undated',
    ])
    // "chicken" covers thighs too (more specific), but not the recipe's own leftovers.
    expect(matchingItems('chicken', stock, cat).map((i) => i.id)).toEqual([
      'soon',
      'late',
      'undated',
      'broad',
    ])
    const onlyBroad = stock.filter((i) => i.id === 'broad')
    expect(matchingItems('chicken-thigh', onlyBroad, cat).map((i) => i.id)).toEqual(['broad'])
    expect(matchingItems(undefined, stock, cat)).toEqual([])
  })

  test('amounts scale by portions made over servings', () => {
    const q = scaledQty({ qty: '800' }, 2, 4)
    expect(q && format(q)).toBe('400')
    expect(scaledQty({}, 2, 4)).toBeNull()
  })

  test('exact items get the converted amount, clamped to what is there', () => {
    const l = line({ qty: '2', unit: 'ea', ingredient_id: 'onion' })
    const onion = item('o', 'p-onion', { quantity: '1', unit: 'kg' })
    expect(suggest(onion, l, scaledQty(l, 4, 4), cat).take).toEqual({
      kind: 'exact',
      quantity: '0.3',
    })
    const few = item('o2', 'p-onion', { quantity: '200', unit: 'g' })
    const s = suggest(few, l, scaledQty(l, 4, 4), cat)
    expect(s.take).toEqual({ kind: 'exact', quantity: '200' })
    expect(usesAll(few, s.take)).toBe(true)
    expect(leftAfter(few, s.take)).toBeNull()
    const rice = line({ qty: '1.5', unit: 'cup', ingredient_id: 'rice' })
    const bag = item('r', 'p-rice', { quantity: '2', unit: 'kg' })
    const t = suggest(bag, rice, scaledQty(rice, 4, 4), cat).take
    expect(t).toEqual({ kind: 'exact', quantity: '0.278' })
    expect(leftAfter(bag, t)).toBe('1.722')
  })

  test('an unknown conversion asks for the amount', () => {
    const l = line({ qty: '2', unit: 'tbsp', ingredient_id: 'oil' })
    const s = suggest(
      item('x', 'p-oil', { quantity: '500', unit: 'g' }),
      l,
      scaledQty(l, 4, 4),
      cat,
    )
    expect(s).toEqual({ take: { kind: 'exact', quantity: '0' }, needsAmount: true })
  })

  test('portions: ceil through a portion conversion, else 1', () => {
    const l = line({ qty: '800', unit: 'g', ingredient_id: 'chicken' })
    const stew = item('s', 'p-stew', { amountKind: 'portions', portions: 5 })
    expect(suggest(stew, l, scaledQty(l, 4, 4), cat).take).toEqual({
      kind: 'portions',
      portions: 2,
    })
    const l2 = line({ qty: '900', unit: 'g', ingredient_id: 'chicken' })
    expect(suggest(stew, l2, scaledQty(l2, 4, 4), cat).take).toEqual({
      kind: 'portions',
      portions: 3,
    })
    const plain = item('p', 'p-chicken', { amountKind: 'portions', portions: 4 })
    expect(suggest(plain, l, scaledQty(l, 4, 4), cat).take).toEqual({
      kind: 'portions',
      portions: 1,
    })
  })

  test('levels step down one, then finished', () => {
    expect(nextLevel('most')).toBe('half')
    expect(nextLevel('almost_out')).toBe('finished')
    const oil = item('l', 'p-oil', { amountKind: 'level', level: 'full' })
    expect(suggest(oil, line({}), null, cat).take).toEqual({ kind: 'level', level: 'most' })
  })

  test('leftover names (shared with Go)', () => {
    for (const c of leftoverNames) expect(leftoverName(c.title)).toBe(c.name)
  })

  test('undo event ids match the server (uuidv5)', () => {
    // Same value as internal/cook TestUndoEventIDMatchesClient.
    expect(undoEventId('0190a1b2-0000-7000-8000-000000000e01')).toBe(
      'e997a083-105a-50c2-8c9a-264b67cad07a',
    )
  })
})

describe('reducers', () => {
  const db = new StockpotDB('cook-test')
  const location = (id: string, kind: Location['kind']): Location => ({
    ...base,
    id,
    name: kind,
    kind,
    sort: 1,
  })
  const leftovers: Category = {
    ...base,
    id: 'c-left',
    slug: 'leftovers',
    name: 'Leftovers',
    icon: null,
    sort: 1,
  }
  const recipe: Recipe = {
    ...base,
    id: 'r1',
    title: 'Chicken curry',
    subtitle: null,
    sourceKind: 'manual',
    sourceUrl: null,
    sourceLang: null,
    currentVersionId: 'v1',
    servings: 4,
    totalMinutes: null,
    coverBlob: null,
    tags: [],
    saved: true,
    ideaExpiresAt: null,
    cookedCount: 0,
    lastCookedAt: null,
    importId: null,
  }
  const version: RecipeVersion = {
    ...base,
    id: 'v1',
    recipeId: 'r1',
    n: 1,
    parentVersionId: null,
    origin: 'manual',
    changeSummary: null,
    title: 'Chicken curry',
    servings: 4,
    totalMinutes: null,
    ingredients: [],
    steps: [],
    notes: null,
    original: null,
    originalVersionId: null,
    createdBy: null,
  }

  beforeEach(async () => {
    await db.open()
    await db.meta.put({ key: 'sync.household', value: HH })
    await db.location.bulkPut([location('l-fridge', 'fridge'), location('l-freezer', 'freezer')])
    await db.category.put(leftovers)
    await db.recipe.put(recipe)
    await db.recipe_version.put(version)
    await db.product.bulkPut([
      product('p-thighs', 'chicken-thigh'),
      product('p-onion', 'onion'),
      product('p-oil', 'oil'),
    ])
    await db.shopping_list.put({ ...base, id: 'list', name: 'Groceries', sort: 0 })
    await db.stock_item.bulkPut([
      item('thighs', 'p-thighs', { quantity: '900', unit: 'g' }),
      item('onion', 'p-onion', { quantity: '2', unit: 'ea' }),
      item('oil', 'p-oil', { amountKind: 'level', level: 'most' }),
    ])
  })
  afterEach(() => db.delete())

  const cook = {
    id: 'cook-1',
    recipeId: 'r1',
    versionId: 'v1',
    cookedOn: '2026-11-28',
    portionsMade: 4,
    leftoverProductId: 'p-new-left',
    uses: [
      { stockItemId: 'thighs', eventId: '0190a1b2-0000-7000-8000-00000000e001', quantity: '800' },
      {
        stockItemId: 'onion',
        eventId: '0190a1b2-0000-7000-8000-00000000e002',
        finished: true,
        toShopping: { id: 's1', listId: 'list' },
      },
      {
        stockItemId: 'oil',
        eventId: '0190a1b2-0000-7000-8000-00000000e003',
        level: 'half' as const,
      },
      { stockItemId: 'gone', eventId: '0190a1b2-0000-7000-8000-00000000e004', quantity: '1' },
    ],
    store: [
      {
        stockItemId: 'left-fridge',
        eventId: '0190a1b2-0000-7000-8000-00000000e005',
        locationId: 'l-fridge',
        portions: 3,
      },
      {
        stockItemId: 'left-freezer',
        eventId: '0190a1b2-0000-7000-8000-00000000e006',
        locationId: 'l-freezer',
        portions: 1,
      },
    ],
    note: 'Good',
  }

  test('cook deducts, stores leftovers, lists used-up items and records the session', async () => {
    await dispatch('recipe.cook', cook, db)
    expect((await db.stock_item.get('thighs'))?.quantity).toBe('100')
    expect(await db.stock_item.get('onion')).toBeUndefined()
    expect((await db.stock_item.get('oil'))?.level).toBe('half')
    const left = await db.stock_item.get('left-fridge')
    expect(left).toMatchObject({
      amountKind: 'portions',
      portions: 3,
      source: 'cook',
      cookSessionId: 'cook-1',
    })
    expect((await db.stock_item.get('left-freezer'))?.frozenOn).toBe('2026-11-28')
    expect(await db.product.get('p-new-left')).toMatchObject({
      name: 'Leftover chicken curry',
      recipeId: 'r1',
      categoryId: 'c-left',
    })
    expect(await db.shopping_item.get('s1')).toMatchObject({ productId: 'p-onion', recipeId: 'r1' })
    const s = await db.cook_session.get('cook-1')
    const uses = s?.uses as CookUseRecord[]
    expect(uses.map((u) => !!u.skipped)).toEqual([false, false, false, true])
    expect(uses[0]).toMatchObject({ quantity: '800', amount_kind: 'exact' })
    expect((await db.recipe.get('r1'))?.cookedCount).toBe(1)
    expect((await db.stock_event.get('0190a1b2-0000-7000-8000-00000000e001'))?.cookSessionId).toBe(
      'cook-1',
    )
  })

  test('replays apply each delta once, and again after a pull overwrote the row', async () => {
    await dispatch('recipe.cook', cook, db)
    const { applyReducer } = await import('../../offline/reducers')
    await applyReducer(db, 'recipe.cook', cook, '2026-11-28T12:00:00Z')
    expect((await db.stock_item.get('thighs'))?.quantity).toBe('100')
    // A pull rewrote the item with server state that doesn't include the cook yet.
    await db.stock_item.put(item('thighs', 'p-thighs', { quantity: '850', unit: 'g' }))
    await applyReducer(db, 'recipe.cook', cook, '2026-11-28T12:00:00Z')
    expect((await db.stock_item.get('thighs'))?.quantity).toBe('50')
  })

  test('undo gives stock back and removes untouched leftovers and list items', async () => {
    await dispatch('recipe.cook', cook, db)
    await dispatch('recipe.cook.undo', { id: 'cook-1' }, db)
    expect((await db.stock_item.get('thighs'))?.quantity).toBe('900')
    expect((await db.stock_item.get('oil'))?.level).toBe('most')
    expect(await db.stock_item.get('left-fridge')).toBeUndefined()
    expect(await db.shopping_item.get('s1')).toBeUndefined()
    expect((await db.cook_session.get('cook-1'))?.undoneAt).not.toBeNull()
    expect((await db.recipe.get('r1'))?.cookedCount).toBe(0)
    // A second undo (replay) changes nothing.
    await dispatch('recipe.cook.undo', { id: 'cook-1' }, db)
    expect((await db.stock_item.get('thighs'))?.quantity).toBe('900')
  })

  test('level uses keep reason "cook"', async () => {
    await dispatch('recipe.cook', cook, db)
    expect(await db.stock_event.get('0190a1b2-0000-7000-8000-00000000e003')).toMatchObject({
      kind: 'adjust',
      reason: 'cook',
    })
  })

  test('offline undo brings back items the cook finished', async () => {
    const oilDone = {
      stockItemId: 'oil',
      eventId: '0190a1b2-0000-7000-8000-00000000e003',
      finished: true,
    }
    await dispatch('recipe.cook', { ...cook, uses: [...cook.uses.slice(0, 2), oilDone] }, db)
    expect(await db.stock_item.get('onion')).toBeUndefined()
    expect(await db.stock_item.get('oil')).toBeUndefined()
    await dispatch('recipe.cook.undo', { id: 'cook-1' }, db)
    expect((await db.stock_item.get('onion'))?.quantity).toBe('2')
    expect((await db.stock_item.get('oil'))?.level).toBe('most')
  })

  test('two lines can use one item; undo restores it', async () => {
    const twice = {
      ...cook,
      store: [],
      uses: [
        { stockItemId: 'thighs', eventId: '0190a1b2-0000-7000-8000-00000000e011', quantity: '300' },
        { stockItemId: 'thighs', eventId: '0190a1b2-0000-7000-8000-00000000e012', quantity: '700' },
      ],
    }
    await dispatch('recipe.cook', twice, db)
    expect(await db.stock_item.get('thighs')).toBeUndefined()
    const uses = (await db.cook_session.get('cook-1'))?.uses as CookUseRecord[]
    expect(uses.map((u) => [u.quantity, u.finished])).toEqual([
      ['300', false],
      ['600', true],
    ])
    const { applyReducer } = await import('../../offline/reducers')
    await applyReducer(db, 'recipe.cook', twice, '2026-11-28T12:00:00Z')
    expect(await db.stock_item.get('thighs')).toBeUndefined()
    await dispatch('recipe.cook.undo', { id: 'cook-1' }, db)
    expect((await db.stock_item.get('thighs'))?.quantity).toBe('900')
  })
})
