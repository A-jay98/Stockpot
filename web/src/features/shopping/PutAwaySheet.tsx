import { useState } from 'react'
import { v7 as uuidv7 } from 'uuid'
import { dispatch } from '../../offline/outbox'
import type { Location } from '../../offline/replica'
import { Button, Input, Sheet, SheetContent } from '../../ui'
import { defaultLocation, type Row } from './items'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  rows: Row[]
  locations: Location[]
  /** The store being shopped at (the active filter), so purchases teach the right store. */
  storeId?: string | null
}

/**
 * "Move N checked to inventory": a location and an optional expiry per item. Leaving the date
 * empty lets the server estimate it from the ingredient's shelf life.
 */
export function PutAwaySheet({ open, onOpenChange, rows, locations, storeId }: Props) {
  const [loc, setLoc] = useState<Record<string, string>>({})
  const [expiry, setExpiry] = useState<Record<string, string>>({})

  const confirm = async () => {
    for (const r of rows) {
      const locationId = loc[r.id] ?? defaultLocation(r, locations)?.id
      await dispatch('shopping.item.check', {
        id: r.id,
        ...(storeId ? { storeId } : {}),
        inventory: {
          stockItemId: uuidv7(),
          ...(locationId ? { locationId } : {}),
          ...(expiry[r.id] ? { expiresOn: expiry[r.id] } : {}),
        },
      })
    }
    // Only the items just moved leave the list; one the server rejects stays visible.
    for (const r of rows) await dispatch('shopping.item.delete', { id: r.id })
    onOpenChange(false)
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        title={`Move ${rows.length} to inventory`}
        description="Pick where each goes. Expiry is guessed from the food unless you set it."
        footer={
          <Button block onClick={confirm} disabled={rows.length === 0}>
            Add to inventory
          </Button>
        }
      >
        <ul className="m-0 flex list-none flex-col gap-3 p-0">
          {rows.map((r) => (
            <li key={r.id} className="flex flex-col gap-2 rounded-lg bg-surface p-3 shadow-card">
              <span className="font-semibold">{r.title}</span>
              <div className="flex gap-2">
                <select
                  aria-label={`Location for ${r.title}`}
                  className="min-w-0 flex-1 rounded-md border border-border bg-surface px-3 py-2 text-base"
                  value={loc[r.id] ?? defaultLocation(r, locations)?.id ?? ''}
                  onChange={(e) => setLoc({ ...loc, [r.id]: e.target.value })}
                >
                  {locations.map((l) => (
                    <option key={l.id} value={l.id}>
                      {l.name}
                    </option>
                  ))}
                </select>
                <Input
                  type="date"
                  aria-label={`Expiry for ${r.title}`}
                  className="min-w-0 flex-1"
                  value={expiry[r.id] ?? ''}
                  onChange={(e) => setExpiry({ ...expiry, [r.id]: e.target.value })}
                />
              </div>
            </li>
          ))}
        </ul>
      </SheetContent>
    </Sheet>
  )
}
