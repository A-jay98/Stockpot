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
}

export const unitFromWord = (w: string): string | undefined => aliases[w.toLowerCase()]

/** Human label for a unit code next to a quantity: count units read as words. */
export function formatQty(qty: string | null | undefined, unit: string | null | undefined): string {
  if (!qty) return unit && unit !== 'ea' ? unit : ''
  const n = Number(qty)
  const q = Number.isFinite(n) ? String(Math.round(n * 100) / 100) : qty
  if (!unit || unit === 'ea') return q
  const plural = ['pack', 'bag', 'can', 'jar', 'bottle', 'box', 'bunch', 'head']
  return plural.includes(unit) && n !== 1 ? `${q} ${unit}s` : `${q} ${unit}`
}
