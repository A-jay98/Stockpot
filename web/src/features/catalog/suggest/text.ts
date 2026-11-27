/** Text helpers for the suggest engine: normalisation and the two fuzzy measures it uses. */

/** Lowercase, strip diacritics, punctuation to spaces, collapse whitespace. */
export function normalize(s: string): string {
  return s
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/['’]/g, '')
    .replace(/[^\p{L}\p{N}%]+/gu, ' ')
    .trim()
}

export const words = (normalized: string): string[] => (normalized ? normalized.split(' ') : [])

/**
 * Optimal string alignment distance (Damerau-Levenshtein with adjacent transpositions), capped:
 * returns `max + 1` as soon as the distance is known to exceed `max`.
 */
export function damerau(a: string, b: string, max: number): number {
  if (Math.abs(a.length - b.length) > max) return max + 1
  const n = a.length
  const m = b.length
  // Rows are plain number arrays; `at` makes the in-bounds reads explicit for the strict checker.
  const at = (row: number[], j: number): number => row[j] as number
  let prev2: number[] = []
  let prev = Array.from({ length: m + 1 }, (_, j) => j)
  for (let i = 1; i <= n; i++) {
    const cur: number[] = [i]
    let rowMin = i
    for (let j = 1; j <= m; j++) {
      const cost = a.charCodeAt(i - 1) === b.charCodeAt(j - 1) ? 0 : 1
      let v = Math.min(at(prev, j) + 1, at(cur, j - 1) + 1, at(prev, j - 1) + cost)
      if (
        i > 1 &&
        j > 1 &&
        a.charCodeAt(i - 1) === b.charCodeAt(j - 2) &&
        a.charCodeAt(i - 2) === b.charCodeAt(j - 1)
      ) {
        v = Math.min(v, at(prev2, j - 2) + 1)
      }
      cur[j] = v
      if (v < rowMin) rowMin = v
    }
    if (rowMin > max) return max + 1
    prev2 = prev
    prev = cur
  }
  return at(prev, m)
}

const trigramCache = new Map<string, Set<string>>()

function trigrams(w: string): Set<string> {
  let t = trigramCache.get(w)
  if (!t) {
    const p = `  ${w} `
    t = new Set()
    for (let i = 0; i + 3 <= p.length; i++) t.add(p.slice(i, i + 3))
    if (trigramCache.size > 20000) trigramCache.clear()
    trigramCache.set(w, t)
  }
  return t
}

/** Sørensen-Dice coefficient over padded trigrams, 0..1. */
export function dice(a: string, b: string): number {
  const x = trigrams(a)
  const y = trigrams(b)
  let shared = 0
  for (const g of x) if (y.has(g)) shared++
  return (2 * shared) / (x.size + y.size)
}

/** Typos tolerated for a query token of this length: none under 4, one up to 7, two from 8. */
export const maxTypos = (len: number): number => (len < 4 ? 0 : len < 8 ? 1 : 2)

export const SCORE_PREFIX = 1
export const SCORE_TYPO = 0.85
export const DICE_MIN = 0.4

/**
 * How well one query token matches one word: 1 for a word prefix, 0.85 within the typo budget
 * (against the word or its same-length prefix, so half-typed misspellings still match), else
 * the trigram Dice coefficient when it clears 0.4, else 0.
 */
export function tokenScore(token: string, word: string): number {
  if (word.startsWith(token)) return SCORE_PREFIX
  const budget = maxTypos(token.length)
  if (budget > 0 && word.length >= token.length - budget) {
    for (let len = token.length - 1; len <= token.length + 1; len++) {
      if (len < 1 || len > word.length) continue
      if (damerau(token, word.slice(0, len), budget) <= budget) return SCORE_TYPO
    }
  }
  if (token.length >= 4) {
    const d = dice(token, word)
    if (d >= DICE_MIN) return d
  }
  return 0
}
