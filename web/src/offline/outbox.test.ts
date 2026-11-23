import 'fake-indexeddb/auto'
import { afterEach, expect, test } from 'vitest'
import { StockpotDB } from './db'
import { enqueueCommand, pendingCommands } from './outbox'

declare module './commands' {
  interface CommandPayloads {
    'test.ping': { n: number }
  }
}

const db = new StockpotDB('stockpot-test')

afterEach(async () => {
  await db.delete()
  await db.open()
})

test('schema v1 has only meta and outbox', () => {
  expect(db.verno).toBe(1)
  expect(db.tables.map((t) => t.name).sort()).toEqual(['meta', 'outbox'])
})

test('enqueueCommand stores pending commands in order with uuidv7 ids', async () => {
  const first = await enqueueCommand('test.ping', { n: 1 }, db)
  const second = await enqueueCommand('test.ping', { n: 2 }, db)
  expect(first).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-/)
  const rows = await pendingCommands(db)
  expect(rows.map((r) => r.id)).toEqual([first, second])
  expect(rows[0]).toMatchObject({
    type: 'test.ping',
    payload: { n: 1 },
    status: 'pending',
    attempts: 0,
  })
})
