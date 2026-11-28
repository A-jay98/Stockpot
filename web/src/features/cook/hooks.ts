import { useLiveQuery } from 'dexie-react-hooks'
import { useEffect } from 'react'
import { db } from '../../offline/db'
import type {
  CookSession,
  Location,
  Recipe,
  RecipePhoto,
  RecipeVersion,
  ShoppingList,
  StockItem,
  Substitution,
} from '../../offline/replica'
import { refreshFoodRules } from '../inventory/foodRules'
import type { Catalog } from './plan'
import './commands' // registers the optimistic reducers

export type CookData = {
  recipe: Recipe
  version: RecipeVersion
  stock: StockItem[]
  catalog: Catalog
  locations: Location[]
  lists: ShoppingList[]
  substitutions: Substitution[]
}

/** Everything the cook sheet reads, from the replica. undefined while loading, null if gone. */
export function useCookData(recipeId: string | undefined): CookData | null | undefined {
  useEffect(() => void refreshFoodRules(), []) // warm the offline estimate cache for eat-by dates
  return useLiveQuery(async () => {
    const recipe = recipeId ? await db.recipe.get(recipeId) : undefined
    if (!recipe?.currentVersionId) return null
    const version = await db.recipe_version.get(recipe.currentVersionId)
    if (!version) return null
    const [stock, products, ingredients, stats, locations, lists, substitutions] =
      await Promise.all([
        db.stock_item.toArray(),
        db.product.toArray(),
        db.ingredient.toArray(),
        db.product_stat.toArray(),
        db.location.toArray(),
        db.shopping_list.toArray(),
        db.substitution.toArray(),
      ])
    const timesBought = new Map<string, number>()
    for (const s of stats)
      timesBought.set(s.productId, (timesBought.get(s.productId) ?? 0) + s.timesBought)
    return {
      recipe,
      version,
      stock,
      catalog: {
        products: new Map(products.map((p) => [p.id, p])),
        ingredients: new Map(ingredients.map((i) => [i.id, i])),
        timesBought,
      },
      locations: locations.sort((a, b) => a.sort - b.sort),
      lists: lists.sort((a, b) => a.sort - b.sort),
      substitutions,
    }
  }, [recipeId])
}

export type CookEntry = { session: CookSession; photos: RecipePhoto[] }

/** A recipe's cooks, newest first, with their photos; and photos not tied to a cook. */
export function useCooks(recipeId: string): { cooks: CookEntry[]; loose: RecipePhoto[] } {
  return useLiveQuery(
    async () => {
      const [sessions, photos] = await Promise.all([
        db.cook_session.where('recipeId').equals(recipeId).toArray(),
        db.recipe_photo.where('recipeId').equals(recipeId).toArray(),
      ])
      const live = sessions
        .filter((s) => !s.undoneAt)
        .sort(
          (a, b) => b.cookedOn.localeCompare(a.cookedOn) || b.createdAt.localeCompare(a.createdAt),
        )
      return {
        cooks: live.map((session) => ({
          session,
          photos: photos.filter((p) => p.cookSessionId === session.id),
        })),
        loose: photos.filter((p) => !p.cookSessionId),
      }
    },
    [recipeId],
    { cooks: [], loose: [] },
  )
}
