import { BookOpen, Clapperboard, Mic, RotateCw, TriangleAlert } from 'lucide-react'
import type { ReactNode } from 'react'
import { ApiError } from '../api/problem'
import { Badge, type BadgeProps, Button, toast } from '../ui'
import { type Setting, useSettings } from './api'
import { useAdminRecipes, useRetryImport } from './recipes-api'
import { SettingEditor } from './SettingsPage'
import { AdminPage, ErrorNote, formatBytes, formatTime, number, Table, Tile } from './ui'

const statuses = [
  'done',
  'queued',
  'fetching',
  'parsing',
  'translating',
  'waiting_quota',
  'needs_input',
  'failed',
] as const

const statusTone: Record<string, BadgeProps['tone']> = {
  done: 'fresh',
  failed: 'expired',
  needs_input: 'soon',
  waiting_quota: 'soon',
}

const isRecipeSetting = (s: Setting) =>
  s.key === 'flag.recipe_web_import' ||
  s.key === 'flag.recipe_instagram' ||
  s.key === 'retention.recipe_fetch_days'

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <h2 className="t-label m-0 text-ink-muted">{title}</h2>
      {children}
    </section>
  )
}

const sum = (m: Record<string, number>) => Object.values(m).reduce((a, b) => a + b, 0)

/** Recipe import: volume by status, failures to retry, the fetch log and the reel tools. */
export function RecipesPage() {
  const { data: r, error } = useAdminRecipes()
  const retry = useRetryImport()
  const settings = useSettings()

  const doRetry = (id: string) =>
    retry.mutate(id, {
      onSuccess: () => toast({ title: 'Import queued again', tone: 'success' }),
      onError: (e) =>
        toast({
          title: 'Could not retry',
          description: e instanceof ApiError ? e.message : 'Check your connection and retry.',
          tone: 'error',
        }),
    })

  return (
    <AdminPage
      title="Recipes"
      intro="Imports from recipe sites, Instagram reels, photos and pasted text. Pages with recipe data import without AI; the rest use the recipes_* LLM features."
    >
      {error && <ErrorNote error={error} />}
      {r && (
        <>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-3">
            <Tile
              icon={BookOpen}
              label="Imports, 24 h"
              value={number.format(sum(r.last24h))}
              detail={`${number.format(r.last24h.done ?? 0)} done · ${number.format(r.last24h.failed ?? 0)} failed`}
            />
            <Tile
              icon={BookOpen}
              label="Web import"
              value={r.webImport ? 'On' : 'Off'}
              tone={r.webImport ? 'fresh' : 'soon'}
              detail="flag.recipe_web_import"
            />
            <Tile
              icon={Clapperboard}
              label="Instagram reels"
              value={
                !r.instagram ? 'Off' : r.tools.ytDlp && r.tools.ffmpeg ? 'Ready' : 'Tools missing'
              }
              tone={!r.instagram ? 'soon' : r.tools.ytDlp && r.tools.ffmpeg ? 'fresh' : 'expired'}
              detail={
                r.tools.error ??
                `yt-dlp ${r.tools.ytDlpVersion ?? 'missing'} · ffmpeg ${r.tools.ffmpegVersion ?? (r.tools.ffmpeg ? 'found' : 'missing')}`
              }
            />
            <Tile
              icon={Mic}
              label="Reel transcripts"
              value={r.transcription ? 'Available' : 'Caption only'}
              tone={r.transcription ? 'fresh' : 'soon'}
              detail={
                r.transcription
                  ? 'A provider has a transcription model'
                  : 'Set transcription_model on a provider in LLM'
              }
            />
          </div>

          <Section title="By status">
            <Table aria-label="Imports by status">
              <thead>
                <tr>
                  <th>Status</th>
                  <th>24 h</th>
                  <th>14 days</th>
                </tr>
              </thead>
              <tbody>
                {statuses.map((s) => (
                  <tr key={s}>
                    <td>
                      <Badge tone={statusTone[s] ?? 'neutral'}>{s}</Badge>
                    </td>
                    <td className="tabular">{number.format(r.last24h[s] ?? 0)}</td>
                    <td className="tabular">{number.format(r.last14d[s] ?? 0)}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </Section>

          <Section title="Needs attention">
            <Table aria-label="Failed imports">
              <thead>
                <tr>
                  <th>Updated</th>
                  <th>Kind</th>
                  <th>Input</th>
                  <th>Status</th>
                  <th>Reason</th>
                  <th>Tries</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {r.failures.length === 0 && (
                  <tr>
                    <td colSpan={7} className="text-ink-muted">
                      Nothing failed or stuck.
                    </td>
                  </tr>
                )}
                {r.failures.map((f) => (
                  <tr key={f.id}>
                    <td className="whitespace-nowrap">{formatTime(f.updatedAt)}</td>
                    <td>{f.kind}</td>
                    <td className="max-w-[32ch] truncate" title={f.input}>
                      {f.input ?? '—'}
                    </td>
                    <td>
                      <Badge tone={statusTone[f.status] ?? 'neutral'}>{f.status}</Badge>
                    </td>
                    <td className="max-w-[40ch] truncate text-ink-muted" title={f.detail}>
                      <span className="font-mono text-[12.5px]">{f.code}</span> {f.detail}
                    </td>
                    <td className="tabular">{f.attempts}</td>
                    <td>
                      <Button
                        size="sm"
                        variant="secondary"
                        disabled={retry.isPending}
                        onClick={() => doRetry(f.id)}
                      >
                        <RotateCw aria-hidden />
                        Retry
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </Section>

          <Section title="Fetch log">
            <Table aria-label="Fetch log">
              <thead>
                <tr>
                  <th>At</th>
                  <th>What</th>
                  <th>URL</th>
                  <th>HTTP</th>
                  <th>Size</th>
                  <th>Time</th>
                  <th>Error</th>
                </tr>
              </thead>
              <tbody>
                {r.fetches.length === 0 && (
                  <tr>
                    <td colSpan={7} className="text-ink-muted">
                      No fetches yet.
                    </td>
                  </tr>
                )}
                {r.fetches.map((f, i) => (
                  // biome-ignore lint/suspicious/noArrayIndexKey: log rows have no id
                  <tr key={i}>
                    <td className="whitespace-nowrap">{formatTime(f.at)}</td>
                    <td>{f.purpose}</td>
                    <td className="max-w-[36ch] truncate" title={f.finalUrl ?? f.url}>
                      {f.url}
                    </td>
                    <td className="tabular">{f.status ?? '—'}</td>
                    <td className="tabular">{f.bytes ? formatBytes(f.bytes) : '—'}</td>
                    <td className="tabular">{f.durationMs} ms</td>
                    <td className="max-w-[32ch] truncate text-expired" title={f.error}>
                      {f.error ? (
                        <span className="inline-flex items-center gap-1">
                          <TriangleAlert className="size-3.5" aria-hidden />
                          {f.error}
                        </span>
                      ) : null}
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
          {(settings.data?.items ?? []).filter(isRecipeSetting).map((s) => (
            <SettingEditor key={s.key} setting={s} />
          ))}
        </ul>
      </Section>
    </AdminPage>
  )
}
