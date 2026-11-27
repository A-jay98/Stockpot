import { useQuery } from '@tanstack/react-query'
import { api, unwrap } from '../../api/client'
import type { components } from '../../api/schema'
import { db, type StockpotDB } from '../../offline/db'
import type { Product, ShelfLife } from '../../offline/replica'
import type { Chain, Layer } from './estimate'

export type FoodRule = components['schemas']['FoodRule']

/** meta key under which the last fetched rules are kept for offline estimates. */
const META = 'foodRules'

export const foodRulesKey = ['food-rules'] as const

async function fetchRules(): Promise<FoodRule[]> {
  const { rules } = await unwrap(api.GET('/v1/food-rules'))
  // Offline-first: the replica's optimistic estimates read this copy when the network is gone.
  await db.meta.put({ key: META, value: rules }).catch(() => {})
  return rules
}

/** Defaults and the household's overrides. Falls back to the cached copy when offline. */
export function useFoodRules() {
  return useQuery({
    queryKey: foodRulesKey,
    queryFn: fetchRules,
    staleTime: 5 * 60_000,
    placeholderData: undefined,
  })
}

async function cachedRules(database: StockpotDB): Promise<FoodRule[]> {
  const row = await database.meta.get(META)
  return Array.isArray(row?.value) ? (row.value as FoodRule[]) : []
}

/** Warm the cache; safe to call whenever online. */
export const refreshFoodRules = () => fetchRules().catch(() => {})

/**
 * The shelf-life chain of a product, read from the replica and the cached rules:
 * the same layers the server walks, so optimistic estimates match its answer.
 */
export async function buildChain(
  database: StockpotDB,
  product: Pick<Product, 'ingredientId' | 'categoryId' | 'shelfLife'>,
): Promise<Chain> {
  const rules = await cachedRules(database)
  const rule = (kind: 'ingredient' | 'category', id: string, def: boolean): ShelfLife | undefined =>
    rules.find(
      (r) => r.subjectKind === kind && r.subjectId === id && (r.householdId === undefined) === def,
    )?.shelfLife
  const ingredients: Layer[] = []
  let categoryId = product.categoryId
  let id = product.ingredientId
  for (let depth = 0; id && depth < 12; depth++) {
    const ing = await database.ingredient.get(id)
    if (!ing) break
    ingredients.push({ household: rule('ingredient', ing.id, false), seed: ing.shelfLife })
    categoryId ??= ing.categoryId
    id = ing.parentId
  }
  return {
    product: product.shelfLife,
    ingredients,
    categoryHousehold: categoryId ? rule('category', categoryId, false) : undefined,
    categoryDefault: categoryId ? rule('category', categoryId, true) : undefined,
  }
}
