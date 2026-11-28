import { ChefHat, Undo2 } from 'lucide-react'
import { dispatch } from '../../../offline/outbox'
import { Button } from '../../../ui'
import { UndoWindowMs } from '../../cook/commands'
import { type CookEntry, useCooks } from '../../cook/hooks'
import { BlobImage } from '../../photos/BlobImage'

const day = (d: string) =>
  new Date(`${d}T12:00:00`).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })

/** "Your cooks": a photo strip of every cook, then the cook history (newest first). */
export function CooksSection({ recipeId }: { recipeId: string; versionId: string }) {
  const { cooks, loose } = useCooks(recipeId)
  const photos = [
    ...cooks.flatMap((c) => c.photos.map((p) => ({ p, on: c.session.cookedOn }))),
    ...loose.map((p) => ({ p, on: p.takenOn })),
  ]
  if (cooks.length === 0 && photos.length === 0) return null
  return (
    <section aria-label="Your cooks" className="flex flex-col gap-2">
      <h2 className="t-label m-0 text-ink-muted">Your cooks</h2>
      {photos.length > 0 ? (
        <ul aria-label="Photos of your cooks" className="m-0 grid list-none grid-cols-4 gap-2 p-0">
          {photos.map(({ p, on }) => (
            <li key={p.id} className="relative">
              <BlobImage
                blobRef={p.blobRef}
                w={256}
                alt={p.caption ?? `Cooked ${on ? day(on) : ''}`}
                className="aspect-square w-full rounded-md object-cover"
              />
              {on ? (
                <span className="absolute bottom-1 left-1 rounded-sm bg-ink/70 px-1.5 text-[11px] font-semibold text-bg">
                  {day(on)}
                </span>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      {cooks.length > 0 ? (
        <ol className="m-0 flex list-none flex-col gap-3 rounded-lg bg-surface p-4 shadow-card">
          {cooks.map((c) => (
            <CookLine key={c.session.id} entry={c} />
          ))}
        </ol>
      ) : null}
    </section>
  )
}

function CookLine({ entry: { session: s } }: { entry: CookEntry }) {
  const stored = s.stored.reduce((n, x) => n + x.portions, 0)
  const used = (s.uses as { skipped?: boolean }[]).filter((u) => !u.skipped).length
  const skipped = s.uses.length - used
  const canUndo = Date.now() - Date.parse(s.createdAt) < UndoWindowMs
  const facts = [
    `${s.portionsMade} ${s.portionsMade === 1 ? 'portion' : 'portions'}`,
    stored > 0 ? `${stored} stored` : '',
    used > 0 ? `${used} ${used === 1 ? 'item' : 'items'} used` : '',
    skipped > 0 ? `${skipped} already gone` : '',
  ].filter(Boolean)
  return (
    <li className="flex gap-2.5">
      <ChefHat aria-hidden className="mt-0.5 size-[18px] shrink-0 text-ink-muted" />
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="t-body">
          <b>{day(s.cookedOn)}</b> · {facts.join(' · ')}
        </span>
        {s.note ? <span className="t-body-sm text-ink-muted">{s.note}</span> : null}
      </div>
      {canUndo ? (
        <Button
          variant="ghost"
          size="sm"
          aria-label={`Undo the cook on ${day(s.cookedOn)}`}
          onClick={() => void dispatch('recipe.cook.undo', { id: s.id })}
        >
          <Undo2 aria-hidden />
          Undo
        </Button>
      ) : null}
    </li>
  )
}
