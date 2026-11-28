import 'fake-indexeddb/auto'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import type {
  Category,
  Ingredient,
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
      'shopping.item.delete',
    ]),
  )
  const put = (await db.outbox.toArray())[2]?.payload as { inventory: { locationId: string } }
  expect(put.inventory.locationId).toBe('loc-fridge')
  await waitFor(() => expect(screen.queryByRole('checkbox')).not.toBeInTheDocument())
  // Offline, the stock item exists locally at once, so nothing vanishes before the next sync.
  const stock = await db.stock_item.toArray()
  expect(stock).toHaveLength(1)
  expect(stock[0]).toMatchObject({
    productId: 'p-milk',
    locationId: 'loc-fridge',
    source: 'shopping',
  })
})

test('a typed-in item gets a product when added, offline too', async () => {
  const user = userEvent.setup()
  renderApp('/shopping')
  await waitFor(async () =>
    expect((await db.meta.get('sync.household'))?.value).toBe(adminMe.activeHouseholdId),
  )
  await seed()
  await user.type(await screen.findByLabelText('Item name'), 'Quince{Enter}')
  await user.click(await screen.findByRole('checkbox', { name: 'Check Quince' }))
  await user.click(await screen.findByRole('button', { name: 'Move 1 to inventory' }))
  await user.click(await screen.findByRole('button', { name: 'Add to inventory' }))
  await waitFor(async () => expect(await db.stock_item.count()).toBe(1))
  const add = (await db.outbox.toArray())[0]?.payload as { productId: string }
  // The add proposes the product id; the server creates it under that id.
  expect(add.productId).toBeTruthy()
  expect(await db.product.get(add.productId)).toMatchObject({ name: 'Quince', rev: 0 })
  expect((await db.stock_item.toArray())[0]?.productId).toBe(add.productId)
})

test('typed text picks the product it names, ignoring case and plurals', async () => {
  const { productNamed } = await import('./items')
  await seed()
  const products = await db.product.toArray()
  expect(productNamed(products, 'MILK')?.id).toBe('p-milk')
  expect(productNamed(products, 'milks')?.id).toBe('p-milk')
  // A branded product needs the exact name.
  expect(productNamed(products, 'mandarin')).toBeUndefined()
  expect(productNamed(products, 'mandarins')?.id).toBe('p-mand')
})

const ingredient = (id: string, name: string, extra: Partial<Ingredient> = {}): Ingredient => ({
  ...base,
  id,
  householdId: null,
  slug: id,
  name,
  plural: null,
  parentId: null,
  categoryId: 'cat-dairy',
  icon: null,
  aliases: [],
  shelfLife: {},
  defaultUnit: null,
  source: 'seed',
  ...extra,
})

async function openList() {
  renderApp('/shopping')
  await waitFor(async () =>
    expect((await db.meta.get('sync.household'))?.value).toBe(adminMe.activeHouseholdId),
  )
  await seed()
}

const sent = async (type: string) =>
  (await db.outbox.toArray()).filter((r) => r.type === type).map((r) => r.payload)

test('an alias pick keeps the typed name, shows the match and its icon', async () => {
  const user = userEvent.setup()
  await openList()
  await db.ingredient.bulkPut([
    ingredient('cheese', 'Cheese', { icon: 'cheese-wedge' }),
    ingredient('cream-cheese', 'Cream cheese', { parentId: 'cheese', aliases: ['philadelphia'] }),
  ])
  await user.type(await screen.findByLabelText('Item name'), 'philadelphia cheese{Enter}')
  await waitFor(async () => expect(await sent('shopping.item.add')).toHaveLength(1))
  const [add] = (await sent('shopping.item.add')) as { productId: string; text: string }[]
  expect(add).toMatchObject({ text: 'Philadelphia cheese', ingredientId: 'cream-cheese' })
  // The new product exists locally at once, matched as the user picked.
  expect(await db.product.get(add?.productId ?? '')).toMatchObject({
    name: 'Philadelphia cheese',
    ingredientId: 'cream-cheese',
    matchSource: 'user',
  })
  const row = (await screen.findByRole('button', { name: /Philadelphia cheese/ })) as HTMLElement
  expect(row).toHaveTextContent('Cream cheese')
  // Not the dairy category's milk glass: the ingredient's parent icon.
  expect(row.querySelector('svg[data-icon]')?.getAttribute('data-icon')).toBe('cheese-wedge')
})

test('an unsure LLM match asks, and Yes makes it the user match', async () => {
  const user = userEvent.setup()
  await openList()
  await db.ingredient.put(ingredient('cream-cheese', 'Cream cheese'))
  await db.product.update('p-milk', {
    name: 'Philly spread',
    ingredientId: 'cream-cheese',
    matchSource: 'llm',
    matchConfidence: 0.6,
  })
  await user.type(await screen.findByLabelText('Item name'), 'philly spread{Enter}')
  expect(await screen.findByText(/Is this/)).toHaveTextContent('Is this Cream cheese?')
  await user.click(screen.getByRole('button', { name: 'Yes' }))
  await waitFor(async () =>
    expect(await sent('catalog.product.set_ingredient')).toEqual([
      { id: 'p-milk', ingredientId: 'cream-cheese', extraIngredientIds: [] },
    ]),
  )
  await waitFor(() => expect(screen.queryByText(/Is this/)).not.toBeInTheDocument())
})

test('a confident match asks nothing', async () => {
  const user = userEvent.setup()
  await openList()
  await db.ingredient.put(ingredient('cream-cheese', 'Cream cheese'))
  await db.product.update('p-milk', {
    name: 'Philly spread',
    ingredientId: 'cream-cheese',
    matchSource: 'llm',
    matchConfidence: 0.95,
  })
  await user.type(await screen.findByLabelText('Item name'), 'philly spread{Enter}')
  expect(await screen.findByRole('button', { name: /Philly spread/ })).toHaveTextContent(
    'Cream cheese',
  )
  expect(screen.queryByText(/Is this/)).not.toBeInTheDocument()
})

test('a known item fills its last hand-picked unit, and a typed unit becomes the default', async () => {
  const user = userEvent.setup()
  await openList()
  // Usually bought by the lb, but last set by hand to a bag of 1.
  await db.product.update('p-mand', { defaultUnit: 'bag', defaultQty: '1' })
  await user.type(await screen.findByLabelText('Item name'), 'mandarins{Enter}')
  await waitFor(async () => expect(await sent('shopping.item.add')).toHaveLength(1))
  expect((await sent('shopping.item.add'))[0]).toMatchObject({ qty: '1', unit: 'bag' })

  await user.type(screen.getByLabelText('Item name'), '2 l milk{Enter}')
  await waitFor(async () => expect(await sent('shopping.item.add')).toHaveLength(2))
  expect((await sent('shopping.item.add'))[1]).toMatchObject({
    productId: 'p-milk',
    qty: '2',
    unit: 'l',
    rememberAmount: true,
  })
  expect(await db.product.get('p-milk')).toMatchObject({ defaultUnit: 'l', defaultQty: '2' })
  // A new item's typed unit is remembered too.
  await user.type(screen.getByLabelText('Item name'), '3 kg quince{Enter}')
  await waitFor(async () => expect(await sent('shopping.item.add')).toHaveLength(3))
  expect((await sent('shopping.item.add'))[2]).toMatchObject({ unit: 'kg', rememberAmount: true })
})

test('the item sheet adds a store inline and remembers store and unit for next time', async () => {
  const user = userEvent.setup()
  await openList()
  await user.type(await screen.findByLabelText('Item name'), 'milk{Enter}')
  await user.click(await screen.findByRole('button', { name: /^Milk/ }))
  await user.click(await screen.findByRole('button', { name: 'New store' }))
  await user.type(screen.getByLabelText('Store name'), 'No Frills{Enter}')
  expect(await screen.findByRole('button', { name: 'No Frills', pressed: true })).toBeVisible()
  await user.selectOptions(screen.getByLabelText('Unit'), 'l')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(async () => expect(await sent('catalog.product.update')).toHaveLength(1))
  const [store] = (await sent('catalog.store.create')) as { id: string; name: string }[]
  expect(store?.name).toBe('No Frills')
  expect((await sent('shopping.item.update'))[0]).toMatchObject({
    unit: 'l',
    storeIds: [store?.id],
  })
  expect((await sent('catalog.product.update'))[0]).toEqual({
    id: 'p-milk',
    defaultUnit: 'l',
    defaultQty: null,
    defaultStoreId: store?.id,
  })
})
