import type { LucideIcon } from 'lucide-react'
import { type ComponentProps, type ReactNode, useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router'
import { ApiError } from '../api/problem'
import { Page } from '../app/Page'
import { Button, cn } from '../ui'

/** Admin screen: title, one-line intro, content. */
export function AdminPage({
  title,
  intro,
  actions,
  children,
}: {
  title: string
  intro?: ReactNode
  actions?: ReactNode
  children: ReactNode
}) {
  return (
    <Page title={title} actions={actions}>
      {intro && <p className="t-body-sm m-0 -mt-2 text-ink-muted">{intro}</p>}
      {children}
    </Page>
  )
}

export function Tile({
  icon: Icon,
  label,
  value,
  detail,
  tone,
}: {
  icon: LucideIcon
  label: string
  value: ReactNode
  detail?: ReactNode
  tone?: 'fresh' | 'soon' | 'expired'
}) {
  return (
    <section
      aria-label={label}
      className="flex min-w-0 flex-col gap-1.5 rounded-lg bg-surface p-3.5 shadow-card"
    >
      <span className="flex items-center gap-1.5 text-[12.5px] font-semibold text-ink-muted [&_svg]:size-[15px]">
        <Icon aria-hidden />
        {label}
      </span>
      <span
        className={cn(
          'tabular font-display text-2xl leading-7',
          tone === 'fresh' && 'text-fresh',
          tone === 'soon' && 'text-soon',
          tone === 'expired' && 'text-expired',
        )}
      >
        {value}
      </span>
      {detail && <span className="text-[12.5px] text-ink-muted">{detail}</span>}
    </section>
  )
}

/** Scrolls sideways on narrow screens instead of squashing columns. */
export function Table({ className, ...props }: ComponentProps<'table'>) {
  return (
    <div className="overflow-x-auto rounded-lg bg-surface shadow-card">
      <table
        className={cn(
          'w-full border-collapse text-[13.5px]',
          '[&_th]:border-b [&_th]:border-border [&_th]:px-3.5 [&_th]:py-2.5 [&_th]:text-left [&_th]:text-xs [&_th]:font-semibold [&_th]:tracking-[.04em] [&_th]:whitespace-nowrap [&_th]:text-ink-muted [&_th]:uppercase',
          '[&_td]:border-b [&_td]:border-border [&_td]:px-3.5 [&_td]:py-2.5 [&_td]:align-middle [&_tr:last-child_td]:border-b-0',
          className,
        )}
        {...props}
      />
    </div>
  )
}

export function FilterBar({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap items-end gap-2">{children}</div>
}

export const compactField =
  'h-9 rounded-md border border-border bg-surface px-2.5 text-[14px] text-ink outline-none focus-visible:border-brand'

export function LoadMore({
  hasMore,
  loading,
  onClick,
}: {
  hasMore: boolean
  loading: boolean
  onClick: () => void
}) {
  if (!hasMore) return null
  return (
    <Button
      variant="secondary"
      size="sm"
      className="self-center"
      disabled={loading}
      onClick={onClick}
    >
      {loading ? 'Loading…' : 'Load older'}
    </Button>
  )
}

export function ErrorNote({ error }: { error: unknown }) {
  const text =
    error instanceof ApiError ? error.message : 'Could not reach the server. Retrying shortly.'
  return (
    <p role="alert" className="m-0 rounded-md bg-expired-bg px-3.5 py-2.5 text-[14px] text-expired">
      {text}
    </p>
  )
}

/**
 * Filter state kept in the URL query, so a filtered view can be reloaded, bookmarked and
 * shared. Empty values are removed from the URL.
 */
export function useUrlState<K extends string>(keys: readonly K[]) {
  const [params, setParams] = useSearchParams()
  const values = useMemo(
    () => Object.fromEntries(keys.map((k) => [k, params.get(k) ?? ''])) as Record<K, string>,
    [params, keys],
  )
  const set = useCallback(
    (patch: Partial<Record<K, string>>) =>
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          for (const [k, v] of Object.entries(patch) as [K, string | undefined][]) {
            if (v) next.set(k, v)
            else next.delete(k)
          }
          return next
        },
        { replace: true },
      ),
    [setParams],
  )
  return [values, set] as const
}

const dateTime = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
})

export function formatTime(iso: string | undefined) {
  return iso ? dateTime.format(new Date(iso)) : '—'
}

export function formatDuration(seconds: number) {
  const d = Math.floor(seconds / 86_400)
  const h = Math.floor((seconds % 86_400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export function formatBytes(n: number) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

export const number = new Intl.NumberFormat()
