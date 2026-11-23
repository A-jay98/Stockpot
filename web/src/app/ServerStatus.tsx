import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'

/** Release tag of the running server, from /api/health (also used as a reachability hint). */
export function useServerHealth() {
  return useQuery({
    queryKey: ['health'],
    queryFn: async () => {
      // 503 still carries a Health body (degraded); only a network failure means unreachable.
      const { data, error } = await api.GET('/health')
      const health = data ?? error
      if (!health || typeof health.version !== 'string') throw new Error('no health body')
      return health
    },
    staleTime: 60_000,
    retry: false,
  })
}

export function ServerStatus() {
  const { data, isPending, isError } = useServerHealth()
  return (
    <span role="status" className="tabular text-[13px] text-ink-muted">
      {isPending && 'Connecting…'}
      {isError && 'Server unreachable'}
      {data && `Server ${data.version}${data.status === 'degraded' ? ' (degraded)' : ''}`}
    </span>
  )
}
