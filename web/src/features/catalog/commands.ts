import type { StockpotDB } from '../../offline/db'
import { registerReducer } from '../../offline/reducers'
import type { Ingredient, Location, Product, Store } from '../../offline/replica'

/** Every catalog command touches the one row it names. */
const byId = { targets: (p: { id: string }) => [p.id] }

/** Slug for an optimistic household ingredient; the server assigns the real one. */
const slugify = (name: string) =>
  name
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '') || 'ingredient'

async function base(db: StockpotDB, id: string, clientTime: string) {
  const household = (await db.meta.get('sync.household'))?.value
  return {
    id,
    householdId: typeof household === 'string' ? household : null,
    createdAt: clientTime,
    updatedAt: clientTime,
    // Rows the server hasn't confirmed carry rev 0; a full re-pull purges them if rejected.
    rev: 0,
  }
}

/** Sets every field the payload carries (null clears); absent fields stay as they are. */
function patch<T extends object>(row: T, payload: object, skip = ['id']): T {
  const next = { ...row } as Record<string, unknown>
  for (const [k, v] of Object.entries(payload)) if (!skip.includes(k)) next[k] = v
  return next as T
}

registerReducer(
  'catalog.product.create',
  async (db, p, { clientTime }) => {
    if (await db.product.get(p.id)) return
    const { id: _id, ...fields } = p
    const row: Product = {
      ...(await base(db, p.id, clientTime)),
      name: p.name,
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
      matchSource: p.ingredientId ? 'user' : 'none',
      matchConfidence: null,
      enrichState: 'pending',
      enrichAttempts: 0,
      offCode: null,
      note: null,
    }
    await db.product.put(patch(row, fields))
  },
  byId,
)

registerReducer(
  'catalog.product.update',
  async (db, p) => {
    const row = await db.product.get(p.id)
    if (row) await db.product.put(patch(row, p))
  },
  byId,
)

registerReducer('catalog.product.delete', (db, p) => db.product.delete(p.id), byId)

registerReducer(
  'catalog.product.set_ingredient',
  async (db, p) => {
    const row = await db.product.get(p.id)
    if (!row) return
    await db.product.put({
      ...row,
      ingredientId: p.ingredientId,
      extraIngredientIds: p.extraIngredientIds ?? [],
      matchSource: 'user',
      matchConfidence: 1,
      enrichState: 'done',
    })
  },
  byId,
)

registerReducer(
  'catalog.store.create',
  async (db, p, { clientTime }) => {
    if (await db.store.get(p.id)) return
    const row: Store = {
      ...(await base(db, p.id, clientTime)),
      name: p.name,
      sort: p.sort ?? 0,
      color: p.color ?? null,
    }
    await db.store.put(row)
  },
  byId,
)

registerReducer(
  'catalog.store.update',
  async (db, p) => {
    const row = await db.store.get(p.id)
    if (row) await db.store.put(patch(row, p))
  },
  byId,
)

registerReducer('catalog.store.delete', (db, p) => db.store.delete(p.id), byId)

registerReducer(
  'catalog.location.create',
  async (db, p, { clientTime }) => {
    if (await db.location.get(p.id)) return
    const row: Location = {
      ...(await base(db, p.id, clientTime)),
      name: p.name,
      kind: p.kind,
      sort: p.sort ?? 100,
    }
    await db.location.put(row)
  },
  byId,
)

registerReducer(
  'catalog.location.update',
  async (db, p) => {
    const row = await db.location.get(p.id)
    if (row) await db.location.put(patch(row, p))
  },
  byId,
)

registerReducer('catalog.location.delete', (db, p) => db.location.delete(p.id), byId)

registerReducer(
  'catalog.ingredient.create',
  async (db, p, { clientTime }) => {
    if (await db.ingredient.get(p.id)) return
    const row: Ingredient = {
      ...(await base(db, p.id, clientTime)),
      slug: slugify(p.name),
      name: p.name,
      plural: p.plural ?? null,
      parentId: p.parentId ?? null,
      categoryId: p.categoryId ?? null,
      icon: p.icon ?? null,
      aliases: p.aliases ?? [],
      shelfLife: {},
      defaultUnit: null,
      source: 'household',
    }
    await db.ingredient.put(row)
  },
  byId,
)
