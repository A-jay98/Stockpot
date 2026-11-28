import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'
import { adminKeys } from './api'

export type AdminRecipes = components['schemas']['AdminRecipes']
export type AdminRecipeImport = components['schemas']['AdminRecipeImport']

const key = () => [...adminKeys.all, 'recipes'] as const

export function useAdminRecipes() {
  return useQuery({
    queryKey: key(),
    queryFn: () => unwrap(api.GET('/v1/admin/recipes')),
    refetchInterval: 15_000,
  })
}

export function useRetryImport() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (importId: string) =>
      unwrap(
        api.POST('/v1/admin/recipes/imports/{importId}/retry', {
          params: { path: { importId } },
        }),
      ),
    onSettled: () => void qc.invalidateQueries({ queryKey: key() }),
  })
}
