import { ArrowLeft, Bookmark, Pencil } from 'lucide-react'
import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { Page } from '../../app/Page'
import { dispatch } from '../../offline/outbox'
import type { RecipeLine } from '../../offline/replica'
import { Badge, Button } from '../../ui'
import { useOriginal, useRecipe, useRecipeVersion } from './hooks'
import { IngredientPicker } from './IngredientPicker'
import { isRtl } from './lang'
import { RecipeCover, useRecipeIcon } from './look'
import { LanguageToggle, OriginalBody, SourceChip, VersionBody, VersionTimeline } from './parts'
import { ChatSection } from './slots/ChatSection'
import { CookButton } from './slots/CookButton'
import { CooksSection } from './slots/CooksSection'
import { MissingToListButton } from './slots/MissingToListButton'
import { formatMinutes } from './slots/SavedTab'

export { amountOf, groupLines } from './parts'

/**
 * One recipe, read entirely from the replica so it works offline in the kitchen: hero, meta,
 * ingredients (tap a line to fix its match), method, notes, cooks, versions and chat. Content
 * edits go through the editor and always make a new version.
 */
export function RecipePage() {
  const { id } = useParams()
  const recipe = useRecipe(id)
  const version = useRecipeVersion(recipe?.currentVersionId)
  const original = useOriginal(version)
  const icon = useRecipeIcon(version)
  const [showOriginal, setShowOriginal] = useState(false)
  const [picking, setPicking] = useState<RecipeLine | null>(null)

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

  const meta = [
    recipe.subtitle,
    recipe.totalMinutes ? formatMinutes(recipe.totalMinutes) : null,
    recipe.servings ? `${recipe.servings} ${recipe.servings === 1 ? 'portion' : 'portions'}` : null,
    version ? `version ${version.n}` : null,
  ].filter(Boolean)
  const viewingOriginal = showOriginal && original !== null

  return (
    <Page
      title={recipe.title}
      actions={
        <>
          {back}
          <Button asChild variant="secondary" size="icon" aria-label="Edit recipe">
            <Link to={`/recipes/${recipe.id}/edit`}>
              <Pencil aria-hidden />
            </Link>
          </Button>
        </>
      }
    >
      <RecipeCover
        recipeId={recipe.id}
        coverBlob={recipe.coverBlob}
        w={1024}
        alt={`${recipe.title} cover photo`}
        icon={icon}
        size={48}
        className="-mt-1 h-[150px] w-full rounded-lg"
      />
      <div className="flex flex-col gap-2">
        {viewingOriginal ? (
          <p
            lang={original.lang}
            dir={isRtl(original.lang) ? 'rtl' : undefined}
            className="t-title m-0 text-[20px]"
          >
            {original.title}
          </p>
        ) : null}
        {meta.length ? <p className="t-body-sm m-0 text-ink-muted">{meta.join(' · ')}</p> : null}
        <div className="flex flex-wrap gap-1.5">
          {original ? (
            <LanguageToggle
              original={original}
              showOriginal={viewingOriginal}
              onToggle={setShowOriginal}
            />
          ) : null}
          <SourceChip recipe={recipe} />
          {recipe.tags.map((t) => (
            <Badge key={t} tone="neutral">
              {t}
            </Badge>
          ))}
        </div>
      </div>

      {!recipe.saved ? (
        <div className="flex items-center gap-3 rounded-lg bg-brand-soft p-3">
          <p className="t-body-sm m-0 flex-1">
            An idea for what you have. Keep it in your library?
          </p>
          <Button
            size="sm"
            onClick={() => dispatch('recipe.update', { id: recipe.id, saved: true })}
          >
            <Bookmark aria-hidden />
            Save
          </Button>
        </div>
      ) : null}

      {version ? (
        <>
          {viewingOriginal ? (
            <OriginalBody original={original} />
          ) : (
            <VersionBody recipeId={recipe.id} version={version} onLine={setPicking} />
          )}
          <CooksSection recipeId={recipe.id} versionId={version.id} />
          <VersionTimeline recipe={recipe} />
          <ChatSection recipeId={recipe.id} versionId={version.id} />
          <div className="sticky bottom-[calc(76px+env(safe-area-inset-bottom))] z-20 -mx-4 flex gap-2.5 border-t border-border bg-bg px-4 py-3 empty:hidden md:bottom-0 md:-mx-8 md:px-8 *:flex-1 md:*:flex-none md:justify-end">
            <MissingToListButton recipeId={recipe.id} versionId={version.id} />
            <CookButton recipeId={recipe.id} versionId={version.id} />
          </div>
          <IngredientPicker
            versionId={version.id}
            line={picking}
            onOpenChange={(open) => !open && setPicking(null)}
          />
        </>
      ) : null}
    </Page>
  )
}
