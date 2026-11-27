import type { LocationKind, ShelfLife } from '../../offline/replica'
import { addDays } from './dates'

/**
 * Expiry estimates from shelf-life rules. Mirrors `internal/inventory/rules.go`: both read the
 * shared cases in `internal/inventory/testdata/estimate.json`, so a change in one fails the other.
 */

export type Layer = { household?: ShelfLife | null; seed?: ShelfLife | null }

/** Everything known about a product's shelf life, most specific first. */
export type Chain = {
  product?: ShelfLife | null
  ingredients?: Layer[]
  categoryHousehold?: ShelfLife | null
  categoryDefault?: ShelfLife | null
}

export type ShelfKey = keyof ShelfLife
export type Op = 'add' | 'freeze' | 'thaw' | 'open'

/** Days for `key`: product → household ingredient rules → household category rule → seed ingredients → category default. */
export function chainDays(c: Chain, key: ShelfKey): number | undefined {
  // A household rule always beats a Stockpot seed value.
  const ings = c.ingredients ?? []
  const layers = [
    c.product,
    ...ings.map((i) => i.household),
    c.categoryHousehold,
    ...ings.map((i) => i.seed),
    c.categoryDefault,
  ]
  for (const l of layers) {
    const d = l?.[key]
    if (d !== undefined && d > 0) return d
  }
  return undefined
}

/** The estimated expiry date for `op`, or null when no rule knows. `loc` is where it is stored: it picks the `add` days, and `open` does nothing in a freezer. */
export function estimate(c: Chain, op: Op, loc: LocationKind, on: string): string | null {
  let days: number | undefined
  switch (op) {
    case 'add':
      days = chainDays(c, loc === 'fridge' ? 'fridge' : loc === 'freezer' ? 'freezer' : 'pantry')
      break
    case 'freeze':
      days = chainDays(c, 'freezer')
      break
    case 'thaw':
      days = chainDays(c, 'thawed_fridge') ?? chainDays(c, 'fridge')
      break
    case 'open':
      if (loc === 'freezer') return null
      days = chainDays(c, 'opened_fridge')
      break
  }
  return days === undefined ? null : addDays(on, days)
}
