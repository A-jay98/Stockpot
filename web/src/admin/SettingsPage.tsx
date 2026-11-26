import { History, KeyRound, RotateCcw } from 'lucide-react'
import { useState } from 'react'
import { ApiError } from '../api/problem'
import { Badge, Button, Field, Input, Switch, Textarea, toast } from '../ui'
import { AuditTable } from './AuditPage'
import { type Setting, useResetSetting, useSaveSetting, useSettings } from './api'
import { useAuditEvents } from './trail-api'
import { AdminPage, ErrorNote, formatTime, LoadMore } from './ui'

export function SettingsPage() {
  const { data, error } = useSettings()
  const items = data?.items ?? []
  const flags = items.filter((s) => s.key.startsWith('flag.'))
  const groups = new Map<string, Setting[]>()
  for (const s of items) {
    if (s.key.startsWith('flag.')) continue
    const group = s.key.split('.')[0] ?? s.key
    groups.set(group, [...(groups.get(group) ?? []), s])
  }

  return (
    <AdminPage
      title="Settings & flags"
      intro="Changes apply immediately in every server process and are recorded in the audit log."
    >
      {error && <ErrorNote error={error} />}
      {flags.length > 0 && (
        <Section title="Feature flags">
          {flags.map((s) => (
            <FlagRow key={s.key} setting={s} />
          ))}
        </Section>
      )}
      {[...groups].map(([group, list]) => (
        <Section key={group} title={sectionTitle(group)}>
          {list.map((s) => (
            <SettingEditor key={s.key} setting={s} />
          ))}
        </Section>
      ))}
    </AdminPage>
  )
}

const sectionTitle = (group: string) =>
  ({ log: 'Logging', retention: 'Data retention', auth: 'Sign-in', llm: 'AI providers' })[group] ??
  group.charAt(0).toUpperCase() + group.slice(1)

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <h2 className="t-label m-0 text-ink-muted">{title}</h2>
      <ul className="m-0 list-none rounded-lg bg-surface p-0 shadow-card [&>li+li]:border-t [&>li+li]:border-border">
        {children}
      </ul>
    </section>
  )
}

function errorText(e: unknown) {
  return e instanceof ApiError ? e.message : 'Could not save. Check your connection and retry.'
}

function Meta({ setting }: { setting: Setting }) {
  return (
    <span className="flex flex-wrap items-center gap-2 text-xs text-ink-muted">
      <code className="font-mono">{setting.key}</code>
      {setting.isSet ? (
        <span>changed {formatTime(setting.updatedAt)}</span>
      ) : (
        <Badge>default</Badge>
      )}
    </span>
  )
}

function FlagRow({ setting }: { setting: Setting }) {
  const save = useSaveSetting()
  const history = useHistory(setting)
  const on = setting.value === true
  return (
    <li className="flex flex-wrap items-center gap-3.5 px-4 py-3.5">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="font-semibold">{setting.description}</span>
        <Meta setting={setting} />
      </div>
      <Switch
        checked={on}
        disabled={save.isPending}
        aria-label={setting.description}
        onCheckedChange={(value) =>
          save.mutate(
            { key: setting.key, value },
            {
              onError: (e) =>
                toast({ title: 'Flag not changed', description: errorText(e), tone: 'error' }),
            },
          )
        }
      />
      {history.toggle}
      {history.panel}
    </li>
  )
}

/** "History" button and the setting's audit trail (setting.update / setting.reset). */
function useHistory(setting: Setting) {
  const [open, setOpen] = useState(false)
  const events = useAuditEvents(
    { targetType: 'setting', targetId: setting.key },
    { limit: 10, enabled: open },
  )
  const toggle = (
    <Button
      type="button"
      size="sm"
      variant="ghost"
      aria-expanded={open}
      aria-label={`History of ${setting.key}`}
      onClick={() => setOpen(!open)}
    >
      <History aria-hidden />
      History
    </Button>
  )
  const panel = open && (
    <div className="flex basis-full flex-col gap-2">
      {events.error && <ErrorNote error={events.error} />}
      <AuditTable
        rows={events.data?.pages.flatMap((p) => p.items) ?? []}
        loading={events.isPending}
        showTarget={false}
      />
      <LoadMore
        hasMore={events.hasNextPage}
        loading={events.isFetchingNextPage}
        onClick={() => void events.fetchNextPage()}
      />
    </div>
  )
  return { toggle, panel }
}

type Parsed = { ok: true; value: unknown } | { ok: false; error: string }

/** Turns the editor's text into a typed value, or says why it can't. */
export function parseDraft(setting: Setting, draft: string): Parsed {
  switch (setting.kind) {
    case 'int': {
      if (!/^-?\d+$/.test(draft.trim())) return { ok: false, error: 'Enter a whole number.' }
      return { ok: true, value: Number(draft.trim()) }
    }
    case 'json':
      try {
        return { ok: true, value: JSON.parse(draft) }
      } catch {
        return { ok: false, error: 'Not valid JSON.' }
      }
    case 'bool':
      return { ok: true, value: draft === 'true' }
    default:
      if (setting.secret && draft === '') return { ok: false, error: 'Enter a value.' }
      return { ok: true, value: draft }
  }
}

function toDraft(setting: Setting) {
  if (setting.secret) return ''
  if (setting.kind === 'json') return JSON.stringify(setting.value, null, 2)
  return setting.value === undefined || setting.value === null ? '' : String(setting.value)
}

/** One typed setting: input for its kind, save, reset to default. Secrets are write-only. */
export function SettingEditor({ setting }: { setting: Setting }) {
  const save = useSaveSetting()
  const reset = useResetSetting()
  const history = useHistory(setting)
  const [draft, setDraft] = useState(() => toDraft(setting))
  const [base, setBase] = useState(setting)
  const [serverError, setServerError] = useState<string>()
  const [touched, setTouched] = useState(false)

  // A newer server copy (after a save elsewhere) replaces an untouched draft.
  if (base !== setting) {
    setBase(setting)
    if (!touched) setDraft(toDraft(setting))
  }

  const parsed = parseDraft(setting, draft)
  const dirty = setting.secret ? draft !== '' : draft !== toDraft(setting)
  const error = serverError ?? (touched && !parsed.ok ? parsed.error : undefined)
  const busy = save.isPending || reset.isPending

  const submit = () => {
    setTouched(true)
    if (!parsed.ok) return
    setServerError(undefined)
    save.mutate(
      { key: setting.key, value: parsed.value },
      {
        onSuccess: (s) => {
          setTouched(false)
          setDraft(toDraft(s))
          toast({ title: `Saved ${s.key}`, tone: 'success' })
        },
        onError: (e) => setServerError(errorText(e)),
      },
    )
  }

  const onReset = () => {
    setServerError(undefined)
    reset.mutate(setting.key, {
      onSuccess: (s) => {
        setTouched(false)
        setDraft(toDraft(s))
        toast({
          title: setting.secret ? `Removed ${s.key}` : `Reset ${s.key} to default`,
          tone: 'success',
        })
      },
      onError: (e) => setServerError(errorText(e)),
    })
  }

  const hint = setting.secret
    ? setting.isSet
      ? 'Stored. Enter a new value to replace it; it is never shown again.'
      : 'Not set. The value is stored encrypted and never shown again.'
    : `Default: ${setting.kind === 'json' ? 'see docs' : String(setting.default ?? '—')}`

  return (
    <li className="flex flex-col gap-2.5 px-4 py-3.5">
      <form
        className="flex flex-col gap-2.5"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <Field label={setting.description} hint={hint} error={error}>
          {(a11y) => {
            const onChange = (v: string) => {
              setDraft(v)
              setTouched(true)
              setServerError(undefined)
            }
            if (setting.options?.length) {
              return (
                <select
                  {...a11y}
                  className="h-11 w-full rounded-md border border-border bg-surface px-3 text-base text-ink sm:w-64"
                  value={draft}
                  onChange={(e) => onChange(e.target.value)}
                >
                  {setting.options.map((o) => (
                    <option key={String(o)} value={String(o)}>
                      {String(o)}
                    </option>
                  ))}
                </select>
              )
            }
            if (setting.kind === 'bool') {
              return (
                <Switch
                  {...a11y}
                  checked={draft === 'true'}
                  onCheckedChange={(c) => onChange(String(c))}
                />
              )
            }
            if (setting.kind === 'json') {
              return (
                <Textarea
                  {...a11y}
                  className="font-mono text-[13px]"
                  rows={6}
                  spellCheck={false}
                  value={draft}
                  onChange={(e) => onChange(e.target.value)}
                />
              )
            }
            return (
              <Input
                {...a11y}
                className={setting.kind === 'int' ? 'sm:w-40' : undefined}
                type={setting.secret ? 'password' : 'text'}
                inputMode={setting.kind === 'int' ? 'numeric' : undefined}
                autoComplete={setting.secret ? 'new-password' : 'off'}
                placeholder={setting.secret && setting.isSet ? '••••••••' : undefined}
                value={draft}
                onChange={(e) => onChange(e.target.value)}
              />
            )
          }}
        </Field>
        <div className="flex flex-wrap items-center gap-2">
          <Button type="submit" size="sm" disabled={!dirty || busy}>
            {setting.secret && <KeyRound aria-hidden />}
            {save.isPending ? 'Saving…' : 'Save'}
          </Button>
          {setting.isSet && (
            <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={onReset}>
              <RotateCcw aria-hidden />
              {setting.secret ? 'Remove' : 'Reset to default'}
            </Button>
          )}
          {history.toggle}
          <span className="ml-auto">
            <Meta setting={setting} />
          </span>
        </div>
      </form>
      {history.panel}
    </li>
  )
}
