import {
  Camera,
  ChevronRight,
  Clapperboard,
  ClipboardPaste,
  GitCommitHorizontal,
  Languages,
  Link as LinkIcon,
  Sparkles,
} from 'lucide-react'
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import type {
  Recipe,
  RecipeLine,
  RecipeOriginal,
  RecipeStep,
  RecipeVersion,
} from '../../offline/replica'
import { Badge, cn } from '../../ui'
import { FoodIcon } from '../../ui/food-icons'
import { unitLabel } from '../inventory/status'
import { useRecipeVersions } from './hooks'
import { needsCheck } from './IngredientPicker'
import { isRtl, nativeName } from './lang'
import { useIngredientIcon } from './look'
import { FindSwapButton } from './slots/FindSwapButton'

/** "2–3 clove", "1.5 cup": the parsed amount, or nothing for raw/unquantified lines. */
export function amountOf(l: RecipeLine): string {
  if (!l.qty) return ''
  const q = l.qty_max ? `${kitchenQty(l.qty)}–${kitchenQty(l.qty_max)}` : kitchenQty(l.qty)
  return [q, unitLabel(l.unit ?? null)].filter(Boolean).join(' ')
}

const FRACTIONS: [number, string][] = [
  [0.125, '⅛'],
  [0.25, '¼'],
  [1 / 3, '⅓'],
  [0.5, '½'],
  [2 / 3, '⅔'],
  [0.75, '¾'],
]

/** Decimal string as cooks write it: "0.5" → "½", "1.5" → "1½", "1.2" stays "1.2". */
export function kitchenQty(qty: string): string {
  const n = Number(qty)
  if (!Number.isFinite(n) || n <= 0) return qty
  const whole = Math.floor(n)
  const frac = n - whole
  if (frac < 0.01) return String(whole)
  const f = FRACTIONS.find(([v]) => Math.abs(v - frac) < 0.01)
  if (!f) return String(Math.round(n * 100) / 100)
  return whole ? `${whole}${f[1]}` : f[1]
}

/** Lines in order, split into their groups ("For the rice"). */
export function groupLines<T extends { group?: string }>(
  lines: T[],
): { group: string; lines: T[] }[] {
  const out: { group: string; lines: T[] }[] = []
  for (const l of lines) {
    const g = l.group ?? ''
    const last = out.at(-1)
    if (last && last.group === g) last.lines.push(l)
    else out.push({ group: g, lines: [l] })
  }
  return out
}

/** Raw lines as the editor shows them: section headers back in front of their groups. */
export function linesToText(lines: RecipeLine[]): string {
  return groupLines(lines)
    .flatMap((g) => [...(g.group ? [`${g.group}:`] : []), ...g.lines.map((l) => l.raw)])
    .join('\n')
}

export function Section({
  title,
  action,
  children,
}: {
  title: string
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <div className="flex items-baseline justify-between gap-2 px-0.5">
        <h2 className="m-0 text-[17px] font-semibold">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  )
}

type BodyProps = {
  recipeId: string
  version: RecipeVersion
  /** Tap on a line opens the match picker; omitted on read-only views. */
  onLine?: (line: RecipeLine) => void
}

/** Ingredients, method and notes of one version (parsed English text). */
export function VersionBody({ recipeId, version, onLine }: BodyProps) {
  const iconOf = useIngredientIcon()
  const parsed = version.rev > 0
  return (
    <>
      <Section title="Ingredients">
        {version.ingredients.length === 0 ? (
          <p className="t-body-sm m-0 text-ink-muted">No ingredients listed.</p>
        ) : (
          <>
            {!parsed ? (
              <p className="t-body-sm m-0 text-ink-muted">
                Amounts and matches show once this version syncs.
              </p>
            ) : null}
            {groupLines(version.ingredients).map((g) => (
              <div key={`${g.group}-${g.lines[0]?.line_id}`} className="flex flex-col gap-1.5">
                {g.group ? <h3 className="t-body-sm m-0 px-0.5 font-semibold">{g.group}</h3> : null}
                <ul className="m-0 list-none rounded-lg bg-surface p-0 py-1 shadow-card [&>li+li]:border-t [&>li+li]:border-border">
                  {g.lines.map((l) => (
                    <IngredientRow
                      key={l.line_id}
                      line={l}
                      icon={parsed ? iconOf(l.ingredient_id) : null}
                      check={onLine && needsCheck(version.rev, l) ? () => onLine(l) : undefined}
                      onTap={onLine && parsed ? () => onLine(l) : undefined}
                      swap={
                        // Read-only views (old versions) don't offer swaps.
                        parsed && onLine ? (
                          <FindSwapButton
                            recipeId={recipeId}
                            versionId={version.id}
                            lineId={l.line_id}
                          />
                        ) : null
                      }
                    />
                  ))}
                </ul>
              </div>
            ))}
          </>
        )}
      </Section>
      <StepsSection steps={version.steps} />
      {version.notes ? <NotesSection notes={version.notes} /> : null}
    </>
  )
}

function IngredientRow({
  line: l,
  icon,
  check,
  onTap,
  swap,
}: {
  line: RecipeLine
  icon: string | null
  check?: () => void
  onTap?: () => void
  swap: ReactNode
}) {
  const amount = amountOf(l)
  const sub = [l.note, l.optional && !/to taste/i.test(l.raw) ? 'optional' : '']
    .filter(Boolean)
    .join(' · ')
  const text = (
    <span className="flex min-w-0 flex-1 flex-col">
      <span>
        {amount ? <b className="tabular mr-1.5">{amount}</b> : null}
        {l.name || l.raw}
      </span>
      {sub ? <span className="text-[13px] leading-5 text-ink-muted">{sub}</span> : null}
    </span>
  )
  const lead = (
    <span className="grid size-[30px] shrink-0 place-items-center rounded-[8px] bg-surface-2 text-ink">
      {icon ? <FoodIcon k={icon} size={20} /> : null}
    </span>
  )
  return (
    <li className="flex items-center gap-2 pr-3">
      {onTap ? (
        <button
          type="button"
          onClick={onTap}
          aria-label={`${l.raw}: change ingredient match`}
          className="flex min-w-0 flex-1 items-center gap-3 border-0 bg-transparent py-2.5 pl-3 text-left text-ink hover:bg-surface-2"
        >
          {lead}
          {text}
        </button>
      ) : (
        <div className="flex min-w-0 flex-1 items-center gap-3 py-2.5 pl-3">
          {lead}
          {text}
        </div>
      )}
      {check ? (
        <button
          type="button"
          onClick={check}
          className="shrink-0 border-0 bg-transparent p-0"
          aria-label={`Check match for ${l.raw}`}
        >
          <Badge tone="soon">{l.ingredient_id ? 'Check match' : 'Match'}</Badge>
        </button>
      ) : null}
      {swap}
    </li>
  )
}

export function StepsSection({ steps, dir }: { steps: RecipeStep[]; dir?: 'rtl' }) {
  return (
    <Section title="Method">
      {steps.length === 0 ? (
        <p className="t-body-sm m-0 text-ink-muted">No steps yet.</p>
      ) : (
        <ol
          dir={dir}
          className="m-0 flex list-none flex-col gap-3 rounded-lg bg-surface p-4 shadow-card"
        >
          {steps.map((s, i) => (
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
    </Section>
  )
}

function NotesSection({ notes }: { notes: string }) {
  return (
    <Section title="Notes">
      <p className="t-body m-0 whitespace-pre-line rounded-lg bg-surface p-4 shadow-card">
        {notes}
      </p>
    </Section>
  )
}

/** The untranslated recipe as imported: raw lines and steps, in its own script direction. */
export function OriginalBody({ original }: { original: RecipeOriginal }) {
  const rtl = isRtl(original.lang)
  const dir = rtl ? 'rtl' : undefined
  return (
    <div lang={original.lang} className="flex flex-col gap-4">
      <Section title="Ingredients">
        <ul
          dir={dir}
          className="m-0 list-none rounded-lg bg-surface p-0 py-1 shadow-card [&>li+li]:border-t [&>li+li]:border-border"
        >
          {original.ingredients.map((raw, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: the original text never changes
            <li key={i} className="px-4 py-2.5">
              {raw}
            </li>
          ))}
        </ul>
      </Section>
      <StepsSection steps={original.steps.map((text) => ({ text }))} dir={dir} />
      {original.notes ? (
        <Section title="Notes">
          <p
            dir={dir}
            className="t-body m-0 whitespace-pre-line rounded-lg bg-surface p-4 shadow-card"
          >
            {original.notes}
          </p>
        </Section>
      ) : null}
    </div>
  )
}

/** "English · فارسی": switches the view between the translation and the original. */
export function LanguageToggle({
  original,
  showOriginal,
  onToggle,
}: {
  original: RecipeOriginal
  showOriginal: boolean
  onToggle: (showOriginal: boolean) => void
}) {
  const part = (active: boolean, children: ReactNode, lang?: string) => (
    <span
      lang={lang}
      dir={lang && isRtl(lang) ? 'rtl' : undefined}
      className={cn(active ? 'underline underline-offset-2' : 'opacity-70')}
    >
      {children}
    </span>
  )
  return (
    <button
      type="button"
      aria-pressed={showOriginal}
      aria-label={showOriginal ? 'Show the English translation' : 'Show the original'}
      onClick={() => onToggle(!showOriginal)}
      className="border-0 bg-transparent p-0"
    >
      <Badge tone="brand">
        <Languages aria-hidden />
        {part(!showOriginal, 'English')} ·{' '}
        {part(showOriginal, nativeName(original.lang), original.lang)}
      </Badge>
    </button>
  )
}

/** Where the recipe came from, as a chip (nothing for ones written here). */
export function SourceChip({ recipe }: { recipe: Recipe }) {
  const host = (() => {
    try {
      return recipe.sourceUrl ? new URL(recipe.sourceUrl).hostname.replace(/^www\./, '') : null
    } catch {
      return null
    }
  })()
  const chip = (icon: ReactNode, text: string) => (
    <Badge tone="neutral">
      {icon}
      {text}
    </Badge>
  )
  switch (recipe.sourceKind) {
    case 'url':
      return host && recipe.sourceUrl ? (
        <a
          href={recipe.sourceUrl}
          target="_blank"
          rel="noreferrer noopener"
          className="no-underline"
        >
          {chip(<LinkIcon aria-hidden />, host)}
        </a>
      ) : (
        chip(<LinkIcon aria-hidden />, 'From a web page')
      )
    case 'instagram':
      return recipe.sourceUrl ? (
        <a
          href={recipe.sourceUrl}
          target="_blank"
          rel="noreferrer noopener"
          className="no-underline"
        >
          {chip(<Clapperboard aria-hidden />, 'From a reel')}
        </a>
      ) : (
        chip(<Clapperboard aria-hidden />, 'From a reel')
      )
    case 'photo':
      return chip(<Camera aria-hidden />, 'From a photo')
    case 'text':
      return chip(<ClipboardPaste aria-hidden />, 'Pasted text')
    case 'idea':
      return chip(<Sparkles aria-hidden />, 'Idea from Stockpot AI')
    default:
      return null
  }
}

const shortDate = (iso: string, now = new Date()) => {
  const d = new Date(iso)
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    ...(d.getFullYear() === now.getFullYear() ? {} : { year: 'numeric' }),
  }).format(d)
}

const originText: Record<RecipeVersion['origin'], string> = {
  import: 'imported',
  manual: 'edited',
  chat: 'from your chat',
  restore: 'restored',
  idea: 'Stockpot AI idea',
}

/** What a version changed, for timelines: its summary or a sensible default. */
export function versionSummary(v: RecipeVersion): string {
  if (v.changeSummary) return v.changeSummary
  if (v.n === 1) return v.origin === 'import' ? 'Imported' : 'First version'
  return v.origin === 'chat' ? 'Changed in chat' : 'Edited'
}

export function versionWhen(v: RecipeVersion): string {
  const origin = v.n === 1 && v.origin === 'manual' ? 'written' : originText[v.origin]
  return `${shortDate(v.createdAt)} · ${origin}`
}

/** "Notes and versions": every version, newest first; tap one to view, compare or restore it. */
export function VersionTimeline({ recipe }: { recipe: Recipe }) {
  const versions = useRecipeVersions(recipe.id)
  if (versions.length === 0) return null
  return (
    <Section title="Notes and versions">
      <ol className="m-0 list-none rounded-lg bg-surface p-0 py-1 shadow-card [&>li+li]:border-t [&>li+li]:border-border">
        {versions.map((v) => (
          <li key={v.id}>
            <Link
              to={`/recipes/${recipe.id}/v/${v.n}`}
              className="flex items-start gap-2.5 px-4 py-3 text-ink no-underline hover:bg-surface-2"
            >
              <GitCommitHorizontal
                className="mt-0.5 size-[18px] shrink-0 text-ink-muted"
                aria-hidden
              />
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="t-body-sm">
                  <b>v{v.n}</b> {versionSummary(v)}
                </span>
                <span className="text-[12.5px] text-ink-muted">
                  {versionWhen(v)}
                  {v.id === recipe.currentVersionId ? ' · current' : ''}
                </span>
              </span>
              <ChevronRight className="mt-0.5 size-[18px] shrink-0 text-ink-muted" aria-hidden />
            </Link>
          </li>
        ))}
      </ol>
    </Section>
  )
}
