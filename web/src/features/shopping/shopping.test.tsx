import 'fake-indexeddb/auto'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import type {
  Category,
  Location,
  Product,
  ProductStat,
  ShoppingList,
  Store,
} from '../../offline/replica'
import { adminMe, renderApp } from '../../test-utils'
import { ProductPicker } from '../catalog/ProductPicker'

beforeEach(() => {
  vi.stubGlobal('fetch', async () => {
    throw new TypeError('offline')
  })
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await Promise.all(db.tables.map((t) => t.clear()))
})

const base = {
  householdId: 'h',
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
  rev: 1,
}
const daysAgo = (n: number) => new Date(Date.now() - n * 86_400_000).toISOString()

async function seed() {
  const cat: Category = {
    ...base,
    id: 'cat-fruit',
    householdId: null,
    slug: 'produce-fruit',
    name: 'Fruit',
    icon: null,
    sort: 1,
  }
  const dairy: Category = {
    ...base,
    id: 'cat-dairy',
    householdId: null,
    slug: 'dairy-eggs',
    name: 'Dairy & eggs',
    icon: null,
    sort: 2,
  }
  const costco: Store = { ...base, id: 'store-costco', name: 'Costco', sort: 1, color: null }
  const fridge: Location = { ...base, id: 'loc-fridge', name: 'Fridge', kind: 'fridge', sort: 1 }
  const list: ShoppingList = { ...base, id: 'list-1', name: 'Groceries', sort: 0 }
  const product = (id: string, name: string, extra: Partial<Product> = {}): Product => ({
    ...base,
    id,
    name,
    brand: null,
    ingredientId: null,
    extraIngredientIds: [],
    categoryId: 'cat-fruit',
    icon: null,
    defaultStoreId: null,
    defaultQty: null,
    defaultUnit: null,
    package: null,
    defaultLocationKind: null,
    shelfLife: null,
    matchSource: 'rule',
    matchConfidence: 1,
    enrichState: 'done',
    enrichAttempts: 0,
    offCode: null,
    note: null,
    ...extra,
  })
  const stat = (
    productId: string,
    storeId: string | null,
    extra: Partial<ProductStat> = {},
  ): ProductStat => ({
    ...base,
    id: `${productId}-${storeId}`,
    productId,
    storeId,
    timesBought: 5,
    lastBoughtAt: daysAgo(20),
    typicalQty: '2',
    typicalUnit: 'lb',
    medianIntervalDays: 10,
    lastQty: '2',
    ...extra,
  })
  await db.category.bulkPut([cat, dairy])
  await db.store.put(costco)
  await db.location.put(fridge)
  await db.shopping_list.put(list)
  await db.product.bulkPut([
    product('p-mand', 'Mandarins', { brand: 'Kirkland', defaultStoreId: 'store-costco' }),
    product('p-milk', 'Milk', { categoryId: 'cat-dairy' }),
  ])
  // Mandarins are bought at Costco about every 10 days, last 20 days ago: overdue.
  await db.product_stat.bulkPut([stat('p-mand', 'store-costco'), stat('p-mand', null)])
}

test('typing a typo and pressing Enter fills the whole item from history', async () => {
  await seed()
  const onPick = vi.fn()
  const user = userEvent.setup()
  render(<ProductPicker context="shopping" onPick={onPick} />)
  await user.type(screen.getByLabelText('Item name'), 'manderins{Enter}')
  expect(onPick).toHaveBeenCalledWith({
    productId: 'p-mand',
    name: 'Mandarins',
    brand: 'Kirkland',
    storeIds: ['store-costco'],
    categoryId: 'cat-fruit',
    qty: '2',
    unit: 'lb',
  })
})

test('the ghost completion is accepted with Tab or a tap', async () => {
  await seed()
  const onPick = vi.fn()
  const user = userEvent.setup()
  render(<ProductPicker context="shopping" onPick={onPick} />)
  const input = screen.getByLabelText('Item name')
  await user.type(input, 'mand')
  expect(await screen.findByRole('button', { name: 'Accept completion' })).toHaveTextContent(
    'arins',
  )
  await user.keyboard('{Tab}')
  expect(onPick).toHaveBeenCalledTimes(1)
  expect(onPick.mock.calls[0]?.[0]).toMatchObject({
    productId: 'p-mand',
    storeIds: ['store-costco'],
  })

  await user.type(input, 'mand')
  await user.click(await screen.findByRole('button', { name: 'Accept completion' }))
  expect(onPick).toHaveBeenCalledTimes(2)
})

test('unmatched text is added as typed, with a leading quantity', async () => {
  await seed()
  const onPick = vi.fn()
  const user = userEvent.setup()
  render(<ProductPicker context="shopping" onPick={onPick} />)
  await user.type(screen.getByLabelText('Item name'), '3 dragon fruit{Enter}')
  expect(onPick).toHaveBeenCalledWith({ name: 'dragon fruit', storeIds: [], qty: '3' })
})

test('guess chips add an overdue item with its usual store and quantity, then disappear', async () => {
  const user = userEvent.setup()
  renderApp('/shopping')
  await waitFor(async () =>
    expect((await db.meta.get('sync.household'))?.value).toBe(adminMe.activeHouseholdId),
  )
  await seed()
  const chip = await screen.findByRole('button', { name: 'Mandarins' })
  expect(
    within(screen.getByRole('region', { name: 'Suggested' })).getByRole('button', {
      name: 'Mandarins',
    }),
  ).toBe(chip)
  await user.click(chip)
  await waitFor(async () =>
    expect((await db.outbox.toArray()).map((r) => r.type)).toEqual(['shopping.item.add']),
  )
  const sent = (await db.outbox.toArray())[0]?.payload
  expect(sent).toMatchObject({
    listId: 'list-1',
    productId: 'p-mand',
    qty: '2',
    unit: 'lb',
    storeIds: ['store-costco'],
  })
  expect(await screen.findByText('Mandarins · Kirkland')).toBeInTheDocument()
  await waitFor(() =>
    expect(screen.queryByRole('region', { name: 'Suggested' })).not.toBeInTheDocument(),
  )
})

test('ticking items offers to move them to inventory', async () => {
  const user = userEvent.setup()
  renderApp('/shopping')
  await waitFor(async () =>
    expect((await db.meta.get('sync.household'))?.value).toBe(adminMe.activeHouseholdId),
  )
  await seed()
  await user.type(await screen.findByLabelText('Item name'), 'milk{Enter}')
  await user.click(await screen.findByRole('checkbox', { name: 'Check Milk' }))
  await user.click(await screen.findByRole('button', { name: 'Move 1 to inventory' }))
  await user.click(await screen.findByRole('button', { name: 'Add to inventory' }))
  await waitFor(async () =>
    expect((await db.outbox.toArray()).map((r) => r.type)).toEqual([
      'shopping.item.add',
      'shopping.item.check',
      'shopping.item.check',
      'shopping.item.clear_checked',
    ]),
  )
  const put = (await db.outbox.toArray())[2]?.payload as { inventory: { locationId: string } }
  expect(put.inventory.locationId).toBe('loc-fridge')
  await waitFor(() => expect(screen.queryByRole('checkbox')).not.toBeInTheDocument())
})
