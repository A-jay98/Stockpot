import { useQuery } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'
import { adminKeys } from './api'

export type AdminFoodRules = components['schemas']['AdminFoodRules']

export const useAdminFoodRules = () =>
  useQuery({
    queryKey: [...adminKeys.all, 'food-rules'],
    queryFn: () => unwrap(api.GET('/v1/admin/food-rules')),
  })
