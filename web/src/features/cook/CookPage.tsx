import { ArrowLeft, Check, Minus, Plus, Refrigerator, Replace, Snowflake, X } from 'lucide-react'
import { type ReactNode, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { v7 } from 'uuid'
import { Page } from '../../app/Page'
import { format } from '../../lib/units'
import type { CommandPayloads } from '../../offline/commands'
import { db } from '../../offline/db'
import { dispatch } from '../../offline/outbox'
import type { Location, RecipeLine, StockItem, StockLevel } from '../../offline/replica'
import { Badge, Button, cn, Field, Segmented, Switch, Textarea, toast } from '../../ui'
import { addDays, today } from '../inventory/dates'
import { add, cmp, sub } from '../inventory/decimal'
import { estimate } from '../inventory/estimate'
import { buildChain } from '../inventory/foodRules'
import { expiryLabel, levelLabel, unitLabel } from '../inventory/status'
import { BlobImage } from '../photos/BlobImage'
import { PhotoPicker } from '../photos/PhotoPicker'
import { kitchenQty } from '../recipes/parts'
import { type CookData, useCookData } from './hooks'
import {
  boughtRegularly,
  leftAfter,
  matchingItems,
  scaledQty,
  suggest,
  type Take,
  usableSwaps,
  usesAll,
} from './plan'

type CookPayload = CommandPayloads['recipe.cook']

/** One recipe line's choices on the sheet. */
type LineState = {
  itemId: string | null
  take: Take | null
  /** The user changed the amount, so portion changes no longer re-suggest it. */
  edited: boolean
  needsAmount: boolean
  toList: boolean
  skip: boolean
  swapId: string | null
}

/** What the user changed on a line; everything else is suggested from stock and portions. */
type Override = {
  itemId?: string | null
  /** An amount the user set, for itemId (portion changes no longer re-suggest it). */
  take?: Take
  toList?: boolean
  skip?: boolean
  swapId?: string | null
}

type PlanContext = {
  recipeId: string
  stock: StockItem[]
  catalog: CookData['catalog']
  substitutions: CookData['substitutions']
  made: number
  servings: number | null
}

type Resolved = {
  swap?: ReturnType<typeof usableSwaps>[number]
  candidates: StockItem[]
  item: StockItem | undefined
  state: LineState
}

/** A line's stock item and amount: the user's picks, else first-expiring stock and a suggestion. */
function resolveLine(l: RecipeLine, o: Override | undefined, c: PlanContext): Resolved {
  const swapId = o?.swapId ?? null
  const swap = swapId
    ? usableSwaps(l, c.recipeId, c.substitutions, c.stock, c.catalog).find(
        (x) => x.sub.id === swapId,
      )
    : undefined
  const candidates = swap ? swap.items : matchingItems(l.ingredient_id, c.stock, c.catalog)
  const item = candidates.find((x) => x.id === o?.itemId) ?? candidates[0]
  const empty: LineState = {
    itemId: null,
    take: null,
    edited: false,
    needsAmount: false,
    toList: false,
    skip: false,
    swapId,
  }
  if (!item) return { swap, candidates, item, state: empty }
  const own = o?.take && o.itemId === item.id ? o.take : undefined
  const sug = own
    ? { take: own, needsAmount: false }
    : suggest(item, l, scaledQty(l, c.made, c.servings), c.catalog, swap?.ratio)
  return {
    swap,
    candidates,
    item,
    state: {
      itemId: item.id,
      take: sug.take,
      edited: !!own,
      needsAmount: sug.needsAmount,
      toList: o?.toList ?? boughtRegularly(item, c.catalog),
      skip: o?.skip ?? false,
      swapId,
    },
  }
}

const LEVELS: StockLevel[] = ['full', 'most', 'half', 'low', 'almost_out']

/** Required lines (not optional, not an assumed staple) are what a cook deducts. */
const required = (lines: RecipeLine[]) => lines.filter((l) => !l.optional && !l.staple)

const qtyLabel = (q: string, unit: string | null | undefined) =>
  [kitchenQty(q), unitLabel(unit ?? null)].filter(Boolean).join(' ')

function stepFor(unit: string | null): string {
  switch (unit) {
    case 'g':
    case 'ml':
      return '25'
    case 'kg':
    case 'l':
      return '0.1'
    case 'lb':
    case 'cup':
    case 'bunch':
      return '0.25'
    case 'tbsp':
    case 'tsp':
      return '0.5'
    default:
      return '1'
  }
}

/** The cook sheet (architecture-m2 §9): what was used, leftovers to store, one recipe.cook command. */
export function CookPage() {
  const { id } = useParams()
  const data = useCookData(id)
  const back = (
    <Button asChild variant="secondary" size="icon" aria-label="Back to the recipe">
      <Link to={id ? `/recipes/${id}` : '/recipes'}>
        <ArrowLeft aria-hidden />
      </Link>
    </Button>
  )
  if (data === undefined) return <Page title="I cooked this" actions={back} />
  if (data === null)
    return (
      <Page title="I cooked this" actions={back}>
        <p className="t-body-sm m-0 text-ink-muted">This recipe isn't here any more.</p>
      </Page>
    )
  return <CookSheet data={data} back={back} />
}

function CookSheet({ data, back }: { data: CookData; back: ReactNode }) {
  const { recipe, version, stock, catalog, locations, lists, substitutions } = data
  const navigate = useNavigate()
  const servings = version.servings ?? recipe.servings ?? null
  const [made, setMade] = useState(servings && servings > 0 ? Math.min(servings, 100) : 2)
  const lines = useMemo(() => required(version.ingredients), [version.ingredients])
  const fridge = locations.find((l) => l.kind === 'fridge')
  const freezer = locations.find((l) => l.kind === 'freezer')
  const [toFridge, setToFridge] = useState<number | null>(null)
  const [toFreezer, setToFreezer] = useState(0)
  const fridgeN = toFridge ?? Math.max(0, made - toFreezer)
  const [photos, setPhotos] = useState<string[]>([])
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [overrides, setOverrides] = useState<Record<string, Override>>({})

  const ctx: PlanContext = { recipeId: recipe.id, stock, catalog, substitutions, made, servings }
  const resolved = Object.fromEntries(
    lines.map((l) => [l.line_id, resolveLine(l, overrides[l.line_id], ctx)]),
  )
  const state = Object.fromEntries(lines.map((l) => [l.line_id, resolved[l.line_id]?.state]))

  const patch = (lineId: string, p: Override) =>
    setOverrides((prev) => ({ ...prev, [lineId]: { ...prev[lineId], ...p } }))

  const stored = fridgeN + toFreezer
  const uses = lines.flatMap((l) => {
    const s = state[l.line_id]
    const item = s?.itemId ? stock.find((x) => x.id === s.itemId) : undefined
    if (!s || !item || !s.take || s.skip) return []
    if (s.take.kind === 'exact' && cmp(s.take.quantity, '0') <= 0) return []
    return [{ line: l, item, s, take: s.take }]
  })

  const eatBy = useEatBy(fridge, freezer)

  async function submit() {
    setBusy(true)
    const session = v7()
    const list = lists[0]
    const existing = [...catalog.products.values()].find((p) => p.recipeId === recipe.id)
    const payload: CookPayload = {
      id: session,
      recipeId: recipe.id,
      versionId: version.id,
      cookedOn: today(),
      portionsMade: made,
      leftoverProductId: stored > 0 ? (existing?.id ?? v7()) : null,
      uses: uses.map(({ line, item, s, take }) => {
        const all = usesAll(item, take)
        return {
          lineId: line.line_id,
          stockItemId: item.id,
          eventId: v7(),
          ...(all
            ? { finished: true }
            : take.kind === 'exact'
              ? { quantity: take.quantity }
              : take.kind === 'portions'
                ? { portions: take.portions }
                : { level: take.level as StockLevel }),
          toShopping: all && s.toList && list ? { id: v7(), listId: list.id } : null,
        }
      }),
      store: [
        ...(fridge && fridgeN > 0
          ? [{ stockItemId: v7(), eventId: v7(), locationId: fridge.id, portions: fridgeN }]
          : []),
        ...(freezer && toFreezer > 0
          ? [{ stockItemId: v7(), eventId: v7(), locationId: freezer.id, portions: toFreezer }]
          : []),
      ],
      subs: lines.flatMap((l) => {
        const sw = state[l.line_id]?.swapId
        return sw ? [{ lineId: l.line_id, substitutionId: sw }] : []
      }),
      photos: photos.map((ref) => ({ id: v7(), blobRef: ref })),
      note: note.trim() || null,
    }
    try {
      await dispatch('recipe.cook', payload)
    } finally {
      setBusy(false)
    }
    const parts = [
      `Updated ${uses.length} ${uses.length === 1 ? 'item' : 'items'}`,
      stored > 0 ? `${stored} ${stored === 1 ? 'portion' : 'portions'} stored` : '',
    ].filter(Boolean)
    toast({
      title: parts.join(' · '),
      tone: 'success',
      duration: 10000,
      action: { label: 'Undo', onClick: () => void dispatch('recipe.cook.undo', { id: session }) },
    })
    navigate(`/recipes/${recipe.id}`)
  }

  return (
    <Page title="I cooked this" actions={back}>
      <section
        aria-label="Portions"
        className="flex flex-col gap-3 rounded-lg bg-surface p-4 shadow-card"
      >
        <div>
          <h2 className="t-title m-0 text-[20px]">{recipe.title}</h2>
          {servings ? (
            <span className="t-body-sm text-ink-muted">
              Recipe makes {servings} {servings === 1 ? 'portion' : 'portions'}
            </span>
          ) : null}
        </div>
        <Row
          label="Portions made"
          hint="How many servings this batch makes"
          control={
            <Stepper
              label="portions made"
              value={String(made)}
              onMinus={() => setMade((m) => Math.max(1, m - 1))}
              onPlus={() => setMade((m) => Math.min(100, m + 1))}
            />
          }
        />
      </section>

      <div className="flex items-baseline justify-between gap-2 px-0.5">
        <h2 className="m-0 text-[17px] font-semibold">What you used</h2>
        <span className="t-body-sm text-ink-muted">
          Scaled to {made} {made === 1 ? 'portion' : 'portions'}
        </span>
      </div>
      {lines.length === 0 ? (
        <p className="t-body-sm m-0 text-ink-muted">
          This recipe has no ingredients to take from stock.
        </p>
      ) : (
        <ul
          aria-label="Ingredients used"
          className="m-0 flex list-none flex-col rounded-lg bg-surface p-0 shadow-card"
        >
          {lines.map((l) => (
            <UseRow
              key={l.line_id}
              line={l}
              s={state[l.line_id]}
              data={data}
              made={made}
              servings={servings}
              plan={resolved[l.line_id] as Resolved}
              onChange={(p) => patch(l.line_id, p)}
            />
          ))}
        </ul>
      )}

      <h2 className="m-0 px-0.5 text-[17px] font-semibold">Store the portions</h2>
      <section
        aria-label="Store the portions"
        className="flex flex-col gap-3.5 rounded-lg bg-surface p-4 shadow-card"
      >
        {fridge ? (
          <Row
            label={
              <span className="flex items-center gap-2 [&_svg]:size-5">
                <Refrigerator aria-hidden /> {fridge.name}
              </span>
            }
            control={
              <Stepper
                label={`portions in the ${fridge.name.toLowerCase()}`}
                value={String(fridgeN)}
                onMinus={() => setToFridge(Math.max(0, fridgeN - 1))}
                onPlus={() => setToFridge(Math.min(made - toFreezer, fridgeN + 1))}
              />
            }
          />
        ) : null}
        {freezer ? (
          <Row
            label={
              <span className="flex items-center gap-2 [&_svg]:size-5">
                <Snowflake aria-hidden /> {freezer.name}
              </span>
            }
            control={
              <Stepper
                label={`portions in the ${freezer.name.toLowerCase()}`}
                value={String(toFreezer)}
                onMinus={() => setToFreezer(Math.max(0, toFreezer - 1))}
                onPlus={() => {
                  if (toFreezer >= made) return
                  setToFreezer(toFreezer + 1)
                  if (toFridge !== null && toFridge + toFreezer + 1 > made)
                    setToFridge(Math.max(0, made - toFreezer - 1))
                }}
              />
            }
          />
        ) : null}
        <p className="t-body-sm m-0 text-ink-muted">
          {made - stored > 0 ? (
            <>
              <b className="text-ink">{made - stored} not stored</b> (not tracked).{' '}
            </>
          ) : null}
          {fridgeN > 0 && eatBy.fridge
            ? `${fridge?.name} portions: eat by ${dayLabel(eatBy.fridge)}. `
            : ''}
          {toFreezer > 0 && eatBy.freezer
            ? `${freezer?.name} portions keep until ${dayLabel(eatBy.freezer)}.`
            : ''}
        </p>
      </section>

      <section
        aria-label="Photo and note"
        className="flex flex-col gap-3 rounded-lg bg-surface p-4 shadow-card"
      >
        <div className="flex items-center justify-between gap-2">
          <span className="font-semibold">Photo</span>
          <PhotoPicker onPicked={(refs) => setPhotos((p) => [...p, ...refs].slice(0, 10))} />
        </div>
        {photos.length > 0 ? (
          <div className="grid grid-cols-4 gap-2">
            {photos.map((ref) => (
              <div key={ref} className="relative">
                <BlobImage
                  blobRef={ref}
                  w={256}
                  alt="Photo of this cook"
                  className="aspect-square w-full rounded-md object-cover"
                />
                <button
                  type="button"
                  aria-label="Remove photo"
                  className="absolute top-1 right-1 grid size-6 place-items-center rounded-full bg-ink/70 text-bg [&_svg]:size-3.5"
                  onClick={() => setPhotos((p) => p.filter((x) => x !== ref))}
                >
                  <X aria-hidden />
                </button>
              </div>
            ))}
          </div>
        ) : null}
        <Field label="Note">
          {(f) => (
            <Textarea
              {...f}
              value={note}
              maxLength={2000}
              placeholder="How did it turn out?"
              onChange={(e) => setNote(e.target.value)}
            />
          )}
        </Field>
      </section>

      <div className="sticky bottom-[calc(76px+env(safe-area-inset-bottom))] z-20 -mx-4 border-t border-border bg-bg px-4 py-3 md:bottom-0 md:-mx-8 md:flex md:justify-end md:px-8">
        <Button block className="md:w-auto" disabled={busy} onClick={() => void submit()}>
          <Check aria-hidden />
          {stored > 0
            ? `Update stock and store ${stored} ${stored === 1 ? 'portion' : 'portions'}`
            : 'Update stock'}
        </Button>
      </div>
    </Page>
  )
}

function dayLabel(d: string): string {
  return new Date(`${d}T12:00:00`).toLocaleDateString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
  })
}

/** Eat-by dates for leftovers stored today, from the leftovers food rule (fridge 4 d, freezer 90 d). */
function useEatBy(fridge: Location | undefined, freezer: Location | undefined) {
  const [dates, setDates] = useState<{ fridge?: string; freezer?: string }>({})
  useEffect(() => {
    let live = true
    void (async () => {
      const cat = await db.category.filter((c) => c.slug === 'leftovers').first()
      const chain = await buildChain(db, {
        ingredientId: null,
        categoryId: cat?.id ?? null,
        shelfLife: null,
      })
      const on = today()
      const f = fridge ? estimate(chain, 'add', 'fridge', on) : null
      const z = freezer ? estimate(chain, 'add', 'freezer', on) : null
      if (live) setDates({ fridge: f ?? addDays(on, 4), freezer: z ?? addDays(on, 90) })
    })()
    return () => {
      live = false
    }
  }, [fridge, freezer])
  return dates
}

function Row({ label, hint, control }: { label: ReactNode; hint?: string; control: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-2.5">
      <span className="min-w-0">
        <b>{label}</b>
        {hint ? <span className="t-body-sm block text-ink-muted">{hint}</span> : null}
      </span>
      {control}
    </div>
  )
}

function Stepper({
  label,
  value,
  onMinus,
  onPlus,
  children,
}: {
  label: string
  value: string
  onMinus: () => void
  onPlus: () => void
  children?: ReactNode
}) {
  return (
    <div className="flex shrink-0 items-center gap-1 rounded-full bg-surface-2 p-[3px]">
      <Button
        variant="ghost"
        size="icon"
        aria-label={`Less ${label}`}
        className="size-8 bg-surface shadow-card"
        onClick={onMinus}
      >
        <Minus aria-hidden />
      </Button>
      {children ?? (
        <output
          aria-label={label}
          className="tabular min-w-16 px-1 text-center font-bold whitespace-nowrap"
        >
          {value}
        </output>
      )}
      <Button
        variant="ghost"
        size="icon"
        aria-label={`More ${label}`}
        className="size-8 bg-surface shadow-card"
        onClick={onPlus}
      >
        <Plus aria-hidden />
      </Button>
    </div>
  )
}

function itemLabel(it: StockItem, data: CookData): string {
  const p = data.catalog.products.get(it.productId)
  const loc = data.locations.find((l) => l.id === it.locationId)
  const amount =
    it.amountKind === 'exact'
      ? qtyLabel(it.quantity ?? '0', it.unit)
      : it.amountKind === 'portions'
        ? `${it.portions} portions`
        : it.level
          ? levelLabel[it.level]
          : ''
  return [
    p?.name ?? 'Item',
    amount,
    loc?.name,
    it.expiresOn ? expiryLabel(it.expiresOn, today()) : '',
  ]
    .filter(Boolean)
    .join(' · ')
}

function UseRow({
  line,
  s,
  data,
  made,
  servings,
  plan,
  onChange,
}: {
  line: RecipeLine
  s: LineState | undefined
  data: CookData
  made: number
  servings: number | null
  plan: Resolved
  onChange: (p: Override) => void
}) {
  const { swap, candidates, item } = plan
  const need = scaledQty(line, made, servings)
  const recipeAmount = need ? qtyLabel(format(need), line.unit) : ''
  const swaps = item
    ? []
    : usableSwaps(line, data.recipe.id, data.substitutions, data.stock, data.catalog)
  const swapName = swap ? data.catalog.ingredients.get(swap.ingredientId)?.name : undefined
  const take = s?.take
  const all = item && take ? usesAll(item, take) : false
  const left = item && take ? leftAfter(item, take) : null

  const header = (
    <span className="flex min-w-0 flex-col">
      <span className="font-semibold">{line.name}</span>
      <span className="text-[12.5px] text-ink-muted">
        {swap
          ? `Swapped for ${swapName ?? swap.sub.note ?? 'a substitute'}`
          : recipeAmount
            ? `Recipe: ${recipeAmount}`
            : 'Recipe: to taste'}
      </span>
    </span>
  )

  if (!item || !take) {
    return (
      <li className={cn('flex flex-col gap-2 border-border px-4 py-3 opacity-80 [&+&]:border-t')}>
        <div className="flex items-center justify-between gap-2.5">
          {header}
          <Badge>Not in stock</Badge>
        </div>
        {swaps.map((sw) => (
          <Button
            key={sw.sub.id}
            variant="secondary"
            size="sm"
            className="self-start"
            onClick={() => onChange({ swapId: sw.sub.id, itemId: null, take: undefined })}
          >
            <Replace aria-hidden />
            Swap for {data.catalog.ingredients.get(sw.ingredientId)?.name ?? 'substitute'}
          </Button>
        ))}
      </li>
    )
  }

  const setTake = (t: Take) => onChange({ take: t, itemId: item.id })
  const unit = item.unit
  const step = stepFor(unit)

  return (
    <li
      className={cn(
        'flex flex-col gap-2 border-border px-4 py-3 [&+&]:border-t',
        s?.skip && 'opacity-60',
      )}
    >
      <div className="flex items-center justify-between gap-2.5">
        {header}
        {s?.skip ? null : take.kind === 'level' ? null : (
          <Stepper
            label={line.name}
            value={take.kind === 'exact' ? qtyLabel(take.quantity, unit) : `${take.portions}`}
            onMinus={() =>
              take.kind === 'exact'
                ? setTake({
                    kind: 'exact',
                    quantity: cmp(take.quantity, step) <= 0 ? '0' : sub(take.quantity, step),
                  })
                : setTake({ kind: 'portions', portions: Math.max(1, take.portions - 1) })
            }
            onPlus={() =>
              take.kind === 'exact'
                ? setTake({
                    kind: 'exact',
                    quantity:
                      cmp(add(take.quantity, step), item.quantity ?? '0') > 0
                        ? (item.quantity ?? '0')
                        : add(take.quantity, step),
                  })
                : setTake({
                    kind: 'portions',
                    portions: Math.min(item.portions ?? 1, take.portions + 1),
                  })
            }
          />
        )}
      </div>
      {candidates.length > 1 ? (
        <select
          aria-label={`Stock item for ${line.name}`}
          className="h-9 rounded-md border border-border bg-surface px-2 text-[13px]"
          value={item.id}
          onChange={(e) => onChange({ itemId: e.target.value, take: undefined })}
        >
          {candidates.map((c) => (
            <option key={c.id} value={c.id}>
              {itemLabel(c, data)}
            </option>
          ))}
        </select>
      ) : (
        <span className="text-[12.5px] text-ink-muted">From {itemLabel(item, data)}</span>
      )}
      {take.kind === 'level' && !s?.skip ? (
        <Segmented
          aria-label={`Level of ${line.name} after cooking`}
          value={take.level}
          onValueChange={(v) => setTake({ kind: 'level', level: v })}
          options={[
            ...LEVELS.slice(item.level ? LEVELS.indexOf(item.level) + 1 : 0).map((l) => ({
              value: l as StockLevel | 'finished',
              label: levelLabel[l],
            })),
            { value: 'finished' as const, label: 'Finished' },
          ]}
        />
      ) : null}
      {s?.needsAmount && !s.skip ? (
        <Badge tone="soon" className="self-start">
          Set amount: recipe says {recipeAmount || 'to taste'}
        </Badge>
      ) : null}
      <div className="flex flex-wrap items-center justify-between gap-2 text-[12.5px] text-ink-muted">
        {s?.skip ? (
          <span>Not taken from stock</span>
        ) : all ? (
          <span className="flex items-center gap-2">
            <span className="font-semibold text-low">Uses it all.</span> Add to shopping list?
            <Switch
              aria-label={`Add ${line.name} to the shopping list`}
              checked={!!s?.toList}
              onCheckedChange={(on) => onChange({ toList: on, itemId: item.id })}
            />
          </span>
        ) : (
          <span>
            Leaves{' '}
            <b className="text-ink">
              {take.kind === 'exact' && left
                ? qtyLabel(left, unit)
                : take.kind === 'portions'
                  ? `${left} portions`
                  : left
                    ? levelLabel[left as StockLevel]
                    : ''}
            </b>{' '}
            in stock
          </span>
        )}
        <Button
          variant="link"
          size="sm"
          onClick={() => onChange({ skip: !s?.skip, itemId: item.id })}
        >
          {s?.skip ? 'Use it' : 'Skip'}
        </Button>
      </div>
    </li>
  )
}
