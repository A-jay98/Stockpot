import { Pause, Play } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Button, cn, Segmented } from '../ui'
import { type LogEntry, type LogFilters, type LogLevel, useLogs, useLogTail } from './api'
import { AdminPage, compactField, ErrorNote, FilterBar, LoadMore, useUrlState } from './ui'

const levelOptions: { value: LogLevel; label: string }[] = [
  { value: 'debug', label: 'All' },
  { value: 'info', label: 'Info+' },
  { value: 'warn', label: 'Warnings+' },
  { value: 'error', label: 'Errors' },
]

const levelStyle: Record<LogLevel, string> = {
  debug: 'text-[#7D8A9A]',
  info: 'text-[#8AA6F5]',
  warn: 'text-[#F0B254]',
  error: 'text-[#F48A82]',
}

const keys = ['level', 'q', 'requestId', 'since', 'until', 'live'] as const

/** `datetime-local` value (local time, no zone) ⇄ RFC 3339. */
const toISO = (local: string) => (local ? new Date(local).toISOString() : '')
const toLocal = (iso: string) => {
  if (!iso) return ''
  const d = new Date(iso)
  return new Date(d.getTime() - d.getTimezoneOffset() * 60_000).toISOString().slice(0, 16)
}

/** Text input that writes to the URL after the user pauses typing. */
function useDebounced(value: string, commit: (v: string) => void, ms = 300) {
  const [draft, setDraft] = useState(value)
  useEffect(() => setDraft(value), [value])
  useEffect(() => {
    if (draft === value) return
    const t = setTimeout(() => commit(draft), ms)
    return () => clearTimeout(t)
  }, [draft, value, commit, ms])
  return [draft, setDraft] as const
}

export function LogsPage() {
  const [url, setUrl] = useUrlState(keys)
  const filters: LogFilters = {
    level: (url.level || 'info') as LogLevel,
    q: url.q || undefined,
    requestId: url.requestId || undefined,
    since: url.since || undefined,
    until: url.until || undefined,
  }
  const live = url.live !== 'off' && !filters.until
  const logs = useLogs(filters)
  const lines = logs.data?.pages.flatMap((p) => p.items) ?? []
  useLogTail(filters, lines[0]?.id ?? (logs.isSuccess ? 0 : undefined), live)

  const [q, setQ] = useDebounced(url.q, (v) => setUrl({ q: v }))
  const [rid, setRid] = useDebounced(url.requestId, (v) => setUrl({ requestId: v }))

  return (
    <AdminPage
      title="System logs"
      intro="Server log lines, newest first. Same lines as journalctl, no SSH."
      actions={
        <Button
          variant="secondary"
          size="sm"
          disabled={!!filters.until}
          aria-pressed={live}
          onClick={() => setUrl({ live: live ? 'off' : '' })}
        >
          {live ? <Pause aria-hidden /> : <Play aria-hidden />}
          {live ? 'Live' : 'Paused'}
        </Button>
      }
    >
      <FilterBar>
        <Segmented
          aria-label="Level"
          className="w-full sm:w-auto"
          value={filters.level ?? 'info'}
          onValueChange={(v) => setUrl({ level: v === 'info' ? '' : v })}
          options={levelOptions}
        />
        <input
          type="search"
          aria-label="Search text"
          placeholder="Search message or fields"
          className={cn(compactField, 'min-w-0 flex-1 basis-48')}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <input
          type="search"
          aria-label="Request id"
          placeholder="Request id"
          className={cn(compactField, 'w-full font-mono sm:w-64')}
          value={rid}
          onChange={(e) => setRid(e.target.value.trim())}
        />
        <label className="flex items-center gap-1.5 text-[13px] text-ink-muted">
          From
          <input
            type="datetime-local"
            className={compactField}
            value={toLocal(url.since)}
            onChange={(e) => setUrl({ since: toISO(e.target.value) })}
          />
        </label>
        <label className="flex items-center gap-1.5 text-[13px] text-ink-muted">
          To
          <input
            type="datetime-local"
            className={compactField}
            value={toLocal(url.until)}
            onChange={(e) => setUrl({ until: toISO(e.target.value) })}
          />
        </label>
      </FilterBar>

      {logs.error && <ErrorNote error={logs.error} />}
      <div
        role="log"
        aria-label="Log lines"
        aria-busy={logs.isFetching}
        className="overflow-x-auto rounded-lg bg-[#0D1218] px-4 py-3.5 font-mono text-[12.5px] leading-5 text-[#C9D3DF]"
      >
        {lines.length === 0 && !logs.isPending && (
          <p className="m-0 text-[#7D8A9A]">No lines match.</p>
        )}
        {lines.map((l) => (
          <LogLine key={l.id} line={l} onRequest={(id) => setUrl({ requestId: id })} />
        ))}
      </div>
      <LoadMore
        hasMore={logs.hasNextPage}
        loading={logs.isFetchingNextPage}
        onClick={() => void logs.fetchNextPage()}
      />
    </AdminPage>
  )
}

const time = new Intl.DateTimeFormat(undefined, {
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
})

function LogLine({ line, onRequest }: { line: LogEntry; onRequest: (id: string) => void }) {
  const fields = Object.entries(line.attrs)
  const summary = fields
    .slice(0, 6)
    .map(([k, v]) => `${k}=${typeof v === 'string' ? v : JSON.stringify(v)}`)
    .join(' ')
  return (
    <details className="group whitespace-pre">
      <summary className="cursor-pointer list-none hover:bg-white/5 [&::-webkit-details-marker]:hidden">
        <span className="text-[#7D8A9A]">{time.format(new Date(line.time))}</span>{' '}
        <span className={levelStyle[line.level]}>{line.level.toUpperCase().padEnd(5)}</span>{' '}
        {line.msg} <span className="text-[#7D8A9A]">{summary}</span>
      </summary>
      <div className="my-1 ml-4 border-l border-white/15 pl-3 whitespace-pre-wrap">
        {line.requestId && (
          <div>
            request_id={' '}
            <button
              type="button"
              className="border-0 bg-transparent p-0 font-mono text-[#8AA6F5] underline"
              onClick={() => line.requestId && onRequest(line.requestId)}
            >
              {line.requestId}
            </button>
          </div>
        )}
        {line.userId && <div>user_id={line.userId}</div>}
        {fields.length > 0 && <div>{JSON.stringify(line.attrs, null, 2)}</div>}
      </div>
    </details>
  )
}
