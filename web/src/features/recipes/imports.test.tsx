import 'fake-indexeddb/auto'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import { dispatch, pendingCommands } from '../../offline/outbox'
import type { RecipeImport } from '../../offline/replica'
import { renderApp } from '../../test-utils'
import { importLabel, importLinks, parseLinks } from './imports'
import { sharedInput } from './slots/ImportTab'

beforeEach(async () => {
  vi.stubGlobal('fetch', async () => {
    throw new TypeError('offline') // the outbox keeps commands
  })
  await db.meta.put({ key: 'sync.household', value: '0190a1b2-0000-7000-8000-0000000000a1' })
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await Promise.all(db.tables.map((t) => t.clear()))
})

describe('parseLinks', () => {
  test('one per line, inside shared text, deduped, trailing punctuation dropped', () => {
    const { links, skipped } = parseLinks(
      'https://a.example/r/1\n\nLook at this: https://www.instagram.com/reel/C9x/?igsh=1).\nnot a link\nhttps://a.example/r/1#top',
    )
    expect(links).toEqual(['https://a.example/r/1', 'https://www.instagram.com/reel/C9x/?igsh=1'])
    expect(skipped).toBe(1)
  })
  test('caps a batch at 50', () => {
    const text = Array.from({ length: 60 }, (_, i) => `https://a.example/${i}`).join('\n')
    expect(parseLinks(text).links).toHaveLength(50)
  })
})

test('sharedInput picks links or text from the share target', () => {
  expect(sharedInput('?url=https%3A%2F%2Fa.example%2Fr')).toEqual({
    mode: 'links',
    value: 'https://a.example/r',
  })
  expect(sharedInput('?title=Dal&text=1+cup+lentils')).toEqual({
    mode: 'text',
    value: 'Dal\n1 cup lentils',
  })
  expect(sharedInput('')).toBeNull()
})

test('a bulk import queues one row per link with a shared batch and Instagram detected', async () => {
  const ids = await importLinks(['https://a.example/r/1', 'https://instagram.com/reel/C9x/'])
  const rows = await db.recipe_import.bulkGet(ids)
  expect(rows.map((r) => r?.kind)).toEqual(['url', 'instagram'])
  expect(rows[0]?.batchId).toBeTruthy()
  expect(rows[0]?.batchId).toBe(rows[1]?.batchId)
  expect(rows.every((r) => r?.status === 'queued')).toBe(true)
  const cmds = (await pendingCommands()).filter((c) => c.type === 'recipe.import.create')
  expect(cmds).toHaveLength(2)
})

test('retry requeues a failed import; cancel removes it', async () => {
  const [id] = await importLinks(['https://a.example/r/1'])
  if (!id) throw new Error('no id')
  const row = (await db.recipe_import.get(id)) as RecipeImport
  await db.recipe_import.put({ ...row, status: 'failed', error: { code: 'http_500' } })
  await dispatch('recipe.import.retry', { id })
  expect((await db.recipe_import.get(id))?.status).toBe('queued')
  await dispatch('recipe.import.cancel', { id })
  expect(await db.recipe_import.get(id)).toBeUndefined()
})

test('importLabel names a site link by host and slug', () => {
  const row = {
    kind: 'url',
    input: 'https://www.bbcgoodfood.com/recipes/easy-chicken-curry',
    blobRefs: [],
  }
  expect(importLabel(row as unknown as RecipeImport)).toBe('bbcgoodfood.com · easy chicken curry')
})

test('the Import tab queues pasted links and lists them; needs input asks for text', async () => {
  renderApp('/recipes/import')
  const box = await screen.findByRole('textbox', { name: 'Paste links, one per line' })
  await userEvent.type(box, 'https://a.example/r/1{Enter}https://b.example/r/2')
  await userEvent.click(screen.getByRole('button', { name: 'Import 2 recipes' }))
  await waitFor(() => expect(screen.getAllByText('Waiting to start…')).toHaveLength(2))
  expect(box).toHaveValue('')

  const row = (await db.recipe_import.toArray())[0] as RecipeImport
  await db.recipe_import.put({
    ...row,
    kind: 'instagram',
    status: 'needs_input',
    error: { code: 'reel_blocked', detail: 'Instagram blocked reading this reel.' },
  })
  await userEvent.click(await screen.findByRole('button', { name: 'Paste text' }))
  await userEvent.type(
    await screen.findByRole('textbox', { name: 'Recipe text' }),
    'Kuku sabzi: 6 eggs, 2 cups herbs. Fry both sides.',
  )
  await userEvent.click(screen.getByRole('button', { name: 'Continue import' }))
  await waitFor(async () =>
    expect((await pendingCommands()).some((c) => c.type === 'recipe.import.provide')).toBe(true),
  )
  expect((await db.recipe_import.get(row.id))?.status).toBe('queued')
})
