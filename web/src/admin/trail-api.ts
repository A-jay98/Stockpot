import { keepPreviousData, useInfiniteQuery } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components, operations } from '../api/schema'
import { adminKeys } from './api'

type S = components['schemas']
export type AuditEvent = S['AdminAuditEvent']
export type AccessRecord = S['AdminAccessRecord']

type Query<Op extends keyof operations> = Omit<
  NonNullable<operations[Op]['parameters']['query']>,
  'cursor' | 'limit'
>
export type AuditFilters = Query<'listAdminAudit'>
export type AccessFilters = Query<'listAdminAccess'>

export const trailKeys = {
  audit: (f: AuditFilters) => [...adminKeys.all, 'audit', f] as const,
  access: (f: AccessFilters) => [...adminKeys.all, 'access', f] as const,
}

const PAGE = 50

export function useAuditEvents(filters: AuditFilters, { limit = PAGE, enabled = true } = {}) {
  return useInfiniteQuery({
    queryKey: [...trailKeys.audit(filters), limit],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/v1/admin/audit', { params: { query: { ...filters, cursor: pageParam, limit } } }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    placeholderData: keepPreviousData,
    enabled,
  })
}

export function useAccessLog(filters: AccessFilters) {
  return useInfiniteQuery({
    queryKey: trailKeys.access(filters),
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/v1/admin/access', {
          params: { query: { ...filters, cursor: pageParam, limit: PAGE } },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    placeholderData: keepPreviousData,
  })
}
