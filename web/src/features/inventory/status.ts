import type { StockItem } from '../../offline/replica'
import type { FoodState } from '../../ui'
import { daysBetween } from './dates'

/** "Soon" means expiring within this many days. */
export const SOON_DAYS = 3

export const levelLabel: Record<NonNullable<StockItem['level']>, string> = {
  full: 'Full',
  most: 'Most',
  half: 'Half',
  low: 'Low',
  almost_out: 'Almost out',
}

export const unitLabel = (u: string | null) =>
  u === null || u === 'ea' ? '' : u === 'fl_oz' ? 'fl oz' : u

export function amountLabel(
  it: Pick<StockItem, 'amountKind' | 'quantity' | 'unit' | 'portions' | 'level'>,
): string {
  switch (it.amountKind) {
    case 'exact':
      return [it.quantity, unitLabel(it.unit)].filter(Boolean).join(' ')
    case 'portions':
      return `${it.portions ?? 0} ${it.portions === 1 ? 'portion' : 'portions'}`
    case 'level':
      return it.level ? levelLabel[it.level] : ''
  }
}

export type Freshness = 'expired' | 'soon' | 'fresh' | 'none'

/** Where an item stands on its date, independent of being low. */
export function freshness(it: Pick<StockItem, 'expiresOn'>, now: string): Freshness {
  if (!it.expiresOn) return 'none'
  const d = daysBetween(now, it.expiresOn)
  return d < 0 ? 'expired' : d <= SOON_DAYS ? 'soon' : 'fresh'
}

export const isLow = (it: Pick<StockItem, 'low' | 'level'>) =>
  it.low || it.level === 'low' || it.level === 'almost_out'

/** The one state chip an item shows: expired, then soon, then low, else fresh. */
export function foodState(
  it: Pick<StockItem, 'expiresOn' | 'low' | 'level'>,
  now: string,
): FoodState {
  const f = freshness(it, now)
  if (f === 'expired' || f === 'soon') return f
  return isLow(it) ? 'low' : 'fresh'
}

const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`

/** "Expired 2 days ago", "Today", "Tomorrow", "In 5 days", or "No date". */
export function expiryLabel(expiresOn: string | null, now: string): string {
  if (!expiresOn) return 'No date'
  const d = daysBetween(now, expiresOn)
  if (d < -1) return `Expired ${plural(-d, 'day')} ago`
  if (d === -1) return 'Expired yesterday'
  if (d === 0) return 'Today'
  if (d === 1) return 'Tomorrow'
  return `In ${plural(d, 'day')}`
}

export const kindLabel: Record<NonNullable<StockItem['expiryKind']>, string> = {
  use_by: 'Use by',
  best_before: 'Best before',
  estimated: 'Estimated',
}
