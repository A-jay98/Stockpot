import { ArrowLeft, Minus, Plus, RotateCcw } from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { v7 } from 'uuid'
import { Page } from '../../app/Page'
import { dispatch } from '../../offline/outbox'
import type { RecipeVersion } from '../../offline/replica'
import { Button, cn, Segmented, toast } from '../../ui'
import { type DiffRow, diffLines, hasChanges } from './diff'
import { useOriginal, useRecipe, useRecipeVersion, useVersionByN } from './hooks'
import {
  LanguageToggle,
  linesToText,
  OriginalBody,
  Section,
  VersionBody,
  versionSummary,
  versionWhen,
} from './parts'
import { formatMinutes } from './slots/SavedTab'

type View = 'version' | 'changes'

/** Everything a version diff compares, as text lines. */
export function versionDiff(from: RecipeVersion, to: RecipeVersion) {
  const facts = (v: RecipeVersion) =>
    [
      `Title: ${v.title}`,
      v.servings ? `Portions: ${v.servings}` : '',
      v.totalMinutes ? `Time: ${formatMinutes(v.totalMinutes)}` : '',
    ].filter(Boolean)
  const notes = (v: RecipeVersion) => (v.notes ?? '').split('\n').filter((l) => l.trim() !== '')
  return {
    details: diffLines(facts(from), facts(to)),
    ingredients: diffLines(
      linesToText(from.ingredients).split('\n').filter(Boolean),
      linesToText(to.ingredients).split('\n').filter(Boolean),
    ),
    steps: diffLines(
      from.steps.map((s) => s.text),
      to.steps.map((s) => s.text),
    ),
    notes: diffLines(notes(from), notes(to)),
  }
}

/** One old (or the current) version, read-only, with a diff against current and Restore. */
export function VersionPage() {
  const { id, n } = useParams()
  const navigate = useNavigate()
  const recipe = useRecipe(id)
  const version = useVersionByN(id, Number(n))
  const current = useRecipeVersion(recipe?.currentVersionId)
  const original = useOriginal(version)
  const [view, setView] = useState<View>('version')
  const [showOriginal, setShowOriginal] = useState(false)

  const back = (
    <Button asChild variant="secondary" size="icon" aria-label="Back to recipe">
      <Link to={`/recipes/${id}`}>
        <ArrowLeft aria-hidden />
      </Link>
    </Button>
  )
  if (recipe === undefined || version === undefined) return <Page title="Version" actions={back} />
  if (recipe === null || version === null)
    return (
      <Page title="Version" actions={back}>
        <p className="t-body-sm m-0 text-ink-muted">This version isn't here any more.</p>
      </Page>
    )

  const isCurrent = version.id === recipe.currentVersionId
  const restore = async () => {
    const versionId = v7()
    await dispatch('recipe.version.restore', {
      recipeId: recipe.id,
      versionId,
      fromVersionId: version.id,
    })
    toast({ title: `Restored v${version.n} as a new version`, tone: 'success' })
    navigate(`/recipes/${recipe.id}`, { replace: true })
  }

  return (
    <Page title={`Version ${version.n}`} actions={back}>
      <div className="-mt-2 flex flex-col gap-1">
        <p className="t-body-strong m-0">{version.title}</p>
        <p className="t-body-sm m-0">{versionSummary(version)}</p>
        <p className="t-body-sm m-0 text-ink-muted">
          {versionWhen(version)}
          {isCurrent ? ' · current version' : ''}
        </p>
      </div>

      {isCurrent ? null : (
        <>
          <Button onClick={restore}>
            <RotateCcw aria-hidden />
            Restore this version
          </Button>
          <p className="t-body-sm -mt-2 m-0 text-ink-muted">
            Restoring adds it back as a new version, so nothing in the history is lost.
          </p>
          <Segmented
            value={view}
            onValueChange={setView}
            aria-label="Show"
            options={[
              { value: 'version', label: 'This version' },
              { value: 'changes', label: 'Changes vs current' },
            ]}
          />
        </>
      )}

      {view === 'changes' && current && !isCurrent ? (
        <Changes from={version} to={current} />
      ) : (
        <>
          {original ? (
            <div className="flex">
              <LanguageToggle
                original={original}
                showOriginal={showOriginal}
                onToggle={setShowOriginal}
              />
            </div>
          ) : null}
          {showOriginal && original ? (
            <OriginalBody original={original} />
          ) : (
            <VersionBody recipeId={recipe.id} version={version} />
          )}
        </>
      )}
    </Page>
  )
}

/** What changed from this version to the current one. */
function Changes({ from, to }: { from: RecipeVersion; to: RecipeVersion }) {
  const d = versionDiff(from, to)
  const sections: [string, DiffRow[]][] = [
    ['Details', d.details],
    ['Ingredients', d.ingredients],
    ['Method', d.steps],
    ['Notes', d.notes],
  ]
  const changed = sections.filter(([, rows]) => hasChanges(rows))
  return (
    <>
      <p className="t-body-sm m-0 text-ink-muted">
        From v{from.n} to the current v{to.n}: <Plus className="inline size-3.5" aria-hidden /> was
        added since, <Minus className="inline size-3.5" aria-hidden /> was removed.
      </p>
      {changed.length === 0 ? (
        <p className="t-body-sm m-0 rounded-lg bg-surface p-4 shadow-card">
          Same content as the current version.
        </p>
      ) : (
        changed.map(([title, rows]) => (
          <Section key={title} title={title}>
            <DiffList rows={rows} />
          </Section>
        ))
      )}
    </>
  )
}

export function DiffList({ rows }: { rows: DiffRow[] }) {
  return (
    <ul className="m-0 list-none overflow-hidden rounded-lg bg-surface p-0 py-1 shadow-card">
      {rows.map((r, i) => (
        <li
          // biome-ignore lint/suspicious/noArrayIndexKey: rows are derived and never reordered
          key={i}
          data-kind={r.kind}
          className={cn(
            'flex items-start gap-2 px-4 py-2 text-[14px]',
            r.kind === 'added' && 'bg-fresh-bg text-fresh',
            r.kind === 'removed' && 'bg-expired-bg text-expired line-through',
            r.kind === 'same' && 'text-ink-muted',
          )}
        >
          <span aria-hidden className="mt-0.5 shrink-0">
            {r.kind === 'added' ? (
              <Plus className="size-4" />
            ) : r.kind === 'removed' ? (
              <Minus className="size-4" />
            ) : (
              <span className="inline-block size-4" />
            )}
          </span>
          <span>
            <span className="sr-only">
              {r.kind === 'added' ? 'Added: ' : r.kind === 'removed' ? 'Removed: ' : ''}
            </span>
            {r.text}
          </span>
        </li>
      ))}
    </ul>
  )
}
