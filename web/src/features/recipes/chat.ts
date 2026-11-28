import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useLiveQuery } from 'dexie-react-hooks'
import { v7 } from 'uuid'
import { api, unwrap } from '../../api/client'
import type { components } from '../../api/schema'
import { db } from '../../offline/db'
import { dispatch } from '../../offline/outbox'
import { versionRawLines } from './commands'

export type ChatMessage = components['schemas']['RecipeChatMessage']
export type ChatProposal = components['schemas']['RecipeChatProposal']
type Chat = components['schemas']['RecipeChat']

export const chatKey = (recipeId: string) => ['recipes', recipeId, 'chat'] as const

/** The recipe's chat (REST, online only; not in the replica). */
export function useRecipeChat(recipeId: string, enabled = true) {
  return useQuery({
    queryKey: chatKey(recipeId),
    queryFn: () => unwrap(api.GET('/v1/recipes/{id}/chat', { params: { path: { id: recipeId } } })),
    enabled,
  })
}

const append = (old: Chat | undefined, ms: ChatMessage[]): Chat => ({
  messages: [...(old?.messages ?? []), ...ms],
})

const replace = (old: Chat | undefined, m: ChatMessage): Chat | undefined =>
  old && { messages: old.messages.map((x) => (x.id === m.id ? m : x)) }

export function usePostChat(recipeId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (text: string) =>
      unwrap(
        api.POST('/v1/recipes/{id}/chat', { params: { path: { id: recipeId } }, body: { text } }),
      ),
    onSuccess: (data) =>
      qc.setQueryData<Chat>(chatKey(recipeId), (old) => append(old, data.messages)),
  })
}

export function useDismissProposal(recipeId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (messageId: string) =>
      unwrap(
        api.POST('/v1/recipes/{id}/chat/{messageId}/dismiss', {
          params: { path: { id: recipeId, messageId } },
        }),
      ),
    onSuccess: (m) => qc.setQueryData<Chat>(chatKey(recipeId), (old) => replace(old, m)),
  })
}

/**
 * "Save as vN": applies the proposed changes to the recipe's current version (so edits made since
 * the proposal are kept) and queues recipe.version.create. Returns the command id; the server marks
 * the message saved when it applies the command (see useCommandSettled).
 */
export async function saveProposal(recipeId: string, m: ChatMessage): Promise<string | undefined> {
  const p = m.proposal
  const recipe = await db.recipe.get(recipeId)
  const current = recipe?.currentVersionId
    ? await db.recipe_version.get(recipe.currentVersionId)
    : undefined
  if (!p) return undefined
  if (!current) throw new Error('the recipe is not loaded yet')
  return dispatch('recipe.version.create', {
    recipeId,
    versionId: v7(),
    origin: 'chat',
    parentVersionId: current.id,
    fromMessageId: m.id,
    changeSummary: p.changeSummary,
    title: p.title ?? current.title,
    servings: p.servings ?? current.servings,
    totalMinutes: p.totalMinutes ?? current.totalMinutes,
    ingredients: p.ingredients ?? versionRawLines(current.ingredients),
    steps: p.steps ?? current.steps,
    notes: p.notes === undefined ? current.notes : p.notes || null,
  })
}

/**
 * Where a queued command stands: 'pending' while it waits in the outbox (offline included),
 * 'rejected' if the server refused it, 'done' once it applied (the outbox drops it).
 */
export function useCommandState(commandId: string | undefined): 'pending' | 'rejected' | 'done' {
  const row = useLiveQuery(
    async () =>
      commandId ? ((await db.outbox.where('id').equals(commandId).first()) ?? null) : null,
    [commandId],
  )
  if (!commandId || row === null) return 'done'
  if (row === undefined) return 'pending' // still loading
  return row.status === 'rejected' ? 'rejected' : 'pending'
}

/** Refetches the chat so a saved proposal shows the server's state. */
export function useRefreshChat(recipeId: string) {
  const qc = useQueryClient()
  return () => qc.invalidateQueries({ queryKey: chatKey(recipeId) })
}
