import {
  type InfiniteData,
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'

type S = components['schemas']
export type Overview = S['AdminOverview']
export type LogEntry = S['AdminLogEntry']
export type LogLevel = S['AdminLogLevel']
export type LogPage = S['AdminLogPage']
export type Job = S['AdminJob']
export type JobState = S['AdminJobState']
export type Setting = S['AdminSetting']
type SettingList = S['AdminSettingList']

export const adminKeys = {
  all: ['admin'] as const,
  overview: () => [...adminKeys.all, 'overview'] as const,
  logs: (f: LogFilters) => [...adminKeys.all, 'logs', f] as const,
  jobs: (f: JobFilters) => [...adminKeys.all, 'jobs', f] as const,
  settings: () => [...adminKeys.all, 'settings'] as const,
}

/** `poll` refreshes every 15 s (Overview page only; the access guard reads it once). */
export function useOverview({ poll = false } = {}) {
  return useQuery({
    queryKey: adminKeys.overview(),
    queryFn: () => unwrap(api.GET('/v1/admin/overview')),
    refetchInterval: poll ? 15_000 : false,
    staleTime: 5_000,
  })
}

export type LogFilters = {
  level?: LogLevel
  q?: string
  requestId?: string
  since?: string
  until?: string
}

const PAGE = 100
/** Most lines the live tail keeps in memory. */
export const TAIL_CAP = 1000

export function useLogs(filters: LogFilters) {
  return useInfiniteQuery({
    queryKey: adminKeys.logs(filters),
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/v1/admin/logs', {
          params: { query: { ...filters, cursor: pageParam, limit: PAGE } },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    placeholderData: keepPreviousData,
    staleTime: Number.POSITIVE_INFINITY, // the tail keeps it fresh
  })
}

/**
 * Live tail: polls for lines newer than the newest one shown and prepends them to the
 * first page. If more arrived than one page holds, the list is reloaded instead.
 */
export function useLogTail(filters: LogFilters, newestId: number | undefined, enabled: boolean) {
  const qc = useQueryClient()
  return useQuery({
    queryKey: [...adminKeys.logs(filters), 'tail', newestId],
    enabled: enabled && newestId !== undefined && !filters.until,
    refetchInterval: 3_000,
    gcTime: 0,
    queryFn: async () => {
      const page = await unwrap(
        api.GET('/v1/admin/logs', {
          params: { query: { ...filters, after: newestId, limit: PAGE } },
        }),
      )
      const key = adminKeys.logs(filters)
      if (page.nextCursor) {
        await qc.resetQueries({ queryKey: key, exact: true })
      } else if (page.items.length > 0) {
        qc.setQueryData<InfiniteData<LogPage, string | undefined>>(key, (old) => {
          if (!old) return old
          const [first, ...rest] = old.pages
          const items = [...page.items, ...(first?.items ?? [])]
          if (items.length <= TAIL_CAP) {
            return { ...old, pages: [{ ...first, items }, ...rest] }
          }
          // Bounded memory: keep the newest lines only. Older pages no longer
          // follow on, so paging back starts again after pausing the tail.
          return { pages: [{ items: items.slice(0, TAIL_CAP) }], pageParams: [old.pageParams[0]] }
        })
      }
      return page.items.length
    },
  })
}

export type JobFilters = { state?: JobState[]; kind?: string[] }

export function useJobs(filters: JobFilters) {
  return useInfiniteQuery({
    queryKey: adminKeys.jobs(filters),
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/v1/admin/jobs', {
          params: { query: { ...filters, cursor: pageParam, limit: 50 } },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    placeholderData: keepPreviousData,
    refetchInterval: 10_000,
  })
}

export function useJobAction() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, action }: { id: number; action: 'retry' | 'cancel' }) =>
      action === 'retry'
        ? unwrap(api.POST('/v1/admin/jobs/{id}/retry', { params: { path: { id } } }))
        : unwrap(api.POST('/v1/admin/jobs/{id}/cancel', { params: { path: { id } } })),
    onSettled: () => qc.invalidateQueries({ queryKey: [...adminKeys.all, 'jobs'] }),
  })
}

export function useSettings() {
  return useQuery({
    queryKey: adminKeys.settings(),
    queryFn: () => unwrap(api.GET('/v1/admin/settings')),
  })
}

function replaceSetting(old: { items: Setting[] } | undefined, next: Setting) {
  if (!old) return old
  return { items: old.items.map((s) => (s.key === next.key ? next : s)) }
}

export function useSaveSetting() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ key, value }: { key: string; value: unknown }) =>
      unwrap(api.PUT('/v1/admin/settings/{key}', { params: { path: { key } }, body: { value } })),
    onSuccess: (s) =>
      qc.setQueryData<SettingList>(adminKeys.settings(), (old) => replaceSetting(old, s)),
  })
}

export function useResetSetting() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (key: string) =>
      unwrap(api.DELETE('/v1/admin/settings/{key}', { params: { path: { key } } })),
    onSuccess: (s) =>
      qc.setQueryData<SettingList>(adminKeys.settings(), (old) => replaceSetting(old, s)),
  })
}

// --- LLM gateway -------------------------------------------------------------

export type LLMStatus = S['LLMStatus']
export type LLMProviderStatus = S['LLMProviderStatus']
export type LLMUsage = S['LLMUsage']
export type LLMTestResult = S['LLMTestResult']

export const llmKeys = {
  all: [...adminKeys.all, 'llm'] as const,
  status: () => [...llmKeys.all, 'status'] as const,
  usage: (days: number) => [...llmKeys.all, 'usage', days] as const,
}

export function useLLMStatus() {
  return useQuery({
    queryKey: llmKeys.status(),
    queryFn: () => unwrap(api.GET('/v1/admin/llm/status')),
    refetchInterval: 15_000,
  })
}

export function useLLMUsage(days: number) {
  return useQuery({
    queryKey: llmKeys.usage(days),
    queryFn: () => unwrap(api.GET('/v1/admin/llm/usage', { params: { query: { days } } })),
    placeholderData: keepPreviousData,
  })
}

/** Stores (apiKey set) or removes (apiKey empty) a provider's key. */
export function useLLMKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, apiKey }: { id: string; apiKey: string }) =>
      apiKey
        ? unwrap(
            api.PUT('/v1/admin/llm/providers/{id}/key', {
              params: { path: { id } },
              body: { apiKey },
            }),
          )
        : unwrap(api.DELETE('/v1/admin/llm/providers/{id}/key', { params: { path: { id } } })),
    onSettled: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: llmKeys.status() }),
        qc.invalidateQueries({ queryKey: adminKeys.settings() }),
      ]),
  })
}

export function useLLMTest() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { prompt: string; provider?: string }) =>
      unwrap(api.POST('/v1/admin/llm/test', { body })),
    onSettled: () => qc.invalidateQueries({ queryKey: llmKeys.all }),
  })
}
