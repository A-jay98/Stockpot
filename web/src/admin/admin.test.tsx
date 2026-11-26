import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { jsonResponse, renderApp } from '../test-utils'
import type { Overview, Setting } from './api'

const overview: Overview = {
  version: 'r12',
  commit: 'abcdef123',
  builtAt: '',
  startedAt: '2026-11-23T10:00:00Z',
  uptimeSeconds: 7260,
  database: {
    reachable: true,
    latencyMs: 1.2,
    schemaCurrent: true,
    appVersion: 1,
    appTarget: 1,
    sizeBytes: 9_000_000,
  },
  jobs: { available: 1, scheduled: 2, discarded: 1, completed: 9 },
  errors24h: 3,
  warnings24h: 5,
}

const settings: Setting[] = [
  {
    key: 'flag.voice_input',
    kind: 'bool',
    scope: 'system',
    description: 'Voice add',
    secret: false,
    isSet: false,
    default: false,
    value: false,
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
    options: ['debug', 'info', 'warn', 'error'],
  },
  {
    key: 'retention.app_log_days',
    kind: 'int',
    scope: 'system',
    description: 'Days to keep application log lines.',
    secret: false,
    isSet: false,
    default: 14,
    value: 14,
  },
  {
    key: 'telegram.bot_token',
    kind: 'string',
    scope: 'system',
    description: 'Telegram bot token',
    secret: true,
    isSet: true,
  },
]

type Handler = (req: Request) => Response | Promise<Response>

/** Routes fetch calls by "METHOD /path" (path relative to /api). */
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

const base = {
  'GET /v1/admin/overview': () => jsonResponse(overview),
  'GET /v1/admin/settings': () => jsonResponse({ items: settings }),
}

afterEach(() => vi.unstubAllGlobals())

test('non-admins see an explanation, not the panel', async () => {
  mockApi({
    'GET /v1/admin/overview': () =>
      jsonResponse(
        { title: 'Forbidden', status: 403, code: 'forbidden' },
        403,
        'application/problem+json',
      ),
  })
  renderApp('/admin')
  expect(await screen.findByRole('heading', { name: 'Admins only' })).toBeInTheDocument()
  expect(screen.queryByRole('navigation', { name: 'Admin' })).not.toBeInTheDocument()
})

test('overview shows release, database, jobs and errors', async () => {
  mockApi(base)
  renderApp('/admin')
  const release = await screen.findByRole('region', { name: 'Release' })
  expect(release).toHaveTextContent('r12')
  expect(release).toHaveTextContent('abcdef1')
  expect(screen.getByRole('region', { name: 'Database' })).toHaveTextContent('Healthy')
  expect(screen.getByRole('region', { name: 'Jobs' })).toHaveTextContent('3 queued')
  expect(screen.getByRole('region', { name: 'Errors (24 h)' })).toHaveTextContent('3')
})

test('settings editor: typed inputs per kind, secrets write-only', async () => {
  mockApi(base)
  renderApp('/admin/settings')
  expect(await screen.findByRole('switch', { name: 'Voice add' })).not.toBeChecked()
  expect(screen.getByRole('combobox', { name: 'Log level' })).toHaveValue('info')
  const days = screen.getByRole('textbox', { name: 'Days to keep application log lines.' })
  expect(days).toHaveValue('14')
  expect(days).toHaveAttribute('inputmode', 'numeric')
  const token = screen.getByLabelText('Telegram bot token')
  expect(token).toHaveAttribute('type', 'password')
  expect(token).toHaveValue('')
  expect(screen.getByText(/Stored\. Enter a new value/)).toBeInTheDocument()
})

test('settings editor: client validation blocks a bad int', async () => {
  const user = userEvent.setup()
  const calls = mockApi(base)
  renderApp('/admin/settings')
  const days = await screen.findByRole('textbox', { name: 'Days to keep application log lines.' })
  await user.clear(days)
  await user.type(days, '2 weeks')
  const row = days.closest('li') as HTMLElement
  await user.click(within(row).getByRole('button', { name: 'Save' }))
  expect(within(row).getByText('Enter a whole number.')).toBeInTheDocument()
  expect(days).toHaveAttribute('aria-invalid', 'true')
  expect(calls.some((r) => r.method === 'PUT')).toBe(false)
})

test('settings editor: shows the server validation error, then saves a typed value', async () => {
  const user = userEvent.setup()
  const bodies: unknown[] = []
  mockApi({
    ...base,
    'PUT /v1/admin/settings/retention.app_log_days': async (req) => {
      const body = (await req.json()) as { value: number }
      bodies.push(body)
      if (body.value > 3650) {
        return jsonResponse(
          {
            title: 'Unprocessable Entity',
            status: 422,
            code: 'invalid_value',
            detail: 'must be between 1 and 3650',
          },
          422,
          'application/problem+json',
        )
      }
      return jsonResponse({
        ...settings[2],
        value: body.value,
        isSet: true,
        updatedAt: '2026-11-23T12:00:00Z',
      })
    },
  })
  renderApp('/admin/settings')
  const days = await screen.findByRole('textbox', { name: 'Days to keep application log lines.' })
  const row = days.closest('li') as HTMLElement
  await user.clear(days)
  await user.type(days, '9999{Enter}')
  expect(await within(row).findByText('must be between 1 and 3650')).toBeInTheDocument()

  await user.clear(days)
  await user.type(days, '30')
  expect(within(row).queryByText('must be between 1 and 3650')).not.toBeInTheDocument()
  await user.click(within(row).getByRole('button', { name: 'Save' }))
  expect(await within(row).findByRole('button', { name: 'Reset to default' })).toBeInTheDocument()
  expect(bodies).toEqual([{ value: 9999 }, { value: 30 }]) // numbers, not strings
  expect(days).toHaveValue('30')
})

test('flags toggle immediately', async () => {
  const user = userEvent.setup()
  const bodies: unknown[] = []
  mockApi({
    ...base,
    'PUT /v1/admin/settings/flag.voice_input': async (req) => {
      bodies.push(await req.json())
      return jsonResponse({ ...settings[0], value: true, isSet: true })
    },
  })
  renderApp('/admin/settings')
  const flag = await screen.findByRole('switch', { name: 'Voice add' })
  await user.click(flag)
  await vi.waitFor(() => expect(flag).toBeChecked())
  expect(bodies).toEqual([{ value: true }])
})

test('logs filters live in the URL and reach the API', async () => {
  const user = userEvent.setup()
  const calls = mockApi({
    ...base,
    'GET /v1/admin/logs': () =>
      jsonResponse({
        items: [
          {
            id: 7,
            time: '2026-11-23T12:00:00Z',
            level: 'error',
            msg: 'boom',
            attrs: { route: '/x' },
            requestId: 'r-1',
          },
        ],
      }),
  })
  const { router } = renderApp('/admin/logs?level=error&live=off')
  expect(await screen.findByText('boom')).toBeInTheDocument()
  const first = calls.find((r) => r.url.includes('/v1/admin/logs')) as Request
  expect(new URL(first.url).searchParams.get('level')).toBe('error')

  await user.type(screen.getByRole('searchbox', { name: 'Search text' }), 'timeout')
  await vi.waitFor(() => expect(router.state.location.search).toContain('q=timeout'))
  await vi.waitFor(() =>
    expect(calls.some((r) => new URL(r.url).searchParams.get('q') === 'timeout')).toBe(true),
  )
})

test('jobs list offers retry and cancel by state', async () => {
  const user = userEvent.setup()
  const job = (id: number, state: string) => ({
    id,
    kind: 'retention',
    state,
    queue: 'default',
    attempt: 1,
    maxAttempts: 25,
    priority: 1,
    args: {},
    errors: [],
    createdAt: '2026-11-23T10:00:00Z',
    scheduledAt: '2026-11-23T10:00:00Z',
    tags: [],
  })
  let retried = 0
  mockApi({
    ...base,
    'GET /v1/admin/jobs': () => jsonResponse({ items: [job(1, 'discarded'), job(2, 'running')] }),
    'POST /v1/admin/jobs/1/retry': () => {
      retried++
      return jsonResponse(job(1, 'available'))
    },
  })
  renderApp('/admin/jobs')
  const retry = await screen.findByRole('button', { name: 'Retry job 1' })
  expect(screen.queryByRole('button', { name: 'Cancel job 1' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Retry job 2' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Cancel job 2' })).toBeInTheDocument()
  await user.click(retry)
  await vi.waitFor(() => expect(retried).toBe(1))
})
