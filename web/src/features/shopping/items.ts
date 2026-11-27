import type { Category, Location, Product, ShoppingItem } from '../../offline/replica'

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
