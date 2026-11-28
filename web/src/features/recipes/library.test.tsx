import 'fake-indexeddb/auto'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import { pendingCommands } from '../../offline/outbox'
import type { Ingredient, Recipe, RecipeLine, RecipeVersion } from '../../offline/replica'
import { renderApp } from '../../test-utils'
import { isHeader } from './commands'
import { diffLines } from './diff'
import { rankIngredients } from './IngredientPicker'
import { kitchenQty } from './parts'
import { contentChanged, draftOf, validate } from './RecipeEditor'
import { cookedText, filterSaved, formatMinutes } from './slots/SavedTab'
import { versionDiff } from './VersionPage'

const HH = '0190a1b2-0000-7000-8000-0000000000a1'
const R = '0190a1b2-0000-7000-8000-00000000f001'
const V1 = '0190a1b2-0000-7000-8000-0000000000b1'
const V2 = '0190a1b2-0000-7000-8000-0000000000b2'
const T = '2026-11-20T10:00:00Z'

beforeEach(async () => {
  vi.stubGlobal('fetch', async () => {
    throw new TypeError('offline')
  })
  await db.meta.put({ key: 'sync.household', value: HH })
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await Promise.all(db.tables.map((t) => t.clear()))
})

const base = (id: string, rev = 5) => ({
  id,
  householdId: HH,
  createdAt: T,
  updatedAt: T,
  rev,
})

const recipe = (over: Partial<Recipe> = {}): Recipe => ({
  ...base(R),
  title: 'Khoresh gheymeh',
  subtitle: 'Split pea and beef stew',
  sourceKind: 'instagram',
  sourceUrl: 'https://www.instagram.com/reel/abc',
  sourceLang: 'fa',
  currentVersionId: V2,
  servings: 6,
  totalMinutes: 120,
  coverBlob: null,
  tags: ['dinner'],
  saved: true,
  ideaExpiresAt: null,
  cookedCount: 3,
  lastCookedAt: '2026-11-09T18:00:00Z',
  importId: null,
  ...over,
})

const line = (id: string, raw: string, over: Partial<RecipeLine> = {}): RecipeLine => ({
  line_id: `0190a1b2-0000-7000-8000-0000000001${id}`,
  raw,
  name: raw,
  optional: false,
  staple: false,
  ...over,
})

const version = (id: string, n: number, over: Partial<RecipeVersion> = {}): RecipeVersion => ({
  ...base(id),
  recipeId: R,
  n,
  parentVersionId: null,
  origin: 'import',
  changeSummary: null,
  title: 'Khoresh gheymeh',
  servings: 6,
  totalMinutes: 120,
  ingredients: [],
  steps: [{ text: 'Fry the onions.' }],
  notes: null,
  original: null,
  originalVersionId: null,
  createdBy: null,
  ...over,
})

const ingredient = (id: string, name: string, over: Partial<Ingredient> = {}): Ingredient => ({
  ...base(id),
  householdId: null,
  slug: name.toLowerCase().replace(/\s+/g, '-'),
  name,
  plural: null,
  parentId: null,
  categoryId: null,
  icon: null,
  aliases: [],
  shelfLife: {},
  defaultUnit: null,
  source: 'seed',
  ...over,
})

const ONION = '0190a1b2-0000-7000-8000-0000000000c1'
const PEAS = '0190a1b2-0000-7000-8000-0000000000c2'

async function seed() {
  await db.ingredient.bulkPut([
    ingredient(ONION, 'Onion', { plural: 'onions' }),
    ingredient(PEAS, 'Yellow split peas', { aliases: ['lappeh'] }),
  ])
  await db.recipe.put(recipe())
  await db.recipe_version.bulkPut([
    version(V1, 1, {
      ingredients: [line('01', '2 onions', { qty: '2', unit: 'ea', name: 'onions' })],
      steps: [{ text: 'Fry the onions.' }, { text: 'Add the lamb.' }],
      original: {
        lang: 'fa',
        title: 'خورش قیمه',
        ingredients: ['۲ عدد پیاز'],
        steps: ['پیاز را سرخ کنید.'],
      },
    }),
    version(V2, 2, {
      origin: 'chat',
      changeSummary: 'Lamb swapped for lean beef',
      originalVersionId: V1,
      ingredients: [
        line('01', '2 onions', {
          qty: '2',
          unit: 'ea',
          name: 'onions',
          ingredient_id: ONION,
          match_confidence: 1,
        }),
        line('02', '1 cup yellow split peas (lappeh)', {
          qty: '1',
          unit: 'cup',
          name: 'yellow split peas',
          note: 'lappeh',
          ingredient_id: PEAS,
          match_confidence: 0.6,
        }),
      ],
      steps: [{ text: 'Fry the onions.' }, { text: 'Add the beef.' }],
    }),
  ])
}

test('saved list search, sort and cooked text', () => {
  const rs = [
    recipe({ id: 'a', title: 'Chicken curry', cookedCount: 1, lastCookedAt: null, tags: [] }),
    recipe({ id: 'b', title: 'Beef chili', cookedCount: 5, tags: ['freezer'] }),
    recipe({
      id: 'c',
      title: 'Overnight oats',
      cookedCount: 8,
      lastCookedAt: '2026-11-25T08:00:00Z',
    }),
  ]
  expect(filterSaved(rs, '', 'az').map((r) => r.id)).toEqual(['b', 'a', 'c'])
  expect(filterSaved(rs, '', 'cooked').map((r) => r.id)).toEqual(['c', 'b', 'a'])
  expect(filterSaved(rs, '', 'recent').map((r) => r.id)).toEqual(['c', 'b', 'a'])
  expect(filterSaved(rs, 'curyy', 'az').map((r) => r.id)).toEqual(['a']) // typo
  expect(filterSaved(rs, 'freez', 'az').map((r) => r.id)).toEqual(['b']) // tag
  expect(filterSaved(rs, 'pizza', 'az')).toEqual([])
  expect([0, 1, 6].map(cookedText)).toEqual(['Not cooked yet', 'Cooked once', 'Cooked 6 times'])
  expect([45, 120, 135].map(formatMinutes)).toEqual(['45 min', '2 h', '2 h 15 min'])
})

test('quantities read like a recipe', () => {
  expect(['0.5', '1.5', '0.25', '2', '0.333', '1.2', 'x'].map(kitchenQty)).toEqual([
    '½',
    '1½',
    '¼',
    '2',
    '⅓',
    '1.2',
    'x',
  ])
})

test('line diff keeps order and marks changes', () => {
  expect(diffLines(['a', 'b', 'c'], ['a', 'x', 'c', 'd'])).toEqual([
    { kind: 'same', text: 'a' },
    { kind: 'removed', text: 'b' },
    { kind: 'added', text: 'x' },
    { kind: 'same', text: 'c' },
    { kind: 'added', text: 'd' },
  ])
  const d = versionDiff(
    version(V1, 1, { ingredients: [line('01', '1 onion', { group: 'Sauce' })] }),
    version(V2, 2, { servings: 4, ingredients: [line('01', '1 onion', { group: 'Sauce' })] }),
  )
  expect(d.ingredients.every((r) => r.kind === 'same')).toBe(true)
  expect(d.ingredients[0]?.text).toBe('Sauce:')
  expect(d.details.filter((r) => r.kind !== 'same').map((r) => r.text)).toEqual([
    'Portions: 6',
    'Portions: 4',
  ])
})

test('ingredient ranking prefers close names and knows aliases', () => {
  const all = [
    ingredient('1', 'Garlic'),
    ingredient('2', 'Garlic powder'),
    ingredient('3', 'Yellow split peas', { aliases: ['lappeh'] }),
  ]
  expect(rankIngredients(all, 'garlic').map((i) => i.id)).toEqual(['1', '2'])
  expect(rankIngredients(all, 'lappeh').map((i) => i.id)).toEqual(['3'])
  expect(rankIngredients(all, '')).toEqual([])
})

test('editor draft round-trips a version and validates', () => {
  const v = version(V1, 1, {
    ingredients: [line('01', '1 cup rice', { group: 'For the rice' })],
    steps: [{ text: 'Rinse.', minutes: null }],
  })
  const d = draftOf(recipe(), v)
  expect(d.ingredients).toBe('For the rice:\n1 cup rice')
  expect(contentChanged(d, v)).toBe(false)
  expect(contentChanged({ ...d, notes: 'Soak first.' }, v)).toBe(true)
  expect(validate({ ...d, title: ' ', servings: '0' })).toEqual({
    title: 'Give the recipe a name.',
    servings: 'A whole number, 1 to 100.',
  })
})

test('recipe page: meta, chips, check match only on parsed versions, picker sets the match', async () => {
  await seed()
  renderApp(`/recipes/${R}`)
  expect(await screen.findByRole('heading', { name: 'Khoresh gheymeh' })).toBeInTheDocument()
  expect(
    await screen.findByText('Split pea and beef stew · 2 h · 6 portions · version 2'),
  ).toBeInTheDocument()
  expect(screen.getByText('From a reel')).toBeInTheDocument()

  // Only the low-confidence line asks; the confirmed onion doesn't.
  const checks = await screen.findAllByRole('button', { name: /^Check match for/ })
  expect(checks).toHaveLength(1)
  fireEvent.click(checks[0] as HTMLElement)
  const sheet = await screen.findByRole('dialog', { name: 'Which ingredient is this?' })
  fireEvent.click(await within(sheet).findByText('Yellow split peas'))
  await waitFor(async () =>
    expect((await db.recipe_version.get(V2))?.ingredients[1]?.match_confidence).toBe(1),
  )
  const cmds = await pendingCommands()
  expect(cmds.at(-1)).toMatchObject({
    type: 'recipe.line.set_ingredient',
    payload: { versionId: V2, ingredientId: PEAS },
  })
  await waitFor(() =>
    expect(screen.queryByRole('button', { name: /^Check match for/ })).not.toBeInTheDocument(),
  )

  // Timeline lists both versions, newest first.
  const timeline = screen.getByRole('region', { name: 'Notes and versions' })
  expect(
    within(timeline)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href')),
  ).toEqual([`/recipes/${R}/v/2`, `/recipes/${R}/v/1`])
})

test('unsynced versions show raw lines and no match chips', async () => {
  await db.recipe.put(recipe({ currentVersionId: V1, sourceKind: 'manual' }))
  await db.recipe_version.put({
    ...version(V1, 1, { ingredients: [line('01', '2 onions')] }),
    rev: 0,
  })
  renderApp(`/recipes/${R}`)
  expect(await screen.findByText('2 onions')).toBeInTheDocument()
  expect(screen.getByText(/Amounts and matches show once this version syncs/)).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /Check match|change ingredient match/ })).toBeNull()
})

test('language toggle shows the original right to left', async () => {
  await seed()
  renderApp(`/recipes/${R}`)
  const toggle = await screen.findByRole('button', { name: 'Show the original' })
  fireEvent.click(toggle)
  const title = await screen.findByText('خورش قیمه')
  expect(title).toHaveAttribute('lang', 'fa')
  expect(title).toHaveAttribute('dir', 'rtl')
  expect(screen.getByText('۲ عدد پیاز').closest('[lang]')).toHaveAttribute('lang', 'fa')
  fireEvent.click(screen.getByRole('button', { name: 'Show the English translation' }))
  expect((await screen.findAllByText(/onions/)).length).toBeGreaterThan(0)
})

test('version page diffs against current and restores as a new version', async () => {
  await seed()
  const { router } = renderApp(`/recipes/${R}/v/1`)
  expect(await screen.findByRole('heading', { name: 'Version 1' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('radio', { name: 'Changes vs current' }))
  await waitFor(() =>
    expect(screen.getByText('Add the lamb.').closest('li')).toHaveAttribute('data-kind', 'removed'),
  )
  const method = screen.getByRole('region', { name: 'Method' })
  expect(within(method).getByText('Add the beef.').closest('li')).toHaveAttribute(
    'data-kind',
    'added',
  )

  fireEvent.click(screen.getByRole('button', { name: 'Restore this version' }))
  await waitFor(() => expect(router.state.location.pathname).toBe(`/recipes/${R}`))
  const versions = await db.recipe_version.where('recipeId').equals(R).toArray()
  expect(versions.map((v) => v.n).sort()).toEqual([1, 2, 3])
  const r = await db.recipe.get(R)
  expect(versions.find((v) => v.id === r?.currentVersionId)).toMatchObject({
    n: 3,
    origin: 'restore',
    steps: [{ text: 'Fry the onions.' }, { text: 'Add the lamb.' }],
  })
})

test('editor creates a recipe offline, then saving unchanged content adds no version', async () => {
  const { router } = renderApp('/recipes/new')
  fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Weeknight dal' } })
  fireEvent.change(screen.getByLabelText('Portions'), { target: { value: '4' } })
  fireEvent.change(screen.getByLabelText('Tags'), { target: { value: 'Dinner, quick' } })
  fireEvent.change(screen.getByLabelText('One per line'), {
    target: { value: '1 cup red lentils\n\nFor the tadka:\n2 tbsp ghee' },
  })
  expect(screen.getAllByText('Read on save')).toHaveLength(2)
  fireEvent.change(screen.getByLabelText('Step 1'), { target: { value: 'Simmer the lentils.' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save recipe' }))

  await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/recipes\/[0-9a-f-]{36}$/))
  const [r] = await db.recipe.toArray()
  expect(r).toMatchObject({ title: 'Weeknight dal', servings: 4, tags: ['dinner', 'quick'] })
  const cmds = await pendingCommands()
  expect(cmds[0]).toMatchObject({
    type: 'recipe.create',
    payload: {
      ingredients: ['1 cup red lentils', 'For the tadka:', '2 tbsp ghee'],
      steps: [{ text: 'Simmer the lentils.' }],
    },
  })
  expect(await screen.findByText('For the tadka')).toBeInTheDocument()

  // Edit without changes: no new version.
  await router.navigate(`/recipes/${r?.id}/edit`)
  fireEvent.click(await screen.findByRole('button', { name: 'Save changes' }))
  await waitFor(() => expect(router.state.location.pathname).toBe(`/recipes/${r?.id}`))
  expect(await db.recipe_version.count()).toBe(1)

  // Edit with a change and a summary: version 2.
  await router.navigate(`/recipes/${r?.id}/edit`)
  fireEvent.change(await screen.findByLabelText('Notes'), { target: { value: 'Add lemon.' } })
  fireEvent.change(screen.getByLabelText('What changed?'), {
    target: { value: 'Lemon at the end' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(async () => expect(await db.recipe_version.count()).toBe(2))
  expect((await pendingCommands()).at(-1)).toMatchObject({
    type: 'recipe.version.create',
    payload: { origin: 'manual', changeSummary: 'Lemon at the end', notes: 'Add lemon.' },
  })
})

test('saved tab lists, searches and links to new recipe', async () => {
  await seed()
  renderApp('/recipes?tab=saved')
  const list = await screen.findByRole('list', { name: 'Saved recipes' })
  expect(within(list).getByText('Khoresh gheymeh')).toBeInTheDocument()
  expect(within(list).getByText('Cooked 3 times')).toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('Search saved recipes'), { target: { value: 'tacos' } })
  expect(await screen.findByText(/No saved recipe matches/)).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'New recipe' })).toHaveAttribute('href', '/recipes/new')
})

test('cover photo shows on the page and can be removed in the editor', async () => {
  const ref = 'a'.repeat(64)
  await seed()
  await db.recipe.update(R, { coverBlob: ref })
  const { router } = renderApp(`/recipes/${R}`)
  const img = await screen.findByRole('img', { name: 'Khoresh gheymeh cover photo' })
  expect(img).toHaveAttribute('src', `/api/v1/blobs/${ref}?w=1024`)

  await router.navigate(`/recipes/${R}/edit`)
  expect(await screen.findByRole('img', { name: 'Cover photo' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Remove cover' }))
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(async () => expect((await db.recipe.get(R))?.coverBlob).toBeNull())
  expect((await pendingCommands()).at(-1)).toMatchObject({
    type: 'recipe.update',
    payload: { id: R, coverBlob: null },
  })
  // Only the changed field: tags/subtitle edited elsewhere must survive (LWW per field).
  expect(Object.keys((await pendingCommands()).at(-1)?.payload ?? {}).sort()).toEqual([
    'coverBlob',
    'id',
  ])
  expect(await db.recipe_version.where('recipeId').equals(R).count()).toBe(2)
})

test('section headers and limits match the server', () => {
  expect(
    ['For the rice:', '½ batch:', '۲ cups:', 'x:', ':', `${'a'.repeat(80)}:`].map(isHeader),
  ).toEqual([true, false, false, true, false, false])
  const d = draftOf(recipe(), null)
  expect(validate({ ...d, ingredients: 'x'.repeat(501) }).ingredients).toMatch(/500 characters/)
  expect(validate({ ...d, steps: [{ text: 'y'.repeat(4001) }] }).steps).toMatch(/4000 characters/)
})

test('editor keeps the version it opened: a newer synced version is never reverted silently', async () => {
  await seed()
  renderApp(`/recipes/${R}/edit`)
  expect(await screen.findByDisplayValue('Khoresh gheymeh')).toBeInTheDocument()
  // Another device's v3 syncs in mid-edit.
  const V3 = '0190a1b2-0000-7000-8000-0000000000b3'
  await db.recipe_version.put(
    version(V3, 3, { origin: 'chat', steps: [{ text: 'Fry the onions.' }, { text: 'Add lamb.' }] }),
  )
  await db.recipe.update(R, { currentVersionId: V3 })
  expect(await screen.findByText(/changed on another device/)).toBeInTheDocument()

  // Nothing changed against what I opened (v2): no version.
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(async () =>
    expect(await db.recipe_version.where('recipeId').equals(R).count()).toBe(3),
  )
  expect(await pendingCommands()).toEqual([])
})

test('editor based on v2 records v2 as the parent', async () => {
  await seed()
  renderApp(`/recipes/${R}/edit`)
  fireEvent.change(await screen.findByLabelText('Notes'), { target: { value: 'More lime.' } })
  const V3 = '0190a1b2-0000-7000-8000-0000000000b3'
  await db.recipe_version.put(version(V3, 3, { origin: 'chat' }))
  await db.recipe.update(R, { currentVersionId: V3 })
  await screen.findByText(/changed on another device/)
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(async () => expect((await pendingCommands()).length).toBe(1))
  expect((await pendingCommands())[0]).toMatchObject({
    type: 'recipe.version.create',
    payload: { parentVersionId: V2, notes: 'More lime.' },
  })
})

test('editor opens even when the current version is not on this device', async () => {
  await db.recipe.put(recipe({ currentVersionId: '0190a1b2-0000-7000-8000-0000000000ff' }))
  renderApp(`/recipes/${R}/edit`)
  expect(await screen.findByDisplayValue('Khoresh gheymeh')).toBeInTheDocument()
})
