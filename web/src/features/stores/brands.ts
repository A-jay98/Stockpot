import { normalize } from '../catalog/suggest/text'
import { brandMarks } from './marks.gen'

/** How a store is drawn: its brand colours, and a logo glyph or a short monogram. */
export type StoreLook = {
  bg: string
  fg: string
  /** simple-icons path (24×24), drawn in `fg` on `bg`. */
  path?: string
  /** One to three letters when there is no glyph. */
  mono: string
  /** The well-known brand it matched, if any. */
  brand?: string
}

type Brand = {
  brand: string
  /** Normalized names (and common short forms) the store can be called. */
  names: string[]
  bg: string
  fg?: string
  mark?: string
  mono?: string
}

// Brand colours are approximate; well-known grocers in North America, the UK and Europe. A store
// matches when its normalized name contains one of `names` as whole words ("Costco Burnaby").
const brands: Brand[] = [
  { brand: 'Costco', names: ['costco'], bg: '#E31837', mono: 'C' },
  { brand: 'Walmart', names: ['walmart', 'wal mart'], bg: '#0071CE', mono: 'W' },
  { brand: 'Target', names: ['target'], bg: '#CC0000', mark: 'target' },
  { brand: "Sam's Club", names: ['sams club', 'sam s club'], bg: '#0067A0', mark: 'samsclub' },
  { brand: 'Whole Foods', names: ['whole foods', 'wholefoods'], bg: '#00674B', mono: 'WF' },
  {
    brand: "Trader Joe's",
    names: ['trader joe', 'trader joes', 'trader joe s'],
    bg: '#C8102E',
    mono: 'TJ',
  },
  { brand: 'Kroger', names: ['kroger'], bg: '#134B97', mono: 'K' },
  { brand: 'Safeway', names: ['safeway'], bg: '#E21B23', mono: 'S' },
  { brand: 'Loblaws', names: ['loblaws', 'loblaw'], bg: '#E31837', mono: 'L' },
  {
    brand: 'No Frills',
    names: ['no frills', 'nofrills'],
    bg: '#FFE600',
    fg: '#111111',
    mono: 'NF',
  },
  {
    brand: 'Real Canadian Superstore',
    names: ['superstore', 'real canadian superstore', 'rcss'],
    bg: '#E31837',
    mono: 'RC',
  },
  { brand: 'Sobeys', names: ['sobeys'], bg: '#00704A', mono: 'S' },
  { brand: 'Save-On-Foods', names: ['save on foods', 'save on'], bg: '#E31837', mono: 'SO' },
  { brand: 'FreshCo', names: ['freshco'], bg: '#78BE20', fg: '#111111', mono: 'F' },
  { brand: 'Metro', names: ['metro'], bg: '#E2001A', mono: 'M' },
  { brand: 'Food Basics', names: ['food basics'], bg: '#00A94F', mono: 'FB' },
  { brand: 'T&T', names: ['t t', 't and t', 't t supermarket'], bg: '#E60012', mono: 'T&T' },
  { brand: 'H Mart', names: ['h mart', 'hmart'], bg: '#E31837', mono: 'H' },
  {
    brand: 'Shoppers Drug Mart',
    names: ['shoppers drug mart', 'shoppers'],
    bg: '#D71920',
    mono: 'SDM',
  },
  { brand: 'Publix', names: ['publix'], bg: '#3E9B3A', mono: 'P' },
  { brand: 'Wegmans', names: ['wegmans'], bg: '#231F20', mono: 'W' },
  { brand: 'Instacart', names: ['instacart'], bg: '#43B02A', mark: 'instacart' },
  { brand: 'IKEA', names: ['ikea'], bg: '#0058A3', fg: '#FFDB00', mark: 'ikea' },
  { brand: 'Aldi', names: ['aldi', 'aldi sud', 'aldi nord'], bg: '#00005F', mark: 'aldisud' },
  { brand: 'Lidl', names: ['lidl'], bg: '#0050AA', mark: 'lidl' },
  { brand: 'Tesco', names: ['tesco'], bg: '#00539F', mark: 'tesco' },
  { brand: 'Asda', names: ['asda'], bg: '#68A51C', mark: 'asda' },
  {
    brand: "Sainsbury's",
    names: ['sainsburys', 'sainsbury', 'sainsbury s'],
    bg: '#F06C00',
    mono: 'S',
  },
  { brand: 'Waitrose', names: ['waitrose'], bg: '#5C8B1F', mono: 'W' },
  { brand: 'Iceland', names: ['iceland'], bg: '#CC092F', mark: 'iceland' },
  { brand: 'Carrefour', names: ['carrefour'], bg: '#004E9F', mark: 'carrefour' },
  { brand: 'Auchan', names: ['auchan'], bg: '#D6180B', mark: 'auchan' },
  { brand: 'REWE', names: ['rewe'], bg: '#CC071E', mark: 'rewe' },
  { brand: 'EDEKA', names: ['edeka'], bg: '#1B66B3', fg: '#FFE500', mark: 'edeka' },
  { brand: 'Kaufland', names: ['kaufland'], bg: '#E10915', mark: 'kaufland' },
  { brand: 'Penny', names: ['penny'], bg: '#CD1414', mark: 'penny' },
  { brand: 'Albert Heijn', names: ['albert heijn'], bg: '#04ACE6', mark: 'albertheijn' },
]

const padded = (s: string) =>
  ` ${normalize(s)
    .replace(/[^a-z0-9]+/g, ' ')
    .trim()} `

/** The well-known brand a store name is, if any. */
export function brandOf(name: string): Brand | undefined {
  const n = padded(name)
  return brands.find((b) => b.names.some((x) => n.includes(padded(x))))
}

// Store colours for stores we don't know: steady per name, readable with white text.
const palette = ['#2344A6', '#0F766E', '#7C3AED', '#B45309', '#BE123C', '#15803D', '#1D4ED8']

const initials = (name: string) => {
  const ws = normalize(name)
    .replace(/[^a-z0-9 ]+/g, ' ')
    .split(' ')
    .filter(Boolean)
  const [a = '?', b] = ws
  return (b ? a.charAt(0) + b.charAt(0) : a.slice(0, 2)).toUpperCase()
}

const hash = (s: string) => [...s].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7)

/** How to draw a store: a known brand's look, else its own colour (or one from its name) and initials. */
export function storeLook(store: { name: string; color: string | null }): StoreLook {
  const b = brandOf(store.name)
  if (b) {
    const mark = b.mark ? brandMarks[b.mark] : undefined
    return {
      bg: b.bg,
      fg: b.fg ?? '#FFFFFF',
      path: mark?.path,
      mono: b.mono ?? initials(b.brand),
      brand: b.brand,
    }
  }
  return {
    bg: store.color ?? palette[hash(normalize(store.name)) % palette.length] ?? '#2344A6',
    fg: '#FFFFFF',
    mono: initials(store.name),
  }
}
