import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { jsonResponse, renderApp } from '../test-utils'
import type { AdminCatalog } from './catalog-api'

const catalog: AdminCatalog = {
  enabled: true,
  intervalMinutes: 20,
  counts: { pending: 3, done: 40, failed: 1 },
  headroom: false,
  retryAt: '2026-11-28T08:00:00Z',
  runs: [
    {
      id: '3d6f0a2e-0000-4000-8000-000000000001',
      trigger: 'scheduled',
      outcome: 'deferred',
      startedAt: '2026-11-27T08:00:00Z',
      items: 0,
      applied: 0,
      failed: 0,
      cached: 0,
      calls: 0,
      reason: 'no LLM quota above the reserve for background work',
    },
  ],
  llmIngredients: [
    {
      id: '3d6f0a2e-0000-4000-8000-0000000000aa',
      slug: 'kumquat',
      name: 'Kumquat',
      parent: 'Citrus',
      household: 'Home',
      products: 2,
      createdAt: '2026-11-27T07:00:00Z',
    },
  ],
  review: [
    {
      id: '3d6f0a2e-0000-4000-8000-0000000000bb',
      name: 'Zorbo fruit',
      household: 'Home',
      matchSource: 'rule',
      confidence: 0.8,
      state: 'pending',
      attempts: 0,
    },
  ],
}

function mockApi(routes: Record<string, (req: Request) => Response>) {
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

afterEach(() => vi.unstubAllGlobals())

const base = {
  'GET /v1/admin/catalog': () => jsonResponse(catalog),
  'GET /v1/admin/settings': () => jsonResponse({ items: [] }),
}

test('shows the queue, deferred runs and the review lists', async () => {
  mockApi(base)
  renderApp('/admin/catalog')
  expect(await screen.findByRole('region', { name: 'Waiting' })).toHaveTextContent('3')
  expect(screen.getByRole('region', { name: 'Background LLM' })).toHaveTextContent(
    'Waiting for quota',
  )
  const runs = within(screen.getByRole('table', { name: 'Enrichment runs' })).getAllByRole('row')
  expect(runs[1]).toHaveTextContent('deferred')
  expect(screen.getByRole('table', { name: 'LLM ingredients' })).toHaveTextContent('Kumquat')
  expect(screen.getByRole('table', { name: 'Products to review' })).toHaveTextContent('80%')
})

test('Run now, Retry failed, make global and merge call the API', async () => {
  const calls = mockApi({
    ...base,
    'POST /v1/admin/catalog/enrich/run': () => new Response(null, { status: 202 }),
    'POST /v1/admin/catalog/enrich/retry-failed': () => jsonResponse({ retried: 1 }),
    'POST /v1/admin/catalog/ingredients/3d6f0a2e-0000-4000-8000-0000000000aa/promote': () =>
      new Response(null, { status: 204 }),
    'POST /v1/admin/catalog/ingredients/3d6f0a2e-0000-4000-8000-0000000000aa/merge': () =>
      new Response(null, { status: 204 }),
  })
  renderApp('/admin/catalog')
  await screen.findByRole('table', { name: 'LLM ingredients' })
  await userEvent.click(screen.getByRole('button', { name: /Run now/ }))
  await userEvent.click(screen.getByRole('button', { name: 'Retry failed' }))
  await userEvent.click(screen.getByRole('button', { name: 'Make global' }))
  await userEvent.click(screen.getByRole('button', { name: 'Merge…' }))
  await userEvent.type(screen.getByLabelText(/Merge Kumquat into/), 'orange')
  await userEvent.click(screen.getByRole('button', { name: 'Merge' }))
  expect(calls).toEqual(
    expect.arrayContaining([
      'POST /v1/admin/catalog/enrich/run',
      'POST /v1/admin/catalog/enrich/retry-failed',
      'POST /v1/admin/catalog/ingredients/3d6f0a2e-0000-4000-8000-0000000000aa/promote',
      'POST /v1/admin/catalog/ingredients/3d6f0a2e-0000-4000-8000-0000000000aa/merge',
    ]),
  )
})
