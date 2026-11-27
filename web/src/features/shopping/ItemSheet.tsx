import { Check, Link2, Plus, Trash2 } from 'lucide-react'
import { type FormEvent, useMemo, useState } from 'react'
import { v7 as uuidv7 } from 'uuid'
import { dispatch } from '../../offline/outbox'
import type { Ingredient, Store } from '../../offline/replica'
import { Button, cn, Field, Input, Sheet, SheetContent } from '../../ui'
import { FoodIcon } from '../../ui/food-icons'
import { useIngredients } from '../catalog/hooks'
import { foodIconFor, useIconLookup } from '../catalog/icons'
import { matchState } from '../catalog/match'
import { normalize, SCORE_TYPO, tokenScore, words } from '../catalog/suggest/text'
import type { Row } from './items'

const unitOptions = [
  '',
  'ea',
  'g',
  'kg',
  'lb',
  'oz',
  'ml',
  'l',
  'pack',
  'bag',
  'can',
  'jar',
  'bottle',
  'box',
  'bunch',
  'head',
]

const chip =
  'rounded-full border border-border bg-surface px-3 py-1.5 text-[13px] font-semibold text-ink'
const chipOn = 'border-brand bg-brand-soft text-brand'

type Props = { row: Row | null; stores: Store[]; onClose: () => void }

/**
 * Edit an item: quantity, unit, stores it can be bought at, what it matches, note; or remove it.
 * A unit or store chosen here becomes the product's default for the next time it is added.
 */
export function ItemSheet({ row, stores, onClose }: Props) {
  return (
    <Sheet open={row !== null} onOpenChange={(o) => !o && onClose()}>
      {row && <Body key={row.id} row={row} stores={stores} onClose={onClose} />}
    </Sheet>
  )
}

/** Ingredients whose name, plural or alias covers every typed word, best first. */
export function searchIngredients(all: Ingredient[], q: string, limit = 6): Ingredient[] {
  const tokens = words(normalize(q))
  if (!tokens.length) return []
  const scored: { i: Ingredient; score: number }[] = []
  for (const i of all) {
    const ws = [i.name, i.plural ?? '', ...i.aliases].flatMap((f) => words(normalize(f)))
    let sum = 0
    for (const t of tokens) {
      const best = Math.max(0, ...ws.map((w) => tokenScore(t, w)))
      if (best < SCORE_TYPO) {
        sum = -1
        break
      }
      sum += best
    }
    if (sum < 0) continue
    const exact = normalize(i.name) === tokens.join(' ') ? 1 : 0
    scored.push({ i, score: sum / tokens.length + exact - normalize(i.name).length / 1000 })
  }
  return scored
    .sort((a, b) => b.score - a.score)
    .slice(0, limit)
    .map((s) => s.i)
}

function Body({ row, stores, onClose }: { row: Row; stores: Store[]; onClose: () => void }) {
  const ingredients = useIngredients()
  const icons = useIconLookup()
  const product = row.product
  const [qty, setQty] = useState(row.qty ?? '')
  // No unit yet: preselect the product's hand-picked one, else its ingredient's usual one.
  const [initialUnit] = useState(
    () =>
      row.unit ??
      product?.defaultUnit ??
      (product?.ingredientId && icons.ingredients.get(product.ingredientId)?.defaultUnit) ??
      '',
  )
  const [unit, setUnit] = useState(initialUnit)
  const [note, setNote] = useState(row.note ?? '')
  const [storeIds, setStoreIds] = useState(row.storeIds)
  const [newStore, setNewStore] = useState<string | null>(null)
  const [match, setMatch] = useState<Ingredient | null | undefined>(undefined) // undefined: unchanged
  const [finding, setFinding] = useState(false)
  const [find, setFind] = useState('')
  const valid = qty === '' || /^\d+(\.\d+)?$/.test(qty)

  const state = product ? matchState(product, icons.ingredients) : null
  const current = state && 'ingredient' in state ? state.ingredient : undefined
  const shown = match === undefined ? current : match
  const found = useMemo(() => searchIngredients(ingredients, find), [ingredients, find])

  const save = async () => {
    await dispatch('shopping.item.update', {
      id: row.id,
      qty: qty || null,
      unit: unit || null,
      note: note.trim() || null,
      storeIds,
    })
    if (product) {
      // What the user set by hand is what the product defaults to next time.
      const defaults: {
        defaultUnit?: string | null
        defaultQty?: string | null
        defaultStoreId?: string | null
      } = {}
      if (unit !== initialUnit) {
        defaults.defaultUnit = unit || null
        defaults.defaultQty = qty || null
      }
      const sameStores =
        storeIds.length === row.storeIds.length && storeIds.every((s) => row.storeIds.includes(s))
      if (!sameStores) defaults.defaultStoreId = storeIds[0] ?? null
      if (Object.keys(defaults).length)
        await dispatch('catalog.product.update', { id: product.id, ...defaults })
      // A pick, or "Yes" on an unsure guess, is the user's match from now on.
      if (match !== undefined && (match?.id !== product.ingredientId || state?.kind !== 'matched'))
        await dispatch('catalog.product.set_ingredient', {
          id: product.id,
          ingredientId: match?.id ?? null,
          extraIngredientIds: match?.id === product.ingredientId ? product.extraIngredientIds : [],
        })
    }
    onClose()
  }
  const remove = async () => {
    await dispatch('shopping.item.delete', { id: row.id })
    onClose()
  }
  const toggle = (id: string) =>
    setStoreIds((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]))
  const addStore = (e: FormEvent) => {
    e.preventDefault()
    const name = newStore?.trim()
    if (!name) return
    const existing = stores.find((s) => s.name.toLowerCase() === name.toLowerCase())
    const id = existing?.id ?? uuidv7()
    if (!existing)
      void dispatch('catalog.store.create', {
        id,
        name,
        sort: (stores.at(-1)?.sort ?? 0) + 10,
      })
    setStoreIds((s) => (s.includes(id) ? s : [...s, id]))
    setNewStore(null)
  }

  return (
    <SheetContent
      title={row.brand ? `${row.title} · ${row.brand}` : row.title}
      footer={
        <>
          <Button variant="danger" onClick={remove} aria-label="Remove item">
            <Trash2 aria-hidden />
          </Button>
          <Button block onClick={save} disabled={!valid}>
            Save
          </Button>
        </>
      }
    >
      <div className="flex gap-2">
        <Field label="Quantity" error={valid ? undefined : 'Use a number like 2 or 1.5'}>
          {(p) => (
            <Input
              {...p}
              inputMode="decimal"
              value={qty}
              onChange={(e) => setQty(e.target.value)}
            />
          )}
        </Field>
        <Field label="Unit">
          {(p) => (
            <select
              {...p}
              className="rounded-md border border-border bg-surface px-3 py-[11px] text-base"
              value={unit}
              onChange={(e) => setUnit(e.target.value)}
            >
              {unitOptions.map((u) => (
                <option key={u} value={u}>
                  {u || '—'}
                </option>
              ))}
            </select>
          )}
        </Field>
      </div>
      <fieldset className="m-0 flex flex-col gap-1.5 border-0 p-0">
        <legend className="t-label p-0 text-ink-muted">Buy at (any of)</legend>
        <div className="flex flex-wrap gap-1.5">
          {stores.map((s) => (
            <button
              key={s.id}
              type="button"
              aria-pressed={storeIds.includes(s.id)}
              onClick={() => toggle(s.id)}
              className={cn(chip, storeIds.includes(s.id) && chipOn)}
            >
              {s.name}
            </button>
          ))}
          {newStore === null && (
            <button
              type="button"
              onClick={() => setNewStore('')}
              className={cn(chip, 'flex items-center gap-1 text-ink-muted')}
            >
              <Plus aria-hidden className="size-4" />
              New store
            </button>
          )}
        </div>
        {newStore !== null && (
          <form onSubmit={addStore} className="flex gap-2">
            <Input
              aria-label="Store name"
              placeholder="Store name"
              autoFocus
              maxLength={100}
              value={newStore}
              onChange={(e) => setNewStore(e.target.value)}
            />
            <Button type="submit" disabled={!newStore.trim()}>
              Add
            </Button>
          </form>
        )}
      </fieldset>
      {product && (
        <section aria-label="Match" className="flex flex-col gap-1.5">
          <h3 className="t-label m-0 text-ink-muted">Matches recipes as</h3>
          <div className="flex items-center gap-3 rounded-md bg-surface-2 px-3 py-2">
            {shown ? (
              <>
                <FoodIcon k={foodIconFor({ ingredientId: shown.id }, icons)} size={20} />
                <span className="min-w-0 flex-1 truncate font-semibold">{shown.name}</span>
              </>
            ) : (
              <>
                <Link2 aria-hidden className="size-5 text-ink-muted" />
                <span className="flex-1 text-ink-muted">
                  {state?.kind === 'checking' ? 'Matching…' : 'Nothing yet'}
                </span>
              </>
            )}
            {match === undefined && state?.kind === 'confirm' && shown && (
              <Button size="sm" variant="secondary" onClick={() => setMatch(shown)}>
                Yes
              </Button>
            )}
            {match === undefined && state?.kind === 'checking' && shown && (
              <span className="t-body-sm text-ink-muted">Checking</span>
            )}
            <Button size="sm" variant="ghost" onClick={() => setFinding((f) => !f)}>
              Change
            </Button>
          </div>
          {state?.kind === 'confirm' && match === undefined && (
            <span className="t-body-sm text-ink-muted">Not sure about this one. Is it right?</span>
          )}
          {finding && (
            <div className="flex flex-col gap-1">
              <Input
                aria-label="Find ingredient"
                placeholder="Find an ingredient"
                autoFocus
                value={find}
                onChange={(e) => setFind(e.target.value)}
              />
              <ul className="m-0 flex list-none flex-col p-0">
                {found.map((i) => (
                  <li key={i.id}>
                    <button
                      type="button"
                      onClick={() => {
                        setMatch(i)
                        setFinding(false)
                        setFind('')
                      }}
                      className="flex w-full items-center gap-3 rounded-md border-0 bg-transparent px-2.5 py-2 text-left text-ink hover:bg-surface-2"
                    >
                      <FoodIcon k={foodIconFor({ ingredientId: i.id }, icons)} size={20} />
                      <span className="flex-1 truncate">{i.name}</span>
                      {i.id === shown?.id && <Check aria-hidden className="size-4 text-brand" />}
                    </button>
                  </li>
                ))}
                {shown && (
                  <li>
                    <button
                      type="button"
                      onClick={() => {
                        setMatch(null)
                        setFinding(false)
                      }}
                      className="w-full rounded-md border-0 bg-transparent px-2.5 py-2 text-left text-ink-muted hover:bg-surface-2"
                    >
                      Don't match it to anything
                    </button>
                  </li>
                )}
              </ul>
            </div>
          )}
        </section>
      )}
      <Field label="Note">
        {(p) => (
          <Input {...p} value={note} maxLength={200} onChange={(e) => setNote(e.target.value)} />
        )}
      </Field>
    </SheetContent>
  )
}
