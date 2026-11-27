import { useQuery } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'
import { adminKeys } from './api'

export type AdminTelegram = components['schemas']['AdminTelegram']

export function useAdminTelegram() {
  return useQuery({
    queryKey: [...adminKeys.all, 'telegram'] as const,
    queryFn: () => unwrap(api.GET('/v1/admin/telegram')),
    refetchInterval: 15_000,
  })
}
