import { registerReducer } from '../../offline/reducers'
import type { ShoppingItem, ShoppingList } from '../../offline/replica'

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

registerReducer('shopping.item.check', async (db, p, { clientTime }) => {
  const row = await db.shopping_item.get(p.id)
  if (!row) return
  await db.shopping_item.put({
    ...row,
    checkedAt: row.checkedAt ?? clientTime,
    stockItemId: p.inventory ? (row.stockItemId ?? p.inventory.stockItemId) : row.stockItemId,
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
