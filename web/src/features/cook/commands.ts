import { v5 } from 'uuid'
import type { StockpotDB } from '../../offline/db'
import { applyReducer, registerReducer } from '../../offline/reducers'
import type { CookSession, StockEvent, StockItem } from '../../offline/replica'
import '../catalog/commands'
import '../inventory/commands'
import { add, cmp, isZero, sub } from '../inventory/decimal'
import '../shopping/commands'

/**
 * Optimistic reducers for "I cooked this", mirroring `internal/cook` and `internal/inventory/cook.go`.
 * A cook touches many rows, so the reducer is idempotent per row rather than declaring targets: every
 * delta it applies is recorded on the row it changed (`appliedEvents`, local only). A pull that
 * overwrites the row drops the mark, so the delta applies again on top of server truth, exactly once.
 */

/** A `cook_session.uses` entry (snake_case jsonb, as the server writes it). */
export type CookUseRecord = {
  line_id?: string
  stock_item_id: string
  event_id: string
  product_id?: string
  amount_kind?: StockItem['amountKind']
  quantity?: string
  unit?: string
  portions?: number
  level_before?: StockItem['level']
  level_after?: StockItem['level']
  finished?: boolean
  shopping_item_id?: string
  skipped?: boolean
}

/** A `cook_session.stored` entry. */
export type CookStoreRecord = {
  location_id: string
  portions: number
  stock_item_id: string
  event_id: string
  expires_on?: string
}

/** The compensating event undo writes for an event: uuidv5(event id, "cook.undo"), as the server. */
export const undoEventId = (eventId: string) => v5('cook.undo', eventId)

type Marked = StockItem & { appliedEvents?: string[] }

const applied = (it: StockItem, eventId: string) =>
  (it as Marked).appliedEvents?.includes(eventId) ?? false

const mark = (it: StockItem, eventId: string): Marked => ({
  ...it,
  appliedEvents: [...((it as Marked).appliedEvents ?? []), eventId],
})

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
  if (await db.stock_event.get(e.id)) return
  await db.stock_event.put({
    ...(await base(db, e.id, clientTime)),
    delta: null,
    unit: null,
    fromLocationId: null,
    toLocationId: null,
    reason: null,
    actorId: null,
    commandId: null,
    cookSessionId: null,
    ...e,
  })
}

type Use = {
  lineId?: string | null
  stockItemId: string
  eventId: string
  quantity?: string | null
  portions?: number | null
  level?: StockItem['level']
  finished?: boolean
  toShopping?: { id: string; listId: string; qty?: string | null; unit?: string | null } | null
}

async function applyUse(
  db: StockpotDB,
  session: string,
  recipeId: string,
  u: Use,
  prior: CookUseRecord | undefined,
  clientTime: string,
): Promise<CookUseRecord> {
  const it = await db.stock_item.get(u.stockItemId)
  // Already applied (or finished by this cook and removed): keep what it did.
  if (prior && (!it || applied(it, u.eventId))) return prior
  if (!it) return { line_id: u.lineId ?? undefined, stock_item_id: u.stockItemId, event_id: u.eventId, skipped: true }
  const rec: CookUseRecord = {
    line_id: u.lineId ?? undefined,
    stock_item_id: it.id,
    event_id: u.eventId,
    product_id: it.productId,
    amount_kind: it.amountKind,
    unit: it.unit ?? undefined,
    finished: !!u.finished,
  }
  let next: StockItem | null = it
  const ev: Pick<StockEvent, 'kind' | 'delta' | 'reason'> = { kind: 'consume', delta: null, reason: 'cook' }
  if (it.amountKind === 'exact' && it.quantity) {
    const want = u.finished ? it.quantity : (u.quantity ?? '0')
    const taken = cmp(want, it.quantity) >= 0 ? it.quantity : want
    const left = sub(it.quantity, taken)
    rec.quantity = taken
    rec.finished = isZero(left)
    next = rec.finished ? null : { ...it, quantity: left }
    ev.delta = `-${taken}`
  } else if (it.amountKind === 'portions' && it.portions != null) {
    const taken = u.finished ? it.portions : Math.min(u.portions ?? 0, it.portions)
    rec.portions = taken
    rec.finished = it.portions - taken === 0
    next = rec.finished ? null : { ...it, portions: it.portions - taken }
    ev.delta = `-${taken}`
  } else if (it.amountKind === 'level') {
    rec.level_before = it.level
    if (u.finished) next = null
    else if (u.level) {
      rec.level_after = u.level
      next = { ...it, level: u.level }
      ev.kind = 'adjust'
      ev.reason = u.level
    }
  }
  if (next) await db.stock_item.put(mark(next, u.eventId))
  else await db.stock_item.delete(it.id)
  await logEvent(db, clientTime, {
    id: u.eventId,
    stockItemId: it.id,
    ...ev,
    unit: it.amountKind === 'level' ? null : it.unit,
    fromLocationId: it.locationId,
    cookSessionId: session,
  })
  if (u.toShopping) {
    await applyReducer(
      db,
      'shopping.item.add',
      {
        id: u.toShopping.id,
        listId: u.toShopping.listId,
        productId: it.productId,
        qty: u.toShopping.qty ?? undefined,
        unit: u.toShopping.unit ?? undefined,
        recipeId,
      },
      clientTime,
    )
    rec.shopping_item_id = u.toShopping.id
  }
  return rec
}

/** The recipe's leftover product: the existing one, else a new one under `want`. */
async function ensureLeftover(
  db: StockpotDB,
  recipeId: string,
  title: string,
  want: string,
  clientTime: string,
): Promise<string> {
  const existing = await db.product.filter((p) => p.recipeId === recipeId).first()
  if (existing) return existing.id
  const leftovers = await db.category.filter((c) => c.slug === 'leftovers').first()
  await applyReducer(
    db,
    'catalog.product.create',
    {
      id: want,
      name: leftoverName(title),
      categoryId: leftovers?.id ?? null,
    },
    clientTime,
  )
  const p = await db.product.get(want)
  if (p) await db.product.put({ ...p, recipeId, enrichState: 'skipped', defaultLocationKind: 'fridge' })
  return want
}

/** "Leftover chicken curry" (the server adds " (2)" when that name is taken). */
export function leftoverName(title: string): string {
  const r = [...title]
  if (r.length > 1 && r[1] === r[1]?.toUpperCase() && r[1] !== r[1]?.toLowerCase()) return `Leftover ${title}`
  return `Leftover ${(r[0] ?? '').toLowerCase()}${r.slice(1).join('')}`
}

async function refreshCounts(db: StockpotDB, recipeId: string) {
  const recipe = await db.recipe.get(recipeId)
  if (!recipe) return
  const live = (await db.cook_session.where('recipeId').equals(recipeId).toArray()).filter(
    (s) => !s.undoneAt,
  )
  const last = live.map((s) => s.cookedOn).sort().at(-1)
  await db.recipe.put({
    ...recipe,
    cookedCount: live.length,
    lastCookedAt: last ? `${last}T00:00:00Z` : null,
  })
}

registerReducer(
  'recipe.cook',
  async (db, p, { clientTime }) => {
    const recipe = await db.recipe.get(p.recipeId)
    if (!recipe) return
    const prev = await db.cook_session.get(p.id)
    if (prev?.undoneAt) return
    const priorUses = new Map(
      ((prev?.uses ?? []) as CookUseRecord[]).map((u) => [u.stock_item_id, u]),
    )
    const uses: CookUseRecord[] = []
    for (const u of p.uses) {
      uses.push(await applyUse(db, p.id, p.recipeId, u, priorUses.get(u.stockItemId), clientTime))
    }
    const stored: CookStoreRecord[] = []
    let leftoverProductId: string | null = prev?.leftoverProductId ?? null
    if (p.store.length > 0 && p.leftoverProductId) {
      leftoverProductId = await ensureLeftover(db, recipe.id, recipe.title, p.leftoverProductId, clientTime)
      for (const s of p.store) {
        if (!(await db.stock_item.get(s.stockItemId))) {
          await applyReducer(
            db,
            'inventory.stock.add',
            {
              id: s.stockItemId,
              eventId: s.eventId,
              productId: leftoverProductId,
              locationId: s.locationId,
              amountKind: 'portions',
              portions: s.portions,
              source: 'cook',
              on: p.cookedOn,
            },
            clientTime,
          )
          const it = await db.stock_item.get(s.stockItemId)
          if (it) await db.stock_item.put({ ...it, cookSessionId: p.id })
          const ev = await db.stock_event.get(s.eventId)
          if (ev) await db.stock_event.put({ ...ev, cookSessionId: p.id })
        }
        const it = await db.stock_item.get(s.stockItemId)
        stored.push({
          location_id: s.locationId,
          portions: s.portions,
          stock_item_id: s.stockItemId,
          event_id: s.eventId,
          expires_on: it?.expiresOn ?? undefined,
        })
      }
    }
    for (const ph of p.photos ?? []) {
      await applyReducer(
        db,
        'recipe.photo.add',
        { id: ph.id, recipeId: p.recipeId, blobRef: ph.blobRef, cookSessionId: p.id, takenOn: p.cookedOn },
        clientTime,
      )
    }
    const session: CookSession = {
      ...(prev ?? (await base(db, p.id, clientTime))),
      recipeId: p.recipeId,
      versionId: p.versionId,
      cookedOn: p.cookedOn,
      cookedBy: prev?.cookedBy ?? null,
      portionsMade: p.portionsMade,
      stored,
      uses,
      subs: (p.subs ?? []).map((s) => ({ line_id: s.lineId, substitution_id: s.substitutionId })),
      leftoverProductId,
      note: p.note?.trim() || null,
      undoneAt: null,
    }
    await db.cook_session.put(session)
    await refreshCounts(db, p.recipeId)
  },
  { blobs: (p) => (p.photos ?? []).map((x) => x.blobRef) },
)

registerReducer('recipe.cook.undo', async (db, p, { clientTime }) => {
  const s = await db.cook_session.get(p.id)
  if (!s || s.undoneAt) return
  for (const u of s.uses as CookUseRecord[]) {
    if (u.skipped) continue
    const undoId = undoEventId(u.event_id)
    const it = await db.stock_item.get(u.stock_item_id)
    // A finished item is back after the next pull; deltas onto a live item apply now.
    if (it && !applied(it, undoId)) {
      let next: StockItem | null = null
      if (it.amountKind === 'exact' && it.quantity && u.quantity) next = { ...it, quantity: add(it.quantity, u.quantity) }
      else if (it.amountKind === 'portions' && it.portions != null && u.portions) next = { ...it, portions: it.portions + u.portions }
      else if (it.amountKind === 'level' && u.level_before && it.level === u.level_after) next = { ...it, level: u.level_before }
      if (next) {
        await db.stock_item.put(mark(next, undoId))
        await logEvent(db, clientTime, {
          id: undoId,
          stockItemId: it.id,
          kind: 'adjust',
          delta: u.quantity ?? (u.portions != null ? String(u.portions) : null),
          unit: it.amountKind === 'level' ? null : it.unit,
          toLocationId: it.locationId,
          reason: 'cook_undo',
          cookSessionId: s.id,
        })
      }
    }
    if (u.shopping_item_id) {
      const item = await db.shopping_item.get(u.shopping_item_id)
      if (item && !item.checkedAt) await db.shopping_item.delete(item.id)
    }
  }
  for (const st of s.stored as CookStoreRecord[]) {
    const it = await db.stock_item.get(st.stock_item_id)
    const events = await db.stock_event.where('stockItemId').equals(st.stock_item_id).toArray()
    // Leftovers nobody touched since go; ones someone used are real stock now.
    if (it && events.every((e) => e.cookSessionId === s.id)) await db.stock_item.delete(it.id)
  }
  await db.recipe_photo.where('cookSessionId').equals(s.id).delete()
  await db.cook_session.put({ ...s, undoneAt: clientTime })
  await refreshCounts(db, s.recipeId)
})

registerReducer('catalog.product.set_conversion', async (db, p) => {
  const product = await db.product.get(p.id)
  if (!product) return
  const conversions = { ...(product.conversions ?? {}) }
  if (p.amount) conversions[p.unit] = p.amount.trim().replace(/\s+/, ' ')
  else delete conversions[p.unit]
  await db.product.put({ ...product, conversions })
})
