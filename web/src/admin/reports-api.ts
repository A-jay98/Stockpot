import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'
import { adminKeys } from './api'

export type AdminReports = components['schemas']['AdminReports']
export type AdminReport = components['schemas']['AdminReport']
export type ReportStatus = components['schemas']['ReportStatus']
export type ResolveAction = components['schemas']['ReportResolveInput']['action']
export type CatalogDefaults = components['schemas']['AdminCatalogDefaults']

const reportsKey = (status: string) => [...adminKeys.all, 'reports', status] as const
const defaultsKey = (q: string) => [...adminKeys.all, 'defaults', q] as const

export function useReports(status: ReportStatus | '') {
  return useQuery({
    queryKey: reportsKey(status),
    queryFn: () =>
      unwrap(api.GET('/v1/admin/reports', { params: { query: status ? { status } : {} } })),
    refetchInterval: 15_000,
    placeholderData: keepPreviousData,
  })
}

function useInvalidate() {
  const qc = useQueryClient()
  return () => {
    void qc.invalidateQueries({ queryKey: [...adminKeys.all, 'reports'] })
    void qc.invalidateQueries({ queryKey: [...adminKeys.all, 'defaults'] })
  }
}

export function useReviewNow() {
  const inv = useInvalidate()
  return useMutation({
    mutationFn: () => unwrap(api.POST('/v1/admin/reports/review/run')),
    onSettled: () => {
      inv()
      // A queued run starts within a second or two.
      setTimeout(inv, 3_000)
    },
  })
}

export function useResolveReport() {
  const inv = useInvalidate()
  return useMutation({
    mutationFn: ({
      id,
      action,
      value,
      note,
    }: {
      id: string
      action: ResolveAction
      value?: string
      note?: string
    }) =>
      unwrap(
        api.POST('/v1/admin/reports/{id}/resolve', {
          params: { path: { id } },
          body: { action, ...(value ? { value } : {}), ...(note ? { note } : {}) },
        }),
      ),
    onSettled: inv,
  })
}

export function useDefaults(q: string) {
  return useQuery({
    queryKey: defaultsKey(q),
    queryFn: () =>
      unwrap(api.GET('/v1/admin/catalog/defaults', { params: { query: q ? { q } : {} } })),
    placeholderData: keepPreviousData,
  })
}

export function useSetDefaultUnit() {
  const inv = useInvalidate()
  return useMutation({
    mutationFn: ({ id, unit }: { id: string; unit: string | null }) =>
      unwrap(
        api.PUT('/v1/admin/catalog/ingredients/{id}/default-unit', {
          params: { path: { id } },
          body: { unit },
        }),
      ),
    onSettled: inv,
  })
}
