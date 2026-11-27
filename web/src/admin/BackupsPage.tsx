import { DatabaseBackup, Send, ShieldAlert } from 'lucide-react'
import type { ReactNode } from 'react'
import { ApiError } from '../api/problem'
import { Badge, type BadgeProps, Button, toast } from '../ui'
import { type Setting, useSettings } from './api'
import { type BackupRun, useBackupChannelTest, useBackupNow, useBackups } from './backups-api'
import { SettingEditor } from './SettingsPage'
import { AdminPage, ErrorNote, formatBytes, formatTime, Table, Tile } from './ui'

const runTone: Record<BackupRun['status'], BadgeProps['tone']> = {
  running: 'brand',
  ok: 'fresh',
  failed: 'expired',
}

const errorText = (e: unknown) =>
  e instanceof ApiError ? e.message : 'Could not reach the server. Check your connection and retry.'

const isBackupSetting = (s: Setting) =>
  s.key === 'flag.backups' || s.key.startsWith('backup.') || s.key === 'telegram.bot_token'

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <h2 className="t-label m-0 text-ink-muted">{title}</h2>
      {children}
    </section>
  )
}

/** Nightly encrypted backups to a Telegram channel: status, runs, settings, restore. */
export function BackupsPage() {
  const { data: b, error } = useBackups()
  const now = useBackupNow()
  const test = useBackupChannelTest()
  const settings = useSettings()
  const running = b?.runs.some((r) => r.status === 'running') ?? false
  const lastOk = b?.runs.find((r) => r.status === 'ok')
  const last = b?.runs[0]

  const backUp = () =>
    now.mutate(undefined, {
      onSuccess: () => toast({ title: 'Backup queued', tone: 'success' }),
      onError: (e) =>
        toast({ title: 'Could not start a backup', description: errorText(e), tone: 'error' }),
    })
  const sendTest = () =>
    test.mutate(undefined, {
      onSuccess: () => toast({ title: 'Test message sent to the channel', tone: 'success' }),
      onError: (e) =>
        toast({ title: 'Test message failed', description: errorText(e), tone: 'error' }),
    })

  return (
    <AdminPage
      title="Backups"
      intro="The database and uploaded files, compressed and encrypted on this server, then posted to a private Telegram channel. Only your age private key can open them."
      actions={
        <Button onClick={backUp} disabled={!b?.configured || running || now.isPending}>
          <DatabaseBackup aria-hidden />
          {running ? 'Backing up…' : 'Back up now'}
        </Button>
      }
    >
      {error && <ErrorNote error={error} />}
      {b && (
        <>
          {last?.status === 'failed' && (
            <p
              role="alert"
              className="t-body-sm m-0 flex gap-2 rounded-lg bg-surface p-3.5 text-expired shadow-card"
            >
              <ShieldAlert aria-hidden className="size-5 shrink-0" />
              <span>
                The last backup failed ({formatTime(last.startedAt)}):{' '}
                {last.error ?? 'unknown error'}
              </span>
            </p>
          )}
          <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-3">
            <Tile
              icon={DatabaseBackup}
              label="Last backup"
              value={lastOk ? formatTime(lastOk.finishedAt ?? lastOk.startedAt) : 'Never'}
              tone={lastOk ? 'fresh' : b.enabled ? 'expired' : undefined}
              detail={lastOk && `${formatBytes(lastOk.sizeBytes)} in ${lastOk.parts} parts`}
            />
            <Tile
              icon={DatabaseBackup}
              label="Schedule"
              value={b.enabled ? `Nightly ${b.time}` : 'Off'}
              tone={b.enabled ? undefined : 'soon'}
              detail={
                b.enabled
                  ? `${b.timezone}; next ${formatTime(b.nextRunAt)}; keeps ${b.keep}`
                  : 'Turn on flag.backups below once the channel is set up'
              }
            />
            <Tile
              icon={Send}
              label="Telegram channel"
              value={
                b.channel.status === 'ok'
                  ? `@${b.channel.botUsername}`
                  : b.channel.status === 'error'
                    ? 'Problem'
                    : 'Not set up'
              }
              tone={
                b.channel.status === 'ok'
                  ? 'fresh'
                  : b.channel.status === 'error'
                    ? 'expired'
                    : 'soon'
              }
              detail={
                <>
                  {b.channel.detail ?? (b.chatId ? `Chat ${b.chatId}` : null)}{' '}
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={sendTest}
                    disabled={test.isPending || !b.hasToken || !b.chatId}
                  >
                    Send test message
                  </Button>
                </>
              }
            />
            <Tile
              icon={ShieldAlert}
              label="Encryption key"
              value={b.hasRecipient ? 'Set' : 'Missing'}
              tone={b.hasRecipient ? 'fresh' : 'soon'}
              detail={
                b.recipient ? (
                  <span className="break-all font-mono text-[11px]">
                    {b.recipient.slice(0, 20)}…
                  </span>
                ) : (
                  'Paste your age public key below'
                )
              }
            />
          </div>

          <Section title="Runs">
            <Table aria-label="Backup runs">
              <thead>
                <tr>
                  <th>Started</th>
                  <th>Trigger</th>
                  <th>Status</th>
                  <th>Size</th>
                  <th>Parts</th>
                  <th>Version</th>
                  <th>Notes</th>
                </tr>
              </thead>
              <tbody>
                {b.runs.length === 0 && (
                  <tr>
                    <td colSpan={7} className="text-ink-muted">
                      No backups yet.
                    </td>
                  </tr>
                )}
                {b.runs.map((r) => (
                  <tr key={r.id}>
                    <td className="whitespace-nowrap">{formatTime(r.startedAt)}</td>
                    <td>{r.trigger}</td>
                    <td>
                      <Badge tone={runTone[r.status]}>{r.status}</Badge>
                    </td>
                    <td className="tabular">
                      {r.status === 'ok' ? formatBytes(r.sizeBytes) : '—'}
                    </td>
                    <td className="tabular">{r.status === 'ok' ? r.parts : '—'}</td>
                    <td className="font-mono text-[12.5px]">
                      {r.appVersion}
                      {r.schemaVersion !== undefined && ` · v${r.schemaVersion}`}
                    </td>
                    <td
                      className="max-w-[40ch] truncate text-ink-muted"
                      title={r.error ?? r.deleteNote}
                    >
                      {r.error ?? r.deleteNote ?? (r.pruned ? 'Removed from the channel' : '')}
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
          {(settings.data?.items ?? []).filter(isBackupSetting).map((s) => (
            <SettingEditor key={s.key} setting={s} />
          ))}
        </ul>
      </Section>

      <Section title="Restore">
        <div className="t-body-sm rounded-lg bg-surface p-4 shadow-card">
          <ol className="m-0 flex list-decimal flex-col gap-1.5 pl-5">
            <li>
              Keep your <code>age-keygen</code> private key somewhere safe. The server never has it.
            </li>
            <li>
              On a new server, create an empty database and download the newest{' '}
              <code>manifest.json</code> from the channel.
            </li>
            <li>
              Run{' '}
              <code>stockpot restore --manifest manifest.json --identity key.txt --blob-dir …</code>{' '}
              with the bot token in <code>STOCKPOT_BOT_TOKEN</code>.
            </li>
          </ol>
          <p className="m-0 mt-2 text-ink-muted">Full steps: docs/restore.md in the repository.</p>
        </div>
      </Section>
    </AdminPage>
  )
}
