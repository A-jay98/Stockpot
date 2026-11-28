/** Unit words people type after a quantity ("2 lb", "500g"), mapped to the server's unit codes. */
const aliases: Record<string, string> = {
  g: 'g',
  gram: 'g',
  grams: 'g',
  kg: 'kg',
  kgs: 'kg',
  kilo: 'kg',
  kilos: 'kg',
  lb: 'lb',
  lbs: 'lb',
  pound: 'lb',
  pounds: 'lb',
  oz: 'oz',
  ounce: 'oz',
  ounces: 'oz',
  ml: 'ml',
  l: 'l',
  lt: 'l',
  litre: 'l',
  litres: 'l',
  liter: 'l',
  liters: 'l',
  tsp: 'tsp',
  tbsp: 'tbsp',
  cup: 'cup',
  cups: 'cup',
  ea: 'ea',
  each: 'ea',
  pack: 'pack',
  packs: 'pack',
  bag: 'bag',
  bags: 'bag',
  can: 'can',
  cans: 'can',
  jar: 'jar',
  jars: 'jar',
  bottle: 'bottle',
  bottles: 'bottle',
  box: 'box',
  boxes: 'box',
  bunch: 'bunch',
  bunches: 'bunch',
  head: 'head',
  heads: 'head',
  tub: 'tub',
  tubs: 'tub',
  carton: 'carton',
  cartons: 'carton',
}

export const unitFromWord = (w: string): string | undefined => aliases[w.toLowerCase()]

/** Units counted in whole packages or pieces (the server's count dimension). */
export const COUNT_UNITS = [
  'ea',
  'pack',
  'bag',
  'can',
  'jar',
  'bottle',
  'box',
  'bunch',
  'head',
  'tub',
  'carton',
  'clove',
  'slice',
  'portion',
] as const

export const isCountUnit = (unit: string | null | undefined): boolean =>
  !!unit && (COUNT_UNITS as readonly string[]).includes(unit)

const plural = (unit: string, n: number) =>
  n !== 1 && unit !== 'ea' ? (unit === 'box' || unit === 'bunch' ? `${unit}es` : `${unit}s`) : unit

const num = (qty: string) => {
  const n = Number(qty)
  return { n, q: Number.isFinite(n) ? String(Math.round(n * 100) / 100) : qty }
}

/** Human label for a unit code next to a quantity: count units read as words. */
export function formatQty(qty: string | null | undefined, unit: string | null | undefined): string {
  if (!qty) return unit && unit !== 'ea' ? unit : ''
  const { n, q } = num(qty)
  if (!unit || unit === 'ea') return q
  return isCountUnit(unit) ? `${q} ${plural(unit, n)}` : `${q} ${unit}`
}

/**
 * How the shopping list shows an amount: packages and pieces as a multiple ("bag", "2× bag",
 * "3×"), weights and volumes as the amount ("500 g"). No quantity on a measure shows nothing.
 */
export function listQty(
  qty: string | null | undefined,
  unit: string | null | undefined,
): { times: string | null; unit: string | null } {
  const u = unit || 'ea'
  if (isCountUnit(u)) {
    const { n, q } = qty ? num(qty) : { n: 1, q: '1' }
    return { times: n !== 1 ? `${q}×` : null, unit: u === 'ea' ? null : u }
  }
  return qty ? { times: null, unit: formatQty(qty, u) } : { times: null, unit: null }
}
