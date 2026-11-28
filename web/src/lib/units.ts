/**
 * Unit conversion, mirroring `internal/units` (Go). Both run the shared fixture
 * `internal/units/testdata/convert.json`, so a change in one fails the other. Arithmetic is exact
 * (BigInt rationals); only `format` rounds.
 */

export type Conversions = Record<string, string>

type Dim = 'mass' | 'volume' | 'count'

const dims: Record<string, Dim> = {
  g: 'mass',
  kg: 'mass',
  lb: 'mass',
  oz: 'mass',
  ml: 'volume',
  l: 'volume',
  tsp: 'volume',
  tbsp: 'volume',
  cup: 'volume',
  fl_oz: 'volume',
  ea: 'count',
  pack: 'count',
  bag: 'count',
  can: 'count',
  jar: 'count',
  bottle: 'count',
  box: 'count',
  bunch: 'count',
  head: 'count',
  clove: 'count',
  slice: 'count',
  portion: 'count',
}

export const dimensionOf = (unit: string | null | undefined): Dim | undefined =>
  unit ? dims[unit] : undefined

/** An exact rational n/d with d > 0. */
export type Rat = { n: bigint; d: bigint }

const gcd = (a: bigint, b: bigint): bigint => {
  let x = a < 0n ? -a : a
  let y = b < 0n ? -b : b
  while (y) [x, y] = [y, x % y]
  return x || 1n
}
const norm = (n: bigint, d: bigint): Rat => {
  const s = d < 0n ? -1n : 1n
  const g = gcd(n, d)
  return { n: (s * n) / g, d: (s * d) / g }
}
export const mul = (a: Rat, b: Rat) => norm(a.n * b.n, a.d * b.d)
export const div = (a: Rat, b: Rat) => norm(a.n * b.d, a.d * b.n)
export const addR = (a: Rat, b: Rat) => norm(a.n * b.d + b.n * a.d, a.d * b.d)
export const subR = (a: Rat, b: Rat) => norm(a.n * b.d - b.n * a.d, a.d * b.d)
export const cmpR = (a: Rat, b: Rat) => {
  const x = a.n * b.d
  const y = b.n * a.d
  return x < y ? -1 : x > y ? 1 : 0
}
const ONE: Rat = { n: 1n, d: 1n }

/** Parses a non-negative decimal string ("1.5"); null when it isn't one. */
export function rat(s: string): Rat | null {
  const m = /^(\d+)(?:\.(\d+))?$/.exec(s.trim())
  if (!m) return null
  const frac = m[2] ?? ''
  return norm(BigInt(m[1] + frac), 10n ** BigInt(frac.length))
}
const R = (s: string) => rat(s) as Rat

// Grams for mass, millilitres for volume. Cooking measures are metric (Canada).
const base: Record<string, Rat> = {
  g: R('1'),
  kg: R('1000'),
  lb: R('453.59237'),
  oz: R('28.349523125'),
  ml: R('1'),
  l: R('1000'),
  tsp: R('5'),
  tbsp: R('15'),
  cup: R('250'),
  fl_oz: R('29.5735295625'),
}

function node(unit: string): [string, Rat] | null {
  const d = dims[unit]
  if (d === 'mass' || d === 'volume') return [d, base[unit] as Rat]
  if (d === 'count') return [unit, ONE]
  return null
}

/** "150 g" → [150, 'g']. */
export function parseAmount(s: string): [Rat, string] | null {
  const f = s.trim().split(/\s+/)
  if (f.length !== 2 || !dims[f[1] as string]) return null
  const q = rat(f[0] as string)
  if (!q || q.n <= 0n) return null
  return [q, f[1] as string]
}

type Edge = { to: string; f: Rat }

function graph(sets: (Conversions | null | undefined)[]): Map<string, Edge[]> {
  const seen = new Set<string>()
  const g = new Map<string, Edge[]>()
  const push = (k: string, e: Edge) => g.set(k, [...(g.get(k) ?? []), e])
  for (const set of sets) {
    if (!set) continue
    for (const k of Object.keys(set).sort()) {
      if (seen.has(k)) continue
      const from = node(k)
      const amt = parseAmount(set[k] as string)
      if (!from || !amt) continue
      const to = node(amt[1]) as [string, Rat]
      if (to[0] === from[0]) continue
      seen.add(k)
      const f = div(mul(amt[0], to[1]), from[1])
      push(from[0], { to: to[0], f })
      push(to[0], { to: from[0], f: div(ONE, f) })
    }
  }
  return g
}

function path(g: Map<string, Edge[]>, from: string, to: string): Rat | null {
  const visited = new Set([from])
  const queue: [string, Rat][] = [[from, ONE]]
  while (queue.length) {
    const [n, f] = queue.shift() as [string, Rat]
    for (const e of g.get(n) ?? []) {
      if (visited.has(e.to)) continue
      const next = mul(f, e.f)
      if (e.to === to) return next
      visited.add(e.to)
      queue.push([e.to, next])
    }
  }
  return null
}

/**
 * qty of `from` in `to`: exact within mass or volume, else through the conversion sets (product,
 * ingredient, ancestors; the first set wins per key). null when there is no path.
 */
export function convert(
  qty: Rat,
  from: string,
  to: string,
  ...sets: (Conversions | null | undefined)[]
): Rat | null {
  const f = node(from)
  const t = node(to)
  if (!f || !t) return null
  let v = mul(qty, f[1])
  if (f[0] !== t[0]) {
    const p = path(graph(sets), f[0], t[0])
    if (!p) return null
    v = mul(v, p)
  }
  return div(v, t[1])
}

/** Decimal string rounded half away from zero to `places`, trailing zeros trimmed. */
export function format(r: Rat, places = 3): string {
  const neg = r.n < 0n
  const a = neg ? -r.n : r.n
  const scale = 10n ** BigInt(places)
  const n = (a * scale * 2n + r.d) / (2n * r.d)
  let s = n.toString()
  if (places > 0) {
    s = s.padStart(places + 1, '0')
    s = `${s.slice(0, -places)}.${s.slice(-places)}`.replace(/\.?0+$/, '')
  }
  return neg && s !== '0' ? `-${s}` : s
}

/** Converts a decimal string; the result is rounded to 3 places. */
export function convertDecimal(
  qty: string,
  from: string,
  to: string,
  ...sets: (Conversions | null | undefined)[]
): string | null {
  const q = rat(qty)
  if (!q) return null
  const r = convert(q, from, to, ...sets)
  return r ? format(r) : null
}
