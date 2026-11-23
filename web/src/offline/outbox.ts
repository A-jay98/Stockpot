import { v7 as uuidv7 } from 'uuid'
import type { CommandPayloads, CommandType } from './commands'
import { db, type OutboxRow, type StockpotDB } from './db'

/**
 * Queue a write command for the server. It is stored durably first, so it survives
 * reloads and offline periods; the sync engine (M1) flushes the outbox in `seq` order.
 * Returns the command id (uuidv7), which also makes retries idempotent server-side.
 */
export async function enqueueCommand<T extends CommandType>(
  type: T,
  payload: CommandPayloads[T],
  database: StockpotDB = db,
): Promise<string> {
  const row: OutboxRow = {
    id: uuidv7(),
    type,
    payload,
    clientTime: new Date().toISOString(),
    status: 'pending',
    attempts: 0,
  }
  await database.outbox.add(row)
  return row.id
}

/** Pending commands in send order. */
export function pendingCommands(database: StockpotDB = db): Promise<OutboxRow[]> {
  return database.outbox.where('status').equals('pending').sortBy('seq')
}
