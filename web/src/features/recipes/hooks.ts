import { useLiveQuery } from 'dexie-react-hooks'
import { db } from '../../offline/db'
import type { Recipe, RecipeVersion } from '../../offline/replica'
import './commands' // registers the optimistic reducers

/** Saved recipes, most recently cooked first, then by title. */
export const useSavedRecipes = (): Recipe[] =>
  useLiveQuery(
    async () =>
      (await db.recipe.filter((r) => r.saved).toArray()).sort(
        (a, b) =>
          (b.lastCookedAt ?? '').localeCompare(a.lastCookedAt ?? '') ||
          a.title.localeCompare(b.title),
      ),
    [],
    [] as Recipe[],
  )

/** undefined while loading, null when the recipe isn't in the replica. */
export const useRecipe = (id: string | undefined): Recipe | null | undefined =>
  useLiveQuery(async () => (id ? ((await db.recipe.get(id)) ?? null) : null), [id])

export const useRecipeVersion = (id: string | null | undefined): RecipeVersion | null | undefined =>
  useLiveQuery(async () => (id ? ((await db.recipe_version.get(id)) ?? null) : null), [id])

/** Every version of a recipe, newest first. */
export const useRecipeVersions = (recipeId: string | undefined): RecipeVersion[] =>
  useLiveQuery(
    async () =>
      recipeId
        ? (await db.recipe_version.where('recipeId').equals(recipeId).toArray()).sort(
            (a, b) => b.n - a.n,
          )
        : [],
    [recipeId],
    [] as RecipeVersion[],
  )
