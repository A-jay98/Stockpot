import type { CommandPayloads } from '../../offline/commands'
import { applyReducer, registerReducer } from '../../offline/reducers'
import type { ShoppingItem, ShoppingList, StockItem } from '../../offline/replica'
import '../catalog/commands' // a free-text check creates its product

// Optimistic shopping reducers. Each one sets fields (never adds), so replaying them after a
// pull is harmless. The server derives product links and purchase stats; the next pull brings them.

async function base(db: import('../../offline/db').StockpotDB, id: string, clientTime: string) {
  const household = (await db.meta.get('sync.household'))?.value
  return {
    id,
    householdId: typeof household === 'string' ? household : null,
    createdAt: clientTime,
    updatedAt: clientTime,
    rev: 0,
  }
}

registerReducer('shopping.list.create', async (db, p, { clientTime }) => {
  if (await db.shopping_list.get(p.id)) return
  const row: ShoppingList = {
    ...(await base(db, p.id, clientTime)),
    name: p.name,
    sort: p.sort ?? 100,
  }
  await db.shopping_list.put(row)
})

registerReducer('shopping.list.rename', async (db, p) => {
  const row = await db.shopping_list.get(p.id)
  if (row) await db.shopping_list.put({ ...row, name: p.name })
})

registerReducer('shopping.list.delete', async (db, p) => {
  await db.shopping_item.where('listId').equals(p.id).delete()
  await db.shopping_list.delete(p.id)
})

registerReducer('shopping.item.add', async (db, p, { clientTime }) => {
  if (await db.shopping_item.get(p.id)) return
  // A new product the item proposes (see the server's linkNewProduct) shows right away.
  const name = p.text?.trim()
  if (p.productId && name && !(await db.product.get(p.productId))) {
    const ingredient = p.ingredientId ? await db.ingredient.get(p.ingredientId) : undefined
    await applyReducer(
      db,
      'catalog.product.create',
      {
        id: p.productId,
        name,
        ...(ingredient
          ? {
              ingredientId: ingredient.id,
              categoryId: ingredient.categoryId ?? p.categoryId ?? null,
            }
          : {}),
      },
      clientTime,
    )
  }
  if (p.rememberAmount && p.productId && p.unit) {
    const product = await db.product.get(p.productId)
    if (product)
      await db.product.put({ ...product, defaultUnit: p.unit, defaultQty: p.qty ?? null })
  }
  const siblings = await db.shopping_item.where('listId').equals(p.listId).toArray()
  const row: ShoppingItem = {
    ...(await base(db, p.id, clientTime)),
    listId: p.listId,
    productId: p.productId ?? null,
    text: p.text ?? '',
    qty: p.qty ?? null,
    unit: p.unit ?? null,
    categoryId: p.categoryId ?? null,
    storeIds: p.storeIds ?? [],
    note: p.note ?? null,
    sort: p.sort ?? Math.max(0, ...siblings.map((s) => s.sort)) + 10,
    checkedAt: null,
    checkedBy: null,
    stockItemId: null,
    recipeId: p.recipeId ?? null,
  }
  await db.shopping_item.put(row)
})

registerReducer('shopping.item.update', async (db, p) => {
  const row = await db.shopping_item.get(p.id)
  if (!row) return
  const { id: _id, ...fields } = p
  const next: Record<string, unknown> = { ...row }
  for (const [k, v] of Object.entries(fields))
    next[k] = k === 'text' ? (v ?? '') : k === 'storeIds' ? (v ?? []) : v
  await db.shopping_item.put(next as ShoppingItem)
})

registerReducer('shopping.item.delete', (db, p) => db.shopping_item.delete(p.id))

registerReducer('shopping.item.reorder', async (db, p) => {
  const rows = await db.shopping_item.bulkGet(p.ids)
  await db.shopping_item.bulkPut(
    rows.flatMap((r, i) => (r && r.listId === p.listId ? [{ ...r, sort: (i + 1) * 10 }] : [])),
  )
})

const shelfKey = {
  fridge: 'fridge',
  freezer: 'freezer',
  pantry: 'pantry',
  other: 'pantry',
} as const

/**
 * Put-away offline: the stock item the server will create, so the item never vanishes between
 * leaving the list and the next sync. Expiry is the typed date, else a shelf-life estimate from
 * the product, its ingredient or the ingredient's parent (the server computes the same).
 */
async function optimisticStock(
  db: import('../../offline/db').StockpotDB,
  item: ShoppingItem & { productId: string },
  m: NonNullable<CommandPayloads['shopping.item.check']['inventory']>,
  clientTime: string,
) {
  if (await db.stock_item.get(m.stockItemId)) return
  const product = await db.product.get(item.productId)
  const locations = await db.location.orderBy('sort').toArray()
  const loc =
    locations.find((l) => l.id === m.locationId) ??
    (m.locationId
      ? undefined
      : (locations.find((l) => l.kind === product?.defaultLocationKind) ?? locations[0]))
  const ingredient = product?.ingredientId
    ? await db.ingredient.get(product.ingredientId)
    : undefined
  const parent = ingredient?.parentId ? await db.ingredient.get(ingredient.parentId) : undefined
  const key = shelfKey[loc?.kind ?? 'other']
  const days = product?.shelfLife?.[key] ?? ingredient?.shelfLife[key] ?? parent?.shelfLife[key]
  const day = (offset: number) => {
    const d = new Date(clientTime)
    d.setDate(d.getDate() + offset)
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  }
  const row: StockItem = {
    ...(await base(db, m.stockItemId, clientTime)),
    productId: item.productId,
    locationId: loc?.id ?? null,
    amountKind: 'exact',
    quantity: m.quantity ?? item.qty,
    unit: m.unit ?? item.unit,
    portions: null,
    level: null,
    low: false,
    expiresOn: m.expiresOn ?? (days ? day(days) : null),
    expiryKind: m.expiresOn ? 'best_before' : days ? 'estimated' : null,
    openedOn: null,
    thawedOn: null,
    frozenOn: loc?.kind === 'freezer' ? day(0) : null,
    source: 'shopping',
    parentItemId: null,
    note: null,
  }
  await db.stock_item.put(row)
}

/**
 * The product a free-text item becomes when ticked: the client's pick (see `productPick`), which
 * the server uses as is or creates under that id. A new one is shown optimistically meanwhile.
 */
async function productFor(
  db: import('../../offline/db').StockpotDB,
  row: ShoppingItem,
  pick: string | null | undefined,
  clientTime: string,
): Promise<string | null> {
  if (row.productId || !pick) return row.productId
  const name = row.text.trim()
  if (!name) return null
  await applyReducer(db, 'catalog.product.create', { id: pick, name }, clientTime)
  return pick
}

registerReducer('shopping.item.check', async (db, p, { clientTime }) => {
  const found = await db.shopping_item.get(p.id)
  if (!found) return
  const row = { ...found, productId: await productFor(db, found, p.productId, clientTime) }
  const putAway = p.inventory && !row.stockItemId
  if (p.inventory && putAway && row.productId)
    await optimisticStock(db, { ...row, productId: row.productId }, p.inventory, clientTime)
  await db.shopping_item.put({
    ...row,
    checkedAt: row.checkedAt ?? clientTime,
    stockItemId: putAway && p.inventory ? p.inventory.stockItemId : row.stockItemId,
  })
})

registerReducer('shopping.item.uncheck', async (db, p) => {
  const row = await db.shopping_item.get(p.id)
  if (row && !row.stockItemId)
    await db.shopping_item.put({ ...row, checkedAt: null, checkedBy: null })
})

registerReducer('shopping.item.clear_checked', async (db, p) => {
  await db.shopping_item
    .where('listId')
    .equals(p.listId)
    .filter((i) => i.checkedAt !== null)
    .delete()
})
