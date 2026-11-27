import { type KeyboardEvent, useMemo, useState } from 'react'
import { Input } from '../../ui'
import { useIngredients, useProducts } from './hooks'

/** What the user picked, shared by shopping add and inventory add. */
export type PickedItem = {
  productId?: string
  ingredientId?: string
  name: string
  brand?: string
  storeIds: string[]
  qty?: string
  unit?: string
  categoryId?: string
}

type Props = {
  context: 'shopping' | 'inventory'
  onPick: (item: PickedItem) => void
  autoFocus?: boolean
}

type Suggestion = { key: string; label: string; hint?: string; item: PickedItem }

const norm = (s: string) => s.normalize('NFKD').replace(/[̀-ͯ]/g, '').toLowerCase().trim()

const MAX = 6

/**
 * Search box over the household's products and the ingredient catalog. This is the basic
 * substring version; m1-4 swaps the engine (fuzzy match, history ranking, ghost completion)
 * behind the same props. Enter picks the top suggestion, or adds the typed text as free text.
 */
export function ProductPicker({ onPick, autoFocus }: Props) {
  const [query, setQuery] = useState('')
  const products = useProducts()
  const ingredients = useIngredients()

  const suggestions = useMemo<Suggestion[]>(() => {
    const q = norm(query)
    if (!q) return []
    const out: Suggestion[] = []
    const usedIngredients = new Set<string>()
    for (const p of products) {
      if (p.ingredientId) usedIngredients.add(p.ingredientId)
      if (!norm(`${p.name} ${p.brand ?? ''}`).includes(q)) continue
      out.push({
        key: `p:${p.id}`,
        label: p.brand ? `${p.name} · ${p.brand}` : p.name,
        hint: p.package ?? undefined,
        item: {
          productId: p.id,
          ingredientId: p.ingredientId ?? undefined,
          name: p.name,
          brand: p.brand ?? undefined,
          storeIds: p.defaultStoreId ? [p.defaultStoreId] : [],
          qty: p.defaultQty ?? undefined,
          unit: p.defaultUnit ?? undefined,
          categoryId: p.categoryId ?? undefined,
        },
      })
    }
    for (const i of ingredients) {
      if (usedIngredients.has(i.id)) continue
      const names = [i.name, i.plural ?? '', ...i.aliases]
      if (!names.some((n) => norm(n).includes(q))) continue
      out.push({
        key: `i:${i.id}`,
        label: i.name,
        hint: 'new',
        item: {
          ingredientId: i.id,
          name: i.name,
          storeIds: [],
          categoryId: i.categoryId ?? undefined,
        },
      })
    }
    // Products first, then shorter (closer) names.
    return out
      .sort(
        (a, b) =>
          Number(b.key[0] === 'p') - Number(a.key[0] === 'p') || a.label.length - b.label.length,
      )
      .slice(0, MAX)
  }, [query, products, ingredients])

  const pick = (item: PickedItem) => {
    onPick(item)
    setQuery('')
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key !== 'Enter') return
    e.preventDefault()
    const text = query.trim()
    if (!text) return
    const first = suggestions[0]
    pick(first ? first.item : { name: text, storeIds: [] })
  }

  return (
    <div className="flex flex-col gap-2">
      <Input
        aria-label="Item name"
        placeholder="Add an item"
        autoComplete="off"
        // biome-ignore lint/a11y/noAutofocus: opt-in prop for flows that open straight into the picker
        autoFocus={autoFocus}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={onKeyDown}
      />
      <ul aria-label="Suggestions" className="m-0 list-none p-0" hidden={suggestions.length === 0}>
        {suggestions.map((s) => (
          <li key={s.key}>
            <button
              type="button"
              className="flex w-full items-center justify-between gap-3 rounded-md border-0 bg-transparent px-3 py-2.5 text-left text-ink hover:bg-surface-2"
              onClick={() => pick(s.item)}
            >
              <span className="truncate font-semibold">{s.label}</span>
              {s.hint && <span className="t-body-sm text-ink-muted">{s.hint}</span>}
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}
