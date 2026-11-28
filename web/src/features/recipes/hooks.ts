import { useLiveQuery } from 'dexie-react-hooks'
import { db } from '../../offline/db'
import type { Recipe, RecipeOriginal, RecipeVersion } from '../../offline/replica'
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

/** Version `n` of a recipe (the /recipes/:id/v/:n route). */
export const useVersionByN = (
  recipeId: string | undefined,
  n: number,
): RecipeVersion | null | undefined =>
  useLiveQuery(
    async () =>
      recipeId && Number.isInteger(n)
        ? ((await db.recipe_version
            .where('recipeId')
            .equals(recipeId)
            .filter((v) => v.n === n)
            .first()) ?? null)
        : null,
    [recipeId, n],
  )

/**
 * The untranslated text of a version: its own `original`, else the one held by the version it
 * points at (later versions of a translated import carry `originalVersionId`).
 */
export const useOriginal = (version: RecipeVersion | null | undefined): RecipeOriginal | null =>
  useLiveQuery(
    async () => {
      if (!version) return null
      if (version.original) return version.original
      if (!version.originalVersionId) return null
      return (await db.recipe_version.get(version.originalVersionId))?.original ?? null
    },
    [version?.id, version?.original, version?.originalVersionId],
    null,
  )
