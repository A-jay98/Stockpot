import { api, unwrap } from '../../../api/client'
import { db } from '../../../offline/db'
import { DEFAULT_STAPLES } from './rank'

/** meta key for the household's recipes.assumed_staples, kept for offline ranking. */
const META = 'recipes.staples'

/** The cached staple slugs, or the server default before the first fetch. */
export async function cachedStaples(): Promise<string[]> {
  const row = await db.meta.get(META)
  return Array.isArray(row?.value) ? (row.value as string[]) : DEFAULT_STAPLES
}

/** Fetch the household's staples into the cache; failures keep the last copy. */
export async function refreshStaples(): Promise<void> {
  try {
    const { slugs } = await unwrap(api.GET('/v1/recipes/staples'))
    await db.meta.put({ key: META, value: slugs })
  } catch {
    // offline or signed out: rank with what we have
  }
}
