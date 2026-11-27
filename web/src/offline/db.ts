import { Dexie, type EntityTable } from 'dexie'
import type { ReplicaTables } from './replica'

/**
 * Local replica + outbox (architecture §10, architecture-m1 §2). Never edit an existing
 * version block: add a new one with an upgrade function instead.
 */

/** Key/value bookkeeping: `sync.rev`, `sync.revGlobal` (pull cursors), `sync.household`. */
export type MetaRow = { key: string; value: unknown }

/**
 * A photo prepared on this device (resized, re-encoded, hashed) and not uploaded yet. `ref` is its
 * SHA-256, the same ref the server computes, so commands can reference it before the upload
 * (architecture-m2 §6). Local only, never synced.
 */
export type PendingBlobRow = {
  ref: string
  blob: Blob
  createdAt: string
  /** Failed upload tries; see MAX_UPLOAD_ATTEMPTS in src/lib/photos.ts. */
  attempts?: number
}

export type OutboxStatus = 'pending' | 'sending' | 'rejected'

/** A queued write command, sent to `POST /api/v1/commands` in order of `seq`. */
export type OutboxRow = {
  /** uuidv7; the server stores it to apply each command exactly once. */
  id: string
  /** Auto-incremented send order. */
  seq?: number
  /** Household the user was in when acting; commands are only ever sent to this one. */
  householdId?: string
  type: string
  payload: unknown
  /** ISO 8601 time the user acted. */
  clientTime: string
  status: OutboxStatus
  attempts: number
  /** Problem from the server when status is `rejected`: `{code, detail}` as JSON. */
  error?: string
}

type Replica = { [K in keyof ReplicaTables]: EntityTable<ReplicaTables[K], 'id'> }

export class StockpotDB extends Dexie {
  meta!: EntityTable<MetaRow, 'key'>
  outbox!: EntityTable<OutboxRow, 'seq'>
  category!: Replica['category']
  ingredient!: Replica['ingredient']
  store!: Replica['store']
  location!: Replica['location']
  product!: Replica['product']
  product_barcode!: Replica['product_barcode']
  product_stat!: Replica['product_stat']
  stock_item!: Replica['stock_item']
  stock_event!: Replica['stock_event']
  shopping_list!: Replica['shopping_list']
  shopping_item!: Replica['shopping_item']
  recipe!: Replica['recipe']
  recipe_version!: Replica['recipe_version']
  recipe_photo!: Replica['recipe_photo']
  cook_session!: Replica['cook_session']
  recipe_import!: Replica['recipe_import']
  substitution!: Replica['substitution']
  pending_blob!: EntityTable<PendingBlobRow, 'ref'>

  constructor(name = 'stockpot') {
    super(name)
    this.version(1).stores({
      meta: '&key',
      outbox: '++seq, &id, status',
    })
    // v2: one table per synced server table, same names, primary key `id`.
    this.version(2).stores({
      category: 'id, slug',
      ingredient: 'id, slug, parentId, categoryId',
      store: 'id, sort',
      location: 'id, kind, sort',
      product: 'id, ingredientId, categoryId, defaultStoreId, enrichState',
      product_barcode: 'id, productId, code',
      product_stat: 'id, productId, storeId',
      stock_item: 'id, productId, locationId, expiresOn',
      stock_event: 'id, stockItemId',
      shopping_list: 'id, sort',
      shopping_item: 'id, listId, productId, checkedAt',
    })
    // v3 (M2): recipe tables, and the local-only pending_blob outbox for photos. Existing tables
    // gained columns (conversions, recipeId, cookSessionId) whose rows weren't re-revved, so the
    // pull cursors restart and the next pull refills everything.
    this.version(3)
      .stores({
        recipe: 'id, saved, lastCookedAt, importId',
        recipe_version: 'id, recipeId',
        recipe_photo: 'id, recipeId, cookSessionId',
        cook_session: 'id, recipeId, cookedOn',
        recipe_import: 'id, batchId, status',
        substitution: 'id, ingredientId, recipeId',
        pending_blob: 'ref',
      })
      .upgrade((tx) => tx.table('meta').bulkDelete(['sync.rev', 'sync.revGlobal']))
  }
}

export const db = new StockpotDB()
