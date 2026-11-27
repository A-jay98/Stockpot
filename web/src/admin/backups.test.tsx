import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { jsonResponse, renderApp } from '../test-utils'
import type { Backups } from './backups-api'

const backups: Backups = {
  enabled: true,
  configured: true,
  time: '03:30',
  timezone: 'America/Toronto',
  nextRunAt: '2026-11-28T08:30:00Z',
  keep: 14,
  hasToken: true,
  chatId: '-1001234567890',
  hasRecipient: true,
  recipient: 'age1abcdefghijklmnopqrstuvwxyz',
  channel: { status: 'ok', botUsername: 'stockpot_bot' },
  runs: [
    {
      id: '3d6f0a2e-0000-4000-8000-000000000003',
      trigger: 'scheduled',
      status: 'failed',
      startedAt: '2026-11-27T08:30:00Z',
      sizeBytes: 0,
      parts: 0,
      error: 'upload failed: telegram sendDocument: 400 chat not found',
      pruned: true,
    },
    {
      id: '3d6f0a2e-0000-4000-8000-000000000002',
      trigger: 'manual',
      status: 'ok',
      startedAt: '2026-11-26T20:00:00Z',
      finishedAt: '2026-11-26T20:01:00Z',
      sizeBytes: 5_000_000,
      parts: 3,
      appVersion: 'r12',
      schemaVersion: 4,
      pruned: false,
    },
  ],
}

type Handler = (req: Request) => Response | Promise<Response>

function mockApi(routes: Record<string, Handler>) {
  const calls: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      const path = new URL(req.url).pathname.replace(/^\/api/, '')
      calls.push(`${req.method} ${path}`)
      const h = routes[`${req.method} ${path}`]
      return h ? h(req) : jsonResponse({ title: 'Not Found', status: 404 }, 404)
    }),
  )
  return calls
}

const base: Record<string, Handler> = {
  'GET /v1/admin/backups': () => jsonResponse(backups),
  'GET /v1/admin/settings': () =>
    jsonResponse({
      items: [
        {
          key: 'backup.keep',
          kind: 'int',
          scope: 'system',
          description: 'How many backups to keep in the channel.',
          secret: false,
          isSet: false,
          default: 14,
          value: 14,
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

test('shows status, runs with a failure alert, and only backup settings', async () => {
  mockApi(base)
  renderApp('/admin/backups')
  expect(await screen.findByRole('alert')).toHaveTextContent('chat not found')
  expect(screen.getByRole('region', { name: 'Telegram channel' })).toHaveTextContent(
    '@stockpot_bot',
  )
  expect(screen.getByRole('region', { name: 'Schedule' })).toHaveTextContent('Nightly 03:30')
  const rows = within(screen.getByRole('table', { name: 'Backup runs' })).getAllByRole('row')
  expect(rows).toHaveLength(3) // header + 2
  expect(rows[2]).toHaveTextContent('r12')
  expect(await screen.findByText('How many backups to keep in the channel.')).toBeInTheDocument()
  expect(screen.queryByText('Log level')).not.toBeInTheDocument()
})

test('Back up now queues a run; disabled until configured', async () => {
  const calls = mockApi({
    ...base,
    'POST /v1/admin/backups/run': () => new Response(null, { status: 202 }),
  })
  renderApp('/admin/backups')
  await screen.findByRole('table', { name: 'Backup runs' })
  await userEvent.click(screen.getByRole('button', { name: 'Back up now' }))
  expect(calls).toContain('POST /v1/admin/backups/run')
})

test('Back up now is disabled while the channel is not configured', async () => {
  mockApi({
    ...base,
    'GET /v1/admin/backups': () =>
      jsonResponse({
        ...backups,
        configured: false,
        hasToken: false,
        channel: { status: 'not_configured', detail: 'No bot token set (telegram.bot_token).' },
        runs: [],
      }),
  })
  renderApp('/admin/backups')
  expect(await screen.findByText('No backups yet.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Back up now' })).toBeDisabled()
})
