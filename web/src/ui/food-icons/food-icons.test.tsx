import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { FoodIcon } from './FoodIcon'
import { setColourfulIcons } from './preference'
import { FALLBACK_ICON, iconRegistry, isIconKey, resolveFoodIcon } from './registry'
import flatRaw from './sprites/flat.svg?raw'
import highContrastRaw from './sprites/high-contrast.svg?raw'

const CATEGORY_SLUGS = [
  'produce-fruit',
  'produce-veg',
  'herbs',
  'dairy-eggs',
  'meat',
  'seafood',
  'deli',
  'bakery',
  'pantry',
  'baking',
  'spices-condiments',
  'oils-sauces',
  'frozen',
  'snacks-sweets',
  'drinks',
  'household',
  'personal-care',
  'other',
]

describe('registry', () => {
  it('has a category icon for every seeded category and a basket fallback', () => {
    for (const slug of CATEGORY_SLUGS) expect(isIconKey(`cat-${slug}`), slug).toBe(true)
    expect(isIconKey(FALLBACK_ICON)).toBe(true)
  })
  it('keys sets consistently', () => {
    for (const [k, e] of Object.entries(iconRegistry)) {
      expect(k.startsWith('tb-') ? e.set === 'tb' : true, k).toBe(true)
      expect(['fe', 'tb']).toContain(e.set)
    }
  })
  it('every key exists in both committed sprites', () => {
    for (const [style, svg] of [
      ['high-contrast', highContrastRaw],
      ['flat', flatRaw],
    ]) {
      for (const k of Object.keys(iconRegistry))
        expect(svg, `${style}:${k}`).toContain(`id="fi-${k}"`)
    }
  })
})

describe('resolveFoodIcon', () => {
  it('walks product → ingredient → ancestors → category → basket', () => {
    expect(resolveFoodIcon({ product: 'avocado', ingredient: 'tangerine' })).toBe('avocado')
    expect(
      resolveFoodIcon({ product: 'nope', ingredient: null, ancestors: [undefined, 'garlic'] }),
    ).toBe('garlic')
    expect(resolveFoodIcon({ category: 'cat-dairy-eggs' })).toBe('cat-dairy-eggs')
    expect(resolveFoodIcon({ product: 'bogus' })).toBe('basket')
  })
})

describe('FoodIcon', () => {
  it('labels, falls back, and switches style with the preference', () => {
    setColourfulIcons(false)
    const { container, rerender } = render(<FoodIcon k="tangerine" label="Mandarins" />)
    expect(screen.getByRole('img', { name: 'Mandarins' })).toBeInTheDocument()
    expect(container.querySelector('use')?.getAttribute('href')).toMatch(
      /high-contrast.*#fi-tangerine/,
    )
    setColourfulIcons(true)
    rerender(<FoodIcon k="unknown-key" />)
    expect(container.querySelector('use')?.getAttribute('href')).toMatch(/flat.*#fi-basket/)
    expect(container.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true')
    setColourfulIcons(false)
  })
})
