import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { jsonResponse, renderApp } from '../../test-utils'
import type { Notification, Preferences, TelegramConnection } from './api'

type Handler = (req: Request) => Response | Promise<Response>

function mockApi(routes: Record<string, Handler>) {
  const calls: { key: string; body: unknown }[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      const key = `${req.method} ${new URL(req.url).pathname.replace(/^\/api/, '')}`
      const text = await req.clone().text()
      calls.push({ key, body: text ? JSON.parse(text) : undefined })
      const h = routes[key]
      return h ? h(req) : jsonResponse({ title: 'Not Found', status: 404 }, 404)
    }),
  )
  return calls
}

afterEach(() => vi.unstubAllGlobals())

const digest: Notification = {
  id: '3d6f0a2e-0000-4000-8000-000000000001',
  householdId: '0190a1b2-0000-7000-8000-0000000000a1',
  kind: 'expiry_digest',
  title: '1 expired · 2 expiring soon',
  body: 'Expired\n• Milk (Fridge), yesterday',
  createdAt: '2026-11-27T13:00:00Z',
  read: false,
}

test('the bell shows unread count, lists the inbox and marks read', async () => {
  const calls = mockApi({
    'GET /v1/notifications': () => jsonResponse({ items: [digest], unread: 1 }),
    'POST /v1/notifications/read': () => new Response(null, { status: 204 }),
  })
  renderApp('/settings')
  const bell = await screen.findByRole('button', { name: 'Notifications, 1 unread' })
  await userEvent.click(bell)
  expect(await screen.findByText('1 expired · 2 expiring soon')).toBeInTheDocument()
  expect(screen.getByText(/Milk \(Fridge\), yesterday/)).toBeInTheDocument()

  await userEvent.click(screen.getByRole('button', { name: /1 expired/ }))
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'POST /v1/notifications/read')?.body).toEqual({
      ids: [digest.id],
    }),
  )
  await userEvent.click(screen.getByRole('button', { name: 'Mark all read' }))
  await waitFor(() =>
    expect(calls.filter((c) => c.key === 'POST /v1/notifications/read').at(-1)?.body).toEqual({}),
  )
})

const prefs: Preferences = {
  digestTime: '08:00',
  leadDays: 2,
  quietStart: '22:00',
  quietEnd: '07:00',
}
const unlinked: TelegramConnection = {
  available: true,
  botUsername: 'stockpot_bot',
  linked: false,
  enabled: false,
}

test('connect Telegram opens the deep link and waits', async () => {
  const open = vi.fn()
  vi.stubGlobal('open', open)
  mockApi({
    'GET /v1/notifications': () => jsonResponse({ items: [], unread: 0 }),
    'GET /v1/notifications/preferences': () => jsonResponse(prefs),
    'GET /v1/notifications/telegram': () => jsonResponse(unlinked),
    'POST /v1/notifications/telegram/link': () =>
      jsonResponse(
        {
          url: 'https://t.me/stockpot_bot?start=abc',
          expiresAt: new Date(Date.now() + 60_000).toISOString(),
        },
        201,
      ),
  })
  renderApp('/settings/notifications')
  await userEvent.click(await screen.findByRole('button', { name: 'Connect Telegram' }))
  expect(open).toHaveBeenCalledWith(
    'https://t.me/stockpot_bot?start=abc',
    '_blank',
    'noopener,noreferrer',
  )
  expect(await screen.findByText(/Waiting for you to press Start/)).toBeInTheDocument()
})

test('without a bot token the connect button is disabled and explains why', async () => {
  mockApi({
    'GET /v1/notifications': () => jsonResponse({ items: [], unread: 0 }),
    'GET /v1/notifications/preferences': () => jsonResponse(prefs),
    'GET /v1/notifications/telegram': () => jsonResponse({ ...unlinked, available: false }),
  })
  renderApp('/settings/notifications')
  expect(await screen.findByText(/isn't set up on this server yet/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Connect Telegram' })).toBeDisabled()
})

test('a connected user can disconnect, and preferences save', async () => {
  const calls = mockApi({
    'GET /v1/notifications': () => jsonResponse({ items: [], unread: 0 }),
    'GET /v1/notifications/preferences': () => jsonResponse(prefs),
    'PUT /v1/notifications/preferences': (r) =>
      r.json().then((b) => jsonResponse({ ...prefs, ...b })),
    'GET /v1/notifications/telegram': () =>
      jsonResponse({ ...unlinked, linked: true, enabled: true, username: 'kim' }),
    'DELETE /v1/notifications/telegram': () => new Response(null, { status: 204 }),
  })
  renderApp('/settings/notifications')
  expect(await screen.findByText('Connected')).toBeInTheDocument()
  expect(screen.getByText(/@kim/)).toBeInTheDocument()

  const days = await screen.findByLabelText(/Expiring soon means within/)
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  await userEvent.clear(days)
  await userEvent.type(days, '5')
  await userEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(calls.find((c) => c.key === 'PUT /v1/notifications/preferences')?.body).toEqual({
      leadDays: 5,
    }),
  )

  await userEvent.click(screen.getByRole('button', { name: 'Disconnect' }))
  await waitFor(() =>
    expect(calls.some((c) => c.key === 'DELETE /v1/notifications/telegram')).toBe(true),
  )
})

test('admin settings show the bot status', async () => {
  mockApi({
    'GET /v1/admin/settings': () => jsonResponse({ items: [] }),
    'GET /v1/admin/telegram': () =>
      jsonResponse({ status: 'ok', botUsername: 'stockpot_bot', linkedChats: 2, polling: true }),
    'GET /v1/notifications': () => jsonResponse({ items: [], unread: 0 }),
  })
  renderApp('/admin/settings')
  expect(await screen.findByText('@stockpot_bot')).toBeInTheDocument()
  expect(screen.getByText('2 linked chats')).toBeInTheDocument()
  expect(screen.getByText('Listening for messages')).toBeInTheDocument()
})
