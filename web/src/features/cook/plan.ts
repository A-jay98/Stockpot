import { convert, format, mul, type Rat, rat } from '../../lib/units'
import type {
  Ingredient,
  Product,
  RecipeLine,
  StockItem,
  StockLevel,
  Substitution,
} from '../../offline/replica'
import { cmp, isZero, sub } from '../inventory/decimal'

/**
 * The cook sheet's plan (architecture-m2 §9), pure so it is table-tested: which stock item each
 * recipe line uses (first-expiring first), and a suggested amount in that item's own amount kind.
 */

export type Catalog = {
  products: Map<string, Product>
  ingredients: Map<string, Ingredient>
  /** Purchases per product (product_stat.timesBought summed over stores). */
  timesBought: Map<string, number>
}

/** What the cook takes from one item, in the item's amount kind. */
export type Take =
  | { kind: 'exact'; quantity: string }
  | { kind: 'portions'; portions: number }
  | { kind: 'level'; level: StockLevel | 'finished' }

export type Suggestion = {
  take: Take
  /** The recipe amount couldn't be converted to the item's unit: the user sets it (chip). */
  needsAmount: boolean
}

const LEVELS: StockLevel[] = ['full', 'most', 'half', 'low', 'almost_out']

/** The next rough level down ("most" → "half"); below "almost out" the item is finished. */
export function nextLevel(l: StockLevel | null): StockLevel | 'finished' {
  const i = l ? LEVELS.indexOf(l) : -1
  return i < 0 || i >= LEVELS.length - 1 ? 'finished' : (LEVELS[i + 1] as StockLevel)
}

/** The ingredient and its ancestors, nearest first. */
export function ancestry(
  id: string | null | undefined,
  ingredients: Map<string, Ingredient>,
): Ingredient[] {
  const out: Ingredient[] = []
  for (let cur = id ? ingredients.get(id) : undefined, n = 0; cur && n < 12; n++) {
    out.push(cur)
    cur = cur.parentId ? ingredients.get(cur.parentId) : undefined
  }
  return out
}

const productIngredients = (p: Product) =>
  [p.ingredientId, ...p.extraIngredientIds].filter(Boolean) as string[]

/**
 * Live stock items for an ingredient, first-expiring first (undated last): products of the
 * ingredient or a more specific one ("chicken thighs" for "chicken"); if none, products of a broader
 * one ("chicken" for "chicken thighs").
 */
export function matchingItems(
  ingredientId: string | undefined,
  stock: StockItem[],
  cat: Catalog,
): StockItem[] {
  if (!ingredientId) return []
  const narrower = (p: Product) =>
    productIngredients(p).some((i) =>
      ancestry(i, cat.ingredients).some((a) => a.id === ingredientId),
    )
  const broader = new Set(
    ancestry(ingredientId, cat.ingredients)
      .slice(1)
      .map((a) => a.id),
  )
  const pick = (ok: (p: Product) => boolean) =>
    stock.filter((it) => {
      const p = cat.products.get(it.productId)
      return p && p.recipeId == null && ok(p)
    })
  let items = pick(narrower)
  if (items.length === 0) items = pick((p) => productIngredients(p).some((i) => broader.has(i)))
  return items.sort(byExpiry)
}

export const byExpiry = (a: StockItem, b: StockItem) =>
  (a.expiresOn ?? '9999').localeCompare(b.expiresOn ?? '9999') ||
  a.createdAt.localeCompare(b.createdAt)

/** The line's quantity scaled to the portions made (null when it has none). */
export function scaledQty(
  line: Pick<RecipeLine, 'qty'>,
  made: number,
  servings: number | null,
): Rat | null {
  const q = line.qty ? rat(line.qty) : null
  if (!q) return null
  const base = servings && servings > 0 ? servings : made
  return mul(q, { n: BigInt(made), d: BigInt(base) })
}

/** Conversion sets for an item: product, then its ingredient and ancestors, then the line's. */
function conversionSets(it: StockItem, line: RecipeLine, cat: Catalog) {
  const p = cat.products.get(it.productId)
  return [
    p?.conversions,
    ...ancestry(p?.ingredientId, cat.ingredients).map((i) => i.conversions),
    ...ancestry(line.ingredient_id, cat.ingredients).map((i) => i.conversions),
  ]
}

/** A suggested take from `it` for `line` at the scaled amount `need` (in the line's unit). */
export function suggest(
  it: StockItem,
  line: RecipeLine,
  need: Rat | null,
  cat: Catalog,
  ratio: Rat = { n: 1n, d: 1n },
): Suggestion {
  const lineUnit = line.unit ?? 'ea'
  const scaled = need ? mul(need, ratio) : null
  switch (it.amountKind) {
    case 'exact': {
      const have = it.quantity ?? '0'
      const got =
        scaled && it.unit
          ? convert(scaled, lineUnit, it.unit, ...conversionSets(it, line, cat))
          : null
      if (!got) return { take: { kind: 'exact', quantity: '0' }, needsAmount: true }
      const q = format(got)
      return { take: { kind: 'exact', quantity: cmp(q, have) > 0 ? have : q }, needsAmount: false }
    }
    case 'portions': {
      const have = it.portions ?? 0
      const got = scaled
        ? convert(scaled, lineUnit, 'portion', ...conversionSets(it, line, cat))
        : null
      const n = got ? Number((got.n + got.d - 1n) / got.d) : 1
      return {
        take: { kind: 'portions', portions: Math.max(1, Math.min(have, n)) },
        needsAmount: false,
      }
    }
    case 'level':
      return { take: { kind: 'level', level: nextLevel(it.level) }, needsAmount: false }
  }
}

/** Whether a take uses everything the item has. */
export function usesAll(it: StockItem, t: Take): boolean {
  if (t.kind === 'exact') return !!it.quantity && cmp(t.quantity, it.quantity) >= 0
  if (t.kind === 'portions') return t.portions >= (it.portions ?? 0)
  return t.level === 'finished'
}

/** What stays in stock after a take ("500 g", "2 portions", "Half"), or null when it's all used. */
export function leftAfter(it: StockItem, t: Take): string | null {
  if (usesAll(it, t)) return null
  if (t.kind === 'exact') {
    const left = sub(it.quantity ?? '0', t.quantity)
    return isZero(left) ? null : left
  }
  if (t.kind === 'portions') return String((it.portions ?? 0) - t.portions)
  return t.level === 'finished' ? null : t.level
}

/** "Uses it all → add to list" defaults to yes for things bought regularly (2+ purchases). */
export const boughtRegularly = (it: StockItem, cat: Catalog) =>
  (cat.timesBought.get(it.productId) ?? 0) >= 2

/** A substitution the household can make now: its first use's ingredient is in stock. */
export function usableSwaps(
  line: RecipeLine,
  recipeId: string,
  subs: Substitution[],
  stock: StockItem[],
  cat: Catalog,
): { sub: Substitution; ingredientId: string; ratio: Rat; items: StockItem[] }[] {
  if (!line.ingredient_id) return []
  return (
    subs
      .filter(
        (s) =>
          s.ingredientId === line.ingredient_id && (s.recipeId === null || s.recipeId === recipeId),
      )
      .flatMap((s) => {
        const first = s.uses[0]
        if (!first) return []
        const items = matchingItems(first.ingredient_id, stock, cat)
        if (items.length === 0) return []
        const ratio = rat(String(first.ratio)) ?? { n: 1n, d: 1n }
        return [{ sub: s, ingredientId: first.ingredient_id, ratio, items }]
      })
      // Recipe-specific answers first.
      .sort((a, b) => Number(b.sub.recipeId !== null) - Number(a.sub.recipeId !== null))
  )
}
