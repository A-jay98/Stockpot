import { useLiveQuery } from 'dexie-react-hooks'
import { db } from '../../offline/db'
import type { ShoppingItem, ShoppingList, StockItem } from '../../offline/replica'
import './commands' // registers the optimistic reducers

export const useShoppingLists = (): ShoppingList[] =>
  useLiveQuery(() => db.shopping_list.orderBy('sort').toArray(), [], [] as ShoppingList[])

export const useShoppingItems = (listId: string | undefined): ShoppingItem[] =>
  useLiveQuery(
    () => (listId ? db.shopping_item.where('listId').equals(listId).sortBy('sort') : []),
    [listId],
    [] as ShoppingItem[],
  )

/** Every item of every list, for the guess list's "already on a list" check. */
export const useAllShoppingItems = (): ShoppingItem[] =>
  useLiveQuery(() => db.shopping_item.toArray(), [], [] as ShoppingItem[])

export const useStockItems = (): StockItem[] =>
  useLiveQuery(() => db.stock_item.toArray(), [], [] as StockItem[])
