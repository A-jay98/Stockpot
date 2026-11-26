import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { adminMe, jsonResponse, renderApp } from '../../test-utils'
import { base64urlToBuffer, bufferToBase64url, creationOptions } from './webauthn'

type Route = (url: URL, init: Request) => Response | Promise<Response>
let routes: Record<string, Route>

beforeEach(() => {
  routes = {
    'GET /api/health': () =>
      jsonResponse({ status: 'ok', version: 'r7', commit: 'abc', builtAt: '', checks: {} }),
    'GET /api/v1/setup': () => jsonResponse({ needed: false, tokenConfigured: true }),
  }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      const url = new URL(req.url)
      const route = routes[`${req.method} ${url.pathname}`]
      if (!route) return jsonResponse({ title: 'Not Found', status: 404, code: 'not_found' }, 404)
      return route(url, req)
    }),
  )
})
afterEach(() => {
  vi.unstubAllGlobals()
})

test('signed out: app screens send you to sign-in and remember where you were', async () => {
  const { router } = renderApp('/shopping', { me: null })
  expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
  expect(router.state.location.pathname).toBe('/login')
  expect(new URLSearchParams(router.state.location.search).get('next')).toBe('/shopping')
})

test('signed out before setup: app screens go to first-run setup', async () => {
  routes['GET /api/v1/setup'] = () => jsonResponse({ needed: true, tokenConfigured: true })
  const { router } = renderApp('/', { me: null })
  expect(await screen.findByRole('heading', { name: 'Set up Stockpot' })).toBeInTheDocument()
  expect(router.state.location.pathname).toBe('/setup')
})

test('pending users only see the waiting room', async () => {
  const pending = { ...adminMe, user: { ...adminMe.user, status: 'pending' as const } }
  routes['GET /api/v1/me'] = () => jsonResponse(pending)
  const { router } = renderApp('/', { me: pending })
  expect(await screen.findByRole('heading', { name: 'Waiting for approval' })).toBeInTheDocument()
  expect(router.state.location.pathname).toBe('/pending')
})

test('signed out: the admin panel asks for sign-in too', async () => {
  const { router } = renderApp('/admin/users', { me: null })
  await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
})

test('a rejected passkey shows an error on the sign-in page', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('PublicKeyCredential', class {})
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: {
      get: async () => ({
        id: 'abc',
        rawId: new Uint8Array([1]).buffer,
        type: 'public-key',
        authenticatorAttachment: 'platform',
        getClientExtensionResults: () => ({}),
        response: {
          clientDataJSON: new Uint8Array([2]).buffer,
          authenticatorData: new Uint8Array([3]).buffer,
          signature: new Uint8Array([4]).buffer,
          userHandle: new Uint8Array([5]).buffer,
        },
      }),
    },
  })
  routes['POST /api/v1/auth/login/begin'] = () =>
    jsonResponse({
      ceremonyId: '0190a1b2-0000-7000-8000-00000000c001',
      expiresAt: '2026-11-23T00:05:00Z',
      options: { challenge: 'AAEC', rpId: 'localhost', userVerification: 'required' },
    })
  routes['POST /api/v1/auth/login/finish'] = () =>
    jsonResponse(
      {
        title: 'Unauthorized',
        status: 401,
        code: 'passkey_rejected',
        detail: "That passkey wasn't accepted.",
      },
      401,
      'application/problem+json',
    )
  const { router } = renderApp('/login', { me: null })
  await user.click(screen.getByRole('button', { name: 'Sign in with a passkey' }))
  expect(await screen.findByRole('alert')).toHaveTextContent("That passkey wasn't accepted.")
  expect(router.state.location.pathname).toBe('/login')
})

test('switching household from the account sheet', async () => {
  const user = userEvent.setup()
  const cabin = adminMe.households[1]?.householdId ?? ''
  let sent: unknown
  routes['PUT /api/v1/me/household'] = async (_u, req) => {
    sent = await req.json()
    return jsonResponse({ ...adminMe, activeHouseholdId: cabin })
  }
  renderApp('/')
  await user.click(screen.getAllByRole('button', { name: 'Account' })[0] as HTMLElement)
  await user.click(await screen.findByRole('radio', { name: 'Cabin' }))
  await waitFor(() => expect(sent).toEqual({ householdId: cabin }))
  expect(await screen.findByRole('radio', { name: 'Cabin' })).toHaveAttribute(
    'aria-checked',
    'true',
  )
})

test('base64url helpers round-trip and decode creation options', () => {
  const bytes = new Uint8Array([0, 250, 251, 252, 253, 254, 255])
  expect(new Uint8Array(base64urlToBuffer(bufferToBase64url(bytes)))).toEqual(bytes)
  const opts = creationOptions({
    challenge: 'AAEC',
    rp: { id: 'stockpot.test', name: 'Stockpot' },
    user: { id: '-_8', name: 'AJ', displayName: 'AJ' },
    pubKeyCredParams: [],
    excludeCredentials: [{ id: 'AQ', type: 'public-key' }],
  })
  expect(new Uint8Array(opts.challenge as ArrayBuffer)).toEqual(new Uint8Array([0, 1, 2]))
  expect(new Uint8Array(opts.user.id as ArrayBuffer)).toEqual(new Uint8Array([251, 255]))
  expect(opts.excludeCredentials?.[0]?.id).toBeInstanceOf(ArrayBuffer)
})
