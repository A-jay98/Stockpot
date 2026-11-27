import type { Ingredient, Product, ProductStat, Store } from '../../../offline/replica'
import type { PickedItem } from '../ProductPicker'
import { type ParsedQuery, parseQuery } from './parse'
import { normalize, SCORE_TYPO, tokenScore, words } from './text'

/** What the engine indexes: one slice of the local replica. */
export type SuggestData = {
  products: Product[]
  ingredients: Ingredient[]
  stats: ProductStat[]
  stores: Store[]
}

/** One thing the user can pick: a product (per store it was bought at) or a bare ingredient. */
export type Entry = {
  key: string
  kind: 'product' | 'ingredient'
  productId?: string
  ingredientId?: string
  storeId: string | null
  name: string
  brand?: string
  storeName?: string
  qty?: string
  unit?: string
  categoryId?: string
  /** Registry key from the product, else the ingredient (callers resolve the rest of the chain). */
  icon?: string
  timesBought: number
  /** Epoch ms. */
  lastBoughtAt: number | null
  defaultStoreId?: string
  /** Normalised name, for the exact-phrase bonus and coverage. */
  nameNorm: string
}

type Base = { words: string[]; entries: Entry[] }

export type Index = {
  bases: Base[]
  stores: { id: string; name: string }[]
  size: number
}

const DAY = 86_400_000

/** Build the index once per replica change; searching is then allocation-light. */
export function buildIndex({ products, ingredients, stats, stores }: SuggestData): Index {
  const storeName = new Map(stores.map((s) => [s.id, s.name]))
  const ingredientById = new Map(ingredients.map((i) => [i.id, i]))
  const statsByProduct = new Map<string, ProductStat[]>()
  for (const s of stats) {
    if (s.timesBought <= 0) continue
    const list = statsByProduct.get(s.productId)
    if (list) list.push(s)
    else statsByProduct.set(s.productId, [s])
  }

  const bases: Base[] = []
  const usedIngredients = new Set<string>()
  let size = 0

  for (const p of products) {
    const ing = p.ingredientId ? ingredientById.get(p.ingredientId) : undefined
    if (p.ingredientId) usedIngredients.add(p.ingredientId)
    const fields = [
      p.name,
      p.brand ?? '',
      ing?.name ?? '',
      ing?.plural ?? '',
      ...(ing?.aliases ?? []),
    ]
    const ws = [...new Set(fields.flatMap((f) => words(normalize(f))))]
    const rows = statsByProduct.get(p.id) ?? []
    const perStore = rows.filter((r) => r.storeId)
    const all = rows.find((r) => !r.storeId)
    const mk = (st: ProductStat | undefined, storeId: string | null): Entry => ({
      key: `p:${p.id}:${storeId ?? ''}`,
      kind: 'product',
      productId: p.id,
      ingredientId: p.ingredientId ?? undefined,
      storeId,
      name: p.name,
      brand: p.brand ?? undefined,
      storeName: storeId ? storeName.get(storeId) : undefined,
      qty: st?.typicalQty ?? p.defaultQty ?? undefined,
      unit: st?.typicalUnit ?? p.defaultUnit ?? undefined,
      categoryId: p.categoryId ?? ing?.categoryId ?? undefined,
      icon: p.icon ?? ing?.icon ?? undefined,
      timesBought: st?.timesBought ?? 0,
      lastBoughtAt: st?.lastBoughtAt ? Date.parse(st.lastBoughtAt) : null,
      defaultStoreId: p.defaultStoreId ?? undefined,
      nameNorm: normalize(p.name),
    })
    // One entry per store it was bought at; the store-less one only when there is no store history.
    const entries = perStore.length ? perStore.map((r) => mk(r, r.storeId)) : [mk(all, null)]
    size += entries.length
    bases.push({ words: ws, entries })
  }

  for (const i of ingredients) {
    if (usedIngredients.has(i.id)) continue
    const ws = [
      ...new Set([i.name, i.plural ?? '', ...i.aliases].flatMap((f) => words(normalize(f)))),
    ]
    bases.push({
      words: ws,
      entries: [
        {
          key: `i:${i.id}`,
          kind: 'ingredient',
          ingredientId: i.id,
          storeId: null,
          name: i.name,
          categoryId: i.categoryId ?? undefined,
          icon: i.icon ?? undefined,
          timesBought: 0,
          lastBoughtAt: null,
          nameNorm: normalize(i.name),
        },
      ],
    })
    size++
  }
  return { bases, stores: stores.map((s) => ({ id: s.id, name: s.name })), size }
}

export type Suggestion = {
  entry: Entry
  /** 0..1.2 text match quality; >= 0.85 means every word matched by prefix or within typo budget. */
  text: number
  rank: number
  /** Safe to accept on Enter. */
  confident: boolean
  /** What to show greyed after the caret when the typed text is a prefix of the name. */
  ghost?: string
  item: PickedItem
}

export type SearchResult = { parsed: ParsedQuery; suggestions: Suggestion[] }

export const CONFIDENT = SCORE_TYPO
const RECENCY_HALF_LIFE_DAYS = 60
const MAX_RESULTS = 6

/** Frecency: how often and how recently it was bought. 1 for never, up to ~2.7 for a staple. */
export function frecency(timesBought: number, lastBoughtAt: number | null, now: number): number {
  if (timesBought <= 0) return 1
  const days = lastBoughtAt ? Math.max(0, (now - lastBoughtAt) / DAY) : RECENCY_HALF_LIFE_DAYS
  const recency = 0.6 + 0.4 * 0.5 ** (days / RECENCY_HALF_LIFE_DAYS)
  return (1 + Math.log(1 + timesBought)) * recency
}

/** The item a pick fills in: product, store(s), quantity (typed quantity wins), category. */
export function toPicked(
  e: Entry,
  parsed?: Pick<ParsedQuery, 'qty' | 'unit' | 'storeId'>,
): PickedItem {
  const storeId = e.storeId ?? parsed?.storeId ?? e.defaultStoreId
  const item: PickedItem = {
    name: e.name,
    storeIds: storeId ? [storeId] : [],
  }
  if (e.productId) item.productId = e.productId
  if (e.ingredientId) item.ingredientId = e.ingredientId
  if (e.brand) item.brand = e.brand
  if (e.categoryId) item.categoryId = e.categoryId
  const qty = parsed?.qty ?? e.qty
  if (qty) item.qty = qty
  const unit = parsed?.unit ?? e.unit
  if (unit) item.unit = unit
  return item
}

/** Score every base against the query words: all must match, the score is their mean. */
function textScore(tokens: string[], ws: string[], memo: Map<string, number>): number {
  let sum = 0
  for (const t of tokens) {
    let best = 0
    for (const w of ws) {
      const k = `${t}\u0000${w}`
      let s = memo.get(k)
      if (s === undefined) {
        s = tokenScore(t, w)
        memo.set(k, s)
      }
      if (s > best) {
        best = s
        if (best === 1) break
      }
    }
    if (best === 0) return 0
    sum += best
  }
  return sum / tokens.length
}

/**
 * A typed store narrows a product to its entry for that store. A product never bought there is
 * still offered, filed under the typed store with its most frecent entry's usual quantity.
 */
function entriesFor(base: Base, storeId: string | undefined, now: number): Entry[] {
  if (!storeId) return base.entries
  const here = base.entries.filter((e) => e.storeId === storeId)
  if (here.length) return here
  const best = [...base.entries].sort(
    (a, b) =>
      frecency(b.timesBought, b.lastBoughtAt, now) - frecency(a.timesBought, a.lastBoughtAt, now),
  )[0]
  if (!best) return []
  return [
    {
      ...best,
      key: `${best.key}@${storeId}`,
      storeId,
      storeName: undefined,
      timesBought: 0,
      lastBoughtAt: null,
    },
  ]
}

/** Rank the index for a typed query. Pure: pass `now` for recency. */
export function search(index: Index, raw: string, now = Date.now()): SearchResult {
  const parsed = parseQuery(raw, index.stores)
  const { tokens } = parsed
  if (tokens.length === 0) return { parsed, suggestions: [] }
  const memo = new Map<string, number>()
  const queryNorm = tokens.join(' ')
  const out: Suggestion[] = []
  for (const base of index.bases) {
    const text = textScore(tokens, base.words, memo)
    if (text === 0) continue
    for (const e of entriesFor(base, parsed.storeId, now)) {
      let t = text
      const coverage = Math.min(1, queryNorm.length / Math.max(1, e.nameNorm.length))
      t *= 0.85 + 0.15 * coverage
      if (e.nameNorm === queryNorm) t *= 1.2
      let rank = t * frecency(e.timesBought, e.lastBoughtAt, now) * (e.kind === 'product' ? 1 : 0.6)
      if (parsed.storeId && e.storeId === parsed.storeId) rank *= 1.5
      out.push({
        entry: e,
        text: t,
        rank,
        confident: text >= CONFIDENT,
        item: toPicked(e, parsed),
      })
    }
  }
  out.sort((a, b) => b.rank - a.rank || a.entry.name.length - b.entry.name.length)
  const top = out.slice(0, MAX_RESULTS)
  const first = top[0]
  if (first?.confident) first.ghost = ghostFor(first.entry.name, parsed, raw)
  return { parsed, suggestions: top }
}

/** The part of `name` still to type, when what was typed (after any quantity) is its prefix. */
function ghostFor(name: string, parsed: ParsedQuery, raw: string): string | undefined {
  // Only when what's typed ends with the words being completed: no store, no trailing quantity,
  // no trailing space (that ends the word).
  if (parsed.storeId || !parsed.rest || !raw.endsWith(parsed.rest)) return undefined
  const n = normalize(name)
  const t = normalize(parsed.rest)
  if (t.length >= n.length || !n.startsWith(t)) return undefined
  if (name.length !== n.length) return undefined // normalisation changed the length: don't misalign
  return name.slice(parsed.rest.length)
}
