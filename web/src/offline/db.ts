import { Dexie, type EntityTable } from 'dexie'

/**
 * Local replica + outbox (architecture §10). M0 ships only the `meta` and `outbox`
 * tables; household domain tables arrive in M1 as a new `version(n)`. Never edit an
 * existing version block: add a new one with an upgrade function instead.
 */

/** Key/value bookkeeping, e.g. the sync cursor (`sync.rev`) or the household id. */
export type MetaRow = { key: string; value: unknown }

export type OutboxStatus = 'pending' | 'sending' | 'rejected'

/** A queued write command, sent to `POST /api/v1/commands` in order of `seq`. */
export type OutboxRow = {
  /** uuidv7; the server stores it to apply each command exactly once. */
  id: string
  /** Auto-incremented send order. */
  seq?: number
  type: string
  payload: unknown
  /** ISO 8601 time the user acted. */
  clientTime: string
  status: OutboxStatus
  attempts: number
  /** Problem detail from the server when status is `rejected`. */
  error?: string
}

export class StockpotDB extends Dexie {
  meta!: EntityTable<MetaRow, 'key'>
  outbox!: EntityTable<OutboxRow, 'seq'>

  constructor(name = 'stockpot') {
    super(name)
    this.version(1).stores({
      meta: '&key',
      outbox: '++seq, &id, status',
    })
  }
}

export const db = new StockpotDB()
