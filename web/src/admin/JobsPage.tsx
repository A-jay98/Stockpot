import { RotateCcw, X } from 'lucide-react'
import { ApiError } from '../api/problem'
import { Badge, type BadgeProps, Button, cn, toast } from '../ui'
import { type Job, type JobState, useJobAction, useJobs } from './api'
import {
  AdminPage,
  compactField,
  ErrorNote,
  FilterBar,
  formatTime,
  LoadMore,
  Table,
  useUrlState,
} from './ui'

const states: JobState[] = [
  'available',
  'scheduled',
  'running',
  'retryable',
  'completed',
  'discarded',
  'cancelled',
  'pending',
]

const stateTone: Record<JobState, BadgeProps['tone']> = {
  available: 'brand',
  scheduled: 'neutral',
  pending: 'neutral',
  running: 'brand',
  retryable: 'soon',
  completed: 'fresh',
  discarded: 'expired',
  cancelled: 'neutral',
}

// River's rules: finished or waiting jobs can be retried; anything not finished can be cancelled.
const canRetry = (s: JobState) => s !== 'running'
const canCancel = (s: JobState) => !['completed', 'discarded', 'cancelled'].includes(s)

const keys = ['state', 'kind'] as const

export function JobsPage() {
  const [url, setUrl] = useUrlState(keys)
  const filters = {
    state: url.state ? (url.state.split(',') as JobState[]) : undefined,
    kind: url.kind
      ? url.kind
          .split(',')
          .map((k) => k.trim())
          .filter(Boolean)
      : undefined,
  }
  const jobs = useJobs(filters)
  const action = useJobAction()
  const rows = jobs.data?.pages.flatMap((p) => p.items) ?? []

  const run = (job: Job, kind: 'retry' | 'cancel') =>
    action.mutate(
      { id: job.id, action: kind },
      {
        onSuccess: (j) =>
          toast({
            title: `Job ${j.id} ${kind === 'retry' ? 'queued to run' : 'cancelled'}`,
            tone: 'success',
          }),
        onError: (e) =>
          toast({
            title: `Could not ${kind} job ${job.id}`,
            description: e instanceof ApiError ? e.message : undefined,
            tone: 'error',
          }),
      },
    )

  return (
    <AdminPage
      title="Jobs"
      intro="Background work: retention sweeps now, alerts, imports and OCR later. Refreshes every 10 s."
    >
      <FilterBar>
        <select
          aria-label="State"
          className={compactField}
          value={url.state}
          onChange={(e) => setUrl({ state: e.target.value })}
        >
          <option value="">All states</option>
          <option value="available,scheduled,running,retryable,pending">Waiting or running</option>
          {states.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <input
          type="search"
          aria-label="Kind"
          placeholder="Kind, e.g. retention"
          className={cn(compactField, 'min-w-0 flex-1 basis-48')}
          key={url.kind}
          defaultValue={url.kind}
          onKeyDown={(e) => {
            if (e.key === 'Enter') setUrl({ kind: e.currentTarget.value })
          }}
          onBlur={(e) => setUrl({ kind: e.currentTarget.value })}
        />
      </FilterBar>

      {jobs.error && <ErrorNote error={jobs.error} />}
      <Table aria-label="Jobs">
        <thead>
          <tr>
            <th>Id</th>
            <th>Kind</th>
            <th>State</th>
            <th>Attempts</th>
            <th>Scheduled</th>
            <th>Last error</th>
            <th>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && !jobs.isPending && (
            <tr>
              <td colSpan={7} className="text-ink-muted">
                No jobs match.
              </td>
            </tr>
          )}
          {rows.map((j) => {
            const lastError = j.errors.at(-1)
            return (
              <tr key={j.id}>
                <td className="tabular font-mono text-[12.5px]">{j.id}</td>
                <td className="font-mono text-[12.5px]">{j.kind}</td>
                <td>
                  <Badge tone={stateTone[j.state]}>{j.state}</Badge>
                </td>
                <td className="tabular">
                  {j.attempt} / {j.maxAttempts}
                </td>
                <td className="whitespace-nowrap">{formatTime(j.finalizedAt ?? j.scheduledAt)}</td>
                <td className="max-w-[28ch] truncate text-ink-muted" title={lastError?.error}>
                  {lastError?.error ?? ''}
                </td>
                <td className="whitespace-nowrap text-right">
                  {canRetry(j.state) && (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={action.isPending}
                      onClick={() => run(j, 'retry')}
                      aria-label={`Retry job ${j.id}`}
                    >
                      <RotateCcw aria-hidden />
                      Retry
                    </Button>
                  )}
                  {canCancel(j.state) && (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={action.isPending}
                      onClick={() => run(j, 'cancel')}
                      aria-label={`Cancel job ${j.id}`}
                    >
                      <X aria-hidden />
                      Cancel
                    </Button>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </Table>
      <LoadMore
        hasMore={jobs.hasNextPage}
        loading={jobs.isFetchingNextPage}
        onClick={() => void jobs.fetchNextPage()}
      />
    </AdminPage>
  )
}
