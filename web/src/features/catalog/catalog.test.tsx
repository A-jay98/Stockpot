import 'fake-indexeddb/auto'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import type { Ingredient, Product } from '../../offline/replica'
import { adminMe, renderApp } from '../../test-utils'
import { ProductPicker } from './ProductPicker'

beforeEach(() => {
  // No server: the sync engine stays offline and the outbox keeps what the UI queues.
  vi.stubGlobal('fetch', async () => {
    throw new TypeError('offline')
  })
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await Promise.all(db.tables.map((t) => t.clear()))
})

const base = { householdId: 'h', createdAt: '', updatedAt: '', rev: 1 }

const ingredient = (id: string, name: string, aliases: string[] = []): Ingredient => ({
  ...base,
  id,
  slug: id,
  name,
  plural: `${name}s`,
  parentId: null,
  categoryId: 'cat-fruit',
  icon: null,
  aliases,
  shelfLife: {},
  defaultUnit: null,
  source: 'seed',
})

test('stores settings: quick pick, rename and delete go through the replica and outbox', async () => {
  const user = userEvent.setup()
  renderApp('/settings/stores')
  // The shell adopts the household (clearing the replica) before the user can act.
  await waitFor(async () =>
    expect((await db.meta.get('sync.household'))?.value).toBe(adminMe.activeHouseholdId),
  )
  await user.click(await screen.findByRole('button', { name: 'Costco' }))
  expect(await screen.findByText('Costco', { selector: 'span.truncate' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Costco' })).not.toBeInTheDocument() // pick used up
  await waitFor(async () =>
    expect((await db.outbox.toArray()).map((r) => r.type)).toEqual(['catalog.store.create']),
  )

  await user.click(screen.getByRole('button', { name: 'Rename Costco' }))
  const name = await screen.findByLabelText('Name')
  await user.clear(name)
  await user.type(name, 'Costco Wholesale')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByText('Costco Wholesale')).toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Delete Costco Wholesale' }))
  await user.click(await screen.findByRole('button', { name: 'Delete' }))
  await waitFor(() => expect(screen.queryByText('Costco Wholesale')).not.toBeInTheDocument())
  expect((await db.outbox.toArray()).map((r) => r.type)).toEqual([
    'catalog.store.create',
    'catalog.store.update',
    'catalog.store.delete',
  ])
})

test('product picker matches products and unused ingredients, and adds free text on Enter', async () => {
  const mandarin = ingredient('ing-mandarin', 'Mandarin', ['clementine'])
  const product: Product = {
    ...base,
    id: 'prod-1',
    name: 'Mandarins',
    brand: 'Kirkland',
    ingredientId: mandarin.id,
    extraIngredientIds: [],
    categoryId: 'cat-fruit',
    icon: null,
    defaultStoreId: 'store-costco',
    defaultQty: '2',
    defaultUnit: 'lb',
    package: '2 lb bag',
    defaultLocationKind: null,
    shelfLife: null,
    matchSource: 'rule',
    matchConfidence: 1,
    enrichState: 'done',
    enrichAttempts: 0,
    offCode: null,
    note: null,
  }
  await db.ingredient.bulkPut([mandarin, ingredient('ing-avocado', 'Avocado')])
  await db.product.put(product)

  const onPick = vi.fn()
  const user = userEvent.setup()
  render(<ProductPicker context="shopping" onPick={onPick} />)
  const input = screen.getByLabelText('Item name')

  await user.type(input, 'mand')
  await user.click(await screen.findByRole('button', { name: /Mandarins · Kirkland/ }))
  expect(onPick).toHaveBeenLastCalledWith({
    productId: 'prod-1',
    ingredientId: 'ing-mandarin',
    name: 'Mandarins',
    brand: 'Kirkland',
    storeIds: ['store-costco'],
    qty: '2',
    unit: 'lb',
    categoryId: 'cat-fruit',
  })
  expect(screen.queryByRole('button', { name: /Mandarin/ })).not.toBeInTheDocument() // cleared

  await user.type(input, 'avo')
  await user.click(await screen.findByRole('button', { name: /Avocado/ }))
  expect(onPick).toHaveBeenLastCalledWith({
    ingredientId: 'ing-avocado',
    name: 'Avocado',
    storeIds: [],
    categoryId: 'cat-fruit',
  })

  await user.type(input, 'dragon fruit{Enter}')
  expect(onPick).toHaveBeenLastCalledWith({ name: 'dragon fruit', storeIds: [] })
})
