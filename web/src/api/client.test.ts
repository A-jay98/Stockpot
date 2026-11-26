import { afterEach, expect, test, vi } from 'vitest'
import { jsonResponse } from '../test-utils'
import { api, setUnauthorizedHandler, unwrap } from './client'
import { ApiError } from './problem'

afterEach(() => {
  vi.unstubAllGlobals()
})

test('unwrap turns problem+json into an ApiError', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      jsonResponse(
        {
          type: 'about:blank',
          title: 'Service Unavailable',
          status: 503,
          detail: 'db down',
          code: 'db_unavailable',
        },
        503,
        'application/problem+json',
      ),
    ),
  )
  const err = await unwrap(api.GET('/health')).catch((e: unknown) => e)
  expect(err).toBeInstanceOf(ApiError)
  expect(err).toMatchObject({ status: 503, message: 'db down', code: 'db_unavailable' })
})

test('unwrap falls back to the status text for non-problem bodies', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response('gateway', { status: 502, statusText: 'Bad Gateway' })),
  )
  const err = await unwrap(api.GET('/health')).catch((e: unknown) => e)
  expect(err).toMatchObject({ status: 502, problem: { title: 'Bad Gateway', status: 502 } })
})

test('a 401 calls the unauthorized handler', async () => {
  const handler = vi.fn()
  setUnauthorizedHandler(handler)
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      jsonResponse(
        { title: 'Unauthorized', status: 401, code: 'unauthenticated' },
        401,
        'application/problem+json',
      ),
    ),
  )
  await expect(unwrap(api.GET('/health'))).rejects.toMatchObject({ status: 401 })
  expect(handler).toHaveBeenCalledOnce()
})

test('a 401 from a public endpoint (rejected passkey) does not redirect', async () => {
  const handler = vi.fn()
  setUnauthorizedHandler(handler)
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      jsonResponse(
        { title: 'Unauthorized', status: 401, code: 'passkey_rejected' },
        401,
        'application/problem+json',
      ),
    ),
  )
  await expect(unwrap(api.GET('/health'))).rejects.toMatchObject({ code: 'passkey_rejected' })
  expect(handler).not.toHaveBeenCalled()
})
