import createClient from 'openapi-fetch'
import type { paths } from './schema'

// Same origin in production (Caddy routes /api to the server) and in dev
// (Vite proxies it). Absolute, because Request needs one outside a browser.
export const api = createClient<paths>({
  baseUrl: new URL('/api', window.location.origin).href,
  // Looked up per call, so tests and a future offline layer can replace fetch.
  fetch: (request) => globalThis.fetch(request),
})
