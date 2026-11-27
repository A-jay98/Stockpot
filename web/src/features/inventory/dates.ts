/** Dates travel as `YYYY-MM-DD`; arithmetic is in UTC so a day is always 24h, wherever the user is. */

const pad = (n: number) => String(n).padStart(2, '0')

/** The user's local calendar date. */
export function today(now = new Date()): string {
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`
}

const toUtc = (d: string) => Date.parse(`${d}T00:00:00Z`)

export function addDays(date: string, days: number): string {
  return new Date(toUtc(date) + days * 86_400_000).toISOString().slice(0, 10)
}

/** Whole days from `from` to `to` (negative when `to` is earlier). */
export const daysBetween = (from: string, to: string) =>
  Math.round((toUtc(to) - toUtc(from)) / 86_400_000)
