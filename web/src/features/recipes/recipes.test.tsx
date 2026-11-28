import 'fake-indexeddb/auto'
import { screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import { dispatch } from '../../offline/outbox'
import { renderApp } from '../../test-utils'
import { rawLines } from './commands'
import { amountOf, groupLines } from './RecipePage'

beforeEach(async () => {
  vi.stubGlobal('fetch', async () => {
    throw new TypeError('offline')
  })
  await db.meta.put({ key: 'sync.household', value: '0190a1b2-0000-7000-8000-0000000000a1' })
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await Promise.all(db.tables.map((t) => t.clear()))
})

const R = 'recipe-1'

test('raw lines group under headers until the server parses them', () => {
  const ls = rawLines(['2 cups flour', '', 'For the rice:', '1 cup rice'])
  expect(ls.map((l) => [l.group, l.raw])).toEqual([
    [undefined, '2 cups flour'],
    ['For the rice', '1 cup rice'],
  ])
  expect(groupLines(ls).map((g) => g.group)).toEqual(['', 'For the rice'])
  const line = { line_id: 'l', raw: 'x', name: 'x', optional: false, staple: false }
  expect(amountOf({ ...line, qty: '2', qty_max: '3', unit: 'clove' })).toBe('2–3 clove')
  expect(amountOf({ ...line, qty: '2', unit: 'ea' })).toBe('2')
})

test('create, new version, restore and match pick apply optimistically', async () => {
  await dispatch('recipe.create', {
    id: R,
    versionId: 'v1',
    title: ' Curry ',
    servings: 4,
    ingredients: ['2 tbsp tomato paste', '1 onion'],
    steps: [{ text: 'Cook.' }, { text: ' ' }],
    tags: ['Dinner', 'dinner'],
  })
  let r = await db.recipe.get(R)
  expect(r).toMatchObject({ title: 'Curry', currentVersionId: 'v1', servings: 4, tags: ['dinner'] })
  const v1 = await db.recipe_version.get('v1')
  expect(v1).toMatchObject({ n: 1, origin: 'manual', steps: [{ text: 'Cook.' }] })

  await dispatch('recipe.version.create', {
    recipeId: R,
    versionId: 'v2',
    origin: 'manual',
    title: 'Better curry',
    ingredients: ['3 tbsp tomato paste'],
    steps: [],
  })
  r = await db.recipe.get(R)
  expect(r).toMatchObject({ title: 'Better curry', currentVersionId: 'v2' })
  expect(await db.recipe_version.get('v2')).toMatchObject({ n: 2, parentVersionId: 'v1' })

  await dispatch('recipe.version.restore', { recipeId: R, versionId: 'v3', fromVersionId: 'v1' })
  expect(await db.recipe_version.get('v3')).toMatchObject({
    n: 3,
    origin: 'restore',
    title: 'Curry',
    changeSummary: 'Restored v1',
  })
  expect(await db.recipe_version.count()).toBe(3)

  const line = (await db.recipe_version.get('v3'))?.ingredients[0]
  await dispatch('recipe.line.set_ingredient', {
    versionId: 'v3',
    lineId: line?.line_id ?? '',
    ingredientId: 'ing-1',
  })
  expect((await db.recipe_version.get('v3'))?.ingredients[0]).toMatchObject({
    ingredient_id: 'ing-1',
    match_confidence: 1,
  })

  await dispatch('recipe.delete', { id: R })
  expect(await db.recipe.count()).toBe(0)
  expect(await db.recipe_version.count()).toBe(0)
})

test('recipe page renders the current version from the replica', async () => {
  await dispatch('recipe.create', {
    id: R,
    versionId: 'v1',
    title: 'Chicken curry',
    ingredients: ['For the sauce:', '1 (400 mL) can coconut milk'],
    steps: [{ text: 'Simmer for 20 minutes.', minutes: 20 }],
    notes: 'Better the next day.',
  })
  renderApp(`/recipes/${R}`)
  expect(await screen.findByRole('heading', { name: 'Chicken curry' })).toBeInTheDocument()
  expect(await screen.findByText('For the sauce')).toBeInTheDocument()
  expect(screen.getByText('1 (400 mL) can coconut milk')).toBeInTheDocument()
  expect(screen.getByText(/Simmer for 20 minutes/)).toBeInTheDocument()
  expect(screen.getByText('Better the next day.')).toBeInTheDocument()
})

test('photo add and remove apply optimistically and declare their photo for upload', async () => {
  const { commandBlobRefs } = await import('../../offline/reducers')
  await dispatch('recipe.create', {
    id: R,
    versionId: 'v1',
    title: 'Curry',
    ingredients: [],
    steps: [],
  })
  const ref = 'a'.repeat(64)
  const add = { id: 'p1', recipeId: R, blobRef: ref, caption: '  Sunday ', takenOn: '2026-11-27' }
  await dispatch('recipe.photo.add', add)
  expect(await db.recipe_photo.get('p1')).toMatchObject({
    recipeId: R,
    blobRef: ref,
    caption: 'Sunday',
    cookSessionId: null,
    rev: 0,
  })
  expect(commandBlobRefs('recipe.photo.add', add)).toEqual([ref])
  expect(commandBlobRefs('recipe.update', { id: R, coverBlob: ref })).toEqual([ref])
  expect(commandBlobRefs('recipe.update', { id: R, coverBlob: null })).toEqual([])
  await dispatch('recipe.photo.remove', { id: 'p1' })
  expect(await db.recipe_photo.get('p1')).toBeUndefined()
})
