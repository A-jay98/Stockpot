import { ArrowLeft } from 'lucide-react'
import { Link, useParams } from 'react-router'
import { Page } from '../../app/Page'
import type { RecipeLine, RecipeVersion } from '../../offline/replica'
import { Button, List, ListItem } from '../../ui'
import { unitLabel } from '../inventory/status'
import { useRecipe, useRecipeVersion } from './hooks'
import { ChatSection } from './slots/ChatSection'
import { CookButton } from './slots/CookButton'
import { CooksSection } from './slots/CooksSection'
import { MissingToListButton } from './slots/MissingToListButton'
import { RecipeMeta } from './slots/SavedTab'

/** "2–3 clove", "1.5 cup": the parsed amount, or nothing for raw/unquantified lines. */
export function amountOf(l: RecipeLine): string {
  if (!l.qty) return ''
  const q = l.qty_max ? `${l.qty}–${l.qty_max}` : l.qty
  return [q, unitLabel(l.unit ?? null)].filter(Boolean).join(' ')
}

/** Lines in order, split into their groups ("For the rice"). */
export function groupLines(lines: RecipeLine[]): { group: string; lines: RecipeLine[] }[] {
  const out: { group: string; lines: RecipeLine[] }[] = []
  for (const l of lines) {
    const g = l.group ?? ''
    const last = out.at(-1)
    if (last && last.group === g) last.lines.push(l)
    else out.push({ group: g, lines: [l] })
  }
  return out
}

/**
 * Read-only recipe page over the replica (works offline in the kitchen). m2-3 replaces it with the
 * full page (photos, versions, editor, language toggle) and keeps composing the slots.
 */
export function RecipePage() {
  const { id } = useParams()
  const recipe = useRecipe(id)
  const version = useRecipeVersion(recipe?.currentVersionId)
  const back = (
    <Button asChild variant="secondary" size="icon" aria-label="Back to recipes">
      <Link to="/recipes?tab=saved">
        <ArrowLeft aria-hidden />
      </Link>
    </Button>
  )
  if (recipe === undefined) return <Page title="Recipe" actions={back} />
  if (recipe === null)
    return (
      <Page title="Recipe" actions={back}>
        <p className="t-body-sm m-0 text-ink-muted">This recipe isn't here any more.</p>
      </Page>
    )
  return (
    <Page title={recipe.title} actions={back}>
      {recipe.subtitle ? (
        <p className="t-body m-0 -mt-3 text-ink-muted">{recipe.subtitle}</p>
      ) : null}
      <div className="t-body-sm text-ink-muted">
        <RecipeMeta servings={recipe.servings} minutes={recipe.totalMinutes} />
      </div>
      {version ? (
        <>
          <div className="flex flex-wrap gap-2">
            <CookButton recipeId={recipe.id} versionId={version.id} />
            <MissingToListButton recipeId={recipe.id} versionId={version.id} />
          </div>
          <VersionBody version={version} />
          <CooksSection recipeId={recipe.id} versionId={version.id} />
          <ChatSection recipeId={recipe.id} versionId={version.id} />
        </>
      ) : null}
    </Page>
  )
}

export function VersionBody({ version }: { version: RecipeVersion }) {
  return (
    <>
      <section aria-label="Ingredients" className="flex flex-col gap-2">
        <h2 className="t-label m-0 text-ink-muted">Ingredients</h2>
        {version.ingredients.length === 0 ? (
          <p className="t-body-sm m-0 text-ink-muted">No ingredients listed.</p>
        ) : (
          groupLines(version.ingredients).map((g) => (
            <div key={`${g.group}-${g.lines[0]?.line_id}`} className="flex flex-col gap-1.5">
              {g.group ? <h3 className="t-body-sm m-0 font-semibold">{g.group}</h3> : null}
              <List>
                {g.lines.map((l) => (
                  <ListItem
                    key={l.line_id}
                    title={
                      <span>
                        {amountOf(l) ? <b className="tabular mr-1.5">{amountOf(l)}</b> : null}
                        {l.name}
                      </span>
                    }
                    subtitle={[
                      l.note,
                      l.optional && !l.note?.includes('to taste') ? 'optional' : '',
                    ]
                      .filter(Boolean)
                      .join(' · ')}
                  />
                ))}
              </List>
            </div>
          ))
        )}
      </section>
      <section aria-label="Method" className="flex flex-col gap-2">
        <h2 className="t-label m-0 text-ink-muted">Method</h2>
        {version.steps.length === 0 ? (
          <p className="t-body-sm m-0 text-ink-muted">No steps yet.</p>
        ) : (
          <ol className="m-0 flex list-none flex-col gap-3 rounded-lg bg-surface p-4 shadow-card">
            {version.steps.map((s, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: a version's steps are immutable
              <li key={i} className="flex gap-3">
                <span className="tabular grid size-6 shrink-0 place-items-center rounded-full bg-brand-soft text-[13px] font-bold text-brand">
                  {i + 1}
                </span>
                <p className="t-body m-0">
                  {s.text}
                  {s.minutes ? <span className="text-ink-muted"> · {s.minutes} min</span> : null}
                </p>
              </li>
            ))}
          </ol>
        )}
      </section>
      {version.notes ? (
        <section aria-label="Notes" className="flex flex-col gap-2">
          <h2 className="t-label m-0 text-ink-muted">Notes</h2>
          <p className="t-body m-0 whitespace-pre-line rounded-lg bg-surface p-4 shadow-card">
            {version.notes}
          </p>
        </section>
      ) : null}
    </>
  )
}
