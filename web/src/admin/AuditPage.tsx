import { ChevronDown, ChevronRight } from 'lucide-react'
import { Fragment, useState } from 'react'
import { Link } from 'react-router'
import { Button, cn } from '../ui'
import { type AuditEvent, type AuditFilters, useAuditEvents } from './trail-api'
import {
  AdminPage,
  compactField,
  ErrorNote,
  FilterBar,
  formatTime,
  LoadMore,
  Table,
  toISO,
  toLocal,
  useDebounced,
  useUrlState,
} from './ui'
import { useUsers } from './users-api'

const keys = ['actor', 'action', 'targetType', 'targetId', 'since', 'until'] as const
const actionPattern = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$/

export function AuditPage() {
  const [url, setUrl] = useUrlState(keys)
  const [action, setAction] = useDebounced(url.action, (v) => setUrl({ action: v }))
  const [targetId, setTargetId] = useDebounced(url.targetId, (v) => setUrl({ targetId: v }))
  const users = useUsers()
  const filters: AuditFilters = {
    actorId: url.actor || undefined,
    // A half-typed action is not sent; the server would reject it.
    action: actionPattern.test(url.action) ? url.action : undefined,
    targetType: url.targetType || undefined,
    targetId: url.targetId || undefined,
    since: url.since || undefined,
    until: url.until || undefined,
  }
  const events = useAuditEvents(filters)
  const rows = events.data?.pages.flatMap((p) => p.items) ?? []

  return (
    <AdminPage
      title="Audit log"
      intro="Every security event and admin change, newest first, with what it looked like before and after."
    >
      <FilterBar>
        <select
          aria-label="Actor"
          className={compactField}
          value={url.actor}
          onChange={(e) => setUrl({ actor: e.target.value })}
        >
          <option value="">Anyone</option>
          {users.data?.map((u) => (
            <option key={u.id} value={u.id}>
              {u.displayName}
            </option>
          ))}
        </select>
        <input
          type="search"
          aria-label="Action"
          placeholder="Action, e.g. user or setting.update"
          className={cn(compactField, 'min-w-0 flex-1 basis-56')}
          value={action}
          onChange={(e) => setAction(e.target.value.trim().toLowerCase())}
        />
        <select
          aria-label="Target type"
          className={compactField}
          value={url.targetType}
          onChange={(e) => setUrl({ targetType: e.target.value })}
        >
          <option value="">Any target</option>
          {['user', 'session', 'invite', 'household', 'setting', 'job'].map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        <input
          type="search"
          aria-label="Target id"
          placeholder="Target id"
          className={cn(compactField, 'min-w-0 basis-40')}
          value={targetId}
          onChange={(e) => setTargetId(e.target.value.trim())}
        />
        <TimeRange since={url.since} until={url.until} onChange={setUrl} />
      </FilterBar>

      {events.error && <ErrorNote error={events.error} />}
      <AuditTable rows={rows} loading={events.isPending} />
      <LoadMore
        hasMore={events.hasNextPage}
        loading={events.isFetchingNextPage}
        onClick={() => void events.fetchNextPage()}
      />
    </AdminPage>
  )
}

export function TimeRange({
  since,
  until,
  onChange,
}: {
  since: string
  until: string
  onChange: (patch: { since?: string; until?: string }) => void
}) {
  return (
    <>
      <input
        type="datetime-local"
        aria-label="From"
        className={compactField}
        value={toLocal(since)}
        onChange={(e) => onChange({ since: toISO(e.target.value) })}
      />
      <input
        type="datetime-local"
        aria-label="To"
        className={compactField}
        value={toLocal(until)}
        onChange={(e) => onChange({ until: toISO(e.target.value) })}
      />
    </>
  )
}

/** Audit rows with an expandable before/after. Also used for a setting's history. */
export function AuditTable({
  rows,
  loading,
  showTarget = true,
}: {
  rows: AuditEvent[]
  loading: boolean
  showTarget?: boolean
}) {
  const [open, setOpen] = useState<number>()
  const cols = showTarget ? 6 : 5
  return (
    <Table aria-label="Audit events">
      <thead>
        <tr>
          <th>
            <span className="sr-only">Details</span>
          </th>
          <th>Time</th>
          <th>Actor</th>
          <th>Action</th>
          {showTarget && <th>Target</th>}
          <th>IP</th>
        </tr>
      </thead>
      <tbody>
        {rows.length === 0 && !loading && (
          <tr>
            <td colSpan={cols} className="text-ink-muted">
              No events match.
            </td>
          </tr>
        )}
        {rows.map((e) => {
          const expanded = open === e.id
          return (
            <Fragment key={e.id}>
              <tr>
                <td className="w-10">
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-expanded={expanded}
                    aria-label={`Details of event ${e.id}`}
                    onClick={() => setOpen(expanded ? undefined : e.id)}
                  >
                    {expanded ? <ChevronDown aria-hidden /> : <ChevronRight aria-hidden />}
                  </Button>
                </td>
                <td className="whitespace-nowrap">{formatTime(e.time)}</td>
                <td>{e.actorName || (e.actorId ? 'Deleted user' : 'System')}</td>
                <td className="font-mono text-[12.5px]">{e.action}</td>
                {showTarget && (
                  <td className="max-w-[32ch] truncate font-mono text-[12.5px]">
                    {e.targetType}
                    {e.targetId && <span className="text-ink-muted"> {e.targetId}</span>}
                  </td>
                )}
                <td className="font-mono text-[12.5px] text-ink-muted">{e.ip || '—'}</td>
              </tr>
              {expanded && (
                <tr>
                  <td colSpan={cols} className="bg-surface-2">
                    <EventDetail event={e} />
                  </td>
                </tr>
              )}
            </Fragment>
          )
        })}
      </tbody>
    </Table>
  )
}

function EventDetail({ event: e }: { event: AuditEvent }) {
  return (
    <div className="flex flex-col gap-3">
      <div className="grid gap-3 md:grid-cols-2">
        <Snapshot label="Before" value={e.before} />
        <Snapshot label="After" value={e.after} />
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-muted">
        {e.requestId && (
          <span>
            Request{' '}
            <Link
              className="font-mono"
              to={`/admin/logs?requestId=${encodeURIComponent(e.requestId)}&live=off`}
            >
              {e.requestId}
            </Link>
          </span>
        )}
        {e.sessionId && (
          <span>
            Session <code className="font-mono">{e.sessionId}</code>
          </span>
        )}
        {e.householdId && (
          <span>
            Household <code className="font-mono">{e.householdId}</code>
          </span>
        )}
      </div>
    </div>
  )
}

function Snapshot({ label, value }: { label: string; value: unknown }) {
  return (
    <section aria-label={label} className="flex min-w-0 flex-col gap-1">
      <span className="t-label text-ink-muted">{label}</span>
      {value === undefined || value === null ? (
        <span className="text-[13px] text-ink-muted">None</span>
      ) : (
        <pre className="m-0 max-h-64 overflow-auto rounded-md bg-surface p-2.5 font-mono text-[12.5px] whitespace-pre-wrap break-all">
          {JSON.stringify(value, null, 2)}
        </pre>
      )}
    </section>
  )
}
