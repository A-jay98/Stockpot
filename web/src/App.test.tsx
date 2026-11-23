import { render, screen } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'
import { App } from './App'

afterEach(() => {
  vi.unstubAllGlobals()
})

test('shows the server version from /api/health', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({ status: 'ok', version: 'r7', commit: 'abc', builtAt: '', checks: {} }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
    ),
  )
  render(<App />)
  expect(screen.getByRole('heading', { name: 'Stockpot' })).toBeInTheDocument()
  expect(await screen.findByText('Server r7')).toBeInTheDocument()
})

test('says so when the server is down', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      throw new TypeError('offline')
    }),
  )
  render(<App />)
  expect(await screen.findByText('Server unreachable')).toBeInTheDocument()
})
