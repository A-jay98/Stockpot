import { Plus, Search } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { Page } from '../../app/Page'
import { Button, List, Segmented } from '../../ui'
import { useCategories } from '../catalog/hooks'
import { today as todayOf } from './dates'
import { type StockRow, useStock } from './hooks'
import { StockListItem } from './StockListItem'
import { freshness, isLow } from './status'

type Group = 'location' | 'category'
type Sort = 'expiry' | 'name' | 'recent'
type Show = 'all' | 'soon' | 'expired' | 'low'

const norm = (s: string) => s.normalize('NFKD').replace(/[̀-ͯ]/g, '').toLowerCase()

const sorters: Record<Sort, (a: StockRow, b: StockRow) => number> = {
  // Soonest first; undated items last.
  expiry: (a, b) =>
    (a.item.expiresOn ?? '9999').localeCompare(b.item.expiresOn ?? '9999') ||
    a.name.localeCompare(b.name),
  name: (a, b) => a.name.localeCompare(b.name),
  recent: (a, b) => b.item.createdAt.localeCompare(a.item.createdAt),
}

export function InventoryPage() {
  const stock = useStock()
  const categories = useCategories()
  const [params, setParams] = useSearchParams()
  const [query, setQuery] = useState('')
  const [group, setGroup] = useState<Group>('location')
  const [sort, setSort] = useState<Sort>('expiry')
  const show = (params.get('show') as Show | null) ?? 'all'
  const now = todayOf()

  const groups = useMemo(() => {
    const q = norm(query.trim())
    const rows = stock
      .filter((r) => !q || norm(`${r.name} ${r.ingredient?.name ?? ''}`).includes(q))
      .filter((r) => {
        const f = freshness(r.item, now)
        return show === 'all' || (show === 'low' ? isLow(r.item) : f === show)
      })
      .sort(sorters[sort])
    const by = new Map<string, { title: string; order: number; rows: StockRow[] }>()
    for (const r of rows) {
      const cat = categories.find((c) => c.id === r.categoryId)
      const [key, title, order] =
        group === 'location'
          ? [r.location?.id ?? '-', r.location?.name ?? 'No place', r.location?.sort ?? 9999]
          : [cat?.id ?? '-', cat?.name ?? 'Other', cat?.sort ?? 9999]
      const g = by.get(key) ?? { title, order, rows: [] }
      g.rows.push(r)
      by.set(key, g)
    }
    return [...by.values()].sort((a, b) => a.order - b.order)
  }, [stock, categories, query, group, sort, show, now])

  return (
    <Page
      title="Inventory"
      actions={
        <Button asChild size="sm">
          <Link to="/add">
            <Plus aria-hidden />
            Add
          </Link>
        </Button>
      }
    >
      <label className="flex items-center gap-2 rounded-md border border-border bg-surface px-3 py-[9px] text-ink-muted [&_svg]:size-5">
        <Search aria-hidden />
        <input
          placeholder="Search items"
          aria-label="Search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="min-w-0 flex-1 border-0 bg-transparent text-base text-ink outline-none"
        />
      </label>
      <div className="flex flex-wrap gap-2">
        <Segmented<Group>
          aria-label="Group by"
          value={group}
          onValueChange={setGroup}
          options={[
            { value: 'location', label: 'Place' },
            { value: 'category', label: 'Category' },
          ]}
        />
        <Segmented<Sort>
          aria-label="Sort by"
          value={sort}
          onValueChange={setSort}
          options={[
            { value: 'expiry', label: 'Expiry' },
            { value: 'name', label: 'Name' },
            { value: 'recent', label: 'Recent' },
          ]}
        />
      </div>
      {show !== 'all' && (
        <button
          type="button"
          className="self-start rounded-full border-0 bg-brand-soft px-3 py-1 text-sm font-semibold text-brand"
          onClick={() => setParams({})}
        >
          Showing {show === 'soon' ? 'expiring soon' : show} · clear
        </button>
      )}
      {groups.length === 0 ? (
        <p className="t-body-sm m-0 rounded-lg bg-surface p-4 text-ink-muted shadow-card">
          {stock.length === 0 ? 'Nothing here yet. Add items from Quick add.' : 'No items match.'}
        </p>
      ) : (
        <div className="flex flex-col gap-4 md:grid md:grid-cols-[repeat(auto-fit,minmax(320px,1fr))] md:items-start">
          {groups.map((g) => (
            <section key={g.title} aria-label={g.title} className="flex flex-col gap-2">
              <h2 className="m-0 flex items-baseline gap-2 text-[17px] font-semibold">
                {g.title}
                <span className="tabular text-[13px] text-ink-muted">{g.rows.length}</span>
              </h2>
              <List>
                {g.rows.map((r) => (
                  <StockListItem key={r.item.id} row={r} now={now} />
                ))}
              </List>
            </section>
          ))}
        </div>
      )}
    </Page>
  )
}
