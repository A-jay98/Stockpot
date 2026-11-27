import { Send, Unplug } from 'lucide-react'
import { useEffect, useState } from 'react'
import { ApiError } from '../../api/problem'
import { Page } from '../../app/Page'
import { Badge, Button, Field, Input, toast } from '../../ui'
import {
  type Preferences,
  useDisconnectTelegram,
  usePreferences,
  useSavePreferences,
  useStartTelegramLink,
  useTelegram,
  useTelegramTest,
} from './api'

const errorText = (e: unknown) =>
  e instanceof ApiError ? e.message : 'Could not reach the server. Check your connection and retry.'

function TelegramCard() {
  const [pendingUntil, setPendingUntil] = useState<number | null>(null)
  const tg = useTelegram(pendingUntil !== null)
  const start = useStartTelegramLink()
  const disconnect = useDisconnectTelegram()
  const test = useTelegramTest()
  const linked = tg.data?.linked ?? false

  // Stop waiting once connected or when the code has expired.
  useEffect(() => {
    if (pendingUntil === null) return
    if (linked) return setPendingUntil(null)
    const t = setTimeout(() => setPendingUntil(null), Math.max(0, pendingUntil - Date.now()))
    return () => clearTimeout(t)
  }, [linked, pendingUntil])

  const connect = () =>
    start.mutate(undefined, {
      onSuccess: (l) => {
        setPendingUntil(new Date(l.expiresAt).getTime())
        window.open(l.url, '_blank', 'noopener,noreferrer')
      },
      onError: (e) =>
        toast({ title: 'Could not start connecting', description: errorText(e), tone: 'error' }),
    })

  return (
    <section
      aria-label="Telegram"
      className="flex flex-col gap-3 rounded-lg bg-surface p-4 shadow-card"
    >
      <div className="flex items-center gap-2">
        <h2 className="t-title m-0 flex-1">Telegram</h2>
        {linked && tg.data?.enabled && <Badge tone="fresh">Connected</Badge>}
        {linked && !tg.data?.enabled && <Badge tone="expired">Not reachable</Badge>}
      </div>
      {tg.data && !tg.data.available && (
        <p className="t-body-sm m-0 text-ink-muted">
          Telegram isn't set up on this server yet. An admin needs to add the bot token under Admin
          → Settings.
        </p>
      )}
      {linked && (
        <p className="t-body-sm m-0 text-ink-muted">
          Your digest is also sent to Telegram
          {tg.data?.username ? ` (@${tg.data.username})` : ''}. Send /expiring to the bot for a
          quick look, or /stop to disconnect.
        </p>
      )}
      {linked && tg.data?.lastError && (
        <p role="alert" className="t-body-sm m-0 text-expired">
          Telegram refused the last message: {tg.data.lastError}. Connect again to resume.
        </p>
      )}
      {pendingUntil !== null && (
        <p role="status" className="t-body-sm m-0 text-ink-muted">
          Waiting for you to press Start in Telegram…
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        {!linked || !tg.data?.enabled ? (
          <Button onClick={connect} disabled={!tg.data?.available || start.isPending}>
            <Send aria-hidden />
            {linked ? 'Reconnect Telegram' : 'Connect Telegram'}
          </Button>
        ) : (
          <Button
            variant="secondary"
            disabled={test.isPending}
            onClick={() =>
              test.mutate(undefined, {
                onSuccess: () => toast({ title: 'Test message sent', tone: 'success' }),
                onError: (e) =>
                  toast({ title: 'Test message failed', description: errorText(e), tone: 'error' }),
              })
            }
          >
            Send test message
          </Button>
        )}
        {linked && (
          <Button
            variant="ghost"
            disabled={disconnect.isPending}
            onClick={() =>
              disconnect.mutate(undefined, {
                onError: (e) =>
                  toast({
                    title: 'Could not disconnect',
                    description: errorText(e),
                    tone: 'error',
                  }),
              })
            }
          >
            <Unplug aria-hidden />
            Disconnect
          </Button>
        )}
      </div>
    </section>
  )
}

function PreferencesForm({ initial }: { initial: Preferences }) {
  const [form, setForm] = useState(initial)
  const save = useSavePreferences()
  const dirty = JSON.stringify(form) !== JSON.stringify(initial)
  const set = <K extends keyof Preferences>(k: K, v: Preferences[K]) =>
    setForm((f) => ({ ...f, [k]: v }))
  return (
    <form
      aria-label="Alert preferences"
      className="flex flex-col gap-3.5 rounded-lg bg-surface p-4 shadow-card"
      onSubmit={(e) => {
        e.preventDefault()
        // Send only what changed, so untouched fields keep following the defaults.
        const changed = Object.fromEntries(
          Object.entries(form).filter(([k, v]) => initial[k as keyof Preferences] !== v),
        )
        save.mutate(changed, {
          onSuccess: () => toast({ title: 'Saved', tone: 'success' }),
          onError: (err) =>
            toast({ title: 'Could not save', description: errorText(err), tone: 'error' }),
        })
      }}
    >
      <h2 className="t-title m-0">Daily digest</h2>
      <Field
        label="Digest time"
        hint="Toronto time. One digest a day, only when something needs attention."
      >
        {(p) => (
          <Input
            {...p}
            type="time"
            required
            value={form.digestTime}
            onChange={(e) => set('digestTime', e.target.value)}
          />
        )}
      </Field>
      <Field label="Expiring soon means within (days)">
        {(p) => (
          <Input
            {...p}
            type="number"
            inputMode="numeric"
            min={0}
            max={30}
            required
            value={form.leadDays}
            onChange={(e) => set('leadDays', Number(e.target.value))}
          />
        )}
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Quiet from" hint="Telegram waits until quiet hours end.">
          {(p) => (
            <Input
              {...p}
              type="time"
              required
              value={form.quietStart}
              onChange={(e) => set('quietStart', e.target.value)}
            />
          )}
        </Field>
        <Field label="Quiet until">
          {(p) => (
            <Input
              {...p}
              type="time"
              required
              value={form.quietEnd}
              onChange={(e) => set('quietEnd', e.target.value)}
            />
          )}
        </Field>
      </div>
      <Button type="submit" disabled={!dirty || save.isPending} className="self-start">
        Save
      </Button>
    </form>
  )
}

/** Settings → Notifications: Telegram connection and digest preferences. */
export function NotificationSettingsPage() {
  const prefs = usePreferences()
  return (
    <Page title="Notifications">
      <TelegramCard />
      {prefs.data && <PreferencesForm initial={prefs.data} />}
      {prefs.error && (
        <p role="alert" className="t-body-sm m-0 text-expired">
          {errorText(prefs.error)}
        </p>
      )}
    </Page>
  )
}
