import type { StockpotDB } from '../../offline/db'
import { applyReducer, registerReducer } from '../../offline/reducers'
import type { LocationKind, StockEvent, StockItem } from '../../offline/replica'
import '../catalog/commands' // product create runs inside stock add
import { today } from './dates'
import { add, isZero, sub, subClamped } from './decimal'
import { estimate, type Op } from './estimate'
import { buildChain } from './foodRules'

/**
 * Optimistic reducers for the inventory commands. Each mirrors its server handler
 * (`internal/inventory/commands.go`); the next pull overwrites the result with server truth.
 * Event rows use the payload's `eventId`, so a replay after a pull rewrites the same row.
 * Amount deltas (consume, split) declare their targets, so they apply exactly once on top of
 * whatever the pull wrote.
 */

const byId = { targets: (p: { id: string }) => [p.id] }

async function base(db: StockpotDB, id: string, clientTime: string) {
  const household = (await db.meta.get('sync.household'))?.value
  return {
    id,
    householdId: typeof household === 'string' ? household : null,
    createdAt: clientTime,
    updatedAt: clientTime,
    rev: 0,
  }
}

async function logEvent(
  db: StockpotDB,
  clientTime: string,
  e: Pick<StockEvent, 'id' | 'stockItemId' | 'kind'> & Partial<StockEvent>,
) {
  await db.stock_event.put({
    ...(await base(db, e.id, clientTime)),
    delta: null,
    unit: null,
    fromLocationId: null,
    toLocationId: null,
    reason: null,
    actorId: null,
    commandId: null,
    ...e,
  })
}

async function kindOf(db: StockpotDB, locationId: string | null): Promise<LocationKind> {
  return (locationId && (await db.location.get(locationId))?.kind) || 'other'
}

type Amount = {
  amountKind: StockItem['amountKind']
  quantity?: string | null
  unit?: string | null
  portions?: number | null
  level?: StockItem['level']
}

const amountFields = (a: Amount) => ({
  amountKind: a.amountKind,
  quantity: a.quantity ?? null,
  unit: a.unit ?? null,
  portions: a.portions ?? null,
  level: a.level ?? null,
})

const deltaOf = (q?: string | null, p?: number | null) => q ?? (p != null ? String(p) : null)
const negate = (d: string | null) => (d === null ? null : `-${d}`)

registerReducer(
  'inventory.stock.add',
  async (db, p, { clientTime }) => {
    if (await db.stock_item.get(p.id)) return
    const on = p.on ?? today()
    let productId = p.productId
    if (p.newProduct) {
      productId = p.newProduct.id
      await applyReducer(db, 'catalog.product.create', p.newProduct, clientTime)
    }
    const product = productId ? await db.product.get(productId) : undefined
    if (!product) return
    const loc = await kindOf(db, p.locationId)
    let expiresOn = p.expiresOn ?? null
    let expiryKind: StockItem['expiryKind'] = p.expiresOn ? (p.expiryKind ?? 'best_before') : null
    if (!expiresOn) {
      expiresOn = estimate(await buildChain(db, product), 'add', loc, on)
      if (expiresOn) expiryKind = 'estimated'
    }
    await db.stock_item.put({
      ...(await base(db, p.id, clientTime)),
      productId: product.id,
      locationId: p.locationId,
      ...amountFields(p),
      low: false,
      expiresOn,
      expiryKind,
      openedOn: p.openedOn ?? null,
      thawedOn: null,
      frozenOn: loc === 'freezer' ? on : null,
      source: p.source,
      parentItemId: null,
      note: p.note ?? null,
    })
    await logEvent(db, clientTime, {
      id: p.eventId,
      stockItemId: p.id,
      kind: 'add',
      delta: deltaOf(p.quantity, p.portions),
      unit: p.unit ?? null,
      toLocationId: p.locationId,
    })
  },
  byId,
)

/** Removes a finished item the way the server's tombstone does. */
const finishItem = (db: StockpotDB, id: string) => db.stock_item.delete(id)

registerReducer(
  'inventory.stock.consume',
  async (db, p, { clientTime }) => {
    const it = await db.stock_item.get(p.id)
    if (!it) return
    let taken: string | null
    if (it.amountKind === 'exact' && p.quantity && it.quantity) {
      const left = subClamped(it.quantity, p.quantity)
      taken = sub(it.quantity, left)
      if (isZero(left)) await finishItem(db, it.id)
      else await db.stock_item.put({ ...it, quantity: left })
    } else if (it.amountKind === 'portions' && p.portions && it.portions != null) {
      const left = Math.max(0, it.portions - p.portions)
      taken = String(it.portions - left)
      if (left === 0) await finishItem(db, it.id)
      else await db.stock_item.put({ ...it, portions: left })
    } else return
    await logEvent(db, clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: 'consume',
      delta: negate(taken),
      unit: it.unit,
      fromLocationId: it.locationId,
      reason: p.reason ?? null,
    })
  },
  // Delta: replayed only when a pull rewrote this item, so it applies once on server state.
  byId,
)

registerReducer(
  'inventory.stock.adjust',
  async (db, p, { clientTime }) => {
    const it = await db.stock_item.get(p.id)
    if (!it || it.amountKind === 'level') return
    let delta: string
    let zero: boolean
    if (it.amountKind === 'exact' && p.quantity != null) {
      delta = sub(p.quantity, it.quantity ?? '0')
      zero = isZero(p.quantity)
      if (!zero) await db.stock_item.put({ ...it, quantity: p.quantity })
    } else if (p.portions != null) {
      delta = String(p.portions - (it.portions ?? 0))
      zero = p.portions === 0
      if (!zero) await db.stock_item.put({ ...it, portions: p.portions })
    } else return
    if (zero) await finishItem(db, it.id)
    await logEvent(db, clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: 'adjust',
      delta,
      unit: it.unit,
    })
  },
  byId,
)

registerReducer(
  'inventory.stock.set_level',
  async (db, p, { clientTime }) => {
    const it = await db.stock_item.get(p.id)
    if (!it || it.amountKind !== 'level') return
    await db.stock_item.put({ ...it, level: p.level })
    await logEvent(db, clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: 'adjust',
      reason: p.level,
    })
  },
  byId,
)

const mark =
  (low: boolean) =>
  async (db: StockpotDB, p: { id: string; eventId: string }, ctx: { clientTime: string }) => {
    const it = await db.stock_item.get(p.id)
    if (!it) return
    await db.stock_item.put({ ...it, low })
    await logEvent(db, ctx.clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: 'low',
      reason: low ? 'marked' : 'cleared',
    })
  }
registerReducer('inventory.stock.mark_low', mark(true), byId)
registerReducer('inventory.stock.unmark_low', mark(false), byId)

/** Mirrors the server's relocate: freezer boundaries restart clocks; user-given dates otherwise stay. */
async function relocate(
  db: StockpotDB,
  it: StockItem,
  to: string,
  op: Op | undefined,
  on: string,
): Promise<StockItem> {
  const kind = await kindOf(db, to)
  const from = await kindOf(db, it.locationId)
  const freezing = kind === 'freezer' && from !== 'freezer'
  const thawing = kind !== 'freezer' && from === 'freezer'
  const next: Op = op ?? (freezing ? 'freeze' : thawing ? 'thaw' : 'add')
  const crosses = next === 'freeze' || next === 'thaw'
  const out: StockItem = { ...it, locationId: to }
  if (next === 'freeze') out.frozenOn = on
  if (next === 'thaw') out.thawedOn = on
  if (crosses || it.expiryKind === null || it.expiryKind === 'estimated') {
    const product = await db.product.get(it.productId)
    const date = product ? estimate(await buildChain(db, product), next, kind, on) : null
    if (date) Object.assign(out, { expiresOn: date, expiryKind: 'estimated' })
    else if (crosses) Object.assign(out, { expiresOn: null, expiryKind: null })
  }
  return out
}

registerReducer(
  'inventory.stock.move',
  async (db, p, { clientTime }) => {
    const it = await db.stock_item.get(p.id)
    if (!it || it.locationId === p.locationId) return
    await db.stock_item.put(await relocate(db, it, p.locationId, undefined, p.on ?? today()))
    await logEvent(db, clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: 'move',
      fromLocationId: it.locationId,
      toLocationId: p.locationId,
    })
  },
  byId,
)

const transfer =
  (op: 'thaw' | 'freeze') =>
  async (
    db: StockpotDB,
    p: { id: string; eventId: string; locationId: string; on?: string },
    ctx: { clientTime: string },
  ) => {
    const it = await db.stock_item.get(p.id)
    if (!it) return
    await db.stock_item.put(await relocate(db, it, p.locationId, op, p.on ?? today()))
    await logEvent(db, ctx.clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: op,
      fromLocationId: it.locationId,
      toLocationId: p.locationId,
    })
  }
registerReducer('inventory.stock.thaw', transfer('thaw'), byId)
registerReducer('inventory.stock.freeze', transfer('freeze'), byId)

registerReducer(
  'inventory.stock.open',
  async (db, p, { clientTime }) => {
    const it = await db.stock_item.get(p.id)
    if (!it || it.openedOn) return
    const on = p.on ?? today()
    const out: StockItem = { ...it, openedOn: on }
    const product = await db.product.get(it.productId)
    const date = product
      ? estimate(await buildChain(db, product), 'open', await kindOf(db, it.locationId), on)
      : null
    // Opening only shortens an expiry.
    if (date && (!it.expiresOn || date < it.expiresOn)) {
      out.expiresOn = date
      out.expiryKind = 'estimated'
    }
    await db.stock_item.put(out)
    await logEvent(db, clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: 'open',
      fromLocationId: it.locationId,
    })
  },
  byId,
)

registerReducer(
  'inventory.stock.discard',
  async (db, p, { clientTime }) => {
    const it = await db.stock_item.get(p.id)
    if (!it) return
    await finishItem(db, it.id)
    await logEvent(db, clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: 'discard',
      delta: negate(deltaOf(it.quantity, it.portions)),
      unit: it.unit,
      fromLocationId: it.locationId,
      reason: p.reason,
    })
  },
  byId,
)

registerReducer(
  'inventory.stock.update',
  async (db, p, { clientTime }) => {
    const it = await db.stock_item.get(p.id)
    if (!it) return
    const next: StockItem = { ...it }
    if ('expiresOn' in p) {
      next.expiresOn = p.expiresOn ?? null
      if (!next.expiresOn) next.expiryKind = null
      else if (!next.expiryKind || next.expiryKind === 'estimated') next.expiryKind = 'best_before'
    }
    if ('expiryKind' in p && p.expiryKind) next.expiryKind = p.expiryKind
    if ('note' in p) next.note = p.note ?? null
    if (p.amount) Object.assign(next, amountFields(p.amount))
    await db.stock_item.put(next)
    await logEvent(db, clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: 'adjust',
      reason: 'edit',
    })
  },
  byId,
)

registerReducer(
  'inventory.stock.split',
  async (db, p, { clientTime }) => {
    const it = await db.stock_item.get(p.id)
    if (!it || it.amountKind === 'level') return
    const to = p.locationId ?? it.locationId
    if (!to) return
    const on = p.on ?? today()
    const toKind = await kindOf(db, to)
    const fromKind = await kindOf(db, it.locationId)
    let { expiresOn, expiryKind, frozenOn } = it
    if (toKind === 'freezer' && fromKind !== 'freezer') {
      const product = await db.product.get(it.productId)
      frozenOn = on
      expiresOn = product ? estimate(await buildChain(db, product), 'freeze', toKind, on) : null
      expiryKind = expiresOn ? 'estimated' : null
    }
    let leftQ = it.quantity
    let leftP = it.portions
    let sumQ = '0'
    let sumP = 0
    for (const part of p.parts) {
      await db.stock_item.put({
        ...(await base(db, part.id, clientTime)),
        productId: it.productId,
        locationId: to,
        amountKind: it.amountKind,
        quantity: it.amountKind === 'exact' ? (part.quantity ?? null) : null,
        unit: it.amountKind === 'exact' ? it.unit : null,
        portions: it.amountKind === 'portions' ? (part.portions ?? null) : null,
        level: null,
        low: false,
        expiresOn,
        expiryKind,
        openedOn: it.openedOn,
        thawedOn: null,
        frozenOn,
        source: 'split',
        parentItemId: it.id,
        note: it.note,
      })
      await logEvent(db, clientTime, {
        id: part.eventId,
        stockItemId: part.id,
        kind: 'split',
        delta: deltaOf(part.quantity, part.portions),
        unit: it.amountKind === 'exact' ? it.unit : null,
        fromLocationId: it.locationId,
        toLocationId: to,
      })
      if (part.quantity) {
        sumQ = add(sumQ, part.quantity)
        leftQ = subClamped(leftQ ?? '0', part.quantity)
      }
      if (part.portions) {
        sumP += part.portions
        leftP = Math.max(0, (leftP ?? 0) - part.portions)
      }
    }
    const gone = it.amountKind === 'exact' ? isZero(leftQ ?? '0') : (leftP ?? 0) === 0
    if (gone) await finishItem(db, it.id)
    else await db.stock_item.put({ ...it, quantity: leftQ, portions: leftP })
    await logEvent(db, clientTime, {
      id: p.eventId,
      stockItemId: it.id,
      kind: 'split',
      delta: negate(it.amountKind === 'exact' ? sumQ : String(sumP)),
      unit: it.unit,
      fromLocationId: it.locationId,
      toLocationId: to,
    })
  },
  // Takes amounts off the parent, so it is a delta there; its parts are written by id, so a replay rewrites them.
  byId,
)
