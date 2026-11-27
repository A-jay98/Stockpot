import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'
import { adminKeys } from './api'

export type Backups = components['schemas']['AdminBackups']
export type BackupRun = components['schemas']['AdminBackupRun']

const key = () => [...adminKeys.all, 'backups'] as const

/** Polls faster while a run is in progress. */
export function useBackups() {
  return useQuery({
    queryKey: key(),
    queryFn: () => unwrap(api.GET('/v1/admin/backups')),
    refetchInterval: (q) =>
      q.state.data?.runs.some((r) => r.status === 'running') ? 3_000 : 15_000,
  })
}

export function useBackupNow() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(api.POST('/v1/admin/backups/run')),
    onSettled: () => {
      // The job starts within a second or two; check again shortly.
      void qc.invalidateQueries({ queryKey: key() })
      setTimeout(() => void qc.invalidateQueries({ queryKey: key() }), 2_000)
    },
  })
}

export function useBackupChannelTest() {
  return useMutation({ mutationFn: () => unwrap(api.POST('/v1/admin/backups/test')) })
}
