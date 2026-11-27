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

type Options<T extends CommandType> = {
  /**
   * Ids of the rows the command touches. After a pull overwrites some rows, only commands whose
   * targets were overwritten are replayed, so reducers that add deltas (consume 1) apply exactly
   * once on top of server state. Omit it only for reducers that set fields (replayed after every
   * pull, harmlessly).
   */
  targets?: (payload: CommandPayloads[T]) => string[]
}

const reducers = new Map<string, AnyReducer>()
const targetFns = new Map<string, (payload: never) => string[]>()

/** Register the reducer for a command type (from the feature's `commands.ts`). */
export function registerReducer<T extends CommandType>(
  type: T,
  fn: Reducer<T>,
  options: Options<T> = {},
) {
  reducers.set(type, fn as AnyReducer)
  if (options.targets) targetFns.set(type, options.targets as (payload: never) => string[])
  else targetFns.delete(type)
}

/** Row ids a queued command touches, or null when its reducer didn't declare any. */
export function reducerTargets(type: string, payload: unknown): string[] | null {
  return targetFns.get(type)?.(payload as never) ?? null
}

export async function applyReducer(
  db: StockpotDB,
  type: string,
  payload: unknown,
  clientTime: string,
): Promise<void> {
  await reducers.get(type)?.(db, payload as never, { clientTime })
}
