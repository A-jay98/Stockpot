import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { jsonResponse, renderApp } from '../test-utils'
import type { LLMStatus, Overview } from './api'

const overview = {
  version: 'r12',
  commit: 'abc',
  builtAt: '',
  startedAt: '2026-11-23T10:00:00Z',
  uptimeSeconds: 60,
  database: {
    reachable: true,
    latencyMs: 1,
    schemaCurrent: true,
    appVersion: 2,
    appTarget: 2,
    sizeBytes: 1,
  },
  jobs: {},
  errors24h: 0,
  warnings24h: 0,
} satisfies Overview

const provider = (id: string, over: Partial<LLMStatus['providers'][number]> = {}) => ({
  config: {
    id,
    kind: 'openai_compat',
    base_url: `https://${id}.example/v1`,
    model: `${id}-model`,
    vision: id === 'gemini',
    json_schema: true,
    enabled: true,
    daily_request_cap: 450,
    daily_token_cap: 0,
    reset_tz: 'America/Los_Angeles',
  },
  hasKey: false,
  state: 'no_key',
  detail: 'no API key',
  requestsToday: 0,
  tokensToday: 0,
  resetAt: '2026-11-24T08:00:00Z',
  ...over,
})

const status: LLMStatus = {
  enabled: true,
  available: true,
  vision: true,
  tz: 'America/Toronto',
  requestsToday: 12,
  tokensToday: 3400,
  globalRequestCap: 1000,
  globalRemaining: 988,
  resetAt: '2026-11-24T05:00:00Z',
  providers: [
    provider('gemini', { hasKey: true, state: 'ready', detail: undefined, requestsToday: 12 }),
    provider('groq'),
  ],
}

type Handler = (req: Request) => Response | Promise<Response>

function mockApi(routes: Record<string, Handler>) {
  const calls: Request[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      calls.push(req.clone())
      const path = new URL(req.url).pathname.replace(/^\/api/, '')
      const h = routes[`${req.method} ${path}`]
      return h ? h(req) : jsonResponse({ title: 'Not Found', status: 404 }, 404)
    }),
  )
  return calls
}

const base: Record<string, Handler> = {
  'GET /v1/admin/overview': () => jsonResponse(overview),
  'GET /v1/admin/llm/status': () => jsonResponse(status),
  'GET /v1/admin/llm/usage': () =>
    jsonResponse({
      tz: 'America/Toronto',
      rows: [
        {
          day: '2026-11-23',
          provider: 'gemini',
          feature: 'recipe_parse',
          requests: 12,
          cacheHits: 3,
          failures: 1,
          tokensIn: 3000,
          tokensOut: 400,
          avgLatencyMs: 820,
        },
      ],
    }),
  'GET /v1/admin/settings': () =>
    jsonResponse({
      items: [
        {
          key: 'llm.global_daily_request_cap',
          kind: 'int',
          scope: 'system',
          description: 'Maximum LLM calls per day across all providers (0 = no cap).',
          secret: false,
          isSet: false,
          default: 1000,
          value: 1000,
        },
        {
          key: 'llm.api_key.gemini',
          kind: 'string',
          scope: 'system',
          description: 'API key of an LLM provider.',
          secret: true,
          isSet: true,
        },
        {
          key: 'log.level',
          kind: 'string',
          scope: 'system',
          description: 'Log level',
          secret: false,
          isSet: false,
          default: 'info',
          value: 'info',
        },
      ],
    }),
}

afterEach(() => vi.unstubAllGlobals())

test('shows providers, quota, usage and only gateway settings', async () => {
  mockApi(base)
  renderApp('/admin/llm')
  const gemini = await screen.findByRole('listitem', { name: 'gemini' })
  expect(within(gemini).getByText('Ready')).toBeInTheDocument()
  expect(within(gemini).getByText(/12 \/ 450 requests/)).toBeInTheDocument()
  expect(
    within(screen.getByRole('listitem', { name: 'groq' })).getByText('No key'),
  ).toBeInTheDocument()
  expect(screen.getByRole('region', { name: 'Requests today' })).toHaveTextContent('12 / 1,000')
  expect(await screen.findByRole('cell', { name: 'recipe_parse' })).toBeInTheDocument()
  expect(
    await screen.findByRole('textbox', { name: /Maximum LLM calls per day/ }),
  ).toBeInTheDocument()
  expect(screen.queryByRole('combobox', { name: 'Log level' })).not.toBeInTheDocument()
  expect(screen.queryByText('llm.api_key.gemini')).not.toBeInTheDocument()
  const features = screen.getByRole('region', { name: 'Features' })
  expect(
    within(features).getByRole('cell', { name: 'Edit the recipe by hand.' }),
  ).toBeInTheDocument()
})

test('saves a provider key write-only', async () => {
  const user = userEvent.setup()
  const bodies: unknown[] = []
  mockApi({
    ...base,
    'PUT /v1/admin/llm/providers/groq/key': async (req) => {
      bodies.push(await req.json())
      return new Response(null, { status: 204 })
    },
  })
  renderApp('/admin/llm')
  const input = await screen.findByLabelText('groq API key')
  expect(input).toHaveAttribute('type', 'password')
  await user.type(input, 'gsk_test{Enter}')
  expect(await screen.findByText('Saved the groq key')).toBeInTheDocument()
  expect(bodies).toEqual([{ apiKey: 'gsk_test' }])
  expect(input).toHaveValue('')
})

test('test prompt shows the reply, or why the gateway is unavailable', async () => {
  const user = userEvent.setup()
  let unavailable = false
  mockApi({
    ...base,
    'POST /v1/admin/llm/test': () =>
      unavailable
        ? jsonResponse(
            {
              title: 'Service Unavailable',
              status: 503,
              code: 'llm_unavailable',
              detail: 'llm unavailable: groq: no API key',
            },
            503,
            'application/problem+json',
          )
        : jsonResponse({
            text: 'ready',
            provider: 'gemini',
            model: 'gemini-model',
            tokensIn: 5,
            tokensOut: 1,
            latencyMs: 640,
          }),
  })
  renderApp('/admin/llm')
  const send = await screen.findByRole('button', { name: 'Send' })
  await user.click(send)
  expect(await screen.findByText('ready')).toBeInTheDocument()
  expect(screen.getByText(/gemini · gemini-model · 5 in \/ 1 out/)).toBeInTheDocument()

  unavailable = true
  await user.click(send)
  expect(await screen.findByText(/groq: no API key/)).toBeInTheDocument()
})
