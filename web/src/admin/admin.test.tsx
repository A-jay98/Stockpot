import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { adminMe, jsonResponse, renderApp } from '../test-utils'
import type { Overview, Setting } from './api'
import type { AdminUser } from './users-api'

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
  const calls = mockApi(base)
  renderApp('/admin', { me: { ...adminMe, user: { ...adminMe.user, systemRole: 'user' } } })
  expect(await screen.findByRole('heading', { name: 'Admins only' })).toBeInTheDocument()
  expect(screen.queryByRole('navigation', { name: 'Admin' })).not.toBeInTheDocument()
  expect(calls.some((r) => r.url.includes('/v1/admin/'))).toBe(false)
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

const user = (id: string, over: Partial<AdminUser> = {}): AdminUser => ({
  id,
  displayName: `User ${id}`,
  status: 'active',
  systemRole: 'user',
  createdAt: '2026-11-20T10:00:00Z',
  households: [],
  ...over,
})

const usersBase = {
  ...base,
  'GET /v1/me': () => jsonResponse(adminMe),
  'GET /v1/admin/users': () =>
    jsonResponse([
      user('u1', { displayName: 'Pat', status: 'pending' }),
      user('u2', {
        displayName: 'Sam',
        households: [{ householdId: 'h1', name: 'Home', role: 'owner' }],
      }),
    ]),
  'GET /v1/invites': () => jsonResponse([]),
  'GET /v1/admin/households': () =>
    jsonResponse([{ id: 'h1', name: 'Home', createdAt: '2026-11-20T10:00:00Z', memberCount: 2 }]),
}

test('users: approve at once, reject only after confirming', async () => {
  const ue = userEvent.setup()
  const calls = mockApi({
    ...usersBase,
    'POST /v1/admin/users/u1/approve': () => jsonResponse(user('u1')),
    'POST /v1/admin/users/u1/reject': () => jsonResponse(user('u1', { status: 'disabled' })),
  })
  renderApp('/admin/users')
  const pending = await screen.findByRole('region', { name: 'Waiting for approval (1)' })
  expect(within(pending).getByText('Pat')).toBeInTheDocument()
  expect(
    within(screen.getByRole('table', { name: 'Users' })).getByText('Home (owner)'),
  ).toBeInTheDocument()

  await ue.click(within(pending).getByRole('button', { name: 'Reject Pat' }))
  const dialog = await screen.findByRole('dialog', { name: 'Reject Pat?' })
  expect(calls.some((r) => r.url.endsWith('/reject'))).toBe(false)
  await ue.click(within(dialog).getByRole('button', { name: 'Reject' }))
  await vi.waitFor(() => expect(calls.some((r) => r.url.endsWith('/u1/reject'))).toBe(true))

  await ue.click(within(pending).getByRole('button', { name: 'Approve Pat' }))
  await vi.waitFor(() => expect(calls.some((r) => r.url.endsWith('/u1/approve'))).toBe(true))
})

test('users: manage dialog changes the role and revokes a session', async () => {
  const ue = userEvent.setup()
  const patches: unknown[] = []
  let revoked = false
  mockApi({
    ...usersBase,
    'GET /v1/admin/users/u2/sessions': () =>
      jsonResponse([
        {
          id: 's1',
          createdAt: '2026-11-20T10:00:00Z',
          lastSeenAt: '2026-11-23T10:00:00Z',
          expiresAt: '2026-12-20T10:00:00Z',
          ip: '10.0.0.2',
          userAgent: 'Firefox on Linux',
          current: false,
        },
      ]),
    'PATCH /v1/admin/users/u2': async (req) => {
      patches.push(await req.json())
      return jsonResponse(user('u2', { systemRole: 'admin' }))
    },
    'DELETE /v1/admin/sessions/s1': () => {
      revoked = true
      return new Response(null, { status: 204 })
    },
  })
  renderApp('/admin/users')
  await ue.click(await screen.findByRole('button', { name: 'Manage Sam' }))
  const dialog = await screen.findByRole('dialog', { name: 'Sam' })
  await ue.click(within(dialog).getByRole('radio', { name: 'Admin' }))
  await vi.waitFor(() => expect(patches).toEqual([{ systemRole: 'admin' }]))
  await ue.click(await within(dialog).findByRole('button', { name: 'Revoke session s1' }))
  await vi.waitFor(() => expect(revoked).toBe(true))
})

test('users: a new invite shows its link once', async () => {
  const ue = userEvent.setup()
  const bodies: unknown[] = []
  mockApi({
    ...usersBase,
    'POST /v1/invites': async (req) => {
      bodies.push(await req.json())
      return jsonResponse({ invite: {}, code: 'abc', url: 'https://stockpot.test/join#abc' }, 201)
    },
  })
  renderApp('/admin/users')
  await ue.click(await screen.findByRole('button', { name: 'Create invite' }))
  const dialog = await screen.findByRole('dialog', { name: 'Create invite' })
  await vi.waitFor(() =>
    expect(within(dialog).getByRole('combobox', { name: 'Household' })).toHaveValue('h1'),
  )
  await ue.type(within(dialog).getByRole('textbox', { name: 'Note' }), 'for Alex')
  await ue.click(within(dialog).getByRole('button', { name: 'Create invite' }))
  const ready = await screen.findByRole('dialog', { name: 'Invite ready' })
  expect(within(ready).getByRole('textbox', { name: 'Invite link' })).toHaveValue(
    'https://stockpot.test/join#abc',
  )
  expect(bodies).toEqual([{ householdId: 'h1', role: 'member', note: 'for Alex' }])
})

const auditEvent = {
  id: 9,
  time: '2026-11-23T12:00:00Z',
  actorId: 'u2',
  actorName: 'Sam',
  action: 'setting.update',
  targetType: 'setting',
  targetId: 'log.level',
  before: { set: false },
  after: { set: true, value: 'debug' },
  requestId: 'req-9',
  ip: '10.0.0.2',
}

test('audit log: URL filters reach the API, rows expand to before/after', async () => {
  const ue = userEvent.setup()
  const calls = mockApi({
    ...usersBase,
    'GET /v1/admin/audit': () => jsonResponse({ items: [auditEvent] }),
  })
  renderApp('/admin/audit?action=setting&targetType=setting&actor=u2')
  expect(await screen.findByText('setting.update')).toBeInTheDocument()
  const q = new URL(calls.find((r) => r.url.includes('/v1/admin/audit'))?.url ?? '').searchParams
  expect(q.get('action')).toBe('setting')
  expect(q.get('targetType')).toBe('setting')
  expect(q.get('actorId')).toBe('u2')

  await ue.click(screen.getByRole('button', { name: 'Details of event 9' }))
  expect(screen.getByRole('region', { name: 'After' })).toHaveTextContent('"debug"')
  expect(screen.getByRole('link', { name: 'req-9' })).toHaveAttribute(
    'href',
    '/admin/logs?requestId=req-9&live=off',
  )
})

test('access log: status class and method filters reach the API', async () => {
  const calls = mockApi({
    ...usersBase,
    'GET /v1/admin/access': () =>
      jsonResponse({
        items: [
          {
            id: 1,
            time: '2026-11-23T12:00:00Z',
            requestId: 'req-1',
            userName: 'Sam',
            userId: 'u2',
            method: 'DELETE',
            route: '/api/v1/admin/sessions/{sessionId}',
            path: '/api/v1/admin/sessions/s1',
            status: 404,
            durationMs: 3,
            userAgent: 'curl',
          },
        ],
      }),
  })
  renderApp('/admin/access?status=4xx&method=DELETE')
  expect(await screen.findByText('404')).toBeInTheDocument()
  const q = new URL(calls.find((r) => r.url.includes('/v1/admin/access'))?.url ?? '').searchParams
  expect(q.get('status')).toBe('4xx')
  expect(q.get('method')).toBe('DELETE')
})

test('settings: history lists the changes to one setting', async () => {
  const ue = userEvent.setup()
  const calls = mockApi({
    ...base,
    'GET /v1/admin/audit': () => jsonResponse({ items: [auditEvent] }),
  })
  renderApp('/admin/settings')
  await ue.click(await screen.findByRole('button', { name: 'History of log.level' }))
  expect(await screen.findByText('setting.update')).toBeInTheDocument()
  const q = new URL(calls.find((r) => r.url.includes('/v1/admin/audit'))?.url ?? '').searchParams
  expect(q.get('targetType')).toBe('setting')
  expect(q.get('targetId')).toBe('log.level')
})

test('users: your own role is locked and your current session has no Revoke', async () => {
  const ue = userEvent.setup()
  mockApi({
    ...usersBase,
    'GET /v1/admin/users': () =>
      jsonResponse([user(adminMe.user.id, { displayName: 'AJ', systemRole: 'admin' })]),
    [`GET /v1/admin/users/${adminMe.user.id}/sessions`]: () =>
      jsonResponse([
        {
          id: 'mine',
          createdAt: '2026-11-20T10:00:00Z',
          lastSeenAt: '2026-11-23T10:00:00Z',
          expiresAt: '2026-12-20T10:00:00Z',
          userAgent: 'Firefox',
          current: true,
        },
      ]),
  })
  renderApp('/admin/users')
  await ue.click(await screen.findByRole('button', { name: 'Manage AJ' }))
  const dialog = await screen.findByRole('dialog', { name: 'AJ' })
  expect(within(dialog).getByRole('radio', { name: 'User' })).toBeDisabled()
  expect(await within(dialog).findByText('this session')).toBeInTheDocument()
  expect(
    within(dialog).queryByRole('button', { name: 'Revoke session mine' }),
  ).not.toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: 'Disable account' })).toBeDisabled()
})

test('audit log: an invalid action says so instead of listing everything', async () => {
  const calls = mockApi({
    ...usersBase,
    'GET /v1/admin/audit': () => jsonResponse({ items: [auditEvent] }),
  })
  renderApp('/admin/audit?action=setting.')
  expect(await screen.findByText(/Not a valid action/)).toBeInTheDocument()
  expect(screen.queryByRole('table', { name: 'Audit events' })).not.toBeInTheDocument()
  expect(calls.some((r) => r.url.includes('/v1/admin/audit'))).toBe(false)
})
