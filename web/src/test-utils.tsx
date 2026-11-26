import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { routes } from './app/router'
import { type Me, meKey } from './features/auth/session'
import { Toaster } from './ui'

/** A signed-in admin who owns one household. */
export const adminMe: Me = {
  user: {
    id: '0190a1b2-0000-7000-8000-000000000001',
    displayName: 'AJ',
    status: 'active',
    systemRole: 'admin',
    createdAt: '2026-11-23T00:00:00Z',
  },
  households: [
    { householdId: '0190a1b2-0000-7000-8000-0000000000a1', name: 'Home', role: 'owner' },
    { householdId: '0190a1b2-0000-7000-8000-0000000000a2', name: 'Cabin', role: 'member' },
  ],
  activeHouseholdId: '0190a1b2-0000-7000-8000-0000000000a1',
}

/**
 * Renders the real route tree at `path` with a fresh, retry-free query client.
 * `me` seeds the session (default: a signed-in admin; null: signed out).
 */
export function renderApp(path = '/', { me = adminMe }: { me?: Me | null } = {}) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  queryClient.setQueryData(meKey, me)
  const result = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      <Toaster />
    </QueryClientProvider>,
  )
  return { router, queryClient, ...result }
}

export function jsonResponse(body: unknown, status = 200, contentType = 'application/json') {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': contentType } })
}
