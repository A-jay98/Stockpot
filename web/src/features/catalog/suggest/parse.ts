import { damerau, normalize, words } from './text'
import { unitFromWord } from './units'

export type ParsedQuery = {
  /** Query words left for matching: quantity and store removed, normalised. */
  tokens: string[]
  /** The same words as typed (original case), for free-text items. */
  text: string
  qty?: string
  unit?: string
  storeId?: string
  /** The raw part before the quantity prefix, used to align the ghost completion. */
  rest: string
}

type StoreRef = { id: string; name: string }

const NUM = String.raw`\d+(?:[.,]\d+)?`
const dec = (s: string) => s.replace(',', '.')

/** Pull a quantity (and unit) off either end: `2kg`, `3 `, `x3`, `milk 2l`. */
function takeQty(raw: string): { rest: string; qty?: string; unit?: string } {
  let m = raw.match(new RegExp(`^(${NUM})\\s*([a-zA-Z]+)(?=\\s|$)`))
  if (m?.[1] && m[2] && unitFromWord(m[2])) {
    return { rest: raw.slice(m[0].length).trim(), qty: dec(m[1]), unit: unitFromWord(m[2]) }
  }
  m = raw.match(/^(?:x\s*(\d+)|(\d+)\s*x)\s+/i)
  if (m && (m[1] ?? m[2])) return { rest: raw.slice(m[0].length).trim(), qty: m[1] ?? m[2] }
  m = raw.match(new RegExp(`^(${NUM})\\s+(?=\\p{L})`, 'u'))
  if (m?.[1]) return { rest: raw.slice(m[0].length).trim(), qty: dec(m[1]) }
  m = raw.match(new RegExp(`\\s(?:x\\s*(\\d+)|(${NUM})\\s*([a-zA-Z]+)?)$`, 'i'))
  const num = m ? (m[1] ?? m[2]) : undefined
  if (m && num && (m[1] || !m[3] || unitFromWord(m[3]))) {
    return {
      rest: raw.slice(0, m.index).trim(),
      qty: dec(num),
      unit: m[3] ? unitFromWord(m[3]) : undefined,
    }
  }
  return { rest: raw }
}

function storeWordMatches(token: string, word: string): boolean {
  if (token.length < 3) return false
  if (word.startsWith(token) || token === word) return true
  return token.length >= 5 && damerau(token, word, 1) <= 1
}

/**
 * Split a typed query into quantity, store and the words to match.
 * A store is `@name`, `from name`, or any word(s) that match a store's name (never the only word).
 */
export function parseQuery(raw: string, stores: StoreRef[]): ParsedQuery {
  const { rest, qty, unit } = takeQty(raw.trim())
  const original = rest.split(/\s+/).filter(Boolean)
  let toks = original.map((w) => normalize(w)).filter(Boolean)
  const textWords = original.filter((w) => normalize(w))
  let storeId: string | undefined
  const prepared = stores.map((s) => ({ id: s.id, words: words(normalize(s.name)) }))

  const drop = (from: number, count: number) => {
    toks = [...toks.slice(0, from), ...toks.slice(from + count)]
    textWords.splice(from, count)
  }

  // @store
  const at = original.findIndex((w) => w.startsWith('@'))
  if (at >= 0) {
    const t = normalize(original[at] ?? '')
    const hit = prepared.find((s) => s.words.some((w) => storeWordMatches(t, w)))
    if (hit) {
      storeId = hit.id
      drop(at, 1)
    }
  }
  // Words naming a store at position i: the whole (multi-word) name, else one word of it.
  const storeAt = (i: number): { id: string; len: number } | undefined => {
    for (const s of prepared) {
      if (s.words.length > 1 && s.words.every((w, k) => toks[i + k] === w))
        return { id: s.id, len: s.words.length }
    }
    const hit = prepared.find((s) => s.words.some((w) => storeWordMatches(toks[i] ?? '', w)))
    return hit && { id: hit.id, len: 1 }
  }
  // "from <store>": needs a store after it
  if (!storeId) {
    const i = toks.indexOf('from')
    const hit = i >= 0 ? storeAt(i + 1) : undefined
    if (hit) {
      storeId = hit.id
      drop(i, 1 + hit.len)
    }
  }
  // A bare store name in the query, if something else remains to search for.
  if (!storeId) {
    for (let i = 0; i < toks.length && !storeId; i++) {
      const hit = storeAt(i)
      if (hit && toks.length > hit.len) {
        storeId = hit.id
        drop(i, hit.len)
      }
    }
  }
  return { tokens: toks, text: textWords.join(' '), qty, unit, storeId, rest }
}
