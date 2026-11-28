import type { Product, ProductStat, ShoppingItem, StockItem } from '../../../offline/replica'
import type { PickedItem } from '../ProductPicker'
import { usualAmount } from './engine'

export type GuessReason = 'overdue' | 'low' | 'finished' | 'expired'

export type Guess = {
  productId: string
  name: string
  reason: GuessReason
  /** Days past the expected repurchase, for ordering; "needed now" reasons sort first. */
  urgency: number
  item: PickedItem
  icon?: string
}

export type GuessData = {
  products: Product[]
  stats: ProductStat[]
  stockItems: StockItem[]
  shoppingItems: ShoppingItem[]
}

const DAY = 86_400_000
export const GUESS_MAX = 12
const OVERDUE_FACTOR = 0.9
const FINISHED_WINDOW_DAYS = 14
const NEEDED_NOW = 10_000

/**
 * "Suggested" chips shown before typing: things the household probably needs.
 * - overdue repurchases: last bought + 0.9 x the median interval has passed (>= 2 purchases),
 *   unless some of it is in stock and not marked low;
 * - stock marked low, or used up to zero in the last 14 days;
 * - staples (bought at least twice) whose stock has expired.
 * Anything already unchecked on a list is left out. Most urgent first, at most 12.
 */
export function guessList(d: GuessData, now = Date.now()): Guess[] {
  const onList = new Set<string>()
  const onListText = new Set<string>()
  for (const i of d.shoppingItems) {
    if (i.checkedAt) continue
    if (i.productId) onList.add(i.productId)
    else if (i.text) onListText.add(i.text.trim().toLowerCase())
  }
  const byProduct = new Map<string, ProductStat[]>()
  for (const s of d.stats) {
    if (s.timesBought <= 0) continue
    byProduct.set(s.productId, [...(byProduct.get(s.productId) ?? []), s])
  }
  const stockByProduct = new Map<string, StockItem[]>()
  for (const s of d.stockItems)
    stockByProduct.set(s.productId, [...(stockByProduct.get(s.productId) ?? []), s])
  const today = new Date(now).toISOString().slice(0, 10)

  const guesses = new Map<string, Guess>()
  const add = (p: Product, reason: GuessReason, urgency: number) => {
    if (onList.has(p.id) || onListText.has(p.name.trim().toLowerCase())) return
    const prev = guesses.get(p.id)
    if (prev && prev.urgency >= urgency) return
    guesses.set(p.id, {
      productId: p.id,
      name: p.name,
      reason,
      urgency,
      item: usualPick(p, byProduct.get(p.id)),
      icon: p.icon ?? undefined,
    })
  }

  for (const p of d.products) {
    const stats = byProduct.get(p.id) ?? []
    const all = stats.find((s) => !s.storeId)
    const stock = stockByProduct.get(p.id) ?? []
    const have = stock.some((s) => !s.low && !isEmpty(s))
    if (stock.some((s) => s.low)) add(p, 'low', NEEDED_NOW)
    if (
      stock.some((s) => isEmpty(s) && now - Date.parse(s.updatedAt) <= FINISHED_WINDOW_DAYS * DAY)
    )
      add(p, 'finished', NEEDED_NOW)
    if (
      all &&
      all.timesBought >= 2 &&
      stock.some((s) => s.expiresOn && s.expiresOn < today && !isEmpty(s))
    )
      add(p, 'expired', NEEDED_NOW - 1)
    if (all && all.timesBought >= 2 && all.lastBoughtAt && all.medianIntervalDays && !have) {
      const due = Date.parse(all.lastBoughtAt) + all.medianIntervalDays * OVERDUE_FACTOR * DAY
      if (due <= now) add(p, 'overdue', (now - due) / DAY)
    }
  }
  return [...guesses.values()]
    .sort((a, b) => b.urgency - a.urgency || a.name.localeCompare(b.name))
    .slice(0, GUESS_MAX)
}

const isEmpty = (s: StockItem) =>
  s.amountKind === 'exact' && s.quantity !== null && Number(s.quantity) <= 0

/** Usual store and amount: the most recently bought store row (else the default store), and see `usualAmount`. */
export function usualPick(p: Product, stats: ProductStat[] | undefined): PickedItem {
  const rows = (stats ?? []).filter((s) => s.storeId)
  rows.sort((a, b) => Date.parse(b.lastBoughtAt ?? '0') - Date.parse(a.lastBoughtAt ?? '0'))
  const best = rows[0] ?? (stats ?? []).find((s) => !s.storeId)
  const storeId = best?.storeId ?? p.defaultStoreId
  const item: PickedItem = { productId: p.id, name: p.name, storeIds: storeId ? [storeId] : [] }
  if (p.ingredientId) item.ingredientId = p.ingredientId
  if (p.brand) item.brand = p.brand
  if (p.categoryId) item.categoryId = p.categoryId
  return { ...item, ...usualAmount(p, best) }
}
