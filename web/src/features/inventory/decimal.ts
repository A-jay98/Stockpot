/** Exact decimal arithmetic on the wire's decimal strings ("1.5"): quantities never touch floats. */

type Dec = { n: bigint; scale: number }

function parse(s: string): Dec {
  const [int = '0', frac = ''] = s.trim().split('.')
  return { n: BigInt(int + frac), scale: frac.length }
}

function align(a: Dec, b: Dec): [bigint, bigint, number] {
  const scale = Math.max(a.scale, b.scale)
  return [a.n * 10n ** BigInt(scale - a.scale), b.n * 10n ** BigInt(scale - b.scale), scale]
}

function format(n: bigint, scale: number): string {
  const neg = n < 0n
  let digits = (neg ? -n : n).toString().padStart(scale + 1, '0')
  if (scale > 0) digits = `${digits.slice(0, -scale)}.${digits.slice(-scale)}`
  // Trim trailing zeros so 1.50 reads 1.5 (the server's numeric keeps whatever it was given).
  if (scale > 0) digits = digits.replace(/\.?0+$/, '')
  return (neg && digits !== '0' ? '-' : '') + digits
}

export function add(a: string, b: string): string {
  const [x, y, s] = align(parse(a), parse(b))
  return format(x + y, s)
}

export function sub(a: string, b: string): string {
  const [x, y, s] = align(parse(a), parse(b))
  return format(x - y, s)
}

export function cmp(a: string, b: string): -1 | 0 | 1 {
  const [x, y] = align(parse(a), parse(b))
  return x < y ? -1 : x > y ? 1 : 0
}

export const isZero = (a: string) => cmp(a, '0') === 0
export const min = (a: string, b: string) => (cmp(a, b) <= 0 ? a : b)
/** max(a - b, 0) */
export const subClamped = (a: string, b: string) => (cmp(a, b) <= 0 ? '0' : sub(a, b))

/** Splits `total` into `n` parts, each rounded down to `places` decimals; the last takes the rest. */
export function splitEvenly(total: string, n: number, places = 3): string[] {
  const t = parse(total)
  const scale = Math.max(t.scale, places)
  const whole = t.n * 10n ** BigInt(scale - t.scale)
  const unit = 10n ** BigInt(scale - places)
  const share = (whole / BigInt(n) / unit) * unit
  const parts = Array.from({ length: n - 1 }, () => format(share, scale))
  parts.push(format(whole - share * BigInt(n - 1), scale))
  return parts
}
