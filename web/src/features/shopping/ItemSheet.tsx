import { Trash2 } from 'lucide-react'
import { useState } from 'react'
import { dispatch } from '../../offline/outbox'
import type { Store } from '../../offline/replica'
import { Button, cn, Field, Input, Sheet, SheetContent } from '../../ui'
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

type Props = { row: Row | null; stores: Store[]; onClose: () => void }

/** Edit an item: quantity, unit, stores it can be bought at, note; or remove it. */
export function ItemSheet({ row, stores, onClose }: Props) {
  return (
    <Sheet open={row !== null} onOpenChange={(o) => !o && onClose()}>
      {row && <Body key={row.id} row={row} stores={stores} onClose={onClose} />}
    </Sheet>
  )
}

function Body({ row, stores, onClose }: { row: Row; stores: Store[]; onClose: () => void }) {
  const [qty, setQty] = useState(row.qty ?? '')
  const [unit, setUnit] = useState(row.unit ?? '')
  const [note, setNote] = useState(row.note ?? '')
  const [storeIds, setStoreIds] = useState(row.storeIds)
  const valid = qty === '' || /^\d+(\.\d+)?$/.test(qty)

  const save = async () => {
    await dispatch('shopping.item.update', {
      id: row.id,
      qty: qty || null,
      unit: unit || null,
      note: note.trim() || null,
      storeIds,
    })
    onClose()
  }
  const remove = async () => {
    await dispatch('shopping.item.delete', { id: row.id })
    onClose()
  }
  const toggle = (id: string) =>
    setStoreIds((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]))

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
      {stores.length > 0 && (
        <fieldset className="m-0 flex flex-col gap-1.5 border-0 p-0">
          <legend className="t-label p-0 text-ink-muted">Buy at (any of)</legend>
          <div className="flex flex-wrap gap-1.5">
            {stores.map((s) => (
              <button
                key={s.id}
                type="button"
                aria-pressed={storeIds.includes(s.id)}
                onClick={() => toggle(s.id)}
                className={cn(
                  'rounded-full border border-border bg-surface px-3 py-1.5 text-[13px] font-semibold',
                  storeIds.includes(s.id) && 'border-brand bg-brand-soft text-brand',
                )}
              >
                {s.name}
              </button>
            ))}
          </div>
        </fieldset>
      )}
      <Field label="Note">
        {(p) => (
          <Input {...p} value={note} maxLength={200} onChange={(e) => setNote(e.target.value)} />
        )}
      </Field>
    </SheetContent>
  )
}
