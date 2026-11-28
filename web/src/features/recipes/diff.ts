/** One row of a line diff: kept, added in `b`, or removed from `a`. */
export type DiffRow = { kind: 'same' | 'added' | 'removed'; text: string }

/**
 * Line diff of two string lists by longest common subsequence (recipes have at most a few
 * hundred lines, so the O(n·m) table is fine). Removals come before additions at each change.
 */
export function diffLines(a: string[], b: string[]): DiffRow[] {
  const n = a.length
  const m = b.length
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    const row = lcs[i] as number[]
    const next = lcs[i + 1] as number[]
    for (let j = m - 1; j >= 0; j--) {
      row[j] =
        a[i] === b[j]
          ? (next[j + 1] as number) + 1
          : Math.max(next[j] as number, row[j + 1] as number)
    }
  }
  const out: DiffRow[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ kind: 'same', text: a[i] as string })
      i++
      j++
    } else if ((lcs[i + 1]?.[j] ?? 0) >= (lcs[i]?.[j + 1] ?? 0)) {
      out.push({ kind: 'removed', text: a[i++] as string })
    } else {
      out.push({ kind: 'added', text: b[j++] as string })
    }
  }
  while (i < n) out.push({ kind: 'removed', text: a[i++] as string })
  while (j < m) out.push({ kind: 'added', text: b[j++] as string })
  return out
}

export const hasChanges = (rows: DiffRow[]) => rows.some((r) => r.kind !== 'same')
