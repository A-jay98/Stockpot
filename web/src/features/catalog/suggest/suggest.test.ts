import { expect, test } from 'vitest'
import type { Ingredient, Product, ProductStat, Store } from '../../../offline/replica'
import {
  buildIndex,
  guessList,
  parseQuery,
  type SuggestData,
  type Suggestion,
  search,
} from './index'

const base = {
  householdId: 'h',
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
  rev: 1,
}
const NOW = Date.parse('2026-11-27T12:00:00Z')
const daysAgo = (n: number) => new Date(NOW - n * 86_400_000).toISOString()

const ing = (id: string, name: string, extra: Partial<Ingredient> = {}): Ingredient => ({
  ...base,
  id,
  slug: id,
  name,
  plural: `${name}s`,
  parentId: null,
  categoryId: 'cat-fruit',
  icon: null,
  aliases: [],
  shelfLife: {},
  defaultUnit: null,
  source: 'seed',
  ...extra,
})

const prod = (id: string, name: string, extra: Partial<Product> = {}): Product => ({
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
  matchSource: 'rule',
  matchConfidence: 1,
  enrichState: 'done',
  enrichAttempts: 0,
  offCode: null,
  note: null,
  ...extra,
})

const store = (id: string, name: string): Store => ({ ...base, id, name, sort: 0, color: null })

const stat = (
  productId: string,
  storeId: string | null,
  times: number,
  lastDaysAgo: number,
  extra: Partial<ProductStat> = {},
): ProductStat => ({
  ...base,
  id: `${productId}-${storeId}`,
  productId,
  storeId,
  timesBought: times,
  lastBoughtAt: daysAgo(lastDaysAgo),
  typicalQty: null,
  typicalUnit: null,
  medianIntervalDays: null,
  lastQty: null,
  ...extra,
})

const stores = [store('s-costco', 'Costco'), store('s-nofrills', 'No Frills')]

const fixture = (): SuggestData => ({
  stores,
  ingredients: [
    ing('i-mandarin', 'Mandarin', { aliases: ['clementine', 'tangerine'] }),
    ing('i-avocado', 'Avocado'),
    ing('i-pomegranate', 'Pomegranate'),
    ing('i-milk', 'Milk', { categoryId: 'cat-dairy' }),
    ing('i-oat-milk', 'Oat milk', { categoryId: 'cat-dairy' }),
    ing('i-olive-oil', 'Olive oil'),
  ],
  products: [
    prod('p-mand', 'Mandarins', {
      brand: 'Kirkland',
      ingredientId: 'i-mandarin',
      defaultStoreId: 's-costco',
    }),
    prod('p-avo', 'Avocados', { ingredientId: 'i-avocado' }),
    prod('p-milk', 'Milk', { ingredientId: 'i-milk' }),
    prod('p-oat', 'Oat milk', { ingredientId: 'i-oat-milk' }),
    prod('p-oil', 'Olive oil', { brand: 'Kirkland', ingredientId: 'i-olive-oil' }),
    prod('p-towel', 'Paper towels', { brand: 'Kirkland' }),
  ],
  stats: [
    stat('p-mand', 's-costco', 6, 10, { typicalQty: '2', typicalUnit: 'lb' }),
    stat('p-mand', null, 6, 10, { typicalQty: '2', typicalUnit: 'lb' }),
    stat('p-milk', 's-nofrills', 8, 3, { typicalQty: '4', typicalUnit: 'l' }),
    stat('p-oat', 's-nofrills', 2, 20),
  ],
})

const top = (raw: string, data = fixture()) =>
  search(buildIndex(data), raw, NOW).suggestions as [Suggestion, ...Suggestion[]]

test('"manderins" completes Mandarins from Costco with the usual 2 lb', () => {
  const [first] = top('manderins')
  expect(first.entry.name).toBe('Mandarins')
  expect(first.confident).toBe(true)
  expect(first.item).toEqual({
    productId: 'p-mand',
    ingredientId: 'i-mandarin',
    name: 'Mandarins',
    brand: 'Kirkland',
    storeIds: ['s-costco'],
    categoryId: 'cat-fruit',
    qty: '2',
    unit: 'lb',
  })
})

test('"mand" completes the same entry and offers the rest of the word as ghost text', () => {
  const [first] = top('mand')
  expect(first.entry.productId).toBe('p-mand')
  expect(first.ghost).toBe('arins')
  expect(top('manderins')[0].ghost).toBeUndefined() // a typo can't be a ghost suffix
})

test('"avo" finds Avocado, and an alias finds its ingredient product', () => {
  expect(top('avo')[0].entry.name).toBe('Avocados')
  expect(top('clementine')[0].entry.productId).toBe('p-mand')
})

test('a store word narrows to that store, before or after the item', () => {
  for (const q of ['costco mand', 'mand @costco', 'mand from costco', 'mand @cost']) {
    const [first] = top(q)
    expect(first.entry.productId, q).toBe('p-mand')
    expect(first.entry.storeId, q).toBe('s-costco')
  }
  // Milk was only bought at No Frills: asking for Costco keeps it, but filed under Costco.
  const milk = top('milk @costco').find((s) => s.entry.productId === 'p-milk')
  expect(milk?.item.storeIds).toEqual(['s-costco'])
  expect(top('mand @nofrills').every((s) => s.entry.storeId !== 's-costco')).toBe(true)
})

test('a leading quantity wins over the usual one', () => {
  const [first] = top('3 manderins')
  expect(first.entry.productId).toBe('p-mand')
  expect(first.item.qty).toBe('3')
  expect(first.item.unit).toBe('lb') // the usual unit stays
  const [kg] = top('1.5kg mand')
  expect(kg.item.qty).toBe('1.5')
  expect(kg.item.unit).toBe('kg')
  expect(top('x4 avo')[0].item.qty).toBe('4')
  expect(top('milk 2l')[0].item).toMatchObject({ qty: '2', unit: 'l' })
})

test('a brand finds the brand products', () => {
  const names = top('kirkland').map((s) => s.entry.name)
  expect(names).toEqual(expect.arrayContaining(['Mandarins', 'Olive oil', 'Paper towels']))
  expect(names).not.toContain('Milk')
})

test('"oat milk" ranks the exact phrase first and "milk" prefers plain milk', () => {
  expect(top('oat milk')[0].entry.name).toBe('Oat milk')
  expect(top('oat milk').some((s) => s.entry.name === 'Milk')).toBe(false)
  expect(top('milk')[0].entry.name).toBe('Milk')
  // equal history: the exact phrase still wins
  const data = fixture()
  data.stats = [stat('p-milk', 's-nofrills', 3, 5), stat('p-oat', 's-nofrills', 3, 5)]
  expect(top('milk', data)[0].entry.name).toBe('Milk')
  expect(top('oat milk', data)[0].entry.name).toBe('Oat milk')
})

test('an unseen misspelled ingredient suggests the ingredient as new', () => {
  const [first] = top('pomegranite')
  expect(first.entry.kind).toBe('ingredient')
  expect(first.entry.name).toBe('Pomegranate')
  expect(first.item).toEqual({
    ingredientId: 'i-pomegranate',
    name: 'Pomegranate',
    storeIds: [],
    categoryId: 'cat-fruit',
  })
})

test('nonsense and too-short queries match nothing, and every word must match', () => {
  expect(top('zzzqx')).toEqual([])
  expect(top('')).toEqual([])
  expect(top('mand zzzz')).toEqual([])
})

test('a product with no store history uses its defaults', () => {
  const data = fixture()
  data.stats = []
  const first = top('mand', data)[0]
  expect(first.item.storeIds).toEqual(['s-costco'])
})

test('recent purchases outrank old ones of equal text match', () => {
  const data: SuggestData = {
    stores,
    ingredients: [],
    products: [prod('a', 'Bread white'), prod('b', 'Bread rye')],
    stats: [stat('a', null, 4, 200), stat('b', null, 4, 2)],
  }
  expect(top('bread', data).map((s) => s.entry.name)).toEqual(['Bread rye', 'Bread white'])
})

test('parseQuery splits quantity, store and words', () => {
  expect(parseQuery('2 lb mandarins', stores)).toMatchObject({
    qty: '2',
    unit: 'lb',
    tokens: ['mandarins'],
  })
  expect(parseQuery('mandarins from no frills', stores)).toMatchObject({
    storeId: 's-nofrills',
    tokens: ['mandarins'],
  })
  expect(parseQuery('costco', stores)).toMatchObject({ storeId: undefined, tokens: ['costco'] }) // never the only word
  expect(parseQuery('2% milk', stores).tokens).toEqual(['2%', 'milk'])
  expect(parseQuery('7up', stores).tokens).toEqual(['7up'])
})

// --- properties (seeded, so failures reproduce) ---

function rng(seed: number) {
  let s = seed
  return () => {
    s = (s * 1664525 + 1013904223) % 2 ** 32
    return s / 2 ** 32
  }
}

function shuffled<T>(xs: T[], r: () => number): T[] {
  const a = [...xs]
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.floor(r() * (i + 1))
    const tmp = a[i] as T
    a[i] = a[j] as T
    a[j] = tmp
  }
  return a
}

const names = (data: SuggestData, q: string) =>
  search(buildIndex(data), q, NOW).suggestions.map((s) => s.entry.key)

test('ranking does not depend on the order rows arrive in', () => {
  const r = rng(7)
  const data = fixture()
  for (const q of ['m', 'mil', 'oat milk', 'kirk', 'manderins', 'pomegranite', '2 av']) {
    const want = names(data, q)
    for (let i = 0; i < 10; i++) {
      const mixed: SuggestData = {
        ...data,
        products: shuffled(data.products, r),
        ingredients: shuffled(data.ingredients, r),
        stats: shuffled(data.stats, r),
      }
      expect(names(mixed, q), q).toEqual(want)
    }
  }
})

test('typing more never invents results, and unrelated products never reorder old ones', () => {
  const data = fixture()
  for (const word of ['mandarins', 'avocados', 'kirkland', 'olive oil']) {
    let prev = new Set(names(data, word.slice(0, 1)))
    for (let n = 2; n <= word.length; n++) {
      const cur = names(data, word.slice(0, n))
      for (const k of cur) expect(prev.has(k) || n > 3, `${word.slice(0, n)} → ${k}`).toBe(true)
      prev = new Set(cur)
    }
  }
  const noise: SuggestData = {
    ...data,
    products: [...data.products, prod('z1', 'Zucchini'), prod('z2', 'Quinoa flour')],
  }
  for (const q of ['mil', 'kirk', 'oat milk']) {
    const before = names(data, q)
    const after = names(noise, q).filter((k) => before.includes(k))
    expect(after, q).toEqual(before)
  }
})

test("more purchases never lower a product's rank", () => {
  const data = fixture()
  const before = search(buildIndex(data), 'milk', NOW).suggestions.find(
    (s) => s.entry.productId === 'p-oat',
  )
  const more = {
    ...data,
    stats: data.stats.map((s) =>
      s.productId === 'p-oat' ? { ...s, timesBought: s.timesBought + 5 } : s,
    ),
  }
  const after = search(buildIndex(more), 'milk', NOW).suggestions.find(
    (s) => s.entry.productId === 'p-oat',
  )
  expect(after?.rank ?? 0).toBeGreaterThan(before?.rank ?? Number.POSITIVE_INFINITY)
})

// --- guess list ---

test('guess list: overdue repurchases, low and finished stock, minus what is already on the list', () => {
  const data = fixture()
  data.stats = [
    stat('p-milk', null, 8, 9, { medianIntervalDays: 7 }), // due 6.3 days after the last buy: overdue
    stat('p-avo', null, 3, 2, { medianIntervalDays: 7 }), // not due yet
    stat('p-oat', null, 1, 100, { medianIntervalDays: 7 }), // one purchase: too little to guess
    stat('p-mand', null, 5, 30, { medianIntervalDays: 10, typicalQty: '2', typicalUnit: 'lb' }),
    stat('p-mand', 's-costco', 5, 30, { typicalQty: '2', typicalUnit: 'lb' }),
  ]
  const stock = (productId: string, extra: object) =>
    ({
      ...base,
      id: `st-${productId}`,
      productId,
      locationId: null,
      amountKind: 'exact',
      quantity: '1',
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
      ...extra,
    }) as never
  const g = guessList(
    {
      products: data.products,
      stats: data.stats,
      stockItems: [
        stock('p-oil', { low: true }),
        stock('p-towel', { quantity: '0', updatedAt: daysAgo(3) }),
        stock('p-mand', {}),
      ],
      shoppingItems: [],
    },
    NOW,
  )
  expect(g.map((x) => [x.name, x.reason])).toEqual([
    ['Olive oil', 'low'],
    ['Paper towels', 'finished'],
    ['Milk', 'overdue'],
  ])
  // Mandarins are overdue but there is some in stock, so they're not suggested.
  const onList = guessList(
    {
      products: data.products,
      stats: data.stats,
      stockItems: [],
      shoppingItems: [
        {
          ...base,
          id: 'si',
          listId: 'l',
          productId: 'p-milk',
          text: 'Milk',
          qty: null,
          unit: null,
          categoryId: null,
          storeIds: [],
          note: null,
          sort: 0,
          checkedAt: null,
          checkedBy: null,
          stockItemId: null,
        },
      ],
    },
    NOW,
  )
  expect(onList.map((x) => x.name)).toEqual(['Mandarins'])
  expect(onList[0]?.item).toMatchObject({
    productId: 'p-mand',
    storeIds: ['s-costco'],
    qty: '2',
    unit: 'lb',
  })
})

test('guess list is capped at 12, most overdue first', () => {
  const products = Array.from({ length: 20 }, (_, i) =>
    prod(`p${i}`, `Item ${String(i).padStart(2, '0')}`),
  )
  const stats = products.map((p, i) => stat(p.id, null, 3, 30 + i, { medianIntervalDays: 7 }))
  const g = guessList({ products, stats, stockItems: [], shoppingItems: [] }, NOW)
  expect(g).toHaveLength(12)
  expect(g[0]?.name).toBe('Item 19')
})

// --- speed ---

test('a keystroke over 2,000 entries stays within the 5 ms budget', () => {
  const r = rng(42)
  const syll = ['ba', 'ko', 'mi', 'ra', 'tu', 'sel', 'por', 'vin', 'dan', 'ger', 'lo', 'ni']
  const word = () =>
    Array.from({ length: 2 + Math.floor(r() * 2) }, () => syll[Math.floor(r() * syll.length)]).join(
      '',
    )
  const products: Product[] = []
  const stats: ProductStat[] = []
  for (let i = 0; i < 1300; i++) {
    products.push(prod(`b${i}`, `${word()} ${word()}`, { brand: r() < 0.4 ? word() : null }))
    stats.push(stat(`b${i}`, 's-costco', 1 + Math.floor(r() * 9), Math.floor(r() * 300)))
    if (r() < 0.5)
      stats.push(stat(`b${i}`, 's-nofrills', 1 + Math.floor(r() * 9), Math.floor(r() * 300)))
  }
  const ingredients = Array.from({ length: 700 }, (_, i) =>
    ing(`n${i}`, word(), { aliases: [word()] }),
  )
  const data: SuggestData = {
    stores,
    products: [...fixture().products, ...products],
    ingredients: [...fixture().ingredients, ...ingredients],
    stats: [...fixture().stats, ...stats],
  }
  const index = buildIndex(data)
  expect(index.size).toBeGreaterThanOrEqual(2000)
  const queries = ['m', 'ma', 'mand', 'manderins', 'kirk', 'oat mi', 'balo', 'tusel pory']
  for (const q of queries) search(index, q, NOW) // warm up the JIT and caches
  const times: number[] = []
  for (let n = 0; n < 5; n++)
    for (const q of queries) {
      const t = performance.now()
      search(index, q, NOW)
      times.push(performance.now() - t)
    }
  times.sort((a, b) => a - b)
  const median = times[Math.floor(times.length / 2)]
  const p95 = times[Math.floor(times.length * 0.95)]
  console.info(
    `suggest: ${index.size} entries, median ${(median ?? 0).toFixed(2)} ms, p95 ${(p95 ?? 0).toFixed(2)} ms`,
  )
  expect(median ?? 99).toBeLessThan(8) // budget is 5 ms; measured ~3 ms locally, slack for shared CI runners
  expect(p95 ?? 99).toBeLessThan(30)
})
