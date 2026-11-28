import type { Category, Ingredient, Location, Product, ShoppingItem } from '../../offline/replica'
import { normalize } from '../catalog/suggest/text'

/** An item as the list shows it: display name from the product when linked, else what was typed. */
export type Row = ShoppingItem & { title: string; brand: string | null; product: Product | null }

export function toRows(items: ShoppingItem[], products: Map<string, Product>): Row[] {
  return items.map((i) => {
    const p = i.productId ? (products.get(i.productId) ?? null) : null
    return { ...i, title: p?.name ?? (i.text || 'Item'), brand: p?.brand ?? null, product: p }
  })
}

export const categoryOf = (r: Row): string | null => r.categoryId ?? r.product?.categoryId ?? null

/** Group rows by category (in category order; uncategorised last) or by their first store. */
export function groupRows(
  rows: Row[],
  by: 'type' | 'store',
  categories: Category[],
  stores: { id: string; name: string }[],
): { key: string; title: string; rows: Row[] }[] {
  const groups = new Map<string, Row[]>()
  const push = (k: string, r: Row) => groups.set(k, [...(groups.get(k) ?? []), r])
  for (const r of rows) push(by === 'type' ? (categoryOf(r) ?? '') : (r.storeIds[0] ?? ''), r)
  const order = by === 'type' ? categories.map((c) => c.id) : stores.map((s) => s.id)
  const titles = new Map<string, string>(
    by === 'type' ? categories.map((c) => [c.id, c.name]) : stores.map((s) => [s.id, s.name]),
  )
  const empty = by === 'type' ? 'Other' : 'Any store'
  const keys = [
    ...order.filter((k) => groups.has(k)),
    ...[...groups.keys()].filter((k) => !order.includes(k)),
  ]
  return keys.map((k) => ({
    key: k || 'none',
    title: titles.get(k) ?? empty,
    rows: groups.get(k) ?? [],
  }))
}

/** Where a product usually goes: a location of its default kind, else the first one. */
export function defaultLocation(r: Row, locations: Location[]): Location | undefined {
  const kind = r.product?.defaultLocationKind
  return (kind && locations.find((l) => l.kind === kind)) || locations[0]
}

/** Name key for matching typed text to products: case, accents and a plural s/es ignored. */
export const sameName = (name: string) =>
  normalize(name)
    .split(' ')
    .map((w) => (w.length > 3 ? w.replace(/(es|s)$/, '') : w))
    .join(' ')

/**
 * The household product typed text names: the same name (brand-less first, as the server
 * picks), else a brand-less one that differs only by case, accents or a plural.
 */
export function productNamed(products: Product[], text: string): Product | undefined {
  const exact = text.trim().toLowerCase()
  const byAge = (a: Product, b: Product) =>
    Number(a.brand !== null) - Number(b.brand !== null) || a.createdAt.localeCompare(b.createdAt)
  const same = products.filter((p) => p.name.trim().toLowerCase() === exact).sort(byAge)
  if (same[0]) return same[0]
  const key = sameName(text)
  return products.filter((p) => !p.brand && sameName(p.name) === key).sort(byAge)[0]
}

/** The productId a tick sends for an item without one: the product it names, else a new id. */
export const productPick = (row: ShoppingItem, products: Product[], newId: () => string) =>
  row.productId ? {} : { productId: productNamed(products, row.text)?.id ?? newId() }

/**
 * How an ingredient is usually bought: its own unit, else the nearest ancestor's ("salt" is a
 * bag, "olive oil" a bottle). The server fills the same unit when an item arrives without one.
 */
export function buyUnit(
  ingredientId: string | null | undefined,
  ingredients: Map<string, Ingredient>,
): string | undefined {
  let id = ingredientId
  for (let depth = 0; id && depth < 8; depth++) {
    const i = ingredients.get(id)
    if (!i) return undefined
    if (i.defaultUnit) return i.defaultUnit
    id = i.parentId
  }
  return undefined
}
