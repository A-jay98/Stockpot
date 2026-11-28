import { ArrowLeft, Plus, Trash2, X } from 'lucide-react'
import { type FormEvent, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { v7 } from 'uuid'
import { Page } from '../../app/Page'
import type { CommandPayloads } from '../../offline/commands'
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
import { PhotoPicker } from '../photos/PhotoPicker'
import { rawLines } from './commands'
import { useRecipe, useRecipeVersion, useSettledVersion } from './hooks'
import { RecipeCover, useIngredientIcon, useRecipeIcon } from './look'
import { amountOf, groupLines, linesToText, Section } from './parts'

const MAX_LINES = 200
const MAX_STEPS = 100
// Mirrors internal/recipes/version.go (counted in characters).
const MAX_LINE_LEN = 500
const MAX_STEP_LEN = 4000

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
  const lines = ingredientList(d.ingredients)
  if (lines.length > MAX_LINES) e.ingredients = `At most ${MAX_LINES} lines.`
  else if (lines.some((l) => [...l].length > MAX_LINE_LEN))
    e.ingredients = `Each line can be at most ${MAX_LINE_LEN} characters.`
  const steps = stepList(d.steps)
  if (steps.length > MAX_STEPS) e.steps = `At most ${MAX_STEPS} steps.`
  else if (steps.some((x) => [...x.text].length > MAX_STEP_LEN))
    e.steps = `Each step can be at most ${MAX_STEP_LEN} characters.`
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
  const version = useSettledVersion(recipe?.currentVersionId)
  // The version this edit started from, fixed at the first settled read: a newer version syncing
  // in later must neither remount the form nor become the base (see EditorForm).
  const [openedFor, setOpened] = useState<{ id: string; v: RecipeVersion | null }>()
  if (id && recipe && openedFor?.id !== id && version !== undefined) setOpened({ id, v: version })
  const opened = openedFor && openedFor.id === id ? openedFor.v : undefined
  if (isNew) return <EditorForm recipe={null} version={null} />
  const back = (
    <Button asChild variant="secondary" size="icon" aria-label="Back to recipe">
      <Link to={`/recipes/${id}`}>
        <ArrowLeft aria-hidden />
      </Link>
    </Button>
  )
  // A current version missing from the replica (null) still opens the editor, from the recipe.
  if (recipe === undefined || opened === undefined)
    return <Page title="Edit recipe" actions={back} />
  if (recipe === null)
    return (
      <Page title="Edit recipe" actions={back}>
        <p className="t-body-sm m-0 text-ink-muted">This recipe isn't here any more.</p>
      </Page>
    )
  return <EditorForm key={recipe.id} recipe={recipe} version={opened} />
}

function EditorForm({ recipe, version }: { recipe: Recipe | null; version: RecipeVersion | null }) {
  const navigate = useNavigate()
  // `version` is the one this edit started from. It stays the compare base and the parent even if
  // a newer version syncs in meanwhile, so saving never silently reverts someone else's change.
  // Read it live only to pick up the server's parse of the same version.
  const liveBase = useRecipeVersion(version?.id)
  const base = liveBase && liveBase.id === version?.id ? liveBase : version
  const movedOn = recipe !== null && base !== null && recipe.currentVersionId !== base.id
  const [draft, setDraft] = useState<Draft>(() => (recipe ? draftOf(recipe, version) : emptyDraft))
  const [cover, setCover] = useState<string | null>(recipe?.coverBlob ?? null)
  const [tried, setTried] = useState(false)
  const errors = validate(draft)
  const icon = useRecipeIcon(base)
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
      if (cover) await dispatch('recipe.update', { id: rid, coverBlob: cover })
      navigate(`/recipes/${rid}`, { replace: true })
      return
    }
    if (contentChanged(draft, base)) {
      await dispatch('recipe.version.create', {
        recipeId: recipe.id,
        versionId: v7(),
        origin: 'manual',
        parentVersionId: base?.id ?? null,
        changeSummary: draft.changeSummary.trim() || null,
        ...content,
      })
    }
    const changes: CommandPayloads['recipe.update'] = { id: recipe.id }
    if (cover !== recipe.coverBlob) changes.coverBlob = cover
    if (subtitle !== recipe.subtitle) changes.subtitle = subtitle
    if (tags.join(',') !== recipe.tags.join(',')) changes.tags = tags
    // LWW per field: send only what changed so concurrent edits to other fields survive.
    if (Object.keys(changes).length > 1) await dispatch('recipe.update', changes)
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
      {/* Outside the form: the picker's buttons must not submit it. */}
      <section aria-label="Cover photo" className="flex items-center gap-3">
        <RecipeCover
          recipeId={recipe?.id ?? 'new'}
          coverBlob={cover}
          w={256}
          alt="Cover photo"
          icon={icon}
          size={28}
          className="size-20 rounded-lg"
        />
        <div className="flex min-w-0 flex-1 flex-col gap-2">
          <PhotoPicker
            multiple={false}
            className="flex-wrap"
            onPicked={(refs) => setCover(refs[0] ?? null)}
          />
          {cover ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="self-start"
              onClick={() => setCover(null)}
            >
              <Trash2 aria-hidden />
              Remove cover
            </Button>
          ) : null}
        </div>
      </section>
      <form onSubmit={save} className="flex flex-col gap-4" noValidate>
        {movedOn ? (
          <p role="status" className="t-body-sm m-0 rounded-lg bg-soon-bg p-3 text-soon">
            This recipe changed on another device since you opened it. Saving adds your text as a
            new version; the other change stays in the history.
          </p>
        ) : null}
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
          <LinesPreview text={draft.ingredients} version={base} />
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
            hint={`Saved as a new version based on v${base?.n ?? 0}. Optional.`}
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
