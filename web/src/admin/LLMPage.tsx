import { Bot, Gauge, KeyRound, Send, Sparkles, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { ApiError } from '../api/problem'
import { Badge, type BadgeProps, Button, Field, Input, Segmented, Textarea, toast } from '../ui'
import {
  type LLMProviderStatus,
  type Setting,
  useLLMKey,
  useLLMStatus,
  useLLMTest,
  useLLMUsage,
  useSettings,
} from './api'
import { llmFeatures } from './llm-features'
import { SettingEditor } from './SettingsPage'
import { AdminPage, ErrorNote, formatTime, number, Table, Tile } from './ui'

const stateLabel: Record<string, [string, BadgeProps['tone']]> = {
  ready: ['Ready', 'fresh'],
  no_key: ['No key', 'neutral'],
  disabled: ['Disabled', 'neutral'],
  unknown_kind: ['Unknown kind', 'expired'],
  capped: ['Daily cap reached', 'soon'],
  exhausted: ['Quota exhausted', 'soon'],
  cooling_down: ['Failing, retrying soon', 'expired'],
}

const errorText = (e: unknown) =>
  e instanceof ApiError ? e.message : 'Could not reach the server. Check your connection and retry.'

/** Providers, keys, quota, usage, a test prompt and the gateway's settings. */
export function LLMPage() {
  const { data: st, error } = useLLMStatus()
  return (
    <AdminPage
      title="LLM"
      intro="Providers are tried in order. When none has quota left, LLM features fall back to manual entry."
    >
      {error && <ErrorNote error={error} />}
      {st && (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-3">
          <Tile
            icon={Sparkles}
            label="Status"
            value={!st.enabled ? 'Off' : st.available ? 'Available' : 'Manual mode'}
            tone={st.enabled && st.available ? 'fresh' : 'soon'}
            detail={
              st.available
                ? st.vision
                  ? 'Text and images'
                  : 'Text only'
                : 'No provider can answer now'
            }
          />
          <Tile
            icon={Gauge}
            label="Requests today"
            value={
              st.globalRequestCap
                ? `${number.format(st.requestsToday)} / ${number.format(st.globalRequestCap)}`
                : number.format(st.requestsToday)
            }
            detail={`${number.format(st.tokensToday)} tokens · resets ${formatTime(st.resetAt)}`}
          />
        </div>
      )}
      {st && (
        <Section title="Providers">
          {st.providers.length === 0 && (
            <li className="px-4 py-3.5 text-ink-muted">No providers configured.</li>
          )}
          {st.providers.map((p) => (
            <ProviderRow key={p.config.id} p={p} />
          ))}
        </Section>
      )}
      <TestPrompt providers={st?.providers.map((p) => p.config.id) ?? []} />
      <Usage />
      <Features />
      <Configuration />
    </AdminPage>
  )
}

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

function quota(used: number, cap: number, unit: string) {
  return cap > 0
    ? `${number.format(used)} / ${number.format(cap)} ${unit}`
    : `${number.format(used)} ${unit}`
}

function ProviderRow({ p }: { p: LLMProviderStatus }) {
  const [label, tone] = stateLabel[p.state] ?? [p.state, 'neutral']
  const key = useLLMKey()
  const [draft, setDraft] = useState('')
  const [err, setErr] = useState<string>()
  const c = p.config

  const store = (apiKey: string) => {
    setErr(undefined)
    key.mutate(
      { id: c.id, apiKey },
      {
        onSuccess: () => {
          setDraft('')
          toast({
            title: apiKey ? `Saved the ${c.id} key` : `Removed the ${c.id} key`,
            tone: 'success',
          })
        },
        onError: (e) => setErr(errorText(e)),
      },
    )
  }

  return (
    <li className="flex flex-col gap-3 px-4 py-3.5" aria-label={c.id}>
      <div className="flex flex-wrap items-center gap-2">
        <Bot aria-hidden className="size-4 text-ink-muted" />
        <span className="font-semibold">{c.id}</span>
        <code className="font-mono text-xs text-ink-muted">{c.model}</code>
        <Badge tone={tone}>{label}</Badge>
        {c.vision && <Badge tone="brand">images</Badge>}
      </div>
      {p.detail && p.state !== 'ready' && (
        <p className="m-0 text-[13.5px] text-ink-muted">
          {p.detail}
          {p.until && ` · back ${formatTime(p.until)}`}
        </p>
      )}
      <p className="tabular m-0 text-[13.5px]">
        Today: {quota(p.requestsToday, c.daily_request_cap, 'requests')} ·{' '}
        {quota(p.tokensToday, c.daily_token_cap, 'tokens')} · resets {formatTime(p.resetAt)}
      </p>
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (draft.trim()) store(draft.trim())
        }}
      >
        <Field
          label={`${c.id} API key`}
          hint={
            p.hasKey
              ? 'Stored. Enter a new key to replace it; it is never shown again.'
              : 'Not set: this provider is skipped.'
          }
          error={err}
        >
          {(a11y) => (
            <Input
              {...a11y}
              className="sm:w-80"
              type="password"
              autoComplete="new-password"
              placeholder={p.hasKey ? '••••••••' : undefined}
              value={draft}
              onChange={(e) => {
                setDraft(e.target.value)
                setErr(undefined)
              }}
            />
          )}
        </Field>
        <Button type="submit" size="sm" disabled={!draft.trim() || key.isPending}>
          <KeyRound aria-hidden />
          Save key
        </Button>
        {p.hasKey && (
          <Button
            type="button"
            size="sm"
            variant="secondary"
            disabled={key.isPending}
            onClick={() => store('')}
          >
            <Trash2 aria-hidden />
            Remove key
          </Button>
        )}
      </form>
    </li>
  )
}

function TestPrompt({ providers }: { providers: string[] }) {
  const test = useLLMTest()
  const [prompt, setPrompt] = useState('Reply with one word: ready?')
  const [provider, setProvider] = useState('')
  const r = test.data

  return (
    <section aria-label="Test prompt" className="flex flex-col gap-2">
      <h2 className="t-label m-0 text-ink-muted">Test prompt</h2>
      <form
        className="flex flex-col gap-2.5 rounded-lg bg-surface p-4 shadow-card"
        onSubmit={(e) => {
          e.preventDefault()
          test.mutate({ prompt, provider: provider || undefined })
        }}
      >
        <Field label="Prompt" hint="A real call: it is metered and counts against today's quota.">
          {(a11y) => (
            <Textarea
              {...a11y}
              rows={3}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
            />
          )}
        </Field>
        <div className="flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1 text-[13px] font-semibold text-ink-muted">
            Provider
            <select
              className="h-9 rounded-md border border-border bg-surface px-2.5 text-[14px] text-ink"
              value={provider}
              onChange={(e) => setProvider(e.target.value)}
            >
              <option value="">Normal routing</option>
              {providers.map((id) => (
                <option key={id} value={id}>
                  {id}
                </option>
              ))}
            </select>
          </label>
          <Button type="submit" size="sm" disabled={!prompt.trim() || test.isPending}>
            <Send aria-hidden />
            {test.isPending ? 'Sending…' : 'Send'}
          </Button>
        </div>
        {test.error && <ErrorNote error={test.error} />}
        {r && !test.error && (
          <div className="flex flex-col gap-1">
            <p className="m-0 whitespace-pre-wrap rounded-md bg-surface-2 px-3.5 py-2.5 text-[14px]">
              {r.text}
            </p>
            <span className="text-xs text-ink-muted">
              {r.provider} · {r.model} · {r.tokensIn} in / {r.tokensOut} out ·{' '}
              {number.format(r.latencyMs)} ms
            </span>
          </div>
        )}
      </form>
    </section>
  )
}

const ranges = [
  { value: '7', label: '7 days' },
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
] as const

function Usage() {
  const [days, setDays] = useState<'7' | '30' | '90'>('7')
  const { data, error } = useLLMUsage(Number(days))
  return (
    <section aria-label="Usage" className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="t-label m-0 text-ink-muted">Usage{data ? ` (days in ${data.tz})` : ''}</h2>
        <Segmented
          aria-label="Range"
          value={days}
          onValueChange={setDays}
          options={[...ranges]}
          className="w-64"
        />
      </div>
      {error && <ErrorNote error={error} />}
      {data && data.rows.length === 0 && <p className="m-0 text-ink-muted">No LLM calls yet.</p>}
      {data && data.rows.length > 0 && (
        <Table>
          <thead>
            <tr>
              <th>Day</th>
              <th>Provider</th>
              <th>Feature</th>
              <th>Requests</th>
              <th>Cache hits</th>
              <th>Failures</th>
              <th>Tokens in / out</th>
              <th>Avg latency</th>
            </tr>
          </thead>
          <tbody className="tabular">
            {data.rows.map((r) => (
              <tr key={`${r.day}-${r.provider}-${r.feature}`}>
                <td className="whitespace-nowrap">{r.day}</td>
                <td>{r.provider}</td>
                <td>
                  <code className="font-mono text-xs">{r.feature}</code>
                </td>
                <td>{number.format(r.requests)}</td>
                <td>{number.format(r.cacheHits)}</td>
                <td className={r.failures ? 'text-expired' : undefined}>
                  {number.format(r.failures)}
                </td>
                <td className="whitespace-nowrap">
                  {number.format(r.tokensIn)} / {number.format(r.tokensOut)}
                </td>
                <td>{number.format(r.avgLatencyMs)} ms</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </section>
  )
}

/** What each feature does, and its manual path when no provider can answer. */
function Features() {
  return (
    <section aria-label="Features" className="flex flex-col gap-2">
      <h2 className="t-label m-0 text-ink-muted">Features</h2>
      <Table>
        <thead>
          <tr>
            <th>Feature</th>
            <th>What it does</th>
            <th>Without AI</th>
          </tr>
        </thead>
        <tbody>
          {llmFeatures.map((f) => (
            <tr key={f.name}>
              <td>
                <code className="font-mono text-xs">{f.name}</code>
              </td>
              <td>{f.what}</td>
              <td>{f.withoutAI}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    </section>
  )
}

/** The gateway's own settings (keys are managed per provider above). */
const isGatewaySetting = (s: Setting) =>
  s.key === 'flag.llm_enabled' || (s.key.startsWith('llm.') && !s.key.startsWith('llm.api_key'))

function Configuration() {
  const { data, error } = useSettings()
  const items = (data?.items ?? []).filter(isGatewaySetting)
  return (
    <Section title="Configuration">
      {error && (
        <li className="px-4 py-3.5">
          <ErrorNote error={error} />
        </li>
      )}
      {items.map((s) => (
        <SettingEditor key={s.key} setting={s} />
      ))}
    </Section>
  )
}
