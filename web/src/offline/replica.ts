/**
 * Row types of the local replica: the server's synced tables with camelCase
 * fields (architecture-m1 §3). Decimals are strings, timestamps ISO strings,
 * dates `YYYY-MM-DD`. Nested JSON (`shelfLife`) keeps the server's keys.
 */

type Base = {
  id: string
  /** null for global catalog rows. */
  householdId: string | null
  createdAt: string
  updatedAt: string
  rev: number
}

export type LocationKind = 'fridge' | 'freezer' | 'pantry' | 'other'
export type ShelfLife = {
  fridge?: number
  freezer?: number
  pantry?: number
  opened_fridge?: number
  thawed_fridge?: number
}

export type Category = Base & { slug: string; name: string; icon: string | null; sort: number }

export type Ingredient = Base & {
  slug: string
  name: string
  plural: string | null
  parentId: string | null
  categoryId: string | null
  icon: string | null
  aliases: string[]
  shelfLife: ShelfLife
  defaultUnit: string | null
  source: 'seed' | 'household' | 'llm' | 'admin'
}

export type Store = Base & { name: string; sort: number; color: string | null }
export type Location = Base & { name: string; kind: LocationKind; sort: number }

export type Product = Base & {
  name: string
  brand: string | null
  ingredientId: string | null
  extraIngredientIds: string[]
  categoryId: string | null
  icon: string | null
  defaultStoreId: string | null
  defaultQty: string | null
  defaultUnit: string | null
  package: string | null
  defaultLocationKind: LocationKind | null
  shelfLife: ShelfLife | null
  matchSource: 'none' | 'rule' | 'llm' | 'user' | 'barcode'
  matchConfidence: number | null
  enrichState: 'pending' | 'done' | 'failed' | 'skipped'
  enrichAttempts: number
  offCode: string | null
  note: string | null
}

export type ProductBarcode = Base & { productId: string; code: string }

export type ProductStat = Base & {
  productId: string
  storeId: string | null
  timesBought: number
  lastBoughtAt: string | null
  typicalQty: string | null
  typicalUnit: string | null
  medianIntervalDays: number | null
  lastQty: string | null
}

export type StockLevel = 'full' | 'most' | 'half' | 'low' | 'almost_out'

export type StockItem = Base & {
  productId: string
  locationId: string | null
  amountKind: 'exact' | 'portions' | 'level'
  quantity: string | null
  unit: string | null
  portions: number | null
  level: StockLevel | null
  low: boolean
  expiresOn: string | null
  expiryKind: 'use_by' | 'best_before' | 'estimated' | null
  openedOn: string | null
  thawedOn: string | null
  frozenOn: string | null
  source: 'manual' | 'shopping' | 'capture' | 'cook' | 'split'
  parentItemId: string | null
  note: string | null
}

export type StockEvent = Base & {
  stockItemId: string
  kind:
    | 'add'
    | 'consume'
    | 'discard'
    | 'move'
    | 'adjust'
    | 'open'
    | 'thaw'
    | 'freeze'
    | 'split'
    | 'low'
  delta: string | null
  unit: string | null
  fromLocationId: string | null
  toLocationId: string | null
  reason: string | null
  actorId: string | null
  commandId: string | null
}

export type ShoppingList = Base & { name: string; sort: number }

export type ShoppingItem = Base & {
  listId: string
  productId: string | null
  text: string
  qty: string | null
  unit: string | null
  categoryId: string | null
  storeIds: string[]
  note: string | null
  sort: number
  checkedAt: string | null
  checkedBy: string | null
  stockItemId: string | null
}

/** Server table name → row type. Keep in step with `syncdb.Tables`. */
export type ReplicaTables = {
  category: Category
  ingredient: Ingredient
  store: Store
  location: Location
  product: Product
  product_barcode: ProductBarcode
  product_stat: ProductStat
  stock_item: StockItem
  stock_event: StockEvent
  shopping_list: ShoppingList
  shopping_item: ShoppingItem
}

export type ReplicaTable = keyof ReplicaTables

export const replicaTables: ReplicaTable[] = [
  'category',
  'ingredient',
  'store',
  'location',
  'product',
  'product_barcode',
  'product_stat',
  'stock_item',
  'stock_event',
  'shopping_list',
  'shopping_item',
]

const snake = /_([a-z])/g

/** `default_qty` → `defaultQty`, shallow: nested JSON keeps its keys. */
export function camelizeRow<T>(row: Record<string, unknown>): T {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(row))
    out[k.replace(snake, (_, c: string) => c.toUpperCase())] = v
  return out as T
}
