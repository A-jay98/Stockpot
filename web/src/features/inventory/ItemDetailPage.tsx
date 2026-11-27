import {
  ArrowDown,
  ArrowLeft,
  Check,
  Minus,
  PackageOpen,
  Plus,
  Scissors,
  Snowflake,
  Sun,
  Trash2,
  Truck,
} from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { v7 as uuidv7 } from 'uuid'
import { Page } from '../../app/Page'
import { dispatch } from '../../offline/outbox'
import type { Ingredient, StockEvent } from '../../offline/replica'
import {
  Button,
  Input,
  Segmented,
  Sheet,
  SheetClose,
  SheetContent,
  StateBadge,
  Switch,
} from '../../ui'
import { FoodIcon } from '../../ui/food-icons'
import { useIngredients, useLocations } from '../catalog/hooks'
import { today as todayOf } from './dates'
import { add, splitEvenly } from './decimal'
import { type StockRow, useStockEvents, useStockRow } from './hooks'
import { amountLabel, expiryLabel, foodState, kindLabel, levelLabel, unitLabel } from './status'

const levels = ['full', 'most', 'half', 'low', 'almost_out'] as const

const eventText = (e: StockEvent, loc: (id: string | null) => string): string => {
  const d = e.delta
    ? `${e.delta.startsWith('-') ? '' : '+'}${e.delta}${e.unit && e.unit !== 'ea' ? ` ${unitLabel(e.unit)}` : ''}`
    : ''
  switch (e.kind) {
    case 'add':
      return `Added ${d.replace('+', '')} to ${loc(e.toLocationId)}`.replace('  ', ' ')
    case 'consume':
      return `Used ${d.replace('-', '')}`
    case 'discard':
      return `Thrown out${e.reason ? ` (${e.reason})` : ''}`
    case 'move':
      return `Moved to ${loc(e.toLocationId)}`
    case 'adjust':
      return e.reason === 'edit' ? 'Edited' : e.reason ? `Set to ${e.reason}` : `Changed ${d}`
    case 'open':
      return 'Opened'
    case 'thaw':
      return `Thawed in ${loc(e.toLocationId)}`
    case 'freeze':
      return `Frozen in ${loc(e.toLocationId)}`
    case 'split':
      return e.delta?.startsWith('-')
        ? `Split off ${d.replace('-', '')}`
        : `Split from a larger pack (${d.replace('+', '')})`
    case 'low':
      return e.reason === 'marked' ? 'Marked low' : 'No longer low'
  }
}

function Row({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-1.5">
      <dt className="text-ink-muted">{k}</dt>
      <dd className="m-0 text-right font-semibold">{children}</dd>
    </div>
  )
}

function ActionButton({
  icon,
  label,
  onClick,
  danger,
}: {
  icon: ReactNode
  label: string
  onClick: () => void
  danger?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`flex flex-col items-center gap-1.5 rounded-lg border-0 bg-surface px-1 py-3 text-[12.5px] font-semibold shadow-card [&_svg]:size-5 ${danger ? 'text-expired' : 'text-ink'}`}
    >
      {icon}
      {label}
    </button>
  )
}

export function ItemDetailPage() {
  const { id } = useParams()
  const row = useStockRow(id)
  if (row === undefined) return <Page title="Item" />
  if (row === null)
    return (
      <Page title="Item">
        <p className="t-body-sm m-0 rounded-lg bg-surface p-4 text-ink-muted shadow-card">
          This item is gone: used up, thrown out or removed on another device.{' '}
          <Link to="/inventory" className="font-semibold text-brand">
            Back to inventory
          </Link>
        </p>
      </Page>
    )
  return <Detail row={row} />
}

function Detail({ row }: { row: StockRow }) {
  const { item, product } = row
  const now = todayOf()
  const locations = useLocations()
  const events = useStockEvents(item.id)
  const navigate = useNavigate()
  const [sheet, setSheet] = useState<'move' | 'split' | 'discard' | 'ingredient' | 'expiry' | null>(
    null,
  )
  const locName = (lid: string | null) => locations.find((l) => l.id === lid)?.name ?? 'a location'
  const state = foodState(item, now)
  const kind = row.location?.kind
  const ev = () => uuidv7()
  const send = <T extends Parameters<typeof dispatch>[0]>(
    type: T,
    payload: Omit<Parameters<typeof dispatch<T>>[1], 'id' | 'eventId'> & { eventId?: string },
  ) => void dispatch(type, { id: item.id, eventId: ev(), ...payload } as never)

  const step = item.unit === 'g' || item.unit === 'ml' ? '100' : '1'
  const adjustBy = (dir: 1 | -1) => {
    if (item.amountKind === 'exact' && item.quantity) {
      if (dir < 0)
        send('inventory.stock.consume', { quantity: step, unit: item.unit ?? undefined } as never)
      else send('inventory.stock.adjust', { quantity: add(item.quantity, step) } as never)
    } else if (item.amountKind === 'portions') {
      if (dir < 0) send('inventory.stock.consume', { portions: 1 } as never)
      else send('inventory.stock.adjust', { portions: (item.portions ?? 0) + 1 } as never)
    }
  }

  return (
    <Page
      title={product.name}
      actions={
        <Button asChild variant="secondary" size="icon" aria-label="Back to inventory">
          <Link to="/inventory">
            <ArrowLeft aria-hidden />
          </Link>
        </Button>
      }
    >
      <div className="flex items-center gap-3.5">
        <span className="grid size-16 shrink-0 place-items-center rounded-lg bg-surface-2 text-ink">
          <FoodIcon k={row.icon} size={34} />
        </span>
        <div className="flex min-w-0 flex-col gap-1.5">
          <h2 className="t-title m-0 truncate">{row.name}</h2>
          <div className="flex flex-wrap gap-1.5">
            <StateBadge state={state}>
              {item.expiresOn ? expiryLabel(item.expiresOn, now) : undefined}
            </StateBadge>
            {item.low && state !== 'low' && <StateBadge state="low" />}
          </div>
        </div>
      </div>

      <section
        aria-label="Amount"
        className="flex flex-col gap-3 rounded-lg bg-surface p-4 shadow-card"
      >
        <div className="flex items-center justify-between gap-3">
          <span className="font-semibold">Amount</span>
          {item.amountKind === 'level' ? (
            <span className="font-semibold">{item.level ? levelLabel[item.level] : ''}</span>
          ) : (
            <div className="flex items-center gap-1 rounded-full bg-surface-2 p-[3px]">
              <Button
                variant="ghost"
                size="icon"
                aria-label="Use one less"
                className="size-8 bg-surface shadow-card"
                onClick={() => adjustBy(-1)}
              >
                <Minus aria-hidden />
              </Button>
              <output
                aria-label="Current amount"
                className="tabular min-w-16 px-1 text-center font-bold"
              >
                {amountLabel(item)}
              </output>
              <Button
                variant="ghost"
                size="icon"
                aria-label="Add one more"
                className="size-8 bg-surface shadow-card"
                onClick={() => adjustBy(1)}
              >
                <Plus aria-hidden />
              </Button>
            </div>
          )}
        </div>
        {item.amountKind === 'level' && (
          <Segmented
            aria-label="Level"
            value={item.level ?? 'full'}
            onValueChange={(l) => send('inventory.stock.set_level', { level: l } as never)}
            options={levels.map((l) => ({ value: l, label: levelLabel[l] }))}
          />
        )}
        <div className="flex items-center justify-between gap-3">
          <span className="flex items-center gap-2 font-semibold [&_svg]:size-4">
            <ArrowDown aria-hidden />
            Running low
          </span>
          <Switch
            aria-label="Running low"
            checked={item.low}
            onCheckedChange={(on) =>
              send(on ? 'inventory.stock.mark_low' : 'inventory.stock.unmark_low', {} as never)
            }
          />
        </div>
      </section>

      <div className="grid grid-cols-4 gap-2">
        {!item.openedOn && (
          <ActionButton
            icon={<PackageOpen aria-hidden />}
            label="Open"
            onClick={() => send('inventory.stock.open', { on: now } as never)}
          />
        )}
        {kind === 'freezer' ? (
          <ActionButton icon={<Sun aria-hidden />} label="Thaw" onClick={() => setSheet('move')} />
        ) : (
          <ActionButton
            icon={<Snowflake aria-hidden />}
            label="Freeze"
            onClick={() => {
              const f = locations.find((l) => l.kind === 'freezer')
              if (f) send('inventory.stock.freeze', { locationId: f.id, on: now } as never)
            }}
          />
        )}
        <ActionButton icon={<Truck aria-hidden />} label="Move" onClick={() => setSheet('move')} />
        {item.amountKind !== 'level' && (
          <ActionButton
            icon={<Scissors aria-hidden />}
            label="Split"
            onClick={() => setSheet('split')}
          />
        )}
        <ActionButton
          icon={<Trash2 aria-hidden />}
          label="Discard"
          danger
          onClick={() => setSheet('discard')}
        />
      </div>

      <dl className="m-0 rounded-lg bg-surface px-4 py-2 text-sm shadow-card">
        <Row k="Where">{row.location?.name ?? 'No place'}</Row>
        <Row k="Expires">
          <button
            type="button"
            className="border-0 bg-transparent p-0 font-semibold text-brand"
            onClick={() => setSheet('expiry')}
          >
            {item.expiresOn
              ? `${item.expiresOn} · ${item.expiryKind ? kindLabel[item.expiryKind] : ''}`
              : 'Set a date'}
          </button>
        </Row>
        {item.openedOn && <Row k="Opened">{item.openedOn}</Row>}
        {item.frozenOn && kind === 'freezer' && <Row k="Frozen">{item.frozenOn}</Row>}
        {item.thawedOn && kind !== 'freezer' && <Row k="Thawed">{item.thawedOn}</Row>}
        <Row k="Matches recipes as">
          <button
            type="button"
            className="border-0 bg-transparent p-0 font-semibold text-brand"
            onClick={() => setSheet('ingredient')}
          >
            {row.ingredient?.name ?? 'Choose'}
          </button>
        </Row>
      </dl>
      {item.expiryKind === 'estimated' && (
        <p className="t-body-sm m-0 text-ink-muted">
          This date is an estimate from typical shelf life. It is guidance, not a guarantee: check
          how it looks and smells before eating.
        </p>
      )}

      <section aria-label="Timeline" className="flex flex-col gap-2">
        <h2 className="t-label m-0 text-ink-muted">Timeline</h2>
        <ol className="m-0 flex list-none flex-col gap-3 p-0 text-sm">
          {events.map((e) => (
            <li key={e.id} className="flex flex-col">
              <span>{eventText(e, locName)}</span>
              <span className="text-[12.5px] text-ink-muted">
                {new Date(e.createdAt).toLocaleString()}
              </span>
            </li>
          ))}
        </ol>
      </section>

      <MoveSheet open={sheet === 'move'} onClose={() => setSheet(null)} row={row} />
      <SplitSheet open={sheet === 'split'} onClose={() => setSheet(null)} row={row} />
      <DiscardSheet
        open={sheet === 'discard'}
        onClose={() => setSheet(null)}
        row={row}
        onDone={() => navigate('/inventory')}
      />
      <ExpirySheet open={sheet === 'expiry'} onClose={() => setSheet(null)} row={row} />
      <IngredientSheet open={sheet === 'ingredient'} onClose={() => setSheet(null)} row={row} />
    </Page>
  )
}

type SheetProps = { open: boolean; onClose: () => void; row: StockRow }

function MoveSheet({ open, onClose, row }: SheetProps) {
  const locations = useLocations()
  const { item } = row
  const go = (locationId: string, kind: string) => {
    const base = { id: item.id, eventId: uuidv7(), locationId, on: todayOf() }
    // Freezing and thawing restart the clock; the plain move is the same thing under the hood.
    const from = row.location?.kind
    if (kind === 'freezer' && from !== 'freezer') void dispatch('inventory.stock.freeze', base)
    else if (kind !== 'freezer' && from === 'freezer') void dispatch('inventory.stock.thaw', base)
    else void dispatch('inventory.stock.move', base)
    onClose()
  }
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent title="Move to">
        <div className="flex flex-col gap-2">
          {locations.map((l) => (
            <Button
              key={l.id}
              variant="secondary"
              block
              disabled={l.id === item.locationId}
              onClick={() => go(l.id, l.kind)}
            >
              {l.name}
            </Button>
          ))}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function SplitSheet({ open, onClose, row }: SheetProps) {
  const locations = useLocations()
  const { item } = row
  const [n, setN] = useState(2)
  const [loc, setLoc] = useState<string | undefined>(undefined)
  const total = item.amountKind === 'exact' ? item.quantity : String(item.portions ?? 0)
  const max = item.amountKind === 'portions' ? (item.portions ?? 1) : 12
  const submit = () => {
    if (!total) return
    const shares =
      item.amountKind === 'exact' ? splitEvenly(total, n) : Array.from({ length: n }, () => '1')
    const parts = shares.map((q) => ({
      id: uuidv7(),
      eventId: uuidv7(),
      ...(item.amountKind === 'exact' ? { quantity: q } : { portions: Number(q) }),
    }))
    void dispatch('inventory.stock.split', {
      id: item.id,
      eventId: uuidv7(),
      parts,
      locationId: loc ?? item.locationId ?? undefined,
      on: todayOf(),
    })
    onClose()
  }
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent
        title="Split into portions"
        description="Cut it into equal parts, e.g. a pack into freezer portions."
        footer={
          <Button block onClick={submit}>
            Split
          </Button>
        }
      >
        <div className="flex items-center justify-between">
          <span className="font-semibold">Parts</span>
          <div className="flex items-center gap-2">
            <Button
              variant="secondary"
              size="icon"
              aria-label="Fewer parts"
              onClick={() => setN(Math.max(2, n - 1))}
            >
              <Minus aria-hidden />
            </Button>
            <output aria-label="Number of parts" className="tabular w-6 text-center font-bold">
              {n}
            </output>
            <Button
              variant="secondary"
              size="icon"
              aria-label="More parts"
              onClick={() => setN(Math.min(max, n + 1))}
            >
              <Plus aria-hidden />
            </Button>
          </div>
        </div>
        <Segmented
          aria-label="Put the parts in"
          value={loc ?? item.locationId ?? ''}
          onValueChange={setLoc}
          options={locations.map((l) => ({ value: l.id, label: l.name }))}
        />
      </SheetContent>
    </Sheet>
  )
}

const reasons = [
  ['expired', 'Expired'],
  ['spoiled', 'Spoiled'],
  ['unwanted', 'Didn’t want it'],
  ['mistake', 'Added by mistake'],
  ['other', 'Other'],
] as const

function DiscardSheet({ open, onClose, row, onDone }: SheetProps & { onDone: () => void }) {
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent title="Throw out" description="Why? It helps you waste less.">
        <div className="flex flex-col gap-2">
          {reasons.map(([value, label]) => (
            <SheetClose key={value} asChild>
              <Button
                variant="secondary"
                block
                onClick={() => {
                  void dispatch('inventory.stock.discard', {
                    id: row.item.id,
                    eventId: uuidv7(),
                    reason: value,
                  })
                  onDone()
                }}
              >
                {label}
              </Button>
            </SheetClose>
          ))}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function ExpirySheet({ open, onClose, row }: SheetProps) {
  const { item } = row
  const [date, setDate] = useState(item.expiresOn ?? '')
  const [kind, setKind] = useState<'use_by' | 'best_before'>(
    item.expiryKind === 'use_by' ? 'use_by' : 'best_before',
  )
  const save = () => {
    void dispatch('inventory.stock.update', {
      id: item.id,
      eventId: uuidv7(),
      expiresOn: date || null,
      ...(date ? { expiryKind: kind } : {}),
    })
    onClose()
  }
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent
        title="Expiry date"
        footer={
          <Button block onClick={save}>
            <Check aria-hidden />
            Save
          </Button>
        }
      >
        <Input
          type="date"
          aria-label="Expiry date"
          value={date}
          onChange={(e) => setDate(e.target.value)}
        />
        <Segmented
          aria-label="Kind of date"
          value={kind}
          onValueChange={setKind}
          options={[
            { value: 'best_before', label: 'Best before' },
            { value: 'use_by', label: 'Use by' },
          ]}
        />
        {date && (
          <button
            type="button"
            className="self-start border-0 bg-transparent p-0 font-semibold text-expired"
            onClick={() => setDate('')}
          >
            Clear the date
          </button>
        )}
      </SheetContent>
    </Sheet>
  )
}

const normalize = (s: string) => s.normalize('NFKD').replace(/[̀-ͯ]/g, '').toLowerCase()

function IngredientSheet({ open, onClose, row }: SheetProps) {
  const ingredients = useIngredients()
  const [q, setQ] = useState('')
  const matches: Ingredient[] = q.trim()
    ? ingredients
        .filter((i) =>
          [i.name, i.plural ?? '', ...i.aliases].some((n) =>
            normalize(n).includes(normalize(q.trim())),
          ),
        )
        .slice(0, 8)
    : []
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent
        title="Matches recipes as"
        description="Recipes that call for this ingredient will find this product."
      >
        <Input
          aria-label="Search ingredients"
          placeholder="Search ingredients"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <ul className="m-0 flex list-none flex-col p-0">
          {matches.map((i) => (
            <li key={i.id}>
              <button
                type="button"
                className="flex w-full items-center justify-between rounded-md border-0 bg-transparent px-3 py-2.5 text-left text-ink hover:bg-surface-2"
                onClick={() => {
                  void dispatch('catalog.product.set_ingredient', {
                    id: row.product.id,
                    ingredientId: i.id,
                  })
                  onClose()
                }}
              >
                <span className="font-semibold">{i.name}</span>
              </button>
            </li>
          ))}
        </ul>
      </SheetContent>
    </Sheet>
  )
}
