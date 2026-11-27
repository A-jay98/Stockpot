import { useLiveQuery } from 'dexie-react-hooks'
import { useEffect } from 'react'
import { db } from '../../offline/db'
import type { Ingredient, Location, Product, StockEvent, StockItem } from '../../offline/replica'
import { resolveFoodIcon } from '../../ui/food-icons'
import '../catalog/commands'
import './commands' // registers the optimistic reducers
import { refreshFoodRules } from './foodRules'

/** A stock item joined to what the screens show about it. */
export type StockRow = {
  item: StockItem
  product: Product
  location: Location | undefined
  ingredient: Ingredient | undefined
  /** Display name: the product's, with the brand when there is one. */
  name: string
  icon: string
  categoryId: string | null
}

async function load(): Promise<StockRow[]> {
  const [items, products, locations, ingredients, categories] = await Promise.all([
    db.stock_item.toArray(),
    db.product.toArray(),
    db.location.toArray(),
    db.ingredient.toArray(),
    db.category.toArray(),
  ])
  const p = new Map(products.map((x) => [x.id, x]))
  const l = new Map(locations.map((x) => [x.id, x]))
  const i = new Map(ingredients.map((x) => [x.id, x]))
  const c = new Map(categories.map((x) => [x.id, x]))
  const rows: StockRow[] = []
  for (const item of items) {
    const product = p.get(item.productId)
    if (!product) continue
    const ingredient = product.ingredientId ? i.get(product.ingredientId) : undefined
    const ancestors: (string | null)[] = []
    for (let a = ingredient, n = 0; a?.parentId && n < 12; n++) {
      a = i.get(a.parentId)
      if (a) ancestors.push(a.icon)
    }
    const categoryId = product.categoryId ?? ingredient?.categoryId ?? null
    rows.push({
      item,
      product,
      location: item.locationId ? l.get(item.locationId) : undefined,
      ingredient,
      name: product.brand ? `${product.name} · ${product.brand}` : product.name,
      icon: resolveFoodIcon({
        product: product.icon,
        ingredient: ingredient?.icon,
        ancestors,
        category: categoryId ? c.get(categoryId)?.icon : undefined,
      }),
      categoryId,
    })
  }
  return rows
}

/** Every live stock item, joined. Empty until the first read finishes. */
export function useStock(): StockRow[] {
  useEffect(() => void refreshFoodRules(), []) // warm the offline estimate cache
  return useLiveQuery(load, [], [] as StockRow[])
}

export function useStockRow(id: string | undefined): StockRow | undefined | null {
  const rows = useLiveQuery(load, [], undefined)
  if (!rows) return undefined // loading
  return rows.find((r) => r.item.id === id) ?? null // null: gone
}

export function useStockEvents(itemId: string | undefined): StockEvent[] {
  return useLiveQuery(
    async () =>
      itemId
        ? (await db.stock_event.where('stockItemId').equals(itemId).toArray()).sort((a, b) =>
            b.createdAt.localeCompare(a.createdAt),
          )
        : [],
    [itemId],
    [] as StockEvent[],
  )
}
