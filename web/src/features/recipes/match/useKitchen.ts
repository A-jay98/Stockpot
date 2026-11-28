import { useLiveQuery } from 'dexie-react-hooks'
import { useEffect } from 'react'
import { db } from '../../../offline/db'
import type { RecipeVersion } from '../../../offline/replica'
import { today } from '../../inventory/dates'
import {
  indexKitchen,
  isCandidate,
  type Kitchen,
  type RankedRecipe,
  rankRecipe,
  rankRecipes,
} from './rank'
import { cachedStaples, refreshStaples } from './staples'

async function loadKitchen(): Promise<Kitchen> {
  const [ingredients, products, stock, substitutions, stapleSlugs] = await Promise.all([
    db.ingredient.toArray(),
    db.product.toArray(),
    db.stock_item.toArray(),
    db.substitution.toArray(),
    cachedStaples(),
  ])
  return indexKitchen({ today: today(), ingredients, products, stock, substitutions, stapleSlugs })
}

/** Saved recipes and live ideas ranked by what's in the kitchen; undefined while loading. */
export function useRankedRecipes(): RankedRecipe[] | undefined {
  useEffect(() => void refreshStaples(), []) // the household may have changed its staples
  return useLiveQuery(async () => {
    const now = new Date().toISOString()
    const [k, recipes] = await Promise.all([
      loadKitchen(),
      db.recipe.filter((r) => isCandidate(r, now)).toArray(),
    ])
    const ids = recipes.flatMap((r) => (r.currentVersionId ? [r.currentVersionId] : []))
    const versions = new Map<string, RecipeVersion>()
    for (const v of await db.recipe_version.bulkGet(ids)) if (v) versions.set(v.id, v)
    return rankRecipes(k, recipes, versions)
  }, [])
}

/** One recipe version against the kitchen (any recipe, saved or not). */
export function useRecipeMatch(
  recipeId: string | undefined,
  versionId: string | undefined,
): { kitchen: Kitchen; ranked: RankedRecipe } | null | undefined {
  useEffect(() => void refreshStaples(), [])
  return useLiveQuery(async () => {
    if (!recipeId || !versionId) return null
    const [k, recipe, version] = await Promise.all([
      loadKitchen(),
      db.recipe.get(recipeId),
      db.recipe_version.get(versionId),
    ])
    if (!recipe || !version) return null
    return { kitchen: k, ranked: rankRecipe(k, recipe, version) }
  }, [recipeId, versionId])
}
