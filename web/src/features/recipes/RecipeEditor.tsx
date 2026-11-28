import { ArrowLeft, ImagePlus, Plus, Trash2, X } from 'lucide-react'
import { type FormEvent, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { v7 } from 'uuid'
import { Page } from '../../app/Page'
import { dispatch } from '../../offline/outbox'
import type { Recipe, RecipeLine, RecipeStep, RecipeVersion } from '../../offline/replica'
import {
  Badge,
  Button,
  Dialog,
  DialogClose,
  DialogContent,
  DialogTrigger,
  Field,
  Input,
  Textarea,
} from '../../ui'
import { FoodIcon } from '../../ui/food-icons'
import { rawLines } from './commands'
import { useRecipe, useRecipeVersion } from './hooks'
import { RecipeTile, useIngredientIcon, useRecipeIcon } from './look'
import { amountOf, groupLines, linesToText, Section } from './parts'

const MAX_LINES = 200
const MAX_STEPS = 100

export type Draft = {
  title: string
  subtitle: string
  servings: string
  minutes: string
  tags: string
  ingredients: string
  steps: RecipeStep[]
  notes: string
  changeSummary: string
}

const emptyDraft: Draft = {
  title: '',
  subtitle: '',
  servings: '',
  minutes: '',
  tags: '',
  ingredients: '',
  steps: [{ text: '' }],
  notes: '',
  changeSummary: '',
}

export function draftOf(recipe: Recipe, version: RecipeVersion | null): Draft {
  return {
    title: version?.title ?? recipe.title,
    subtitle: recipe.subtitle ?? '',
    servings: version?.servings ? String(version.servings) : '',
    minutes: version?.totalMinutes ? String(version.totalMinutes) : '',
    tags: recipe.tags.join(', '),
    ingredients: version ? linesToText(version.ingredients) : '',
    steps: version?.steps.length ? version.steps.map((s) => ({ ...s })) : [{ text: '' }],
    notes: version?.notes ?? '',
    changeSummary: '',
  }
}

const splitTags = (s: string) => [
  ...new Set(
    s
      .split(',')
      .map((t) => t.trim().toLowerCase())
      .filter(Boolean),
  ),
]
const ingredientList = (s: string) =>
  s
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
const stepList = (steps: RecipeStep[]) =>
  steps
    .map((s) => ({ text: s.text.trim(), ...(s.minutes ? { minutes: s.minutes } : {}) }))
    .filter((s) => s.text !== '')
const intOrNull = (s: string) => (s.trim() === '' ? null : Number(s))

/** Validation messages by field; empty when the draft can be saved. */
export function validate(
  d: Draft,
): Partial<Record<'title' | 'servings' | 'minutes' | 'ingredients' | 'steps', string>> {
  const e: ReturnType<typeof validate> = {}
  if (!d.title.trim()) e.title = 'Give the recipe a name.'
  const s = intOrNull(d.servings)
  if (s !== null && !(Number.isInteger(s) && s >= 1 && s <= 100))
    e.servings = 'A whole number, 1 to 100.'
  const m = intOrNull(d.minutes)
  if (m !== null && !(Number.isInteger(m) && m >= 0 && m <= 10000))
    e.minutes = 'Minutes, 0 to 10000.'
  if (ingredientList(d.ingredients).length > MAX_LINES)
    e.ingredients = `At most ${MAX_LINES} lines.`
  if (stepList(d.steps).length > MAX_STEPS) e.steps = `At most ${MAX_STEPS} steps.`
  return e
}

/** True when the version content differs from what the draft would save. */
export function contentChanged(d: Draft, v: RecipeVersion | null): boolean {
  if (!v) return true
  const same =
    d.title.trim() === v.title &&
    intOrNull(d.servings) === v.servings &&
    intOrNull(d.minutes) === v.totalMinutes &&
    ingredientList(d.ingredients).join('\n') ===
      ingredientList(linesToText(v.ingredients)).join('\n') &&
    JSON.stringify(stepList(d.steps)) === JSON.stringify(stepList(v.steps)) &&
    (d.notes.trim() || null) === (v.notes?.trim() || null)
  return !same
}

/**
 * Write or edit a recipe. Each save of changed content is a new version (`recipe.version.create`);
 * tags and subtitle are recipe fields (`recipe.update`). Works offline: the server parses the
 * ingredient lines when the commands sync.
 */
export function RecipeEditor() {
  const { id } = useParams()
  const isNew = id === undefined
  const recipe = useRecipe(id)
  const version = useRecipeVersion(recipe?.currentVersionId)
  if (isNew) return <EditorForm recipe={null} version={null} />
  const back = (
    <Button asChild variant="secondary" size="icon" aria-label="Back to recipe">
      <Link to={`/recipes/${id}`}>
        <ArrowLeft aria-hidden />
      </Link>
    </Button>
  )
  // Wait for the current version itself: a live query briefly keeps its previous result.
  if (recipe === undefined || (recipe?.currentVersionId && version?.id !== recipe.currentVersionId))
    return <Page title="Edit recipe" actions={back} />
  if (recipe === null)
    return (
      <Page title="Edit recipe" actions={back}>
        <p className="t-body-sm m-0 text-ink-muted">This recipe isn't here any more.</p>
      </Page>
    )
  return <EditorForm key={recipe.id} recipe={recipe} version={version ?? null} />
}

function EditorForm({ recipe, version }: { recipe: Recipe | null; version: RecipeVersion | null }) {
  const navigate = useNavigate()
  const [draft, setDraft] = useState<Draft>(() => (recipe ? draftOf(recipe, version) : emptyDraft))
  const [tried, setTried] = useState(false)
  const errors = validate(draft)
  const icon = useRecipeIcon(version)
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }))
  const backTo = recipe ? `/recipes/${recipe.id}` : '/recipes?tab=saved'

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setTried(true)
    if (Object.keys(errors).length) return
    const content = {
      title: draft.title.trim(),
      servings: intOrNull(draft.servings),
      totalMinutes: intOrNull(draft.minutes),
      ingredients: ingredientList(draft.ingredients),
      steps: stepList(draft.steps),
      notes: draft.notes.trim() || null,
    }
    const tags = splitTags(draft.tags)
    const subtitle = draft.subtitle.trim() || null
    if (!recipe) {
      const rid = v7()
      await dispatch('recipe.create', {
        id: rid,
        versionId: v7(),
        sourceKind: 'manual',
        subtitle,
        tags,
        ...content,
      })
      navigate(`/recipes/${rid}`, { replace: true })
      return
    }
    if (contentChanged(draft, version)) {
      await dispatch('recipe.version.create', {
        recipeId: recipe.id,
        versionId: v7(),
        origin: 'manual',
        parentVersionId: version?.id ?? null,
        changeSummary: draft.changeSummary.trim() || null,
        ...content,
      })
    }
    if (subtitle !== recipe.subtitle || tags.join(',') !== recipe.tags.join(',')) {
      await dispatch('recipe.update', { id: recipe.id, subtitle, tags })
    }
    navigate(backTo, { replace: true })
  }

  const err = (k: keyof typeof errors) => (tried ? errors[k] : undefined)

  return (
    <Page
      title={recipe ? 'Edit recipe' : 'New recipe'}
      actions={
        <Button asChild variant="secondary" size="icon" aria-label="Cancel">
          <Link to={backTo}>
            <X aria-hidden />
          </Link>
        </Button>
      }
    >
      <form onSubmit={save} className="flex flex-col gap-4" noValidate>
        <div className="flex items-center gap-3">
          <RecipeTile
            recipeId={recipe?.id ?? 'new'}
            icon={icon}
            size={28}
            className="size-16 rounded-lg"
          />
          {/* TODO(m2-2): PhotoPicker → savePhoto → recipe.update { coverBlob }. */}
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <Button type="button" variant="secondary" size="sm" disabled className="self-start">
              <ImagePlus aria-hidden />
              Cover photo
            </Button>
            <span className="text-xs text-ink-muted">Photos arrive with the photo update.</span>
          </div>
        </div>

        <Field label="Name" error={err('title')}>
          {(p) => (
            <Input
              {...p}
              value={draft.title}
              onChange={(e) => set('title', e.target.value)}
              maxLength={200}
              autoFocus={!recipe}
              placeholder="Chicken curry with rice"
            />
          )}
        </Field>
        <Field label="Short description" hint="Optional, e.g. “Split pea and beef stew”.">
          {(p) => (
            <Input
              {...p}
              value={draft.subtitle}
              onChange={(e) => set('subtitle', e.target.value)}
              maxLength={300}
            />
          )}
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Portions" error={err('servings')}>
            {(p) => (
              <Input
                {...p}
                inputMode="numeric"
                value={draft.servings}
                onChange={(e) => set('servings', e.target.value.replace(/\D/g, ''))}
                placeholder="4"
              />
            )}
          </Field>
          <Field label="Total time (min)" error={err('minutes')}>
            {(p) => (
              <Input
                {...p}
                inputMode="numeric"
                value={draft.minutes}
                onChange={(e) => set('minutes', e.target.value.replace(/\D/g, ''))}
                placeholder="45"
              />
            )}
          </Field>
        </div>
        <Field label="Tags" hint="Comma separated, e.g. dinner, freezer friendly.">
          {(p) => <Input {...p} value={draft.tags} onChange={(e) => set('tags', e.target.value)} />}
        </Field>

        <Section title="Ingredients">
          <Field
            label="One per line"
            hint="Write them as you would on paper: “2 tbsp tomato paste”. A line ending in “:” starts a section."
            error={err('ingredients')}
          >
            {(p) => (
              <Textarea
                {...p}
                value={draft.ingredients}
                onChange={(e) => set('ingredients', e.target.value)}
                className="min-h-40 font-mono text-[14px]"
                placeholder={
                  '800 g boneless chicken thighs\n1 (400 mL) can coconut milk\n\nFor the rice:\n1½ cups basmati rice, rinsed'
                }
              />
            )}
          </Field>
          <LinesPreview text={draft.ingredients} version={version} />
        </Section>

        <Section title="Method">
          <ol className="m-0 flex list-none flex-col gap-2 p-0">
            {draft.steps.map((s, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: steps are edited in place by position
              <li key={i} className="flex items-start gap-2">
                <span className="tabular mt-2.5 grid size-6 shrink-0 place-items-center rounded-full bg-brand-soft text-[13px] font-bold text-brand">
                  {i + 1}
                </span>
                <Textarea
                  aria-label={`Step ${i + 1}`}
                  value={s.text}
                  onChange={(e) =>
                    set(
                      'steps',
                      draft.steps.map((x, j) => (j === i ? { ...x, text: e.target.value } : x)),
                    )
                  }
                  className="min-h-16 flex-1"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={`Remove step ${i + 1}`}
                  disabled={draft.steps.length === 1}
                  onClick={() =>
                    set(
                      'steps',
                      draft.steps.filter((_, j) => j !== i),
                    )
                  }
                >
                  <Trash2 aria-hidden />
                </Button>
              </li>
            ))}
          </ol>
          {err('steps') ? <span className="text-xs text-expired">{err('steps')}</span> : null}
          <Button
            type="button"
            variant="secondary"
            onClick={() => set('steps', [...draft.steps, { text: '' }])}
          >
            <Plus aria-hidden />
            Add step
          </Button>
        </Section>

        <Field label="Notes">
          {(p) => (
            <Textarea
              {...p}
              value={draft.notes}
              onChange={(e) => set('notes', e.target.value)}
              maxLength={10000}
              placeholder="Better the next day. Freezes well."
            />
          )}
        </Field>

        {recipe ? (
          <Field
            label="What changed?"
            hint={`Shown in the history as v${(version?.n ?? 0) + 1}. Optional.`}
          >
            {(p) => (
              <Input
                {...p}
                value={draft.changeSummary}
                onChange={(e) => set('changeSummary', e.target.value)}
                maxLength={500}
                placeholder="Less turmeric, simmer 30 min longer"
              />
            )}
          </Field>
        ) : null}

        <Button type="submit" size="lg">
          {recipe ? 'Save changes' : 'Save recipe'}
        </Button>
        {recipe ? <DeleteRecipe recipe={recipe} /> : null}
      </form>
    </Page>
  )
}

/**
 * What the server will make of the lines: lines unchanged from the synced version show their
 * parsed amount and match (the server carries them over); new lines are read when they sync.
 */
function LinesPreview({ text, version }: { text: string; version: RecipeVersion | null }) {
  const iconOf = useIngredientIcon()
  const lines = useMemo(() => rawLines(text.split('\n')), [text])
  const known = useMemo(() => {
    const m = new Map<string, RecipeLine>()
    if (version && version.rev > 0)
      for (const l of version.ingredients) m.set(`${l.group ?? ''}\u0000${l.raw}`, l)
    return m
  }, [version])
  if (lines.length === 0) return null
  return (
    <section className="flex flex-col gap-1.5" aria-label="Preview">
      <span className="text-[13px] font-semibold text-ink-muted">Preview</span>
      {groupLines(lines).map((g) => (
        <div key={`${g.group}-${g.lines[0]?.line_id}`} className="flex flex-col gap-1">
          {g.group ? <span className="t-body-sm px-0.5 font-semibold">{g.group}</span> : null}
          <ul className="m-0 list-none rounded-lg bg-surface p-0 py-1 shadow-card [&>li+li]:border-t [&>li+li]:border-border">
            {g.lines.map((l) => {
              const p = known.get(`${l.group ?? ''}\u0000${l.raw}`)
              const icon = p ? iconOf(p.ingredient_id) : null
              return (
                <li key={l.line_id} className="flex items-center gap-2.5 px-3 py-2 text-[14px]">
                  <span className="grid size-6 shrink-0 place-items-center">
                    {icon ? <FoodIcon k={icon} size={18} /> : null}
                  </span>
                  {p ? (
                    <span className="min-w-0 flex-1">
                      {amountOf(p) ? <b className="tabular mr-1.5">{amountOf(p)}</b> : null}
                      {p.name}
                      {p.note ? <span className="text-ink-muted"> · {p.note}</span> : null}
                    </span>
                  ) : (
                    <>
                      <span className="min-w-0 flex-1 text-ink-muted">{l.raw}</span>
                      <Badge tone="neutral">Read on save</Badge>
                    </>
                  )}
                </li>
              )
            })}
          </ul>
        </div>
      ))}
    </section>
  )
}

function DeleteRecipe({ recipe }: { recipe: Recipe }) {
  const navigate = useNavigate()
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button type="button" variant="danger">
          <Trash2 aria-hidden />
          Delete recipe
        </Button>
      </DialogTrigger>
      <DialogContent
        title={`Delete ${recipe.title}?`}
        description="It leaves your library along with its versions. Cooks already recorded keep their stock changes."
      >
        <div className="flex gap-2.5 *:flex-1">
          <DialogClose asChild>
            <Button variant="secondary">Keep it</Button>
          </DialogClose>
          <Button
            variant="danger"
            onClick={async () => {
              await dispatch('recipe.delete', { id: recipe.id })
              navigate('/recipes?tab=saved', { replace: true })
            }}
          >
            Delete
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
