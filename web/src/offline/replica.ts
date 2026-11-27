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
  /** Cross-dimension conversions, `{ea: '150 g'}`. Absent on rows pulled before M2. */
  conversions?: Conversions
}

/** Unit code → amount in a mass or volume unit, e.g. `{ea: '150 g', cup: '185 g'}`. */
export type Conversions = Record<string, string>

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
  conversions?: Conversions
  /** Set on leftover products ("Leftover chili"): one per recipe. */
  recipeId?: string | null
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
  cookSessionId?: string | null
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
  cookSessionId?: string | null
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
  /** The recipe the item is for ("Recipe: Chicken curry"). */
  recipeId?: string | null
}

export type RecipeSourceKind = 'manual' | 'url' | 'instagram' | 'photo' | 'text' | 'idea'

export type Recipe = Base & {
  title: string
  subtitle: string | null
  sourceKind: RecipeSourceKind
  sourceUrl: string | null
  /** BCP 47; null = English. */
  sourceLang: string | null
  currentVersionId: string | null
  servings: number | null
  totalMinutes: number | null
  coverBlob: string | null
  tags: string[]
  saved: boolean
  ideaExpiresAt: string | null
  cookedCount: number
  lastCookedAt: string | null
  importId: string | null
}

/**
 * One structured ingredient line (architecture-m2 §3). Nested JSON keeps the server's snake_case
 * keys. Lines the client has not synced yet carry only `line_id`, `raw` and `name` (= raw).
 */
export type RecipeLine = {
  line_id: string
  group?: string
  raw: string
  qty?: string
  qty_max?: string
  unit?: string
  name: string
  note?: string
  optional: boolean
  staple: boolean
  ingredient_id?: string
  match_confidence?: number
}

export type RecipeStep = { text: string; minutes?: number | null }

export type RecipeOriginal = {
  lang: string
  title: string
  ingredients: string[]
  steps: string[]
  notes?: string | null
}

export type RecipeVersion = Base & {
  recipeId: string
  n: number
  parentVersionId: string | null
  origin: 'import' | 'manual' | 'chat' | 'restore' | 'idea'
  changeSummary: string | null
  title: string
  servings: number | null
  totalMinutes: number | null
  ingredients: RecipeLine[]
  steps: RecipeStep[]
  notes: string | null
  original: RecipeOriginal | null
  originalVersionId: string | null
  createdBy: string | null
}

export type RecipePhoto = Base & {
  recipeId: string
  cookSessionId: string | null
  blobRef: string
  takenOn: string | null
  caption: string | null
  createdBy: string | null
}

export type CookSession = Base & {
  recipeId: string
  versionId: string | null
  cookedOn: string
  cookedBy: string | null
  portionsMade: number
  stored: { location_id: string; portions: number; stock_item_id: string }[]
  uses: unknown[]
  subs: unknown[]
  leftoverProductId: string | null
  note: string | null
  undoneAt: string | null
}

export type RecipeImportStatus =
  | 'queued'
  | 'fetching'
  | 'parsing'
  | 'translating'
  | 'done'
  | 'failed'
  | 'needs_input'
  | 'waiting_quota'

export type RecipeImport = Base & {
  batchId: string | null
  kind: 'url' | 'instagram' | 'photo' | 'text'
  input: string | null
  blobRefs: string[]
  status: RecipeImportStatus
  error: { code?: string; detail?: string } | null
  recipeId: string | null
  attempts: number
  detectedLang: string | null
}

export type Substitution = Base & {
  /** The ingredient that is missing. */
  ingredientId: string
  uses: { ingredient_id: string; ratio: number; unit?: string; note?: string }[]
  note: string | null
  /** Recipe-specific answer (LLM), or null for a general swap. */
  recipeId: string | null
  source: 'seed' | 'llm' | 'user'
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
  recipe: Recipe
  recipe_version: RecipeVersion
  recipe_photo: RecipePhoto
  cook_session: CookSession
  recipe_import: RecipeImport
  substitution: Substitution
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
  'recipe',
  'recipe_version',
  'recipe_photo',
  'cook_session',
  'recipe_import',
  'substitution',
]

const snake = /_([a-z])/g

/** `default_qty` → `defaultQty`, shallow: nested JSON keeps its keys. */
export function camelizeRow<T>(row: Record<string, unknown>): T {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(row))
    out[k.replace(snake, (_, c: string) => c.toUpperCase())] = v
  return out as T
}
