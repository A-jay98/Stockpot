import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { jsonResponse, renderApp } from '../test-utils'
import { setTheme } from './theme'

afterEach(() => {
  setTheme('system')
  vi.unstubAllGlobals()
})

test('the account sheet switches and remembers the theme', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      jsonResponse({ status: 'ok', version: 'r1', commit: '', builtAt: '', checks: {} }),
    ),
  )
  const user = userEvent.setup()
  renderApp('/')
  await user.click(screen.getAllByRole('button', { name: 'Account' })[0] as HTMLElement)
  await user.click(await screen.findByRole('tab', { name: 'Dark' }))
  expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
  expect(localStorage.getItem('stockpot.theme')).toBe('dark')
  await user.click(screen.getByRole('tab', { name: 'System' }))
  expect(document.documentElement).not.toHaveAttribute('data-theme')
})
