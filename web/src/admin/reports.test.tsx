import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { ReportSheet } from '../features/reports/ReportSheet'
import { jsonResponse, renderApp } from '../test-utils'
import type { AdminReports, CatalogDefaults } from './reports-api'

const reports: AdminReports = {
  enabled: true,
  autoApply: true,
  intervalMinutes: 30,
  counts: { open: 1, needs_admin: 1, applied: 2, rejected: 0, dismissed: 0 },
  headroom: true,
  reports: [
    {
      id: '3d6f0a2e-0000-4000-8000-000000000001',
      createdAt: '2026-11-27T08:00:00Z',
      household: 'Home',
      reporter: 'Sam',
      kind: 'wrong_unit',
      source: 'shopping_item',
      status: 'needs_admin',
      suggested: 'bag',
      context: {
        productName: 'Salt',
        ingredientName: 'Salt',
        ingredientSlug: 'salt',
        global: true,
        unit: 'g',
        qty: '500',
      },
      attempts: 0,
      llmVerdict: 'approve',
      llmValue: 'bag',
      llmConfidence: 0.6,
      llmReason: 'Salt is sold in bags',
    },
  ],
}

const defaults: CatalogDefaults = {
  ingredients: [
    {
      id: '3d6f0a2e-0000-4000-8000-0000000000aa',
      slug: 'salt',
      name: 'Salt',
      source: 'seed',
      unit: 'bag',
      products: 3,
      openReports: 1,
    },
  ],
  history: [
    {
      time: '2026-11-27T09:00:00Z',
      targetType: 'ingredient',
      targetId: '3d6f0a2e-0000-4000-8000-0000000000aa',
      name: 'Salt',
      before: 'g',
      after: 'bag',
      actor: 'system:report_review',
      reportId: 'x',
    },
  ],
}

function mockApi(routes: Record<string, (req: Request) => Response | Promise<Response>>) {
  const calls: { key: string; body: unknown }[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (req: Request) => {
      const path = new URL(req.url).pathname.replace(/^\/api/, '')
      const key = `${req.method} ${path}`
      const text = req.method === 'GET' ? '' : await req.clone().text()
      calls.push({ key, body: text ? JSON.parse(text) : undefined })
      const h = routes[key]
      return h ? h(req) : jsonResponse({ title: 'Not Found', status: 404 }, 404)
    }),
  )
  return calls
}

afterEach(() => vi.unstubAllGlobals())

test('reports page shows the LLM verdict and applies a unit', async () => {
  const calls = mockApi({
    'GET /v1/admin/reports': () => jsonResponse(reports),
    'GET /v1/admin/settings': () => jsonResponse({ items: [] }),
    'POST /v1/admin/reports/3d6f0a2e-0000-4000-8000-000000000001/resolve': () =>
      new Response(null, { status: 204 }),
  })
  renderApp('/admin/reports')
  const card = await screen.findByRole('listitem', { name: 'Report: Salt' })
  expect(card).toHaveTextContent('approve → bag (60%)')
  expect(card).toHaveTextContent('500 g')
  expect(screen.getByRole('region', { name: 'Needs you' })).toHaveTextContent('1')
  await userEvent.click(within(card).getByRole('button', { name: 'Apply' }))
  expect(calls.find((c) => c.key.endsWith('/resolve'))?.body).toEqual({
    action: 'apply',
    value: 'bag',
  })
})

test('defaults page lists units and history and saves a change', async () => {
  const calls = mockApi({
    'GET /v1/admin/catalog/defaults': () => jsonResponse(defaults),
    'PUT /v1/admin/catalog/ingredients/3d6f0a2e-0000-4000-8000-0000000000aa/default-unit': () =>
      new Response(null, { status: 204 }),
  })
  renderApp('/admin/defaults')
  const select = await screen.findByLabelText('Unit for Salt')
  expect(screen.getByRole('table', { name: 'Default changes' })).toHaveTextContent('g → bag')
  await userEvent.selectOptions(select, 'pack')
  expect(calls.find((c) => c.key.startsWith('PUT'))?.body).toEqual({ unit: 'pack' })
})

test('report sheet sends the kind, suggestion and note', async () => {
  const calls = mockApi({
    'POST /v1/reports': () => jsonResponse({ id: '3d6f0a2e-0000-4000-8000-000000000009' }, 201),
  })
  const onClose = vi.fn()
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ReportSheet
        open
        onClose={onClose}
        source="shopping_item"
        subjectId="3d6f0a2e-0000-4000-8000-000000000002"
        title="Salt"
        unit="g"
      />
    </QueryClientProvider>,
  )
  await userEvent.selectOptions(screen.getByLabelText(/It shows "g"/), 'bag')
  await userEvent.type(screen.getByLabelText('Note (optional)'), 'comes in a bag')
  await userEvent.click(screen.getByRole('button', { name: 'Send report' }))
  await vi.waitFor(() => expect(onClose).toHaveBeenCalled())
  expect(calls[0]?.body).toEqual({
    kind: 'wrong_unit',
    source: 'shopping_item',
    subjectId: '3d6f0a2e-0000-4000-8000-000000000002',
    suggested: 'bag',
    note: 'comes in a bag',
  })
})
