import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { v7 } from 'uuid'
import { api, unwrap } from '../../api/client'
import type { components } from '../../api/schema'
import { dispatch } from '../../offline/outbox'

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
 * "Save as vN": queues recipe.version.create with the proposal (a full snapshot based on the
 * message's version, so it works even if the recipe moved on). The server marks the message saved
 * when the command applies; until the next fetch the message shows as saved here.
 */
export function useSaveProposal(recipeId: string) {
  const qc = useQueryClient()
  return async (m: ChatMessage) => {
    const p = m.proposal
    if (!p) return
    const versionId = v7()
    await dispatch('recipe.version.create', {
      recipeId,
      versionId,
      origin: 'chat',
      parentVersionId: m.versionId,
      fromMessageId: m.id,
      changeSummary: p.changeSummary,
      title: p.title,
      servings: p.servings,
      totalMinutes: p.totalMinutes,
      ingredients: p.ingredients,
      steps: p.steps,
      notes: p.notes,
    })
    qc.setQueryData<Chat>(chatKey(recipeId), (old) =>
      replace(old, { ...m, proposalState: 'saved', savedVersionId: versionId }),
    )
  }
}
