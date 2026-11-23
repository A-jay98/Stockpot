import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import { StateBadge } from './badge'

test.each([
  ['fresh', 'Fresh'],
  ['soon', 'Use soon'],
  ['expired', 'Expired'],
  ['low', 'Low'],
] as const)('%s badge has a shape and a label, not only colour', (state, label) => {
  render(<StateBadge state={state} />)
  const badge = screen.getByText(label)
  expect(badge).toHaveAttribute('data-state', state)
  expect(badge.querySelector('svg, span[aria-hidden]')).not.toBeNull()
})

test('the label can be overridden', () => {
  render(<StateBadge state="soon">2 days</StateBadge>)
  expect(screen.getByText('2 days')).toHaveAttribute('data-state', 'soon')
})
