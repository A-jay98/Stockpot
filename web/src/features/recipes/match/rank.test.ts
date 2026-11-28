import { describe, expect, test } from 'vitest'
import type {
  Ingredient,
  Product,
  Recipe,
  RecipeLine,
  RecipeVersion,
  StockItem,
  Substitution,
} from '../../../offline/replica'
import fixture from './fixture.json'
import {
  cookTonight,
  indexKitchen,
  isCandidate,
  isLive,
  rankRecipes,
  scaleQty,
  urgency,
} from './rank'

const base = { householdId: 'hh', createdAt: '', updatedAt: '', rev: 1 }

const ingredients = fixture.ingredients.map(
  (i): Ingredient => ({
    ...base,
    householdId: null,
    name: i.slug,
    plural: null,
    categoryId: null,
    icon: null,
    aliases: [],
    shelfLife: {},
    defaultUnit: null,
    source: 'seed',
    ...i,
  }),
)

const products = fixture.products.map(
  (p): Product => ({
    ...base,
    brand: null,
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
    ...p,
  }),
)

const stockItem = (s: Partial<StockItem> & Pick<StockItem, 'id' | 'productId'>): StockItem => ({
  ...base,
  locationId: null,
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
  ...s,
})
const stock = fixture.stock.map((s) => stockItem(s as Parameters<typeof stockItem>[0]))

const substitutions = fixture.substitutions.map(
  (s): Substitution => ({
    ...base,
    ...(s as Omit<Substitution, keyof typeof base>),
    householdId: s.householdId,
  }),
)

const recipes: Recipe[] = []
const versions = new Map<string, RecipeVersion>()
for (const r of fixture.recipes) {
  const lines = r.lines.map(
    (l, i): RecipeLine => ({
      line_id: `${r.id}-${i}`,
      raw: l.name,
      optional: false,
      staple: false,
      ...l,
    }),
  )
  recipes.push({
    ...base,
    id: r.id,
    title: r.title,
    subtitle: null,
    sourceKind: (r.sourceKind ?? 'manual') as Recipe['sourceKind'],
    sourceUrl: null,
    sourceLang: null,
    currentVersionId: `${r.id}-v1`,
    servings: 4,
    totalMinutes: 30,
    coverBlob: null,
    tags: [],
    saved: r.saved,
    ideaExpiresAt: r.ideaExpiresAt ?? null,
    cookedCount: r.cookedCount ?? 0,
    lastCookedAt: r.lastCookedAt ?? null,
    importId: null,
  })
  versions.set(`${r.id}-v1`, {
    ...base,
    id: `${r.id}-v1`,
    recipeId: r.id,
    n: 1,
    parentVersionId: null,
    origin: 'manual',
    changeSummary: null,
    title: r.title,
    servings: 4,
    totalMinutes: 30,
    ingredients: lines,
    steps: [],
    notes: null,
    original: null,
    originalVersionId: null,
    createdBy: null,
  })
}

describe('fixture kitchen', () => {
  const k = indexKitchen({ today: fixture.today, ingredients, products, stock, substitutions })
  const ranked = rankRecipes(
    k,
    recipes.filter((r) => isCandidate(r, fixture.now)),
    versions,
  )

  test('ranks by coverage, urgency and cooks; ties put recently cooked last', () => {
    expect(ranked.map((r) => r.recipe.id)).toEqual(fixture.expect.order)
  })

  test.each(Object.entries(fixture.expect.recipes))('%s', (id, want) => {
    const r = ranked.find((x) => x.recipe.id === id)
    expect(r).toBeDefined()
    if (!r) return
    expect({ have: r.have, sub: r.sub, missing: r.missing, required: r.required }).toEqual({
      have: want.have,
      sub: want.sub,
      missing: want.missing,
      required: want.required,
    })
    expect(r.soon.map((s) => s.product.name)).toEqual(want.soon)
    expect(r.score).toBeCloseTo(want.score, 3)
    if ('firstSub' in want) expect(r.firstSub?.sub?.id).toBe(want.firstSub)
    if ('missingLines' in want)
      expect(r.lines.filter((l) => l.status === 'missing').map((l) => l.line.name)).toEqual(
        want.missingLines,
      )
  })

  test('cook tonight is the top recipe mostly in stock that uses something urgent', () => {
    expect(cookTonight(ranked)?.recipe.id).toBe(fixture.expect.cookTonight)
    expect(cookTonight(ranked.filter((r) => r.recipe.id !== 'skewers'))).toBeUndefined() // curry: 3 of 5
  })

  test('a swap whose uses are not all in stock is skipped for the next one', () => {
    const curry = ranked.find((r) => r.recipe.id === 'curry')
    expect(curry?.lines.find((l) => l.line.name === 'coconut milk')?.sub?.id).toBe('sub-coco')
  })
})

test.each([
  [null, 0],
  ['2026-11-20', 1],
  ['2026-11-27', 1],
  ['2026-11-28', 1],
  ['2026-11-29', 0.6],
  ['2026-11-30', 0.6],
  ['2026-12-01', 0.3],
  ['2026-12-04', 0.3],
  ['2026-12-05', 0],
])('urgency of %s is %s', (expiresOn, want) => {
  expect(urgency(expiresOn, '2026-11-27')).toBe(want)
})

test('live stock has something left', () => {
  const s = (x: Partial<StockItem>) => stockItem({ id: 'x', productId: 'p', ...x })
  expect(isLive(s({ quantity: '0' }))).toBe(false)
  expect(isLive(s({ quantity: '0.5' }))).toBe(true)
  expect(isLive(s({ quantity: null }))).toBe(true)
  expect(isLive(s({ amountKind: 'portions', portions: 0 }))).toBe(false)
  expect(isLive(s({ amountKind: 'level', level: 'almost_out' }))).toBe(true)
})

test('quantities scale to servings', () => {
  expect(scaleQty('2', 1.5)).toBe('3')
  expect(scaleQty('0.333', 2)).toBe('0.67')
  expect(scaleQty(undefined, 2)).toBeUndefined()
})
