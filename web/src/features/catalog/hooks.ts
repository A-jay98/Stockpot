import { useLiveQuery } from 'dexie-react-hooks'
import { db } from '../../offline/db'
import type { Category, Ingredient, Location, Product, Store } from '../../offline/replica'

// UI reads household data only from the local replica, never the API. Each hook returns
// [] until the first read finishes; the replica fills as syncing catches up.
import './commands' // registers the optimistic reducers

const byName = <T extends { name: string }>(a: T, b: T) => a.name.localeCompare(b.name)

export const useCategories = (): Category[] =>
  useLiveQuery(() => db.category.orderBy('sort').toArray(), [], [] as Category[])

export const useIngredients = (): Ingredient[] =>
  useLiveQuery(() => db.ingredient.toArray(), [], [] as Ingredient[])

export const useStores = (): Store[] =>
  useLiveQuery(() => db.store.orderBy('sort').toArray(), [], [] as Store[])

export const useLocations = (): Location[] =>
  useLiveQuery(() => db.location.orderBy('sort').toArray(), [], [] as Location[])

export const useProducts = (): Product[] =>
  useLiveQuery(async () => (await db.product.toArray()).sort(byName), [], [] as Product[])
