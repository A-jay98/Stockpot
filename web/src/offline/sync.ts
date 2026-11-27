import { v7 } from 'uuid'
import { api } from '../api/client'
import { clearBlobCache, uploadPendingBlobs } from '../lib/photos'
import { db, type MetaRow, type OutboxRow, type StockpotDB } from './db'
import { syncHooks } from './outbox'
import { applyReducer, commandBlobRefs, reducerTargets } from './reducers'
import { camelizeRow, replicaTables } from './replica'

const REV = 'sync.rev'
const REV_GLOBAL = 'sync.revGlobal'
const HOUSEHOLD = 'sync.household'
const REBUILD = 'sync.rebuild'
const BATCH = 50
const PAGE = 1000

/** Problem the server (or client) recorded on a rejected command. */
export type RejectedProblem = { code: string; detail: string }

export function parseProblem(error?: string): RejectedProblem {
  try {
    return JSON.parse(error ?? '') as RejectedProblem
  } catch {
    return { code: 'invalid', detail: error ?? 'Rejected' }
  }
}

async function getMeta<T>(database: StockpotDB, key: string, fallback: T): Promise<T> {
  const row = await database.meta.get(key)
  return (row?.value as T | undefined) ?? fallback
}

const setMeta = (database: StockpotDB, key: string, value: unknown) =>
  database.meta.put({ key, value } satisfies MetaRow)

/** Empties the replica, the outbox, pending photos and the cursors (sign-out). */
export async function resetReplica(database: StockpotDB = db): Promise<void> {
  await database.transaction('rw', database.tables, async () => {
    await Promise.all(database.tables.map((t) => t.clear()))
  })
  await clearBlobCache()
}

/**
 * Makes the replica belong to `householdId`. Another household's rows and cursors are dropped;
 * its unsent commands stay in the outbox, parked under their own household (never sent to the
 * wrong one), and this household's own parked commands are shown optimistically again.
 */
export async function adoptHousehold(householdId: string, database: StockpotDB = db) {
  const current = await getMeta<string | null>(database, HOUSEHOLD, null)
  if (current === householdId) return
  await database.transaction('rw', database.tables, async () => {
    await Promise.all(replicaTables.map((t) => database.table(t).clear()))
    await database.meta.clear()
    await setMeta(database, HOUSEHOLD, householdId)
    await replayPending(database, householdId, 'all')
  })
  if (current !== null) await clearBlobCache()
}

/** Commands still waiting to be sent, in order, for the household the replica shows. */
async function pendingFor(database: StockpotDB, householdId: string | null) {
  const rows = await database.outbox.where('status').equals('pending').sortBy('seq')
  return rows.filter((r) => !r.householdId || r.householdId === householdId)
}

/**
 * Re-applies queued commands over rows the server just overwrote. A command is replayed when its
 * reducer didn't declare targets, or a target is among `ids` (rows the page wrote or deleted),
 * so delta reducers run exactly once on top of server state.
 */
async function replayPending(
  database: StockpotDB,
  householdId: string | null,
  ids: Set<string> | 'all',
) {
  for (const row of await pendingFor(database, householdId)) {
    const targets = reducerTargets(row.type, row.payload)
    if (ids === 'all' || targets === null || targets.some((id) => ids.has(id))) {
      await applyReducer(database, row.type, row.payload, row.clientTime)
    }
  }
}

export type FlushResult = 'done' | 'offline' | 'rejected'

type Result = {
  id: string
  status: 'applied' | 'duplicate' | 'rejected'
  problem?: RejectedProblem
}

type Sent = { results: Result[] } | 'bad_request' | 'offline' | 'server_error'

async function send(householdId: string, rows: OutboxRow[]): Promise<Sent> {
  try {
    const res = await api.POST('/v1/commands', {
      body: {
        householdId,
        commands: rows.map((r) => ({
          id: r.id,
          type: r.type,
          payload: r.payload,
          clientTime: r.clientTime,
        })) as never,
      },
    })
    const status = res.response.status
    if (status === 400) return 'bad_request'
    if (status >= 500) return 'server_error'
    if (!res.response.ok || !res.data) return 'offline' // 401 (sign-in handled elsewhere), 409 household switch
    return { results: res.data.results as Result[] }
  } catch {
    return 'offline'
  }
}

/** Server failures on the same command before it is set aside as rejected. */
const MAX_ATTEMPTS = 5

const refusal = (detail: string) => JSON.stringify({ code: 'invalid', detail })

/**
 * Sends this household's pending commands in `seq` order, in batches, each batch after the
 * photos it references are uploaded. Network failures stop the flush (commands stay pending). A
 * command the server can't process (400, or 5xx five times in a row when sent alone) is set aside
 * as rejected so it never blocks the ones behind it. Rejected commands stay for the "Couldn't
 * sync" sheet.
 */
export async function flushOutbox(database: StockpotDB = db): Promise<FlushResult> {
  const household = await getMeta<string | null>(database, HOUSEHOLD, null)
  if (!household) return 'done'
  let rejected = false
  let size = BATCH
  for (;;) {
    const rows = (await pendingFor(database, household)).slice(0, size)
    if (rows.length === 0) return rejected ? 'rejected' : 'done'
    // Photos first: the server only accepts a command whose blob refs it already has.
    const refs = rows.flatMap((r) => commandBlobRefs(r.type, r.payload))
    if (refs.length > 0 && (await uploadPendingBlobs(refs, database)) === 'offline')
      return 'offline'
    const out = await send(household, rows)
    if (out === 'offline') return bump(database, rows, 'offline')
    if (out === 'bad_request' || out === 'server_error') {
      if (rows.length > 1) {
        size = 1 // find the culprit by sending one at a time
        continue
      }
      const [row] = rows as [OutboxRow]
      if (out === 'server_error' && row.attempts + 1 < MAX_ATTEMPTS)
        return bump(database, rows, 'offline')
      await database.outbox.update(row.seq as number, {
        status: 'rejected',
        error: refusal(
          out === 'bad_request'
            ? 'The server could not read this change.'
            : 'The server failed to process this change.',
        ),
      })
      rejected = true
      size = BATCH
      continue
    }
    size = BATCH
    const byId = new Map(out.results.map((r) => [r.id, r]))
    await database.transaction('rw', database.outbox, async () => {
      for (const row of rows) {
        const r = byId.get(row.id)
        if (!r) continue
        if (r.status === 'rejected') {
          rejected = true
          await database.outbox.update(row.seq as number, {
            status: 'rejected',
            error: JSON.stringify(r.problem),
          })
        } else {
          await database.outbox.delete(row.seq as number)
        }
      }
    })
    if (rows.some((r) => !byId.has(r.id))) return 'offline' // short answer: try again later
  }
}

async function bump(
  database: StockpotDB,
  rows: OutboxRow[],
  result: FlushResult,
): Promise<FlushResult> {
  await database.outbox
    .where('seq')
    .anyOf(rows.map((r) => r.seq as number))
    .modify((r) => {
      r.attempts += 1
    })
  return result
}

type SyncPage = {
  rows: Record<string, Record<string, unknown>[]>
  tombstones: Record<string, string[]>
  next: number
  nextGlobal: number
  more: boolean
}

/** Pulls until caught up. Returns false when offline. */
export async function pull(database: StockpotDB = db): Promise<boolean> {
  for (;;) {
    const since = await getMeta(database, REV, 0)
    const sinceGlobal = await getMeta(database, REV_GLOBAL, 0)
    let page: SyncPage
    try {
      const res = await api.GET('/v1/sync', {
        params: { query: { since, sinceGlobal, limit: PAGE } },
      })
      if (!res.response.ok || !res.data) return false
      page = res.data as unknown as SyncPage
      if (typeof page.rows !== 'object' || typeof page.tombstones !== 'object') return false
    } catch {
      return false
    }
    await applyPage(database, page)
    if (!page.more) {
      if (await getMeta(database, REBUILD, false)) await purgeGhosts(database)
      return true
    }
  }
}

async function applyPage(database: StockpotDB, page: SyncPage) {
  await database.transaction('rw', database.tables, async () => {
    for (const t of replicaTables) {
      const rows = page.rows[t]
      if (rows?.length) await database.table(t).bulkPut(rows.map((r) => camelizeRow(r)))
      const dead = page.tombstones[t]
      if (dead?.length) await database.table(t).bulkDelete(dead)
    }
    await setMeta(database, REV, page.next)
    await setMeta(database, REV_GLOBAL, page.nextGlobal)
    // Rows this page wrote or deleted lost their optimistic state: replay what's queued for them.
    const touched = new Set<string>()
    for (const rows of Object.values(page.rows)) for (const r of rows) touched.add(String(r.id))
    for (const ids of Object.values(page.tombstones)) for (const id of ids) touched.add(id)
    await replayPending(database, await getMeta<string | null>(database, HOUSEHOLD, null), touched)
  })
}

/**
 * Schedules a full re-pull: cursors back to 0, so every server row overwrites its local copy
 * (undoing a rejected edit or delete). Optimistic rows the server never had carry rev 0 and are
 * purged once the re-pull completes (see `purgeGhosts`). The tables stay readable meanwhile.
 */
async function requestFullPull(database: StockpotDB) {
  await database.transaction('rw', database.meta, async () => {
    await setMeta(database, REV, 0)
    await setMeta(database, REV_GLOBAL, 0)
    await setMeta(database, REBUILD, true)
  })
}

async function purgeGhosts(database: StockpotDB) {
  await database.transaction('rw', database.tables, async () => {
    const purged = new Set<string>()
    for (const t of replicaTables) {
      const ghosts = await database
        .table(t)
        .filter((r) => r.rev === 0)
        .primaryKeys()
      for (const id of ghosts) purged.add(String(id))
      await database.table(t).bulkDelete(ghosts)
    }
    await database.meta.delete(REBUILD)
    // Rows the re-pull rewrote were already replayed; bring back what only exists optimistically.
    await replayPending(database, await getMeta<string | null>(database, HOUSEHOLD, null), purged)
  })
}

let inflight: Promise<void> | null = null
let again = false

/**
 * Flush the outbox, then pull. One run at a time; a request during a run schedules one more.
 * After a rejection the replica is rebuilt from the server so the optimistic change disappears.
 */
export function syncNow(database: StockpotDB = db): Promise<void> {
  if (inflight) {
    again = true
    return inflight
  }
  inflight = (async () => {
    try {
      do {
        again = false
        const flushed = await flushOutbox(database)
        if (flushed === 'rejected') await requestFullPull(database)
        if (flushed !== 'offline') await pull(database)
      } while (again)
    } finally {
      inflight = null
    }
  })()
  return inflight
}

/** Discard a rejected command (its optimistic effect is dropped by a full re-pull). */
export async function discardRejected(seq: number, database: StockpotDB = db) {
  await database.outbox.delete(seq)
  await requestFullPull(database)
  void syncNow(database)
}

/** Queue a rejected command again under a new id. */
export async function retryRejected(seq: number, database: StockpotDB = db) {
  await database.transaction('rw', database.tables, async () => {
    const row = await database.outbox.get(seq)
    if (!row) return
    await database.outbox.delete(seq)
    await database.outbox.add({
      ...row,
      seq: undefined,
      id: v7(),
      status: 'pending',
      attempts: 0,
      error: undefined,
    })
    await applyReducer(database, row.type, row.payload, row.clientTime)
  })
  void syncNow(database)
}

const POLL_MS = 60_000

/**
 * Starts syncing: now, on SSE nudges, on focus/online, and every 60 s while visible.
 * Returns a stop function.
 */
export function startSyncEngine(database: StockpotDB = db): () => void {
  let stopped = false
  const run = () => {
    if (!stopped) void syncNow(database)
  }
  let timer: ReturnType<typeof setTimeout> | undefined
  syncHooks.requested = () => {
    clearTimeout(timer)
    timer = setTimeout(run, 0)
  }
  const onVisible = () => document.visibilityState === 'visible' && run()
  window.addEventListener('online', run)
  window.addEventListener('focus', run)
  document.addEventListener('visibilitychange', onVisible)
  const poll = setInterval(() => document.visibilityState === 'visible' && run(), POLL_MS)

  let events: EventSource | undefined
  if (typeof EventSource !== 'undefined') {
    events = new EventSource('/api/v1/sync/events')
    events.addEventListener('changed', run)
    events.onopen = run // pull what happened while the stream was down
  }
  run()

  return () => {
    stopped = true
    clearTimeout(timer)
    clearInterval(poll)
    events?.close()
    window.removeEventListener('online', run)
    window.removeEventListener('focus', run)
    document.removeEventListener('visibilitychange', onVisible)
    syncHooks.requested = () => {}
  }
}
