import { Check, Link2, ListPlus, PackageCheck } from 'lucide-react'
import { useMemo, useState } from 'react'
import { v7 as uuidv7 } from 'uuid'
import { Page } from '../../app/Page'
import { dispatch } from '../../offline/outbox'
import type { Product, ProductStat } from '../../offline/replica'
import { Button, cn, Segmented, Sheet, SheetContent, toast } from '../../ui'
import { FoodIcon } from '../../ui/food-icons'
import {
  useCategories,
  useLocations,
  useProductStats,
  useProducts,
  useStores,
} from '../catalog/hooks'
import { foodIconFor, useIconLookup } from '../catalog/icons'
import { type MatchState, matchState, matchWorthShowing } from '../catalog/match'
import { type PickedItem, ProductPicker } from '../catalog/ProductPicker'
import { formatQty, guessList, usualPick } from '../catalog/suggest'
import { ManageList } from '../settings/ManageList'
import { useAllShoppingItems, useShoppingItems, useShoppingLists, useStockItems } from './hooks'
import { ItemSheet } from './ItemSheet'
import { groupRows, productNamed, productPick, type Row, sameName, toRows } from './items'
import { PutAwaySheet } from './PutAwaySheet'

export function ShoppingPage() {
  const lists = useShoppingLists()
  const [chosen, setChosen] = useState<string>()
  const list = lists.find((l) => l.id === chosen) ?? lists[0]
  const items = useShoppingItems(list?.id)
  const allItems = useAllShoppingItems()
  const products = useProducts()
  const stats = useProductStats()
  const stores = useStores()
  const locations = useLocations()
  const categories = useCategories()
  const stock = useStockItems()

  const [by, setBy] = useState<'type' | 'store'>('type')
  const [storeFilter, setStoreFilter] = useState<string | null>(null)
  const [editing, setEditing] = useState<Row | null>(null)
  const [putAway, setPutAway] = useState(false)
  const [managing, setManaging] = useState(false)

  const icons = useIconLookup()
  const productById = useMemo(() => new Map(products.map((p) => [p.id, p])), [products])
  const statsByProduct = useMemo(() => {
    const m = new Map<string, ProductStat[]>()
    for (const s of stats) m.set(s.productId, [...(m.get(s.productId) ?? []), s])
    return m
  }, [stats])
  const rows = useMemo(() => toRows(items, productById), [items, productById])
  const inStore = (r: Row) =>
    !storeFilter || r.storeIds.length === 0 || r.storeIds.includes(storeFilter)
  const open = rows.filter((r) => !r.checkedAt && inStore(r))
  const cart = rows.filter((r) => r.checkedAt && !r.stockItemId)
  const groups = groupRows(open, by, categories, stores)

  const guesses = useMemo(
    () => guessList({ products, stats, stockItems: stock, shoppingItems: allItems }),
    [products, stats, stock, allItems],
  )

  /** The household product a pick is: its own, the one named the same, else none (a new one). */
  const knownProduct = (p: PickedItem): Product | undefined => {
    if (p.productId) return productById.get(p.productId)
    const named = productNamed(products, p.name)
    if (named) return named
    if (!p.ingredientId) return undefined
    return products.find(
      (x) => !x.brand && x.ingredientId === p.ingredientId && sameName(x.name) === sameName(p.name),
    )
  }

  // Every item is linked to a product: a known one (filling in its usual store and amount), or a
  // new one the add creates, so it gets its icon and match straight away.
  const add = (picked: PickedItem) => {
    if (!list) return
    const product = knownProduct(picked)
    let p = picked
    if (product) {
      const usual = usualPick(product, statsByProduct.get(product.id))
      p = {
        ...p,
        productId: product.id,
        name: product.name,
        qty: p.qty ?? (p.unit && p.unit !== usual.unit ? undefined : usual.qty),
        unit: p.unit ?? usual.unit,
        storeIds: p.storeIds.length ? p.storeIds : usual.storeIds,
        categoryId: p.categoryId ?? usual.categoryId,
      }
    }
    const dupe = items.find(
      (i) =>
        !i.checkedAt &&
        (product ? i.productId === product.id : i.text.toLowerCase() === p.name.toLowerCase()),
    )
    if (dupe) {
      toast({ title: `${p.name} is already on the list`, tone: 'info' })
      return
    }
    void dispatch('shopping.item.add', {
      id: uuidv7(),
      listId: list.id,
      text: p.name,
      productId: product?.id ?? uuidv7(),
      ...(!product && p.ingredientId ? { ingredientId: p.ingredientId } : {}),
      ...(p.qty ? { qty: p.qty } : {}),
      ...(p.unit ? { unit: p.unit } : {}),
      ...(p.categoryId ? { categoryId: p.categoryId } : {}),
      ...(p.storeIds.length ? { storeIds: p.storeIds } : {}),
    })
    // A typed unit ("2 kg chicken") is a hand-picked one: it becomes the item's default.
    if (product && p.unitTyped && p.unit && p.unit !== product.defaultUnit)
      void dispatch('catalog.product.update', {
        id: product.id,
        defaultUnit: p.unit,
        defaultQty: p.qty ?? null,
      })
  }

  const confirmMatch = (product: Product, m: Extract<MatchState, { kind: 'confirm' }>) =>
    void dispatch('catalog.product.set_ingredient', {
      id: product.id,
      ingredientId: m.ingredient.id,
      extraIngredientIds: m.extras.map((i) => i.id),
    })

  const toggle = (r: Row) =>
    void (r.checkedAt
      ? dispatch('shopping.item.uncheck', { id: r.id })
      : dispatch('shopping.item.check', {
          id: r.id,
          ...(storeFilter ? { storeId: storeFilter } : {}),
          ...productPick(r, products, uuidv7),
        }))

  // Items added before every item had a product fall back to the product their text names.
  const iconFor = (r: Row) => {
    const p = r.product ?? productNamed(products, r.text)
    return foodIconFor(
      { icon: p?.icon, ingredientId: p?.ingredientId, categoryId: p?.categoryId ?? r.categoryId },
      icons,
    )
  }

  const matchLine = (r: Row, done: boolean) => {
    if (!r.product || done) return null
    const m = matchState(r.product, icons.ingredients)
    // An unsure match gets its own question below the row instead.
    if (m.kind === 'confirm' || !matchWorthShowing(r.product, m)) return null
    const names =
      'ingredient' in m && m.ingredient
        ? [m.ingredient, ...m.extras].map((i) => i.name).join(' + ')
        : ''
    const text =
      m.kind === 'unmatched'
        ? 'Not matched yet'
        : m.kind === 'checking'
          ? names
            ? `${names} (checking)`
            : 'Matching…'
          : names
    return (
      <span
        className="t-body-sm flex min-w-0 items-center gap-1 text-ink-muted"
        data-match={m.kind}
      >
        <Link2 aria-hidden className="size-3.5 shrink-0" />
        <span className="sr-only">Matches </span>
        <span className="truncate">{text}</span>
      </span>
    )
  }

  const confirmBar = (r: Row, done: boolean) => {
    if (!r.product || done) return null
    const m = matchState(r.product, icons.ingredients)
    if (m.kind !== 'confirm') return null
    const product = r.product
    return (
      <div className="flex items-center gap-2 pt-2 pl-[38px]">
        <span className="t-body-sm min-w-0 flex-1 text-ink-muted">
          Is this <b className="text-ink">{m.ingredient.name}</b>?
        </span>
        <Button size="sm" variant="secondary" onClick={() => setEditing(r)}>
          Change
        </Button>
        <Button size="sm" onClick={() => confirmMatch(product, m)}>
          Yes
        </Button>
      </div>
    )
  }

  const row = (r: Row, done = false) => (
    <li key={r.id} className={cn('flex flex-col px-3 py-2.5', done && 'opacity-70')}>
      <div className="flex items-center gap-3">
        {/* biome-ignore lint/a11y/useSemanticElements: a styled button; a native checkbox can't take the design */}
        <button
          type="button"
          role="checkbox"
          aria-checked={done}
          aria-label={`${done ? 'Uncheck' : 'Check'} ${r.title}`}
          onClick={() => toggle(r)}
          className={cn(
            'grid size-[26px] shrink-0 place-items-center rounded-[8px] border-2 border-border bg-surface p-0 text-on-brand',
            done && 'border-brand bg-brand',
          )}
        >
          {done && <Check aria-hidden className="size-4" strokeWidth={3} />}
        </button>
        <button
          type="button"
          onClick={() => setEditing(r)}
          className="flex min-w-0 flex-1 items-center gap-3 border-0 bg-transparent p-0 text-left text-ink"
        >
          <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-surface-2">
            <FoodIcon k={iconFor(r)} size={20} />
          </span>
          <span className="flex min-w-0 flex-1 flex-col">
            <span className={cn('truncate font-semibold', done && 'line-through text-ink-muted')}>
              {r.brand ? `${r.title} · ${r.brand}` : r.title}
            </span>
            {(r.storeIds.length > 0 || r.note) && (
              <span className="t-body-sm truncate text-ink-muted">
                {[...r.storeIds.map((id) => stores.find((s) => s.id === id)?.name), r.note]
                  .filter(Boolean)
                  .join(' · ')}
              </span>
            )}
            {matchLine(r, done)}
          </span>
          <span className="font-semibold tabular-nums">{formatQty(r.qty, r.unit)}</span>
        </button>
      </div>
      {confirmBar(r, done)}
    </li>
  )

  return (
    <Page
      title="Shopping"
      actions={
        <Button
          variant="ghost"
          size="icon"
          aria-label="Manage lists"
          onClick={() => setManaging(true)}
        >
          <ListPlus />
        </Button>
      }
    >
      {lists.length > 1 && (
        <Segmented
          aria-label="List"
          value={list?.id ?? ''}
          onValueChange={setChosen}
          options={lists.map((l) => ({ value: l.id, label: l.name }))}
        />
      )}
      <ProductPicker context="shopping" onPick={add} />
      {guesses.length > 0 && (
        <section aria-label="Suggested" className="flex flex-col gap-1.5">
          <h2 className="t-label m-0 text-ink-muted">Suggested</h2>
          <div className="flex flex-wrap gap-1.5">
            {guesses.map((g) => (
              <button
                key={g.productId}
                type="button"
                onClick={() => add(g.item)}
                className="flex items-center gap-1.5 rounded-full border border-border bg-surface px-3 py-1.5 text-[13px] font-semibold text-ink hover:bg-surface-2"
              >
                <ListPlus aria-hidden className="size-4 text-brand" />
                {g.name}
              </button>
            ))}
          </div>
        </section>
      )}
      <Segmented
        aria-label="Group by"
        value={by}
        onValueChange={setBy}
        options={[
          { value: 'type', label: 'By type' },
          { value: 'store', label: 'By store' },
        ]}
      />
      {stores.length > 0 && (
        <fieldset className="m-0 flex flex-wrap gap-1.5 border-0 p-0" aria-label="Store filter">
          {[{ id: null, name: 'All stores' }, ...stores].map((s) => (
            <button
              key={s.id ?? 'all'}
              type="button"
              aria-pressed={storeFilter === s.id}
              onClick={() => setStoreFilter(s.id)}
              className={cn(
                'rounded-full border border-border bg-surface px-3 py-1.5 text-[13px] font-semibold text-ink-muted',
                storeFilter === s.id && 'border-brand bg-brand-soft text-brand',
              )}
            >
              {s.name}
            </button>
          ))}
        </fieldset>
      )}
      {cart.length > 0 && (
        <div className="flex items-center gap-3 rounded-lg bg-brand-soft p-3">
          <PackageCheck aria-hidden className="shrink-0 text-brand" />
          <div className="flex-1 text-[14px]">
            <b className="block">{cart.length} in your cart</b>
            Put them away to add them to inventory.
          </div>
          <Button size="sm" onClick={() => setPutAway(true)}>
            Move {cart.length} to inventory
          </Button>
        </div>
      )}
      {open.length === 0 && cart.length === 0 && (
        <p className="t-body-sm m-0 text-ink-muted">
          Nothing on the list. Type above to add something.
        </p>
      )}
      {groups.map((g) => (
        <section key={g.key} className="flex flex-col gap-1.5">
          <h3 className="t-label m-0 px-0.5 tracking-wide text-ink-muted uppercase">{g.title}</h3>
          <ul className="m-0 list-none rounded-lg bg-surface p-0 py-1 shadow-card [&>li+li]:border-t [&>li+li]:border-border">
            {g.rows.map((r) => row(r))}
          </ul>
        </section>
      ))}
      {cart.length > 0 && (
        <section className="flex flex-col gap-1.5">
          <h3 className="t-label m-0 px-0.5 tracking-wide text-ink-muted uppercase">In cart</h3>
          <ul className="m-0 list-none rounded-lg bg-surface p-0 py-1 shadow-card [&>li+li]:border-t [&>li+li]:border-border">
            {cart.map((r) => row(r, true))}
          </ul>
        </section>
      )}
      <ItemSheet
        row={editing && (rows.find((x) => x.id === editing.id) ?? editing)}
        stores={stores}
        onClose={() => setEditing(null)}
      />
      {list && (
        <PutAwaySheet
          open={putAway}
          onOpenChange={setPutAway}
          rows={cart}
          products={products}
          locations={locations}
          storeId={storeFilter}
        />
      )}
      <Sheet open={managing} onOpenChange={setManaging}>
        <SheetContent title="Shopping lists">
          <ManageList
            noun="list"
            rows={lists.map((l) => ({ id: l.id, name: l.name }))}
            onAdd={(name) => {
              const id = uuidv7()
              void dispatch('shopping.list.create', {
                id,
                name,
                sort: (lists.at(-1)?.sort ?? 0) + 10,
              })
              setChosen(id)
            }}
            onRename={(id, name) => void dispatch('shopping.list.rename', { id, name })}
            onDelete={(id) => void dispatch('shopping.list.delete', { id })}
          />
        </SheetContent>
      </Sheet>
    </Page>
  )
}
