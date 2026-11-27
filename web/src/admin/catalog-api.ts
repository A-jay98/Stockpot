import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'
import { adminKeys } from './api'

export type AdminCatalog = components['schemas']['AdminCatalog']
export type EnrichRun = components['schemas']['AdminEnrichRun']
export type LLMIngredient = components['schemas']['AdminLLMIngredient']
export type ReviewProduct = components['schemas']['AdminReviewProduct']

const key = () => [...adminKeys.all, 'catalog'] as const

export function useCatalog() {
  return useQuery({
    queryKey: key(),
    queryFn: () => unwrap(api.GET('/v1/admin/catalog')),
    refetchInterval: 15_000,
  })
}

function useCatalogMutation<V, R>(fn: (v: V) => Promise<R>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: key() })
      // A queued run starts within a second or two.
      setTimeout(() => void qc.invalidateQueries({ queryKey: key() }), 2_000)
    },
  })
}

export const useEnrichNow = () =>
  useCatalogMutation(() => unwrap(api.POST('/v1/admin/catalog/enrich/run')))

export const useRetryFailed = () =>
  useCatalogMutation(() => unwrap(api.POST('/v1/admin/catalog/enrich/retry-failed')))

export const usePromoteIngredient = () =>
  useCatalogMutation((id: string) =>
    unwrap(api.POST('/v1/admin/catalog/ingredients/{id}/promote', { params: { path: { id } } })),
  )

export const useMergeIngredient = () =>
  useCatalogMutation(({ id, targetSlug }: { id: string; targetSlug: string }) =>
    unwrap(
      api.POST('/v1/admin/catalog/ingredients/{id}/merge', {
        params: { path: { id } },
        body: { targetSlug },
      }),
    ),
  )

export const useDeleteIngredient = () =>
  useCatalogMutation((id: string) =>
    unwrap(api.DELETE('/v1/admin/catalog/ingredients/{id}', { params: { path: { id } } })),
  )
