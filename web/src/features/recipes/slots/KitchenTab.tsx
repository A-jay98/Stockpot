import { Bookmark, ChefHat, Clock, Replace, Sparkles } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { dispatch } from '../../../offline/outbox'
import { Badge, Button } from '../../../ui'
import { requestIdeas } from '../match/assist'
import { metaLabel, soonLabel, sourceLabel, swapLabel } from '../match/labels'
import type { RankedRecipe } from '../match/rank'
import { useRankedRecipes } from '../match/useKitchen'

/** "From your kitchen": saved recipes and AI ideas ranked by what's in stock and expiring. */
export function KitchenTab() {
  const ranked = useRankedRecipes()
  if (ranked === undefined) return null
  return (
    <div className="flex flex-col gap-3">
      {ranked.length === 0 ? (
        <p className="t-body-sm m-0 rounded-lg bg-surface p-4 text-ink-muted shadow-card">
          No recipes yet. Import some, or ask for ideas from what you have.
        </p>
      ) : (
        <ul className="m-0 grid list-none gap-3 p-0 md:grid-cols-2">
          {ranked.map((r) => (
            <li key={r.recipe.id}>
              <KitchenCard r={r} />
            </li>
          ))}
        </ul>
      )}
      <MoreIdeas />
    </div>
  )
}

export function KitchenCard({ r }: { r: RankedRecipe }) {
  const meta = [metaLabel(r.recipe), sourceLabel(r.recipe)].filter(Boolean).join(' · ')
  const pct = r.required ? Math.round((r.have / r.required) * 100) : 0
  const swap = r.firstSub ? swapLabel(r.firstSub) : ''
  const idea = !r.recipe.saved
  return (
    <article className="relative flex flex-col gap-2.5 rounded-lg bg-surface p-4 shadow-card">
      <div className="flex items-start gap-3">
        <span className="grid size-[72px] shrink-0 place-items-center rounded-lg bg-brand-soft text-brand">
          <ChefHat aria-hidden className="size-[30px]" />
        </span>
        <div className="flex min-w-0 flex-1 flex-col">
          <h3 className="m-0 text-[17px] leading-[22px] font-semibold">
            {/* The whole card opens the recipe; the Save button sits above the link. */}
            <Link
              to={`/recipes/${r.recipe.id}`}
              className="text-ink no-underline after:absolute after:inset-0 after:content-['']"
            >
              {r.recipe.title}
            </Link>
          </h3>
          {meta ? <span className="text-[12.5px] text-ink-muted">{meta}</span> : null}
          {r.required > 0 ? (
            <div className="mt-2 flex items-center gap-2">
              <div aria-hidden className="h-1.5 flex-1 overflow-hidden rounded-full bg-surface-2">
                <i className="block h-full rounded-full bg-brand" style={{ width: `${pct}%` }} />
              </div>
              <span className="tabular text-[12.5px] font-semibold">
                {r.have} of {r.required}
                <span className="sr-only"> ingredients in stock</span>
              </span>
            </div>
          ) : null}
        </div>
      </div>
      {r.soon.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {r.soon.slice(0, 3).map((s) => (
            <Badge key={s.item.id} tone={s.days !== null && s.days < 0 ? 'expired' : 'soon'}>
              <Clock aria-hidden />
              {soonLabel(s)}
            </Badge>
          ))}
        </div>
      ) : null}
      {swap ? (
        <p className="m-0 flex items-start gap-2 rounded-md bg-surface-2 px-2.5 py-2 text-[13.5px]">
          <Replace aria-hidden className="mt-px size-[17px] shrink-0 text-brand" />
          <span>{swap}</span>
        </p>
      ) : null}
      {idea ? (
        <div className="relative z-10 flex gap-2">
          <Button
            size="sm"
            variant="secondary"
            onClick={() => void dispatch('recipe.update', { id: r.recipe.id, saved: true })}
          >
            <Bookmark aria-hidden />
            Save idea
          </Button>
        </div>
      ) : null}
    </article>
  )
}

function MoreIdeas() {
  const [busy, setBusy] = useState(false)
  return (
    <Button
      variant="secondary"
      className="self-start"
      disabled={busy}
      onClick={async () => {
        setBusy(true)
        await requestIdeas()
        setBusy(false)
      }}
    >
      <Sparkles aria-hidden />
      {busy ? 'Thinking…' : 'More ideas'}
    </Button>
  )
}
