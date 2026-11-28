import type { Recipe } from '../../../offline/replica'
import type { LineMatch, StockUse } from './rank'

/** "today", "tomorrow", "2 days", "expired". */
export function daysLabel(days: number | null): string {
  if (days === null) return ''
  if (days < 0) return 'expired'
  if (days === 0) return 'today'
  if (days === 1) return 'tomorrow'
  return `${days} days`
}

/** "Chicken thighs, tomorrow": a soon chip. */
export const soonLabel = (u: StockUse) => `${u.product.name}, ${daysLabel(u.days)}`

/** "No coconut milk? Use Greek yogurt thinned with water." */
export function swapLabel(m: LineMatch): string {
  const note = m.sub?.note?.trim()
  if (!note) return ''
  return `No ${m.line.name}? ${note}`
}

const langNames: Record<string, string> = {
  fa: 'Persian',
  ar: 'Arabic',
  fr: 'French',
  es: 'Spanish',
  it: 'Italian',
  de: 'German',
  tr: 'Turkish',
  hi: 'Hindi',
  zh: 'Chinese',
  ja: 'Japanese',
  ko: 'Korean',
}

/** Where a recipe came from, for cards: "Your library", "Idea from Stockpot AI", … */
export function sourceLabel(r: Recipe): string {
  if (r.sourceKind === 'idea') return 'Idea from Stockpot AI'
  const lang = r.sourceLang?.split('-')[0]
  if (lang && lang !== 'en') return `Translated from ${langNames[lang] ?? lang}`
  switch (r.sourceKind) {
    case 'instagram':
      return 'From an Instagram reel'
    case 'url':
      return 'From a recipe site'
    case 'photo':
      return 'From a photo'
    default:
      return 'Your library'
  }
}

/** "45 min · 4 servings". */
export function metaLabel(r: Recipe): string {
  return [
    r.totalMinutes ? `${r.totalMinutes} min` : '',
    r.servings ? `${r.servings} ${r.servings === 1 ? 'serving' : 'servings'}` : '',
  ]
    .filter(Boolean)
    .join(' · ')
}
