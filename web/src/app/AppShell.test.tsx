import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { jsonResponse, renderApp } from '../test-utils'

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      jsonResponse({ status: 'ok', version: 'r7', commit: 'abc', builtAt: '', checks: {} }),
    ),
  )
})
afterEach(() => {
  vi.unstubAllGlobals()
})

test('home shows the tab bar and the server version', async () => {
  renderApp('/')
  expect(screen.getByRole('heading', { name: 'Use soon' })).toBeInTheDocument()
  const tabs = screen.getByRole('navigation', { name: 'Tabs' })
  for (const name of ['Home', 'Inventory', 'Quick add', 'Shopping', 'Recipes']) {
    expect(within(tabs).getByRole('link', { name })).toBeInTheDocument()
  }
  expect(within(tabs).getByRole('link', { name: 'Home' })).toHaveAttribute('aria-current', 'page')
  expect(await screen.findByText('Server r7')).toBeInTheDocument()
})

test('tabs navigate between screens', async () => {
  const user = userEvent.setup()
  const { router } = renderApp('/')
  const tabs = screen.getByRole('navigation', { name: 'Tabs' })
  await user.click(within(tabs).getByRole('link', { name: 'Shopping' }))
  expect(screen.getByRole('heading', { name: 'Shopping' })).toBeInTheDocument()
  expect(router.state.location.pathname).toBe('/shopping')
})

test('says so when the server is down', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      throw new TypeError('offline')
    }),
  )
  renderApp('/')
  expect(await screen.findByText('Server unreachable')).toBeInTheDocument()
})

test('admin route group renders its own layout', async () => {
  renderApp('/admin/audit')
  expect(await screen.findByRole('navigation', { name: 'Admin' })).toBeInTheDocument()
  expect(screen.getByRole('heading', { name: 'Audit log' })).toBeInTheDocument()
})

test('unknown paths show not found', () => {
  renderApp('/nope')
  expect(screen.getByRole('heading', { name: 'Page not found' })).toBeInTheDocument()
})
