import createClient, { type Middleware } from 'openapi-fetch'
import { ApiError, toProblem } from './problem'
import type { paths } from './schema'

type UnauthorizedHandler = () => void

// The router registers a handler that navigates to /login without a page reload;
// until then (and in tests) fall back to a full navigation.
let onUnauthorized: UnauthorizedHandler = () => {
  const next = window.location.pathname + window.location.search
  if (window.location.pathname !== '/login') {
    window.location.assign(`/login?next=${encodeURIComponent(next)}`)
  }
}

export function setUnauthorizedHandler(handler: UnauthorizedHandler) {
  onUnauthorized = handler
}

const authRedirect: Middleware = {
  onResponse({ response }) {
    if (response.status === 401) onUnauthorized()
    return undefined
  },
}

// Same origin in production (Caddy routes /api to the server) and in dev
// (Vite proxies it). Absolute, because Request needs one outside a browser.
export const api = createClient<paths>({
  baseUrl: new URL('/api', window.location.origin).href,
  // Looked up per call, so tests and a future offline layer can replace fetch.
  fetch: (request) => globalThis.fetch(request),
})
api.use(authRedirect)

type FetchResult<T> = { data?: T; error?: unknown; response: Response }

/**
 * Turns an openapi-fetch result into data or a thrown ApiError (problem+json).
 * Use it in TanStack Query functions: `queryFn: () => unwrap(api.GET('/health'))`.
 */
export async function unwrap<T>(call: Promise<FetchResult<T>>): Promise<T> {
  const { data, error, response } = await call
  if (!response.ok) throw new ApiError(response.status, toProblem(response, error))
  return data as T
}
