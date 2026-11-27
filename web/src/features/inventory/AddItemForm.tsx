import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { v7 as uuidv7 } from 'uuid'
import { db } from '../../offline/db'
import { dispatch } from '../../offline/outbox'
import type { LocationKind } from '../../offline/replica'
import { Button, Field, Input, Segmented, toast } from '../../ui'
import { useLocations, useProducts } from '../catalog/hooks'
import { type PickedItem, ProductPicker } from '../catalog/ProductPicker'
import { today } from './dates'
import { estimate } from './estimate'
import { buildChain } from './foodRules'
import { kindLabel, levelLabel } from './status'
import './hooks' // reducers

type Mode = 'exact' | 'portions' | 'level'

const units = [
  'ea',
  'g',
  'kg',
  'lb',
  'oz',
  'ml',
  'l',
  'cup',
  'pack',
  'bag',
  'can',
  'jar',
  'bottle',
  'box',
  'bunch',
  'head',
] as const
const levels = ['full', 'most', 'half', 'low', 'almost_out'] as const
const kindToLocation: Record<string, LocationKind> = {
  fridge: 'fridge',
  freezer: 'freezer',
  pantry: 'pantry',
  other: 'other',
}

/** Manual add: pick or name a product, say where it goes and how much, optionally a date. */
export function AddItemForm() {
  const locations = useLocations()
  const products = useProducts()
  const navigate = useNavigate()
  const [picked, setPicked] = useState<PickedItem | null>(null)
  const [locationId, setLocationId] = useState<string>('')
  const [mode, setMode] = useState<Mode>('exact')
  const [qty, setQty] = useState('1')
  const [unit, setUnit] = useState<string>('ea')
  const [portions, setPortions] = useState('2')
  const [level, setLevel] = useState<(typeof levels)[number]>('full')
  const [date, setDate] = useState('')
  const [kind, setKind] = useState<'best_before' | 'use_by'>('best_before')
  const [guess, setGuess] = useState<string | null>(null)

  // Default the place from the product's usual one, else the first location.
  useEffect(() => {
    if (!locations.length) return
    const product = picked?.productId ? products.find((p) => p.id === picked.productId) : undefined
    const preferred =
      product?.defaultLocationKind &&
      locations.find((l) => l.kind === kindToLocation[product.defaultLocationKind as string])
    setLocationId((cur) =>
      picked ? (preferred?.id ?? locations[0]?.id ?? '') : cur || (locations[0]?.id ?? ''),
    )
  }, [picked, locations, products])

  useEffect(() => {
    if (picked?.qty) setQty(picked.qty)
    if (picked?.unit) setUnit(picked.unit)
  }, [picked])

  // Preview the estimate that would be used when no date is typed.
  useEffect(() => {
    let live = true
    void (async () => {
      const loc = locations.find((l) => l.id === locationId)
      const product = picked?.productId ? await db.product.get(picked.productId) : undefined
      const ingredientId = picked?.ingredientId ?? product?.ingredientId ?? null
      if (!loc || (!product && !ingredientId)) return setGuess(null)
      const chain = await buildChain(
        db,
        product ?? { ingredientId, categoryId: picked?.categoryId ?? null, shelfLife: null },
      )
      if (live) setGuess(estimate(chain, 'add', loc.kind, today()))
    })()
    return () => {
      live = false
    }
  }, [picked, locationId, locations])

  const valid =
    !!picked &&
    !!locationId &&
    (mode !== 'exact' || Number(qty) > 0) &&
    (mode !== 'portions' || Number(portions) > 0)

  const submit = () => {
    if (!picked) return
    const productId = picked.productId
    const newProduct = productId
      ? undefined
      : {
          id: uuidv7(),
          name: picked.name,
          ...(picked.ingredientId ? { ingredientId: picked.ingredientId } : {}),
          ...(picked.categoryId ? { categoryId: picked.categoryId } : {}),
        }
    void dispatch('inventory.stock.add', {
      id: uuidv7(),
      eventId: uuidv7(),
      ...(productId ? { productId } : { newProduct }),
      locationId,
      amountKind: mode,
      ...(mode === 'exact'
        ? { quantity: qty, unit }
        : mode === 'portions'
          ? { portions: Number(portions) }
          : { level }),
      ...(date ? { expiresOn: date, expiryKind: kind } : {}),
      source: 'manual',
      ...(picked.storeIds[0] ? { storeId: picked.storeIds[0] } : {}),
      on: today(),
    } as never)
    toast({ title: `Added ${picked.name}`, tone: 'success' })
    setPicked(null)
    setDate('')
    void navigate('/inventory')
  }

  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        if (valid) submit()
      }}
    >
      {picked ? (
        <div className="flex items-center justify-between gap-3 rounded-lg bg-surface p-3 shadow-card">
          <span className="truncate font-semibold">
            {picked.brand ? `${picked.name} · ${picked.brand}` : picked.name}
          </span>
          <Button type="button" variant="link" onClick={() => setPicked(null)}>
            Change
          </Button>
        </div>
      ) : (
        <ProductPicker context="inventory" onPick={setPicked} autoFocus />
      )}
      {picked && (
        <>
          <Field label="Where">
            {() => (
              <Segmented
                aria-label="Where"
                value={locationId}
                onValueChange={setLocationId}
                options={locations.map((l) => ({ value: l.id, label: l.name }))}
              />
            )}
          </Field>
          <Field label="How much">
            {() => (
              <div className="flex flex-col gap-2">
                <Segmented<Mode>
                  aria-label="Amount type"
                  value={mode}
                  onValueChange={setMode}
                  options={[
                    { value: 'exact', label: 'Exact' },
                    { value: 'portions', label: 'Portions' },
                    { value: 'level', label: 'Rough' },
                  ]}
                />
                {mode === 'exact' && (
                  <div className="flex gap-2">
                    <Input
                      aria-label="Quantity"
                      inputMode="decimal"
                      value={qty}
                      onChange={(e) => setQty(e.target.value)}
                    />
                    <select
                      aria-label="Unit"
                      value={unit}
                      onChange={(e) => setUnit(e.target.value)}
                      className="rounded-md border border-border bg-surface px-3 text-base"
                    >
                      {units.map((u) => (
                        <option key={u} value={u}>
                          {u === 'ea' ? 'each' : u}
                        </option>
                      ))}
                    </select>
                  </div>
                )}
                {mode === 'portions' && (
                  <Input
                    aria-label="Portions"
                    inputMode="numeric"
                    value={portions}
                    onChange={(e) => setPortions(e.target.value)}
                  />
                )}
                {mode === 'level' && (
                  <Segmented
                    aria-label="Level"
                    value={level}
                    onValueChange={setLevel}
                    options={levels.map((l) => ({ value: l, label: levelLabel[l] }))}
                  />
                )}
              </div>
            )}
          </Field>
          <Field
            label="Expiry date (optional)"
            hint={
              date
                ? undefined
                : guess
                  ? `Without a date we estimate ${guess} (${kindLabel.estimated.toLowerCase()}). Guidance only: check before eating.`
                  : 'No typical shelf life known for this one.'
            }
          >
            {(props) => (
              <div className="flex flex-col gap-2">
                <Input
                  type="date"
                  value={date}
                  onChange={(e) => setDate(e.target.value)}
                  {...props}
                />
                {date && (
                  <Segmented
                    aria-label="Kind of date"
                    value={kind}
                    onValueChange={setKind}
                    options={[
                      { value: 'best_before', label: 'Best before' },
                      { value: 'use_by', label: 'Use by' },
                    ]}
                  />
                )}
              </div>
            )}
          </Field>
          <Button type="submit" size="lg" block disabled={!valid}>
            Add to inventory
          </Button>
        </>
      )}
    </form>
  )
}
