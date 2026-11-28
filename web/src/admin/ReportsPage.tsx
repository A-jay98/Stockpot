import { MessageSquareWarning, Sparkles } from 'lucide-react'
import { useState } from 'react'
import { ApiError } from '../api/problem'
import { Badge, type BadgeProps, Button, Segmented, toast } from '../ui'
import { type Setting, useSettings } from './api'
import {
  type AdminReport,
  type ReportStatus,
  type ResolveAction,
  useReports,
  useResolveReport,
  useReviewNow,
} from './reports-api'
import { SettingEditor } from './SettingsPage'
import { AdminPage, compactField, ErrorNote, formatTime, Tile } from './ui'

export const unitCodes = [
  'ea',
  'pack',
  'bag',
  'bottle',
  'can',
  'jar',
  'box',
  'bunch',
  'head',
  'clove',
  'slice',
  'portion',
  'g',
  'kg',
  'lb',
  'oz',
  'ml',
  'l',
  'tsp',
  'tbsp',
  'cup',
  'fl_oz',
]

const statusTone: Record<ReportStatus, BadgeProps['tone']> = {
  open: 'brand',
  needs_admin: 'soon',
  applied: 'fresh',
  rejected: 'neutral',
  dismissed: 'neutral',
}

const statusLabel: Record<ReportStatus, string> = {
  open: 'Waiting for LLM',
  needs_admin: 'Needs you',
  applied: 'Applied',
  rejected: 'Rejected',
  dismissed: 'Dismissed',
}

const kindLabel: Record<AdminReport['kind'], string> = {
  wrong_unit: 'Default unit is wrong',
  wrong_match: 'Wrong match',
  wrong_icon: 'Wrong icon',
  other: 'Other',
}

const errorText = (e: unknown) =>
  e instanceof ApiError ? e.message : 'Could not reach the server. Check your connection and retry.'

const isReportSetting = (s: Setting) =>
  s.key.startsWith('flag.report_') ||
  s.key.startsWith('reports.') ||
  s.key === 'retention.problem_report_days'

/** The unit the person saw: the row's own, else the default that applied. */
export function shownUnit(r: AdminReport) {
  const c = r.context
  return c.unit || c.productDefaultUnit || c.ingredientUnit || c.inheritedUnit || ''
}

type Filter = ReportStatus | 'all'

function ReportCard({ r }: { r: AdminReport }) {
  const resolve = useResolveReport()
  const [unit, setUnit] = useState(r.llmValue ?? r.suggested ?? '')
  const c = r.context
  const name = c.productName || c.itemText || 'Item'
  const shown = shownUnit(r)
  const act = (action: ResolveAction, value?: string) =>
    resolve.mutate(
      { id: r.id, action, value },
      {
        onSuccess: () => toast({ title: `${name}: ${action} done`, tone: 'success' }),
        onError: (e) =>
          toast({ title: 'Could not update', description: errorText(e), tone: 'error' }),
      },
    )
  const closed = r.status === 'applied' || r.status === 'rejected' || r.status === 'dismissed'

  return (
    <li
      aria-label={`Report: ${name}`}
      className="flex flex-col gap-2 rounded-lg bg-surface p-3.5 shadow-card"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-semibold">{name}</span>
        {c.brand && <span className="text-ink-muted">· {c.brand}</span>}
        <Badge tone={statusTone[r.status]}>{statusLabel[r.status]}</Badge>
        <Badge>{kindLabel[r.kind]}</Badge>
        <span className="ml-auto text-[12.5px] whitespace-nowrap text-ink-muted">
          {r.household}
          {r.reporter && ` · ${r.reporter}`} · {formatTime(r.createdAt)}
        </span>
      </div>
      <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 text-[13.5px] [&_dt]:text-ink-muted">
        {c.ingredientName && (
          <>
            <dt>Ingredient</dt>
            <dd className="m-0">
              {c.ingredientName}{' '}
              <span className="font-mono text-[12px] text-ink-muted">
                {c.ingredientSlug}
                {c.global ? ' · global' : ' · household'}
              </span>
            </dd>
          </>
        )}
        {r.kind === 'wrong_unit' && (
          <>
            <dt>Shown</dt>
            <dd className="m-0">
              {c.qty ? `${c.qty} ` : ''}
              {shown || '—'}
              {c.ingredientUnit || c.inheritedUnit
                ? ` (ingredient default: ${c.ingredientUnit || `${c.inheritedUnit}, inherited`})`
                : ''}
            </dd>
            <dt>Person says</dt>
            <dd className="m-0">{r.suggested || 'not sure'}</dd>
          </>
        )}
        {r.note && (
          <>
            <dt>Note</dt>
            <dd className="m-0">{r.note}</dd>
          </>
        )}
        {r.llmVerdict && (
          <>
            <dt>LLM</dt>
            <dd className="m-0">
              <strong>{r.llmVerdict}</strong>
              {r.llmValue && ` → ${r.llmValue}`}
              {r.llmConfidence != null && ` (${Math.round(r.llmConfidence * 100)}%)`}
              {r.llmReason && <span className="text-ink-muted"> · {r.llmReason}</span>}
            </dd>
          </>
        )}
        {r.changes && r.changes.length > 0 && (
          <>
            <dt>Changed</dt>
            <dd className="m-0">
              {r.changes.map((ch) => (
                <div key={`${ch.target}-${ch.id}`}>
                  {ch.target} {ch.name}
                  {ch.global ? ' (global)' : ''}: {ch.before ?? '—'} → {ch.after ?? '—'}
                </div>
              ))}
            </dd>
          </>
        )}
        {r.resolvedBy && (
          <>
            <dt>Closed by</dt>
            <dd className="m-0">
              {r.resolvedBy} {r.resolvedAt && `· ${formatTime(r.resolvedAt)}`}
            </dd>
          </>
        )}
      </dl>
      <div className="flex flex-wrap items-center gap-1.5">
        {r.kind === 'wrong_unit' && !closed && (
          <>
            <select
              aria-label={`Unit for ${name}`}
              className={compactField}
              value={unit}
              onChange={(e) => setUnit(e.target.value)}
            >
              <option value="">Pick a unit</option>
              {unitCodes.map((u) => (
                <option key={u} value={u}>
                  {u}
                </option>
              ))}
            </select>
            <Button
              size="sm"
              disabled={!unit || resolve.isPending}
              onClick={() => act('apply', unit)}
            >
              Apply
            </Button>
          </>
        )}
        {!closed && (
          <>
            <Button
              size="sm"
              variant="ghost"
              disabled={resolve.isPending}
              onClick={() => act('reject')}
            >
              Reject
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={resolve.isPending}
              onClick={() => act('dismiss')}
            >
              Dismiss
            </Button>
          </>
        )}
        {r.status !== 'open' && (
          <Button
            size="sm"
            variant="ghost"
            disabled={resolve.isPending}
            onClick={() => act('reopen')}
          >
            Send back to LLM
          </Button>
        )}
      </div>
    </li>
  )
}

/** Problem reports from the app: the LLM's verdicts, what changed, and what needs you. */
export function ReportsPage() {
  const [filter, setFilter] = useState<Filter>('needs_admin')
  const { data, error } = useReports(filter === 'all' ? '' : filter)
  const settings = useSettings()
  const now = useReviewNow()
  const n = (s: ReportStatus) => data?.counts[s] ?? 0

  return (
    <AdminPage
      title="Reports"
      intro="Problems people report from the app. A background job asks the LLM about them, only while quota above the reserve is left. Default-unit fixes it is sure about are applied to the catalog; everything else waits here."
      actions={
        <Button
          onClick={() =>
            now.mutate(undefined, {
              onSuccess: () => toast({ title: 'Review queued', tone: 'success' }),
              onError: (e) =>
                toast({
                  title: 'Could not start a review',
                  description: errorText(e),
                  tone: 'error',
                }),
            })
          }
          disabled={now.isPending}
        >
          <Sparkles aria-hidden />
          Review now
        </Button>
      }
    >
      {error && <ErrorNote error={error} />}
      {data && (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-3">
          <Tile
            icon={MessageSquareWarning}
            label="Needs you"
            value={String(n('needs_admin'))}
            tone={n('needs_admin') > 0 ? 'soon' : 'fresh'}
          />
          <Tile icon={MessageSquareWarning} label="Waiting for LLM" value={String(n('open'))} />
          <Tile icon={MessageSquareWarning} label="Applied" value={String(n('applied'))} />
          <Tile
            icon={Sparkles}
            label="Background LLM"
            value={!data.enabled ? 'Off' : data.headroom ? 'Has quota' : 'Waiting for quota'}
            tone={!data.enabled ? 'soon' : data.headroom ? 'fresh' : undefined}
            detail={
              data.enabled
                ? `every ${data.intervalMinutes} min${data.autoApply ? '' : ', auto-apply off'}${data.retryAt ? `; quota back ${formatTime(data.retryAt)}` : ''}`
                : 'Turn on flag.report_review below'
            }
          />
        </div>
      )}
      <Segmented<Filter>
        aria-label="Show"
        value={filter}
        onValueChange={setFilter}
        options={[
          { value: 'needs_admin', label: 'Needs you' },
          { value: 'open', label: 'Waiting' },
          { value: 'applied', label: 'Applied' },
          { value: 'rejected', label: 'Rejected' },
          { value: 'all', label: 'All' },
        ]}
      />
      {data && data.reports.length === 0 && <p className="m-0 text-ink-muted">No reports here.</p>}
      <ul className="m-0 flex list-none flex-col gap-2.5 p-0">
        {data?.reports.map((r) => (
          <ReportCard key={r.id} r={r} />
        ))}
      </ul>
      <section aria-label="Settings" className="flex flex-col gap-2">
        <h2 className="t-label m-0 text-ink-muted">Settings</h2>
        <ul className="m-0 list-none rounded-lg bg-surface p-0 shadow-card [&>li+li]:border-t [&>li+li]:border-border">
          {settings.error && (
            <li className="px-4 py-3.5">
              <ErrorNote error={settings.error} />
            </li>
          )}
          {(settings.data?.items ?? []).filter(isReportSetting).map((s) => (
            <SettingEditor key={s.key} setting={s} />
          ))}
        </ul>
      </section>
    </AdminPage>
  )
}
