import { ArrowRight } from 'lucide-react'
import { Link } from 'react-router'
import { daysLabel } from '../match/labels'
import { cookTonight, URGENT } from '../match/rank'
import { useRankedRecipes } from '../match/useKitchen'

const list = (xs: string[]) =>
  xs.length <= 1 ? (xs[0] ?? '') : `${xs.slice(0, -1).join(', ')} and ${xs.at(-1)}`

/** Home "Cook tonight": the best recipe for what's expiring; hidden when none qualifies. */
export function CookTonightCard() {
  const ranked = useRankedRecipes()
  const top = ranked ? cookTonight(ranked) : undefined
  if (!top) return null
  const uses = top.soon
    .filter((s) => s.urgency >= URGENT)
    .slice(0, 3)
    .map((s) => `the ${s.product.name.toLowerCase()} (${daysLabel(s.days)})`)
  const servings = top.recipe.servings
  return (
    <Link
      to={`/recipes/${top.recipe.id}`}
      className="flex flex-col gap-2.5 rounded-lg bg-brand p-4 text-on-brand no-underline"
    >
      <span className="text-[12px] font-bold tracking-[.06em] uppercase opacity-85">
        Cook tonight
      </span>
      <h2 className="m-0 font-display text-[21px] leading-[26px]">{top.recipe.title}</h2>
      <p className="m-0 text-[14px] opacity-90">
        Uses {list(uses)}. You have {top.have} of {top.required} ingredients.
        {servings ? ` Makes ${servings} ${servings === 1 ? 'portion' : 'portions'}.` : ''}
      </p>
      <span className="inline-flex items-center gap-2 self-start rounded-[8px] bg-white/15 px-3 py-1.5 text-[13px] font-semibold">
        See recipe <ArrowRight aria-hidden className="size-4" />
      </span>
    </Link>
  )
}
