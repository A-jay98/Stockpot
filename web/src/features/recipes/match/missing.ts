import type { Product, ProductStat, ShoppingItem } from '../../../offline/replica'
import { unitFromWord } from '../../catalog/suggest/units'
import type { Kitchen, RankedRecipe } from './rank'
import { scaleQty } from './rank'

/** A shopping line "Missing to list" would add. */
export type MissingItem = {
  lineId: string
  text: string
  /** The household's product for it; absent means a new product named `text`. */
  productId?: string
  /** The line's ingredient, for a new product. */
  ingredientId?: string
  qty?: string
  unit?: string
}

const norm = (s: string) => s.trim().toLowerCase()

/** Whether product's ingredient (or an extra one) is the wanted ingredient or a kind of it. */
function satisfies(k: Kitchen, p: Product, want: string): boolean {
  for (const own of [p.ingredientId, ...p.extraIngredientIds]) {
    const seen = new Set<string>()
    for (let id: string | null | undefined = own; id && !seen.has(id); ) {
      if (id === want) return true
      seen.add(id)
      id = k.ingredients.get(id)?.parentId
    }
  }
  return false
}

/**
 * The missing lines of a recipe as shopping items: scaled by factor, matched to the household's
 * product for that ingredient (the one bought most often), and skipping lines already open on the
 * list (same product, a product of that ingredient, or the same text).
 */
export function missingItems(
  k: Kitchen,
  r: RankedRecipe,
  products: Product[],
  stats: ProductStat[],
  open: ShoppingItem[],
  factor = 1,
): MissingItem[] {
  const bought = new Map<string, number>()
  for (const s of stats) bought.set(s.productId, (bought.get(s.productId) ?? 0) + s.timesBought)
  const byId = new Map(products.map((p) => [p.id, p]))
  const out: MissingItem[] = []
  for (const m of r.lines) {
    if (m.status !== 'missing') continue
    const want = m.line.ingredient_id
    const onList = open.some((i) => {
      if (i.checkedAt) return false
      const p = i.productId ? byId.get(i.productId) : undefined
      if (want && p && satisfies(k, p, want)) return true
      return norm(p?.name ?? i.text) === norm(m.line.name)
    })
    if (onList) continue
    const product = want
      ? products
          .filter((p) => !p.recipeId && satisfies(k, p, want))
          .sort(
            (a, b) =>
              (bought.get(b.id) ?? 0) - (bought.get(a.id) ?? 0) || a.name.localeCompare(b.name),
          )[0]
      : undefined
    const unit = m.line.unit ? unitFromWord(m.line.unit) : undefined
    const item: MissingItem = { lineId: m.line.line_id, text: product?.name ?? m.line.name }
    if (product) item.productId = product.id
    else if (want) item.ingredientId = want
    if (m.line.qty && unit) {
      item.qty = scaleQty(m.line.qty_max ?? m.line.qty, factor)
      item.unit = unit
    }
    out.push(item)
  }
  return out
}
