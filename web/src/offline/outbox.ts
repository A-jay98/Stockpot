import { v7 as uuidv7 } from 'uuid'
import type { CommandPayloads, CommandType } from './commands'
import { db, type OutboxRow, type StockpotDB } from './db'
import { applyReducer } from './reducers'

function newRow<T extends CommandType>(type: T, payload: CommandPayloads[T]): OutboxRow {
  return {
    id: uuidv7(),
    type,
    payload,
    clientTime: new Date().toISOString(),
    status: 'pending',
    attempts: 0,
  }
}

/**
 * Queue a write command for the server. It is stored durably first, so it survives
 * reloads and offline periods; the sync engine flushes the outbox in `seq` order.
 * Returns the command id (uuidv7), which also makes retries idempotent server-side.
 */
export async function enqueueCommand<T extends CommandType>(
  type: T,
  payload: CommandPayloads[T],
  database: StockpotDB = db,
): Promise<string> {
  const row = newRow(type, payload)
  await database.outbox.add(row)
  return row.id
}

/**
 * The app's write path: queue the command and apply its reducer in one transaction, so the UI
 * (which reads only the replica) updates at once, then nudge the sync engine.
 */
export async function dispatch<T extends CommandType>(
  type: T,
  payload: CommandPayloads[T],
  database: StockpotDB = db,
): Promise<string> {
  const row = newRow(type, payload)
  await database.transaction('rw', database.tables, async () => {
    await database.outbox.add(row)
    await applyReducer(database, type, payload, row.clientTime)
  })
  syncHooks.requested()
  return row.id
}

/** Set by the sync engine; a no-op until it runs (and in unit tests). */
export const syncHooks = { requested: () => {} }

/** Pending commands in send order. */
export function pendingCommands(database: StockpotDB = db): Promise<OutboxRow[]> {
  return database.outbox.where('status').equals('pending').sortBy('seq')
}
