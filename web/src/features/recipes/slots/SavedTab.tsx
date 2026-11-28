import { ChevronRight, Clock, Plus, Users } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import type { Recipe } from '../../../offline/replica'
import { Button, List, ListItem, Segmented } from '../../../ui'
import { normalize, tokenScore, words } from '../../catalog/suggest/text'
import { useSavedRecipes } from '../hooks'
import { RecipeTile } from '../look'
import { SearchField } from '../SearchField'
import { ComingSoon } from './ComingSoon'

export type SavedSort = 'recent' | 'cooked' | 'az'

const sortOptions: { value: SavedSort; label: string }[] = [
  { value: 'recent', label: 'Recent' },
  { value: 'cooked', label: 'Most cooked' },
  { value: 'az', label: 'A–Z' },
]

/** Last time the recipe mattered: cooked, else added or changed. */
const recency = (r: Recipe) =>
  (r.lastCookedAt ?? '') > r.updatedAt ? (r.lastCookedAt as string) : r.updatedAt

const byTitle = (a: Recipe, b: Recipe) => a.title.localeCompare(b.title)

/**
 * Typo-tolerant match of every query word against the recipe's title, subtitle and tags.
 * Returns 0 when some word matches nothing.
 */
export function recipeScore(r: Recipe, query: string): number {
  const tokens = words(normalize(query))
  if (tokens.length === 0) return 1
  const hay = words(normalize([r.title, r.subtitle ?? '', ...r.tags].join(' ')))
  let total = 0
  for (const t of tokens) {
    const best = Math.max(0, ...hay.map((w) => tokenScore(t, w)))
    if (best === 0) return 0
    total += best
  }
  return total / tokens.length
}

export function filterSaved(recipes: Recipe[], query: string, sort: SavedSort): Recipe[] {
  const scored = recipes
    .map((r) => ({ r, s: recipeScore(r, query) }))
    .filter((x) => x.s > 0)
    .map((x) => x.r)
  const cmp: Record<SavedSort, (a: Recipe, b: Recipe) => number> = {
    recent: (a, b) => recency(b).localeCompare(recency(a)) || byTitle(a, b),
    cooked: (a, b) => b.cookedCount - a.cookedCount || byTitle(a, b),
    az: byTitle,
  }
  return scored.sort(cmp[sort])
}

export const cookedText = (n: number) =>
  n === 0 ? 'Not cooked yet' : n === 1 ? 'Cooked once' : `Cooked ${n} times`

/** Saved recipes: search by title or tag, sort, and a way to write a new one. */
export function SavedTab() {
  const recipes = useSavedRecipes()
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<SavedSort>('recent')
  const shown = useMemo(() => filterSaved(recipes, query, sort), [recipes, query, sort])

  const newButton = (
    <Button asChild variant="secondary" block>
      <Link to="/recipes/new">
        <Plus aria-hidden />
        New recipe
      </Link>
    </Button>
  )

  if (recipes.length === 0)
    return (
      <div className="flex flex-col gap-3">
        <ComingSoon>
          No saved recipes yet. Write one down, or bring one in from the Import tab.
        </ComingSoon>
        {newButton}
      </div>
    )

  return (
    <div className="flex flex-col gap-3">
      <SearchField
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="Search recipes or tags"
        aria-label="Search saved recipes"
      />
      <Segmented value={sort} onValueChange={setSort} options={sortOptions} aria-label="Sort" />
      {shown.length === 0 ? (
        <p className="t-body-sm m-0 px-1 text-ink-muted">No saved recipe matches “{query}”.</p>
      ) : (
        <List aria-label="Saved recipes">
          {shown.map((r) => (
            <ListItem
              key={r.id}
              asChild
              leading={
                <RecipeTile
                  recipeId={r.id}
                  icon={null}
                  size={19}
                  className="size-full rounded-[10px]"
                />
              }
              title={r.title}
              subtitle={
                <span className="flex flex-col">
                  <span>{cookedText(r.cookedCount)}</span>
                  {r.tags.length ? <span className="truncate">{r.tags.join(' · ')}</span> : null}
                </span>
              }
              trailing={<ChevronRight className="size-5 text-ink-muted" aria-hidden />}
            >
              <Link to={`/recipes/${r.id}`} />
            </ListItem>
          ))}
        </List>
      )}
      {newButton}
    </div>
  )
}

export function RecipeMeta({
  servings,
  minutes,
}: {
  servings: number | null
  minutes: number | null
}) {
  if (!servings && !minutes) return null
  return (
    <span className="inline-flex items-center gap-3">
      {minutes ? (
        <span className="inline-flex items-center gap-1">
          <Clock className="size-3.5" aria-hidden /> {formatMinutes(minutes)}
        </span>
      ) : null}
      {servings ? (
        <span className="inline-flex items-center gap-1">
          <Users className="size-3.5" aria-hidden /> {servings}{' '}
          {servings === 1 ? 'portion' : 'portions'}
        </span>
      ) : null}
    </span>
  )
}

/** 45 → "45 min", 120 → "2 h", 135 → "2 h 15 min". */
export function formatMinutes(m: number): string {
  if (m < 60) return `${m} min`
  const h = Math.floor(m / 60)
  const rest = m % 60
  return rest ? `${h} h ${rest} min` : `${h} h`
}
