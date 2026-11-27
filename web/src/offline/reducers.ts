import type { CommandPayloads, CommandType } from './commands'
import type { StockpotDB } from './db'

/**
 * A client reducer applies one command to the replica immediately (optimistic apply).
 * It runs inside the Dexie transaction that enqueues the command, and again after every
 * pull for commands still in the outbox, so it must set fields rather than add to them.
 * The next pull overwrites the result with server truth.
 */
export type Reducer<T extends CommandType> = (
  db: StockpotDB,
  payload: CommandPayloads[T],
  ctx: { clientTime: string },
) => Promise<void> | void

type AnyReducer = (
  db: StockpotDB,
  payload: never,
  ctx: { clientTime: string },
) => Promise<void> | void

const reducers = new Map<string, AnyReducer>()

/** Register the reducer for a command type (from the feature's `commands.ts`). */
export function registerReducer<T extends CommandType>(type: T, fn: Reducer<T>) {
  reducers.set(type, fn as AnyReducer)
}

export async function applyReducer(
  db: StockpotDB,
  type: string,
  payload: unknown,
  clientTime: string,
): Promise<void> {
  await reducers.get(type)?.(db, payload as never, { clientTime })
}
