import { Sparkles } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { ApiError } from '../api/problem'
import { Badge, type BadgeProps, Button, Input, toast } from '../ui'
import { type Setting, useSettings } from './api'
import {
  type EnrichRun,
  useCatalog,
  useDeleteIngredient,
  useEnrichNow,
  useMergeIngredient,
  usePromoteIngredient,
  useRetryFailed,
} from './catalog-api'
import { SettingEditor } from './SettingsPage'
import { AdminPage, ErrorNote, formatTime, Table, Tile } from './ui'

const outcomeTone: Record<EnrichRun['outcome'], BadgeProps['tone']> = {
  running: 'brand',
  ok: 'fresh',
  partial: 'soon',
  deferred: 'neutral',
  error: 'expired',
}

const errorText = (e: unknown) =>
  e instanceof ApiError ? e.message : 'Could not reach the server. Check your connection and retry.'

const isEnrichSetting = (s: Setting) =>
  s.key === 'flag.catalog_enrich' ||
  s.key.startsWith('enrich.') ||
  s.key === 'llm.background_reserve_pct' ||
  s.key === 'retention.enrich_run_days'

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <h2 className="t-label m-0 text-ink-muted">{title}</h2>
      {children}
    </section>
  )
}

function MergeForm({ id, name }: { id: string; name: string }) {
  const [open, setOpen] = useState(false)
  const [slug, setSlug] = useState('')
  const merge = useMergeIngredient()
  if (!open) {
    return (
      <Button size="sm" variant="ghost" onClick={() => setOpen(true)}>
        Merge…
      </Button>
    )
  }
  return (
    <form
      className="flex items-center gap-1.5"
      onSubmit={(e) => {
        e.preventDefault()
        merge.mutate(
          { id, targetSlug: slug.trim() },
          {
            onSuccess: () => toast({ title: `Merged ${name} into ${slug}`, tone: 'success' }),
            onError: (err) =>
              toast({ title: 'Could not merge', description: errorText(err), tone: 'error' }),
          },
        )
      }}
    >
      <Input
        aria-label={`Merge ${name} into (ingredient slug)`}
        placeholder="target slug, e.g. orange"
        value={slug}
        onChange={(e) => setSlug(e.target.value)}
        className="w-44"
      />
      <Button size="sm" type="submit" disabled={!slug.trim() || merge.isPending}>
        Merge
      </Button>
    </form>
  )
}

/** Background matching of products to ingredients: queue, runs, review lists, settings. */
export function CatalogPage() {
  const { data: c, error } = useCatalog()
  const settings = useSettings()
  const now = useEnrichNow()
  const retry = useRetryFailed()
  const promote = usePromoteIngredient()
  const del = useDeleteIngredient()

  const failed = c?.counts.failed ?? 0
  const fail = (title: string) => (e: unknown) =>
    toast({ title, description: errorText(e), tone: 'error' })

  return (
    <AdminPage
      title="Catalog"
      intro="Products are matched to ingredients by rules first. Whatever the rules could not place is sent to the LLM in the background, only while quota above the reserve is left. A match you chose yourself is never changed."
      actions={
        <Button
          onClick={() =>
            now.mutate(undefined, {
              onSuccess: () => toast({ title: 'Enrichment queued', tone: 'success' }),
              onError: fail('Could not start a run'),
            })
          }
          disabled={now.isPending}
        >
          <Sparkles aria-hidden />
          Run now
        </Button>
      }
    >
      {error && <ErrorNote error={error} />}
      {c && (
        <>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-3">
            <Tile
              icon={Sparkles}
              label="Waiting"
              value={String(c.counts.pending ?? 0)}
              tone={(c.counts.pending ?? 0) > 0 ? 'soon' : 'fresh'}
              detail="products to match"
            />
            <Tile icon={Sparkles} label="Matched" value={String(c.counts.done ?? 0)} />
            <Tile
              icon={Sparkles}
              label="Failed"
              value={String(failed)}
              tone={failed > 0 ? 'expired' : undefined}
              detail={
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={failed === 0 || retry.isPending}
                  onClick={() =>
                    retry.mutate(undefined, {
                      onSuccess: (r) => toast({ title: `${r.retried} requeued`, tone: 'success' }),
                      onError: fail('Could not requeue'),
                    })
                  }
                >
                  Retry failed
                </Button>
              }
            />
            <Tile
              icon={Sparkles}
              label="Background LLM"
              value={!c.enabled ? 'Off' : c.headroom ? 'Has quota' : 'Waiting for quota'}
              tone={!c.enabled ? 'soon' : c.headroom ? 'fresh' : undefined}
              detail={
                c.enabled
                  ? `every ${c.intervalMinutes} min${c.retryAt ? `; quota back ${formatTime(c.retryAt)}` : ''}`
                  : 'Turn on flag.catalog_enrich below'
              }
            />
          </div>

          <Section title="Runs">
            <Table aria-label="Enrichment runs">
              <thead>
                <tr>
                  <th>Started</th>
                  <th>Trigger</th>
                  <th>Outcome</th>
                  <th>Items</th>
                  <th>Applied</th>
                  <th>Cached</th>
                  <th>Failed</th>
                  <th>Calls</th>
                  <th>Provider</th>
                  <th>Why</th>
                </tr>
              </thead>
              <tbody>
                {c.runs.length === 0 && (
                  <tr>
                    <td colSpan={10} className="text-ink-muted">
                      No runs yet.
                    </td>
                  </tr>
                )}
                {c.runs.map((r) => (
                  <tr key={r.id}>
                    <td className="whitespace-nowrap">{formatTime(r.startedAt)}</td>
                    <td>{r.trigger}</td>
                    <td>
                      <Badge tone={outcomeTone[r.outcome]}>{r.outcome}</Badge>
                    </td>
                    <td className="tabular">{r.items}</td>
                    <td className="tabular">{r.applied}</td>
                    <td className="tabular">{r.cached}</td>
                    <td className="tabular">{r.failed}</td>
                    <td className="tabular">{r.calls}</td>
                    <td>{r.provider ?? '—'}</td>
                    <td className="max-w-[40ch] truncate text-ink-muted" title={r.reason}>
                      {r.reason ?? ''}
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </Section>

          <Section title="Ingredients created by the LLM">
            <Table aria-label="LLM ingredients">
              <thead>
                <tr>
                  <th>Ingredient</th>
                  <th>Kind of</th>
                  <th>Household</th>
                  <th>Products</th>
                  <th>Added</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {c.llmIngredients.length === 0 && (
                  <tr>
                    <td colSpan={6} className="text-ink-muted">
                      None. Everything matched an existing ingredient.
                    </td>
                  </tr>
                )}
                {c.llmIngredients.map((i) => (
                  <tr key={i.id}>
                    <td>
                      {i.name}{' '}
                      <span className="font-mono text-[12px] text-ink-muted">{i.slug}</span>
                    </td>
                    <td>{i.parent ?? '—'}</td>
                    <td>{i.household}</td>
                    <td className="tabular">{i.products}</td>
                    <td className="whitespace-nowrap">{formatTime(i.createdAt)}</td>
                    <td>
                      <div className="flex flex-wrap items-center justify-end gap-1">
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={promote.isPending}
                          onClick={() =>
                            promote.mutate(i.id, {
                              onSuccess: () =>
                                toast({ title: `${i.name} is now global`, tone: 'success' }),
                              onError: fail('Could not promote'),
                            })
                          }
                        >
                          Make global
                        </Button>
                        <MergeForm id={i.id} name={i.name} />
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={del.isPending}
                          onClick={() =>
                            del.mutate(i.id, {
                              onSuccess: () =>
                                toast({ title: `${i.name} deleted`, tone: 'success' }),
                              onError: fail('Could not delete'),
                            })
                          }
                        >
                          Delete
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </Section>

          <Section title="Products to review">
            <Table aria-label="Products to review">
              <thead>
                <tr>
                  <th>Product</th>
                  <th>Household</th>
                  <th>Matched to</th>
                  <th>Source</th>
                  <th>Confidence</th>
                  <th>State</th>
                </tr>
              </thead>
              <tbody>
                {c.review.length === 0 && (
                  <tr>
                    <td colSpan={6} className="text-ink-muted">
                      Nothing to review.
                    </td>
                  </tr>
                )}
                {c.review.map((p) => (
                  <tr key={p.id}>
                    <td>
                      {p.name}
                      {p.brand && <span className="text-ink-muted"> · {p.brand}</span>}
                    </td>
                    <td>{p.household}</td>
                    <td>{p.ingredient ?? '—'}</td>
                    <td>{p.matchSource}</td>
                    <td className="tabular">
                      {p.confidence == null ? '—' : `${Math.round(p.confidence * 100)}%`}
                    </td>
                    <td>
                      <Badge tone={p.state === 'failed' ? 'expired' : 'neutral'}>
                        {p.state}
                        {p.attempts > 0 && ` (${p.attempts})`}
                      </Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </Section>
        </>
      )}

      <Section title="Settings">
        <ul className="m-0 list-none rounded-lg bg-surface p-0 shadow-card [&>li+li]:border-t [&>li+li]:border-border">
          {settings.error && (
            <li className="px-4 py-3.5">
              <ErrorNote error={settings.error} />
            </li>
          )}
          {(settings.data?.items ?? []).filter(isEnrichSetting).map((s) => (
            <SettingEditor key={s.key} setting={s} />
          ))}
        </ul>
      </Section>
    </AdminPage>
  )
}
