import { CornerDownLeft, Plus } from 'lucide-react'
import { type KeyboardEvent, useMemo, useState } from 'react'
import { cn, Input } from '../../ui'
import { categoryIconKey, FoodIcon, resolveFoodIcon } from '../../ui/food-icons'
import { useCategories, useStores } from './hooks'
import { formatQty, type Suggestion, search } from './suggest'
import { useSuggestIndex } from './useSuggest'

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

/**
 * Search box over the household's products and the ingredient catalog, learning from history
 * (architecture-m1 §6): typo-tolerant, ranked by how often and how recently things were bought,
 * with an inline ghost completion. Enter or Tab accepts the top suggestion when it is a confident
 * match; otherwise Enter adds the typed text as a free-text item.
 */
export function ProductPicker({ onPick, autoFocus }: Props) {
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const index = useSuggestIndex()
  const stores = useStores()
  const categories = useCategories()
  const storeName = useMemo(() => new Map(stores.map((s) => [s.id, s.name])), [stores])
  const categoryIcon = useMemo(
    () => new Map(categories.map((c) => [c.id, c.icon ?? categoryIconKey(c.slug)])),
    [categories],
  )

  const result = useMemo(() => search(index, query), [index, query])
  const { suggestions, parsed } = result
  const top = suggestions[0]
  const ghost = top?.ghost

  const freeText = (): PickedItem => {
    const item: PickedItem = {
      name: parsed.text || query.trim(),
      storeIds: parsed.storeId ? [parsed.storeId] : [],
    }
    if (parsed.qty) item.qty = parsed.qty
    if (parsed.unit) item.unit = parsed.unit
    return item
  }

  const pick = (item: PickedItem) => {
    onPick(item)
    setQuery('')
    setActive(0)
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      if (suggestions.length === 0) return
      e.preventDefault()
      const n = suggestions.length
      setActive((a) => (e.key === 'ArrowDown' ? (a + 1) % n : (a - 1 + n) % n))
      return
    }
    const atEnd = e.currentTarget.selectionStart === query.length
    if ((e.key === 'Tab' || e.key === 'ArrowRight') && ghost && top && atEnd) {
      e.preventDefault()
      pick(top.item)
      return
    }
    if (e.key !== 'Enter') return
    e.preventDefault()
    if (!query.trim() || !parsed.tokens.length) return
    const chosen = suggestions[active]
    // The top row is only taken for granted when the match is confident; a lower row was chosen.
    if (chosen && (active > 0 || chosen.confident)) pick(chosen.item)
    else pick(freeText())
  }

  const label = (s: Suggestion) =>
    s.entry.brand ? `${s.entry.name} · ${s.entry.brand}` : s.entry.name
  const detail = (s: Suggestion) => {
    const sid = s.item.storeIds[0]
    return [sid ? storeName.get(sid) : undefined, formatQty(s.item.qty, s.item.unit)]
      .filter(Boolean)
      .join(' · ')
  }

  return (
    <div className="flex flex-col gap-1">
      <div className="relative rounded-md bg-surface">
        {ghost && (
          <div className="pointer-events-none absolute inset-0 flex items-center overflow-hidden border border-transparent px-3 text-base whitespace-pre">
            <span className="invisible">{query}</span>
            <button
              type="button"
              tabIndex={-1}
              aria-label="Accept completion"
              className="pointer-events-auto cursor-pointer border-0 bg-transparent p-0 text-ink-muted"
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => top && pick(top.item)}
            >
              {ghost}
            </button>
          </div>
        )}
        <Input
          aria-label="Item name"
          placeholder="Add an item"
          autoComplete="off"
          autoCapitalize="off"
          spellCheck={false}
          enterKeyHint="done"
          // biome-ignore lint/a11y/noAutofocus: opt-in prop for flows that open straight into the picker
          autoFocus={autoFocus}
          className="relative bg-transparent"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value)
            setActive(0)
          }}
          onKeyDown={onKeyDown}
        />
      </div>
      {query.trim() && parsed.tokens.length > 0 && (
        <ul
          aria-label="Suggestions"
          className="m-0 list-none rounded-lg bg-surface p-1 shadow-card"
        >
          {suggestions.map((s, i) => (
            <li key={s.entry.key}>
              <button
                type="button"
                className={cn(
                  'flex w-full items-center gap-3 rounded-md border-0 bg-transparent px-2.5 py-2 text-left text-ink hover:bg-surface-2',
                  i === active && 'bg-brand-soft hover:bg-brand-soft',
                )}
                onClick={() => pick(s.item)}
              >
                <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-surface-2">
                  <FoodIcon
                    k={resolveFoodIcon({
                      product: s.entry.icon,
                      category: s.entry.categoryId
                        ? categoryIcon.get(s.entry.categoryId)
                        : undefined,
                    })}
                    size={20}
                  />
                </span>
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate font-semibold">{label(s)}</span>
                  {detail(s) && (
                    <span className="t-body-sm truncate text-ink-muted">{detail(s)}</span>
                  )}
                </span>
                {s.entry.kind === 'ingredient' && (
                  <span className="t-body-sm text-ink-muted">new</span>
                )}
                {i === active && s.confident && (
                  <CornerDownLeft aria-hidden className="size-4 shrink-0 text-brand" />
                )}
              </button>
            </li>
          ))}
          <li>
            <button
              type="button"
              className="flex w-full items-center gap-3 rounded-md border-0 bg-transparent px-2.5 py-2 text-left text-ink-muted hover:bg-surface-2"
              onClick={() => pick(freeText())}
            >
              <Plus aria-hidden className="size-5 shrink-0" />
              <span className="truncate">Add “{parsed.text || query.trim()}” as typed</span>
            </button>
          </li>
        </ul>
      )}
    </div>
  )
}
