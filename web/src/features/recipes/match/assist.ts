import { api, unwrap } from '../../../api/client'
import { ApiError } from '../../../api/problem'
import { syncNow } from '../../../offline/sync'
import { toast } from '../../../ui'

/** What the user reads when the assistant can't help; every LLM feature has a manual path. */
function explain(err: unknown, manual: string): string {
  if (err instanceof ApiError) {
    if (err.code === 'llm_unavailable') return `The assistant is unavailable right now. ${manual}`
    if (err.code === 'line_unmatched') return 'Pick what this ingredient is first, then try again.'
    return err.message
  }
  return `You're offline. ${manual}`
}

/** "More ideas": up to 3 new unsaved recipes from what's in stock. Resolves to how many arrived. */
export async function requestIdeas(note?: string): Promise<number | null> {
  try {
    const res = await unwrap(api.POST('/v1/recipes/ideas', { body: note ? { note } : {} }))
    await syncNow()
    toast(
      res.recipeIds.length
        ? {
            title: `${res.recipeIds.length} new ${res.recipeIds.length === 1 ? 'idea' : 'ideas'}`,
            description: 'Save the ones you like; the rest go after a week.',
            tone: 'success',
          }
        : { title: 'No new ideas', description: 'Add a few things to your kitchen and try again.' },
    )
    return res.recipeIds.length
  } catch (err) {
    toast({
      title: 'No ideas this time',
      description: explain(err, 'Import or add a recipe instead.'),
      tone: 'error',
    })
    return null
  }
}

/** "Find a swap" for a missing line. Resolves to how many swaps were saved. */
export async function requestSwap(
  recipeId: string,
  versionId: string,
  lineId: string,
): Promise<number | null> {
  try {
    const res = await unwrap(
      api.POST('/v1/recipes/{id}/substitutes', {
        params: { path: { id: recipeId } },
        body: { lineId, versionId },
      }),
    )
    await syncNow()
    if (res.substitutions.length === 0)
      toast({
        title: 'No swap in your kitchen',
        description: 'Nothing in stock works for this one.',
      })
    return res.substitutions.length
  } catch (err) {
    toast({
      title: "Couldn't find a swap",
      description: explain(err, 'Swap it by hand while you cook.'),
      tone: 'error',
    })
    return null
  }
}
