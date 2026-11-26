import { Link } from 'react-router'
import { Badge, type BadgeProps, cn } from '../ui'
import { TimeRange } from './AuditPage'
import { type AccessFilters, useAccessLog } from './trail-api'
import {
  AdminPage,
  compactField,
  ErrorNote,
  FilterBar,
  formatTime,
  LoadMore,
  number,
  Table,
  useDebounced,
  useUrlState,
} from './ui'
import { useUsers } from './users-api'

const keys = ['user', 'method', 'route', 'status', 'since', 'until'] as const
const methods = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'] as const
const classes = ['2xx', '3xx', '4xx', '5xx'] as const

const oneOf = <T extends string>(list: readonly T[], v: string) =>
  (list as readonly string[]).includes(v) ? (v as T) : undefined

function statusTone(s: number): BadgeProps['tone'] {
  if (s >= 500) return 'expired'
  if (s >= 400) return 'soon'
  if (s >= 300) return 'neutral'
  return 'fresh'
}

export function AccessPage() {
  const [url, setUrl] = useUrlState(keys)
  const [route, setRoute] = useDebounced(url.route, (v) => setUrl({ route: v }))
  const users = useUsers()
  const filters: AccessFilters = {
    userId: url.user || undefined,
    method: oneOf(methods, url.method),
    route: url.route || undefined,
    status: oneOf(classes, url.status),
    since: url.since || undefined,
    until: url.until || undefined,
  }
  const log = useAccessLog(filters)
  const rows = log.data?.pages.flatMap((p) => p.items) ?? []

  return (
    <AdminPage title="Access log" intro="Every API request served, newest first.">
      <FilterBar>
        <select
          aria-label="User"
          className={compactField}
          value={url.user}
          onChange={(e) => setUrl({ user: e.target.value })}
        >
          <option value="">Anyone</option>
          {users.data?.map((u) => (
            <option key={u.id} value={u.id}>
              {u.displayName}
            </option>
          ))}
        </select>
        <select
          aria-label="Method"
          className={compactField}
          value={url.method}
          onChange={(e) => setUrl({ method: e.target.value })}
        >
          <option value="">Any method</option>
          {methods.map((m) => (
            <option key={m}>{m}</option>
          ))}
        </select>
        <select
          aria-label="Status"
          className={compactField}
          value={url.status}
          onChange={(e) => setUrl({ status: e.target.value })}
        >
          <option value="">Any status</option>
          {classes.map((c) => (
            <option key={c}>{c}</option>
          ))}
        </select>
        <input
          type="search"
          aria-label="Route"
          placeholder="Route, e.g. /v1/admin"
          className={cn(compactField, 'min-w-0 flex-1 basis-48')}
          value={route}
          onChange={(e) => setRoute(e.target.value)}
        />
        <TimeRange since={url.since} until={url.until} onChange={setUrl} />
      </FilterBar>

      {log.error && <ErrorNote error={log.error} />}
      <Table aria-label="Requests">
        <thead>
          <tr>
            <th>Time</th>
            <th>User</th>
            <th>Request</th>
            <th>Status</th>
            <th>Duration</th>
            <th>IP</th>
            <th>Request id</th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && !log.isPending && (
            <tr>
              <td colSpan={7} className="text-ink-muted">
                No requests match.
              </td>
            </tr>
          )}
          {rows.map((r) => (
            <tr key={r.id}>
              <td className="whitespace-nowrap">{formatTime(r.time)}</td>
              <td>{r.userName || (r.userId ? 'Deleted user' : '—')}</td>
              <td className="max-w-[44ch] truncate font-mono text-[12.5px]" title={r.path}>
                <span className="font-semibold">{r.method}</span> {r.route || r.path}
              </td>
              <td>
                <Badge tone={statusTone(r.status)}>{r.status}</Badge>
              </td>
              <td className="tabular whitespace-nowrap">{number.format(r.durationMs)} ms</td>
              <td className="font-mono text-[12.5px] text-ink-muted">{r.ip || '—'}</td>
              <td className="font-mono text-[12.5px]">
                <Link
                  to={`/admin/logs?requestId=${encodeURIComponent(r.requestId)}&live=off`}
                  title="Show this request's log lines"
                >
                  {r.requestId.slice(0, 12)}
                </Link>
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      <LoadMore
        hasMore={log.hasNextPage}
        loading={log.isFetchingNextPage}
        onClick={() => void log.fetchNextPage()}
      />
    </AdminPage>
  )
}
