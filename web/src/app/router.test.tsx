import { afterEach, expect, test, vi } from 'vitest'
import { api } from '../api/client'
import { jsonResponse } from '../test-utils'
import { createRouter } from './router'

afterEach(() => {
  vi.unstubAllGlobals()
  window.history.replaceState(null, '', '/')
})

test('a 401 from the API sends the user to sign-in and keeps where they were', async () => {
  window.history.replaceState(null, '', '/shopping?store=costco')
  const router = createRouter()
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => jsonResponse({ title: 'Unauthorized', status: 401 }, 401)),
  )
  await api.GET('/health')
  await vi.waitFor(() => expect(router.state.location.pathname).toBe('/login'))
  expect(new URLSearchParams(router.state.location.search).get('next')).toBe(
    '/shopping?store=costco',
  )
  router.dispose()
})
